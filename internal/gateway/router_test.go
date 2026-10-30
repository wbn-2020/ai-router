package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ai-router/internal/store"
)

const (
	testClientKey   = "sk-test-client-key-0123456789abcdef"
	testClientModel = "gpt-5"
)

// routeQuery 记录 Handler 对 RouteCatalog 的查询，用于断言协议与模型。
type routeQuery struct {
	model    string
	protocol string
}

type fakeRouteCatalog struct {
	models     []string
	listErr    error
	selectErr  error
	candidates map[string][]store.ModelRoute
	queries    []routeQuery
}

func routeKey(protocol, model string) string {
	return protocol + "\x00" + model
}

func (f *fakeRouteCatalog) ListModels(context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]string(nil), f.models...), nil
}

func (f *fakeRouteCatalog) ListByModelAndProtocol(
	_ context.Context,
	model, protocol string,
) ([]store.ModelRoute, error) {
	f.queries = append(f.queries, routeQuery{model: model, protocol: protocol})
	if f.selectErr != nil {
		return nil, f.selectErr
	}
	return append([]store.ModelRoute(nil), f.candidates[routeKey(protocol, model)]...), nil
}

// recordingForwarder 模拟 T20/T21 的代理入口，记录是否被调用以及路由上下文。
type recordingForwarder struct {
	calls   int
	hasInfo bool
	info    RequestInfo
	body    []byte
	path    string
	method  string
}

func (f *recordingForwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.info, f.hasInfo = RequestInfoFromContext(r.Context())
	f.path = r.URL.Path
	f.method = r.Method
	if r.Body != nil {
		f.body, _ = io.ReadAll(r.Body)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func newTestHandler(t *testing.T, catalog RouteCatalog) (*Handler, *recordingForwarder) {
	t.Helper()
	forwarder := &recordingForwarder{}
	handler, err := New(Options{
		ClientKey: testClientKey,
		Routes:    catalog,
		Forwarder: forwarder,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return handler, forwarder
}

func performRequest(
	handler http.Handler,
	method, target, body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func bearerHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testClientKey}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) APIError {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("错误响应 Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("错误响应 Cache-Control = %q, want no-store", got)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析错误响应失败：%v（body=%s）", err, rec.Body.String())
	}
	if len(raw) != 1 {
		t.Fatalf("错误响应顶层字段 = %d，期望仅 error（body=%s）", len(raw), rec.Body.String())
	}
	errorRaw, ok := raw["error"]
	if !ok {
		t.Fatalf("错误响应缺少 error：%s", rec.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(errorRaw, &fields); err != nil {
		t.Fatalf("解析 error 字段失败：%v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("error 字段数 = %d，期望 message/type/code（body=%s）", len(fields), rec.Body.String())
	}
	for _, key := range []string{"message", "type", "code"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("error 缺少 %s：%s", key, rec.Body.String())
		}
	}

	var envelope errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析 error envelope 失败：%v", err)
	}
	if envelope.Error.Message == "" || envelope.Error.Type == "" || envelope.Error.Code == "" {
		t.Fatalf("error envelope 字段为空：%+v", envelope.Error)
	}
	return envelope.Error
}

func assertNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestNewRejectsEmptyClientKey(t *testing.T) {
	for _, key := range []string{"", "   "} {
		if _, err := New(Options{ClientKey: key}); err == nil {
			t.Fatalf("New(ClientKey=%q) 未返回错误", key)
		}
	}
}

func TestHandlerAuthentication(t *testing.T) {
	catalog := func() *fakeRouteCatalog {
		return &fakeRouteCatalog{
			candidates: map[string][]store.ModelRoute{
				routeKey(ProtocolOpenAIResponses, testClientModel): {{ID: 1}},
			},
		}
	}

	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{
			name: "缺少 Key",
			want: http.StatusUnauthorized,
		},
		{
			name:    "只有管理端 cookie",
			headers: map[string]string{"Cookie": "gateway_admin_session=admin-token"},
			want:    http.StatusUnauthorized,
		},
		{
			name: "Bearer 错误 Key",
			headers: map[string]string{
				"Authorization": "Bearer sk-wrong-key-0123456789abcdef",
				"Cookie":        "gateway_admin_session=admin-token",
			},
			want: http.StatusUnauthorized,
		},
		{
			name:    "x-api-key 错误 Key",
			headers: map[string]string{"X-Api-Key": "sk-wrong-key-0123456789abcdef"},
			want:    http.StatusUnauthorized,
		},
		{
			name:    "非 Bearer scheme",
			headers: map[string]string{"Authorization": "Basic c2std3Jvbmc6c2std3Jvbmc="},
			want:    http.StatusUnauthorized,
		},
		{
			name:    "Bearer 正确 Key",
			headers: bearerHeaders(),
			want:    http.StatusOK,
		},
		{
			name:    "Bearer scheme 大小写不敏感",
			headers: map[string]string{"Authorization": "bearer " + testClientKey},
			want:    http.StatusOK,
		},
		{
			name:    "x-api-key 正确 Key",
			headers: map[string]string{"X-Api-Key": testClientKey},
			want:    http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, forwarder := newTestHandler(t, catalog())
			rec := performRequest(handler, http.MethodPost, PathResponses,
				`{"model":"gpt-5"}`, tc.headers)

			if rec.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d（body=%s）", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusOK {
				if forwarder.calls != 1 {
					t.Fatalf("Forwarder 调用 = %d, want 1", forwarder.calls)
				}
				return
			}

			if forwarder.calls != 0 {
				t.Fatalf("鉴权失败时 Forwarder 调用 = %d, want 0", forwarder.calls)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
			if got := rec.Header().Get("Set-Cookie"); got != "" {
				t.Errorf("客户端鉴权失败不应改写管理端 cookie：%q", got)
			}
			apiErr := decodeError(t, rec)
			if apiErr.Code != errorCodeInvalidAPIKey || apiErr.Type != errorTypeInvalidRequest {
				t.Errorf("鉴权错误 = %+v", apiErr)
			}
			for _, secret := range []string{testClientKey, "sk-wrong-key", "admin-token"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("401 响应泄漏凭据 %q：%s", secret, rec.Body.String())
				}
			}
		})
	}
}

