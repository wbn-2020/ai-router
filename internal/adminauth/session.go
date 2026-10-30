package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	// SessionTokenSize 是 opaque session token 的随机字节数。
	SessionTokenSize = 32
	// SessionIDSize 是 session 对外 ID 的随机字节数。
	SessionIDSize = 16
	// DefaultSessionTTL 是 session 的绝对有效期。
	DefaultSessionTTL = 12 * time.Hour
	// MaxLoginFailures 是同一来源在一个窗口内允许的失败次数。
	MaxLoginFailures = 5
	// LoginFailureWindow 是登录失败计数的固定窗口。
	LoginFailureWindow = 15 * time.Minute
)

// sessionTimeLayout 与 0001 迁移中的时间格式保持一致。
const sessionTimeLayout = time.RFC3339Nano

// 全部 SQL 都使用参数绑定，不对调用方输入做字符串拼接。
const (
	selectCredentialHashSQL = `SELECT password_hash FROM admin_credentials WHERE id = 1`
	insertCredentialSQL     = `INSERT INTO admin_credentials (id, password_hash, created_at, updated_at) VALUES (1, ?, ?, ?)`
	updateCredentialSQL     = `UPDATE admin_credentials SET password_hash = ?, updated_at = ? WHERE id = 1`

	insertSessionSQL        = `INSERT INTO admin_sessions (id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`
	selectSessionByTokenSQL = `SELECT id, created_at, expires_at, revoked_at FROM admin_sessions WHERE token_hash = ?`
	selectActiveSessionsSQL = `SELECT id, created_at, expires_at FROM admin_sessions WHERE revoked_at IS NULL`
	revokeSessionByTokenSQL = `UPDATE admin_sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`
	revokeSessionByIDSQL    = `UPDATE admin_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`
	revokeOtherSessionsSQL  = `UPDATE admin_sessions SET revoked_at = ? WHERE token_hash <> ? AND revoked_at IS NULL`
)

var (
	// ErrNilDatabase 表示构造 Manager 时没有提供数据库连接。
	ErrNilDatabase = errors.New("adminauth: 数据库连接不能为空")
	// ErrAdminNotInitialized 表示尚无 admin_credentials 记录且未提供初始密码。
	ErrAdminNotInitialized = errors.New("adminauth: 尚无管理员凭据，请设置 GATEWAY_ADMIN_PASSWORD 完成首次初始化")
	// ErrInvalidCredentials 表示密码错误；调用方应统一映射为 401。
	ErrInvalidCredentials = errors.New("adminauth: 管理员密码错误")
	// ErrInvalidSession 表示 token 不存在。
	ErrInvalidSession = errors.New("adminauth: session 无效")
	// ErrSessionExpired 表示 session 已超过绝对过期时间。
	ErrSessionExpired = errors.New("adminauth: session 已过期")
	// ErrSessionRevoked 表示 session 已被登出、改密或其他操作撤销。
	ErrSessionRevoked = errors.New("adminauth: session 已撤销")
	// ErrRateLimited 表示来源在窗口内的失败次数已达上限。
	ErrRateLimited = errors.New("adminauth: 登录尝试过于频繁，请稍后再试")
)

// DB 是 adminauth 需要的数据库操作集合；*sql.DB 直接满足。
//
// 只暴露最小接口，便于 adminapi 注入 store.DB()，也便于测试使用临时 SQLite。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

var _ DB = (*sql.DB)(nil)

// Session 是对外可见的 session 元数据；不包含 token 或 token 哈希。
type Session struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
	// Current 仅在 ListSessions 中标记调用方当前使用的 session。
	Current bool
}

// LoginResult 是成功登录后返回给 HTTP 层的结果。
//
// Token 只在这里返回一次，数据库仅保存其 SHA-256。
type LoginResult struct {
	Token     string
	SessionID string
	ExpiresAt time.Time
}

// Manager 组合管理员凭据、session 与登录限流。
//
// Manager 可被并发使用；时间通过 now 注入，便于测试过期语义。
type Manager struct {
	db         DB
	limiter    *loginLimiter
	now        func() time.Time
	sessionTTL time.Duration
}

// NewManager 构造管理员鉴权管理器。
func NewManager(db DB) (*Manager, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	return &Manager{
		db:         db,
		limiter:    newLoginLimiter(LoginFailureWindow, MaxLoginFailures),
		now:        time.Now,
		sessionTTL: DefaultSessionTTL,
	}, nil
}

