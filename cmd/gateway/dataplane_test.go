package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/store"
)

// TestBuildServerServesClientRoutes 覆盖生产装配的鉴权及模型发现入口。
func TestBuildServerServesClientRoutes(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	t.Setenv(config.EnvMasterKey, base64.StdEncoding.EncodeToString(master))

	dir := t.TempDir()
	st, err := store.Open(store.Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	cfg := config.Config{
		ListenAddr:      "127.0.0.1:0",
		DataDir:         dir,
		DBPath:          filepath.Join(dir, "gateway.db"),
		MasterKey:       master,
		ClientKey:       "gw_client_dataplane_test_0123456789",
		CapacityWait:    time.Second,
		CaptureMaxBytes: config.DefaultCaptureMaxBytes,
	}
	srv, _, err := buildServer(cfg, st)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

	request := func(path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	unauthorized := request("/v1/models", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("无客户端 Key /v1/models = %d，期望 401（body=%s）",
			unauthorized.Code, unauthorized.Body.String())
	}

	authorized := request("/v1/models", cfg.ClientKey)
	if authorized.Code != http.StatusOK {
		t.Fatalf("已鉴权 /v1/models = %d（body=%s）",
			authorized.Code, authorized.Body.String())
	}
	if !strings.Contains(authorized.Body.String(), `"object":"list"`) {
		t.Fatalf("模型列表响应 = %s", authorized.Body.String())
	}

	codexModels := request("/backend-api/codex/models", cfg.ClientKey)
	if codexModels.Code != http.StatusOK {
		t.Fatalf("已鉴权 /backend-api/codex/models = %d（body=%s）",
			codexModels.Code, codexModels.Body.String())
	}
}
