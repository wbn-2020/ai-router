package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-router/internal/observability"
	storepkg "ai-router/internal/store"
)

// newObservedGateway 把 T23 的观测层接到现有代理测试栈上。
func newObservedGateway(t *testing.T, stack *proxyTestStack) *ObservedGateway {
	t.Helper()
	enableProxyStream(t, stack)
	// 故意不单独传 stream：验证 ObserveGateway 能从 proxy.streamHandler
	// 发现已接线的 StreamProxy。
	observed, err := ObserveGateway(stack.handler, stack.proxy, nil, stack.obs)
	if err != nil {
		t.Fatalf("ObserveGateway: %v", err)
	}
	return observed
}

func doObserved(
	t *testing.T,
	observed *ObservedGateway,
	path, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+proxyTestClientKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	observed.ServeHTTP(rec, req)
	return rec
}

func responsesCompletedUsageFrame(sequence int64, model string, input, output int64) string {
	return sseDataFrame("response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": sequence,
		"response": map[string]any{
			"id":         "resp_completed",
			"object":     "response",
			"created_at": int64(1700000000),
			"model":      model,
			"status":     "completed",
			"output":     []any{},
			"usage": map[string]any{
				"input_tokens":  input,
				"output_tokens": output,
				"total_tokens":  input + output,
			},
		},
	})
}

func responsesFailedUsageFrame(sequence int64, model string, input, output int64) string {
	return sseDataFrame("response.failed", map[string]any{
		"type":            "response.failed",
		"sequence_number": sequence,
		"response": map[string]any{
			"id":         "resp_failed",
			"object":     "response",
			"created_at": int64(1700000000),
			"model":      model,
			"status":     "failed",
			"output":     []any{},
			"error":      map[string]any{"code": "server_error", "message": "boom"},
			"usage": map[string]any{
				"input_tokens":  input,
				"output_tokens": output,
				"total_tokens":  input + output,
			},
		},
	})
}

func chatUsageFrame(input, output int64) string {
	return sseDataFrame("", map[string]any{
		"id":      "chatcmpl_test",
		"object":  "chat.completion.chunk",
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     input,
			"completion_tokens": output,
			"total_tokens":      input + output,
		},
	})
}

func requireTokens(
	t *testing.T,
	log observability.RequestLog,
	input, output, total int64,
	source string,
) {
	t.Helper()
	if log.InputTokens == nil || *log.InputTokens != input {
		t.Fatalf("input_tokens = %v，期望 %d", log.InputTokens, input)
	}
	if log.OutputTokens == nil || *log.OutputTokens != output {
		t.Fatalf("output_tokens = %v，期望 %d", log.OutputTokens, output)
	}
	if log.TotalTokens == nil || *log.TotalTokens != total {
		t.Fatalf("total_tokens = %v，期望 %d", log.TotalTokens, total)
	}
	if log.UsageSource != source {
		t.Fatalf("usage_source = %q，期望 %q", log.UsageSource, source)
	}
}

func requireNoTokens(t *testing.T, log observability.RequestLog) {
	t.Helper()
	if log.InputTokens != nil || log.OutputTokens != nil || log.TotalTokens != nil {
		t.Fatalf("缺失 usage 时 token 应为 nil，实际 %v/%v/%v",
			log.InputTokens, log.OutputTokens, log.TotalTokens)
	}
	if log.UsageSource != "" {
		t.Fatalf("缺失 usage 时 usage_source 应为空，实际 %q", log.UsageSource)
	}
}