// TestAuthenticationFailureIsUniform 校验缺失 Key 与错误 Key 返回同一 envelope，
// 避免客户端通过错误差异枚举 Key。
func TestAuthenticationFailureIsUniform(t *testing.T) {
	catalog := &fakeRouteCatalog{}
	handler, _ := newTestHandler(t, catalog)

	missing := performRequest(handler, http.MethodPost, PathResponses,
		`{"model":"gpt-5"}`, nil)
	bad := performRequest(handler, http.MethodPost, PathResponses,
		`{"model":"gpt-5"}`, map[string]string{
			"Authorization": "Bearer sk-wrong-key-0123456789abcdef",
		})

	if missing.Code != http.StatusUnauthorized || bad.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d/%d, want 401", missing.Code, bad.Code)
	}
	if missing.Body.String() != bad.Body.String() {
		t.Fatalf("401 响应不一致：missing=%s bad=%s", missing.Body.String(), bad.Body.String())
	}
}

func TestDataRoutesForwardWithProtocol(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		protocol string
		model    string
		endpoint Endpoint
	}{
		{
			name:     "Responses",
			path:     PathResponses,
			protocol: ProtocolOpenAIResponses,
			model:    testClientModel,
			endpoint: EndpointResponses,
		},
		{
			name:     "Codex Responses 别名",
			path:     PathCodexResponses,
			protocol: ProtocolOpenAIResponses,
			model:    testClientModel,
			endpoint: EndpointCodexResponses,
		},
		{
			name:     "Chat Completions",
			path:     PathChatCompletions,
			protocol: ProtocolOpenAIChat,
			model:    "chat-model",
			endpoint: EndpointChatCompletions,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &fakeRouteCatalog{
				candidates: map[string][]store.ModelRoute{
					routeKey(tc.protocol, tc.model): {{ID: 7, ClientModel: tc.model}},
				},
			}
			handler, forwarder := newTestHandler(t, catalog)
			body := fmt.Sprintf(
				`{"model":%q,"input":"hello","tools":[{"type":"function","name":"shell"}],`+
					`"input_image":{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"original"},`+
					`"unknown_field":{"nested":true}}`,
				tc.model,
			)
			headers := bearerHeaders()
			headers["Content-Type"] = "application/json"

			rec := performRequest(handler, http.MethodPost, tc.path, body, headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
			}
			if forwarder.calls != 1 {
				t.Fatalf("Forwarder 调用 = %d, want 1", forwarder.calls)
			}
			if !forwarder.hasInfo {
				t.Fatal("Forwarder 未收到 RequestInfo")
			}
			if forwarder.info.Protocol != tc.protocol ||
				forwarder.info.Endpoint != tc.endpoint ||
				forwarder.info.Model != tc.model {
				t.Fatalf("RequestInfo = %+v", forwarder.info)
			}
			if string(forwarder.body) != body {
				t.Fatalf("Forwarder 收到的 body 被改写：\n got=%s\nwant=%s", forwarder.body, body)
			}
			if forwarder.path != tc.path || forwarder.method != http.MethodPost {
				t.Fatalf("Forwarder 收到 %s %s", forwarder.method, forwarder.path)
			}
			if len(catalog.queries) != 1 ||
				catalog.queries[0].model != tc.model ||
				catalog.queries[0].protocol != tc.protocol {
				t.Fatalf("候选查询 = %+v", catalog.queries)
			}
		})
	}
}

