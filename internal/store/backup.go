package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const backupTimestampLayout = "20060102T150405.000000000Z"

// requiredBackupTables 是恢复时必须存在的核心表；缺少任何一张都视为
// 非网关数据库或损坏备份，拒绝覆盖当前库。
var requiredBackupTables = []string{
	"schema_migrations",
	"admin_credentials",
	"admin_sessions",
	"channels",
	"model_routes",
	"request_logs",
	"request_attempts",
	"request_captures",
}

// RestoreResult 描述一次恢复操作。
type RestoreResult struct {
	// BackupPath 是恢复所使用备份的路径。
	BackupPath string
	// BackupVersion 是该备份的 schema 版本。
	BackupVersion int
	// DBPath 是被替换的数据库路径。
	DBPath string
	// PreRestoreBackup 是覆盖前对当前库生成的备份；当前库不存在时为空。
	PreRestoreBackup string
}

type backupInfo struct {
	Version int
	Size    int64
}

// BackupTo 使用 VACUUM INTO 将当前数据库一致性快照写入 dest。
//
// 目标文件必须不存在；父目录会自动创建。调用前不能有未提交的业务写事务
// （本包单连接模型下，VACUUM INTO 会等待当前语句结束）。
func (s *Store) BackupTo(ctx context.Context, dest string) (string, error) {
	if strings.TrimSpace(dest) == "" {
		return "", errors.New("store: 备份目标路径不能为空")
	}
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", fmt.Errorf("store: 创建备份目录失败：%w", err)
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("store: 备份目标已存在：%s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("store: 检查备份目标失败：%w", err)
	}

	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		// VACUUM INTO 失败时可能留下未完成的文件；清理它，避免下次同名
		// 备份被“目标已存在”误挡。
		_ = os.Remove(dest)
		return "", fmt.Errorf("store: VACUUM INTO 备份失败：%w", err)
	}
	return dest, nil
}

// Backup 打开数据库并生成一份默认命名的备份。
//
// dest 为空时写入 DataDir/backups/backup-<timestamp>.db；非空时按用户
// 指定路径写入。返回最终备份路径。
func Backup(ctx context.Context, paths Paths, dest string) (string, error) {
	s, err := Open(paths)
	if err != nil {
		return "", err
	}
	defer s.Close()

	if strings.TrimSpace(dest) == "" {
		dest = filepath.Join(paths.BackupsDir(), "backup-"+backupTimestamp(time.Now())+".db")
	}
	return s.BackupTo(ctx, dest)
}

// Restore 校验备份并用它替换当前数据库。
//
// 安全顺序：
//  1. 只读打开备份，执行 integrity_check、核心表检查与版本兼容检查；
//  2. 当前库存在时先 VACUUM INTO 生成 pre-restore 备份；
//  3. 备份复制到同目录临时文件并再次校验，最后原子替换；
//  4. 清理旧库遗留的 -wal/-shm。
//
// 任何校验失败都发生在写操作之前，因此不会覆盖当前库。
func Restore(ctx context.Context, paths Paths, backupPath string) (RestoreResult, error) {
	if err := paths.Validate(); err != nil {
		return RestoreResult{}, err
	}
	if strings.TrimSpace(backupPath) == "" {
		return RestoreResult{}, errors.New("store: 备份路径不能为空")
	}

	sourcePath, err := filepath.Abs(filepath.Clean(backupPath))
	if err != nil {
		return RestoreResult{}, fmt.Errorf("store: 解析备份路径失败：%w", err)
	}
	dbPath, err := filepath.Abs(filepath.Clean(paths.DBPath))
	if err != nil {
		return RestoreResult{}, fmt.Errorf("store: 解析数据库路径失败：%w", err)
	}
	if strings.EqualFold(sourcePath, dbPath) {
		return RestoreResult{}, errors.New("store: 不能用当前数据库自身作为恢复来源")
	}

	info, err := inspectBackup(ctx, sourcePath)
	if err != nil {
		return RestoreResult{}, err
	}

	result := RestoreResult{
		BackupPath:    sourcePath,
		BackupVersion: info.Version,
		DBPath:        paths.DBPath,
	}

	if err := os.MkdirAll(filepath.Dir(paths.DBPath), 0o750); err != nil {
		return result, fmt.Errorf("store: 创建数据库目录失败：%w", err)
	}
	if err := os.MkdirAll(paths.BackupsDir(), 0o750); err != nil {
		return result, fmt.Errorf("store: 创建备份目录失败：%w", err)
	}

	// 覆盖前备份当前库；当前库不存在时无需备份。
	if _, statErr := os.Stat(paths.DBPath); statErr == nil {
		preRestoreBackup, err := backupCurrentForRestore(ctx, paths)
		if err != nil {
			return result, err
		}
		result.PreRestoreBackup = preRestoreBackup
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return result, fmt.Errorf("store: 检查当前数据库失败：%w", statErr)
	}

	tempPath, err := copyToTemp(filepath.Dir(paths.DBPath), sourcePath)
	if err != nil {
		return result, err
	}
	// 复制完成后再校验一次，确保落盘副本完整。
	if _, err := inspectBackup(ctx, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return result, fmt.Errorf("store: 恢复副本校验失败，当前库未改动：%w", err)
	}

	// 进程应已停止；删除旧库的 WAL/SHM，避免旧日志覆盖新数据。
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(paths.DBPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(tempPath)
			return result, fmt.Errorf("store: 清理旧库 %s 失败：%w", suffix, err)
		}
	}
	if err := os.Rename(tempPath, paths.DBPath); err != nil {
		_ = os.Remove(tempPath)
		return result, fmt.Errorf("store: 替换数据库失败：%w", err)
	}
	return result, nil
}

