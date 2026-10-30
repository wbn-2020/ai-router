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
	"ai-router/internal/upstream"
)

// enableProxyStream 把 StreamProxy 接到现有 T20 测试栈上，复用同一个
// scheduler/upstream/journal/decryptKey，确保测试经过完整路由与鉴权路径。
func enableProxyStream(t *testing.T, stack *proxyTestStack) *StreamProxy {
	t.Helper()

	stream, err := NewStreamProxy(StreamProxyOptions{
		Scheduler:  stack.scheduler,
		Upstream:   stack.proxy.upstream,
		Journal:    stack.proxy.journal,
		DecryptKey: stack.proxy.decryptKey,
		Clock:      stack.proxy.now,
		RequestID:  stack.proxy.newRequestID,
	})
	if err != nil {
		t.Fatalf("NewStreamProxy: %v", err)
	}
	stack.proxy.streamHandler = stream
	return stream
}

// newSSEFake 返回一个只写 SSE 的假上游；写完后正常结束连接。
func newSSEFake(t *testing.T, frames ...string) *proxyFakeUpstream {
	t.Helper()
	return newProxyFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
}

func sseDataFrame(event string, payload any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	if event == "" {
		return "data: " + string(encoded) + "\n\n"
	}
	return "event: " + event + "\ndata: " + string(encoded) + "\n\n"
}

func responsesCreatedFrame(id, model string, sequence int64) string {
	return sseDataFrame("response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": sequence,
		"response": map[string]any{
			"id":         id,
			"object":     "response",
			"created_at": int64(1700000000),
			"model":      model,
			"status":     "in_progress",
			"output":     []any{},
		},
	})
}

func responsesTextDeltaFrame(sequence int64, text string) string {
	return sseDataFrame("response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": sequence,
		"item_id":         "msg_test",
		"output_index":    0,
		"content_index":   0,
		"delta":           text,
	})
}

func responsesCompletedFrame(sequence int64, model string) string {
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
		},
	})
}

func chatRoleFrame() string {
	return sseDataFrame("", map[string]any{
		"id":      "chatcmpl_test",
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}}},
	})
}

func chatContentFrame(text string) string {
	return sseDataFrame("", map[string]any{
		"id":      "chatcmpl_test",
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}},
	})
}

func chatDoneFrame() string {
	return "data: [DONE]\n\n"
}

func streamResponsesBody() string {
	return `{"model":"` + proxyTestModel + `","input":"hello","stream":true}`
}

func streamChatBody() string {
	return `{"model":"` + proxyTestModel + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
}

// parsedSSEFrame 是测试侧解析出的 SSE 帧。
type parsedSSEFrame struct {
	Event string
	Data  string
}

func parseSSEBody(t *testing.T, body string) []parsedSSEFrame {
	t.Helper()
	reader := upstream.NewSSEReader(strings.NewReader(body), upstream.SSEReaderOptions{})
	defer reader.Close()

	var frames []parsedSSEFrame
	for {
		event, err := reader.Next()
		if err == io.EOF {
			return frames
		}
		if err != nil {
			t.Fatalf("解析 SSE body 失败：%v（body=%q）", err, body)
		}
		frames = append(frames, parsedSSEFrame{
			Event: string(event.Event),
			Data:  string(event.Data),
		})
	}
}

