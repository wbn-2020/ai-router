// stream.go 实现流式请求（SSE）的数据面编排（T21）：
//
//   - 在首个语义内容出现前不向客户端写任何字节，只缓冲上游原始 SSE；
//   - 提交前上游错误、SSE error 事件或断流都可以切换到下一个渠道；
//   - 提交后不再发起第二个上游：Responses 只补发一次完整的 response.failed，
//     Chat 补发 error 对象 + [DONE]，HTTP 状态保持 200；
//   - 客户端断开时取消上游 context，attempt 标记 cancelled，不重试。
//
// 本文件只做原生协议直通，不做 Responses↔Chat 转换。SSE 字节解析由
// internal/upstream 负责，语义分类在本文件按入站协议完成。
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/upstream"
)

const (
	// defaultStreamBufferLimit 是首语义内容前允许缓冲的上游 SSE 原始字节上限。
	//
	// 正常 Responses/Chat 的前导事件（response.created、role delta）只有几百
	// 字节；8 MiB 足以覆盖异常大的 reasoning/工具预置内容，同时避免恶意上游
	// 在提交前无限占用内存。
	defaultStreamBufferLimit = 8 << 20

	// maxNonSSEBodyBytes 限制读取“请求了 stream 但上游返回 JSON”的响应体，
	// 只用于丢弃，避免异常上游用大 body 占满内存。
	maxNonSSEBodyBytes int64 = 64 << 10

	streamContentType = "text/event-stream; charset=utf-8"
)

// StreamProxyOptions 是 StreamProxy 的构造参数。
//
// 与 ProxyOptions 相同的依赖必须非 nil；DecryptKey 只接收渠道 Key 密文，
// StreamProxy 不接触主密钥。StreamProxy 通过 ProxyOptions.StreamHandler
// 接入数据面，也可在测试中直接作为 http.Handler 使用。
type StreamProxyOptions struct {
	Scheduler  CandidateScheduler
	Upstream   UpstreamSender
	Journal    AttemptJournal
	DecryptKey func(ciphertext string) (string, error)

	// MaxAttempts 是单次请求最多尝试的不同渠道数；<= 0 时使用默认值 3。
	MaxAttempts         int
	RequestTimeout      time.Duration
	FirstContentTimeout time.Duration
	WriteTimeout        time.Duration

	Logger *slog.Logger
	// Clock 为零时使用 time.Now。
	Clock func() time.Time
	// RequestID 为零时生成 16 字节随机十六进制 ID；非法返回值会被替换。
	RequestID func(*http.Request) string
}

// StreamProxy 是流式数据面代理。构造后字段不可变，可并发使用。
type StreamProxy struct {
	scheduler           CandidateScheduler
	upstream            UpstreamSender
	journal             AttemptJournal
	decryptKey          func(string) (string, error)
	maxAttempts         int
	requestTimeout      time.Duration
	firstContentTimeout time.Duration
	writeTimeout        time.Duration
	logger              *slog.Logger
	now                 func() time.Time
	newRequestID        func(*http.Request) string

	// maxBufferBytes 只由测试调整；生产使用 defaultStreamBufferLimit。
	maxBufferBytes int
}

// NewStreamProxy 构造流式代理。缺少任何必要依赖时返回错误，错误不含 Key。
func NewStreamProxy(opts StreamProxyOptions) (*StreamProxy, error) {
	if opts.Scheduler == nil {
		return nil, errors.New("gateway: 流式代理缺少调度器")
	}
	if opts.Upstream == nil {
		return nil, errors.New("gateway: 流式代理缺少上游客户端")
	}
	if opts.Journal == nil {
		return nil, errors.New("gateway: 流式代理缺少日志仓储")
	}
	if opts.DecryptKey == nil {
		return nil, errors.New("gateway: 流式代理缺少渠道 Key 解密器")
	}

	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &StreamProxy{
		scheduler:           opts.Scheduler,
		upstream:            opts.Upstream,
		journal:             opts.Journal,
		decryptKey:          opts.DecryptKey,
		maxAttempts:         normalizeMaxAttempts(opts.MaxAttempts),
		requestTimeout:      timeoutOrDefault(opts.RequestTimeout, config.DefaultUpstreamStreamTimeout),
		firstContentTimeout: timeoutOrDefault(opts.FirstContentTimeout, config.DefaultFirstContentTimeout),
		writeTimeout:        timeoutOrDefault(opts.WriteTimeout, config.DefaultDownstreamWriteTimeout),
		logger:              logger,
		now:                 now,
		newRequestID:        opts.RequestID,
		maxBufferBytes:      defaultStreamBufferLimit,
	}, nil
}

