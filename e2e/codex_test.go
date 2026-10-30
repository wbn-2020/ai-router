package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-router/internal/store"
)

// TestCodexVersionGate 证明版本门禁是“失败而不是 skip”。
func TestCodexVersionGate(t *testing.T) {
	good := []string{
		"codex-cli 0.164.0",
		"codex-cli 0.164.12",
		"codex-cli 0.164.0 (something)",
	}
	for _, output := range good {
		if err := requireCodexVersion(output); err != nil {
			t.Errorf("requireCodexVersion(%q) = %v，期望 nil", output, err)
		}
	}

	bad := []string{
		"codex-cli 0.163.9",
		"codex-cli 0.165.0",
		"codex-cli 0.164",
		"",
		"not codex output",
	}
	for _, output := range bad {
		if err := requireCodexVersion(output); err == nil {
			t.Errorf("requireCodexVersion(%q) = nil，期望版本不匹配错误", output)
		}
	}
}

// TestPreflightRequiresEnabledResponsesChannel 覆盖“harness seed 缺少
// openai_responses 渠道时 preflight 失败”。
func TestPreflightRequiresEnabledResponsesChannel(t *testing.T) {
	baseURL := "http://127.0.0.1:1/v1"
	if err := preflight(defaultSeed(baseURL)); err != nil {
		t.Fatalf("默认 seed 应通过 preflight：%v", err)
	}

	cases := []struct {
		name string
		seed seedConfig
	}{
		{
			name: "完全没有渠道",
			seed: seedConfig{},
		},
		{
			name: "只有 openai_chat 渠道",
			seed: seedConfig{channels: []seedChannel{{
				Name:           "chat-only",
				Protocol:       store.ChannelProtocolOpenAIChat,
				BaseURL:        baseURL,
				Priority:       10,
				MaxConcurrency: 1,
				Enabled:        true,
			}}},
		},
		{
			name: "openai_responses 渠道被禁用",
			seed: seedConfig{channels: []seedChannel{{
				Name:           "disabled-responses",
				Protocol:       store.ChannelProtocolOpenAIResponses,
				BaseURL:        baseURL,
				Priority:       10,
				MaxConcurrency: 1,
				Enabled:        false,
			}}},
		},
		{
			name: "openai_responses 渠道缺少 base_url",
			seed: seedConfig{channels: []seedChannel{{
				Name:           "empty-base-url",
				Protocol:       store.ChannelProtocolOpenAIResponses,
				BaseURL:        " ",
				Priority:       10,
				MaxConcurrency: 1,
				Enabled:        true,
			}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := preflight(tc.seed); err == nil {
				t.Fatalf("preflight 应失败，但返回 nil")
			}
		})
	}
}

// TestCodexConfigTemplateAvoidsModelCatalogJSON 证明自定义模型不会被
// model_catalog_json 绕过 /v1/models 模型发现。
func TestCodexConfigTemplateAvoidsModelCatalogJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-0.164.0", "config.toml.tmpl"))
	if err != nil {
		t.Fatalf("读取 Codex 配置模板失败：%v", err)
	}
	text := string(raw)
	if strings.Contains(text, "model_catalog_json") {
		t.Fatalf("模板不得设置 model_catalog_json")
	}
	for _, required := range []string{
		`wire_api = "responses"`,
		"model_catalog_url",
		"supports_websockets = false",
		"requires_openai_auth = false",
		"experimental_bearer_token",
		"base_url",
		"stream_max_retries = 0",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("模板缺少 %q", required)
		}
	}
}