// EnsureAdmin 在数据库尚无管理员记录时用 initialPassword 创建管理员。
//
// initialPassword 由启动流程从 GATEWAY_ADMIN_PASSWORD 读取后传入；已有记录时
// 完全忽略该值（包括空值与长度），返回 created=false。无记录且 initialPassword
// 为空时返回 ErrAdminNotInitialized；过短则返回 ErrPasswordTooShort 且不写入
// 任何记录。
func (m *Manager) EnsureAdmin(ctx context.Context, initialPassword string) (bool, error) {
	var existing string
	err := m.db.QueryRowContext(ctx, selectCredentialHashSQL).Scan(&existing)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, sql.ErrNoRows):
		// 继续初始化。
	default:
		return false, fmt.Errorf("adminauth: 读取管理员凭据失败：%w", err)
	}

	if initialPassword == "" {
		return false, ErrAdminNotInitialized
	}
	hash, err := HashPassword(initialPassword)
	if err != nil {
		return false, err
	}

	now := formatSessionTime(m.now())
	if _, err := m.db.ExecContext(ctx, insertCredentialSQL, hash, now, now); err != nil {
		return false, fmt.Errorf("adminauth: 创建管理员凭据失败：%w", err)
	}
	return true, nil
}

// Login 校验密码并创建新的 session。
//
// 每次成功登录都会生成新的 token；同一来源在窗口内的尝试次数达到上限后，
// 直接返回 ErrRateLimited，不再执行密码校验。尝试名额在密码校验前占位，
// 并发请求也无法绕过限流。
func (m *Manager) Login(ctx context.Context, source, password string) (LoginResult, error) {
	now := m.now().UTC()
	if !m.limiter.beginAttempt(source, now) {
		return LoginResult{}, ErrRateLimited
	}

	var encoded string
	err := m.db.QueryRowContext(ctx, selectCredentialHashSQL).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		// 无管理员记录时也执行一次等价耗时的派生，避免时序上暴露初始化状态。
		verifyDummy(password)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("adminauth: 读取管理员凭据失败：%w", err)
	}

	ok, err := VerifyPassword(encoded, password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("adminauth: 校验管理员密码失败：%w", err)
	}
	if !ok {
		return LoginResult{}, ErrInvalidCredentials
	}

	result, err := m.createSession(ctx, now)
	if err != nil {
		return LoginResult{}, err
	}
	m.limiter.recordSuccess(source)
	return result, nil
}

// ValidateSession 校验 token 是否存在、未撤销且未过期。
func (m *Manager) ValidateSession(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrInvalidSession
	}

	session, revoked, err := m.findSession(ctx, token)
	if err != nil {
		return Session{}, err
	}
	if revoked {
		return Session{}, ErrSessionRevoked
	}
	if !m.now().Before(session.ExpiresAt) {
		return Session{}, ErrSessionExpired
	}
	return session, nil
}

// Logout 撤销 token 对应的 session。
//
// token 为空或已失效时静默成功，避免登出接口成为 token 探测入口。
func (m *Manager) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if _, err := m.db.ExecContext(ctx, revokeSessionByTokenSQL, formatSessionTime(m.now()), hashSessionToken(token)); err != nil {
		return fmt.Errorf("adminauth: 撤销 session 失败：%w", err)
	}
	return nil
}

// RevokeSession 按 session ID 撤销 session，供设置页的 session 管理使用。
//
// 目标不存在或已经撤销时返回 ErrInvalidSession。
func (m *Manager) RevokeSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return ErrInvalidSession
	}
	result, err := m.db.ExecContext(ctx, revokeSessionByIDSQL, formatSessionTime(m.now()), sessionID)
	if err != nil {
		return fmt.Errorf("adminauth: 撤销 session 失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauth: 读取撤销结果失败：%w", err)
	}
	if affected == 0 {
		return ErrInvalidSession
	}
	return nil
}