// streamFrameClass 是一个 SSE 事件在提交/终止语义上的分类。
type streamFrameClass struct {
	// semantic 表示事件携带用户可见的语义内容；必须先把缓冲写出去。
	semantic bool
	// completed 表示上游已给出协议完成信号（Responses completed/incomplete、
	// Chat finish_reason/[DONE]）。Chat 的 finish_reason 之后仍可能跟 usage/DONE。
	completed bool
	// terminal 表示该事件就是流的最后一个成功事件。
	terminal bool
	// failure 表示上游明确报告失败（Responses response.failed/error、
	// Chat error 对象或 event: error）。
	failure    bool
	invalid    bool
	incomplete bool
}

// streamGate 管理“首语义内容前缓冲、提交后直通”的状态。
//
// committed 一旦为 true，HTTP 200 与 SSE 响应头已经写出，调用方不得再回退
// 到 JSON 错误，也不得切换渠道。pending 只在未提交时保存原始 SSE 字节。
type streamGate struct {
	protocol     string
	info         RequestInfo
	requestID    string
	w            http.ResponseWriter
	ctx          context.Context
	writeTimeout time.Duration
	firstBudget  *firstContentBudget
	maxBuffer    int
	pending      []byte
	committed    bool
	completed    bool
	written      int64
	lastByte     byte
	blankTail    bool
	responseID   string
	createdAt    int64
	model        string
	lastSeq      int64
	hasSequence  bool
	ttftMS       *int64
	requestAt    time.Time
	now          func() time.Time
}

// buffer 保存提交前的原始事件，并执行缓冲上限。
func (g *streamGate) buffer(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	if len(g.pending)+len(raw) > g.maxBuffer {
		return fmt.Errorf("gateway: 首内容前缓冲超过 %d 字节", g.maxBuffer)
	}
	g.pending = append(g.pending, raw...)
	return nil
}

// commit 写出 SSE 响应头以及首语义内容之前的缓冲，再写出当前事件。
//
// 第一次调用时写入 HTTP 200；之后的调用只追加事件。Write 或 Flush 失败
// 必须取消上游并停止重试；只有入站 context 取消才记为客户端取消。
func (g *streamGate) commit(raw []byte) error {
	if !g.committed {
		if err := g.firstBudget.commit(); err != nil {
			return err
		}
		if err := renewWriteDeadline(g.ctx, g.w, g.writeTimeout); err != nil {
			return err
		}
		header := g.w.Header()
		header.Set("Content-Type", streamContentType)
		header.Set("Cache-Control", "no-store")
		header.Set("Pragma", "no-cache")
		header.Set("X-Accel-Buffering", "no")
		g.w.WriteHeader(http.StatusOK)
		g.committed = true

		if len(g.pending) > 0 {
			if err := g.write(g.pending); err != nil {
				return err
			}
			g.pending = nil
		}
		if err := http.NewResponseController(g.w).Flush(); err != nil {
			return err
		}
		ttft := g.now().Sub(g.requestAt).Milliseconds()
		if ttft < 0 {
			ttft = 0
		}
		g.ttftMS = &ttft
	}
	return g.write(raw)
}

// write 写出一段原始字节并 flush。
func (g *streamGate) write(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	if err := renewWriteDeadline(g.ctx, g.w, g.writeTimeout); err != nil {
		return err
	}
	n, err := g.w.Write(raw)
	g.written += int64(n)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	g.lastByte = raw[len(raw)-1]
	g.blankTail = hasBlankLineSuffix(raw)
	return http.NewResponseController(g.w).Flush()
}

// hasBlankLineSuffix 判断一段已写出的 SSE 字节是否以空行结束。
func hasBlankLineSuffix(raw []byte) bool {
	return bytes.HasSuffix(raw, []byte("\n\n")) ||
		bytes.HasSuffix(raw, []byte("\r\n\r\n"))
}

// withEventBoundary 在补写协议终止事件前补齐事件分隔。
//
// 正常上游事件以空行结束；如果最后一个事件在 EOF 处被截断（例如只有一行
// data 没有结尾空行），直接追加 response.failed 会被客户端解析为上一个事件
// 的续行。这里根据已写出的最后字节补一个或两个换行。
func (g *streamGate) withEventBoundary(frame []byte) []byte {
	if g.written == 0 || g.blankTail {
		return frame
	}
	prefix := "\n\n"
	if g.lastByte == '\n' {
		prefix = "\n"
	}
	out := make([]byte, 0, len(prefix)+len(frame))
	out = append(out, prefix...)
	out = append(out, frame...)
	return out
}