func requestLogCount(t *testing.T, obs *observability.Store) int64 {
	t.Helper()
	page, err := obs.ListRequests(context.Background(), observability.RequestFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	return page.Total
}

func TestObservedResponsesStreamUsage(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	fake := newSSEFake(t,
		responsesCreatedFrame("resp_first", "gpt-upstream-stream", 0),
		responsesTextDeltaFrame(1, "hello"),
		responsesCompletedUsageFrame(2, "gpt-upstream-stream", 12, 34),
	)
	channel := addResponsesChannel(t, stack, "stream", fake)

	rec := doObserved(t, observed, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"usage"`) {
		t.Fatalf("转发流被改写，缺少 usage：%s", rec.Body.String())
	}

	detail := stack.latestDetail(t)
	log := detail.Request
	if log.Status != observability.StatusSuccess ||
		log.HTTPStatus == nil || *log.HTTPStatus != http.StatusOK ||
		!log.Stream || log.AttemptCount != 1 || log.RetryCount != 0 ||
		!log.SemanticCommitted {
		t.Fatalf("request log 错误：%+v", log)
	}
	requireTokens(t, log, 12, 34, 46, observability.UsageSourceResponsesCompleted)
	if len(detail.Attempts) != 1 {
		t.Fatalf("attempt 数 = %d，期望 1", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], channel.ID)
	if got := requestLogCount(t, stack.obs); got != 1 {
		t.Fatalf("request_log 条数 = %d，期望 1", got)
	}
}

func TestObservedResponsesStreamUsageMissingIsNull(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	fake := newSSEFake(t,
		responsesCreatedFrame("resp_first", "gpt-upstream-stream", 0),
		responsesTextDeltaFrame(1, "hello"),
		responsesCompletedFrame(2, "gpt-upstream-stream"),
	)
	addResponsesChannel(t, stack, "stream", fake)

	rec := doObserved(t, observed, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusSuccess {
		t.Fatalf("request status = %q", detail.Request.Status)
	}
	requireNoTokens(t, detail.Request)
}

func TestObservedResponsesFailedEventUsage(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	fake := newSSEFake(t,
		responsesCreatedFrame("resp_first", "gpt-upstream-stream", 0),
		responsesTextDeltaFrame(1, "partial"),
		responsesFailedUsageFrame(2, "gpt-upstream-stream", 9, 4),
	)
	addResponsesChannel(t, stack, "stream", fake)

	rec := doObserved(t, observed, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}

	detail := stack.latestDetail(t)
	log := detail.Request
	if log.Status != observability.StatusError ||
		log.HTTPStatus == nil || *log.HTTPStatus != http.StatusOK ||
		!log.SemanticCommitted || log.AttemptCount != 1 ||
		log.ErrorCode != errorCodeUpstreamError {
		t.Fatalf("request log 错误：%+v", log)
	}
	requireTokens(t, log, 9, 4, 13, observability.UsageSourceResponsesFailed)
}

func TestObservedChatStreamFinalUsage(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	fake := newSSEFake(t,
		chatRoleFrame(),
		chatContentFrame("hi"),
		chatUsageFrame(9, 4),
		chatDoneFrame(),
	)
	channel := addChatChannel(t, stack, "chat", fake)

	rec := doObserved(t, observed, PathChatCompletions, streamChatBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}

	detail := stack.latestDetail(t)
	log := detail.Request
	if log.Status != observability.StatusSuccess ||
		log.HTTPStatus == nil || *log.HTTPStatus != http.StatusOK ||
		!log.Stream || log.AttemptCount != 1 || !log.SemanticCommitted {
		t.Fatalf("request log 错误：%+v", log)
	}
	requireTokens(t, log, 9, 4, 13, observability.UsageSourceChatStream)
	requireAttemptChannel(t, detail.Attempts[0], channel.ID)
}

func TestObservedChatStreamUsageMissingIsNull(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	fake := newSSEFake(t,
		chatRoleFrame(),
		chatContentFrame("hi"),
		chatDoneFrame(),
	)
	addChatChannel(t, stack, "chat", fake)

	rec := doObserved(t, observed, PathChatCompletions, streamChatBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}
	requireNoTokens(t, stack.latestDetail(t).Request)
}

func TestObservedNonStreamUsage(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		protocol string
		request  string
		response string
		source   string
		input    int64
		output   int64
		total    int64
	}{
		{
			name:     "Responses",
			path:     PathResponses,
			protocol: storepkg.ChannelProtocolOpenAIResponses,
			request:  proxyRequestBody(),
			response: `{"id":"resp_1","object":"response","status":"completed",` +
				`"usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}`,
			source: observability.UsageSourceResponsesResponse,
			input:  12,
			output: 34,
			total:  46,
		},
		{
			name:     "Chat",
			path:     PathChatCompletions,
			protocol: storepkg.ChannelProtocolOpenAIChat,
			request:  `{"model":"` + proxyTestModel + `","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			response: `{"id":"chatcmpl_1","object":"chat.completion","choices":[],` +
				`"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
			source: observability.UsageSourceChatCompletion,
			input:  9,
			output: 4,
			total:  13,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := newProxyTestStack(t, proxyStackOptions{})
			observed := newObservedGateway(t, stack)
			fake := newProxyJSONUpstream(t, http.StatusOK, tt.response)
			channel := stack.addChannel(t, "upstream", fake.server.URL, tt.protocol, 1)
			stack.addRoute(t, channel.ID, "gpt-upstream")

			rec := doObserved(t, observed, tt.path, tt.request)
			if rec.Code != http.StatusOK {
				t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
			}

			detail := stack.latestDetail(t)
			log := detail.Request
			if log.Status != observability.StatusSuccess ||
				log.HTTPStatus == nil || *log.HTTPStatus != http.StatusOK ||
				log.Stream || !log.SemanticCommitted {
				t.Fatalf("request log 错误：%+v", log)
			}
			requireTokens(t, log, tt.input, tt.output, tt.total, tt.source)
			if got := requestLogCount(t, stack.obs); got != 1 {
				t.Fatalf("request_log 条数 = %d，期望 1", got)
			}
		})
	}
}

func TestObservedNonStreamUsageMissingIsNull(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	fake := newProxyJSONUpstream(t, http.StatusOK,
		`{"id":"resp_1","object":"response","status":"completed"}`)
	channel := stack.addChannel(t, "upstream", fake.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream")

	rec := doObserved(t, observed, PathResponses, proxyRequestBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}
	requireNoTokens(t, stack.latestDetail(t).Request)
}

func TestObservedUsageFollowsFinalAttemptOnly(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)

	first := newSSEFake(t,
		responsesCreatedFrame("resp_first", "gpt-upstream-first", 0),
		responsesFailedUsageFrame(1, "gpt-upstream-first", 77, 88),
	)
	second := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
		responsesTextDeltaFrame(1, "recovered"),
		responsesCompletedUsageFrame(2, "gpt-upstream-second", 12, 34),
	)
	firstChannel := addResponsesChannel(t, stack, "first", first)
	secondChannel := addResponsesChannel(t, stack, "second", second)

	rec := doObserved(t, observed, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}
	if got := second.hits.Load(); got != 1 {
		t.Fatalf("第二个渠道调用 %d 次，期望 1", got)
	}

	detail := stack.latestDetail(t)
	log := detail.Request
	if log.Status != observability.StatusSuccess ||
		log.AttemptCount != 2 || log.RetryCount != 1 || !log.SemanticCommitted {
		t.Fatalf("request log 错误：%+v", log)
	}
	// 第一个渠道的 failed 事件带 usage，但最终成功的是第二个渠道；日志必须
	// 使用最终尝试的 usage，不能被中间失败尝试覆盖。
	requireTokens(t, log, 12, 34, 46, observability.UsageSourceResponsesCompleted)
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempt 数 = %d，期望 2", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], firstChannel.ID)
	requireAttemptChannel(t, detail.Attempts[1], secondChannel.ID)
	if got := requestLogCount(t, stack.obs); got != 1 {
		t.Fatalf("request_log 条数 = %d，期望 1", got)
	}
}

