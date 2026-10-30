package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ai-router/internal/store"
)

// requiredCodexModelFields 是 Codex 0.164.0 ModelInfo 的完整顶层字段集。
// 可空字段也必须出现（值为 null），不能靠 omitempty 省略。
var requiredCodexModelFields = []string{
	"slug",
	"display_name",
	"description",
	"default_reasoning_level",
	"supported_reasoning_levels",
	"multi_agent_reasoning_effort",
	"shell_type",
	"visibility",
	"supported_in_api",
	"priority",
	"additional_speed_tiers",
	"service_tiers",
	"default_service_tier",
	"availability_nux",
	"upgrade",
	"model_messages",
	"include_skills_usage_instructions",
	"include_plugin_usage_instructions",
	"include_apps_usage_instructions",
	"supports_reasoning_summary_parameter",
	"default_reasoning_summary",
	"support_verbosity",
	"default_verbosity",
	"apply_patch_tool_type",
	"web_search_tool_type",
	"truncation_policy",
	"supports_image_detail_original",
	"supports_parallel_tool_calls",
	"context_window",
	"max_context_window",
	"auto_compact_token_limit",
	"comp_hash",
	"effective_context_window_percent",
	"experimental_supported_tools",
	"input_modalities",
	"supports_search_tool",
	"use_responses_lite",
	"node_repl_auto_review_required",
	"node_repl_disabled",
	"auto_review_model_override",
	"model_specialty",
	"tool_mode",
	"multi_agent_version",
}

var requiredCodexModelMessageFields = []string{
	"instructions_template",
	"instructions_variables",
	"approvals",
	"collaboration_modes",
	"auto_review",
	"permissions",
	"multi_agent",
	"token_budget",
	"guardian_v2",
}

func newCodexTestHandler(t *testing.T, catalog RouteCatalog, contextWindow int) *Handler {
	t.Helper()
	handler, err := New(Options{
		ClientKey:          testClientKey,
		Routes:             catalog,
		CodexContextWindow: contextWindow,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return handler
}

// decodeRawCodexModels 解码 manifest 顶层 models，保留每个 descriptor 的原始
// JSON 字段，用于断言键存在性（包括 null 字段）。
func decodeRawCodexModels(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var manifest struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("解析 Codex manifest 失败：%v（body=%s）", err, body)
	}
	if manifest.Models == nil {
		t.Fatalf("manifest 缺少 models 数组：%s", body)
	}
	return manifest.Models
}

func decodeRawString(t *testing.T, value json.RawMessage) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		t.Fatalf("解析字符串字段失败：%v（raw=%s）", err, value)
	}
	return text
}

