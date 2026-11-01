package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var expectedTables = []string{
	"schema_migrations",
	"admin_credentials",
	"admin_sessions",
	"channels",
	"model_routes",
	"request_logs",
	"request_attempts",
	"request_captures",
}

var expectedIndexes = []string{
	"idx_admin_sessions_expires_at",
	"idx_channels_enabled_priority",
	"idx_model_routes_client_model",
	"idx_model_routes_channel_id",
	"idx_request_logs_started_at",
	"idx_request_logs_channel_id",
	"idx_request_logs_model",
	"idx_request_logs_status",
	"idx_request_attempts_request_id",
	"idx_request_attempts_channel_id",
	"idx_request_captures_expires_at",
}

func newTestPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")}
}

func openMigratedStore(t *testing.T, paths Paths) (*Store, MigrateResult) {
	t.Helper()
	s, err := Open(paths)
	if err != nil {
		t.Fatalf("Open(%s): %v", paths.DBPath, err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	result, err := s.Migrate(context.Background())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s, result
}

func openReadOnly(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqliteDriverName, fileDSN(dbPath, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		t.Fatalf("以只读方式打开 %s: %v", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := db.Ping(); err != nil {
		t.Fatalf("只读连接 %s 不可用: %v", dbPath, err)
	}
	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&count); err != nil {
		t.Fatalf("查询表 %s: %v", name, err)
	}
	return count > 0
}

func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
	).Scan(&count); err != nil {
		t.Fatalf("查询索引 %s: %v", name, err)
	}
	return count > 0
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	result, err := db.Exec(query, args...)
	if err != nil {
		t.Fatalf("执行 SQL 失败: %v\nSQL: %s", err, query)
	}
	return result
}

func insertChannel(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result := mustExec(t, db, `
		INSERT INTO channels (
			name, base_url, protocol, key_ciphertext, key_mask,
			priority, max_concurrency, enabled, created_at, updated_at
		) VALUES (?, 'https://upstream.example/v1', 'openai_responses', 'v1:test-ciphertext', 'sk-***test', 10, 2, 1, ?, ?)`,
		name, now, now,
	)
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("读取 channel id: %v", err)
	}
	return id
}

func channelCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("统计 channels: %v", err)
	}
	return count
}

func latestVersion(t *testing.T) int {
	t.Helper()
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("没有内嵌迁移")
	}
	return migrations[len(migrations)-1].Version
}

