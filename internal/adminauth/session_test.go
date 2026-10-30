package adminauth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-router/internal/store"
)

// testHashIterations 让 session 相关测试使用低成本哈希；VerifyPassword 从编码中
// 读取迭代次数，因此仍覆盖真实校验路径。
const testHashIterations = 1000

const (
	testAdminPassword = "correct-horse-battery"
	testSource        = "10.0.0.1"
)

func newTestManager(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()

	dir := t.TempDir()
	s, err := store.Open(store.Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")})
	if err != nil {
		t.Fatalf("打开测试数据库失败：%v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移测试数据库失败：%v", err)
	}

	manager, err := NewManager(s.DB())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager, s.DB()
}

// seedAdmin 直接写入低成本哈希，避免每个测试都执行 600k 次派生。
func seedAdmin(t *testing.T, db *sql.DB, password string) string {
	t.Helper()

	hash, err := hashPassword(password, []byte("0123456789abcdef"), testHashIterations, PasswordKeySize)
	if err != nil {
		t.Fatalf("生成测试哈希失败：%v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO admin_credentials (id, password_hash, created_at, updated_at) VALUES (1, ?, ?, ?)`,
		hash, now, now,
	); err != nil {
		t.Fatalf("写入管理员凭据失败：%v", err)
	}
	return hash
}

// setClock 固定 Manager 的当前时间，返回可在测试中推进的指针。
func setClock(t *testing.T, manager *Manager, start time.Time) *time.Time {
	t.Helper()
	now := start
	manager.now = func() time.Time { return now }
	return &now
}

func mustLogin(t *testing.T, manager *Manager, source, password string) LoginResult {
	t.Helper()
	result, err := manager.Login(context.Background(), source, password)
	if err != nil {
		t.Fatalf("Login(%s): %v", source, err)
	}
	return result
}

func TestEnsureAdminCreatesOnceAndHashesPassword(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)

	created, err := manager.EnsureAdmin(ctx, testAdminPassword)
	if err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}
	if !created {
		t.Fatal("空库首次 EnsureAdmin 应返回 created=true")
	}

	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id = 1`).Scan(&hash); err != nil {
		t.Fatalf("读取密码哈希失败：%v", err)
	}
	if strings.Contains(hash, testAdminPassword) {
		t.Fatalf("数据库包含明文密码：%s", hash)
	}
	if !strings.HasPrefix(hash, PasswordHashAlgorithm+"$") {
		t.Fatalf("密码哈希前缀错误：%s", hash)
	}
	if ok, err := VerifyPassword(hash, testAdminPassword); err != nil || !ok {
		t.Fatalf("入库哈希无法校验：ok=%v err=%v", ok, err)
	}

	// 已有记录时必须忽略新的环境变量值（包括空值）。
	created, err = manager.EnsureAdmin(ctx, "")
	if err != nil {
		t.Fatalf("已有记录时 EnsureAdmin 返回错误：%v", err)
	}
	if created {
		t.Fatal("已有记录时 EnsureAdmin 不应重复创建")
	}
	var hashAfter string
	if err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id = 1`).Scan(&hashAfter); err != nil {
		t.Fatalf("再次读取密码哈希失败：%v", err)
	}
	if hashAfter != hash {
		t.Fatal("已有记录时密码哈希被改写")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM admin_credentials`).Scan(&count); err != nil {
		t.Fatalf("统计管理员记录失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("admin_credentials 记录数 = %d，期望 1", count)
	}
}

func TestEnsureAdminRejectsMissingOrShortInitialPassword(t *testing.T) {
	ctx := context.Background()

	t.Run("空密码", func(t *testing.T) {
		manager, db := newTestManager(t)
		if _, err := manager.EnsureAdmin(ctx, ""); !errors.Is(err, ErrAdminNotInitialized) {
			t.Fatalf("错误 = %v，期望 ErrAdminNotInitialized", err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM admin_credentials`).Scan(&count); err != nil {
			t.Fatalf("统计管理员记录失败：%v", err)
		}
		if count != 0 {
			t.Fatalf("失败初始化写入了 %d 条记录", count)
		}
	})

	t.Run("短密码", func(t *testing.T) {
		manager, db := newTestManager(t)
		if _, err := manager.EnsureAdmin(ctx, "too-short"); !errors.Is(err, ErrPasswordTooShort) {
			t.Fatalf("错误 = %v，期望 ErrPasswordTooShort", err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM admin_credentials`).Scan(&count); err != nil {
			t.Fatalf("统计管理员记录失败：%v", err)
		}
		if count != 0 {
			t.Fatalf("失败初始化写入了 %d 条记录", count)
		}
	})
}

func TestLoginCreatesRotatedSessions(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)
	seedAdmin(t, db, testAdminPassword)

	start := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	setClock(t, manager, start)

	if _, err := manager.Login(ctx, testSource, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("错误密码登录错误 = %v，期望 ErrInvalidCredentials", err)
	}

	first := mustLogin(t, manager, testSource, testAdminPassword)
	if want := base64.RawURLEncoding.EncodedLen(SessionTokenSize); len(first.Token) != want {
		t.Fatalf("token 长度 = %d，期望 %d", len(first.Token), want)
	}
	if !first.ExpiresAt.Equal(start.Add(DefaultSessionTTL)) {
		t.Fatalf("过期时间 = %s，期望 %s", first.ExpiresAt, start.Add(DefaultSessionTTL))
	}

	session, err := manager.ValidateSession(ctx, first.Token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if session.ID != first.SessionID {
		t.Fatalf("session ID = %q，期望 %q", session.ID, first.SessionID)
	}

	second := mustLogin(t, manager, testSource, testAdminPassword)
	if second.Token == first.Token {
		t.Fatal("重新登录生成了相同 token")
	}
	if second.SessionID == first.SessionID {
		t.Fatal("重新登录生成了相同 session ID")
	}
	if _, err := manager.ValidateSession(ctx, second.Token); err != nil {
		t.Fatalf("第二个 session 不可用：%v", err)
	}

	var stored string
	if err := db.QueryRow(
		`SELECT token_hash FROM admin_sessions WHERE id = ?`, first.SessionID,
	).Scan(&stored); err != nil {
		t.Fatalf("读取 token 哈希失败：%v", err)
	}
	if stored != hashSessionToken(first.Token) {
		t.Fatalf("数据库 token_hash = %q，期望 SHA-256", stored)
	}
	if stored == first.Token {
		t.Fatal("数据库保存了 session token 明文")
	}

	var total int
	if err := db.QueryRow(`SELECT count(*) FROM admin_sessions`).Scan(&total); err != nil {
		t.Fatalf("统计 session 失败：%v", err)
	}
	if total != 2 {
		t.Fatalf("session 数 = %d，期望 2", total)
	}
}

func TestLoginWithoutAdminRecordFailsClosed(t *testing.T) {
	manager, _ := newTestManager(t)
	setClock(t, manager, time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC))

	if _, err := manager.Login(context.Background(), testSource, "whatever-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("无管理员记录时错误 = %v，期望 ErrInvalidCredentials", err)
	}
}

func TestLoginRejectsCorruptStoredHash(t *testing.T) {
	manager, db := newTestManager(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO admin_credentials (id, password_hash, created_at, updated_at) VALUES (1, 'not-a-hash', ?, ?)`,
		now, now,
	); err != nil {
		t.Fatalf("写入非法哈希失败：%v", err)
	}

	if _, err := manager.Login(context.Background(), testSource, testAdminPassword); !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatalf("错误 = %v，期望 ErrInvalidPasswordHash", err)
	}
}

func TestSessionExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)
	seedAdmin(t, db, testAdminPassword)

	start := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	now := setClock(t, manager, start)

	first := mustLogin(t, manager, testSource, testAdminPassword)
	*now = start.Add(DefaultSessionTTL - time.Second)
	if _, err := manager.ValidateSession(ctx, first.Token); err != nil {
		t.Fatalf("过期前应有效：%v", err)
	}

	*now = start.Add(DefaultSessionTTL)
	if _, err := manager.ValidateSession(ctx, first.Token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("到期时错误 = %v，期望 ErrSessionExpired", err)
	}

	*now = start
	second := mustLogin(t, manager, testSource, testAdminPassword)
	if err := manager.Logout(ctx, second.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := manager.ValidateSession(ctx, second.Token); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("撤销后错误 = %v，期望 ErrSessionRevoked", err)
	}
	if err := manager.Logout(ctx, second.Token); err != nil {
		t.Fatalf("重复 Logout 应静默成功：%v", err)
	}
	if err := manager.Logout(ctx, "unknown-token"); err != nil {
		t.Fatalf("未知 token Logout 应静默成功：%v", err)
	}

	if _, err := manager.ValidateSession(ctx, "unknown-token"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("未知 token 错误 = %v，期望 ErrInvalidSession", err)
	}
	if _, err := manager.ValidateSession(ctx, ""); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("空 token 错误 = %v，期望 ErrInvalidSession", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)
	seedAdmin(t, db, testAdminPassword)

	start := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	now := setClock(t, manager, start)

	for i := 0; i < MaxLoginFailures; i++ {
		if _, err := manager.Login(ctx, testSource, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("第 %d 次失败错误 = %v，期望 ErrInvalidCredentials", i+1, err)
		}
	}
	// 第 6 次即便密码正确也被限流。
	if _, err := manager.Login(ctx, testSource, testAdminPassword); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("第 6 次尝试错误 = %v，期望 ErrRateLimited", err)
	}

	// 其他来源不受影响。
	if _, err := manager.Login(ctx, "10.0.0.2", testAdminPassword); err != nil {
		t.Fatalf("其他来源登录失败：%v", err)
	}

	// 窗口到期后恢复。
	*now = start.Add(LoginFailureWindow)
	if _, err := manager.Login(ctx, testSource, testAdminPassword); err != nil {
		t.Fatalf("窗口到期后登录失败：%v", err)
	}

	// 成功登录清零计数：再失败 4 次仍允许，第 5 次失败后才限流。
	for i := 0; i < MaxLoginFailures-1; i++ {
		if _, err := manager.Login(ctx, testSource, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("清零后第 %d 次失败错误 = %v", i+1, err)
		}
	}
	if _, err := manager.Login(ctx, testSource, testAdminPassword); err != nil {
		t.Fatalf("4 次失败后正确密码应可登录：%v", err)
	}
	for i := 0; i < MaxLoginFailures; i++ {
		if _, err := manager.Login(ctx, testSource, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("再次失败第 %d 次错误 = %v", i+1, err)
		}
	}
	if _, err := manager.Login(ctx, testSource, testAdminPassword); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("连续 5 次失败后错误 = %v，期望 ErrRateLimited", err)
	}
}