// ChangePassword 校验旧密码后更新管理员密码，并撤销除当前 session 外的全部 session。
//
// 旧密码错误返回 ErrInvalidCredentials；新密码不满足长度要求返回
// ErrPasswordTooShort，此时密码与 session 都不变。
func (m *Manager) ChangePassword(ctx context.Context, currentToken, oldPassword, newPassword string) error {
	if _, err := m.ValidateSession(ctx, currentToken); err != nil {
		return err
	}

	var encoded string
	err := m.db.QueryRowContext(ctx, selectCredentialHashSQL).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminNotInitialized
	}
	if err != nil {
		return fmt.Errorf("adminauth: 读取管理员凭据失败：%w", err)
	}

	ok, err := VerifyPassword(encoded, oldPassword)
	if err != nil {
		return fmt.Errorf("adminauth: 校验管理员密码失败：%w", err)
	}
	if !ok {
		return ErrInvalidCredentials
	}

	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	// 先完成耗时的密码派生，再用短事务更新凭据并撤销其他 session。
	now := formatSessionTime(m.now())
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adminauth: 开启事务失败：%w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	result, err := tx.ExecContext(ctx, updateCredentialSQL, newHash, now)
	if err != nil {
		return fmt.Errorf("adminauth: 更新管理员密码失败：%w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauth: 读取更新结果失败：%w", err)
	}
	if affected != 1 {
		return ErrAdminNotInitialized
	}

	if _, err := tx.ExecContext(ctx, revokeOtherSessionsSQL, now, hashSessionToken(currentToken)); err != nil {
		return fmt.Errorf("adminauth: 撤销其他 session 失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adminauth: 提交事务失败：%w", err)
	}
	return nil
}

// ListSessions 返回当前未撤销且未过期的 session，并标记当前 session。
//
// currentToken 必须有效，否则返回对应 session 错误。
func (m *Manager) ListSessions(ctx context.Context, currentToken string) ([]Session, error) {
	current, err := m.ValidateSession(ctx, currentToken)
	if err != nil {
		return nil, err
	}

	rows, err := m.db.QueryContext(ctx, selectActiveSessionsSQL)
	if err != nil {
		return nil, fmt.Errorf("adminauth: 读取 session 列表失败：%w", err)
	}
	defer rows.Close()

	now := m.now()
	var sessions []Session
	for rows.Next() {
		var (
			session    Session
			createdRaw string
			expiresRaw string
		)
		if err := rows.Scan(&session.ID, &createdRaw, &expiresRaw); err != nil {
			return nil, fmt.Errorf("adminauth: 解析 session 列表失败：%w", err)
		}
		if session.CreatedAt, err = parseSessionTime(createdRaw); err != nil {
			return nil, err
		}
		if session.ExpiresAt, err = parseSessionTime(expiresRaw); err != nil {
			return nil, err
		}
		if !now.Before(session.ExpiresAt) {
			continue
		}
		session.Current = session.ID == current.ID
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminauth: 遍历 session 列表失败：%w", err)
	}

	// 在 Go 侧排序，避免 RFC3339Nano 文本比较在可变小数位下出错。
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})
	return sessions, nil
}

func (m *Manager) createSession(ctx context.Context, now time.Time) (LoginResult, error) {
	token, err := randomBase64(SessionTokenSize)
	if err != nil {
		return LoginResult{}, fmt.Errorf("adminauth: 生成 session token 失败：%w", err)
	}
	id, err := randomHex(SessionIDSize)
	if err != nil {
		return LoginResult{}, fmt.Errorf("adminauth: 生成 session ID 失败：%w", err)
	}

	expiresAt := now.Add(m.sessionTTL)
	if _, err := m.db.ExecContext(
		ctx,
		insertSessionSQL,
		id,
		hashSessionToken(token),
		formatSessionTime(now),
		formatSessionTime(expiresAt),
	); err != nil {
		return LoginResult{}, fmt.Errorf("adminauth: 创建 session 失败：%w", err)
	}

	return LoginResult{Token: token, SessionID: id, ExpiresAt: expiresAt}, nil
}

// findSession 按 token 查询 session；revoked 表示 revoked_at 非空。
func (m *Manager) findSession(ctx context.Context, token string) (Session, bool, error) {
	var (
		session    Session
		createdRaw string
		expiresRaw string
		revokedAt  sql.NullString
	)
	err := m.db.QueryRowContext(ctx, selectSessionByTokenSQL, hashSessionToken(token)).Scan(
		&session.ID,
		&createdRaw,
		&expiresRaw,
		&revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, ErrInvalidSession
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("adminauth: 读取 session 失败：%w", err)
	}

	if session.CreatedAt, err = parseSessionTime(createdRaw); err != nil {
		return Session{}, false, err
	}
	if session.ExpiresAt, err = parseSessionTime(expiresRaw); err != nil {
		return Session{}, false, err
	}
	return session, revokedAt.Valid, nil
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomBase64(size int) (string, error) {
	buf, err := randomBytes(size)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomHex(size int) (string, error) {
	buf, err := randomBytes(size)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func randomBytes(size int) ([]byte, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func formatSessionTime(t time.Time) string {
	return t.UTC().Format(sessionTimeLayout)
}

func parseSessionTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(sessionTimeLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("adminauth: 解析 session 时间失败：%w", err)
	}
	return parsed, nil
}