// TestMigrateFreshAndIdempotent 覆盖：新库迁移后表/索引/版本齐全，第二次
// migrate 不重复执行，且 schema 不含多租户表。
func TestMigrateFreshAndIdempotent(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, result := openMigratedStore(t, paths)

	wantVersion := latestVersion(t)
	if result.FromVersion != 0 {
		t.Fatalf("新库 FromVersion = %d，期望 0", result.FromVersion)
	}
	if result.ToVersion != wantVersion {
		t.Fatalf("新库 ToVersion = %d，期望 %d", result.ToVersion, wantVersion)
	}
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != len(migrations) {
		t.Fatalf("新库 Applied = %v，期望 %d 个迁移", result.Applied, len(migrations))
	}
	for i, migration := range migrations {
		if result.Applied[i] != migration.Version {
			t.Fatalf("新库 Applied = %v，迁移顺序不符", result.Applied)
		}
	}
	if result.BackupPath == "" {
		t.Fatal("有待应用迁移时应生成迁移前备份")
	}
	if filepath.Dir(result.BackupPath) != filepath.Join(paths.DataDir, "backups") {
		t.Fatalf("新库迁移前备份目录 = %s，期望 %s",
			filepath.Dir(result.BackupPath), filepath.Join(paths.DataDir, "backups"))
	}
	if !strings.HasPrefix(filepath.Base(result.BackupPath), "pre-migrate-v0-") {
		t.Fatalf("新库迁移前备份文件名 = %s，期望 pre-migrate-v0-<timestamp>.db", filepath.Base(result.BackupPath))
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("新库迁移前备份不可用: %v", err)
	}

	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("SchemaVersion = %d，期望 %d", version, wantVersion)
	}

	var migrationRows int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&migrationRows); err != nil {
		t.Fatalf("统计 schema_migrations: %v", err)
	}
	if migrationRows != len(result.Applied) {
		t.Fatalf("schema_migrations 行数 = %d，期望 %d", migrationRows, len(result.Applied))
	}
	var recordedName, recordedChecksum, recordedAt string
	if err := s.DB().QueryRow(
		`SELECT name, checksum, applied_at FROM schema_migrations WHERE version = ?`, wantVersion,
	).Scan(&recordedName, &recordedChecksum, &recordedAt); err != nil {
		t.Fatalf("读取迁移记录: %v", err)
	}
	if recordedName == "" || recordedChecksum == "" || recordedAt == "" {
		t.Fatalf("迁移记录字段不完整: name=%q checksum=%q applied_at=%q", recordedName, recordedChecksum, recordedAt)
	}
	if _, err := time.Parse(time.RFC3339Nano, recordedAt); err != nil {
		t.Fatalf("applied_at 不是 RFC3339Nano: %q (%v)", recordedAt, err)
	}

	for _, table := range expectedTables {
		if !tableExists(t, s.DB(), table) {
			t.Errorf("缺少表 %s", table)
		}
	}
	for _, index := range expectedIndexes {
		if !indexExists(t, s.DB(), index) {
			t.Errorf("缺少索引 %s", index)
		}
	}

	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("列出 sqlite_master: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描表名: %v", err)
		}
		switch name {
		case "users", "roles", "groups", "billing":
			t.Errorf("schema 不应包含多租户表 %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 sqlite_master: %v", err)
	}

	second, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("第二次 Migrate: %v", err)
	}
	if len(second.Applied) != 0 {
		t.Fatalf("第二次迁移不应重复执行，Applied = %v", second.Applied)
	}
	if second.FromVersion != wantVersion || second.ToVersion != wantVersion {
		t.Fatalf("第二次迁移版本 = %d->%d，期望 %d->%d",
			second.FromVersion, second.ToVersion, wantVersion, wantVersion)
	}
	if second.BackupPath != "" {
		t.Fatalf("无待应用迁移时不应备份，得到 %s", second.BackupPath)
	}
	var afterRows int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&afterRows); err != nil {
		t.Fatalf("第二次统计 schema_migrations: %v", err)
	}
	if afterRows != migrationRows {
		t.Fatalf("第二次迁移后 schema_migrations 行数 = %d，期望 %d", afterRows, migrationRows)
	}
}

// TestOpenAppliesSQLitePragmas 覆盖 WAL/foreign_keys/busy_timeout/
// synchronous 与单写连接。
func TestOpenAppliesSQLitePragmas(t *testing.T) {
	paths := newTestPaths(t)
	s, err := Open(paths)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	var journalMode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q，期望 wal", journalMode)
	}

	var busyTimeout int
	if err := s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != busyTimeoutMS {
		t.Fatalf("busy_timeout = %d，期望 %d", busyTimeout, busyTimeoutMS)
	}

	var foreignKeys int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d，期望 1", foreignKeys)
	}

	var synchronous int
	if err := s.DB().QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d，期望 1 (NORMAL)", synchronous)
	}

	if maxOpen := s.DB().Stats().MaxOpenConnections; maxOpen != 1 {
		t.Fatalf("MaxOpenConnections = %d，期望 1", maxOpen)
	}
}

