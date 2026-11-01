// observability.go 把 usage 提取接入现有数据面（T23）。
//
// 现有 Proxy/StreamProxy 只负责转发与错误分类；本文件在它们外侧提供
// ObserveGateway：为每个入站请求生成稳定的 request_id，把响应字节 tee 给
// 协议级 usage 观察器，并在最终 request_logs 落库前补齐 token 与
// usage_source。
//
// 关键约束：
//   - 一次请求只有一个 request_log writer：attempt 仍由原代理写，最终
//     WriteRequest 被拦截一次、补 usage 后写一次；
//   - 只读上游已经返回的 2xx body，不保存 body/header/prompt，不改变
//     转发字节；
//   - usage 缺失时 token 保持 NULL，绝不按文本长度估算；
//   - 客户端断开、semantic_committed、attempt_count、TTFT、latency、
//     response_bytes 全部沿用原代理的结果，本层不重算。
//
// ObserveGateway 必须在服务开始接收请求前调用一次；它会把 proxy/stream
// 的内部 sender 与 journal 替换为观测包装。未经过 ObserveGateway 的
// Proxy 仍保持 T20/T21 的行为（无 usage）。
package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/requestid"
	"ai-router/internal/upstream"
)

const (
	// defaultUsageBodyCaptureLimit 是非流式 2xx 响应为解析 usage 而额外
	// 保留的最大字节数。超过上限时放弃本次 usage（token 记为 NULL），
	// 不影响已经/正在转发的响应字节。
	defaultUsageBodyCaptureLimit = 32 << 20
)

// requestUsage 是一次入站请求的 usage 暂存区。
//
// 同一请求内的多次渠道尝试共享该对象；每次新的上游尝试开始时清空，
// 避免把失败尝试的 usage 错记到最终成功的请求上。
type requestUsage struct {
	requestID string

	mu     sync.Mutex
	result observability.UsageResult
	logged bool
}

// markLogged 记录代理是否已经为该请求写入最终 request_log。
func (u *requestUsage) markLogged() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.logged = true
	u.mu.Unlock()
}

func (u *requestUsage) wasLogged() bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.logged
}

func (u *requestUsage) reset() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.result = observability.UsageResult{}
	u.mu.Unlock()
}

func (u *requestUsage) set(result observability.UsageResult) {
	if u == nil || !result.Found() {
		return
	}
	u.mu.Lock()
	u.result = result
	u.mu.Unlock()
}