// observe 从 Responses 事件中提取 response.id/created_at/model 与
// sequence_number，供提交后错误事件补齐严格字段。
func (g *streamGate) observe(event upstream.SSEEvent) {
	if g.protocol != ProtocolOpenAIResponses || len(event.Data) == 0 {
		return
	}

	var payload struct {
		SequenceNumber *int64 `json:"sequence_number"`
		Response       *struct {
			ID        string `json:"id"`
			CreatedAt int64  `json:"created_at"`
			Model     string `json:"model"`
		} `json:"response"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return
	}
	if payload.SequenceNumber != nil {
		g.lastSeq = *payload.SequenceNumber
		g.hasSequence = true
	}
	if payload.Response != nil {
		if payload.Response.ID != "" {
			g.responseID = payload.Response.ID
		}
		if payload.Response.CreatedAt > 0 {
			g.createdAt = payload.Response.CreatedAt
		}
		if payload.Response.Model != "" {
			g.model = payload.Response.Model
		}
	}
}

// emitFailure 写出提交后的协议终止错误。
//
// Responses：只发一次 response.failed，包含 type、sequence_number 与完整
// response 子对象（id/object/created_at/model/status/output/error）。
// Chat：发 error 对象 + [DONE]，HTTP 200 已由提交阶段固化。
func (g *streamGate) emitFailure(failure proxyFailure) error {
	var frame []byte
	switch g.protocol {
	case ProtocolOpenAIResponses:
		frame = g.responsesFailedFrame(failure)
	case ProtocolOpenAIChat:
		frame = g.chatErrorFrame(failure)
	default:
		return nil
	}
	return g.write(g.withEventBoundary(frame))
}

// responsesFailedFrame 构造严格的 Responses response.failed SSE 帧。
func (g *streamGate) responsesFailedFrame(failure proxyFailure) []byte {
	sequence := int64(0)
	if g.hasSequence {
		sequence = g.lastSeq + 1
	}

	responseID := g.responseID
	if responseID == "" {
		responseID = "resp_" + g.requestID
	}
	createdAt := g.createdAt
	if createdAt <= 0 {
		createdAt = g.now().Unix()
	}
	model := g.model
	if model == "" {
		model = g.info.Model
	}

	payload := struct {
		Type           string `json:"type"`
		SequenceNumber int64  `json:"sequence_number"`
		Response       struct {
			ID        string `json:"id"`
			Object    string `json:"object"`
			CreatedAt int64  `json:"created_at"`
			Model     string `json:"model"`
			Status    string `json:"status"`
			Output    []any  `json:"output"`
			Error     struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}{
		Type:           "response.failed",
		SequenceNumber: sequence,
	}
	payload.Response.ID = responseID
	payload.Response.Object = "response"
	payload.Response.CreatedAt = createdAt
	payload.Response.Model = model
	payload.Response.Status = "failed"
	payload.Response.Output = []any{}
	payload.Response.Error.Code = failure.Code
	payload.Response.Error.Message = failure.Message

	encoded, err := json.Marshal(payload)
	if err != nil {
		// 结构里的字段都是字符串/整数/空数组，Marshal 不会失败；即使发生，
		// 也只返回一个不含任何上游内容的固定失败帧。
		encoded = []byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"resp_error","object":"response","created_at":0,"model":"","status":"failed","output":[],"error":{"code":"internal_error","message":"Internal server error."}}}`)
	}

	frame := make([]byte, 0, len(encoded)+32)
	frame = append(frame, "event: response.failed\ndata: "...)
	frame = append(frame, encoded...)
	frame = append(frame, "\n\n"...)
	return frame
}

// chatErrorFrame 构造 Chat 提交后的 error 对象 + [DONE]。
func (g *streamGate) chatErrorFrame(failure proxyFailure) []byte {
	encoded, err := json.Marshal(errorEnvelope{Error: failure.apiError()})
	if err != nil {
		encoded = []byte(`{"error":{"message":"Internal server error.","type":"server_error","code":"internal_error"}}`)
	}

	frame := make([]byte, 0, len(encoded)+24)
	frame = append(frame, "data: "...)
	frame = append(frame, encoded...)
	frame = append(frame, "\n\ndata: [DONE]\n\n"...)
	return frame
}

// classifyStreamFrame 按入站协议分类一个 SSE 事件。
func classifyStreamFrame(protocol string, event upstream.SSEEvent) streamFrameClass {
	switch protocol {
	case ProtocolOpenAIResponses:
		return classifyResponsesFrame(event)
	case ProtocolOpenAIChat:
		return classifyChatFrame(event)
	default:
		return streamFrameClass{}
	}
}

// responsesEnvelope 是分类 Responses 事件所需的最小子集。
type responsesEnvelope struct {
	Type     string          `json:"type"`
	Delta    json.RawMessage `json:"delta"`
	Error    json.RawMessage `json:"error"`
	Response *struct {
		ID     string          `json:"id"`
		Object string          `json:"object"`
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
		Error  json.RawMessage `json:"error"`
	} `json:"response"`
}

