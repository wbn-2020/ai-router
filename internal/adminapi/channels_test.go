package adminapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/routing"
	"ai-router/internal/secret"
	"ai-router/internal/store"
)

const (
	testChannelKey        = "sk-primary-0123456789abcdef"
	testRotatedChannelKey = "sk-rotated-zyxwvutsrqponmlk"
)

// newChannelsAPI 构造带主密钥环境变量的管理 API、临时 Store、登录 cookie
// 与可检查的日志缓冲。
func newChannelsAPI(t *testing.T) (*API, *store.Store, *secret.Envelope, *http.Cookie, *bytes.Buffer) {
	t.Helper()
	master := make([]byte, secret.MasterKeySize)
	for i := range master {
		master[i] = 0x7c
	}
	t.Setenv(config.EnvMasterKey, base64.StdEncoding.EncodeToString(master))
	envelope, err := secret.NewEnvelope(master)
	if err != nil {
		t.Fatalf("secret.NewEnvelope: %v", err)
	}

	logs := &bytes.Buffer{}
	api, st := newTestAPI(t, func(opts *Options) {
		opts.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
	})
	cookie := loginCookie(t, api, testAdminPassword)
	return api, st, envelope, cookie, logs
}

func validChannelBody(name string) map[string]any {
	return map[string]any{
		"name":            name,
		"base_url":        "https://upstream.example.com/v1?api-version=2026-01-01",
		"key":             testChannelKey,
		"protocol":        store.ChannelProtocolOpenAIResponses,
		"priority":        10,
		"max_concurrency": 3,
	}
}

func createChannel(t *testing.T, api *API, cookie *http.Cookie, body map[string]any) channelView {
	t.Helper()
	rec := apiRequest(t, api, http.MethodPost, "/admin/api/channels", body, testOrigin, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建渠道 = %d, want 201（body=%s）", rec.Code, rec.Body.String())
	}
	return decodeBody[channelView](t, rec)
}

func rawChannelSecrets(t *testing.T, st *store.Store, id int64) (ciphertext, mask string) {
	t.Helper()
	if err := st.DB().QueryRow(
		`SELECT key_ciphertext, key_mask FROM channels WHERE id = ?`, id,
	).Scan(&ciphertext, &mask); err != nil {
		t.Fatalf("读取渠道密文: %v", err)
	}
	return ciphertext, mask
}

func assertNoChannelSecret(t *testing.T, body string, plaintext string) {
	t.Helper()
	if strings.Contains(body, plaintext) {
		t.Fatalf("响应泄漏上游 Key 明文：%s", body)
	}
	if strings.Contains(body, `"key"`) || strings.Contains(body, `"key_ciphertext"`) {
		t.Fatalf("响应包含 key/key_ciphertext 字段：%s", body)
	}
}

type fakeRuntimeReader struct {
	seen     []store.Channel
	snapshot routing.RuntimeSnapshot
}

func (f *fakeRuntimeReader) Runtime(channel store.Channel) routing.RuntimeSnapshot {
	f.seen = append(f.seen, channel)
	return f.snapshot
}

func TestChannelRuntimeIsReadOnlyAndIndependentFromHealth(t *testing.T) {
	api, st, _, cookie, _ := newChannelsAPI(t)
	created := createChannel(t, api, cookie, validChannelBody("runtime"))
	if created.Runtime.State != "unknown" || created.ConnectionRevision < 1 {
		t.Fatalf("no runtime must be unknown: %+v", created)
	}
	until := time.Now().UTC().Add(time.Minute)
	reader := &fakeRuntimeReader{snapshot: routing.RuntimeSnapshot{
		State: "recovering", InUse: 1, Limit: 3, OpenUntil: &until,
		LastError: "upstream_timeout", ModelCooldowns: 2, ModelRecovering: 1,
	}}
	api.runtime = reader
	for _, path := range []string{"/admin/api/channels", "/admin/api/channels/" + intString(created.ID)} {
		rec := apiRequest(t, api, http.MethodGet, path, nil, "", cookie)
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		var view channelView
		if path == "/admin/api/channels" {
			view = decodeBody[channelListResponse](t, rec).Channels[0]
		} else {
			view = decodeBody[channelView](t, rec)
		}
		if !view.Enabled || view.LastHealthStatus != "" || view.Runtime.State != "recovering" || view.Runtime.InUse != 1 || view.Runtime.Limit != 3 || view.Runtime.ModelCooldowns != 2 || view.Runtime.ModelRecovering != 1 || view.Runtime.OpenUntil == nil || view.Runtime.LastError != "upstream_timeout" {
			t.Fatalf("runtime incorrectly merged with configuration or health: %+v", view)
		}
		for _, field := range []string{"connection_revision", "last_health_revision", "last_health_model", "last_health_protocol", "last_health_kind", "last_health_duration_ms", "runtime", "open_until", "model_recovering"} {
			if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
				t.Errorf("missing JSON field %s", field)
			}
		}
	}
	if len(reader.seen) != 2 || reader.seen[0].ConnectionRevision != created.ConnectionRevision {
		t.Fatalf("reader missing current revision: %+v", reader.seen)
	}
	if health := readDebugHealth(t, st, created.ID); health.Status != "" || health.At != nil {
		t.Fatalf("runtime read persisted health: %+v", health)
	}
}