func (u *requestUsage) snapshot() observability.UsageResult {
	if u == nil {
		return observability.UsageResult{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.result
}

// usageRegistry 按 request_id 关联“正在处理的请求”的 usage 暂存区。
//
// 原代理在 recordRequest 时只带 request_id；这里不改变 AttemptJournal
// 接口，因此在进程内做一次短生命周期映射。请求结束后立即删除，避免
// 日志量增长或串请求。
type usageRegistry struct {
	mu   sync.Mutex
	byID map[string]*requestUsage
}

func newUsageRegistry() *usageRegistry {
	return &usageRegistry{byID: make(map[string]*requestUsage)}
}

func (r *usageRegistry) add(usage *requestUsage) {
	if r == nil || usage == nil {
		return
	}
	r.mu.Lock()
	r.byID[usage.requestID] = usage
	r.mu.Unlock()
}

func (r *usageRegistry) get(requestID string) *requestUsage {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[requestID]
}

func (r *usageRegistry) exists(requestID string) bool {
	return r.get(requestID) != nil
}

func (r *usageRegistry) remove(requestID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.byID, requestID)
	r.mu.Unlock()
}

type requestUsageContextKey struct{}

func requestUsageFromContext(ctx context.Context) *requestUsage {
	if ctx == nil {
		return nil
	}
	usage, _ := ctx.Value(requestUsageContextKey{}).(*requestUsage)
	return usage
}

// ObservedGateway 是带 usage 采集的数据面 Handler。
//
// 它包装原始网关 Handler（路由/鉴权/错误 envelope），在请求进入前注入
// request_id 与 usage 暂存区，其余行为完全由原 Handler/代理决定。
type ObservedGateway struct {
	inner     http.Handler
	registry  *usageRegistry
	requestID func(*http.Request) string
	capture   *observability.CaptureRing
	journal   AttemptJournal
}

var _ http.Handler = (*ObservedGateway)(nil)

// ObserveGateway 为已有的网关 Handler/代理启用 usage 采集与请求日志补齐。
//
// handler 通常是 *Handler，且其 Forwarder 必须最终调用 proxy；proxy 的
// streamHandler 应指向 stream（未传 stream 时会尝试从该字段发现）。
// journal 是真正落库的仓储，nil 时使用 proxy 当前的 journal。函数会替换
// proxy/stream 的 sender、journal 与 RequestID 生成器，因此只能在开始
// ServeHTTP 之前调用一次。
func ObserveGateway(
	handler http.Handler,
	proxy *Proxy,
	stream *StreamProxy,
	journal AttemptJournal,
) (*ObservedGateway, error) {
	if handler == nil {
		return nil, errors.New("gateway: 观测包装缺少 Handler")
	}
	if proxy == nil {
		return nil, errors.New("gateway: 观测包装缺少代理")
	}
	if proxy.upstream == nil || proxy.journal == nil {
		return nil, errors.New("gateway: 代理缺少上游客户端或日志仓储")
	}
	if journal == nil {
		journal = proxy.journal
	}
	if journal == nil {
		return nil, errors.New("gateway: 观测包装缺少日志仓储")
	}
	if stream == nil {
		// 允许调用方只传 Handler/Proxy：如果 stream handler 已经接到
		// proxy.streamHandler，直接复用，避免漏掉流式 usage。
		if candidate, ok := proxy.streamHandler.(*StreamProxy); ok {
			stream = candidate
		}
	}
	if stream != nil && (stream.upstream == nil || stream.journal == nil) {
		return nil, errors.New("gateway: 流式代理缺少上游客户端或日志仓储")
	}

	registry := newUsageRegistry()
	outerRequestID := proxy.newRequestID

	proxy.upstream = &usageCapturingSender{
		inner:   proxy.upstream,
		maxBody: defaultUsageBodyCaptureLimit,
	}
	proxy.journal = &usageCapturingJournal{inner: journal, registry: registry}
	proxy.newRequestID = observedRequestIDGenerator(proxy.newRequestID)

	if stream != nil {
		stream.upstream = &usageCapturingSender{
			inner:   stream.upstream,
			maxBody: defaultUsageBodyCaptureLimit,
		}
		stream.journal = &usageCapturingJournal{inner: journal, registry: registry}
		stream.newRequestID = observedRequestIDGenerator(stream.newRequestID)
		if proxy.streamHandler == nil {
			proxy.streamHandler = stream
		}
	}

	return &ObservedGateway{
		inner:     handler,
		registry:  registry,
		requestID: outerRequestID,
		capture:   observability.DefaultCaptureRing(),
		journal:   journal,
	}, nil
}

// ServeHTTP 为请求注入 usage 上下文，再交给原 Handler。
func (g *ObservedGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g == nil || g.inner == nil {
		writeUnavailable(w)
		return
	}
	if r == nil || r.URL == nil {
		g.inner.ServeHTTP(w, r)
		return
	}

	// 优先复用 server 中间件生成的请求 ID；直接调用 ObservedGateway 时
	// 回退到注入的生成器或内部随机 ID。
	requestID := strings.TrimSpace(requestid.From(r.Context()))
	if !validRequestID(requestID) && g.requestID != nil {
		requestID = strings.TrimSpace(g.requestID(r))
	}
	if !validRequestID(requestID) || g.registry.exists(requestID) {
		requestID = generateRequestID()
	}

	usage := &requestUsage{requestID: requestID}
	g.registry.add(usage)
	defer g.registry.remove(requestID)

	startedAt := time.Now()
	recorder := &captureResponseWriter{ResponseWriter: w}

	// 诊断环只保存请求/响应字节，不保存 header；默认情况下不写 SQLite。
	// 请求体在路由/代理读取时 tee，响应体在写出时 tee；流式 SSE 按写出
	// 顺序原样保存。session 在 handler 返回后提交，因此 request_logs 中的
	// request_id 与诊断环键一致。
	if session := g.capture.NewSession(requestID); session != nil {
		if r.Body != nil {
			r.Body = session.TeeRequest(r.Body)
		}
		recorder.buffer = session.ResponseBuffer()
		defer session.Commit()
	}

	ctx := context.WithValue(r.Context(), requestUsageContextKey{}, usage)
	g.inner.ServeHTTP(recorder, r.WithContext(ctx))

	// 路由层拒绝（鉴权、方法、模型不可用、forwarder 未接）不会进入
	// Proxy/StreamProxy，因此不会写 request_logs。这里补一条最小失败日志，
	// 保证每次失败都有 request_id 可追踪；成功响应仍只由代理层记录。
	g.logRejectedRequest(r, usage, recorder.statusCode(), startedAt)
}

// logRejectedRequest 为未进入代理层的 4xx/5xx 响应补写请求日志。
func (g *ObservedGateway) logRejectedRequest(
	r *http.Request,
	usage *requestUsage,
	status int,
	startedAt time.Time,
) {
	if g == nil || g.journal == nil || usage == nil || usage.wasLogged() || status < 400 {
		return
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	protocol, endpoint := preflightRoute(path)
	httpStatus := status
	log := observability.RequestLog{
		RequestID:  usage.requestID,
		Protocol:   protocol,
		Endpoint:   endpoint,
		Status:     observability.StatusError,
		HTTPStatus: &httpStatus,
		ErrorCode:  preflightErrorCode(status, path),
		StartedAt:  startedAt,
	}
	if status >= 500 {
		log.ErrorMessage = "Internal server error."
	} else {
		log.ErrorMessage = "Request rejected before upstream."
	}
	// 客户端可能已断开，但日志仍应落库。
	_ = g.journal.WriteRequest(context.WithoutCancel(r.Context()), log)
}

func preflightRoute(path string) (protocol, endpoint string) {
	switch path {
	case PathResponses, PathCodexResponses:
		return ProtocolOpenAIResponses, path
	case PathChatCompletions:
		return ProtocolOpenAIChat, path
	default:
		return "", path
	}
}

func preflightErrorCode(status int, path string) string {
	switch status {
	case http.StatusUnauthorized:
		return errorCodeInvalidAPIKey
	case http.StatusNotFound:
		if path == PathModels || path == PathCodexModels ||
			strings.HasPrefix(path, ModelPathPrefix) {
			return errorCodeModelNotAvailable
		}
		return errorCodeNotFound
	case http.StatusMethodNotAllowed:
		return errorCodeMethodNotAllowed
	case http.StatusRequestEntityTooLarge:
		return errorCodeBodyTooLarge
	case http.StatusServiceUnavailable:
		return errorCodeUnavailable
	case http.StatusInternalServerError:
		return errorCodeInternal
	default:
		return errorCodeInvalidRequest
	}
}

// captureResponseWriter 把实际写出的响应字节 tee 到诊断环。
//
// 不改变状态码、header 或 Flush 语义；Unwrap 让 http.ResponseController
// 能访问底层 ResponseWriter。
type captureResponseWriter struct {
	http.ResponseWriter
	buffer *observability.CaptureBuffer
	status int
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	if n > 0 && w.buffer != nil {
		_, _ = w.buffer.Write(p[:n])
	}
	return n, err
}

func (w *captureResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *captureResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *captureResponseWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *captureResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// observedRequestIDGenerator 让原代理使用 ObservedGateway 生成的 request_id。
//
// 直接调用 Proxy（未经 ObservedGateway）时没有上下文值，回退到原生成器，
// 保持既有测试/嵌入用法不变。
func observedRequestIDGenerator(fallback func(*http.Request) string) func(*http.Request) string {
	return func(r *http.Request) string {
		if usage := requestUsageFromContext(r.Context()); usage != nil {
			return usage.requestID
		}
		if fallback != nil {
			return fallback(r)
		}
		return ""
	}
}

// usageCapturingSender 包装原生上游发送器，tee 读取 2xx 响应 body。
//
// 非流式 body 在 EOF/Close 时做一次 JSON usage 解析；流式 body 把每个
// 读取字节块交给 SSE usage 观察器。包装不改变 Read/Close 的返回值，
// 也不预读或截断转发。
type usageCapturingSender struct {
	inner   UpstreamSender
	maxBody int
}

func (s *usageCapturingSender) Send(
	ctx context.Context,
	target upstream.Target,
	req upstream.Request,
) (*http.Response, error) {
	usage := requestUsageFromContext(ctx)
	if usage != nil {
		// 每次渠道尝试都从空 usage 开始：失败尝试的 usage 不进入最终日志。
		usage.reset()
	}

	response, err := s.inner.Send(ctx, target, req)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	if usage == nil {
		return response, nil
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response, nil
	}

	// 用渠道协议决定 usage 解析形状；入站协议与渠道协议在 MVP 中必须
	// 一致，所以这里不会发生 Responses/Chat 错配。
	if req.Stream {
		observer := newUsageObservation(target.Protocol)
		if observer == nil {
			return response, nil
		}
		response.Body = &usageStreamBody{
			ReadCloser: response.Body,
			observer:   observer,
			usage:      usage,
		}
		return response, nil
	}

	response.Body = &usageBufferBody{
		ReadCloser: response.Body,
		usage:      usage,
		chat:       target.Protocol == ProtocolOpenAIChat,
		limit:      s.maxBody,
	}
	return response, nil
}

// usageCapturingJournal 在最终请求日志落库前补齐 usage。
//
// attempt 写入原样透传；request 只拦截一次（原代理保证一次请求只写一条），
// 补齐后仍由调用方提供的仓储执行唯一一次 INSERT。
type usageCapturingJournal struct {
	inner    AttemptJournal
	registry *usageRegistry
}

func (j *usageCapturingJournal) WriteAttempt(ctx context.Context, attempt observability.RequestAttempt) error {
	return j.inner.WriteAttempt(ctx, attempt)
}

func (j *usageCapturingJournal) WriteRequest(ctx context.Context, log observability.RequestLog) error {
	if usage := j.registry.get(log.RequestID); usage != nil {
		usage.markLogged()
		result := usage.snapshot()
		if result.Found() {
			if log.InputTokens == nil {
				log.InputTokens = result.Usage.InputTokens
			}
			if log.OutputTokens == nil {
				log.OutputTokens = result.Usage.OutputTokens
			}
			if log.TotalTokens == nil {
				log.TotalTokens = result.Usage.TotalTokens
			}
			if log.UsageSource == "" {
				log.UsageSource = result.Source
			}
		}
	}
	return j.inner.WriteRequest(ctx, log)
}

// usageStreamBody 在不改变转发字节的前提下观察 SSE。
type usageStreamBody struct {
	io.ReadCloser

	mu       sync.Mutex
	observer *observability.StreamUsageObserver
	usage    *requestUsage
	finished bool
}

func (b *usageStreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		b.observer.Write(p[:n])
		b.mu.Unlock()
	}
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *usageStreamBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *usageStreamBody) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return
	}
	b.finished = true
	b.usage.set(b.observer.Result())
}

