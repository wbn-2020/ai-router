package adminapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

// T22 调试 API 测试：成功、失败、超时、错误脱敏与候选选择。

type debugUpstreamCall struct {
	Path          string
	Authorization string
	Body          map[string]any
}

// debugUpstreamStub 是接收探针请求的假上游；按调用顺序记录路径、鉴权头与
// JSON body。respond 为 nil 时返回 200 + 最小 JSON。
type debugUpstreamStub struct {
	server  *httptest.Server
	mu      sync.Mutex
	calls   []debugUpstreamCall
	respond func(http.ResponseWriter, *http.Request, []byte)
}

func newDebugUpstreamStub(
	t *testing.T,
	respond func(http.ResponseWriter, *http.Request, []byte),
) *debugUpstreamStub {
	t.Helper()
	stub := &debugUpstreamStub{respond: respond}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		stub.mu.Lock()
		stub.calls = append(stub.calls, debugUpstreamCall{
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			Body:          body,
		})
		stub.mu.Unlock()

		if stub.respond != nil {
			stub.respond(w, r, raw)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_debug","object":"response","status":"completed","output":[]}`))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *debugUpstreamStub) URL() string {
	return s.server.URL
}

func (s *debugUpstreamStub) snapshot() []debugUpstreamCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make([]debugUpstreamCall, len(s.calls))
	copy(calls, s.calls)
	return calls
}

func createDebugChannel(
	t *testing.T,
	api *API,
	cookie *http.Cookie,
	name, baseURL, protocol string,
	priority int,
	enabled bool,
) channelView {
	t.Helper()
	body := validChannelBody(name)
	body["base_url"] = baseURL
	body["protocol"] = protocol
	body["priority"] = priority
	body["enabled"] = enabled
	return createChannel(t, api, cookie, body)
}

func createDebugRoute(
	t *testing.T,
	api *API,
	cookie *http.Cookie,
	clientModel string,
	channelID int64,
	upstreamModel string,
	priority int,
	enabled bool,
) modelRouteView {
	t.Helper()
	body := validModelRouteBody(clientModel, channelID)
	body["upstream_model"] = upstreamModel
	body["priority"] = priority
	body["enabled"] = enabled
	return createModelRoute(t, api, cookie, body)
}

type debugHealth struct {
	Status    string
	LatencyMS *int
	At        *time.Time
	LastError string
}

func readDebugHealth(t *testing.T, st *store.Store, id int64) debugHealth {
	t.Helper()
	var (
		status  sql.NullString
		latency sql.NullInt64
		at      sql.NullString
		lastErr sql.NullString
	)
	if err := st.DB().QueryRow(`
		SELECT last_health_status, last_health_latency_ms, last_health_at, last_error
		FROM channels WHERE id = ?`, id,
	).Scan(&status, &latency, &at, &lastErr); err != nil {
		t.Fatalf("读取渠道健康状态: %v", err)
	}

	health := debugHealth{Status: status.String, LastError: lastErr.String}
	if latency.Valid {
		value := int(latency.Int64)
		health.LatencyMS = &value
	}
	if at.Valid && at.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, at.String)
		if err != nil {
			t.Fatalf("解析 last_health_at: %v", err)
		}
		health.At = &parsed
	}
	return health
}

func assertNoDebugSecrets(t *testing.T, body string, secrets ...string) {
	t.Helper()
	for _, value := range secrets {
		if value == "" {
			continue
		}
		if strings.Contains(body, value) {
			t.Fatalf("响应泄漏敏感值 %q：%s", value, body)
		}
	}
}

func debugChannelTestPath(id int64) string {
	return "/admin/api/debug/channels/" + intString(id) + "/test"
}

func debugModelTestPath(model string) string {
	return "/admin/api/debug/models/" + model + "/test"
}