// TestCodexResponsesE2E 是 T27 的真实 Codex CLI 跨进程验收。
//
// 该测试没有 skip 分支：codex 缺失、版本不是 0.164.x 或 Response 行为不符时
// 直接失败。假上游与网关都只监听 127.0.0.1。
func TestCodexResponsesE2E(t *testing.T) {
	version := codexVersion(t)
	if err := requireCodexVersion(version); err != nil {
		t.Fatalf("%v", err)
	}
	codexPath := codexExecutable(t)
	h := newHarness(t)

	t.Run("model_discovery", func(t *testing.T) {
		assertCodexManifest(t, h)
	})

	t.Run("text", func(t *testing.T) {
		h.recorder.reset()
		home := writeCodexHome(t, h.baseURL, testClientKey, modelText)
		workDir := newTempDir(t)
		prompt := "Reply with exactly " + fakeTextOutput + " and do not run tools."
		result := runCodex(t, codexPath, home, workDir, modelText, prompt, nil)
		requireAgentMessage(t, result, fakeTextOutput)
		requireManifestAccepted(t, result)

		requests := h.recorder.snapshot()
		assertCodexEndpoints(t, requests)

		fakeRequests := h.fake.requestsForModel(modelText)
		if len(fakeRequests) == 0 {
			t.Fatalf("假上游没有收到模型 %q 的请求", modelText)
		}
		assertUpstreamRequest(t, fakeRequests[0], modelText)
		if !strings.Contains(string(fakeRequests[0].Body), prompt) {
			t.Fatalf("上游请求体没有包含原始用户提示词")
		}

		responses := requestsByPath(requests, http.MethodPost, "/v1/responses")
		if len(responses) != 1 {
			t.Fatalf("期望 1 个 /v1/responses 请求，实际 %d：%+v", len(responses), requestSummary(requests))
		}
		assertSSEResponse(t, responses[0], fakeTextOutput, true)
	})

	t.Run("image_input", func(t *testing.T) {
		h.recorder.reset()
		home := writeCodexHome(t, h.baseURL, testClientKey, modelImage)
		workDir := newTempDir(t)
		imagePath := filepath.Join(workDir, "tiny.png")
		imageBytes, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
		if err != nil {
			t.Fatalf("解码测试 PNG 失败：%v", err)
		}
		if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
			t.Fatalf("写入测试 PNG 失败：%v", err)
		}

		prompt := "Describe the attached image in one word."
		result := runCodex(t, codexPath, home, workDir, modelImage, prompt, []string{imagePath})
		requireAgentMessage(t, result, fakeImageOutput)
		requireManifestAccepted(t, result)

		requests := h.recorder.snapshot()
		assertCodexEndpoints(t, requests)

		fakeRequests := h.fake.requestsForModel(modelImage)
		if len(fakeRequests) == 0 {
			t.Fatalf("假上游没有收到模型 %q 的请求", modelImage)
		}
		body := string(fakeRequests[0].Body)
		if !strings.Contains(body, "input_image") {
			t.Fatalf("上游请求体没有 input_image 字段：%s", truncateForLog(body))
		}
		if !strings.Contains(body, "data:image/png;base64,") {
			t.Fatalf("上游请求体没有内联 PNG data URL：%s", truncateForLog(body))
		}

		responses := requestsByPath(requests, http.MethodPost, "/v1/responses")
		if len(responses) != 1 {
			t.Fatalf("期望 1 个 /v1/responses 请求，实际 %d：%+v", len(responses), requestSummary(requests))
		}
		assertSSEResponse(t, responses[0], fakeImageOutput, true)
	})

	t.Run("tool_calling", func(t *testing.T) {
		h.recorder.reset()
		home := writeCodexHome(t, h.baseURL, testClientKey, modelTool)
		workDir := newTempDir(t)
		prompt := "Use the exec_command tool to run echo E2E_TOOL_OUTPUT, then reply with the final text."
		result := runCodex(t, codexPath, home, workDir, modelTool, prompt, nil)
		requireAgentMessage(t, result, fakeToolFinalOutput)
		requireManifestAccepted(t, result)

		requests := h.recorder.snapshot()
		assertCodexEndpoints(t, requests)

		fakeRequests := h.fake.requestsForModel(modelTool)
		if len(fakeRequests) < 2 {
			t.Fatalf("两轮 tool calling 期望至少 2 个上游请求，实际 %d", len(fakeRequests))
		}
		if strings.Contains(string(fakeRequests[0].Body), `"function_call_output"`) {
			t.Fatalf("第一轮请求不应包含 function_call_output")
		}
		second := string(fakeRequests[1].Body)
		if !strings.Contains(second, `"function_call_output"`) {
			t.Fatalf("第二轮请求没有 function_call_output：%s", truncateForLog(second))
		}
		if !strings.Contains(second, fakeToolCallID) {
			t.Fatalf("第二轮请求没有回传 call_id %q", fakeToolCallID)
		}
		if !strings.Contains(second, "E2E_TOOL_OUTPUT") {
			t.Fatalf("第二轮请求没有包含工具调用参数/输出 E2E_TOOL_OUTPUT")
		}

		responses := requestsByPath(requests, http.MethodPost, "/v1/responses")
		if len(responses) < 2 {
			t.Fatalf("期望至少 2 个 /v1/responses 请求，实际 %d：%+v", len(responses), requestSummary(requests))
		}
		last := responses[len(responses)-1]
		assertSSEResponse(t, last, fakeToolFinalOutput, true)
	})

	t.Run("disconnect_after_commit", func(t *testing.T) {
		h.recorder.reset()
		home := writeCodexHome(t, h.baseURL, testClientKey, modelDisconnect)
		workDir := newTempDir(t)
		result := runCodex(t, codexPath, home, workDir, modelDisconnect, "Say hi.", nil)
		if result.TimedOut {
			t.Fatalf("codex exec 在断流场景下超过 %s 未结束", codexRunTimeout)
		}
		if result.ExitCode == 0 {
			t.Fatalf("上游提交后断流应让 codex 失败，实际退出码 0\nstdout:\n%s\nstderr:\n%s",
				result.Stdout, result.Stderr)
		}
		events := parseCodexEvents(result.Stdout)
		if !hasCodexEventType(events, "turn.failed") {
			t.Fatalf("codex 没有输出 turn.failed\nstdout:\n%s\nstderr:\n%s", result.Stdout, result.Stderr)
		}
		requireManifestAccepted(t, result)

		fakeRequests := h.fake.requestsForModel(modelDisconnect)
		if len(fakeRequests) != 1 {
			t.Fatalf("提交后断流不应换渠道或重试：期望 1 个上游请求，实际 %d", len(fakeRequests))
		}

		requests := h.recorder.snapshot()
		assertCodexEndpoints(t, requests)
		responses := requestsByPath(requests, http.MethodPost, "/v1/responses")
		if len(responses) != 1 {
			t.Fatalf("期望 1 个 /v1/responses 请求，实际 %d：%+v", len(responses), requestSummary(requests))
		}
		response := responses[0]
		if response.Status != http.StatusOK {
			t.Fatalf("提交后断流的 HTTP 状态 = %d，期望 200", response.Status)
		}
		body := string(response.Body)
		if !strings.Contains(body, fakePartialOutput) {
			t.Fatalf("断流前的语义内容没有到达客户端：%s", truncateForLog(body))
		}
		if got := strings.Count(body, "event: response.failed"); got != 1 {
			t.Fatalf("期望唯一一次 response.failed，实际 %d 次：%s", got, truncateForLog(body))
		}
		if strings.Contains(body, "response.completed") {
			t.Fatalf("提交后断流不得出现 response.completed：%s", truncateForLog(body))
		}
		for _, required := range []string{
			`"type":"response.failed"`,
			`"sequence_number"`,
			`"created_at"`,
			`"status":"failed"`,
			`"error"`,
		} {
			if !strings.Contains(body, required) {
				t.Fatalf("response.failed 缺少严格字段 %s：%s", required, truncateForLog(body))
			}
		}
	})
}

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