// TestOpenAndMigrateWithRelativePaths 覆盖默认数据目录形态（相对路径）：
// file: URI 不能被误解析为 authority，备份仍落在 DataDir/backups。
func TestOpenAndMigrateWithRelativePaths(t *testing.T) {
	workDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir(%s): %v", workDir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("恢复工作目录: %v", err)
		}
	})

	paths := Paths{DataDir: "data", DBPath: filepath.Join("data", "gateway.db")}
	s, err := Open(paths)
	if err != nil {
		t.Fatalf("Open(relative): %v", err)
	}
	defer s.Close()

	result, err := s.Migrate(context.Background())
	if err != nil {
		t.Fatalf("Migrate(relative): %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("相对路径迁移应生成迁移前备份")
	}
	wantBackupPrefix := filepath.Join("data", "backups") + string(os.PathSeparator)
	if !strings.HasPrefix(result.BackupPath, wantBackupPrefix) {
		t.Fatalf("备份路径 = %s，期望位于 %s", result.BackupPath, wantBackupPrefix)
	}
	if _, err := os.Stat(filepath.Join(workDir, "data", "gateway.db")); err != nil {
		t.Fatalf("数据库未落在数据目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, result.BackupPath)); err != nil {
		t.Fatalf("备份文件未落在数据目录: %v", err)
	}
}

// TestMigrateChecksumMismatch 覆盖：修改已应用迁移内容后拒绝启动，原库
// schema 与数据保持不变。
func TestMigrateChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, result := openMigratedStore(t, paths)
	insertChannel(t, s.DB(), "checksum-guard")

	original, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations: %v", err)
	}
	tampered := append([]Migration(nil), original...)
	tampered[0].SQL += "\n-- tampered after apply\n"

	if _, err := s.MigrateWith(ctx, tampered); err == nil {
		t.Fatal("checksum 不匹配时 MigrateWith 应失败")
	} else if !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("错误信息应包含 checksum，得到: %v", err)
	}

	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != result.ToVersion {
		t.Fatalf("checksum 失败后 schema 版本 = %d，期望 %d", version, result.ToVersion)
	}
	if count := channelCount(t, s.DB()); count != 1 {
		t.Fatalf("checksum 失败后 channels 行数 = %d，期望 1", count)
	}
	var name string
	if err := s.DB().QueryRow(`SELECT name FROM channels`).Scan(&name); err != nil {
		t.Fatalf("读取 channel: %v", err)
	}
	if name != "checksum-guard" {
		t.Fatalf("channel name = %q，期望 checksum-guard", name)
	}
}

// TestMigrateFailureRollsBack 覆盖：迁移中 SQL 失败时整体回滚，版本与
// 既有数据不变，且失败前的迁移前备份仍然存在。
func TestMigrateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, result := openMigratedStore(t, paths)
	insertChannel(t, s.DB(), "rollback-guard")

	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations: %v", err)
	}
	migrations = append(migrations, Migration{
		Version: result.ToVersion + 1,
		Name:    "0002_failure_probe",
		SQL: "CREATE TABLE migration_probe (id INTEGER PRIMARY KEY);\n" +
			"INSERT INTO migration_probe (id) VALUES (1);\n" +
			"INSERT INTO missing_table (id) VALUES (2);\n",
	})

	failed, err := s.MigrateWith(ctx, migrations)
	if err == nil {
		t.Fatal("迁移 SQL 失败时应返回错误")
	}
	if failed.BackupPath == "" {
		t.Fatal("失败迁移前应已生成迁移前备份")
	}
	if _, statErr := os.Stat(failed.BackupPath); statErr != nil {
		t.Fatalf("迁移前备份不可用: %v", statErr)
	}

	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != result.ToVersion {
		t.Fatalf("失败迁移后 schema 版本 = %d，期望 %d", version, result.ToVersion)
	}
	if tableExists(t, s.DB(), "migration_probe") {
		t.Fatal("失败迁移创建的表应被回滚")
	}
	if count := channelCount(t, s.DB()); count != 1 {
		t.Fatalf("失败迁移后 channels 行数 = %d，期望 1", count)
	}
	var name string
	if err := s.DB().QueryRow(`SELECT name FROM channels`).Scan(&name); err != nil {
		t.Fatalf("读取 channel: %v", err)
	}
	if name != "rollback-guard" {
		t.Fatalf("channel name = %q，期望 rollback-guard", name)
	}
}

// TestFreshMigrationFailureLeavesEmptySchema 覆盖新库首个迁移失败时连
// schema_migrations 都不残留，保证“迁移失败不改变原库”。
func TestFreshMigrationFailureLeavesEmptySchema(t *testing.T) {
	paths := newTestPaths(t)
	s, err := Open(paths)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	_, err = s.MigrateWith(context.Background(), []Migration{{
		Version: 1,
		Name:    "0001_broken",
		SQL: "CREATE TABLE broken_probe (id INTEGER PRIMARY KEY);\n" +
			"INSERT INTO missing_table (id) VALUES (1);\n",
	}})
	if err == nil {
		t.Fatal("首个迁移失败时应返回错误")
	}
	for _, table := range expectedTables {
		if tableExists(t, s.DB(), table) {
			t.Errorf("失败迁移后不应残留表 %s", table)
		}
	}
	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 0 {
		t.Fatalf("失败迁移后 schema 版本 = %d，期望 0", version)
	}
}