func TestUnsupportedEndpointsReturnNotFound(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/embeddings"},
		{http.MethodPost, "/v1/images/generations"},
		{http.MethodPost, "/v1/audio/transcriptions"},
		{http.MethodPost, "/v1/videos"},
		{http.MethodGet, "/v1/responses/"},
		{http.MethodGet, "/v1/chat/completions/"},
		{http.MethodGet, "/v1/models/"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			catalog := &fakeRouteCatalog{
				candidates: map[string][]store.ModelRoute{
					routeKey(ProtocolOpenAIResponses, testClientModel): {{ID: 1}},
				},
			}
			handler, forwarder := newTestHandler(t, catalog)
			rec := performRequest(handler, tc.method, tc.path,
				`{"model":"gpt-5"}`, bearerHeaders())

			if rec.Code != http.StatusNotFound {
				t.Fatalf("状态码 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
			}
			if got := decodeError(t, rec); got.Code != errorCodeNotFound {
				t.Errorf("错误码 = %q, want %q", got.Code, errorCodeNotFound)
			}
			if forwarder.calls != 0 || len(catalog.queries) != 0 {
				t.Fatalf("未注册端点触发了转发/查询：forwarder=%d queries=%+v",
					forwarder.calls, catalog.queries)
			}
		})
	}

	t.Run("未注册端点不要求客户端 Key", func(t *testing.T) {
		handler, _ := newTestHandler(t, &fakeRouteCatalog{})
		rec := performRequest(handler, http.MethodPost, "/v1/embeddings",
			`{"model":"text-embedding-3-small"}`, nil)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeNotFound {
			t.Errorf("错误码 = %q", got.Code)
		}
	})
}

func TestUnsupportedMethodsReturnMethodNotAllowed(t *testing.T) {
	tests := []struct {
		method string
		path   string
		allow  string
	}{
		{http.MethodGet, PathResponses, http.MethodPost},
		{http.MethodGet, PathCodexResponses, http.MethodPost},
		{http.MethodPost, PathModels, http.MethodGet},
		{http.MethodPost, PathCodexModels, http.MethodGet},
		{http.MethodDelete, PathModels + "/gpt-5", http.MethodGet},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			catalog := &fakeRouteCatalog{}
			handler, forwarder := newTestHandler(t, catalog)
			rec := performRequest(handler, tc.method, tc.path, "", bearerHeaders())

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("状态码 = %d, want 405（body=%s）", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tc.allow {
				t.Errorf("Allow = %q, want %q", got, tc.allow)
			}
			if got := decodeError(t, rec); got.Code != errorCodeMethodNotAllowed {
				t.Errorf("错误码 = %q", got.Code)
			}
			if forwarder.calls != 0 || len(catalog.queries) != 0 {
				t.Fatalf("405 触发了转发/查询")
			}
		})
	}
}