var codexModelFields = []string{
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

var codexModelMessageFields = []string{
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

func assertCodexManifest(t *testing.T, h *harness) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, h.baseURL+"/models?client_version=0.164.0", nil)
	if err != nil {
		t.Fatalf("构造模型发现请求失败：%v", err)
	}
	request.Header.Set("Authorization", "Bearer "+testClientKey)
	request.Header.Set("X-E2E-Direct", "1")

	response, err := newLocalHTTPClient().Do(request)
	if err != nil {
		t.Fatalf("请求模型发现端点失败：%v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取模型发现响应失败：%v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("模型发现状态 = %d，期望 200：%s", response.StatusCode, truncateForLog(string(body)))
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("模型发现 Cache-Control = %q，期望包含 no-store", got)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("模型发现 Content-Type = %q，期望 application/json", got)
	}

	var manifest struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("解析 Codex manifest 失败：%v；body=%s", err, truncateForLog(string(body)))
	}
	if len(manifest.Models) != 4 {
		t.Fatalf("manifest 模型数 = %d，期望 4；body=%s", len(manifest.Models), truncateForLog(string(body)))
	}

	expected := map[string]bool{
		modelText:       true,
		modelImage:      true,
		modelTool:       true,
		modelDisconnect: true,
	}
	seen := make(map[string]bool, len(manifest.Models))
	for index, model := range manifest.Models {
		for _, field := range codexModelFields {
			if _, ok := model[field]; !ok {
				t.Errorf("manifest[%d] 缺少字段 %q", index, field)
			}
		}

		var slug string
		if err := json.Unmarshal(model["slug"], &slug); err != nil {
			t.Fatalf("manifest[%d].slug 不是字符串：%v", index, err)
		}
		if !expected[slug] {
			t.Errorf("manifest 出现未预期模型 slug %q", slug)
		}
		if seen[slug] {
			t.Errorf("manifest 模型 slug %q 重复", slug)
		}
		seen[slug] = true

		var contextWindow int64
		if err := json.Unmarshal(model["context_window"], &contextWindow); err != nil || contextWindow <= 0 {
			t.Errorf("manifest[%d].context_window = %s（解析错误 %v），期望正数", index, model["context_window"], err)
		}
		var maxContextWindow int64
		if err := json.Unmarshal(model["max_context_window"], &maxContextWindow); err != nil || maxContextWindow <= 0 {
			t.Errorf("manifest[%d].max_context_window = %s（解析错误 %v），期望正数", index, model["max_context_window"], err)
		}

		var reasoningLevels []map[string]any
		if err := json.Unmarshal(model["supported_reasoning_levels"], &reasoningLevels); err != nil {
			t.Errorf("manifest[%d].supported_reasoning_levels 不是数组：%v", index, err)
		} else if len(reasoningLevels) == 0 {
			t.Errorf("manifest[%d].supported_reasoning_levels 不能为空", index)
		}

		var modalities []string
		if err := json.Unmarshal(model["input_modalities"], &modalities); err != nil {
			t.Errorf("manifest[%d].input_modalities 不是字符串数组：%v", index, err)
		} else {
			hasText, hasImage := false, false
			for _, modality := range modalities {
				switch modality {
				case "text":
					hasText = true
				case "image":
					hasImage = true
				}
			}
			if !hasText || !hasImage {
				t.Errorf("manifest[%d].input_modalities = %v，期望包含 text 与 image", index, modalities)
			}
		}

		var messages map[string]json.RawMessage
		if err := json.Unmarshal(model["model_messages"], &messages); err != nil {
			t.Errorf("manifest[%d].model_messages 不是对象：%v", index, err)
		} else {
			for _, field := range codexModelMessageFields {
				if _, ok := messages[field]; !ok {
					t.Errorf("manifest[%d].model_messages 缺少字段 %q", index, field)
				}
			}
		}
	}

	for slug := range expected {
		if !seen[slug] {
			t.Errorf("manifest 缺少模型 %q", slug)
		}
	}
}

