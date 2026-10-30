package adminapi

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-router/internal/adminauth"
	"ai-router/internal/store"
)

const (
	testAdminPassword = "correct-horse-battery"
	testNewPassword   = "new-correct-horse-battery"
	testShortPassword = "short-pass"
	testOrigin        = "http://example.com"
	testHost          = "example.com"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestEnv 构造带迁移的临时 SQLite 与 adminapi。
//
// seedAdmin=false 用于验证“管理员尚未初始化”与错误密码返回相同响应。
func newTestEnv(t *testing.T, seedAdmin bool, mutate ...func(*Options)) (*API, *store.Store) {
	t.Helper()

	dir := t.TempDir()
	st, err := store.Open(store.Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	if seedAdmin {
		if err := seedAdminHash(st.DB(), testAdminPassword); err != nil {
			t.Fatalf("写入管理员凭据: %v", err)
		}
	}

	manager, err := adminauth.NewManager(st.DB())
	if err != nil {
		t.Fatalf("adminauth.NewManager: %v", err)
	}
	opts := Options{
		Auth:   manager,
		DB:     st.DB(),
		Logger: discardLogger(),
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	api, err := New(opts)
	if err != nil {
		t.Fatalf("adminapi.New: %v", err)
	}
	return api, st
}

func newTestAPI(t *testing.T, mutate ...func(*Options)) (*API, *store.Store) {
	t.Helper()
	return newTestEnv(t, true, mutate...)
}

// seedAdminHash 写入与 adminauth 相同编码的 PBKDF2 哈希，但使用低成本迭代，
// 避免每个测试都支付 600k 次派生；VerifyPassword 仍从编码读取参数。
func seedAdminHash(db *sql.DB, password string) error {
	salt := []byte("adminapi-test-salt")
	key, err := pbkdf2.Key(sha256.New, password, salt, 1000, adminauth.PasswordKeySize)
	if err != nil {
		return err
	}
	encoded := fmt.Sprintf(
		"%s$1000$%s$%s",
		adminauth.PasswordHashAlgorithm,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(
		`INSERT INTO admin_credentials (id, password_hash, created_at, updated_at) VALUES (1, ?, ?, ?)`,
		encoded, now, now,
	)
	return err
}

// apiRequest 通过 API.Handler 发送请求；默认 Host 为 example.com。
func apiRequest(
	t *testing.T,
	api *API,
	method, path string,
	body any,
	origin string,
	cookies ...*http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://"+testHost+path, reader)
	req.RemoteAddr = "192.0.2.10:12345"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	return rec
}

func findSetCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func loginCookie(t *testing.T, api *API, password string) *http.Cookie {
	t.Helper()
	rec := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: password}, testOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	cookie := findSetCookie(rec, sessionCookieName)
	if cookie == nil {
		t.Fatal("登录响应缺少 session cookie")
	}
	return &http.Cookie{Name: cookie.Name, Value: cookie.Value}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("解析响应 JSON: %v（body=%s）", err, rec.Body.String())
	}
	return value
}

func assertNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", got)
	}
}

func currentSessionID(t *testing.T, api *API, cookie *http.Cookie) string {
	t.Helper()
	rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET session = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	return decodeBody[sessionEnvelope](t, rec).Session.ID
}