// usageBufferBody 为非流式 JSON 保留有限字节，在结束后解析 usage。
type usageBufferBody struct {
	io.ReadCloser

	usage *requestUsage
	chat  bool
	limit int

	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
	done      bool
}

func (b *usageBufferBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.capture(p[:n])
	}
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *usageBufferBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *usageBufferBody) capture(chunk []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done || b.truncated {
		return
	}
	limit := b.limit
	if limit <= 0 {
		limit = defaultUsageBodyCaptureLimit
	}
	remaining := limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		b.buf.Reset()
		return
	}
	if len(chunk) > remaining {
		_, _ = b.buf.Write(chunk[:remaining])
		b.truncated = true
		b.buf.Reset()
		return
	}
	_, _ = b.buf.Write(chunk)
}

func (b *usageBufferBody) finish() {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	if b.truncated {
		b.buf.Reset()
		b.mu.Unlock()
		return
	}
	data := b.buf.Bytes()
	b.buf = bytes.Buffer{}
	b.mu.Unlock()

	var result observability.UsageResult
	if b.chat {
		result = observability.ExtractChatUsage(data)
	} else {
		result = observability.ExtractResponsesUsage(data)
	}
	b.usage.set(result)
}

func newUsageObservation(protocol string) *observability.StreamUsageObserver {
	switch protocol {
	case ProtocolOpenAIChat:
		return observability.NewChatStreamUsageObserver()
	case ProtocolOpenAIResponses:
		return observability.NewResponsesStreamUsageObserver()
	default:
		return nil
	}
}

var (
	_ UpstreamSender = (*usageCapturingSender)(nil)
	_ AttemptJournal = (*usageCapturingJournal)(nil)
)