func TestModelResolutionFailures(t *testing.T) {
	t.Run("无映射返回 model_not_available 且不调用 Forwarder", func(t *testing.T) {
		catalog := &fakeRouteCatalog{candidates: map[string][]store.ModelRoute{}}
		handler, forwarder := newTestHandler(t, catalog)
		rec := performRequest(handler, http.MethodPost, PathResponses,
			`{"model":"gpt-5"}`, bearerHeaders())

		if rec.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeModelNotAvailable {
			t.Errorf("错误码 = %q, want %q", got.Code, errorCodeModelNotAvailable)
		}
		if forwarder.calls != 0 {
			t.Fatalf("Forwarder 调用 = %d, want 0", forwarder.calls)
		}
		if len(catalog.queries) != 1 ||
			catalog.queries[0].model != "gpt-5" ||
			catalog.queries[0].protocol != ProtocolOpenAIResponses {
			t.Fatalf("候选查询 = %+v", catalog.queries)
		}
	})

	t.Run("协议不匹配返回 404，匹配协议可用", func(t *testing.T) {
		catalog := &fakeRouteCatalog{
			candidates: map[string][]store.ModelRoute{
				routeKey(ProtocolOpenAIChat, "gpt-5"): {{ID: 1}},
			},
		}
		handler, forwarder := newTestHandler(t, catalog)

		responses := performRequest(handler, http.MethodPost, PathResponses,
			`{"model":"gpt-5"}`, bearerHeaders())
		if responses.Code != http.StatusNotFound {
			t.Fatalf("Responses 状态码 = %d, want 404（body=%s）",
				responses.Code, responses.Body.String())
		}
		if got := decodeError(t, responses); got.Code != errorCodeModelNotAvailable {
			t.Errorf("Responses 错误码 = %q", got.Code)
		}
		if forwarder.calls != 0 {
			t.Fatalf("协议不匹配时 Forwarder 调用 = %d, want 0", forwarder.calls)
		}
		if len(catalog.queries) != 1 || catalog.queries[0].protocol != ProtocolOpenAIResponses {
			t.Fatalf("Responses 查询 = %+v", catalog.queries)
		}

		chat := performRequest(handler, http.MethodPost, PathChatCompletions,
			`{"model":"gpt-5"}`, bearerHeaders())
		if chat.Code != http.StatusOK || forwarder.calls != 1 {
			t.Fatalf("Chat 状态码 = %d, forwarder=%d（body=%s）",
				chat.Code, forwarder.calls, chat.Body.String())
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		catalog := &fakeRouteCatalog{}
		handler, forwarder := newTestHandler(t, catalog)
		rec := performRequest(handler, http.MethodPost, PathResponses,
			`{"model":`, bearerHeaders())

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d, want 400（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeInvalidJSON {
			t.Errorf("错误码 = %q", got.Code)
		}
		if forwarder.calls != 0 || len(catalog.queries) != 0 {
			t.Fatalf("非法 JSON 触发了转发/查询")
		}
	})

	t.Run("缺少 model", func(t *testing.T) {
		catalog := &fakeRouteCatalog{}
		handler, forwarder := newTestHandler(t, catalog)
		rec := performRequest(handler, http.MethodPost, PathResponses,
			`{"input":"hello"}`, bearerHeaders())

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d, want 400（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeMissingModel {
			t.Errorf("错误码 = %q", got.Code)
		}
		if forwarder.calls != 0 || len(catalog.queries) != 0 {
			t.Fatalf("缺少 model 触发了转发/查询")
		}
	})

	t.Run("空请求体", func(t *testing.T) {
		catalog := &fakeRouteCatalog{}
		handler, _ := newTestHandler(t, catalog)
		rec := performRequest(handler, http.MethodPost, PathResponses, "", bearerHeaders())

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d, want 400（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeMissingModel {
			t.Errorf("错误码 = %q", got.Code)
		}
		if len(catalog.queries) != 0 {
			t.Fatalf("空请求体触发了候选查询")
		}
	})

	t.Run("仓储错误不泄漏上游信息", func(t *testing.T) {
		catalog := &fakeRouteCatalog{
			selectErr: errors.New(
				"dial https://upstream.example/v1/responses?key=sk-upstream-secret failed"),
		}
		handler, forwarder := newTestHandler(t, catalog)
		rec := performRequest(handler, http.MethodPost, PathResponses,
			`{"model":"gpt-5"}`, bearerHeaders())

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("状态码 = %d, want 500（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeInternal || got.Type != errorTypeServer {
			t.Errorf("错误 envelope = %+v", got)
		}
		for _, secret := range []string{
			"upstream.example", "sk-upstream-secret", testClientKey, "SELECT", "sqlite",
		} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("500 响应泄漏 %q：%s", secret, rec.Body.String())
			}
		}
		if forwarder.calls != 0 {
			t.Fatalf("仓储错误后 Forwarder 调用 = %d, want 0", forwarder.calls)
		}
	})

	t.Run("Routes 未配置返回 503", func(t *testing.T) {
		forwarder := &recordingForwarder{}
		handler, err := New(Options{ClientKey: testClientKey, Forwarder: forwarder})
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		rec := performRequest(handler, http.MethodPost, PathResponses,
			`{"model":"gpt-5"}`, bearerHeaders())

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("状态码 = %d, want 503（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeUnavailable {
			t.Errorf("错误码 = %q", got.Code)
		}
		if forwarder.calls != 0 {
			t.Fatalf("Routes 未配置时 Forwarder 调用 = %d, want 0", forwarder.calls)
		}
	})

	t.Run("Forwarder 未配置返回 503", func(t *testing.T) {
		catalog := &fakeRouteCatalog{
			candidates: map[string][]store.ModelRoute{
				routeKey(ProtocolOpenAIResponses, "gpt-5"): {{ID: 1}},
			},
		}
		handler, err := New(Options{ClientKey: testClientKey, Routes: catalog})
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		rec := performRequest(handler, http.MethodPost, PathResponses,
			`{"model":"gpt-5"}`, bearerHeaders())

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("状态码 = %d, want 503（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeUnavailable {
			t.Errorf("错误码 = %q", got.Code)
		}
	})
}

