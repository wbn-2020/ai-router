// Command gateway 是 AI API 网关的单进程入口。
//
// serve 负责加载配置、打开并迁移数据库、启动 HTTP 服务，并在
// SIGINT/SIGTERM 后优雅退出；migrate/backup/restore 等维护子命令
// 复用 internal/store。业务规则不在这里实现。
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/server"
	"ai-router/internal/store"
)

const version = "0.1.0-dev"

// maintenanceTimeout 限制 migrate/backup/restore 与启动迁移的总耗时，避免 CLI 卡死。
const maintenanceTimeout = 10 * time.Minute

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "version":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "gateway: version 不接受参数")
			return 2
		}
		fmt.Fprintf(stdout, "ai-router gateway %s (%s)\n", version, runtime.Version())
		return 0
	case "keygen":
		return runKeygen(args[1:], stdout, stderr)
	case "migrate":
		return runMigrate(args[1:], stdout, stderr)
	case "backup":
		return runBackup(args[1:], stdout, stderr)
	case "restore":
		return runRestore(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "gateway: 未知子命令 %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func runServe(args []string, _ io.Writer, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "gateway: serve 不接受参数")
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 启动配置无效：%v\n", err)
		return 1
	}

	st, err := store.Open(store.Paths{DataDir: cfg.DataDir, DBPath: cfg.DBPath})
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 打开数据库失败：%v\n", err)
		return 1
	}
	defer func() {
		_ = st.Close()
	}()

	// 启动时应用内嵌迁移。迁移失败会保留迁移前备份并停止启动，
	// 不会带着未知 schema 提供服务。
	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), maintenanceTimeout)
	_, err = st.Migrate(migrateCtx)
	cancelMigrate()
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 数据库迁移失败：%v\n", err)
		return 1
	}

	srv, err := server.New(server.Options{
		ListenAddr:      cfg.ListenAddr,
		Store:           st,
		MasterKeyLoaded: len(cfg.MasterKey) == 32,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 初始化 HTTP 服务失败：%v\n", err)
		return 1
	}
	if err := mountDataPlane(cfg, st, srv); err != nil {
		fmt.Fprintf(stderr, "gateway: 初始化数据面失败：%v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stderr, "gateway: 监听 %s\n", srv.Addr())
	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "gateway: HTTP 服务退出：%v\n", err)
		return 1
	}
	return 0
}

func runKeygen(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "gateway: keygen 不接受参数")
		return 2
	}

	master := make([]byte, 32)
	client := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		fmt.Fprintln(stderr, "gateway: 生成随机主密钥失败")
		return 1
	}
	if _, err := rand.Read(client); err != nil {
		fmt.Fprintln(stderr, "gateway: 生成随机客户端 Key 失败")
		return 1
	}

	fmt.Fprintf(stdout, "%s=%s\n", config.EnvMasterKey, base64.StdEncoding.EncodeToString(master))
	fmt.Fprintf(stdout, "%s=gw_%s\n", config.EnvClientKey, base64.RawURLEncoding.EncodeToString(client))
	return 0
}

// gatewayPaths 解析维护子命令需要的数据库路径。
//
// 维护命令不应要求 GATEWAY_MASTER_KEY/客户端 Key，因此这里只读取非敏感的
// 路径变量；默认值与 internal/config 保持一致。
func gatewayPaths() store.Paths {
	dataDir := os.Getenv(config.EnvDataDir)
	if strings.TrimSpace(dataDir) == "" {
		dataDir = config.DefaultDataDir
	}
	dbPath := os.Getenv(config.EnvDBPath)
	if strings.TrimSpace(dbPath) == "" {
		dbPath = filepath.Join(dataDir, "gateway.db")
	}
	return store.Paths{DataDir: dataDir, DBPath: dbPath}
}

func runMigrate(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "gateway: migrate 不接受参数")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), maintenanceTimeout)
	defer cancel()

	result, err := store.Migrate(ctx, gatewayPaths())
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 数据库迁移失败：%v\n", err)
		return 1
	}
	if result.BackupPath != "" {
		fmt.Fprintf(stdout, "gateway: 迁移前备份已写入 %s\n", result.BackupPath)
	}
	if len(result.Applied) == 0 {
		fmt.Fprintf(stdout, "gateway: 数据库 schema 已是 v%d，无需迁移\n", result.ToVersion)
		return 0
	}
	fmt.Fprintf(
		stdout,
		"gateway: 数据库 schema v%d -> v%d，已应用迁移 %v\n",
		result.FromVersion, result.ToVersion, result.Applied,
	)
	return 0
}

func runBackup(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "gateway: backup 最多接受一个目标路径")
		return 2
	}
	dest := ""
	if len(args) == 1 {
		dest = args[0]
	}

	ctx, cancel := context.WithTimeout(context.Background(), maintenanceTimeout)
	defer cancel()

	path, err := store.Backup(ctx, gatewayPaths(), dest)
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 数据库备份失败：%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "gateway: 数据库备份已写入 %s\n", path)
	return 0
}

func runRestore(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "gateway: restore 需要一个备份文件路径")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), maintenanceTimeout)
	defer cancel()

	result, err := store.Restore(ctx, gatewayPaths(), args[0])
	if err != nil {
		fmt.Fprintf(stderr, "gateway: 数据库恢复失败：%v\n", err)
		return 1
	}
	if result.PreRestoreBackup != "" {
		fmt.Fprintf(stdout, "gateway: 恢复前备份已写入 %s\n", result.PreRestoreBackup)
	}
	fmt.Fprintf(
		stdout,
		"gateway: 数据库已从 %s 恢复（schema v%d）\n",
		result.BackupPath, result.BackupVersion,
	)
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, strings.TrimSpace(`
用法: gateway <子命令>

子命令:
  serve               加载配置、迁移数据库并启动 HTTP 服务
  migrate             执行版本化数据库迁移（迁移前自动备份）
  backup [目标路径]   生成 SQLite 一致性备份；默认写入数据目录 backups/
  restore <备份路径>  校验并恢复数据库（覆盖前自动备份当前库）
  version             输出版本信息
  keygen              生成 GATEWAY_MASTER_KEY 与 GATEWAY_CLIENT_KEY
`))
}