func classifyResponsesFrame(event upstream.SSEEvent) streamFrameClass {
	kind := strings.TrimSpace(string(event.Event))
	var payload responsesEnvelope
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			if kind == "error" {
				return streamFrameClass{failure: true}
			}
			if kind == "response.completed" || kind == "response.incomplete" {
				return streamFrameClass{invalid: true}
			}
			return streamFrameClass{}
		}
	}
	if kind == "" || !knownResponsesEvent(kind) {
		if payload.Type != "" {
			kind = strings.TrimSpace(payload.Type)
		}
	}
	if kind == "error" || (kind == "" && jsonValueNonEmpty(payload.Error)) {
		return streamFrameClass{failure: true}
	}

	switch kind {
	case "response.failed", "response.cancelled":
		return streamFrameClass{failure: true}

	case "response.output_text.delta",
		"response.function_call_arguments.delta",
		"response.reasoning_summary_text.delta",
		"response.reasoning_text.delta",
		"response.refusal.delta":
		if jsonValueNonEmpty(payload.Delta) {
			return streamFrameClass{semantic: true}
		}
		return streamFrameClass{}

	case "response.output_text.done",
		"response.function_call_arguments.done",
		"response.reasoning_summary_text.done",
		"response.reasoning_text.done",
		"response.refusal.done",
		"response.output_item.done",
		"response.content_part.done":
		return streamFrameClass{semantic: true}

	case "response.completed", "response.incomplete":
		response := payload.Response
		if payload.Type != kind || response == nil || response.ID == "" || response.Object != "response" ||
			response.Status != strings.TrimPrefix(kind, "response.") || jsonValueNonEmpty(response.Error) {
			return streamFrameClass{invalid: true}
		}
		var output []json.RawMessage
		if len(response.Output) == 0 || bytes.Equal(bytes.TrimSpace(response.Output), []byte("null")) || json.Unmarshal(response.Output, &output) != nil {
			return streamFrameClass{invalid: true}
		}
		return streamFrameClass{semantic: true, completed: true, terminal: true, incomplete: kind == "response.incomplete"}

	default:
		return streamFrameClass{}
	}
}

// knownResponsesEvent 用于判断 event 字段是否是本网关理解的 Responses 事件；
// 不是时回退到 data.type，兼容省略 event 行或使用通用事件名的上游。
func knownResponsesEvent(kind string) bool {
	switch kind {
	case "response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.reasoning_summary_part.added", "response.reasoning_part.added",
		"response.output_text.delta", "response.output_text.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done",
		"response.refusal.delta", "response.refusal.done",
		"response.output_item.done", "response.content_part.done",
		"response.completed", "response.incomplete", "response.failed",
		"response.cancelled", "error":
		return true
	default:
		return false
	}
}

// classifyChatFrame 分类 Chat Completions SSE。data 为 [DONE] 时是成功终止；
// choices[].delta 的文本、tool_calls、reasoning_content 都算语义内容；
// finish_reason 表示协议已完成，但通常后面还会跟 usage/[DONE]。
func classifyChatFrame(event upstream.SSEEvent) streamFrameClass {
	if strings.TrimSpace(string(event.Event)) == "error" {
		return streamFrameClass{failure: true}
	}
	data := bytes.TrimSpace(event.Data)
	if len(data) == 0 {
		return streamFrameClass{}
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return streamFrameClass{completed: true, terminal: true}
	}

	var payload struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				Reasoning        json.RawMessage `json:"reasoning"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
				FunctionCall     json.RawMessage `json:"function_call"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return streamFrameClass{}
	}
	if jsonValueNonEmpty(payload.Error) {
		return streamFrameClass{failure: true}
	}

	class := streamFrameClass{}
	for _, choice := range payload.Choices {
		if jsonValueNonEmpty(choice.Delta.Content) ||
			jsonValueNonEmpty(choice.Delta.ReasoningContent) ||
			jsonValueNonEmpty(choice.Delta.Reasoning) ||
			jsonValueNonEmpty(choice.Delta.ToolCalls) ||
			jsonValueNonEmpty(choice.Delta.FunctionCall) {
			class.semantic = true
		}
		if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
			class.semantic = true
			class.completed = true
		}
	}
	return class
}

// jsonValueNonEmpty 判断一个 JSON 值是否携带内容；null、""、[]、{} 视为空。
func jsonValueNonEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	switch string(trimmed) {
	case `""`, "[]", "{}":
		return false
	default:
		return true
	}
}

// streamAttemptResult 是一次流式渠道尝试的结果。
type streamAttemptResult struct {
	record    observability.RequestAttempt
	failure   *proxyFailure
	committed bool
	success   bool
	canceled  bool
	bytes     int64
	ttftMS    *int64
}

// streamAttemptRuntime 是一次流式尝试所需的请求上下文。
type streamAttemptRuntime struct {
	ctx         context.Context
	cancel      context.CancelFunc
	w           http.ResponseWriter
	firstBudget *firstContentBudget
	requestID   string
	index       int
	candidate   routing.Candidate
	body        []byte
	headers     http.Header
	info        RequestInfo
	requestAt   time.Time
}