// TestChannelCRUDAPI 覆盖创建、列表脱敏、详情、更新（含 Key 轮换）、启停与删除。
func TestChannelCRUDAPI(t *testing.T) {
	api, st, envelope, cookie, logs := newChannelsAPI(t)

	created := createChannel(t, api, cookie, validChannelBody("primary"))
	if created.ID <= 0 || created.Name != "primary" {
		t.Fatalf("创建返回 = %+v", created)
	}
	if created.KeyMask != secret.MaskKey(testChannelKey) {
		t.Errorf("key_mask = %q，期望 %q", created.KeyMask, secret.MaskKey(testChannelKey))
	}
	if created.Priority != 10 || created.MaxConcurrency != 3 || !created.Enabled {
		t.Errorf("创建返回配置不符：%+v", created)
	}

	ciphertext, mask := rawChannelSecrets(t, st, created.ID)
	if !strings.HasPrefix(ciphertext, secret.EnvelopeVersion+":") {
		t.Fatalf("key_ciphertext 不是 v1 信封：%q", ciphertext)
	}
	if strings.Contains(ciphertext, testChannelKey) {
		t.Fatal("key_ciphertext 包含明文")
	}
	decrypted, err := envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("解密落库密文: %v", err)
	}
	if decrypted != testChannelKey {
		t.Fatalf("解密结果 = %q，期望原始 Key", decrypted)
	}
	if mask != secret.MaskKey(testChannelKey) {
		t.Errorf("key_mask = %q", mask)
	}
	var plaintextHits int
	if err := st.DB().QueryRow(
		`SELECT count(*) FROM channels WHERE key_ciphertext = ? OR key_mask = ?`,
		testChannelKey, testChannelKey,
	).Scan(&plaintextHits); err != nil {
		t.Fatalf("检索明文: %v", err)
	}
	if plaintextHits != 0 {
		t.Fatal("数据库可用明文检索到渠道")
	}

	list := apiRequest(t, api, http.MethodGet, "/admin/api/channels", nil, "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("渠道列表 = %d, want 200（body=%s）", list.Code, list.Body.String())
	}
	assertNoChannelSecret(t, list.Body.String(), testChannelKey)
	assertNoStore(t, list)
	listBody := decodeBody[channelListResponse](t, list)
	if len(listBody.Channels) != 1 || listBody.Channels[0].ID != created.ID {
		t.Fatalf("渠道列表 = %+v", listBody.Channels)
	}

	detail := apiRequest(t, api, http.MethodGet,
		"/admin/api/channels/"+intString(created.ID), nil, "", cookie)
	if detail.Code != http.StatusOK {
		t.Fatalf("渠道详情 = %d, want 200（body=%s）", detail.Code, detail.Body.String())
	}
	assertNoChannelSecret(t, detail.Body.String(), testChannelKey)
	assertNoStore(t, detail)

	// 不带 key 的更新保持原密文。
	patched := apiRequest(t, api, http.MethodPatch,
		"/admin/api/channels/"+intString(created.ID),
		map[string]any{"name": "primary-renamed", "priority": 20}, testOrigin, cookie)
	if patched.Code != http.StatusOK {
		t.Fatalf("更新渠道 = %d, want 200（body=%s）", patched.Code, patched.Body.String())
	}
	patchedView := decodeBody[channelView](t, patched)
	if patchedView.Name != "primary-renamed" || patchedView.Priority != 20 {
		t.Errorf("更新后 = %+v", patchedView)
	}
	if patchedView.MaxConcurrency != 3 || !patchedView.Enabled {
		t.Errorf("省略字段未保持原值：%+v", patchedView)
	}
	keptCiphertext, keptMask := rawChannelSecrets(t, st, created.ID)
	if keptCiphertext != ciphertext || keptMask != mask {
		t.Fatal("未提供 key 时密文/脱敏值不应变化")
	}

	// 带 key 的更新替换密文。
	rotated := apiRequest(t, api, http.MethodPatch,
		"/admin/api/channels/"+intString(created.ID),
		map[string]any{"key": testRotatedChannelKey}, testOrigin, cookie)
	if rotated.Code != http.StatusOK {
		t.Fatalf("轮换 Key = %d, want 200（body=%s）", rotated.Code, rotated.Body.String())
	}
	assertNoChannelSecret(t, rotated.Body.String(), testRotatedChannelKey)
	rotatedBody := decodeBody[channelView](t, rotated)
	if rotatedBody.KeyMask != secret.MaskKey(testRotatedChannelKey) {
		t.Errorf("轮换后 key_mask = %q", rotatedBody.KeyMask)
	}
	newCiphertext, _ := rawChannelSecrets(t, st, created.ID)
	if newCiphertext == ciphertext {
		t.Fatal("轮换 Key 后密文未变化")
	}
	newPlaintext, err := envelope.DecryptString(newCiphertext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("解密新密文: %v", err)
	}
	if newPlaintext != testRotatedChannelKey {
		t.Fatalf("新密文解密 = %q，期望轮换后的 Key", newPlaintext)
	}
	if strings.Contains(newCiphertext, testChannelKey) {
		t.Fatal("新密文包含旧明文")
	}

	// 启停。
	disabled := apiRequest(t, api, http.MethodPatch,
		"/admin/api/channels/"+intString(created.ID),
		map[string]any{"enabled": false}, testOrigin, cookie)
	if disabled.Code != http.StatusOK {
		t.Fatalf("禁用渠道 = %d, want 200（body=%s）", disabled.Code, disabled.Body.String())
	}
	if decodeBody[channelView](t, disabled).Enabled {
		t.Error("禁用后 enabled 仍为 true")
	}

	// 删除。
	deleted := apiRequest(t, api, http.MethodDelete,
		"/admin/api/channels/"+intString(created.ID), nil, testOrigin, cookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除渠道 = %d, want 204（body=%s）", deleted.Code, deleted.Body.String())
	}
	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("统计 channels: %v", err)
	}
	if count != 0 {
		t.Fatalf("删除后 channels 行数 = %d", count)
	}
	missing := apiRequest(t, api, http.MethodGet,
		"/admin/api/channels/"+intString(created.ID), nil, "", cookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("删除后详情 = %d, want 404（body=%s）", missing.Code, missing.Body.String())
	}
	missingDelete := apiRequest(t, api, http.MethodDelete,
		"/admin/api/channels/"+intString(created.ID), nil, testOrigin, cookie)
	if missingDelete.Code != http.StatusNotFound {
		t.Fatalf("重复删除 = %d, want 404", missingDelete.Code)
	}

	logOutput := logs.String()
	for _, leaked := range []string{testChannelKey, testRotatedChannelKey, ciphertext, newCiphertext} {
		if strings.Contains(logOutput, leaked) {
			t.Fatalf("日志泄漏 Key 或密文 %q：%s", leaked, logOutput)
		}
	}
}

