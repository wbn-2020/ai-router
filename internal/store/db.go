// Package store 负责 SQLite 连接、版本化迁移、备份/恢复与数据仓储。
//
// 数据库运行约束（见计划 T02）：
//   - WAL 日志模式；
//   - busy_timeout=5000ms；
//   - foreign_keys=ON；
//   - synchronous=NORMAL；
//   - 单写连接（database/sql 最大打开连接数为 1）。
//
// 所有连接参数通过 DSN 的 _pragma 设置，确保 database/sql 重建底层连接时
// 约束仍然生效，而不是只在首次连接上生效。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	sqliteDriverName = "sqlite"
	busyTimeoutMS    = 5000
)

// Paths 描述数据库与备份的落地位置。
//
// DataDir 对应 GATEWAY_DATA_DIR；备份固定写到 DataDir/backups，不依赖进程
// 当前工作目录。DBPath 对应 GATEWAY_DB_PATH，默认 DataDir/gateway.db。
type Paths struct {
	DataDir string
	DBPath  string
}

// Validate 检查路径配置是否可用；错误信息不回显秘密值（这里也没有秘密）。
func (p Paths) Validate() error {
	if strings.TrimSpace(p.DBPath) == "" {
		return errors.New("store: 数据库路径不能为空")
	}
	if strings.TrimSpace(p.DataDir) == "" {
		return errors.New("store: 数据目录不能为空")
	}
	return nil
}

// BackupsDir 返回备用备份目录。
func (p Paths) BackupsDir() string {
	return filepath.Join(p.DataDir, "backups")
}

// Store 持有 SQLite 连接池与路径配置。
type Store struct {
	db    *sql.DB
	paths Paths
}

// Open 打开（必要时创建）数据库并应用运行参数。
//
// 注意：Open 不会自动执行迁移；调用方需要显式调用 Migrate 或
// MigrateWith，以便迁移前备份与失败处理保持可见。
func Open(paths Paths) (*Store, error) {
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.DBPath), 0o750); err != nil {
		return nil, fmt.Errorf("store: 创建数据库目录失败：%w", err)
	}

	query := fmt.Sprintf(
		"mode=rwc&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		busyTimeoutMS,
	)
	db, err := sql.Open(sqliteDriverName, fileDSN(paths.DBPath, query))
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库失败：%w", err)
	}

	// 单写连接：所有写入与读取共享同一连接，避免 SQLite 写锁升级竞争。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 连接数据库失败：%w", err)
	}

	return &Store{db: db, paths: paths}, nil
}

// DB 暴露底层连接，供同包仓储与测试使用；业务层应通过仓储方法访问数据。
func (s *Store) DB() *sql.DB {
	return s.db
}

// Paths 返回创建 Store 时使用的路径配置。
func (s *Store) Paths() Paths {
	return s.paths
}

// DBPath 返回数据库文件路径。
func (s *Store) DBPath() string {
	return s.paths.DBPath
}

// BackupsDir 返回备份目录。
func (s *Store) BackupsDir() string {
	return s.paths.BackupsDir()
}

// Ping 检查数据库连接是否可用。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// fileDSN 构造 SQLite URI DSN。
//
// 统一使用 file: URI 可以避免 Windows 盘符、空格与查询参数之间的歧义；
// modernc.org/sqlite 只在 DSN 以 file: 开头时才保留 mode/_pragma 参数。
// 相对路径先转为绝对路径，避免 file:data/x 被解析成 authority=“data”。
func fileDSN(path, query string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	p := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(p, "/") {
		// Windows 盘符路径 C:/dir/db -> /C:/dir/db，得到 file:///C:/dir/db。
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	u.RawQuery = query
	return u.String()
}
