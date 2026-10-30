package server_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-router/internal/server"
	"ai-router/internal/store"
	"ai-router/internal/webassets"
)

const (
	requestIDHeader = "X-Request-ID"

	reasonShuttingDown        = "shutting_down"
	reasonMasterKeyNotLoaded  = "master_key_not_loaded"
	reasonDatabaseUnavailable = "database_unavailable"
	reasonMigrationPending    = "migration_pending"
)

type healthBody struct {
	Status string `json:"status"`
}

type readinessBody struct {
	Status                string `json:"status"`
	Reason                string `json:"reason"`
	SchemaVersion         *int   `json:"schema_version"`
	RequiredSchemaVersion int    `json:"required_schema_version"`
}

func openTestStore(t *testing.T, migrate bool) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	if migrate {
		if _, err := st.Migrate(context.Background()); err != nil {
			t.Fatalf("store.Migrate: %v", err)
		}
	}
	return st
}

func newTestServer(t *testing.T, opts server.Options) *server.Server {
	t.Helper()
	if opts.ListenAddr == "" {
		opts.ListenAddr = "127.0.0.1:0"
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s, err := server.New(opts)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return s
}

func serveRequest(handler http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeReadiness(t *testing.T, rec *httptest.ResponseRecorder) readinessBody {
	t.Helper()
	var body readinessBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 readiness 响应失败: %v（body=%q）", err, rec.Body.String())
	}
	return body
}

func localHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   5 * time.Second,
	}
}

func embeddedSchemaVersion(t *testing.T) int {
	t.Helper()
	migrations, err := store.EmbeddedMigrations()
	if err != nil {
		t.Fatalf("store.EmbeddedMigrations: %v", err)
	}
	version := 0
	for _, migration := range migrations {
		if migration.Version > version {
			version = migration.Version
		}
	}
	return version
}

func TestHealthzAlwaysOK(t *testing.T) {
	// 没有数据库、主密钥未加载时，存活探针仍应为 200。
	s := newTestServer(t, server.Options{})

	rec := serveRequest(s.Handler(), http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200（body=%q）", rec.Code, rec.Body.String())
	}
	var body healthBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 /healthz 响应失败: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("/healthz status = %q, want ok", body.Status)
	}

	rec = serveRequest(s.Handler(), http.MethodPost, "/healthz", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("POST /healthz Allow = %q, want \"GET, HEAD\"", got)
	}
}

func TestReadyzLifecycle(t *testing.T) {
	wantVersion := embeddedSchemaVersion(t)
	st := openTestStore(t, false)
	s := newTestServer(t, server.Options{Store: st, MasterKeyLoaded: true})
	handler := s.Handler()

	rec := serveRequest(handler, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("迁移前 /readyz = %d, want 503（body=%q）", rec.Code, rec.Body.String())
	}
	body := decodeReadiness(t, rec)
	if body.Reason != reasonMigrationPending {
		t.Errorf("迁移前 reason = %q, want %q", body.Reason, reasonMigrationPending)
	}
	if body.SchemaVersion == nil || *body.SchemaVersion != 0 {
		t.Errorf("迁移前 schema_version = %v, want 0", body.SchemaVersion)
	}
	if body.RequiredSchemaVersion != wantVersion {
		t.Errorf("required_schema_version = %d, want %d", body.RequiredSchemaVersion, wantVersion)
	}

	if rec := serveRequest(handler, http.MethodGet, "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("迁移前 /healthz = %d, want 200", rec.Code)
	}

	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	rec = serveRequest(handler, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("迁移后 /readyz = %d, want 200（body=%q）", rec.Code, rec.Body.String())
	}
	body = decodeReadiness(t, rec)
	if body.Status != "ready" {
		t.Errorf("迁移后 status = %q, want ready", body.Status)
	}
	if body.SchemaVersion == nil || *body.SchemaVersion != wantVersion {
		t.Errorf("迁移后 schema_version = %v, want %d", body.SchemaVersion, wantVersion)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库: %v", err)
	}
	rec = serveRequest(handler, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("关闭数据库后 /readyz = %d, want 503", rec.Code)
	}
	body = decodeReadiness(t, rec)
	if body.Reason != reasonDatabaseUnavailable {
		t.Errorf("关闭数据库后 reason = %q, want %q", body.Reason, reasonDatabaseUnavailable)
	}
	if rec := serveRequest(handler, http.MethodGet, "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("关闭数据库后 /healthz = %d, want 200", rec.Code)
	}
}

