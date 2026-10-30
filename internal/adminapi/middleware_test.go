package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriteRequestsRequireSameOrigin(t *testing.T) {
	api, st := newTestAPI(t)

	cross := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, "http://evil.example")
	if cross.Code != http.StatusForbidden {
		t.Fatalf("跨源登录 = %d, want 403（body=%s）", cross.Code, cross.Body.String())
	}
	if !strings.Contains(cross.Body.String(), "cross_origin_request") {
		t.Errorf("跨源响应缺少稳定错误码：%s", cross.Body.String())
	}
	if cookie := findSetCookie(cross, sessionCookieName); cookie != nil {
		t.Error("跨源登录不应签发 cookie")
	}
	var sessions int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&sessions); err != nil {
		t.Fatalf("统计 session: %v", err)
	}
	if sessions != 0 {
		t.Errorf("跨源登录创建了 %d 个 session, want 0", sessions)
	}
	assertNoStore(t, cross)

	missing := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, "")
	if missing.Code != http.StatusForbidden {
		t.Fatalf("缺少 Origin 的登录 = %d, want 403（body=%s）", missing.Code, missing.Body.String())
	}

	same := apiRequest(t, api, http.MethodPost, "/admin/api/auth/login",
		loginRequest{Password: testAdminPassword}, testOrigin)
	if same.Code != http.StatusOK {
		t.Fatalf("同源登录 = %d, want 200（body=%s）", same.Code, same.Body.String())
	}

	cookie := loginCookie(t, api, testAdminPassword)
	crossLogout := apiRequest(t, api, http.MethodPost, "/admin/api/auth/logout", nil,
		"http://evil.example", cookie)
	if crossLogout.Code != http.StatusForbidden {
		t.Fatalf("跨源登出 = %d, want 403", crossLogout.Code)
	}
	if rec := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", cookie); rec.Code != http.StatusOK {
		t.Errorf("跨源登出不应撤销 session，GET session = %d", rec.Code)
	}
}

func TestWriteMethodsAllRequireOrigin(t *testing.T) {
	api, _ := newTestAPI(t)

	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	} {
		rec := apiRequest(t, api, method, "/admin/api/auth/login", nil, "http://evil.example")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s 跨源请求 = %d, want 403", method, rec.Code)
		}
	}

	// 安全方法不要求 Origin；未登录返回 401 而不是 403。
	safe := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "http://evil.example")
	if safe.Code != http.StatusUnauthorized {
		t.Errorf("跨源 GET session = %d, want 401", safe.Code)
	}
}

func TestSameOriginNormalization(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"完全一致", "example.com", "http://example.com", true},
		{"TLS 代理 scheme 不同", "example.com", "https://example.com", true},
		{"主机大小写", "example.com", "http://EXAMPLE.com", true},
		{"Host 默认端口 80", "example.com:80", "http://example.com", true},
		{"双方默认端口 443", "example.com:443", "https://example.com:443", true},
		{"非默认端口一致", "example.com:8080", "http://example.com:8080", true},
		{"Origin 带非默认端口", "example.com", "http://example.com:8080", false},
		{"Host 带非默认端口", "example.com:8080", "http://example.com", false},
		{"不同主机", "example.com", "http://evil.example", false},
		{"缺少 Origin", "example.com", "", false},
		{"Origin null", "example.com", "null", false},
		{"非 http(s) scheme", "example.com", "ftp://example.com", false},
		{"带用户信息", "example.com", "http://user@example.com", false},
		{"带路径", "example.com", "http://example.com/admin", false},
		{"IPv6 一致", "[::1]:8080", "http://[::1]:8080", true},
		{"IPv6 端口不同", "[::1]:8080", "http://[::1]:9090", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://placeholder.invalid/admin/api/auth/login", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if got := sameOrigin(req); got != tt.want {
				t.Errorf("sameOrigin(host=%q, origin=%q) = %t, want %t",
					tt.host, tt.origin, got, tt.want)
			}
		})
	}
}

func TestSessionMiddlewareRejectsInvalidSessions(t *testing.T) {
	api, st := newTestAPI(t)

	missing := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "")
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("无 cookie GET session = %d, want 401（body=%s）", missing.Code, missing.Body.String())
	}
	if cookie := findSetCookie(missing, sessionCookieName); cookie != nil {
		t.Error("401 响应不应改写 cookie（GET 不应有副作用）")
	}
	assertNoStore(t, missing)

	unknown := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "",
		&http.Cookie{Name: sessionCookieName, Value: "not-a-real-token"})
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("未知 token GET session = %d, want 401", unknown.Code)
	}

	expiredCookie := loginCookie(t, api, testAdminPassword)
	expiredID := currentSessionID(t, api, expiredCookie)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(
		`UPDATE admin_sessions SET expires_at = ? WHERE id = ?`, past, expiredID,
	); err != nil {
		t.Fatalf("设置过期 session: %v", err)
	}
	expired := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", expiredCookie)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("过期 session = %d, want 401（body=%s）", expired.Code, expired.Body.String())
	}
	if strings.Contains(expired.Body.String(), expiredCookie.Value) {
		t.Error("过期 session 响应泄漏 token")
	}

	revokedCookie := loginCookie(t, api, testAdminPassword)
	revokedID := currentSessionID(t, api, revokedCookie)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(
		`UPDATE admin_sessions SET revoked_at = ? WHERE id = ?`, now, revokedID,
	); err != nil {
		t.Fatalf("撤销 session: %v", err)
	}
	revoked := apiRequest(t, api, http.MethodGet, "/admin/api/auth/session", nil, "", revokedCookie)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("已撤销 session = %d, want 401（body=%s）", revoked.Code, revoked.Body.String())
	}
}

func TestAdminUnknownPathReturnsJSON(t *testing.T) {
	api, _ := newTestAPI(t)

	rec := apiRequest(t, api, http.MethodGet, "/admin/api/unknown", nil, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知路径 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("未知路径 Content-Type = %q, want application/json", got)
	}
	if !strings.Contains(rec.Body.String(), "not_found") {
		t.Errorf("未知路径响应缺少 not_found：%s", rec.Body.String())
	}
	assertNoStore(t, rec)

	post := apiRequest(t, api, http.MethodPost, "/admin/api/unknown", nil, testOrigin)
	if post.Code != http.StatusNotFound {
		t.Errorf("同源未知 POST = %d, want 404", post.Code)
	}
}