func backupCurrentForRestore(ctx context.Context, paths Paths) (string, error) {
	live, err := Open(paths)
	if err != nil {
		return "", fmt.Errorf("store: 打开当前数据库失败：%w", err)
	}
	version, versionErr := live.SchemaVersion(ctx)
	if versionErr != nil {
		_ = live.Close()
		return "", fmt.Errorf("store: 读取当前数据库版本失败：%w", versionErr)
	}
	dest := filepath.Join(paths.BackupsDir(), fmt.Sprintf(
		"pre-restore-v%d-%s.db", version, backupTimestamp(time.Now()),
	))
	path, backupErr := live.BackupTo(ctx, dest)
	closeErr := live.Close()
	if backupErr != nil {
		return "", fmt.Errorf("store: 恢复前备份失败，当前库未改动：%w", backupErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("store: 关闭当前数据库失败：%w", closeErr)
	}
	return path, nil
}

// inspectBackup 以只读方式校验备份文件。
func inspectBackup(ctx context.Context, backupPath string) (backupInfo, error) {
	stat, err := os.Stat(backupPath)
	if err != nil {
		return backupInfo{}, fmt.Errorf("store: 备份文件不可用：%w", err)
	}
	if stat.IsDir() {
		return backupInfo{}, errors.New("store: 备份路径是目录，拒绝恢复")
	}
	if stat.Size() == 0 {
		return backupInfo{}, errors.New("store: 备份文件为空，拒绝恢复")
	}

	db, err := sql.Open(sqliteDriverName, fileDSN(backupPath, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return backupInfo{}, fmt.Errorf("store: 打开备份文件失败：%w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return backupInfo{}, fmt.Errorf("store: 备份文件不是可读的 SQLite 数据库：%w", err)
	}
	if !strings.EqualFold(integrity, "ok") {
		return backupInfo{}, fmt.Errorf("store: 备份文件完整性校验失败：%s", integrity)
	}

	for _, table := range requiredBackupTables {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return backupInfo{}, fmt.Errorf("store: 备份缺少表 %s，拒绝恢复", table)
		}
		if err != nil {
			return backupInfo{}, fmt.Errorf("store: 校验备份表 %s 失败：%w", table, err)
		}
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return backupInfo{}, fmt.Errorf("store: 读取备份 schema 版本失败：%w", err)
	}
	if version <= 0 {
		return backupInfo{}, errors.New("store: 备份没有已应用的 schema 版本，拒绝恢复")
	}
	migrations, err := EmbeddedMigrations()
	if err != nil {
		return backupInfo{}, err
	}
	maxVersion := 0
	if len(migrations) > 0 {
		maxVersion = migrations[len(migrations)-1].Version
	}
	if version > maxVersion {
		return backupInfo{}, fmt.Errorf(
			"store: 备份 schema 版本 v%d 高于当前程序支持的 v%d，拒绝恢复", version, maxVersion,
		)
	}
	return backupInfo{Version: version, Size: stat.Size()}, nil
}

func copyToTemp(dir, sourcePath string) (string, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("store: 打开备份文件失败：%w", err)
	}
	defer source.Close()

	temp, err := os.CreateTemp(dir, ".restore-*.db")
	if err != nil {
		return "", fmt.Errorf("store: 创建恢复临时文件失败：%w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}

	if _, err := io.Copy(temp, source); err != nil {
		cleanup()
		return "", fmt.Errorf("store: 复制备份文件失败：%w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("store: 同步恢复临时文件失败：%w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("store: 关闭恢复临时文件失败：%w", err)
	}
	return tempPath, nil
}

func backupTimestamp(t time.Time) string {
	return t.UTC().Format(backupTimestampLayout)
}