func assertCodexEndpoints(t *testing.T, requests []recordedRequest) {
	t.Helper()
	sawModels := false
	sawResponses := false
	for _, request := range requests {
		if upgrade := request.Headers.Get("Upgrade"); upgrade != "" {
			t.Fatalf("观察到 WebSocket Upgrade 头 %q；Codex 配置要求 supports_websockets=false", upgrade)
		}
		if strings.Contains(strings.ToLower(request.Headers.Get("Connection")), "upgrade") {
			t.Fatalf("观察到 Connection: upgrade；不允许 WebSocket")
		}
		if request.Status == http.StatusSwitchingProtocols {
			t.Fatalf("观察到 101 Switching Protocols；不允许 WebSocket")
		}

		switch request.Path {
		case "/v1/models":
			if request.Method != http.MethodGet {
				t.Fatalf("模型发现方法 = %s，期望 GET", request.Method)
			}
			if request.Status != http.StatusOK {
				t.Fatalf("模型发现状态 = %d，期望 200；Codex 没有拿到完整 manifest", request.Status)
			}
			if got := request.Headers.Get("Authorization"); got != "Bearer "+testClientKey {
				t.Fatalf("模型发现 Authorization = %q，期望测试客户端 Key", got)
			}
			manifestBody := string(request.Body)
			for _, required := range []string{"supported_reasoning_levels", "context_window", "input_modalities"} {
				if !strings.Contains(manifestBody, required) {
					t.Fatalf("模型发现响应缺少 manifest 字段 %q：%s", required, truncateForLog(manifestBody))
				}
			}
			sawModels = true
		case "/v1/responses":
			if request.Method != http.MethodPost {
				t.Fatalf("Responses 方法 = %s，期望 POST", request.Method)
			}
			if accept := request.Headers.Get("Accept"); !strings.Contains(accept, "text/event-stream") {
				t.Errorf("Responses 请求 Accept = %q，期望 text/event-stream", accept)
			}
			sawResponses = true
		default:
			t.Fatalf(
				"出现未预期 endpoint %s %s（状态 %d）；全部请求：%+v",
				request.Method, request.Path, request.Status, requestSummary(requests),
			)
		}
	}
	if !sawModels || !sawResponses {
		t.Fatalf("未同时命中 /v1/models 与 /v1/responses；全部请求：%+v", requestSummary(requests))
	}
}