func addResponsesChannel(t *testing.T, stack *proxyTestStack, name string, fake *proxyFakeUpstream) storepkg.Channel {
	t.Helper()
	channel := stack.addChannel(t, name, fake.server.URL,
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-"+name)
	return channel
}

func addChatChannel(t *testing.T, stack *proxyTestStack, name string, fake *proxyFakeUpstream) storepkg.Channel {
	t.Helper()
	channel := stack.addChannel(t, name, fake.server.URL,
		storepkg.ChannelProtocolOpenAIChat, 1)
	stack.addRoute(t, channel.ID, "gpt-upstream-"+name)
	return channel
}

func TestStreamFailsOverOn503ThenCompletes(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	first := newProxyJSONUpstream(t, http.StatusServiceUnavailable,
		`{"error":{"message":"down sk-upstream-proxy-0123456789abcdef"}}`)
	second := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
		responsesTextDeltaFrame(1, "Hello"),
		responsesTextDeltaFrame(2, " world"),
		responsesCompletedFrame(3, "gpt-upstream-second"),
	)
	firstChannel := addResponsesChannel(t, stack, "first", first)
	secondChannel := addResponsesChannel(t, stack, "second", second)

	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "Hello") || !strings.Contains(rec.Body.String(), "resp_second") {
		t.Fatalf("客户端未收到第二个渠道的 SSE：%s", rec.Body.String())
	}
	assertNoSensitiveText(t, "流式响应", rec.Body.String(), proxyTestUpstreamKey, "down")

	if got := first.hits.Load(); got != 1 {
		t.Fatalf("第一个渠道调用 %d 次，期望 1", got)
	}
	if got := second.hits.Load(); got != 1 {
		t.Fatalf("第二个渠道调用 %d 次，期望 1", got)
	}

	detail := stack.latestDetail(t)
	if !detail.Request.Stream {
		t.Fatal("request_log.stream 必须为 true")
	}
	if detail.Request.Status != observability.StatusSuccess ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusOK ||
		detail.Request.AttemptCount != 2 ||
		detail.Request.RetryCount != 1 ||
		!detail.Request.SemanticCommitted {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if detail.Request.TTFTMS == nil {
		t.Fatal("成功流应记录 TTFT")
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempt 记录数 = %d，期望 2", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], firstChannel.ID)
	if detail.Attempts[0].Status != observability.StatusError ||
		detail.Attempts[0].HTTPStatus == nil ||
		*detail.Attempts[0].HTTPStatus != http.StatusServiceUnavailable ||
		detail.Attempts[0].ErrorCode != errorCodeUpstreamUnavailable ||
		detail.Attempts[0].SemanticCommitted {
		t.Fatalf("第一个 attempt 错误：%+v", detail.Attempts[0])
	}
	requireAttemptChannel(t, detail.Attempts[1], secondChannel.ID)
	if detail.Attempts[1].Status != observability.StatusSuccess ||
		!detail.Attempts[1].SemanticCommitted {
		t.Fatalf("第二个 attempt 错误：%+v", detail.Attempts[1])
	}
}

func TestStreamRetriesBeforeSemanticCommit(t *testing.T) {
	tests := []struct {
		name   string
		frames []string
	}{
		{
			name: "首个事件是 error",
			frames: []string{
				sseDataFrame("error", map[string]any{
					"type":    "error",
					"code":    "server_error",
					"message": "upstream exploded",
				}),
			},
		},
		{
			name: "只有前导事件后断流",
			frames: []string{
				responsesCreatedFrame("resp_first", "gpt-upstream-first", 0),
			},
		},
		{
			name: "上游 response.failed",
			frames: []string{
				responsesCreatedFrame("resp_first", "gpt-upstream-first", 0),
				sseDataFrame("response.failed", map[string]any{
					"type":            "response.failed",
					"sequence_number": 1,
					"response": map[string]any{
						"id":         "resp_first",
						"object":     "response",
						"created_at": int64(1700000000),
						"model":      "gpt-upstream-first",
						"status":     "failed",
						"output":     []any{},
						"error":      map[string]any{"code": "server_error", "message": "boom"},
					},
				}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := newProxyTestStack(t, proxyStackOptions{})
			enableProxyStream(t, stack)

			first := newSSEFake(t, tt.frames...)
			second := newSSEFake(t,
				responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
				responsesTextDeltaFrame(1, "recovered"),
				responsesCompletedFrame(2, "gpt-upstream-second"),
			)
			firstChannel := addResponsesChannel(t, stack, "first", first)
			secondChannel := addResponsesChannel(t, stack, "second", second)

			rec := stack.do(t, PathResponses, streamResponsesBody())
			if rec.Code != http.StatusOK {
				t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if strings.Contains(body, "resp_first") || strings.Contains(body, "upstream exploded") {
				t.Fatalf("首个渠道的前导/错误字节泄漏给客户端：%s", body)
			}
			if !strings.Contains(body, "recovered") {
				t.Fatalf("未收到第二个渠道内容：%s", body)
			}
			if got := second.hits.Load(); got != 1 {
				t.Fatalf("第二个渠道调用 %d 次，期望 1", got)
			}

			detail := stack.latestDetail(t)
			if detail.Request.AttemptCount != 2 ||
				detail.Request.Status != observability.StatusSuccess ||
				!detail.Request.SemanticCommitted {
				t.Fatalf("request log 错误：%+v", detail.Request)
			}
			requireAttemptChannel(t, detail.Attempts[0], firstChannel.ID)
			if detail.Attempts[0].SemanticCommitted ||
				detail.Attempts[0].Status != observability.StatusError {
				t.Fatalf("提交前失败 attempt 错误：%+v", detail.Attempts[0])
			}
			requireAttemptChannel(t, detail.Attempts[1], secondChannel.ID)
		})
	}
}

func TestStreamResponsesPostCommitFailureEmitsOneCompleteResponseFailed(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	first := newSSEFake(t,
		responsesCreatedFrame("resp_first", "gpt-upstream-first", 0),
		responsesTextDeltaFrame(1, "Hel"),
		responsesTextDeltaFrame(2, "lo"),
	)
	second := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
		responsesTextDeltaFrame(1, "must-not-be-used"),
		responsesCompletedFrame(2, "gpt-upstream-second"),
	)
	firstChannel := addResponsesChannel(t, stack, "first", first)
	addResponsesChannel(t, stack, "second", second)

	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if got := second.hits.Load(); got != 0 {
		t.Fatalf("提交后不得切换渠道，第二个渠道调用 %d 次", got)
	}

	frames := parseSSEBody(t, rec.Body.String())
	failedFrames := make([]parsedSSEFrame, 0, 1)
	for _, frame := range frames {
		if frame.Event == "response.failed" {
			failedFrames = append(failedFrames, frame)
		}
	}
	if len(failedFrames) != 1 {
		t.Fatalf("response.failed 数量 = %d，期望 1；body=%s", len(failedFrames), rec.Body.String())
	}
	if frames[len(frames)-1].Event != "response.failed" {
		t.Fatalf("最后一个事件 = %q，期望 response.failed", frames[len(frames)-1].Event)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(failedFrames[0].Data), &payload); err != nil {
		t.Fatalf("解码 response.failed 失败：%v（data=%s）", err, failedFrames[0].Data)
	}
	if payload["type"] != "response.failed" {
		t.Fatalf("type = %v", payload["type"])
	}
	sequence, ok := payload["sequence_number"].(float64)
	if !ok {
		t.Fatalf("缺少 sequence_number：%v", payload["sequence_number"])
	}
	if sequence != 3 {
		t.Fatalf("sequence_number = %v，期望 3", sequence)
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 response 对象：%v", payload["response"])
	}
	if response["id"] != "resp_first" {
		t.Fatalf("response.id = %v", response["id"])
	}
	if response["object"] != "response" {
		t.Fatalf("response.object = %v", response["object"])
	}
	if createdAt, ok := response["created_at"].(float64); !ok || createdAt != 1700000000 {
		t.Fatalf("response.created_at = %v", response["created_at"])
	}
	if response["model"] != "gpt-upstream-first" {
		t.Fatalf("response.model = %v", response["model"])
	}
	if response["status"] != "failed" {
		t.Fatalf("response.status = %v", response["status"])
	}
	output, ok := response["output"].([]any)
	if !ok || output == nil || len(output) != 0 {
		t.Fatalf("response.output 必须是 []：%v", response["output"])
	}
	failure, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 response.error：%v", response["error"])
	}
	if failure["code"] != errorCodeUpstreamError || failure["message"] != "Upstream stream ended unexpectedly." {
		t.Fatalf("response.error = %+v", failure)
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusOK ||
		!detail.Request.SemanticCommitted ||
		detail.Request.AttemptCount != 1 ||
		detail.Request.ErrorCode != errorCodeUpstreamError {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("attempt 数 = %d，期望 1", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], firstChannel.ID)
	if !detail.Attempts[0].SemanticCommitted ||
		detail.Attempts[0].Status != observability.StatusError ||
		detail.Attempts[0].ErrorCode != errorCodeUpstreamError {
		t.Fatalf("提交后失败 attempt 错误：%+v", detail.Attempts[0])
	}
}

func TestStreamResponsesPostCommitFailureAfterUnterminatedEvent(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	unterminated := "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"Hi"}`
	first := newSSEFake(t,
		responsesCreatedFrame("resp_truncated", "gpt-upstream-truncated", 0),
		unterminated,
	)
	addResponsesChannel(t, stack, "truncated", first)

	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}

	frames := parseSSEBody(t, rec.Body.String())
	failedCount := 0
	sawDelta := false
	for _, frame := range frames {
		switch frame.Event {
		case "response.output_text.delta":
			sawDelta = true
		case "response.failed":
			failedCount++
		}
	}
	if !sawDelta || failedCount != 1 {
		t.Fatalf("截断事件后终止帧错误：delta=%v failed=%d body=%q",
			sawDelta, failedCount, rec.Body.String())
	}
}

func TestStreamResponsesToolDeltaCommitThenFailure(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	first := newSSEFake(t,
		responsesCreatedFrame("resp_tool", "gpt-upstream-tool", 0),
		sseDataFrame("response.function_call_arguments.delta", map[string]any{
			"type":            "response.function_call_arguments.delta",
			"sequence_number": 1,
			"item_id":         "fc_test",
			"output_index":    0,
			"delta":           `{"city":`,
		}),
	)
	second := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
		responsesCompletedFrame(1, "gpt-upstream-second"),
	)
	addResponsesChannel(t, stack, "first", first)
	addResponsesChannel(t, stack, "second", second)

	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if got := second.hits.Load(); got != 0 {
		t.Fatalf("工具参数提交后不得换渠道，第二个渠道调用 %d 次", got)
	}

	frames := parseSSEBody(t, rec.Body.String())
	sawToolDelta := false
	failedCount := 0
	for _, frame := range frames {
		if frame.Event == "response.function_call_arguments.delta" {
			sawToolDelta = true
		}
		if frame.Event == "response.failed" {
			failedCount++
		}
	}
	if !sawToolDelta || failedCount != 1 {
		t.Fatalf("tool delta/failed 计数错误：tool=%v failed=%d body=%s",
			sawToolDelta, failedCount, rec.Body.String())
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		!detail.Request.SemanticCommitted ||
		detail.Request.AttemptCount != 1 {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 1 || !detail.Attempts[0].SemanticCommitted {
		t.Fatalf("tool delta 后应语义提交：%+v", detail.Attempts)
	}
}

func TestStreamChatPostCommitFailureEmitsErrorAndDone(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	first := newSSEFake(t,
		chatRoleFrame(),
		chatContentFrame("Hi"),
	)
	second := newSSEFake(t,
		chatRoleFrame(),
		chatContentFrame("must-not-be-used"),
		chatDoneFrame(),
	)
	firstChannel := addChatChannel(t, stack, "first", first)
	addChatChannel(t, stack, "second", second)

	rec := stack.do(t, PathChatCompletions, streamChatBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if got := second.hits.Load(); got != 0 {
		t.Fatalf("提交后不得切换渠道，第二个渠道调用 %d 次", got)
	}

	frames := parseSSEBody(t, rec.Body.String())
	if len(frames) == 0 {
		t.Fatal("没有收到任何 Chat SSE 帧")
	}
	if frames[len(frames)-1].Data != "[DONE]" {
		t.Fatalf("最后一个 Chat 帧 = %q，期望 [DONE]", frames[len(frames)-1].Data)
	}
	doneCount := 0
	errorCount := 0
	errorIndex := -1
	contentIndex := -1
	for index, frame := range frames {
		if frame.Data == "[DONE]" {
			doneCount++
			continue
		}
		if strings.Contains(frame.Data, `"content":"Hi"`) {
			contentIndex = index
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(frame.Data), &payload); err == nil {
			if payload["error"] != nil {
				errorCount++
				errorIndex = index
			}
		}
	}
	if doneCount != 1 {
		t.Fatalf("[DONE] 数量 = %d，期望 1；body=%s", doneCount, rec.Body.String())
	}
	if errorCount != 1 {
		t.Fatalf("error 对象数量 = %d，期望 1；body=%s", errorCount, rec.Body.String())
	}
	if contentIndex < 0 || errorIndex <= contentIndex || errorIndex >= len(frames)-1 {
		t.Fatalf("error 帧位置错误：content=%d error=%d total=%d", contentIndex, errorIndex, len(frames))
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusError ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != http.StatusOK ||
		!detail.Request.SemanticCommitted ||
		detail.Request.AttemptCount != 1 {
		t.Fatalf("request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("attempt 数 = %d，期望 1", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], firstChannel.ID)
	if detail.Attempts[0].Status != observability.StatusError ||
		!detail.Attempts[0].SemanticCommitted ||
		detail.Attempts[0].ErrorCode != errorCodeUpstreamError {
		t.Fatalf("提交后失败 attempt 错误：%+v", detail.Attempts[0])
	}
}

func TestStreamClientCancelCancelsUpstreamAndDoesNotRetry(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	upstreamCanceled := make(chan struct{})
	var cancelOnce sync.Once
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
		cancelOnce.Do(func() { close(upstreamCanceled) })
	})
	second := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
		responsesTextDeltaFrame(1, "must-not-be-used"),
		responsesCompletedFrame(2, "gpt-upstream-second"),
	)
	blockingChannel := addResponsesChannel(t, stack, "blocking", blocking)
	addResponsesChannel(t, stack, "second", second)

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
		stack.handler.ServeHTTP(writer, req)
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
		t.Fatal("客户端断开后流式 handler 未返回")
	}

	if got := blocking.hits.Load(); got != 1 {
		t.Fatalf("取消后同一渠道被重试，调用 %d 次", got)
	}
	if got := second.hits.Load(); got != 0 {
		t.Fatalf("客户端断开后不得重试，第二个渠道调用 %d 次", got)
	}

	detail := stack.latestDetail(t)
	if detail.Request.Status != observability.StatusCancelled ||
		detail.Request.HTTPStatus == nil ||
		*detail.Request.HTTPStatus != httpStatusClientClosedRequest ||
		detail.Request.AttemptCount != 1 ||
		!detail.Request.SemanticCommitted {
		t.Fatalf("取消 request log 错误：%+v", detail.Request)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("attempt 数 = %d，期望 1", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], blockingChannel.ID)
	if detail.Attempts[0].Status != observability.StatusCancelled ||
		!detail.Attempts[0].SemanticCommitted {
		t.Fatalf("取消 attempt 错误：%+v", detail.Attempts[0])
	}
}

// notifyingWriter 在第一次成功写入时关闭 wrote，用于确认语义提交已经发生。
type notifyingWriter struct {
	http.ResponseWriter
	flusher http.Flusher
	wrote   chan struct{}
	once    sync.Once
}

func newNotifyingWriter(w http.ResponseWriter) *notifyingWriter {
	flusher, _ := w.(http.Flusher)
	return &notifyingWriter{
		ResponseWriter: w,
		flusher:        flusher,
		wrote:          make(chan struct{}),
	}
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.once.Do(func() { close(w.wrote) })
	return n, err
}

func (w *notifyingWriter) Flush() {
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func TestStreamBufferLimitBeforeCommitRetries(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	stream := enableProxyStream(t, stack)
	stream.maxBufferBytes = 300

	first := newSSEFake(t,
		responsesCreatedFrame("resp_"+strings.Repeat("x", 300), "gpt-upstream-first", 0),
		responsesTextDeltaFrame(1, "must-not-be-used"),
		responsesCompletedFrame(2, "gpt-upstream-first"),
	)
	second := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-second", 0),
		responsesTextDeltaFrame(1, "recovered"),
		responsesCompletedFrame(2, "gpt-upstream-second"),
	)
	firstChannel := addResponsesChannel(t, stack, "first", first)
	addResponsesChannel(t, stack, "second", second)

	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "recovered") {
		t.Fatalf("未切换到第二个渠道：%s", rec.Body.String())
	}
	if got := first.hits.Load(); got != 1 {
		t.Fatalf("第一个渠道调用 %d 次", got)
	}

	detail := stack.latestDetail(t)
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempt 数 = %d，期望 2", len(detail.Attempts))
	}
	requireAttemptChannel(t, detail.Attempts[0], firstChannel.ID)
	if detail.Attempts[0].Status != observability.StatusError ||
		detail.Attempts[0].ErrorCode != errorCodeUpstreamError ||
		detail.Attempts[0].SemanticCommitted {
		t.Fatalf("缓冲超限 attempt 错误：%+v", detail.Attempts[0])
	}
}