// ServeHTTP 执行流式失败切换。
func (s *StreamProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.scheduler == nil || s.upstream == nil || s.journal == nil {
		writeUnavailable(w)
		return
	}
	if r == nil || r.URL == nil {
		writeNotFound(w)
		return
	}
	info, ok := RequestInfoFromContext(r.Context())
	if !ok {
		writeInternalError(w)
		return
	}

	body, err := readProxyBody(r)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeBodyTooLarge(w)
		} else {
			writeInvalidJSON(w)
		}
		return
	}

	requestAt := s.now()
	requestID := s.requestID(r)
	requestCtx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	ctx, firstBudget := startFirstContentBudget(requestCtx, s.firstContentTimeout)
	defer firstBudget.close()

	attempts := make([]observability.RequestAttempt, 0, s.maxAttempts)
	excluded := make(map[int64]struct{}, s.maxAttempts)
	var lastFailure *proxyFailure
	var lastTTFT *int64
	var responseBytes *int64

	for len(attempts) < s.maxAttempts {
		if err := ctx.Err(); err != nil {
			failure := requestFailure(ctx, err)
			httpStatus := failure.Status
			if !failure.Canceled {
				writeFailure(w, failure, s.writeTimeout)
			}
			s.recordStreamRequest(requestID, info, attempts,
				attemptStatus(failure), &httpStatus, &failure,
				false, nil, lastTTFT, requestAt)
			return
		}

		candidate, slot, err := s.scheduler.AcquireExcluding(
			ctx, info.Model, info.Protocol, excluded)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				failure := requestFailure(ctx, err)
				status := failure.Status
				if failure.Canceled {
					status = httpStatusClientClosedRequest
					failure.Status = status
				}
				if !failure.Canceled {
					writeFailure(w, failure, s.writeTimeout)
				}
				s.recordStreamRequest(requestID, info, attempts,
					attemptStatus(failure), &status, &failure,
					false, responseBytes, lastTTFT, requestAt)
				return
			}
			if len(attempts) > 0 {
				break
			}
			failure := schedulingFailure(err)
			writeFailure(w, failure, s.writeTimeout)
			s.recordStreamRequest(requestID, info, attempts,
				observability.StatusError, &failure.Status, &failure,
				false, nil, nil, requestAt)
			return
		}

		result := s.runStreamAttempt(streamAttemptRuntime{
			ctx:         ctx,
			cancel:      cancel,
			w:           w,
			firstBudget: firstBudget,
			requestID:   requestID,
			index:       len(attempts) + 1,
			candidate:   candidate,
			body:        body,
			headers:     r.Header,
			info:        info,
			requestAt:   requestAt,
		}, slot)
		attempts = append(attempts, result.record)
		s.writeAttempt(result.record)
		if result.ttftMS != nil {
			lastTTFT = result.ttftMS
		}
		if result.bytes > 0 {
			size := result.bytes
			responseBytes = &size
		}

		if result.canceled {
			status := httpStatusClientClosedRequest
			failure := result.failure
			if failure == nil {
				empty := classifyUpstreamError(context.Canceled)
				failure = &empty
			}
			s.recordStreamRequest(requestID, info, attempts,
				observability.StatusCancelled, &status, failure,
				result.committed, responseBytes, lastTTFT, requestAt)
			return
		}

		if result.committed {
			// 提交后已经写出 200 与 SSE 字节，不能回退 JSON，也不能换渠道。
			httpStatus := http.StatusOK
			status := observability.StatusSuccess
			if result.failure != nil {
				status = observability.StatusError
			}
			s.recordStreamRequest(requestID, info, attempts,
				status, &httpStatus, result.failure,
				true, responseBytes, lastTTFT, requestAt)
			return
		}

		if result.success {
			httpStatus := http.StatusOK
			s.recordStreamRequest(requestID, info, attempts,
				observability.StatusSuccess, &httpStatus, nil,
				true, responseBytes, lastTTFT, requestAt)
			return
		}

		if result.failure == nil {
			failure := schedulingFailure(errors.New("gateway: 流式代理返回空结果"))
			lastFailure = &failure
			break
		}
		lastFailure = result.failure
		excluded[candidate.Channel.ID] = struct{}{}
		if !result.failure.Retry {
			break
		}
	}

	if lastFailure == nil {
		failure := schedulingFailure(routing.ErrNoCandidates)
		lastFailure = &failure
	}
	writeFailure(w, *lastFailure, s.writeTimeout)
	s.recordStreamRequest(requestID, info, attempts,
		observability.StatusError, &lastFailure.Status, lastFailure,
		false, nil, lastTTFT, requestAt)
}