func TestLoginLimiterSuccessAndWindow(t *testing.T) {
	limiter := newLoginLimiter(LoginFailureWindow, MaxLoginFailures)
	start := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)

	for i := 0; i < MaxLoginFailures-1; i++ {
		if !limiter.beginAttempt("source", start) {
			t.Fatalf("第 %d 次尝试不应被限流", i+1)
		}
	}
	limiter.recordSuccess("source")

	// 成功登录清零后重新获得完整额度：前 4 次允许，第 5 次允许，第 6 次拒绝。
	for i := 0; i < MaxLoginFailures-1; i++ {
		if !limiter.beginAttempt("source", start) {
			t.Fatalf("清零后第 %d 次尝试不应被限流", i+1)
		}
	}
	if !limiter.beginAttempt("source", start) {
		t.Fatal("第 5 次尝试不应被限流")
	}
	if limiter.beginAttempt("source", start) {
		t.Fatal("第 6 次尝试应被限流")
	}

	if !limiter.beginAttempt("source", start.Add(LoginFailureWindow)) {
		t.Fatal("窗口结束后仍被限流")
	}
	if !limiter.beginAttempt("other", start) {
		t.Fatal("不同来源不应共享尝试计数")
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)
	seedAdmin(t, db, testAdminPassword)
	setClock(t, manager, time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC))

	first := mustLogin(t, manager, testSource, testAdminPassword)
	second := mustLogin(t, manager, testSource, testAdminPassword)

	const newPassword = "new-correct-horse-battery"

	if err := manager.ChangePassword(ctx, first.Token, "wrong-old-password", newPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧密码错误时错误 = %v，期望 ErrInvalidCredentials", err)
	}
	if _, err := manager.ValidateSession(ctx, first.Token); err != nil {
		t.Fatalf("失败改密后当前 session 无效：%v", err)
	}
	if _, err := manager.ValidateSession(ctx, second.Token); err != nil {
		t.Fatalf("失败改密后其他 session 无效：%v", err)
	}

	if err := manager.ChangePassword(ctx, first.Token, testAdminPassword, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("短新密码错误 = %v，期望 ErrPasswordTooShort", err)
	}
	if _, err := manager.ValidateSession(ctx, second.Token); err != nil {
		t.Fatalf("短新密码失败后其他 session 无效：%v", err)
	}

	if err := manager.ChangePassword(ctx, "unknown-token", testAdminPassword, newPassword); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("无效当前 session 改密错误 = %v，期望 ErrInvalidSession", err)
	}
	if _, err := manager.ValidateSession(ctx, second.Token); err != nil {
		t.Fatalf("无效 session 改密后其他 session 无效：%v", err)
	}

	if err := manager.ChangePassword(ctx, first.Token, testAdminPassword, newPassword); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := manager.ValidateSession(ctx, first.Token); err != nil {
		t.Fatalf("改密后当前 session 应继续有效：%v", err)
	}
	if _, err := manager.ValidateSession(ctx, second.Token); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("改密后其他 session 错误 = %v，期望 ErrSessionRevoked", err)
	}

	if _, err := manager.Login(ctx, testSource, testAdminPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧密码登录错误 = %v，期望 ErrInvalidCredentials", err)
	}
	if _, err := manager.Login(ctx, testSource, newPassword); err != nil {
		t.Fatalf("新密码登录失败：%v", err)
	}

	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id = 1`).Scan(&hash); err != nil {
		t.Fatalf("读取密码哈希失败：%v", err)
	}
	if strings.Contains(hash, newPassword) {
		t.Fatal("数据库包含新密码明文")
	}
	if ok, err := VerifyPassword(hash, newPassword); err != nil || !ok {
		t.Fatalf("新密码哈希校验失败：ok=%v err=%v", ok, err)
	}
}

func TestListSessionsExcludesExpired(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)
	seedAdmin(t, db, testAdminPassword)

	start := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	now := setClock(t, manager, start)

	first := mustLogin(t, manager, testSource, testAdminPassword)
	*now = start.Add(DefaultSessionTTL / 2)
	second := mustLogin(t, manager, "10.0.0.2", testAdminPassword)
	*now = start.Add(DefaultSessionTTL)

	if _, err := manager.ValidateSession(ctx, first.Token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("过期 session 错误 = %v，期望 ErrSessionExpired", err)
	}
	sessions, err := manager.ListSessions(ctx, second.Token)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session 数 = %d，期望仅保留未过期 session", len(sessions))
	}
	if sessions[0].ID != second.SessionID || !sessions[0].Current {
		t.Fatalf("列表内容错误：%+v", sessions[0])
	}
}

func TestListAndRevokeSessions(t *testing.T) {
	ctx := context.Background()
	manager, db := newTestManager(t)
	seedAdmin(t, db, testAdminPassword)
	setClock(t, manager, time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC))

	first := mustLogin(t, manager, testSource, testAdminPassword)
	second := mustLogin(t, manager, testSource, testAdminPassword)

	sessions, err := manager.ListSessions(ctx, first.Token)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session 数 = %d，期望 2", len(sessions))
	}
	currentCount := 0
	for _, session := range sessions {
		if session.Current {
			currentCount++
			if session.ID != first.SessionID {
				t.Fatalf("当前 session ID = %q，期望 %q", session.ID, first.SessionID)
			}
		}
	}
	if currentCount != 1 {
		t.Fatalf("当前 session 标记数 = %d，期望 1", currentCount)
	}

	if err := manager.RevokeSession(ctx, second.SessionID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := manager.ValidateSession(ctx, second.Token); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("按 ID 撤销后错误 = %v，期望 ErrSessionRevoked", err)
	}
	sessions, err = manager.ListSessions(ctx, first.Token)
	if err != nil {
		t.Fatalf("再次 ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("撤销后 session 数 = %d，期望 1", len(sessions))
	}

	if err := manager.RevokeSession(ctx, second.SessionID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("重复撤销错误 = %v，期望 ErrInvalidSession", err)
	}
	if err := manager.RevokeSession(ctx, ""); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("空 session ID 错误 = %v，期望 ErrInvalidSession", err)
	}
	if _, err := manager.ListSessions(ctx, "unknown-token"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("未知 token 列表错误 = %v，期望 ErrInvalidSession", err)
	}

	if err := manager.Logout(ctx, first.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := manager.ListSessions(ctx, first.Token); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("登出后 ListSessions 错误 = %v，期望 ErrSessionRevoked", err)
	}
}

func TestNewManagerRejectsNilDatabase(t *testing.T) {
	if _, err := NewManager(nil); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("错误 = %v，期望 ErrNilDatabase", err)
	}
}