func assertUpstreamRequest(t *testing.T, request fakeRequest, wantModel string) {
	t.Helper()
	if request.Method != http.MethodPost || request.Path != "/v1/responses" {
		t.Fatalf("假上游收到 %s %s，期望 POST /v1/responses", request.Method, request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+fakeUpstreamAPIKey {
		t.Fatalf("上游 Authorization = %q，期望使用渠道 Key 且不转发客户端 Key", got)
	}

	var body struct {
		Model  string            `json:"model"`
		Stream bool              `json:"stream"`
		Input  []json.RawMessage `json:"input"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("解析上游请求体失败：%v", err)
	}
	if body.Model != wantModel {
		t.Fatalf("上游 model = %q，期望映射后的 %q", body.Model, wantModel)
	}
	if !body.Stream {
		t.Fatalf("上游请求 stream = false，期望 true")
	}
	if len(body.Input) == 0 {
		t.Fatalf("上游请求 input 为空")
	}
	if len(body.Tools) == 0 {
		t.Fatalf("上游请求 tools 为空；Codex 应下发工具定义")
	}
}

func assertSSEResponse(t *testing.T, request recordedRequest, wantText string, wantCompleted bool) {
	t.Helper()
	if request.Status != http.StatusOK {
		t.Fatalf("SSE 响应状态 = %d，期望 200", request.Status)
	}
	if contentType := request.ResponseHeader.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("SSE 响应 Content-Type = %q，期望 text/event-stream", contentType)
	}
	body := string(request.Body)
	if wantCompleted {
		if !strings.Contains(body, "event: response.completed") {
			t.Fatalf("SSE 响应缺少 response.completed：%s", truncateForLog(body))
		}
	}
	if !strings.Contains(body, wantText) {
		t.Fatalf("SSE 响应缺少最终文本 %q：%s", wantText, truncateForLog(body))
	}
}

func requestsByPath(requests []recordedRequest, method, path string) []recordedRequest {
	out := make([]recordedRequest, 0)
	for _, request := range requests {
		if request.Method == method && request.Path == path {
			out = append(out, request)
		}
	}
	return out
}

func requestSummary(requests []recordedRequest) []string {
	out := make([]string, 0, len(requests))
	for _, request := range requests {
		out = append(out, fmt.Sprintf("%s %s -> %d", request.Method, request.Path, request.Status))
	}
	return out
}

func truncateForLog(text string) string {
	const limit = 2000
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "...(truncated)"
}