// TestChannelAPIValidation 覆盖非法输入、重复名称与错误状态码。
func TestChannelAPIValidation(t *testing.T) {
	api, st, _, cookie, logs := newChannelsAPI(t)

	tests := []struct {
		name   string
		mutate func(map[string]any)
		status int
		code   string
		leak   string
	}{
		{"非法 base URL", func(body map[string]any) {
			body["base_url"] = "ftp://upstream.example.com/v1"
		}, http.StatusBadRequest, "invalid_channel_config", ""},
		{"base URL 含 userinfo", func(body map[string]any) {
			body["base_url"] = "https://user:secret@upstream.example.com/v1?token=abc"
		}, http.StatusBadRequest, "invalid_channel_config", "secret"},
		{"非法 protocol", func(body map[string]any) {
			body["protocol"] = "openai"
		}, http.StatusBadRequest, "invalid_channel_config", ""},
		{"并发为 0", func(body map[string]any) {
			body["max_concurrency"] = 0
		}, http.StatusBadRequest, "invalid_channel_config", ""},
		{"并发超过 64", func(body map[string]any) {
			body["max_concurrency"] = 65
		}, http.StatusBadRequest, "invalid_channel_config", ""},
		{"priority 过大", func(body map[string]any) {
			body["priority"] = 1001
		}, http.StatusBadRequest, "invalid_channel_config", ""},
		{"缺少 key", func(body map[string]any) {
			delete(body, "key")
		}, http.StatusBadRequest, "invalid_key", ""},
		{"空 key", func(body map[string]any) {
			body["key"] = "   "
		}, http.StatusBadRequest, "invalid_key", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validChannelBody("invalid")
			tt.mutate(body)
			rec := apiRequest(t, api, http.MethodPost, "/admin/api/channels", body, testOrigin, cookie)
			if rec.Code != tt.status {
				t.Fatalf("状态码 = %d, want %d（body=%s）", rec.Code, tt.status, rec.Body.String())
			}
			var payload errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("解析错误响应: %v", err)
			}
			if payload.Error != tt.code {
				t.Errorf("错误码 = %q, want %q", payload.Error, tt.code)
			}
			if tt.leak != "" && strings.Contains(rec.Body.String(), tt.leak) {
				t.Fatalf("错误响应回显了输入 %q：%s", tt.leak, rec.Body.String())
			}
			assertNoStore(t, rec)
		})
	}

	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("统计 channels: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法输入落库 %d 行", count)
	}

	first := createChannel(t, api, cookie, validChannelBody("duplicate-name"))
	if first.ID <= 0 {
		t.Fatal("首次创建失败")
	}
	conflict := apiRequest(t, api, http.MethodPost, "/admin/api/channels",
		validChannelBody("duplicate-name"), testOrigin, cookie)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("重复 name 创建 = %d, want 409（body=%s）", conflict.Code, conflict.Body.String())
	}
	if !strings.Contains(conflict.Body.String(), "channel_name_conflict") {
		t.Errorf("重复 name 响应缺少稳定错误码：%s", conflict.Body.String())
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("统计 channels: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复创建后 channels 行数 = %d，期望 1", count)
	}

	// 更新为已有名称同样 409。
	other := apiRequest(t, api, http.MethodPost, "/admin/api/channels",
		validChannelBody("other-name"), testOrigin, cookie)
	if other.Code != http.StatusCreated {
		t.Fatalf("创建第二个渠道 = %d（body=%s）", other.Code, other.Body.String())
	}
	otherView := decodeBody[channelView](t, other)
	conflictUpdate := apiRequest(t, api, http.MethodPatch,
		"/admin/api/channels/"+intString(otherView.ID),
		map[string]any{"name": "duplicate-name"}, testOrigin, cookie)
	if conflictUpdate.Code != http.StatusConflict {
		t.Fatalf("重复 name 更新 = %d, want 409（body=%s）", conflictUpdate.Code, conflictUpdate.Body.String())
	}

	// 不存在的渠道与非法 ID。
	if rec := apiRequest(t, api, http.MethodPatch,
		"/admin/api/channels/99999", map[string]any{"name": "ghost"}, testOrigin, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("更新不存在渠道 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodGet,
		"/admin/api/channels/not-a-number", nil, "", cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法渠道 ID = %d, want 400", rec.Code)
	}

	logOutput := logs.String()
	for _, leaked := range []string{testChannelKey, "user:secret", "token=abc"} {
		if strings.Contains(logOutput, leaked) {
			t.Fatalf("日志泄漏输入 %q：%s", leaked, logOutput)
		}
	}
}

// TestChannelsRequireSession 覆盖渠道端点的认证与同源校验。
func TestChannelsRequireSession(t *testing.T) {
	api, _, _, cookie, _ := newChannelsAPI(t)

	list := apiRequest(t, api, http.MethodGet, "/admin/api/channels", nil, "")
	if list.Code != http.StatusUnauthorized {
		t.Fatalf("未登录渠道列表 = %d, want 401（body=%s）", list.Code, list.Body.String())
	}
	assertNoStore(t, list)

	create := apiRequest(t, api, http.MethodPost, "/admin/api/channels",
		validChannelBody("no-session"), testOrigin)
	if create.Code != http.StatusUnauthorized {
		t.Fatalf("未登录创建渠道 = %d, want 401（body=%s）", create.Code, create.Body.String())
	}

	crossOrigin := apiRequest(t, api, http.MethodPost, "/admin/api/channels",
		validChannelBody("cross-origin"), "http://evil.example", cookie)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("跨源创建渠道 = %d, want 403（body=%s）", crossOrigin.Code, crossOrigin.Body.String())
	}
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}