// runStreamAttempt 执行一次完整的流式渠道尝试。
//
// 返回前已完成：Key 解密、上游请求、SSE 解析、首内容提交门控与提交后错误
// 写出；槽位在返回时释放。调用方负责 attempt/request 日志与是否重试。
func (s *StreamProxy) runStreamAttempt(
	rt streamAttemptRuntime,
	slot *routing.Slot,
) streamAttemptResult {
	defer slot.Release()
	attemptStarted := s.now()

	key, err := s.decryptKey(rt.candidate.Channel.KeyCiphertext)
	if err != nil || strings.TrimSpace(key) == "" {
		failure := upstreamConfigurationFailure()
		s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(0))
		return s.streamFailureResult(rt, attemptStarted, nil, &failure, false, 0, nil)
	}

	response, err := s.upstream.Send(rt.ctx, upstream.Target{
		ChannelID:   rt.candidate.Channel.ID,
		ChannelName: rt.candidate.Channel.Name,
		BaseURL:     rt.candidate.Channel.BaseURL,
		Protocol:    rt.candidate.Channel.Protocol,
		APIKey:      key,
	}, upstream.Request{
		UpstreamModel: rt.candidate.Route.UpstreamModel,
		Body:          rt.body,
		Headers:       rt.headers,
		Stream:        true,
	})
	if err != nil {
		failure := requestFailure(rt.ctx, err)
		if !failure.Canceled {
			s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(0))
		}
		return s.streamFailureResult(rt, attemptStarted, nil, &failure, false, 0, nil)
	}
	if response == nil {
		failure := proxyFailure{
			Status:  http.StatusInternalServerError,
			Code:    errorCodeInternal,
			Type:    errorTypeServer,
			Message: "Internal server error.",
		}
		return s.streamFailureResult(rt, attemptStarted, nil, &failure, false, 0, nil)
	}
	if response.Body == nil {
		response.Body = http.NoBody
	}
	response.Header = upstream.FilterResponseHeaders(response.Header)

	upstreamStatus := response.StatusCode
	if upstreamStatus < http.StatusOK || upstreamStatus >= http.StatusMultipleChoices {
		failure := classifyHTTPFailure(response, s.now())
		_ = response.Body.Close()
		if rt.ctx.Err() != nil {
			failure = requestFailure(rt.ctx, rt.ctx.Err())
		}
		if !failure.Canceled {
			s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(upstreamStatus))
		}
		return s.streamFailureResult(rt, attemptStarted, &upstreamStatus, &failure, false, 0, nil)
	}
	defer response.Body.Close()
	httpStatus := upstreamStatus

	// 请求了 stream 但上游返回 JSON（例如某些兼容层忽略 stream=true）时，
	// 不能把它当成空 SSE：先有界读取并丢弃 body，再按可重试的上游错误处理，
	// 由失败切换选择下一个渠道。
	if contentType := strings.TrimSpace(response.Header.Get("Content-Type")); contentType != "" {
		if mediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr != nil ||
			!strings.EqualFold(mediaType, "text/event-stream") {
			_, _ = io.CopyN(io.Discard, response.Body, maxNonSSEBodyBytes)
			if rt.ctx.Err() != nil {
				failure := requestFailure(rt.ctx, rt.ctx.Err())
				return s.streamFailureResult(rt, attemptStarted, &httpStatus, &failure, false, 0, nil)
			}
			failure := proxyFailure{
				Status:  http.StatusBadGateway,
				Code:    errorCodeUpstreamError,
				Type:    errorTypeServer,
				Message: "Upstream returned a non-streaming response.",
				Retry:   true,
			}
			s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(upstreamStatus))
			return s.streamFailureResult(rt, attemptStarted, &upstreamStatus, &failure, false, 0, nil)
		}
	}

	reader := upstream.NewSSEReader(response.Body, upstream.SSEReaderOptions{})
	defer reader.Close()
	gate := &streamGate{
		protocol:     rt.info.Protocol,
		info:         rt.info,
		requestID:    rt.requestID,
		w:            rt.w,
		ctx:          rt.ctx,
		writeTimeout: s.writeTimeout,
		firstBudget:  rt.firstBudget,
		maxBuffer:    s.maxBufferBytes,
		requestAt:    rt.requestAt,
		now:          s.now,
	}

	for {
		if err := rt.ctx.Err(); err != nil {
			failure := requestFailure(rt.ctx, err)
			return s.streamFailureResult(rt, attemptStarted, &httpStatus,
				&failure, gate.committed, gate.written, gate.ttftMS)
		}

		event, err := reader.Next()
		if err != nil {
			if ctxErr := rt.ctx.Err(); ctxErr != nil {
				failure := requestFailure(rt.ctx, ctxErr)
				return s.streamFailureResult(rt, attemptStarted, &httpStatus,
					&failure, gate.committed, gate.written, gate.ttftMS)
			}
			if errors.Is(err, io.EOF) {
				if gate.completed && gate.committed {
					s.scheduler.RecordSuccess(rt.candidate)
					return s.streamSuccessResult(rt, attemptStarted, &httpStatus,
						gate.written, gate.ttftMS)
				}
				failure := interruptedStreamFailure()
				s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(0))
				if !gate.committed {
					return s.streamFailureResult(rt, attemptStarted, &httpStatus,
						&failure, false, 0, nil)
				}
				if writeErr := gate.emitFailure(failure); writeErr != nil {
					return s.downstreamStreamResult(rt, attemptStarted, &httpStatus,
						gate.committed, gate.written, gate.ttftMS, writeErr)
				}
				return s.streamFailureResult(rt, attemptStarted, &httpStatus,
					&failure, true, gate.written, gate.ttftMS)
			}

			failure := streamReadFailure(err)
			if ctxErr := rt.ctx.Err(); ctxErr != nil {
				failure = requestFailure(rt.ctx, ctxErr)
			}
			if failure.Canceled {
				// 客户端断开：不再写终止事件，也不再重试。
				return s.streamFailureResult(rt, attemptStarted, &httpStatus,
					&failure, gate.committed, gate.written, gate.ttftMS)
			}
			s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(0))
			if !gate.committed {
				return s.streamFailureResult(rt, attemptStarted, &httpStatus,
					&failure, false, 0, nil)
			}
			if writeErr := gate.emitFailure(failure); writeErr != nil {
				return s.downstreamStreamResult(rt, attemptStarted, &httpStatus,
					gate.committed, gate.written, gate.ttftMS, writeErr)
			}
			return s.streamFailureResult(rt, attemptStarted, &httpStatus,
				&failure, true, gate.written, gate.ttftMS)
		}

		gate.observe(event)
		class := classifyStreamFrame(rt.info.Protocol, event)

		if class.failure || class.invalid {
			failure := upstreamStreamFailure()
			if class.failure {
				failure = classifyStructuredFailure(failure, event.Data)
			}
			s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(0))
			if !gate.committed {
				return s.streamFailureResult(rt, attemptStarted, &httpStatus,
					&failure, false, 0, nil)
			}
			if writeErr := gate.emitFailure(failure); writeErr != nil {
				return s.downstreamStreamResult(rt, attemptStarted, &httpStatus,
					gate.committed, gate.written, gate.ttftMS, writeErr)
			}
			return s.streamFailureResult(rt, attemptStarted, &httpStatus,
				&failure, true, gate.written, gate.ttftMS)
		}

		if class.semantic || class.terminal {
			if err := gate.commit(event.Raw); err != nil {
				return s.downstreamStreamResult(rt, attemptStarted, &httpStatus,
					gate.committed, gate.written, gate.ttftMS, err)
			}
		} else if !gate.committed {
			if err := gate.buffer(event.Raw); err != nil {
				failure := streamBufferLimitFailure()
				s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(0))
				return s.streamFailureResult(rt, attemptStarted, &httpStatus,
					&failure, false, 0, nil)
			}
		} else if err := gate.write(event.Raw); err != nil {
			return s.downstreamStreamResult(rt, attemptStarted, &httpStatus,
				gate.committed, gate.written, gate.ttftMS, err)
		}

		if class.completed {
			gate.completed = true
		}
		if class.terminal {
			if class.incomplete {
				failure := proxyFailure{Status: http.StatusOK, Code: "upstream_incomplete", Type: errorTypeServer, Message: "Upstream returned an incomplete response.", Scope: routing.FailureScopeRequest}
				s.scheduler.RecordFailure(rt.candidate, failure.routingFailure(upstreamStatus))
				return s.streamFailureResult(rt, attemptStarted, &httpStatus, &failure, true, gate.written, gate.ttftMS)
			}
			s.scheduler.RecordSuccess(rt.candidate)
			return s.streamSuccessResult(rt, attemptStarted, &httpStatus,
				gate.written, gate.ttftMS)
		}
	}
}