func TestLoginSessionLogout(t *testing.T) {
	api, st := newTestAPI(t)

	wrong := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: "not-the-password"}, testOrigin)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码登录 = %d, want 401（body=%s）", wrong.Code, wrong.Body.String())
	}
	if cookie := findSetCookie(wrong, sessionCookieName); cookie != nil {
		t.Errorf("错误密码响应不应设置 cookie：%+v", cookie)
	}
	if strings.Contains(wrong.Body.String(), "not-the-password") {
		t.Errorf("错误响应泄漏密码：%s", wrong.Body.String())
	}
	if !strings.Contains(wrong.Body.String(), "invalid_credentials") {
		t.Errorf("错误响应缺少稳定错误码：%s", wrong.Body.String())
	}
	assertNoStore(t, wrong)

	login := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, testOrigin)
	if login.Code != http.StatusOK {
		t.Fatalf("登录 = %d, want 200（body=%s）", login.Code, login.Body.String())
	}
	cookie := findSetCookie(login, sessionCookieName)
	if cookie == nil {
		t.Fatal("登录响应缺少 session cookie")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie 不是 HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("session cookie Path = %q, want /", cookie.Path)
	}
	wantMaxAge := int(adminauth.DefaultSessionTTL / time.Second)
	if cookie.MaxAge != wantMaxAge {
		t.Errorf("session cookie MaxAge = %d, want %d", cookie.MaxAge, wantMaxAge)
	}
	if cookie.Secure {
		t.Error("默认配置下 session cookie 不应带 Secure")
	}
	assertNoStore(t, login)

	// 数据库只保存 token 的 SHA-256，不保存明文 token。
	var tokenHash string
	if err := st.DB().QueryRow(`SELECT token_hash FROM admin_sessions`).Scan(&tokenHash); err != nil {
		t.Fatalf("读取 token_hash: %v", err)
	}
	if tokenHash == cookie.Value {
		t.Fatal("数据库保存了明文 session token")
	}
	if len(tokenHash) != sha256.Size*2 {
		t.Errorf("token_hash 长度 = %d, want %d", len(tokenHash), sha256.Size*2)
	}

	clientCookie := &http.Cookie{Name: cookie.Name, Value: cookie.Value}
	sessionRec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", clientCookie)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("GET session = %d, want 200（body=%s）", sessionRec.Code, sessionRec.Body.String())
	}
	envelope := decodeBody[sessionEnvelope](t, sessionRec)
	if !envelope.Authenticated {
		t.Error("GET session authenticated = false, want true")
	}
	if envelope.Admin.ID != 1 || envelope.Admin.Role != "admin" {
		t.Errorf("管理员身份 = %+v, want id=1 role=admin", envelope.Admin)
	}
	if envelope.Session.ID == "" || !envelope.Session.Current {
		t.Errorf("当前 session = %+v, want 非空且 current", envelope.Session)
	}
	if strings.Contains(sessionRec.Body.String(), cookie.Value) {
		t.Error("session 响应泄漏明文 token")
	}
	assertNoStore(t, sessionRec)

	logout := apiRequest(t, api, http.MethodPost, "/admin/api/auth/logout", nil, testOrigin, clientCookie)
	if logout.Code != http.StatusOK {
		t.Fatalf("登出 = %d, want 200（body=%s）", logout.Code, logout.Body.String())
	}
	cleared := findSetCookie(logout, sessionCookieName)
	if cleared == nil {
		t.Fatal("登出响应缺少清除 cookie")
	}
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("登出 cookie = %+v, want 空值且 MaxAge<0", cleared)
	}

	after := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", clientCookie)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("登出后 GET session = %d, want 401（body=%s）", after.Code, after.Body.String())
	}
}

func TestSecureCookieFlag(t *testing.T) {
	api, _ := newTestAPI(t, func(opts *Options) {
		opts.CookieSecure = true
	})

	rec := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, testOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	cookie := findSetCookie(rec, sessionCookieName)
	if cookie == nil || !cookie.Secure {
		t.Fatalf("CookieSecure=true 时 session cookie 必须带 Secure：%+v", cookie)
	}

	clientCookie := &http.Cookie{Name: cookie.Name, Value: cookie.Value}
	logout := apiRequest(t, api, http.MethodPost, "/admin/api/auth/logout", nil, testOrigin, clientCookie)
	cleared := findSetCookie(logout, sessionCookieName)
	if cleared == nil || !cleared.Secure {
		t.Fatalf("清除 cookie 也必须保持 Secure：%+v", cleared)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	var logs bytes.Buffer
	api, st := newTestAPI(t, func(opts *Options) {
		opts.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
	})

	first := loginCookie(t, api, testAdminPassword)
	second := loginCookie(t, api, testAdminPassword)
	if first.Value == second.Value {
		t.Fatal("两次登录得到相同 token")
	}

	wrongCurrent := apiRequest(t, api, http.MethodPost, "/admin/api/auth/password",
		changePasswordRequest{
			CurrentPassword: "wrong-current-password",
			NewPassword:     testNewPassword,
		}, testOrigin, first)
	if wrongCurrent.Code != http.StatusBadRequest {
		t.Fatalf("错误当前密码 = %d, want 400（body=%s）", wrongCurrent.Code, wrongCurrent.Body.String())
	}
	if !strings.Contains(wrongCurrent.Body.String(), "invalid_current_password") {
		t.Errorf("错误当前密码响应缺少稳定错误码：%s", wrongCurrent.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", second); rec.Code != http.StatusOK {
		t.Errorf("错误当前密码不应撤销其他 session，GET session = %d", rec.Code)
	}

	tooShort := apiRequest(t, api, http.MethodPost, "/admin/api/auth/password",
		changePasswordRequest{
			CurrentPassword: testAdminPassword,
			NewPassword:     testShortPassword,
		}, testOrigin, first)
	if tooShort.Code != http.StatusBadRequest {
		t.Fatalf("过短新密码 = %d, want 400（body=%s）", tooShort.Code, tooShort.Body.String())
	}
	if !strings.Contains(tooShort.Body.String(), "password_too_short") {
		t.Errorf("过短新密码响应缺少稳定错误码：%s", tooShort.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", second); rec.Code != http.StatusOK {
		t.Errorf("过短新密码不应撤销其他 session，GET session = %d", rec.Code)
	}

	changed := apiRequest(t, api, http.MethodPost, "/admin/api/auth/password",
		changePasswordRequest{
			CurrentPassword: testAdminPassword,
			NewPassword:     testNewPassword,
		}, testOrigin, first)
	if changed.Code != http.StatusOK {
		t.Fatalf("改密 = %d, want 200（body=%s）", changed.Code, changed.Body.String())
	}

	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", first); rec.Code != http.StatusOK {
		t.Errorf("改密后当前 session = %d, want 200", rec.Code)
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", second); rec.Code != http.StatusUnauthorized {
		t.Errorf("改密后其他 session = %d, want 401（body=%s）", rec.Code, rec.Body.String())
	}

	var hash string
	if err := st.DB().QueryRow(`SELECT password_hash FROM admin_credentials WHERE id = 1`).Scan(&hash); err != nil {
		t.Fatalf("读取密码哈希: %v", err)
	}
	if strings.Contains(hash, testAdminPassword) || strings.Contains(hash, testNewPassword) {
		t.Fatalf("数据库密码哈希包含明文：%s", hash)
	}
	wantPrefix := fmt.Sprintf("%s$%d$", adminauth.PasswordHashAlgorithm, adminauth.PasswordHashIterations)
	if !strings.HasPrefix(hash, wantPrefix) {
		t.Errorf("密码哈希前缀 = %q, want %q", hash, wantPrefix)
	}

	oldLogin := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, testOrigin)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Errorf("旧密码登录 = %d, want 401", oldLogin.Code)
	}
	newLogin := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testNewPassword}, testOrigin)
	if newLogin.Code != http.StatusOK {
		t.Errorf("新密码登录 = %d, want 200（body=%s）", newLogin.Code, newLogin.Body.String())
	}

	logOutput := logs.String()
	for _, secret := range []string{testAdminPassword, testNewPassword, "wrong-current-password"} {
		if strings.Contains(logOutput, secret) {
			t.Errorf("日志包含密码明文 %q：%s", secret, logOutput)
		}
	}
}