// TestPreMigrateBackupContents 覆盖：迁移前备份落在 GATEWAY_DATA_DIR/backups，
// 可重新打开，包含迁移前数据且不含新迁移 schema。
func TestPreMigrateBackupContents(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, result := openMigratedStore(t, paths)
	insertChannel(t, s.DB(), "before-migration")

	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations: %v", err)
	}
	nextVersion := result.ToVersion + 1
	migrations = append(migrations, Migration{
		Version: nextVersion,
		Name:    "0002_backup_probe",
		SQL:     "CREATE TABLE v2_backup_probe (id INTEGER PRIMARY KEY);\n",
	})

	// 证明备份位置不依赖进程 cwd：切到独立工作目录后执行迁移。
	workDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir(%s): %v", workDir, err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("恢复工作目录: %v", err)
		}
	}()

	migrated, err := s.MigrateWith(ctx, migrations)
	if err != nil {
		t.Fatalf("MigrateWith: %v", err)
	}
	if migrated.BackupPath == "" {
		t.Fatal("已有 schema 的迁移必须生成迁移前备份")
	}
	wantDir := filepath.Join(paths.DataDir, "backups")
	if filepath.Dir(migrated.BackupPath) != wantDir {
		t.Fatalf("备份目录 = %s，期望 %s", filepath.Dir(migrated.BackupPath), wantDir)
	}
	base := filepath.Base(migrated.BackupPath)
	if !strings.HasPrefix(base, "pre-migrate-v"+strconv.Itoa(result.ToVersion)+"-") || !strings.HasSuffix(base, ".db") {
		t.Fatalf("迁移前备份文件名 = %s，期望 pre-migrate-v%d-<timestamp>.db", base, result.ToVersion)
	}
	if _, err := os.Stat(migrated.BackupPath); err != nil {
		t.Fatalf("迁移前备份不可用: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "data", "backups")); !os.IsNotExist(err) {
		t.Fatalf("不应在进程 cwd 下创建 data/backups，err = %v", err)
	}

	if !tableExists(t, s.DB(), "v2_backup_probe") {
		t.Fatal("迁移后缺少 v2_backup_probe 表")
	}

	backup := openReadOnly(t, migrated.BackupPath)
	var backupVersion int
	if err := backup.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&backupVersion); err != nil {
		t.Fatalf("读取备份版本: %v", err)
	}
	if backupVersion != result.ToVersion {
		t.Fatalf("备份 schema 版本 = %d，期望 %d", backupVersion, result.ToVersion)
	}
	var name string
	if err := backup.QueryRow(`SELECT name FROM channels`).Scan(&name); err != nil {
		t.Fatalf("从备份读取 channel: %v", err)
	}
	if name != "before-migration" {
		t.Fatalf("备份中的 channel name = %q，期望 before-migration", name)
	}
	if tableExists(t, backup, "v2_backup_probe") {
		t.Fatal("迁移前备份不应包含新迁移创建的表")
	}
}

// TestBackupDefaultPath 覆盖 CLI 无参数备份：默认写入 DataDir/backups，
// 生成的文件可重新打开且包含当前 schema。
func TestBackupDefaultPath(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, result := openMigratedStore(t, paths)
	insertChannel(t, s.DB(), "backup-default")

	backupPath, err := Backup(ctx, paths, "")
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if filepath.Dir(backupPath) != filepath.Join(paths.DataDir, "backups") {
		t.Fatalf("默认备份目录 = %s，期望 %s", filepath.Dir(backupPath), filepath.Join(paths.DataDir, "backups"))
	}
	if !strings.HasPrefix(filepath.Base(backupPath), "backup-") {
		t.Fatalf("默认备份文件名 = %s，期望 backup-<timestamp>.db", filepath.Base(backupPath))
	}
	backup := openReadOnly(t, backupPath)
	var version int
	if err := backup.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("读取默认备份版本: %v", err)
	}
	if version != result.ToVersion {
		t.Fatalf("默认备份版本 = %d，期望 %d", version, result.ToVersion)
	}
}