func TestClassifyStreamFrame(t *testing.T) {
	tests := []struct {
		name      string
		protocol  string
		event     string
		data      string
		semantic  bool
		completed bool
		terminal  bool
		failure   bool
	}{
		{
			name:     "Responses created 只是前导",
			protocol: ProtocolOpenAIResponses,
			event:    "response.created",
			data:     `{"type":"response.created","response":{"id":"resp_1"}}`,
		},
		{
			name:     "Responses 非空文本 delta 提交",
			protocol: ProtocolOpenAIResponses,
			event:    "response.output_text.delta",
			data:     `{"type":"response.output_text.delta","delta":"hi"}`,
			semantic: true,
		},
		{
			name:     "Responses 空文本 delta 不提交",
			protocol: ProtocolOpenAIResponses,
			event:    "response.output_text.delta",
			data:     `{"type":"response.output_text.delta","delta":""}`,
		},
		{
			name:     "Responses 工具参数 delta 提交",
			protocol: ProtocolOpenAIResponses,
			event:    "response.function_call_arguments.delta",
			data:     `{"type":"response.function_call_arguments.delta","delta":"{\"a\":"}`,
			semantic: true,
		},
		{
			name:     "Responses 文本 done 提交",
			protocol: ProtocolOpenAIResponses,
			event:    "response.output_text.done",
			data:     `{"type":"response.output_text.done","text":"hi"}`,
			semantic: true,
		},
		{
			name:      "Responses completed 成功终止",
			protocol:  ProtocolOpenAIResponses,
			event:     "response.completed",
			data:      `{"type":"response.completed","response":{"status":"completed"}}`,
			semantic:  true,
			completed: true,
			terminal:  true,
		},
		{
			name:     "Responses failed 是失败",
			protocol: ProtocolOpenAIResponses,
			event:    "response.failed",
			data:     `{"type":"response.failed","response":{"status":"failed"}}`,
			failure:  true,
		},
		{
			name:     "Responses error 事件是失败",
			protocol: ProtocolOpenAIResponses,
			event:    "error",
			data:     `{"type":"error","message":"boom"}`,
			failure:  true,
		},
		{
			name:     "Responses data.type 可替代 event 行",
			protocol: ProtocolOpenAIResponses,
			data:     `{"type":"response.output_text.delta","delta":"hi"}`,
			semantic: true,
		},
		{
			name:     "Chat role 只是前导",
			protocol: ProtocolOpenAIChat,
			data:     `{"choices":[{"delta":{"role":"assistant"}}]}`,
		},
		{
			name:     "Chat content 提交",
			protocol: ProtocolOpenAIChat,
			data:     `{"choices":[{"delta":{"content":"hi"}}]}`,
			semantic: true,
		},
		{
			name:     "Chat tool_calls 提交",
			protocol: ProtocolOpenAIChat,
			data:     `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1"}]}}]}`,
			semantic: true,
		},
		{
			name:     "Chat reasoning_content 提交",
			protocol: ProtocolOpenAIChat,
			data:     `{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
			semantic: true,
		},
		{
			name:      "Chat finish_reason 完成但不终止",
			protocol:  ProtocolOpenAIChat,
			data:      `{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			semantic:  true,
			completed: true,
		},
		{
			name:      "Chat DONE 成功终止",
			protocol:  ProtocolOpenAIChat,
			data:      `[DONE]`,
			completed: true,
			terminal:  true,
		},
		{
			name:     "Chat error 对象是失败",
			protocol: ProtocolOpenAIChat,
			data:     `{"error":{"message":"boom","type":"server_error","code":"server_error"}}`,
			failure:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := upstream.SSEEvent{Data: []byte(tt.data)}
			if tt.event != "" {
				event.Event = []byte(tt.event)
			}
			got := classifyStreamFrame(tt.protocol, event)
			if got.semantic != tt.semantic ||
				got.completed != tt.completed ||
				got.terminal != tt.terminal ||
				got.failure != tt.failure {
				t.Fatalf("classifyStreamFrame() = %+v，want semantic=%v completed=%v terminal=%v failure=%v",
					got, tt.semantic, tt.completed, tt.terminal, tt.failure)
			}
		})
	}
}

// 确保测试栈的 fake 上游始终收到 stream=true；同时避免误用非流式路径。
func TestStreamRequestUsesStreamingUpstreamRequest(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	enableProxyStream(t, stack)

	fake := newSSEFake(t,
		responsesCreatedFrame("resp_second", "gpt-upstream-stream", 0),
		responsesTextDeltaFrame(1, "ok"),
		responsesCompletedFrame(2, "gpt-upstream-stream"),
	)
	addResponsesChannel(t, stack, "stream", fake)

	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d；body=%s", rec.Code, rec.Body.String())
	}
	requests := fake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("上游请求数 = %d", len(requests))
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(requests[0].Body, &payload); err != nil {
		t.Fatalf("解析上游请求体失败：%v", err)
	}
	if !payload.Stream {
		t.Fatal("上游请求 stream 必须为 true")
	}
}