func TestSessionListAndRevoke(t *testing.T) {
	api, _ := newTestAPI(t)

	first := loginCookie(t, api, testAdminPassword)
	second := loginCookie(t, api, testAdminPassword)
	third := loginCookie(t, api, testAdminPassword)

	firstID := currentSessionID(t, api, first)
	secondID := currentSessionID(t, api, second)
	thirdID := currentSessionID(t, api, third)
	if firstID == secondID || firstID == thirdID || secondID == thirdID {
		t.Fatalf("session ID 不唯一：%q %q %q", firstID, secondID, thirdID)
	}

	list := apiRequest(t, api, http.MethodGet, "/admin/api/auth/sessions", nil, "", first)
	if list.Code != http.StatusOK {
		t.Fatalf("session 列表 = %d, want 200（body=%s）", list.Code, list.Body.String())
	}
	envelope := decodeBody[sessionsEnvelope](t, list)
	if len(envelope.Sessions) != 3 {
		t.Fatalf("session 数 = %d, want 3", len(envelope.Sessions))
	}
	currentCount := 0
	for _, session := range envelope.Sessions {
		if session.Current {
			currentCount++
			if session.ID != firstID {
				t.Errorf("current session = %q, want %q", session.ID, firstID)
			}
		}
	}
	if currentCount != 1 {
		t.Errorf("current session 数量 = %d, want 1", currentCount)
	}
	if strings.Contains(list.Body.String(), first.Value) {
		t.Error("session 列表泄漏明文 token")
	}
	assertNoStore(t, list)

	revokeSecond := apiRequest(t, api, http.MethodDelete,
		"/admin/api/auth/sessions/"+secondID, nil, testOrigin, first)
	if revokeSecond.Code != http.StatusOK {
		t.Fatalf("撤销单个 session = %d, want 200（body=%s）", revokeSecond.Code, revokeSecond.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", second); rec.Code != http.StatusUnauthorized {
		t.Errorf("被撤销 session = %d, want 401", rec.Code)
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", first); rec.Code != http.StatusOK {
		t.Errorf("当前 session = %d, want 200", rec.Code)
	}

	revokeOthers := apiRequest(t, api, http.MethodDelete,
		"/admin/api/auth/sessions", nil, testOrigin, first)
	if revokeOthers.Code != http.StatusOK {
		t.Fatalf("撤销其他 session = %d, want 200（body=%s）", revokeOthers.Code, revokeOthers.Body.String())
	}
	result := decodeBody[revokeResponse](t, revokeOthers)
	if result.Revoked != 1 {
		t.Errorf("撤销数量 = %d, want 1", result.Revoked)
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", third); rec.Code != http.StatusUnauthorized {
		t.Errorf("批量撤销后 session = %d, want 401", rec.Code)
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", first); rec.Code != http.StatusOK {
		t.Errorf("批量撤销后当前 session = %d, want 200", rec.Code)
	}

	unknown := apiRequest(t, api, http.MethodDelete,
		"/admin/api/auth/sessions/does-not-exist", nil, testOrigin, first)
	if unknown.Code != http.StatusNotFound {
		t.Errorf("撤销不存在的 session = %d, want 404（body=%s）", unknown.Code, unknown.Body.String())
	}
}

func TestLastSeenRecorded(t *testing.T) {
	api, st := newTestAPI(t)
	cookie := loginCookie(t, api, testAdminPassword)

	sessionRec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", cookie)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("GET session = %d, want 200", sessionRec.Code)
	}
	session := decodeBody[sessionEnvelope](t, sessionRec).Session
	if session.LastSeenAt == nil {
		t.Fatal("GET session 未返回 last_seen_at")
	}

	var lastSeen sql.NullString
	if err := st.DB().QueryRow(
		`SELECT last_seen_at FROM admin_sessions WHERE id = ?`, session.ID,
	).Scan(&lastSeen); err != nil {
		t.Fatalf("读取 last_seen_at: %v", err)
	}
	if !lastSeen.Valid || lastSeen.String == "" {
		t.Fatal("数据库未记录 last_seen_at")
	}

	list := apiRequest(t, api, http.MethodGet, "/admin/api/auth/sessions", nil, "", cookie)
	envelope := decodeBody[sessionsEnvelope](t, list)
	if len(envelope.Sessions) != 1 || envelope.Sessions[0].LastSeenAt == nil {
		t.Fatalf("session 列表缺少 last_seen_at：%+v", envelope.Sessions)
	}
}