// assertCompleteCodexModel 校验单个 descriptor 的完整字段契约。
func assertCompleteCodexModel(
	t *testing.T,
	model map[string]json.RawMessage,
	wantSlug string,
	wantWindow int64,
) {
	t.Helper()

	if got := decodeRawString(t, model["slug"]); got != wantSlug {
		t.Fatalf("slug = %q, want %q", got, wantSlug)
	}
	if got := decodeRawString(t, model["display_name"]); got != wantSlug {
		t.Errorf("display_name = %q, want %q", got, wantSlug)
	}
	if got := decodeRawString(t, model["description"]); got == "" {
		t.Error("description 为空")
	}

	for _, field := range requiredCodexModelFields {
		if _, ok := model[field]; !ok {
			t.Errorf("ModelInfo 缺少字段 %q", field)
		}
	}

	for _, field := range []string{
		"multi_agent_reasoning_effort",
		"default_service_tier",
		"availability_nux",
		"upgrade",
		"default_verbosity",
		"apply_patch_tool_type",
		"auto_compact_token_limit",
		"comp_hash",
		"auto_review_model_override",
		"model_specialty",
		"tool_mode",
		"multi_agent_version",
	} {
		if got := strings.TrimSpace(string(model[field])); got != "null" {
			t.Errorf("%s = %s, want null（可空字段必须显式出现）", field, got)
		}
	}

	if got := decodeRawString(t, model["default_reasoning_level"]); got == "" {
		t.Error("default_reasoning_level 为空")
	}

	var levels []map[string]json.RawMessage
	if err := json.Unmarshal(model["supported_reasoning_levels"], &levels); err != nil {
		t.Fatalf("supported_reasoning_levels 不是数组：%v", err)
	}
	if len(levels) == 0 {
		t.Fatal("supported_reasoning_levels 为空")
	}
	for i, level := range levels {
		if decodeRawString(t, level["effort"]) == "" {
			t.Errorf("supported_reasoning_levels[%d].effort 为空", i)
		}
		if decodeRawString(t, level["description"]) == "" {
			t.Errorf("supported_reasoning_levels[%d].description 为空", i)
		}
	}

	var window int64
	if err := json.Unmarshal(model["context_window"], &window); err != nil {
		t.Fatalf("context_window 不是整数：%v", err)
	}
	if window != wantWindow {
		t.Errorf("context_window = %d, want %d", window, wantWindow)
	}
	if err := json.Unmarshal(model["max_context_window"], &window); err != nil {
		t.Fatalf("max_context_window 不是整数：%v", err)
	}
	if window != wantWindow {
		t.Errorf("max_context_window = %d, want %d", window, wantWindow)
	}

	var modalities []string
	if err := json.Unmarshal(model["input_modalities"], &modalities); err != nil {
		t.Fatalf("input_modalities 不是数组：%v", err)
	}
	if strings.Join(modalities, ",") != "text,image" {
		t.Errorf("input_modalities = %v, want [text image]", modalities)
	}

	var messages map[string]json.RawMessage
	if err := json.Unmarshal(model["model_messages"], &messages); err != nil {
		t.Fatalf("model_messages 不是对象：%v", err)
	}
	for _, field := range requiredCodexModelMessageFields {
		if _, ok := messages[field]; !ok {
			t.Errorf("model_messages 缺少字段 %q", field)
		}
	}
	if got := decodeRawString(t, messages["instructions_template"]); got == "" {
		t.Error("model_messages.instructions_template 为空")
	}

	var policy struct {
		Mode  string `json:"mode"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(model["truncation_policy"], &policy); err != nil {
		t.Fatalf("truncation_policy 不是对象：%v", err)
	}
	if policy.Mode == "" || policy.Limit <= 0 {
		t.Errorf("truncation_policy = %+v", policy)
	}

	var percent int
	if err := json.Unmarshal(model["effective_context_window_percent"], &percent); err != nil {
		t.Fatalf("effective_context_window_percent 不是整数：%v", err)
	}
	if percent <= 0 || percent > 100 {
		t.Errorf("effective_context_window_percent = %d", percent)
	}
}

func TestCodexManifestFullModelInfo(t *testing.T) {
	t.Setenv(codexContextWindowEnv, "")

	handler := newCodexTestHandler(t, &fakeRouteCatalog{
		models: []string{"custom-model"},
	}, 0)

	rec := performRequest(handler, http.MethodGet,
		PathModels+"?client_version=0.164.0", "", bearerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}
	if strings.Contains(rec.Body.String(), `"data"`) {
		t.Fatalf("manifest 不应包含 OpenAI list 的 data：%s", rec.Body.String())
	}

	models := decodeRawCodexModels(t, rec.Body.Bytes())
	if len(models) != 1 {
		t.Fatalf("manifest 模型数 = %d, want 1", len(models))
	}
	assertCompleteCodexModel(t, models[0], "custom-model", defaultCodexContextWindow)

	for _, secret := range []string{testClientKey, "upstream", "channel", "key_ciphertext"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("manifest 泄漏 %q：%s", secret, rec.Body.String())
		}
	}
}

func TestCodexManifestBackendAliasMatchesClientVersion(t *testing.T) {
	t.Setenv(codexContextWindowEnv, "")

	handler := newCodexTestHandler(t, &fakeRouteCatalog{
		models: []string{"alpha", "beta"},
	}, 0)

	viaQuery := performRequest(handler, http.MethodGet,
		PathModels+"?client_version=0.164.0", "", bearerHeaders())
	viaAlias := performRequest(handler, http.MethodGet, PathCodexModels, "", bearerHeaders())

	if viaQuery.Code != http.StatusOK || viaAlias.Code != http.StatusOK {
		t.Fatalf("状态码 = %d/%d, want 200/200", viaQuery.Code, viaAlias.Code)
	}
	assertNoStore(t, viaQuery)
	assertNoStore(t, viaAlias)

	queryModels := decodeRawCodexModels(t, viaQuery.Body.Bytes())
	aliasModels := decodeRawCodexModels(t, viaAlias.Body.Bytes())
	if len(queryModels) != 2 || len(aliasModels) != 2 {
		t.Fatalf("模型数 = %d/%d, want 2/2", len(queryModels), len(aliasModels))
	}
	for i, want := range []string{"alpha", "beta"} {
		if got := decodeRawString(t, queryModels[i]["slug"]); got != want {
			t.Errorf("client_version 第 %d 个 slug = %q, want %q", i, got, want)
		}
		if got := decodeRawString(t, aliasModels[i]["slug"]); got != want {
			t.Errorf("alias 第 %d 个 slug = %q, want %q", i, got, want)
		}
		assertCompleteCodexModel(t, queryModels[i], want, defaultCodexContextWindow)
	}
}

func TestCodexManifestPlainListShape(t *testing.T) {
	handler := newCodexTestHandler(t, &fakeRouteCatalog{
		models: []string{"gpt-5", "o3"},
	}, 0)

	for _, target := range []string{PathModels, PathModels + "?client_version="} {
		rec := performRequest(handler, http.MethodGet, target, "", bearerHeaders())
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 = %d, want 200（body=%s）",
				target, rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("%s Content-Type = %q", target, got)
		}

		var response modelListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("解析普通模型列表失败：%v（body=%s）", err, rec.Body.String())
		}
		if response.Object != "list" || len(response.Data) != 2 {
			t.Fatalf("%s 模型列表 = %+v", target, response)
		}
		if response.Data[0].ID != "gpt-5" || response.Data[1].ID != "o3" {
			t.Fatalf("%s 模型顺序 = %+v", target, response.Data)
		}
		for _, card := range response.Data {
			if card.Object != "model" || card.Created != modelCreatedUnix ||
				card.OwnedBy != modelOwnedBy {
				t.Fatalf("%s 模型条目 = %+v", target, card)
			}
		}
		if strings.Contains(rec.Body.String(), `"models"`) {
			t.Fatalf("普通列表不应包含 Codex models 字段：%s", rec.Body.String())
		}
	}
}

func TestCodexManifestContextWindowConfig(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		configured int
		want       int64
	}{
		{
			name: "默认 128000",
			want: defaultCodexContextWindow,
		},
		{
			name:       "显式配置覆盖默认",
			configured: 64000,
			want:       64000,
		},
		{
			name: "环境变量生效",
			env:  "96000",
			want: 96000,
		},
		{
			name:       "显式配置优先于环境变量",
			env:        "96000",
			configured: 64000,
			want:       64000,
		},
		{
			name: "非法环境变量回退默认",
			env:  "not-a-number",
			want: defaultCodexContextWindow,
		},
		{
			name: "非正环境变量回退默认",
			env:  "-1",
			want: defaultCodexContextWindow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(codexContextWindowEnv, tc.env)
			handler := newCodexTestHandler(t, &fakeRouteCatalog{
				models: []string{"window-model"},
			}, tc.configured)

			rec := performRequest(handler, http.MethodGet,
				PathModels+"?client_version=0.164.0", "", bearerHeaders())
			if rec.Code != http.StatusOK {
				t.Fatalf("状态码 = %d, want 200（body=%s）",
					rec.Code, rec.Body.String())
			}
			models := decodeRawCodexModels(t, rec.Body.Bytes())
			if len(models) != 1 {
				t.Fatalf("模型数 = %d, want 1", len(models))
			}
			assertCompleteCodexModel(t, models[0], "window-model", tc.want)
		})
	}
}

func TestCodexManifestDeduplicatesAndAllowsEmpty(t *testing.T) {
	t.Run("空列表是合法结构", func(t *testing.T) {
		handler := newCodexTestHandler(t, &fakeRouteCatalog{models: []string{}}, 0)
		rec := performRequest(handler, http.MethodGet,
			PathModels+"?client_version=0.164.0", "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）",
				rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		if !strings.Contains(rec.Body.String(), `"models":[]`) {
			t.Fatalf("空 manifest 不是空数组：%s", rec.Body.String())
		}
		if models := decodeRawCodexModels(t, rec.Body.Bytes()); len(models) != 0 {
			t.Fatalf("空 manifest 模型数 = %d, want 0", len(models))
		}
	})

	t.Run("空白与重复模型被去重", func(t *testing.T) {
		handler := newCodexTestHandler(t, &fakeRouteCatalog{
			models: []string{"alpha", " alpha ", "", "beta", "alpha", "beta"},
		}, 0)
		rec := performRequest(handler, http.MethodGet, PathCodexModels, "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）",
				rec.Code, rec.Body.String())
		}
		models := decodeRawCodexModels(t, rec.Body.Bytes())
		if len(models) != 2 {
			t.Fatalf("去重后模型数 = %d, want 2（body=%s）",
				len(models), rec.Body.String())
		}
		for i, want := range []string{"alpha", "beta"} {
			if got := decodeRawString(t, models[i]["slug"]); got != want {
				t.Errorf("第 %d 个 slug = %q, want %q", i, got, want)
			}
			assertCompleteCodexModel(t, models[i], want, defaultCodexContextWindow)
		}
	})
}

// TestCodexManifestOnlyEnabledRoutes 通过真实 SQLite 仓储验证 manifest 只读取
// 启用映射，不暴露上游模型名、base URL 查询串或 Key 密文。
func TestCodexManifestOnlyEnabledRoutes(t *testing.T) {
	t.Setenv(codexContextWindowEnv, "")

	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(store.Paths{
		DataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "gateway.db"),
	})
	if err != nil {
		t.Fatalf("打开测试数据库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移测试数据库失败：%v", err)
	}

	channel, err := st.Channels().Create(ctx, store.ChannelInput{
		ChannelConfig: store.ChannelConfig{
			Name:           "responses-main",
			BaseURL:        "https://upstream.example/v1?token=should-not-leak",
			Protocol:       store.ChannelProtocolOpenAIResponses,
			Priority:       10,
			MaxConcurrency: 2,
			Enabled:        true,
		},
		KeyCiphertext: "v1:ciphertext:should-not-leak",
		KeyMask:       "sk-****wxyz",
	})
	if err != nil {
		t.Fatalf("创建测试渠道失败：%v", err)
	}

	routes := st.ModelRoutes()
	if _, err := routes.Create(ctx, store.ModelRouteInput{
		ClientModel:   "enabled-model",
		ChannelID:     channel.ID,
		UpstreamModel: "upstream-model-should-not-leak",
		Priority:      10,
		Enabled:       true,
	}); err != nil {
		t.Fatalf("创建启用映射失败：%v", err)
	}
	if _, err := routes.Create(ctx, store.ModelRouteInput{
		ClientModel:   "disabled-model",
		ChannelID:     channel.ID,
		UpstreamModel: "disabled-upstream-should-not-leak",
		Priority:      20,
		Enabled:       false,
	}); err != nil {
		t.Fatalf("创建禁用映射失败：%v", err)
	}

	handler, err := New(Options{
		ClientKey:          testClientKey,
		Routes:             routes,
		CodexContextWindow: 64000,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	for _, target := range []string{
		PathModels + "?client_version=0.164.0",
		PathCodexModels,
	} {
		rec := performRequest(handler, http.MethodGet, target, "", bearerHeaders())
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 = %d, want 200（body=%s）",
				target, rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)

		models := decodeRawCodexModels(t, rec.Body.Bytes())
		if len(models) != 1 {
			t.Fatalf("%s 模型数 = %d, want 1（body=%s）",
				target, len(models), rec.Body.String())
		}
		assertCompleteCodexModel(t, models[0], "enabled-model", 64000)

		for _, secret := range []string{
			"disabled-model", "upstream-model-should-not-leak",
			"disabled-upstream-should-not-leak", "should-not-leak",
			"sk-****wxyz", testClientKey,
		} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("%s 泄漏 %q：%s", target, secret, rec.Body.String())
			}
		}
	}
}