// TestBackupAndRestore 覆盖：VACUUM INTO 备份可重新打开；restore 前自动
// 备份当前库，恢复后回到备份时点数据。
func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, result := openMigratedStore(t, paths)
	insertChannel(t, s.DB(), "restored-channel")

	dest := filepath.Join(paths.BackupsDir(), "snapshot.db")
	backupPath, err := s.BackupTo(ctx, dest)
	if err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	snapshot := openReadOnly(t, backupPath)
	var snapshotVersion int
	if err := snapshot.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&snapshotVersion); err != nil {
		t.Fatalf("读取快照版本: %v", err)
	}
	if snapshotVersion != result.ToVersion {
		t.Fatalf("快照版本 = %d，期望 %d", snapshotVersion, result.ToVersion)
	}
	var snapshotName string
	if err := snapshot.QueryRow(`SELECT name FROM channels`).Scan(&snapshotName); err != nil {
		t.Fatalf("读取快照 channel: %v", err)
	}
	if snapshotName != "restored-channel" {
		t.Fatalf("快照 channel = %q，期望 restored-channel", snapshotName)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("关闭快照: %v", err)
	}

	insertChannel(t, s.DB(), "live-only")
	if err := s.Close(); err != nil {
		t.Fatalf("关闭 live store: %v", err)
	}

	restored, err := Restore(ctx, paths, backupPath)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.BackupVersion != result.ToVersion {
		t.Fatalf("恢复备份版本 = %d，期望 %d", restored.BackupVersion, result.ToVersion)
	}
	if restored.PreRestoreBackup == "" {
		t.Fatal("覆盖已有数据库前必须生成 pre-restore 备份")
	}
	if _, err := os.Stat(restored.PreRestoreBackup); err != nil {
		t.Fatalf("pre-restore 备份不可用: %v", err)
	}
	if filepath.Dir(restored.PreRestoreBackup) != filepath.Join(paths.DataDir, "backups") {
		t.Fatalf("pre-restore 备份目录 = %s，期望 %s",
			filepath.Dir(restored.PreRestoreBackup), filepath.Join(paths.DataDir, "backups"))
	}
	if !strings.HasPrefix(filepath.Base(restored.PreRestoreBackup), "pre-restore-v"+strconv.Itoa(result.ToVersion)+"-") {
		t.Fatalf("pre-restore 备份文件名不符合约定: %s", filepath.Base(restored.PreRestoreBackup))
	}

	reopened, err := Open(paths)
	if err != nil {
		t.Fatalf("恢复后 Open: %v", err)
	}
	defer reopened.Close()
	version, err := reopened.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("恢复后 SchemaVersion: %v", err)
	}
	if version != result.ToVersion {
		t.Fatalf("恢复后版本 = %d，期望 %d", version, result.ToVersion)
	}
	var names []string
	rows, err := reopened.DB().Query(`SELECT name FROM channels ORDER BY name`)
	if err != nil {
		t.Fatalf("恢复后查询 channels: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描 channel name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 channels: %v", err)
	}
	_ = rows.Close()
	if len(names) != 1 || names[0] != "restored-channel" {
		t.Fatalf("恢复后 channels = %v，期望 [restored-channel]", names)
	}

	preRestore := openReadOnly(t, restored.PreRestoreBackup)
	var preRestoreNames []string
	preRows, err := preRestore.Query(`SELECT name FROM channels ORDER BY name`)
	if err != nil {
		t.Fatalf("读取 pre-restore 备份: %v", err)
	}
	for preRows.Next() {
		var name string
		if err := preRows.Scan(&name); err != nil {
			t.Fatalf("扫描 pre-restore channel: %v", err)
		}
		preRestoreNames = append(preRestoreNames, name)
	}
	if err := preRows.Err(); err != nil {
		t.Fatalf("遍历 pre-restore channels: %v", err)
	}
	_ = preRows.Close()
	if len(preRestoreNames) != 2 {
		t.Fatalf("pre-restore 备份 channels = %v，期望 2 行", preRestoreNames)
	}
}