func TestObservedClientCancelRecords499(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)

	upstreamCanceled := make(chan struct{})
	var once sync.Once
	blocking := newProxyFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, responsesCreatedFrame("resp_blocking", "gpt-upstream-blocking", 0))
		_, _ = io.WriteString(w, responsesTextDeltaFrame(1, "partial"))
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
		once.Do(func() { close(upstreamCanceled) })
	})
	addResponsesChannel(t, stack, "blocking", blocking)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, PathResponses, strings.NewReader(streamResponsesBody()))
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+proxyTestClientKey)
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	writer := newNotifyingWriter(recorder)
	done := make(chan struct{})
	go func() {
		observed.ServeHTTP(writer, req)
		close(done)
	}()

	select {
	case <-writer.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("等待首个语义字节超时")
	}
	cancel()

	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端断开后上游 context 未被取消")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后观测 Handler 未返回")
	}

	detail := stack.latestDetail(t)
	log := detail.Request
	if log.Status != observability.StatusCancelled ||
		log.HTTPStatus == nil || *log.HTTPStatus != httpStatusClientClosedRequest ||
		log.AttemptCount != 1 || !log.SemanticCommitted {
		t.Fatalf("取消 request log 错误：%+v", log)
	}
	requireNoTokens(t, log)
	if got := requestLogCount(t, stack.obs); got != 1 {
		t.Fatalf("request_log 条数 = %d，期望 1", got)
	}
}

// TestObserveGatewayRejectsMissingDependencies 覆盖构造期依赖校验。
func TestObserveGatewayRejectsMissingDependencies(t *testing.T) {
	if _, err := ObserveGateway(nil, nil, nil, nil); err == nil {
		t.Fatal("缺少 Handler/代理时应返回错误")
	}

	stack := newProxyTestStack(t, proxyStackOptions{})
	stream := enableProxyStream(t, stack)
	stack.proxy.upstream = nil
	if _, err := ObserveGateway(stack.handler, stack.proxy, stream, stack.obs); err == nil {
		t.Fatal("缺少上游客户端时应返回错误")
	}
}

// 确保 usage 只进入 request_logs 的 token 列，不改变响应 body。
func TestObservedGatewayDoesNotRewriteResponseBody(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	observed := newObservedGateway(t, stack)
	body := `{"id":"resp_1","object":"response","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
	fake := newProxyJSONUpstream(t, http.StatusOK, body)
	channel := stack.addChannel(t, "upstream", fake.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream")

	rec := doObserved(t, observed, PathResponses, proxyRequestBody())
	if rec.Body.String() != body {
		t.Fatalf("响应 body 被改写：got=%s want=%s", rec.Body.String(), body)
	}

	var raw struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw.Usage) == 0 {
		t.Fatalf("客户端响应缺少 usage：%v", err)
	}
}
