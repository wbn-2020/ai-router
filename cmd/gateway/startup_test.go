package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ai-router/internal/config"
	"ai-router/internal/secret"
	"ai-router/internal/store"
)

func TestBuildServerChecksStoredChannelKeys(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(store.Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	keyA := bytes.Repeat([]byte{1}, 32)
	envelope, err := secret.NewEnvelope(keyA)
	if err != nil {
		t.Fatal(err)
	}
	const upstreamKey = "startup-private-key-sentinel"
	ciphertext, err := envelope.EncryptString(upstreamKey, secret.PurposeChannelKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Channels().Create(ctx, store.ChannelInput{
		ChannelConfig: store.ChannelConfig{
			Name: "startup", BaseURL: "https://example.invalid/v1",
			Protocol: store.ChannelProtocolOpenAIResponses, MaxConcurrency: 1, Enabled: false,
		},
		KeyCiphertext: ciphertext, KeyMask: "masked",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ListenAddr: "127.0.0.1:0", MasterKey: bytes.Repeat([]byte{2}, 32),
		ClientKey: "startup-client-key-0123456789",
	}
	if _, _, err := buildServer(cfg, st); err == nil {
		t.Fatal("错误但格式合法的主密钥不应启动服务")
	} else if strings.Contains(err.Error(), upstreamKey) || strings.Contains(err.Error(), ciphertext) {
		t.Fatalf("启动错误泄漏加密内容：%v", err)
	}
	cfg.MasterKey = keyA
	srv, _, err := buildServer(cfg, st)
	if err != nil {
		t.Fatalf("正确密钥启动失败：%v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("正确密钥就绪检查：%d %s", rec.Code, rec.Body.String())
	}
}
