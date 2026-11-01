package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/secret"
	storepkg "ai-router/internal/store"
	"ai-router/internal/upstream"
)

const (
	proxyTestClientKey   = "sk-client-proxy-0123456789abcdef"
	proxyTestUpstreamKey = "sk-upstream-proxy-0123456789abcdef"
	proxyTestModel       = "gpt-proxy-test"
)

// proxyCapturedRequest 是假上游收到的一次请求快照。
type proxyCapturedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// proxyFakeUpstream 是本地假上游，记录调用次数与请求内容。
type proxyFakeUpstream struct {
	server *httptest.Server
	hits   atomic.Int64

	mu       sync.Mutex
	requests []proxyCapturedRequest
}

func newProxyFakeUpstream(t *testing.T, handler http.HandlerFunc) *proxyFakeUpstream {
	t.Helper()
	fake := &proxyFakeUpstream{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, proxyCapturedRequest{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Header:   r.Header.Clone(),
			Body:     body,
		})
		fake.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func newProxyJSONUpstream(t *testing.T, status int, body string) *proxyFakeUpstream {
	t.Helper()
	return newProxyFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (f *proxyFakeUpstream) snapshot() []proxyCapturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proxyCapturedRequest(nil), f.requests...)
}

type proxyStackOptions struct {
	Upstream     upstream.Options
	CapacityWait time.Duration
	CapacityPoll time.Duration
}

type proxyTestStack struct {
	handler   *Handler
	proxy     *Proxy
	store     *storepkg.Store
	obs       *observability.Store
	scheduler *routing.Scheduler
	envelope  *secret.Envelope
	seq       atomic.Int64
}

