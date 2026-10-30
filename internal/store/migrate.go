package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationsDir = "migrations"

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

// Migration 是一个待应用或已应用的 SQL 迁移。
//
// Checksum 不对外暴露为字段：它始终由 SQL 内容计算，避免调用方伪造。
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// MigrateResult 描述一次迁移的结果。
type MigrateResult struct {
	// FromVersion 是迁移前数据库的 schema 版本；新库为 0。
	FromVersion int
	// ToVersion 是迁移完成后的 schema 版本。
	ToVersion int
	// Applied 按顺序列出本次实际应用的版本；没有待应用迁移时为空。
	Applied []int
	// BackupPath 是迁移前自动备份的路径；没有待应用迁移时为空。
	BackupPath string
}

type appliedMigration struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt string
}

var migrationFileName = regexp.MustCompile(`^(\d{4})_([a-z0-9][a-z0-9_-]*)\.sql$`)

// EmbeddedMigrations 从二进制内嵌文件加载全部迁移，并按版本升序返回。
func EmbeddedMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("store: 读取内嵌迁移目录失败：%w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		match := migrationFileName.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("store: 迁移文件名 %q 不符合 NNNN_name.sql", name)
		}
		version, err := strconv.Atoi(match[1])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("store: 迁移文件名 %q 的版本号非法", name)
		}
		content, err := migrationFS.ReadFile(path.Join(migrationsDir, name))
		if err != nil {
			return nil, fmt.Errorf("store: 读取迁移 %q 失败：%w", name, err)
		}
		migrations = append(migrations, Migration{
			Version: version,
			Name:    strings.TrimSuffix(name, ".sql"),
			SQL:     string(content),
		})
	}

	sortMigrations(migrations)
	if err := validateMigrations(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

// Migrate 使用内嵌迁移执行版本化迁移。
func (s *Store) Migrate(ctx context.Context) (MigrateResult, error) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		return MigrateResult{}, err
	}
	return s.MigrateWith(ctx, migrations)
}

// MigrateWith 使用给定迁移集合执行迁移。
//
// 该入口用于测试注入额外迁移或失败迁移；生产代码应优先调用 Migrate。
// 每个迁移独占一个事务：SQL 失败或版本记录写入失败都会整体回滚。
func (s *Store) MigrateWith(ctx context.Context, migrations []Migration) (MigrateResult, error) {
	sorted := append([]Migration(nil), migrations...)
	sortMigrations(sorted)
	if err := validateMigrations(sorted); err != nil {
		return MigrateResult{}, err
	}

	applied, _, err := s.readAppliedMigrations(ctx)
	if err != nil {
		return MigrateResult{}, err
	}

	byVersion := make(map[int]Migration, len(sorted))
	for _, migration := range sorted {
		byVersion[migration.Version] = migration
	}

	fromVersion := 0
	appliedVersions := make(map[int]struct{}, len(applied))
	for _, record := range applied {
		migration, ok := byVersion[record.Version]
		if !ok {
			return MigrateResult{}, fmt.Errorf(
				"store: 数据库已应用迁移 v%d（%s），但当前程序不包含该版本；拒绝降级启动",
				record.Version, record.Name,
			)
		}
		if record.Name != migration.Name {
			return MigrateResult{}, fmt.Errorf(
				"store: 迁移 v%d 记录名 %q 与程序内 %q 不一致；拒绝启动",
				record.Version, record.Name, migration.Name,
			)
		}
		if actual := migrationChecksum(migration.SQL); record.Checksum != actual {
			return MigrateResult{}, fmt.Errorf(
				"store: 迁移 v%d（%s）checksum 不匹配（数据库 %s，程序 %s）；拒绝启动",
				record.Version, record.Name, record.Checksum, actual,
			)
		}
		appliedVersions[record.Version] = struct{}{}
		if record.Version > fromVersion {
			fromVersion = record.Version
		}
	}

	var pending []Migration
	for _, migration := range sorted {
		if _, ok := appliedVersions[migration.Version]; ok {
			continue
		}
		if migration.Version < fromVersion {
			return MigrateResult{}, fmt.Errorf(
				"store: 数据库 schema 已到 v%d，但缺少更早的迁移 v%d（%s）；拒绝乱序迁移",
				fromVersion, migration.Version, migration.Name,
			)
		}
		pending = append(pending, migration)
	}

	result := MigrateResult{FromVersion: fromVersion, ToVersion: fromVersion}
	if len(pending) == 0 {
		return result, nil
	}

	// 迁移前备份：即使是从空库升级到 v1 也保留一份可打开的起点，
	// 迁移失败时可按计划“保留备份并停止启动”。
	backupPath, err := s.BackupTo(ctx, s.preMigrateBackupPath(fromVersion))
	if err != nil {
		return result, fmt.Errorf("store: 迁移前备份失败，已中止迁移：%w", err)
	}
	result.BackupPath = backupPath

	for _, migration := range pending {
		if err := s.applyMigration(ctx, migration); err != nil {
			return result, fmt.Errorf(
				"store: 应用迁移 v%d（%s）失败，已回滚：%w",
				migration.Version, migration.Name, err,
			)
		}
		result.Applied = append(result.Applied, migration.Version)
		if migration.Version > result.ToVersion {
			result.ToVersion = migration.Version
		}
	}
	return result, nil
}