// TestDebugChannelTest 覆盖渠道测试成功、显式模型、健康状态写回与输入错误。
func TestDebugChannelTest(t *testing.T) {
	api, st, _, cookie, logs := newChannelsAPI(t)
	stub := newDebugUpstreamStub(t, nil)

	channel := createDebugChannel(t, api, cookie,
		"debug-primary", stub.URL()+"/v1", store.ChannelProtocolOpenAIResponses, 10, true)
	createDebugRoute(t, api, cookie, "gpt-debug", channel.ID, "upstream-gpt", 0, true)
	createDebugRoute(t, api, cookie, "other-debug", channel.ID, "other-upstream", 1, true)

	rec := apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("渠道测试 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
	result := decodeBody[debugTestResponse](t, rec)
	if result.Status != upstream.HealthStatusHealthy {
		t.Errorf("status = %q，期望 healthy", result.Status)
	}
	if result.HTTPStatus != http.StatusOK {
		t.Errorf("http_status = %d，期望 200", result.HTTPStatus)
	}
	if result.LatencyMS < 0 {
		t.Errorf("latency_ms = %d，不能为负", result.LatencyMS)
	}
	if result.Error != "" {
		t.Errorf("error = %q，期望为空", result.Error)
	}
	if result.ChannelID != channel.ID || result.ChannelName != "debug-primary" {
		t.Errorf("channel = %d/%q", result.ChannelID, result.ChannelName)
	}
	if result.Model != "gpt-debug" || result.UpstreamModel != "upstream-gpt" {
		t.Errorf("model = %q, upstream_model = %q", result.Model, result.UpstreamModel)
	}
	wantLink := "/admin/requests?channel_id=" + intString(channel.ID) + "&model=gpt-debug"
	if result.RequestLink != wantLink {
		t.Errorf("request_link = %q，期望 %q", result.RequestLink, wantLink)
	}

	calls := stub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("上游调用次数 = %d，期望 1", len(calls))
	}
	call := calls[0]
	if call.Path != "/v1/responses" {
		t.Errorf("上游路径 = %q，期望 /v1/responses", call.Path)
	}
	if call.Authorization != "Bearer "+testChannelKey {
		t.Errorf("Authorization = %q，期望上游 Key", call.Authorization)
	}
	if call.Body["model"] != "upstream-gpt" {
		t.Errorf("探针 model = %v，期望 upstream-gpt", call.Body["model"])
	}
	if call.Body["input"] != "ping" {
		t.Errorf("探针 input = %v，期望 ping", call.Body["input"])
	}

	health := readDebugHealth(t, st, channel.ID)
	if health.Status != upstream.HealthStatusHealthy || health.LastError != "" {
		t.Errorf("健康状态 = %+v，期望 healthy 且无错误", health)
	}
	if health.LatencyMS == nil || *health.LatencyMS < 0 {
		t.Errorf("last_health_latency_ms = %v，期望非负", health.LatencyMS)
	}
	if health.At == nil {
		t.Error("last_health_at 未写入")
	}

	detail := apiRequest(t, api, http.MethodGet,
		"/admin/api/channels/"+intString(channel.ID), nil, "", cookie)
	if detail.Code != http.StatusOK {
		t.Fatalf("渠道详情 = %d（body=%s）", detail.Code, detail.Body.String())
	}
	view := decodeBody[channelView](t, detail)
	if view.LastHealthStatus != upstream.HealthStatusHealthy ||
		view.LastHealthLatencyMS == nil || view.LastHealthAt == nil {
		t.Errorf("渠道视图未反映健康状态：%+v", view)
	}

	// 显式 model 选择该渠道上的另一条映射。
	rec = apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID),
		map[string]any{"model": "other-debug"}, testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("显式模型测试 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	explicit := decodeBody[debugTestResponse](t, rec)
	if explicit.Model != "other-debug" || explicit.UpstreamModel != "other-upstream" {
		t.Errorf("显式模型结果 = %+v", explicit)
	}
	calls = stub.snapshot()
	if len(calls) != 2 || calls[1].Body["model"] != "other-upstream" {
		t.Fatalf("第二次上游调用 = %+v", calls)
	}

	// 未映射的模型返回明确错误，且不调用上游。
	rec = apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID),
		map[string]any{"model": "missing-model"}, testOrigin, cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_route_not_found") {
		t.Fatalf("未映射模型 = %d（body=%s）", rec.Code, rec.Body.String())
	}

	// 没有任何映射的渠道返回明确错误。
	empty := createDebugChannel(t, api, cookie,
		"debug-empty", stub.URL()+"/empty", store.ChannelProtocolOpenAIResponses, 20, true)
	rec = apiRequest(t, api, http.MethodPost, debugChannelTestPath(empty.ID), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "channel_has_no_mapping") {
		t.Fatalf("无映射渠道 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	if len(stub.snapshot()) != 2 {
		t.Fatalf("错误路径不应调用上游：%+v", stub.snapshot())
	}

	// 非法模型与不存在的渠道。
	rec = apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID),
		map[string]any{"model": "bad\nmodel"}, testOrigin, cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_model") {
		t.Fatalf("非法模型 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	rec = apiRequest(t, api, http.MethodPost, debugChannelTestPath(99999), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在渠道 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	rec = apiRequest(t, api, http.MethodPost, "/admin/api/debug/channels/not-a-number/test",
		nil, testOrigin, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法渠道 ID = %d（body=%s）", rec.Code, rec.Body.String())
	}
	rec = apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID),
		"{not-json", testOrigin, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON = %d（body=%s）", rec.Code, rec.Body.String())
	}

	assertNoDebugSecrets(t, rec.Body.String(), testChannelKey)
	assertNoDebugSecrets(t, logs.String(), testChannelKey)
}