func TestReadyzRequiresMasterKey(t *testing.T) {
	st := openTestStore(t, true)
	s := newTestServer(t, server.Options{Store: st, MasterKeyLoaded: false})

	rec := serveRequest(s.Handler(), http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503", rec.Code)
	}
	if body := decodeReadiness(t, rec); body.Reason != reasonMasterKeyNotLoaded {
		t.Errorf("reason = %q, want %q", body.Reason, reasonMasterKeyNotLoaded)
	}
}

func TestReadyzDuringShutdown(t *testing.T) {
	st := openTestStore(t, true)
	s := newTestServer(t, server.Options{Store: st, MasterKeyLoaded: true})

	if rec := serveRequest(s.Handler(), http.MethodGet, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("退出前 /readyz = %d, want 200", rec.Code)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	rec := serveRequest(s.Handler(), http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("退出中 /readyz = %d, want 503", rec.Code)
	}
	if body := decodeReadiness(t, rec); body.Reason != reasonShuttingDown {
		t.Errorf("reason = %q, want %q", body.Reason, reasonShuttingDown)
	}
}

func TestAdminSPAAndFallback(t *testing.T) {
	index, err := webassets.Index()
	if err != nil {
		t.Fatalf("webassets.Index: %v", err)
	}

	s := newTestServer(t, server.Options{})
	handler := s.Handler()

	for _, target := range []string{"/admin", "/admin/", "/admin/channels/42", "/admin/settings?tab=session"} {
		rec := serveRequest(handler, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", target, rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), index) {
			t.Errorf("GET %s 未返回嵌入的 index.html", target)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html", target, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", target, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
			t.Errorf("GET %s CSP = %q, want default-src 'self'", target, got)
		}
	}

	head := serveRequest(handler, http.MethodHead, "/admin", nil)
	if head.Code != http.StatusOK {
		t.Errorf("HEAD /admin = %d, want 200", head.Code)
	}
	if head.Body.Len() != 0 {
		t.Errorf("HEAD /admin body 长度 = %d, want 0", head.Body.Len())
	}

	post := serveRequest(handler, http.MethodPost, "/admin", nil)
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /admin = %d, want 405", post.Code)
	}
	if got := post.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("POST /admin Allow = %q, want \"GET, HEAD\"", got)
	}

	if rec := serveRequest(handler, http.MethodGet, "/", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404", rec.Code)
	}
	if rec := serveRequest(handler, http.MethodGet, "/assets/missing.js", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /assets/missing.js = %d, want 404", rec.Code)
	}
	api := serveRequest(handler, http.MethodGet, "/admin/api/unknown", nil)
	if api.Code != http.StatusNotFound {
		t.Errorf("GET /admin/api/unknown = %d, want 404", api.Code)
	}
	if got := api.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("GET /admin/api/unknown Content-Type = %q, want application/json", got)
	}
}

func TestRequestIDAndSecurityHeaders(t *testing.T) {
	s := newTestServer(t, server.Options{})
	s.Handle("/rid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, server.RequestIDFromContext(r.Context()))
	}))

	rec := serveRequest(s.Handler(), http.MethodGet, "/rid", nil)
	requestID := rec.Header().Get(requestIDHeader)
	if requestID == "" {
		t.Fatal("响应缺少 X-Request-ID")
	}
	if decoded, err := hex.DecodeString(requestID); err != nil || len(decoded) != 16 {
		t.Errorf("X-Request-ID = %q, want 32 位 hex", requestID)
	}
	if got := rec.Body.String(); got != requestID {
		t.Errorf("context request ID = %q, 响应头 = %q", got, requestID)
	}

	second := serveRequest(s.Handler(), http.MethodGet, "/rid", nil)
	if same := second.Header().Get(requestIDHeader); same == requestID {
		t.Errorf("两个请求得到相同 request ID %q", same)
	}

	headers := rec.Header()
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := headers.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := headers.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	s := newTestServer(t, server.Options{MaxBodyBytes: 8})
	s.Handle("/echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, strings.Repeat("x", int(n)))
	}))

	tooLarge := serveRequest(s.Handler(), http.MethodPost, "/echo", bytes.NewReader([]byte("123456789")))
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超过上限的请求 = %d, want 413", tooLarge.Code)
	}
	if !strings.Contains(tooLarge.Body.String(), "request_body_too_large") {
		t.Errorf("413 body = %q, want request_body_too_large", tooLarge.Body.String())
	}

	atLimit := serveRequest(s.Handler(), http.MethodPost, "/echo", bytes.NewReader([]byte("12345678")))
	if atLimit.Code != http.StatusOK {
		t.Fatalf("等于上限的请求 = %d, want 200（body=%q）", atLimit.Code, atLimit.Body.String())
	}
	if got := atLimit.Body.String(); got != "xxxxxxxx" {
		t.Errorf("echo body = %q, want 8 个 x", got)
	}
}