// TestRestoreIntoEmptyPath 覆盖对尚不存在的数据库执行恢复：不做
// pre-restore 备份，恢复后可直接打开。
func TestRestoreIntoEmptyPath(t *testing.T) {
	ctx := context.Background()
	sourcePaths := newTestPaths(t)
	source, result := openMigratedStore(t, sourcePaths)
	insertChannel(t, source.DB(), "fresh-restore")
	backupPath, err := source.BackupTo(ctx, filepath.Join(t.TempDir(), "fresh-restore.db"))
	if err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("关闭 source: %v", err)
	}

	targetPaths := newTestPaths(t)
	restored, err := Restore(ctx, targetPaths, backupPath)
	if err != nil {
		t.Fatalf("Restore 到空路径: %v", err)
	}
	if restored.PreRestoreBackup != "" {
		t.Fatalf("空路径恢复不应生成 pre-restore 备份，得到 %s", restored.PreRestoreBackup)
	}
	db, err := Open(targetPaths)
	if err != nil {
		t.Fatalf("恢复后 Open: %v", err)
	}
	defer db.Close()
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("恢复后 SchemaVersion: %v", err)
	}
	if version != result.ToVersion {
		t.Fatalf("恢复后版本 = %d，期望 %d", version, result.ToVersion)
	}
	var name string
	if err := db.DB().QueryRow(`SELECT name FROM channels`).Scan(&name); err != nil {
		t.Fatalf("恢复后查询 channel: %v", err)
	}
	if name != "fresh-restore" {
		t.Fatalf("恢复后 channel = %q，期望 fresh-restore", name)
	}
}