func TestModelsEndpoint(t *testing.T) {
	catalog := func() *fakeRouteCatalog {
		return &fakeRouteCatalog{models: []string{"gpt-5", "o3"}}
	}

	t.Run("鉴权失败返回 401", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog())
		rec := performRequest(handler, http.MethodGet, PathModels, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d, want 401（body=%s）", rec.Code, rec.Body.String())
		}
		decodeError(t, rec)
	})

	t.Run("列表只包含启用模型且不泄漏上游信息", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog())
		rec := performRequest(handler, http.MethodGet, PathModels, "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("Content-Type = %q", got)
		}

		var response modelListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("解析模型列表失败：%v", err)
		}
		if response.Object != "list" || len(response.Data) != 2 {
			t.Fatalf("模型列表 = %+v", response)
		}
		if response.Data[0].ID != "gpt-5" || response.Data[1].ID != "o3" {
			t.Fatalf("模型顺序 = %+v", response.Data)
		}
		for _, card := range response.Data {
			if card.Object != "model" || card.Created != modelCreatedUnix ||
				card.OwnedBy != modelOwnedBy {
				t.Fatalf("模型条目 = %+v", card)
			}
		}
		for _, secret := range []string{testClientKey, "upstream", "key_ciphertext"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("模型列表泄漏 %q：%s", secret, rec.Body.String())
			}
		}
	})

	t.Run("单模型详情", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog())
		rec := performRequest(handler, http.MethodGet, PathModels+"/gpt-5", "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		var card modelCard
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Fatalf("解析模型条目失败：%v", err)
		}
		if card.ID != "gpt-5" || card.Object != "model" ||
			card.Created != modelCreatedUnix || card.OwnedBy != modelOwnedBy {
			t.Fatalf("模型条目 = %+v", card)
		}
		if strings.Contains(rec.Body.String(), `"data"`) {
			t.Fatalf("单模型响应不应包含 data：%s", rec.Body.String())
		}
	})

	t.Run("未知模型返回 model_not_available", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog())
		rec := performRequest(handler, http.MethodGet, PathModels+"/missing", "", bearerHeaders())

		if rec.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeModelNotAvailable {
			t.Errorf("错误码 = %q", got.Code)
		}
	})

	t.Run("单模型详情忽略 client_version", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog())
		rec := performRequest(handler, http.MethodGet,
			PathModels+"/gpt-5?client_version=0.164.0", "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
		}
		var card modelCard
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Fatalf("解析模型条目失败：%v", err)
		}
		if card.ID != "gpt-5" || card.Object != "model" {
			t.Fatalf("模型条目 = %+v", card)
		}
	})

	t.Run("模型列表仓储错误返回 500", func(t *testing.T) {
		handler, _ := newTestHandler(t, &fakeRouteCatalog{
			listErr: errors.New("SELECT failed at https://upstream.example"),
		})
		rec := performRequest(handler, http.MethodGet, PathModels, "", bearerHeaders())

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("状态码 = %d, want 500（body=%s）", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != errorCodeInternal {
			t.Errorf("错误码 = %q", got.Code)
		}
		if strings.Contains(rec.Body.String(), "upstream.example") ||
			strings.Contains(rec.Body.String(), "SELECT") {
			t.Fatalf("500 响应泄漏仓储错误：%s", rec.Body.String())
		}
	})
}