// TestDebugModelTest 覆盖模型测试的候选选择、启用过滤与无映射错误。
func TestDebugModelTest(t *testing.T) {
	api, st, _, cookie, logs := newChannelsAPI(t)
	stub := newDebugUpstreamStub(t, nil)

	// 渠道优先级：preferred(10) 应先于 fallback(20)；disabled(0) 即使更高
	// 也不能被选中。
	preferred := createDebugChannel(t, api, cookie,
		"model-preferred", stub.URL()+"/preferred", store.ChannelProtocolOpenAIResponses, 10, true)
	fallback := createDebugChannel(t, api, cookie,
		"model-fallback", stub.URL()+"/fallback", store.ChannelProtocolOpenAIResponses, 20, true)
	disabled := createDebugChannel(t, api, cookie,
		"model-disabled", stub.URL()+"/disabled", store.ChannelProtocolOpenAIResponses, 0, false)
	disabledRoute := createDebugChannel(t, api, cookie,
		"model-disabled-route", stub.URL()+"/disabled-route", store.ChannelProtocolOpenAIResponses, 0, true)

	createDebugRoute(t, api, cookie, "shared-model", preferred.ID, "preferred-upstream", 0, true)
	createDebugRoute(t, api, cookie, "shared-model", fallback.ID, "fallback-upstream", 0, true)
	createDebugRoute(t, api, cookie, "shared-model", disabled.ID, "disabled-upstream", 0, true)
	createDebugRoute(t, api, cookie, "shared-model", disabledRoute.ID, "disabled-route-upstream", 0, false)
	createDebugRoute(t, api, cookie, "vendor/model", preferred.ID, "vendor-upstream", 0, true)

	rec := apiRequest(t, api, http.MethodPost, debugModelTestPath("shared-model"), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("模型测试 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
	result := decodeBody[debugTestResponse](t, rec)
	if result.Status != upstream.HealthStatusHealthy || result.HTTPStatus != http.StatusOK {
		t.Errorf("结果 = %+v，期望 healthy/200", result)
	}
	if result.ChannelID != preferred.ID || result.ChannelName != "model-preferred" {
		t.Errorf("候选渠道 = %d/%q，期望 preferred", result.ChannelID, result.ChannelName)
	}
	if result.Model != "shared-model" || result.UpstreamModel != "preferred-upstream" {
		t.Errorf("模型 = %q, upstream = %q", result.Model, result.UpstreamModel)
	}
	wantLink := "/admin/requests?channel_id=" + intString(preferred.ID) + "&model=shared-model"
	if result.RequestLink != wantLink {
		t.Errorf("request_link = %q，期望 %q", result.RequestLink, wantLink)
	}

	calls := stub.snapshot()
	if len(calls) != 1 || calls[0].Path != "/preferred/responses" {
		t.Fatalf("上游调用 = %+v，期望只命中 preferred", calls)
	}

	preferredHealth := readDebugHealth(t, st, preferred.ID)
	if preferredHealth.Status != upstream.HealthStatusHealthy {
		t.Errorf("preferred 健康状态 = %+v", preferredHealth)
	}
	if fallbackHealth := readDebugHealth(t, st, fallback.ID); fallbackHealth.Status != "" {
		t.Errorf("fallback 不应被探测：%+v", fallbackHealth)
	}

	// 含斜杠的模型名（前端 encodeURIComponent）必须能匹配。
	rec = apiRequest(t, api, http.MethodPost, debugModelTestPath("vendor%2Fmodel"), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("斜杠模型测试 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	slashResult := decodeBody[debugTestResponse](t, rec)
	if slashResult.Model != "vendor/model" || slashResult.UpstreamModel != "vendor-upstream" {
		t.Errorf("斜杠模型结果 = %+v", slashResult)
	}
	calls = stub.snapshot()
	if len(calls) != 2 || calls[1].Body["model"] != "vendor-upstream" {
		t.Fatalf("斜杠模型上游调用 = %+v", calls)
	}

	// 无映射模型返回明确错误。
	rec = apiRequest(t, api, http.MethodPost, debugModelTestPath("not-mapped"), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_not_available") {
		t.Fatalf("无映射模型 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "没有启用的映射渠道") {
		t.Errorf("无映射错误缺少明确文案：%s", rec.Body.String())
	}

	// 只有 anthropic_messages 映射的模型不参与第一版调试。
	anthropic := createDebugChannel(t, api, cookie,
		"model-anthropic", stub.URL()+"/anthropic", store.ChannelProtocolAnthropicMessages, 0, true)
	createDebugRoute(t, api, cookie, "anthropic-only", anthropic.ID, "anthropic-upstream", 0, true)
	rec = apiRequest(t, api, http.MethodPost, debugModelTestPath("anthropic-only"), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_not_available") {
		t.Fatalf("仅 anthropic 模型 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	if len(stub.snapshot()) != 2 {
		t.Fatalf("不应为 anthropic 渠道发起探针：%+v", stub.snapshot())
	}

	assertNoDebugSecrets(t, logs.String(), testChannelKey)
}

// TestDebugProbeTimeout 覆盖探针超时与健康状态写回。
func TestDebugProbeTimeout(t *testing.T) {
	original := newDebugProbeClient
	newDebugProbeClient = func() (*upstream.Client, error) {
		return upstream.NewClient(upstream.Options{RequestTimeout: 60 * time.Millisecond})
	}
	t.Cleanup(func() {
		newDebugProbeClient = original
	})

	api, st, _, cookie, logs := newChannelsAPI(t)
	stub := newDebugUpstreamStub(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})

	channel := createDebugChannel(t, api, cookie,
		"debug-timeout", stub.URL()+"/v1", store.ChannelProtocolOpenAIResponses, 10, true)
	createDebugRoute(t, api, cookie, "timeout-model", channel.ID, "timeout-upstream", 0, true)

	rec := apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("超时测试 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	result := decodeBody[debugTestResponse](t, rec)
	if result.Status != upstream.HealthStatusUnhealthy || result.HTTPStatus != 0 {
		t.Errorf("超时结果 = %+v，期望 unhealthy/0", result)
	}
	if !strings.Contains(result.Error, "超时") {
		t.Errorf("超时错误 = %q，期望包含“超时”", result.Error)
	}
	if result.UpstreamModel != "timeout-upstream" {
		t.Errorf("upstream_model = %q，期望返回调试所用的上游模型", result.UpstreamModel)
	}
	assertNoDebugSecrets(t, rec.Body.String(), testChannelKey)

	health := readDebugHealth(t, st, channel.ID)
	if health.Status != upstream.HealthStatusUnhealthy ||
		!strings.Contains(health.LastError, "超时") {
		t.Errorf("超时健康状态 = %+v", health)
	}
	if health.LatencyMS == nil || health.At == nil {
		t.Errorf("超时也应写入延迟与时间：%+v", health)
	}
	assertNoDebugSecrets(t, logs.String(), testChannelKey)
}

// TestDebugProbeErrorRedaction 覆盖上游非 2xx 时响应、健康状态与日志的脱敏。
func TestDebugProbeErrorRedaction(t *testing.T) {
	const upstreamSecret = "sk-live-upstream-secret-0123456789"
	api, st, _, cookie, logs := newChannelsAPI(t)
	stub := newDebugUpstreamStub(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"leaked ` + upstreamSecret + `"}}`))
	})

	baseURL := stub.URL() + "/v1?api_key=query-secret&api-version=2026-01-01"
	channel := createDebugChannel(t, api, cookie,
		"debug-redact", baseURL, store.ChannelProtocolOpenAIResponses, 10, true)
	createDebugRoute(t, api, cookie, "redact-model", channel.ID, "redact-upstream", 0, true)

	rec := apiRequest(t, api, http.MethodPost, debugChannelTestPath(channel.ID), nil,
		testOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("失败测试 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	result := decodeBody[debugTestResponse](t, rec)
	if result.Status != upstream.HealthStatusUnhealthy || result.HTTPStatus != http.StatusServiceUnavailable {
		t.Errorf("失败结果 = %+v，期望 unhealthy/503", result)
	}
	if !strings.Contains(result.Error, "503") || result.Error == "" {
		t.Errorf("失败错误 = %q，期望包含状态码", result.Error)
	}

	body := rec.Body.String()
	assertNoDebugSecrets(t, body,
		testChannelKey, upstreamSecret, "query-secret", "api_key", "api-version", stub.URL())
	assertNoDebugSecrets(t, logs.String(), testChannelKey, upstreamSecret, "query-secret")

	health := readDebugHealth(t, st, channel.ID)
	if health.Status != upstream.HealthStatusUnhealthy || health.LastError == "" {
		t.Errorf("失败健康状态 = %+v", health)
	}
	assertNoDebugSecrets(t, health.LastError,
		testChannelKey, upstreamSecret, "query-secret", "api_key")
}

// TestDebugRequiresSession 覆盖认证与同源校验。
func TestDebugRequiresSession(t *testing.T) {
	api, _, _, cookie, _ := newChannelsAPI(t)

	channelTest := apiRequest(t, api, http.MethodPost,
		"/admin/api/debug/channels/1/test", nil, testOrigin)
	if channelTest.Code != http.StatusUnauthorized {
		t.Fatalf("未登录渠道测试 = %d，期望 401（body=%s）", channelTest.Code, channelTest.Body.String())
	}
	assertNoStore(t, channelTest)

	modelTest := apiRequest(t, api, http.MethodPost,
		"/admin/api/debug/models/gpt-debug/test", nil, testOrigin)
	if modelTest.Code != http.StatusUnauthorized {
		t.Fatalf("未登录模型测试 = %d，期望 401（body=%s）", modelTest.Code, modelTest.Body.String())
	}

	crossOrigin := apiRequest(t, api, http.MethodPost,
		"/admin/api/debug/channels/1/test", nil, "http://evil.example", cookie)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("跨源渠道测试 = %d，期望 403（body=%s）", crossOrigin.Code, crossOrigin.Body.String())
	}
}
