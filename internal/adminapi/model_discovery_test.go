package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/upstream"
)

type fakeModelLister struct {
	models []string
	err    error
	target upstream.Target
	calls  int
}

func (f *fakeModelLister) ListModels(_ context.Context, target upstream.Target) ([]string, error) {
	f.calls++
	f.target = target
	return append([]string(nil), f.models...), f.err
}

type budgetModelLister struct {
	t      *testing.T
	budget time.Duration
}

func (f budgetModelLister) ListModels(ctx context.Context, _ upstream.Target) ([]string, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > f.budget {
		f.t.Errorf("discovery missing total budget")
		return nil, context.DeadlineExceeded
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestModelDiscoveryTotalBudget(t *testing.T) {
	api, _, _, cookie, _ := newChannelsAPI(t)
	api.modelDiscoveryTimeout = 20 * time.Millisecond
	api.models = budgetModelLister{t: t, budget: api.modelDiscoveryTimeout}
	channel := createChannel(t, api, cookie, validChannelBody("discovery-budget"))
	for _, path := range []string{"/admin/api/channels/preview-models", "/admin/api/channels/" + intString(channel.ID) + "/models/sync"} {
		rec := apiRequest(t, api, http.MethodPost, path, map[string]any{
			"base_url": "https://unused.example/v1", "protocol": "openai_chat", "key": "sk-fake",
		}, testOrigin, cookie)
		if rec.Code != http.StatusGatewayTimeout || decodeBody[errorResponse](t, rec).Error != "upstream_timeout" {
			t.Fatalf("budget response=%d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestAdminClientInjectionAndDefaults(t *testing.T) {
	client, err := upstream.NewClient(upstream.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	api, _ := newTestAPI(t, func(opts *Options) {
		opts.ProbeClient = client
		opts.ProbeTimeout = 2 * time.Second
		opts.ModelDiscoveryTimeout = 3 * time.Second
	})
	if api.probeClient != client || api.models != client || api.probeTimeout != 2*time.Second || api.modelDiscoveryTimeout != 3*time.Second {
		t.Fatal("explicit shared client or budgets not retained")
	}
	api, _ = newTestAPI(t, func(opts *Options) { opts.Models = client })
	if api.probeClient != client || api.models != client || api.probeTimeout != config.DefaultProbeTimeout || api.modelDiscoveryTimeout != config.DefaultModelDiscoveryTimeout {
		t.Fatal("ModelLister client not reused or defaults incorrect")
	}
	lister := &fakeModelLister{}
	api, _ = newTestAPI(t, func(opts *Options) { opts.Models = lister; opts.ProbeClient = client })
	if api.models != lister || api.probeClient != client {
		t.Fatal("explicit ModelLister was replaced")
	}
	for _, opts := range []Options{{Auth: api.auth, ProbeTimeout: -time.Second}, {Auth: api.auth, ModelDiscoveryTimeout: -time.Second}} {
		if _, err := New(opts); err == nil {
			t.Fatal("negative budget accepted")
		}
	}
}

func TestPreviewChannelModelsUsesDraftCredentials(t *testing.T) {
	api, _, _, cookie, logs := newChannelsAPI(t)
	lister := &fakeModelLister{models: []string{"gpt-5", "o3"}}
	api.models = lister

	rec := apiRequest(t, api, http.MethodPost, "/admin/api/channels/preview-models", map[string]any{
		"base_url": "https://upstream.example.com/v1",
		"protocol": "openai_responses",
		"key":      "sk-draft-preview-0123456789",
	}, testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	got := decodeBody[channelModelsResponse](t, rec)
	if len(got.Models) != 2 || got.Models[0] != "gpt-5" || got.Models[1] != "o3" {
		t.Fatalf("models = %v", got.Models)
	}
	if lister.target.BaseURL != "https://upstream.example.com/v1" ||
		lister.target.Protocol != "openai_responses" ||
		lister.target.APIKey != "sk-draft-preview-0123456789" {
		t.Fatalf("lister target = %+v", lister.target)
	}
	if strings.Contains(logs.String(), "sk-draft-preview") {
		t.Fatal("日志泄漏了草稿 Key")
	}
}

func TestSyncChannelModelsDecryptsStoredKey(t *testing.T) {
	api, _, _, cookie, _ := newChannelsAPI(t)
	lister := &fakeModelLister{models: []string{"gpt-5"}}
	api.models = lister
	created := createChannel(t, api, cookie, validChannelBody("sync-models"))

	rec := apiRequest(t, api, http.MethodPost,
		"/admin/api/channels/"+strconv.FormatInt(created.ID, 10)+"/models/sync",
		nil, testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	if lister.target.APIKey != testChannelKey {
		t.Fatalf("lister 收到 Key = %q，期望解密后的渠道 Key", lister.target.APIKey)
	}
	if lister.target.ChannelID != created.ID || lister.target.Protocol != created.Protocol {
		t.Fatalf("lister target = %+v", lister.target)
	}
}

func TestModelDiscoveryErrorsAreSanitized(t *testing.T) {
	api, _, _, cookie, logs := newChannelsAPI(t)
	lister := &fakeModelLister{
		err: &upstream.ModelsHTTPError{StatusCode: http.StatusUnauthorized},
	}
	api.models = lister

	rec := apiRequest(t, api, http.MethodPost, "/admin/api/channels/preview-models", map[string]any{
		"base_url": "https://upstream.example.com/v1",
		"protocol": "openai_responses",
		"key":      "sk-never-log-this",
	}, testOrigin, cookie)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误体: %v", err)
	}
	if body.Error != "upstream_auth_failed" {
		t.Fatalf("error code = %q", body.Error)
	}
	if strings.Contains(rec.Body.String(), "sk-never-log-this") ||
		strings.Contains(logs.String(), "sk-never-log-this") {
		t.Fatal("响应或日志泄漏了 Key")
	}

	api.models = &fakeModelLister{err: upstream.ErrUnsupportedProtocol}
	rec = apiRequest(t, api, http.MethodPost, "/admin/api/channels/preview-models", map[string]any{
		"base_url": "https://upstream.example.com/v1",
		"protocol": "anthropic_messages",
		"key":      "sk-test",
	}, testOrigin, cookie)
	if rec.Code != http.StatusBadRequest ||
		decodeBody[errorResponse](t, rec).Error != "unsupported_protocol" {
		t.Fatalf("unsupported = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateChannelWithModelsCreatesRoutesTransactionally(t *testing.T) {
	api, st, _, cookie, _ := newChannelsAPI(t)
	body := validChannelBody("with-models")
	body["models"] = []string{"gpt-5", " gpt-5 ", "o3", ""}

	created := createChannel(t, api, cookie, body)
	if created.ModelCount != 2 {
		t.Fatalf("model_count = %d，期望 2", created.ModelCount)
	}
	rows, err := st.DB().Query(
		`SELECT client_model, upstream_model, enabled FROM model_routes
		 WHERE channel_id = ? ORDER BY client_model`, created.ID)
	if err != nil {
		t.Fatalf("查询映射: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var client, upstreamModel string
		var enabled int
		if err := rows.Scan(&client, &upstreamModel, &enabled); err != nil {
			t.Fatalf("解析映射: %v", err)
		}
		if client != upstreamModel || enabled != 1 {
			t.Fatalf("映射 = %s -> %s enabled=%d", client, upstreamModel, enabled)
		}
		got = append(got, client)
	}
	if len(got) != 2 || got[0] != "gpt-5" || got[1] != "o3" {
		t.Fatalf("映射 = %v", got)
	}

	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = "model-" + strconv.Itoa(i)
	}
	invalid := validChannelBody("too-many-models")
	invalid["models"] = tooMany
	rec := apiRequest(t, api, http.MethodPost, "/admin/api/channels", invalid, testOrigin, cookie)
	if rec.Code != http.StatusBadRequest ||
		decodeBody[errorResponse](t, rec).Error != "invalid_models" {
		t.Fatalf("超量模型 = %d %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM channels WHERE name = 'too-many-models'`).Scan(&count); err != nil {
		t.Fatalf("统计渠道: %v", err)
	}
	if count != 0 {
		t.Fatalf("模型校验失败后仍创建了 %d 个渠道", count)
	}
}

func TestListChannelsIncludesModelCount(t *testing.T) {
	api, _, _, cookie, _ := newChannelsAPI(t)
	created := createChannel(t, api, cookie, validChannelBody("counted"))
	if _, err := api.db.ExecContext(context.Background(),
		`INSERT INTO model_routes (client_model, channel_id, upstream_model, priority, enabled, created_at, updated_at)
		 VALUES ('gpt-5', ?, 'gpt-5', 0, 1, '2026-10-31T00:00:00Z', '2026-10-31T00:00:00Z')`,
		created.ID); err != nil {
		t.Fatalf("插入映射: %v", err)
	}
	rec := apiRequest(t, api, http.MethodGet, "/admin/api/channels", nil, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	got := decodeBody[channelListResponse](t, rec)
	if len(got.Channels) != 1 || got.Channels[0].ModelCount != 1 {
		t.Fatalf("channels = %+v", got.Channels)
	}
}