func TestCodexManifestPlaceholder(t *testing.T) {
	catalog := func(models []string) *fakeRouteCatalog {
		return &fakeRouteCatalog{models: models}
	}

	t.Run("client_version 走 manifest 分支", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog([]string{"gpt-5", "o3"}))
		rec := performRequest(handler, http.MethodGet,
			PathModels+"?client_version=0.164.0", "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		if strings.Contains(rec.Body.String(), `"data"`) {
			t.Fatalf("manifest 不应包含 data：%s", rec.Body.String())
		}

		var manifest codexManifestPlaceholder
		if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("解析 manifest 失败：%v", err)
		}
		if len(manifest.Models) != 2 ||
			manifest.Models[0].Slug != "gpt-5" ||
			manifest.Models[1].Slug != "o3" {
			t.Fatalf("manifest = %+v", manifest)
		}
		if manifest.Models[0].DisplayName != "gpt-5" {
			t.Errorf("display_name = %q", manifest.Models[0].DisplayName)
		}
		for _, secret := range []string{testClientKey, "upstream", "channel"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("manifest 泄漏 %q：%s", secret, rec.Body.String())
			}
		}
	})

	t.Run("backend-api/codex/models 始终返回 manifest", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog([]string{"gpt-5"}))
		rec := performRequest(handler, http.MethodGet, PathCodexModels, "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
		var manifest codexManifestPlaceholder
		if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("解析 manifest 失败：%v", err)
		}
		if len(manifest.Models) != 1 || manifest.Models[0].Slug != "gpt-5" {
			t.Fatalf("manifest = %+v", manifest)
		}
	})

	t.Run("manifest 需要客户端 Key", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog([]string{"gpt-5"}))
		rec := performRequest(handler, http.MethodGet, PathCodexModels, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d, want 401（body=%s）", rec.Code, rec.Body.String())
		}
		decodeError(t, rec)
	})

	t.Run("空模型列表使用空数组", func(t *testing.T) {
		handler, _ := newTestHandler(t, catalog(nil))
		rec := performRequest(handler, http.MethodGet,
			PathModels+"?client_version=0.164.0", "", bearerHeaders())

		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"models":[]`) {
			t.Fatalf("空 manifest 不是空数组：%s", rec.Body.String())
		}
	})
}

// TestModelsEndpointUsesEnabledStoreRoutes 通过真实 SQLite 仓储验证
// /v1/models 与 manifest 只读取启用映射，且不暴露上游模型/base URL/Key。
func TestModelsEndpointUsesEnabledStoreRoutes(t *testing.T) {
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
		UpstreamModel: "disabled-upstream",
		Priority:      20,
		Enabled:       false,
	}); err != nil {
		t.Fatalf("创建禁用映射失败：%v", err)
	}

	handler, err := New(Options{ClientKey: testClientKey, Routes: routes})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	list := performRequest(handler, http.MethodGet, PathModels, "", bearerHeaders())
	if list.Code != http.StatusOK {
		t.Fatalf("模型列表 = %d（body=%s）", list.Code, list.Body.String())
	}
	var response modelListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析模型列表失败：%v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != "enabled-model" {
		t.Fatalf("模型列表 = %+v，期望仅 enabled-model", response.Data)
	}

	manifest := performRequest(handler, http.MethodGet, PathCodexModels, "", bearerHeaders())
	if manifest.Code != http.StatusOK {
		t.Fatalf("manifest = %d（body=%s）", manifest.Code, manifest.Body.String())
	}
	var codex codexManifestPlaceholder
	if err := json.Unmarshal(manifest.Body.Bytes(), &codex); err != nil {
		t.Fatalf("解析 manifest 失败：%v", err)
	}
	if len(codex.Models) != 1 || codex.Models[0].Slug != "enabled-model" {
		t.Fatalf("manifest = %+v，期望仅 enabled-model", codex.Models)
	}

	for _, body := range []string{list.Body.String(), manifest.Body.String()} {
		for _, secret := range []string{
			"disabled-model", "upstream-model-should-not-leak", "should-not-leak",
			"sk-****wxyz", testClientKey,
		} {
			if strings.Contains(body, secret) {
				t.Fatalf("模型发现响应泄漏 %q：%s", secret, body)
			}
		}
	}
}