// streamFailureResult 构造提交前/提交后的失败结果。
func (s *StreamProxy) streamFailureResult(
	rt streamAttemptRuntime,
	started time.Time,
	httpStatus *int,
	failure *proxyFailure,
	committed bool,
	written int64,
	ttftMS *int64,
) streamAttemptResult {
	status := observability.StatusError
	canceled := failure != nil && failure.Canceled
	if canceled {
		status = observability.StatusCancelled
	}
	record := newAttemptRecord(rt.requestID, rt.index, rt.candidate,
		status, httpStatus, failure, committed, started, s.now())
	return streamAttemptResult{
		record:    record,
		failure:   failure,
		committed: committed,
		canceled:  canceled,
		bytes:     written,
		ttftMS:    ttftMS,
	}
}

// streamSuccessResult 构造成功结果；成功流必然已经提交。
func (s *StreamProxy) streamSuccessResult(
	rt streamAttemptRuntime,
	started time.Time,
	httpStatus *int,
	written int64,
	ttftMS *int64,
) streamAttemptResult {
	record := newAttemptRecord(rt.requestID, rt.index, rt.candidate,
		observability.StatusSuccess, httpStatus, nil, true, started, s.now())
	return streamAttemptResult{
		record:    record,
		committed: true,
		success:   true,
		bytes:     written,
		ttftMS:    ttftMS,
	}
}