func TestLoginRateLimited(t *testing.T) {
	api, _ := newTestAPI(t)

	for i := 0; i < adminauth.MaxLoginFailures; i++ {
		rec := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
			loginRequest{Password: "wrong-password"}, testOrigin)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码 = %d, want 401", i+1, rec.Code)
		}
	}

	blocked := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: "wrong-password"}, testOrigin)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("超限登录 = %d, want 429（body=%s）", blocked.Code, blocked.Body.String())
	}
	if !strings.Contains(blocked.Body.String(), "rate_limited") {
		t.Errorf("限流响应缺少稳定错误码：%s", blocked.Body.String())
	}
	retryAfter, err := strconv.Atoi(blocked.Header().Get("Retry-After"))
	if err != nil || retryAfter <= 0 {
		t.Errorf("Retry-After = %q, want 正整数秒", blocked.Header().Get("Retry-After"))
	}
	if cookie := findSetCookie(blocked, sessionCookieName); cookie != nil {
		t.Error("限流响应不应设置 cookie")
	}
	assertNoStore(t, blocked)

	// 即使密码正确，窗口内达到上限后也必须继续限流。
	correct := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, testOrigin)
	if correct.Code != http.StatusTooManyRequests {
		t.Errorf("限流期间正确密码 = %d, want 429", correct.Code)
	}
}

func TestLoginDoesNotRevealAdminInitialization(t *testing.T) {
	uninitialized, _ := newTestEnv(t, false)
	seeded, _ := newTestAPI(t)

	uninitializedRec := apiRequest(t, uninitialized, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: "some-wrong-password"}, testOrigin)
	seededRec := apiRequest(t, seeded, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: "some-wrong-password"}, testOrigin)

	if uninitializedRec.Code != http.StatusUnauthorized || seededRec.Code != http.StatusUnauthorized {
		t.Fatalf("未初始化/错误密码状态码 = %d/%d, want 401/401",
			uninitializedRec.Code, seededRec.Code)
	}
	if uninitializedRec.Body.String() != seededRec.Body.String() {
		t.Errorf("未初始化与错误密码响应不同，可能泄漏初始化状态：%q vs %q",
			uninitializedRec.Body.String(), seededRec.Body.String())
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	api, _ := newTestAPI(t)

	rec := apiRequest(t, api, http.MethodPost, "/admin/api/auth/logout", nil, testOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("无 session 登出 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	if cookie := findSetCookie(rec, sessionCookieName); cookie == nil {
		t.Error("登出响应缺少清除 cookie")
	}
}

func TestSessionListRequiresSession(t *testing.T) {
	api, _ := newTestAPI(t)

	rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/sessions", nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 session 列表 = %d, want 401（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
}