// TestRestoreRejectsInvalidBackup 覆盖：损坏文件、缺表数据库、版本过新的
// 备份都必须被拒绝，且当前库不被覆盖。
func TestRestoreRejectsInvalidBackup(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, _ := openMigratedStore(t, paths)
	insertChannel(t, s.DB(), "keep-me")
	if err := s.Close(); err != nil {
		t.Fatalf("关闭 store: %v", err)
	}
	liveVersion := latestVersion(t)

	t.Run("损坏文件", func(t *testing.T) {
		corrupt := filepath.Join(t.TempDir(), "corrupt.db")
		if err := os.WriteFile(corrupt, []byte("this is not a sqlite database"), 0o600); err != nil {
			t.Fatalf("写入损坏文件: %v", err)
		}
		if _, err := Restore(ctx, paths, corrupt); err == nil {
			t.Fatal("损坏备份应被拒绝")
		}
		assertLiveChannel(t, paths, "keep-me", liveVersion)
	})

	t.Run("缺少核心表", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other.db")
		otherDB, err := sql.Open(sqliteDriverName, fileDSN(other, "mode=rwc"))
		if err != nil {
			t.Fatalf("打开临时 sqlite: %v", err)
		}
		if _, err := otherDB.Exec(`CREATE TABLE something_else (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatalf("创建临时表: %v", err)
		}
		if err := otherDB.Close(); err != nil {
			t.Fatalf("关闭临时 sqlite: %v", err)
		}
		if _, err := Restore(ctx, paths, other); err == nil {
			t.Fatal("缺少核心表的备份应被拒绝")
		} else if !strings.Contains(err.Error(), "schema_migrations") {
			t.Fatalf("错误应指出缺少 schema_migrations，得到: %v", err)
		}
		assertLiveChannel(t, paths, "keep-me", liveVersion)
	})

	t.Run("版本过新", func(t *testing.T) {
		source, err := Open(Paths{DataDir: t.TempDir(), DBPath: filepath.Join(t.TempDir(), "future.db")})
		if err != nil {
			t.Fatalf("Open future: %v", err)
		}
		if _, err := source.Migrate(ctx); err != nil {
			t.Fatalf("future Migrate: %v", err)
		}
		if _, err := source.DB().Exec(
			`UPDATE schema_migrations SET version = ? WHERE version = ?`, liveVersion+100, liveVersion,
		); err != nil {
			t.Fatalf("抬高版本: %v", err)
		}
		futurePath := source.DBPath()
		if err := source.Close(); err != nil {
			t.Fatalf("关闭 future store: %v", err)
		}
		if _, err := Restore(ctx, paths, futurePath); err == nil {
			t.Fatal("版本过新的备份应被拒绝")
		} else if !strings.Contains(err.Error(), "高于当前程序支持") {
			t.Fatalf("错误应说明版本过高，得到: %v", err)
		}
		assertLiveChannel(t, paths, "keep-me", liveVersion)
	})

	// 校验失败不应产生 pre-restore 备份。
	if _, err := os.Stat(paths.BackupsDir()); err == nil {
		entries, readErr := os.ReadDir(paths.BackupsDir())
		if readErr != nil {
			t.Fatalf("读取备份目录: %v", readErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "pre-restore-") {
				t.Fatalf("校验失败时不应生成 pre-restore 备份: %s", entry.Name())
			}
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("检查备份目录: %v", err)
	}
}

// TestDeleteChannelPreservesHistory 覆盖删除渠道的 FK 语义：
// model_routes 级联删除，request_logs/request_attempts.channel_id 置 NULL，
// channel_name 快照保留。
func TestDeleteChannelPreservesHistory(t *testing.T) {
	ctx := context.Background()
	paths := newTestPaths(t)
	s, _ := openMigratedStore(t, paths)

	var foreignKeys int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d，期望 1", foreignKeys)
	}

	channelID := insertChannel(t, s.DB(), "chan-delete")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	mustExec(t, s.DB(), `
		INSERT INTO model_routes (client_model, channel_id, upstream_model, priority, enabled, created_at, updated_at)
		VALUES ('client-model', ?, 'upstream-model', 5, 1, ?, ?)`,
		channelID, now, now,
	)
	mustExec(t, s.DB(), `
		INSERT INTO request_logs (
			request_id, protocol, endpoint, model, channel_id, channel_name, status,
			http_status, stream, attempt_count, retry_count, latency_ms, semantic_committed, started_at
		) VALUES ('req-1', 'openai_responses', '/v1/responses', 'client-model', ?, 'chan-delete', 'completed',
			200, 1, 1, 0, 120, 0, ?)`,
		channelID, now,
	)
	mustExec(t, s.DB(), `
		INSERT INTO request_attempts (
			request_id, attempt_index, channel_id, channel_name, upstream_model, status,
			http_status, latency_ms, started_at
		) VALUES ('req-1', 1, ?, 'chan-delete', 'upstream-model', 'success', 200, 110, ?)`,
		channelID, now,
	)

	if _, err := s.DB().ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, channelID); err != nil {
		t.Fatalf("删除 channel: %v", err)
	}

	var routeCount int
	if err := s.DB().QueryRow(`SELECT count(*) FROM model_routes`).Scan(&routeCount); err != nil {
		t.Fatalf("统计 model_routes: %v", err)
	}
	if routeCount != 0 {
		t.Fatalf("删除渠道后 model_routes 行数 = %d，期望 0（级联删除）", routeCount)
	}

	var logChannelID sql.NullInt64
	var logChannelName string
	if err := s.DB().QueryRow(
		`SELECT channel_id, channel_name FROM request_logs WHERE request_id = 'req-1'`,
	).Scan(&logChannelID, &logChannelName); err != nil {
		t.Fatalf("读取 request_logs: %v", err)
	}
	if logChannelID.Valid {
		t.Fatalf("request_logs.channel_id = %d，期望 NULL", logChannelID.Int64)
	}
	if logChannelName != "chan-delete" {
		t.Fatalf("request_logs.channel_name = %q，期望 chan-delete", logChannelName)
	}

	var attemptChannelID sql.NullInt64
	var attemptChannelName string
	if err := s.DB().QueryRow(
		`SELECT channel_id, channel_name FROM request_attempts WHERE request_id = 'req-1' AND attempt_index = 1`,
	).Scan(&attemptChannelID, &attemptChannelName); err != nil {
		t.Fatalf("读取 request_attempts: %v", err)
	}
	if attemptChannelID.Valid {
		t.Fatalf("request_attempts.channel_id = %d，期望 NULL", attemptChannelID.Int64)
	}
	if attemptChannelName != "chan-delete" {
		t.Fatalf("request_attempts.channel_name = %q，期望 chan-delete", attemptChannelName)
	}
}

// TestChannelsNameUniqueConstraint 覆盖 0001 迁移中的 channels.name UNIQUE：
// 重复插入被数据库拒绝，原记录保持不变。
func TestChannelsNameUniqueConstraint(t *testing.T) {
	paths := newTestPaths(t)
	s, _ := openMigratedStore(t, paths)

	firstID := insertChannel(t, s.DB(), "unique-name")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB().Exec(`
		INSERT INTO channels (
			name, base_url, protocol, key_ciphertext, key_mask,
			priority, max_concurrency, enabled, created_at, updated_at
		) VALUES ('unique-name', 'https://other.example/v1', 'openai_chat', 'v1:other', 'sk-***other', 0, 1, 1, ?, ?)`,
		now, now,
	)
	if err == nil {
		t.Fatal("重复 channels.name 应被 UNIQUE 约束拒绝")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("约束错误信息应包含 unique，得到: %v", err)
	}
	if count := channelCount(t, s.DB()); count != 1 {
		t.Fatalf("重复插入后 channels 行数 = %d，期望 1", count)
	}
	var id int64
	var name string
	if err := s.DB().QueryRow(`SELECT id, name FROM channels`).Scan(&id, &name); err != nil {
		t.Fatalf("读取 channel: %v", err)
	}
	if id != firstID || name != "unique-name" {
		t.Fatalf("原记录被修改：id=%d name=%q", id, name)
	}
}

func TestRestoreRejectsIncompatibleHistoryAndForeignKeys(t *testing.T) {
	for _, test := range []struct{ name, query string }{
		{"checksum", `UPDATE schema_migrations SET checksum = 'wrong' WHERE version = 1`},
		{"name", `UPDATE schema_migrations SET name = 'wrong' WHERE version = 1`},
		{"history_gap", `DELETE FROM schema_migrations WHERE version = 1`},
		{"foreign_key", `INSERT INTO model_routes (client_model, channel_id, upstream_model, created_at, updated_at) VALUES ('orphan', 99999, 'orphan', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			paths := newTestPaths(t)
			live, result := openMigratedStore(t, paths)
			insertChannel(t, live.DB(), "keep-me")
			if err := live.Close(); err != nil {
				t.Fatal(err)
			}
			source, _ := openMigratedStore(t, newTestPaths(t))
			mustExec(t, source.DB(), `PRAGMA foreign_keys = OFF`)
			mustExec(t, source.DB(), test.query)
			if test.name != "foreign_key" {
				if _, err := source.Migrate(ctx); err == nil {
					t.Fatal("startup accepted incompatible migration history")
				}
			}
			backupPath := source.DBPath()
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(ctx, paths, backupPath); err == nil {
				t.Fatal("invalid backup accepted")
			}
			assertLiveChannel(t, paths, "keep-me", result.ToVersion)
			entries, err := os.ReadDir(paths.BackupsDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "pre-restore-") {
					t.Fatal("preflight failure created a pre-restore backup")
				}
			}
		})
	}
}