func TestPanicRecovery(t *testing.T) {
	s := newTestServer(t, server.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s.Handle("/panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("super-secret-panic-value")
	}))

	rec := serveRequest(s.Handler(), http.MethodGet, "/panic", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 请求 = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "super-secret-panic-value") {
		t.Errorf("panic 响应泄漏 panic 值：%q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request_id") {
		t.Errorf("panic 响应缺少 request_id：%q", rec.Body.String())
	}
	if rec.Header().Get(requestIDHeader) == "" {
		t.Error("panic 响应缺少 X-Request-ID")
	}
}

func TestDefaultListenAddressAndValidation(t *testing.T) {
	s, err := server.New(server.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if got := s.Addr(); got != "127.0.0.1:8080" {
		t.Errorf("默认监听地址 = %q, want 127.0.0.1:8080", got)
	}
	if err := s.Shutdown(); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	if _, err := server.New(server.Options{ListenAddr: "127.0.0.1"}); err == nil {
		t.Error("非法监听地址未被拒绝")
	}
	if _, err := server.New(server.Options{ListenAddr: "127.0.0.1:70000"}); err == nil {
		t.Error("越界监听端口未被拒绝")
	}
}

func TestGracefulShutdownWaitsForInflight(t *testing.T) {
	s := newTestServer(t, server.Options{ShutdownTimeout: 2 * time.Second})
	t.Cleanup(func() {
		_ = s.Shutdown()
	})

	started := make(chan struct{})
	var once sync.Once
	s.Handle("/slow", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "done")
	}))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.Serve(listener)
	}()

	type clientResult struct {
		status int
		err    error
	}
	resultCh := make(chan clientResult, 1)
	go func() {
		resp, err := localHTTPClient().Get("http://" + listener.Addr().String() + "/slow")
		if err != nil {
			resultCh <- clientResult{err: err}
			return
		}
		defer func() {
			_ = resp.Body.Close()
		}()
		_, _ = io.Copy(io.Discard, resp.Body)
		resultCh <- clientResult{status: resp.StatusCode}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未到达 handler")
	}

	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("进行中的请求失败: %v", result.err)
		}
		if result.status != http.StatusOK {
			t.Errorf("进行中的请求 = %d, want 200", result.status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("进行中的请求未在 Shutdown 后完成")
	}

	select {
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve 返回 %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve 未退出")
	}
}

func TestShutdownTimeoutCancelsInflight(t *testing.T) {
	s := newTestServer(t, server.Options{ShutdownTimeout: 100 * time.Millisecond})
	t.Cleanup(func() {
		_ = s.Shutdown()
	})

	started := make(chan struct{})
	unblocked := make(chan struct{})
	var startedOnce, unblockedOnce sync.Once
	s.Handle("/block", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
		unblockedOnce.Do(func() { close(unblocked) })
	}))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.Serve(listener)
	}()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := localHTTPClient().Get("http://" + listener.Addr().String() + "/block")
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未到达 handler")
	}

	start := time.Now()
	shutdownErr := s.Shutdown()
	elapsed := time.Since(start)
	if shutdownErr == nil {
		t.Fatal("Shutdown 应在超时后返回错误")
	}
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Errorf("Shutdown 错误 = %v, want context.DeadlineExceeded", shutdownErr)
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("Shutdown 仅等待 %s，未达到设置的超时", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Shutdown 等待 %s，超出预期", elapsed)
	}

	select {
	case <-unblocked:
	case <-time.After(2 * time.Second):
		t.Fatal("BaseContext 取消后 handler 未退出")
	}
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端请求未结束")
	}
	select {
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve 返回 %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve 未退出")
	}
}

func TestEmbeddedAssets(t *testing.T) {
	index, err := webassets.Index()
	if err != nil {
		t.Fatalf("webassets.Index: %v", err)
	}
	if !bytes.Contains(bytes.ToLower(index), []byte("<!doctype html>")) {
		t.Errorf("嵌入的 index.html 不是 HTML：%q", index)
	}

	dist, err := webassets.Dist()
	if err != nil {
		t.Fatalf("webassets.Dist: %v", err)
	}
	fromFS, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatalf("读取 dist/index.html: %v", err)
	}
	if !bytes.Equal(fromFS, index) {
		t.Error("webassets.Index 与 dist/index.html 内容不一致")
	}
}