// downstreamStreamResult 保留真实取消与写失败的区别；两者均不重试。
func (s *StreamProxy) downstreamStreamResult(
	rt streamAttemptRuntime,
	started time.Time,
	httpStatus *int,
	committed bool,
	written int64,
	ttftMS *int64,
	err error,
) streamAttemptResult {
	failure := downstreamFailure(rt.ctx, err)
	rt.cancel()
	record := newAttemptRecord(rt.requestID, rt.index, rt.candidate,
		attemptStatus(failure), httpStatus, &failure,
		committed, started, s.now())
	return streamAttemptResult{
		record:    record,
		failure:   &failure,
		committed: committed,
		canceled:  failure.Canceled,
		bytes:     written,
		ttftMS:    ttftMS,
	}
}

// interruptedStreamFailure 是上游在语义提交前/后无终止事件断流的固定错误。
func interruptedStreamFailure() proxyFailure {
	return proxyFailure{
		Status:  http.StatusBadGateway,
		Code:    errorCodeUpstreamError,
		Type:    errorTypeServer,
		Message: "Upstream stream ended unexpectedly.",
		Retry:   true,
	}
}

// upstreamStreamFailure 是上游通过 SSE 明确报告错误的固定错误。
func upstreamStreamFailure() proxyFailure {
	return proxyFailure{
		Status:  http.StatusBadGateway,
		Code:    errorCodeUpstreamError,
		Type:    errorTypeServer,
		Message: "Upstream service returned an error.",
		Retry:   true,
	}
}

// streamBufferLimitFailure 表示语义提交前的上游事件超过内存缓冲上限。
func streamBufferLimitFailure() proxyFailure {
	return proxyFailure{
		Status:  http.StatusBadGateway,
		Code:    errorCodeUpstreamError,
		Type:    errorTypeServer,
		Message: "Upstream stream exceeded the buffer limit.",
		Retry:   true,
	}
}

// streamReadFailure 把 SSE 解析错误映射为客户端可见的固定失败。
func streamReadFailure(err error) proxyFailure {
	switch {
	case errors.Is(err, upstream.ErrSSEIdleTimeout),
		errors.Is(err, upstream.ErrStreamIdleTimeout):
		return classifyUpstreamError(upstream.ErrStreamIdleTimeout)
	case errors.Is(err, upstream.ErrSSETruncatedEvent):
		return interruptedStreamFailure()
	case errors.Is(err, upstream.ErrSSEEventTooLarge):
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamError,
			Type:    errorTypeServer,
			Message: "Upstream stream event exceeded the size limit.",
			Retry:   true,
		}
	default:
		return classifyUpstreamError(err)
	}
}

// requestID 优先使用注入的生成器；返回值非法时回退内部随机 ID。
func (s *StreamProxy) requestID(r *http.Request) string {
	if s.newRequestID != nil {
		if id := strings.TrimSpace(s.newRequestID(r)); validRequestID(id) {
			return id
		}
	}
	return generateRequestID()
}

// writeAttempt 写一条 attempt。写入失败不影响请求继续。
func (s *StreamProxy) writeAttempt(attempt observability.RequestAttempt) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultLogWriteTimeout)
	defer cancel()
	if err := s.journal.WriteAttempt(ctx, attempt); err != nil {
		s.logger.Error("写入渠道尝试失败",
			"request_id", attempt.RequestID,
			"attempt_index", attempt.AttemptIndex,
		)
	}
}

// recordStreamRequest 写最终 request_logs。
//
// 复用 T20 的 requestLogFromAttempts 保证渠道快照/attempt 计数一致，再标记
// stream=true。usage 与更细的 token 统计由 T23 补齐。
func (s *StreamProxy) recordStreamRequest(
	requestID string,
	info RequestInfo,
	attempts []observability.RequestAttempt,
	status string,
	httpStatus *int,
	failure *proxyFailure,
	semanticCommitted bool,
	responseBytes *int64,
	ttftMS *int64,
	startedAt time.Time,
) {
	finishedAt := s.now()
	log := requestLogFromAttempts(requestID, info, attempts, status, httpStatus,
		failure, semanticCommitted, responseBytes, startedAt, finishedAt)
	log.Stream = true
	log.TTFTMS = ttftMS

	ctx, cancel := context.WithTimeout(context.Background(), defaultLogWriteTimeout)
	defer cancel()
	if err := s.journal.WriteRequest(ctx, log); err != nil {
		s.logger.Error("写入请求日志失败", "request_id", log.RequestID)
	}
}

var _ http.Handler = (*StreamProxy)(nil)