func TestChannelRevisionMigrationPreservesExistingData(t *testing.T) {
	ctx := context.Background()
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 2 {
		t.Fatal("missing channel revision migration")
	}
	s, err := Open(newTestPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.MigrateWith(ctx, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	id := insertChannel(t, s.DB(), "legacy")
	mustExec(t, s.DB(), `UPDATE channels SET last_health_status = 'healthy', last_health_at = '2026-01-01T00:00:00Z' WHERE id = ?`, id)
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	channel, err := s.Channels().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if channel.ConnectionRevision != 1 || channel.Name != "legacy" || channel.KeyCiphertext != "v1:test-ciphertext" {
		t.Fatalf("migration lost configuration: %+v", channel)
	}
	if channel.LastHealthRevision != 0 || channel.LastHealthStatus != "" || channel.LastHealthAt != nil {
		t.Fatalf("unversioned legacy health should be invalidated: %+v", channel)
	}
}

func assertLiveChannel(t *testing.T, paths Paths, wantName string, wantVersion int) {
	t.Helper()
	db, err := Open(paths)
	if err != nil {
		t.Fatalf("重新打开 live DB: %v", err)
	}
	defer db.Close()
	version, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("live SchemaVersion: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("live schema 版本 = %d，期望 %d", version, wantVersion)
	}
	var name string
	if err := db.DB().QueryRow(`SELECT name FROM channels`).Scan(&name); err != nil {
		t.Fatalf("live 查询 channel: %v", err)
	}
	if name != wantName {
		t.Fatalf("live channel = %q，期望 %q", name, wantName)
	}
}