// SchemaVersion 返回当前数据库 schema 版本；新库或缺失迁移表时返回 0。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	applied, exists, err := s.readAppliedMigrations(ctx)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	version := 0
	for _, record := range applied {
		if record.Version > version {
			version = record.Version
		}
	}
	return version, nil
}

// Migrate 打开数据库、执行内嵌迁移并关闭连接，供 CLI 使用。
func Migrate(ctx context.Context, paths Paths) (MigrateResult, error) {
	s, err := Open(paths)
	if err != nil {
		return MigrateResult{}, err
	}
	defer s.Close()
	return s.Migrate(ctx)
}

func (s *Store) readAppliedMigrations(ctx context.Context) ([]appliedMigration, bool, error) {
	var tableCount int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&tableCount)
	if err != nil {
		return nil, false, fmt.Errorf("store: 检查 schema_migrations 失败：%w", err)
	}
	if tableCount == 0 {
		return nil, false, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version ASC`,
	)
	if err != nil {
		return nil, true, fmt.Errorf("store: 读取 schema_migrations 失败：%w", err)
	}
	defer rows.Close()

	var records []appliedMigration
	for rows.Next() {
		var record appliedMigration
		if err := rows.Scan(&record.Version, &record.Name, &record.Checksum, &record.AppliedAt); err != nil {
			return nil, true, fmt.Errorf("store: 解析 schema_migrations 失败：%w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("store: 遍历 schema_migrations 失败：%w", err)
	}
	return records, true, nil
}

func (s *Store) applyMigration(ctx context.Context, migration Migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// schema_migrations 在事务内按需创建：新库迁移失败时表也随之回滚，
	// 不会留下半成品；已有库则是 no-op。
	if _, err := tx.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败：%w", err)
	}
	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		migration.Version,
		migration.Name,
		migrationChecksum(migration.SQL),
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("写入 schema_migrations 失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败：%w", err)
	}
	return nil
}

func (s *Store) preMigrateBackupPath(fromVersion int) string {
	return filepath.Join(s.paths.BackupsDir(), fmt.Sprintf("pre-migrate-v%d-%s.db", fromVersion, backupTimestamp(time.Now())))
}

func migrationChecksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

func sortMigrations(migrations []Migration) {
	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].Version == migrations[j].Version {
			return migrations[i].Name < migrations[j].Name
		}
		return migrations[i].Version < migrations[j].Version
	})
}

func validateMigrations(migrations []Migration) error {
	seen := make(map[int]string, len(migrations))
	for _, migration := range migrations {
		if migration.Version <= 0 {
			return fmt.Errorf("store: 迁移版本必须为正整数（%q）", migration.Name)
		}
		if strings.TrimSpace(migration.Name) == "" {
			return fmt.Errorf("store: 迁移 v%d 缺少名称", migration.Version)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("store: 迁移 v%d（%s）内容为空", migration.Version, migration.Name)
		}
		if previous, ok := seen[migration.Version]; ok {
			return fmt.Errorf("store: 迁移版本 v%d 重复（%s 与 %s）", migration.Version, previous, migration.Name)
		}
		seen[migration.Version] = migration.Name
	}
	return nil
}