func newProxyTestStack(t *testing.T, opts proxyStackOptions) *proxyTestStack {
	t.Helper()

	if opts.Upstream == (upstream.Options{}) {
		opts.Upstream = upstream.Options{
			ConnectTimeout:        time.Second,
			TLSHandshakeTimeout:   time.Second,
			ResponseHeaderTimeout: 2 * time.Second,
			StreamIdleTimeout:     2 * time.Second,
			RequestTimeout:        2 * time.Second,
			StreamTimeout:         2 * time.Second,
		}
	}
	if opts.CapacityWait <= 0 {
		opts.CapacityWait = 200 * time.Millisecond
	}
	if opts.CapacityPoll <= 0 {
		opts.CapacityPoll = 5 * time.Millisecond
	}

	dir := t.TempDir()
	st, err := storepkg.Open(storepkg.Paths{
		DataDir: dir,
		DBPath:  filepath.Join(dir, "gateway.db"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	envelope, err := secret.NewEnvelope(bytes.Repeat([]byte{0x5a}, secret.MasterKeySize))
	if err != nil {
		t.Fatalf("secret.NewEnvelope: %v", err)
	}

	scheduler, err := routing.New(routing.Options{
		Routes:       st.ModelRoutes(),
		Channels:     st.Channels(),
		LastUsed:     st.Channels(),
		CapacityWait: opts.CapacityWait,
		CapacityPoll: opts.CapacityPoll,
	})
	if err != nil {
		t.Fatalf("routing.New: %v", err)
	}

	client, err := upstream.NewClient(opts.Upstream)
	if err != nil {
		t.Fatalf("upstream.NewClient: %v", err)
	}

	stack := &proxyTestStack{
		store:     st,
		obs:       observability.New(st.DB()),
		scheduler: scheduler,
		envelope:  envelope,
	}

	proxy, err := NewProxy(ProxyOptions{
		Scheduler: scheduler,
		Upstream:  client,
		Journal:   stack.obs,
		DecryptKey: func(ciphertext string) (string, error) {
			return envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
		},
		Clock: time.Now,
		RequestID: func(*http.Request) string {
			return fmt.Sprintf("req-%d", stack.seq.Add(1))
		},
	})
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}

	handler, err := New(Options{
		ClientKey: proxyTestClientKey,
		Routes:    st.ModelRoutes(),
		Forwarder: proxy,
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}

	stack.proxy = proxy
	stack.handler = handler
	return stack
}

func (s *proxyTestStack) addChannel(
	t *testing.T,
	name, baseURL, protocol string,
	maxConcurrency int,
) storepkg.Channel {
	t.Helper()
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	ciphertext, err := s.envelope.EncryptString(proxyTestUpstreamKey, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("加密渠道 Key: %v", err)
	}
	channel, err := s.store.Channels().Create(context.Background(), storepkg.ChannelInput{
		ChannelConfig: storepkg.ChannelConfig{
			Name:           name,
			BaseURL:        baseURL,
			Protocol:       protocol,
			Priority:       0,
			MaxConcurrency: maxConcurrency,
			Enabled:        true,
		},
		KeyCiphertext: ciphertext,
		KeyMask:       secret.MaskKey(proxyTestUpstreamKey),
	})
	if err != nil {
		t.Fatalf("创建渠道: %v", err)
	}
	return channel
}

func (s *proxyTestStack) addRoute(
	t *testing.T,
	channelID int64,
	upstreamModel string,
) storepkg.ModelRoute {
	t.Helper()
	route, err := s.store.ModelRoutes().Create(context.Background(), storepkg.ModelRouteInput{
		ClientModel:   proxyTestModel,
		ChannelID:     channelID,
		UpstreamModel: upstreamModel,
		Priority:      0,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("创建模型映射: %v", err)
	}
	return route
}

func (s *proxyTestStack) do(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+proxyTestClientKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(&deadlineRecorder{ResponseRecorder: rec}, req)
	return rec
}

func (s *proxyTestStack) latestDetail(t *testing.T) observability.RequestDetail {
	t.Helper()
	page, err := s.obs.ListRequests(context.Background(), observability.RequestFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(page.Requests) == 0 {
		t.Fatal("没有请求日志")
	}
	detail, err := s.obs.GetRequest(context.Background(), page.Requests[0].RequestID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	return detail
}

func proxyRequestBody() string {
	return `{"model":"` + proxyTestModel + `","input":"hello","stream":false}`
}

func captureBodyModel(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("解析上游请求体失败：%v（body=%s）", err, body)
	}
	return payload.Model
}

func requireAttemptChannel(t *testing.T, attempt observability.RequestAttempt, want int64) {
	t.Helper()
	if attempt.ChannelID == nil {
		t.Fatalf("attempt %d 缺少 channel_id", attempt.AttemptIndex)
	}
	if *attempt.ChannelID != want {
		t.Fatalf("attempt %d channel = %d, want %d",
			attempt.AttemptIndex, *attempt.ChannelID, want)
	}
}

func assertNoSensitiveText(t *testing.T, where, text string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		if strings.Contains(text, needle) {
			t.Errorf("%s 包含敏感值 %q：%s", where, needle, text)
		}
	}
}

func TestProxyFailsOverOnRetryableStatus(t *testing.T) {
	tests := []struct {
		name          string
		firstStatus   int
		firstBody     string
		wantFirstCode string
	}{
		{
			name:          "503 切换到第二个渠道",
			firstStatus:   http.StatusServiceUnavailable,
			firstBody:     `{"error":{"message":"upstream exploded sk-upstream-proxy-0123456789abcdef"}}`,
			wantFirstCode: errorCodeUpstreamUnavailable,
		},
		{
			name:          "401 切换到第二个渠道",
			firstStatus:   http.StatusUnauthorized,
			firstBody:     `{"error":{"message":"invalid key"}}`,
			wantFirstCode: errorCodeUpstreamAuthFailed,
		},
		{
			name:          "429 切换到第二个渠道",
			firstStatus:   http.StatusTooManyRequests,
			firstBody:     `{"error":{"message":"slow down"}}`,
			wantFirstCode: errorCodeUpstreamRateLimited,
		},
		{
			name:          "529 切换到第二个渠道",
			firstStatus:   529,
			firstBody:     `{"error":{"message":"overloaded"}}`,
			wantFirstCode: errorCodeUpstreamUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := newProxyTestStack(t, proxyStackOptions{})
			first := newProxyJSONUpstream(t, tt.firstStatus, tt.firstBody)
			successBody := `{"id":"resp_ok","object":"response","status":"completed"}`
			second := newProxyJSONUpstream(t, http.StatusOK, successBody)

			firstChannel := stack.addChannel(t, "first", first.server.URL,
				storepkg.ChannelProtocolOpenAIResponses, 1)
			secondChannel := stack.addChannel(t, "second", second.server.URL,
				storepkg.ChannelProtocolOpenAIResponses, 1)
			stack.addRoute(t, firstChannel.ID, "gpt-upstream-first")
			stack.addRoute(t, secondChannel.ID, "gpt-upstream-second")

			rec := stack.do(t, PathResponses, proxyRequestBody())
			if rec.Code != http.StatusOK {
				t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != successBody {
				t.Fatalf("响应 body 被改写：got=%s want=%s", rec.Body.String(), successBody)
			}
			if got := first.hits.Load(); got != 1 {
				t.Fatalf("第一个渠道调用 %d 次，期望 1", got)
			}
			if got := second.hits.Load(); got != 1 {
				t.Fatalf("第二个渠道调用 %d 次，期望 1", got)
			}

			firstRequests := first.snapshot()
			if len(firstRequests) != 1 {
				t.Fatalf("第一个渠道请求记录 = %d", len(firstRequests))
			}
			if firstRequests[0].Path != "/responses" {
				t.Fatalf("上游路径 = %q，期望 /responses", firstRequests[0].Path)
			}
			if got := firstRequests[0].Header.Get("Authorization"); got != "Bearer "+proxyTestUpstreamKey {
				t.Fatalf("上游 Authorization = %q，期望渠道 Key", got)
			}
			if strings.Contains(firstRequests[0].Header.Get("Authorization"), proxyTestClientKey) {
				t.Fatal("客户端 Key 被转发到上游")
			}
			if got := captureBodyModel(t, firstRequests[0].Body); got != "gpt-upstream-first" {
				t.Fatalf("第一个渠道 model = %q", got)
			}
			if got := captureBodyModel(t, second.snapshot()[0].Body); got != "gpt-upstream-second" {
				t.Fatalf("第二个渠道 model = %q", got)
			}

			detail := stack.latestDetail(t)
			if detail.Request.Status != observability.StatusSuccess {
				t.Fatalf("request status = %q", detail.Request.Status)
			}
			if detail.Request.AttemptCount != 2 || detail.Request.RetryCount != 1 {
				t.Fatalf("attempt/retry = %d/%d，期望 2/1",
					detail.Request.AttemptCount, detail.Request.RetryCount)
			}
			if detail.Request.ChannelID == nil || *detail.Request.ChannelID != secondChannel.ID {
				t.Fatalf("最终渠道 = %v，期望 %d", detail.Request.ChannelID, secondChannel.ID)
			}
			if detail.Request.HTTPStatus == nil || *detail.Request.HTTPStatus != http.StatusOK {
				t.Fatalf("request HTTP = %v，期望 200", detail.Request.HTTPStatus)
			}
			if !detail.Request.SemanticCommitted {
				t.Fatal("成功请求应标记 semantic_committed")
			}
			if len(detail.Attempts) != 2 {
				t.Fatalf("attempt 记录数 = %d，期望 2", len(detail.Attempts))
			}

			firstAttempt := detail.Attempts[0]
			requireAttemptChannel(t, firstAttempt, firstChannel.ID)
			if firstAttempt.AttemptIndex != 1 ||
				firstAttempt.Status != observability.StatusError ||
				firstAttempt.HTTPStatus == nil ||
				*firstAttempt.HTTPStatus != tt.firstStatus ||
				firstAttempt.ErrorCode != tt.wantFirstCode ||
				firstAttempt.SemanticCommitted {
				t.Fatalf("第一个 attempt 错误：%+v", firstAttempt)
			}

			secondAttempt := detail.Attempts[1]
			requireAttemptChannel(t, secondAttempt, secondChannel.ID)
			if secondAttempt.AttemptIndex != 2 ||
				secondAttempt.Status != observability.StatusSuccess ||
				secondAttempt.HTTPStatus == nil ||
				*secondAttempt.HTTPStatus != http.StatusOK ||
				!secondAttempt.SemanticCommitted {
				t.Fatalf("第二个 attempt 错误：%+v", secondAttempt)
			}
		})
	}
}

func TestProxyRetriesNetworkError(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	offline := newProxyJSONUpstream(t, http.StatusOK, `{"ok":true}`)
	successBody := `{"id":"resp_ok","object":"response","status":"completed"}`
	second := newProxyJSONUpstream(t, http.StatusOK, successBody)
	// 先绑定第二个 server 再关闭第一个，避免操作系统复用同一端口。
	offline.server.Close()

	offlineChannel := stack.addChannel(t, "offline", offline.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	secondChannel := stack.addChannel(t, "second", second.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, offlineChannel.ID, "gpt-upstream-offline")
	stack.addRoute(t, secondChannel.ID, "gpt-upstream-second")

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != successBody {
		t.Fatalf("响应 body = %s，期望 %s", rec.Body.String(), successBody)
	}
	if got := second.hits.Load(); got != 1 {
		t.Fatalf("第二个渠道调用 %d 次，期望 1", got)
	}

	detail := stack.latestDetail(t)
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempt 记录数 = %d，期望 2", len(detail.Attempts))
	}
	firstAttempt := detail.Attempts[0]
	// 连接被拒绝通常是 upstream_unreachable；若平台把它归为超时，也属于
	// 可重试的网络类错误。
	if (firstAttempt.ErrorCode != errorCodeUpstreamUnreachable &&
		firstAttempt.ErrorCode != errorCodeUpstreamTimeout) ||
		firstAttempt.HTTPStatus != nil ||
		firstAttempt.Status != observability.StatusError {
		t.Fatalf("网络错误 attempt 错误：%+v", firstAttempt)
	}
	requireAttemptChannel(t, firstAttempt, offlineChannel.ID)
	if detail.Attempts[1].Status != observability.StatusSuccess {
		t.Fatalf("第二个 attempt 应成功：%+v", detail.Attempts[1])
	}
}

func TestProxyForwardsChatCompletions(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	successBody := `{"id":"chatcmpl_1","object":"chat.completion","choices":[]}`
	upstreamFake := newProxyJSONUpstream(t, http.StatusOK, successBody)
	channel := stack.addChannel(t, "chat", upstreamFake.server.URL,
		storepkg.ChannelProtocolOpenAIChat, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-chat")

	body := `{"model":"` + proxyTestModel + `","messages":[{"role":"user","content":"hi"}],"stream":false}`
	rec := stack.do(t, PathChatCompletions, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != successBody {
		t.Fatalf("响应 body = %s，期望 %s", rec.Body.String(), successBody)
	}

	requests := upstreamFake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("上游请求记录 = %d，期望 1", len(requests))
	}
	if requests[0].Path != "/chat/completions" {
		t.Fatalf("上游路径 = %q，期望 /chat/completions", requests[0].Path)
	}
	if got := captureBodyModel(t, requests[0].Body); got != "gpt-upstream-chat" {
		t.Fatalf("上游 model = %q", got)
	}

	detail := stack.latestDetail(t)
	if detail.Request.Protocol != storepkg.ChannelProtocolOpenAIChat ||
		detail.Request.Endpoint != PathChatCompletions ||
		detail.Request.Status != observability.StatusSuccess {
		t.Fatalf("Chat 请求日志错误：%+v", detail.Request)
	}
}

func TestProxyDoesNotRetryClientError(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	firstBody := `{"error":{"message":"bad input sk-upstream-proxy-0123456789abcdef at https://upstream.invalid?token=supersecret"}}`
	first := newProxyJSONUpstream(t, http.StatusBadRequest, firstBody)
	second := newProxyJSONUpstream(t, http.StatusOK, `{"ok":true}`)

	firstChannel := stack.addChannel(t, "first", first.server.URL+"?token=supersecret",
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addChannel(t, "second", second.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, firstChannel.ID, "gpt-upstream-first")

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP 状态 = %d，期望 400；body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeError(t, rec)
	if apiErr.Code != errorCodeUpstreamBadRequest {
		t.Fatalf("error code = %q，期望 %q", apiErr.Code, errorCodeUpstreamBadRequest)
	}
	if got := first.hits.Load(); got != 1 {
		t.Fatalf("第一个渠道调用 %d 次，期望 1", got)
	}
	if got := second.hits.Load(); got != 0 {
		t.Fatalf("400 不应尝试第二个渠道，实际调用 %d 次", got)
	}

	requests := first.snapshot()
	if len(requests) != 1 || requests[0].RawQuery != "token=supersecret" {
		t.Fatalf("渠道 base URL 查询串未传给上游：%+v", requests)
	}

	assertNoSensitiveText(t, "错误响应", rec.Body.String(),
		proxyTestUpstreamKey, "supersecret", "upstream.invalid", "token=", "bad input")

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusBadRequest ||
		detail.Request.ErrorCode != errorCodeUpstreamBadRequest ||
		detail.Request.AttemptCount != 1 ||
		detail.Request.RetryCount != 0 {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("attempt 记录数 = %d，期望 1", len(detail.Attempts))
	}
	attempt := detail.Attempts[0]
	if attempt.Status != observability.StatusError ||
		attempt.HTTPStatus == nil ||
		*attempt.HTTPStatus != http.StatusBadRequest ||
		attempt.ErrorCode != errorCodeUpstreamBadRequest {
		t.Fatalf("attempt 错误：%+v", attempt)
	}
	assertNoSensitiveText(t, "request_log.error_message", detail.Request.ErrorMessage,
		proxyTestUpstreamKey, "supersecret", "upstream.invalid", "token=", "bad input")
	assertNoSensitiveText(t, "attempt.error_message", attempt.ErrorMessage,
		proxyTestUpstreamKey, "supersecret", "upstream.invalid", "token=", "bad input")
}

func TestProxyAllChannelsFailReturnsLastError(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	first := newProxyJSONUpstream(t, http.StatusInternalServerError,
		`{"error":{"message":"boom sk-upstream-proxy-0123456789abcdef"}}`)
	second := newProxyFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"down"}}`)
	})

	firstChannel := stack.addChannel(t, "first", first.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	secondChannel := stack.addChannel(t, "second", second.server.URL+"?token=supersecret",
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, firstChannel.ID, "gpt-upstream-first")
	stack.addRoute(t, secondChannel.ID, "gpt-upstream-second")

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP 状态 = %d，期望 503；body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeError(t, rec)
	if apiErr.Code != errorCodeUpstreamUnavailable {
		t.Fatalf("error code = %q，期望 %q", apiErr.Code, errorCodeUpstreamUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Fatalf("Retry-After = %q，期望 3", got)
	}
	assertNoSensitiveText(t, "错误响应", rec.Body.String(),
		proxyTestUpstreamKey, "supersecret", "boom", "down")

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusServiceUnavailable ||
		detail.Request.ErrorCode != errorCodeUpstreamUnavailable ||
		detail.Request.AttemptCount != 2 ||
		detail.Request.RetryCount != 1 {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if detail.Request.ChannelID == nil || *detail.Request.ChannelID != secondChannel.ID {
		t.Fatalf("最终渠道 = %v，期望 %d", detail.Request.ChannelID, secondChannel.ID)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempt 记录数 = %d，期望 2", len(detail.Attempts))
	}
	if detail.Attempts[0].ErrorCode != errorCodeUpstreamError ||
		detail.Attempts[1].ErrorCode != errorCodeUpstreamUnavailable {
		t.Fatalf("attempt 错误码顺序错误：%+v", detail.Attempts)
	}
	assertNoSensitiveText(t, "request_log.error_message", detail.Request.ErrorMessage,
		proxyTestUpstreamKey, "supersecret", "boom", "down")
	for _, attempt := range detail.Attempts {
		assertNoSensitiveText(t, "attempt.error_message", attempt.ErrorMessage,
			proxyTestUpstreamKey, "supersecret", "boom", "down")
	}
}

func TestProxyStopsAfterThreeDistinctChannels(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	upstreams := make([]*proxyFakeUpstream, 0, 4)
	channels := make([]storepkg.Channel, 0, 4)
	for i := 0; i < 4; i++ {
		fake := newProxyJSONUpstream(t, http.StatusServiceUnavailable, `{"error":{"message":"down"}}`)
		channel := stack.addChannel(t, fmt.Sprintf("channel-%d", i), fake.server.URL,
			storepkg.ChannelProtocolOpenAIResponses, 1)
		stack.addRoute(t, channel.ID, fmt.Sprintf("gpt-upstream-%d", i))
		upstreams = append(upstreams, fake)
		channels = append(channels, channel)
	}

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP 状态 = %d，期望 503", rec.Code)
	}

	var total int64
	for i, fake := range upstreams {
		hits := fake.hits.Load()
		total += hits
		want := int64(1)
		if i == 3 {
			want = 0
		}
		if hits != want {
			t.Fatalf("渠道 %d 调用 %d 次，期望 %d", i, hits, want)
		}
	}
	if total != 3 {
		t.Fatalf("上游总调用 = %d，期望 3", total)
	}

	detail := stack.latestDetail(t)
	if detail.Request.AttemptCount != 3 || detail.Request.RetryCount != 2 {
		t.Fatalf("attempt/retry = %d/%d，期望 3/2",
			detail.Request.AttemptCount, detail.Request.RetryCount)
	}
	if len(detail.Attempts) != 3 {
		t.Fatalf("attempt 记录数 = %d，期望 3", len(detail.Attempts))
	}
	for i := 0; i < 3; i++ {
		requireAttemptChannel(t, detail.Attempts[i], channels[i].ID)
		if detail.Attempts[i].AttemptIndex != i+1 {
			t.Fatalf("attempt 顺序错误：%+v", detail.Attempts[i])
		}
	}
}

func TestProxyNoMappingOrProtocolMismatchDoesNotCallUpstream(t *testing.T) {
	t.Run("只有 Chat 渠道收到 Responses 请求", func(t *testing.T) {
		stack := newProxyTestStack(t, proxyStackOptions{})
		fake := newProxyJSONUpstream(t, http.StatusOK, `{"ok":true}`)
		channel := stack.addChannel(t, "chat-only", fake.server.URL,
			storepkg.ChannelProtocolOpenAIChat, 1)
		stack.addRoute(t, channel.ID, "gpt-upstream-chat")

		rec := stack.do(t, PathResponses, proxyRequestBody())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("HTTP 状态 = %d，期望 404；body=%s", rec.Code, rec.Body.String())
		}
		apiErr := decodeError(t, rec)
		if apiErr.Code != errorCodeModelNotAvailable {
			t.Fatalf("error code = %q，期望 %q", apiErr.Code, errorCodeModelNotAvailable)
		}
		if got := fake.hits.Load(); got != 0 {
			t.Fatalf("协议不匹配时上游调用 %d 次，期望 0", got)
		}

		page, err := stack.obs.ListRequests(context.Background(), observability.RequestFilter{Limit: 10})
		if err != nil {
			t.Fatalf("ListRequests: %v", err)
		}
		if page.Total != 0 {
			t.Fatalf("router 层 404 不应产生请求日志，实际 %d 条", page.Total)
		}
	})

	t.Run("没有启用映射", func(t *testing.T) {
		stack := newProxyTestStack(t, proxyStackOptions{})
		fake := newProxyJSONUpstream(t, http.StatusOK, `{"ok":true}`)
		stack.addChannel(t, "responses", fake.server.URL,
			storepkg.ChannelProtocolOpenAIResponses, 1)
		// 故意不创建 model_routes。

		rec := stack.do(t, PathResponses, proxyRequestBody())
		if rec.Code != http.StatusNotFound {
			t.Fatalf("HTTP 状态 = %d，期望 404；body=%s", rec.Code, rec.Body.String())
		}
		apiErr := decodeError(t, rec)
		if apiErr.Code != errorCodeModelNotAvailable {
			t.Fatalf("error code = %q，期望 %q", apiErr.Code, errorCodeModelNotAvailable)
		}
		if got := fake.hits.Load(); got != 0 {
			t.Fatalf("无映射时上游调用 %d 次，期望 0", got)
		}
	})
}

func TestProxyTimeoutMapsTo504(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{
		Upstream: upstream.Options{
			ConnectTimeout:        time.Second,
			TLSHandshakeTimeout:   time.Second,
			ResponseHeaderTimeout: 2 * time.Second,
			StreamIdleTimeout:     2 * time.Second,
			RequestTimeout:        80 * time.Millisecond,
			StreamTimeout:         2 * time.Second,
		},
	})
	slow := newProxyFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	channel := stack.addChannel(t, "slow", slow.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-slow")

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("HTTP 状态 = %d，期望 504；body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeError(t, rec)
	if apiErr.Code != errorCodeUpstreamTimeout {
		t.Fatalf("error code = %q，期望 %q", apiErr.Code, errorCodeUpstreamTimeout)
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		detail.Request.ErrorCode != errorCodeUpstreamTimeout ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("attempt 记录数 = %d，期望 1", len(detail.Attempts))
	}
	if detail.Attempts[0].HTTPStatus != nil ||
		detail.Attempts[0].ErrorCode != errorCodeUpstreamTimeout {
		t.Fatalf("超时 attempt 错误：%+v", detail.Attempts[0])
	}
}

func TestProxyCapacityExhaustedMapsTo429(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{
		CapacityWait: 80 * time.Millisecond,
		CapacityPoll: 5 * time.Millisecond,
	})
	fake := newProxyJSONUpstream(t, http.StatusOK, `{"ok":true}`)
	channel := stack.addChannel(t, "full", fake.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-full")

	_, slot, err := stack.scheduler.Acquire(
		context.Background(), proxyTestModel, ProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("预先占用槽位失败：%v", err)
	}
	defer slot.Release()

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("HTTP 状态 = %d，期望 429；body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeError(t, rec)
	if apiErr.Code != errorCodeChannelCapacity {
		t.Fatalf("error code = %q，期望 %q", apiErr.Code, errorCodeChannelCapacity)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q，期望 1", got)
	}
	if got := fake.hits.Load(); got != 0 {
		t.Fatalf("容量耗尽时上游调用 %d 次，期望 0", got)
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		detail.Request.ErrorCode != errorCodeChannelCapacity ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusTooManyRequests ||
		detail.Request.AttemptCount != 0 {
		t.Fatalf("容量耗尽 request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 0 {
		t.Fatalf("容量耗尽不应有 attempt，实际 %d 条", len(detail.Attempts))
	}
}

// TestProxySuccessForcesNoStoreAndIgnoresUpstreamRequestID 覆盖 C10/C5：
// 数据面成功响应必须禁止缓存，且不能把上游 X-Request-ID 透传给客户端。
func TestProxySuccessForcesNoStoreAndIgnoresUpstreamRequestID(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	fake := newProxyFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Pragma", "cache")
		w.Header().Set("X-Request-Id", "upstream-request-id")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"resp_ok","output":[]}`)
	})
	channel := stack.addChannel(t, "no-store", fake.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-no-store")

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q，期望 no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q，期望 no-cache", got)
	}
	if got := rec.Header().Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id = %q，不应透传上游值", got)
	}
}

// TestProxyCircuitOpenReturns503 覆盖 C8：全部候选熔断时返回可重试的 503，
// 不是 404 model_not_available。
func TestProxyCircuitOpenReturns503(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	fake := newProxyJSONUpstream(t, http.StatusOK, `{"ok":true}`)
	channel := stack.addChannel(t, "circuit", fake.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-circuit")
	candidate, slot, err := stack.scheduler.Acquire(context.Background(), proxyTestModel, storepkg.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	if !stack.scheduler.RecordFailure(candidate, routing.Failure{Scope: routing.FailureScopeChannel, Status: http.StatusUnauthorized, Message: "upstream auth failed"}) {
		t.Fatal("401 应触发熔断")
	}

	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP 状态 = %d，期望 503；body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q，期望 1", got)
	}
	if got := fake.hits.Load(); got != 0 {
		t.Fatalf("熔断时上游调用 %d 次，期望 0", got)
	}
}
