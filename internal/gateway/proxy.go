// proxy.go 实现数据面的非流式请求编排（T20）：
//
//   - 通过 routing.Scheduler 取候选、抢占进程内并发槽位；
//   - 解密渠道上游 Key 后调用原生 adapter（upstream.Client）；
//   - 首内容写入客户端前失败时切换下一个渠道，最多 3 个不同渠道；
//   - 每次尝试写 request_attempts，最终写 request_logs；
//   - 按入站协议（当前只有 OpenAI/Responses 形状）返回固定错误 envelope。
//
// 本文件不直接访问 SQLite，也不做跨协议转换。流式请求由 T21 通过
// ProxyOptions.StreamHandler 接入；未注入时返回 503 gateway_unavailable，
// 避免把 SSE 当作普通 JSON 缓冲。
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/upstream"
)

const (
	// defaultLogWriteTimeout 限制请求结束后写 attempt/log 的时间，避免
	// SQLite 短时繁忙把响应拖住。
	defaultLogWriteTimeout = 5 * time.Second
)

// CandidateScheduler 是失败切换需要的调度能力；*routing.Scheduler 满足。
//
// 每次尝试都重新查询候选（排除已失败渠道），以便看到管理员刚修改的
// 启停与优先级，并让熔断状态即时生效。
type CandidateScheduler interface {
	AcquireExcluding(
		ctx context.Context,
		clientModel, protocol string,
		exclude map[int64]struct{},
	) (routing.Candidate, *routing.Slot, error)
	RecordSuccess(channelID int64)
	RecordFailure(channelID int64, status int, message string) bool
}

// UpstreamSender 是原生上游发送能力；*upstream.Client 满足。
type UpstreamSender interface {
	Send(ctx context.Context, target upstream.Target, req upstream.Request) (*http.Response, error)
}

// AttemptJournal 是 T20 需要的日志写入能力；*observability.Store 满足。
type AttemptJournal interface {
	WriteAttempt(ctx context.Context, attempt observability.RequestAttempt) error
	WriteRequest(ctx context.Context, log observability.RequestLog) error
}

var (
	_ CandidateScheduler = (*routing.Scheduler)(nil)
	_ UpstreamSender     = (*upstream.Client)(nil)
	_ AttemptJournal     = (*observability.Store)(nil)
	_ http.Handler       = (*Proxy)(nil)
)

// ProxyOptions 是 Proxy 的构造参数。
//
// Scheduler/Upstream/Journal/DecryptKey 必须非 nil。DecryptKey 只接收
// channel.KeyCiphertext，应由调用方绑定 secret.Envelope 与
// secret.PurposeChannelKey；Proxy 本身不接触主密钥。
type ProxyOptions struct {
	Scheduler  CandidateScheduler
	Upstream   UpstreamSender
	Journal    AttemptJournal
	DecryptKey func(ciphertext string) (string, error)

	// StreamHandler 接收 stream=true 的请求（T21 接入）。nil 时返回 503，
	// 绝不把 SSE 当作非流式 JSON 处理。
	StreamHandler http.Handler

	Logger *slog.Logger
	// Clock 为零时使用 time.Now。
	Clock func() time.Time
	// RequestID 为零时生成 16 字节随机十六进制 ID；非法返回值会被替换。
	RequestID func(*http.Request) string
}

// Proxy 是非流式数据面代理。构造后字段不可变，可并发使用。
type Proxy struct {
	scheduler     CandidateScheduler
	upstream      UpstreamSender
	journal       AttemptJournal
	decryptKey    func(string) (string, error)
	streamHandler http.Handler
	logger        *slog.Logger
	now           func() time.Time
	newRequestID  func(*http.Request) string
}

// NewProxy 构造非流式代理。缺少任何必要依赖时返回错误，错误不包含 Key。
func NewProxy(opts ProxyOptions) (*Proxy, error) {
	if opts.Scheduler == nil {
		return nil, errors.New("gateway: 代理缺少调度器")
	}
	if opts.Upstream == nil {
		return nil, errors.New("gateway: 代理缺少上游客户端")
	}
	if opts.Journal == nil {
		return nil, errors.New("gateway: 代理缺少日志仓储")
	}
	if opts.DecryptKey == nil {
		return nil, errors.New("gateway: 代理缺少渠道 Key 解密器")
	}

	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Proxy{
		scheduler:     opts.Scheduler,
		upstream:      opts.Upstream,
		journal:       opts.Journal,
		decryptKey:    opts.DecryptKey,
		streamHandler: opts.StreamHandler,
		logger:        logger,
		now:           now,
		newRequestID:  opts.RequestID,
	}, nil
}

// upstreamResponse 是非流式成功响应的内存快照。
type upstreamResponse struct {
	status int
	header http.Header
	body   []byte
}

// attemptOutcome 是一次渠道尝试的结果。
//
// failure 为 nil 表示上游返回 2xx 且 body 已完整读取；此时 response 非 nil。
// failure 非 nil 时 response 为 nil，record 已带归一化错误。
type attemptOutcome struct {
	record   observability.RequestAttempt
	failure  *proxyFailure
	response *upstreamResponse
}

// ServeHTTP 实现非流式转发入口。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.scheduler == nil || p.upstream == nil || p.journal == nil {
		writeUnavailable(w)
		return
	}
	if r == nil || r.URL == nil {
		writeNotFound(w)
		return
	}

	info, ok := RequestInfoFromContext(r.Context())
	if !ok {
		// Router 未写入协议/模型时不能猜测；返回固定 500，且不写日志。
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

	if requestWantsStream(body) {
		if p.streamHandler == nil {
			writeUnavailable(w)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		p.streamHandler.ServeHTTP(w, r)
		return
	}

	p.forward(w, r, info, body)
}

// forward 执行非流式失败切换。
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, info RequestInfo, body []byte) {
	startedAt := p.now()
	requestID := p.requestID(r)
	attempts := make([]observability.RequestAttempt, 0, maxUpstreamAttempts)
	excluded := make(map[int64]struct{}, maxUpstreamAttempts)

	var lastFailure *proxyFailure
	for len(attempts) < maxUpstreamAttempts {
		if err := r.Context().Err(); err != nil {
			failure := classifyUpstreamError(err)
			p.finishFailure(w, requestID, info, attempts, startedAt, &failure, observability.StatusCancelled)
			return
		}

		candidate, slot, err := p.scheduler.AcquireExcluding(
			r.Context(), info.Model, info.Protocol, excluded)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				failure := classifyUpstreamError(err)
				p.finishFailure(w, requestID, info, attempts, startedAt, &failure, observability.StatusCancelled)
				return
			}
			if len(attempts) > 0 {
				// 还有尝试历史时，最终错误沿用最后一次上游失败，而不是把
				// “没有更多候选”误报成 model_not_available。
				break
			}
			failure := schedulingFailure(err)
			p.finishFailure(w, requestID, info, attempts, startedAt, &failure, observability.StatusError)
			return
		}

		attemptIndex := len(attempts) + 1
		outcome := p.runAttempt(
			r.Context(), requestID, attemptIndex, candidate, slot, body, r.Header)

		if outcome.failure == nil && outcome.response != nil {
			upstreamStatus := outcome.response.status
			writeErr := writeUpstreamResponse(w, outcome.response)
			committed := writeErr == nil

			// 非流式请求只有完整写出后才算语义提交；写失败时不再重试。
			outcome.record.SemanticCommitted = committed
			attempts = append(attempts, outcome.record)
			p.writeAttempt(outcome.record)

			status := observability.StatusSuccess
			var finalFailure *proxyFailure
			var responseBytes *int64
			httpStatus := upstreamStatus
			semanticCommitted := committed
			if committed {
				size := int64(len(outcome.response.body))
				responseBytes = &size
			} else {
				failure := classifyUpstreamError(r.Context().Err())
				if r.Context().Err() == nil {
					failure = proxyFailure{
						Status:   httpStatusClientClosedRequest,
						Code:     errorCodeClientCanceled,
						Type:     errorTypeInvalidRequest,
						Message:  "Request was canceled by the client.",
						Canceled: true,
					}
				}
				status = observability.StatusCancelled
				httpStatus = failure.Status
				finalFailure = &failure
				semanticCommitted = false
			}

			p.recordRequest(requestID, info, attempts, status, &httpStatus,
				finalFailure, semanticCommitted, responseBytes, startedAt)
			return
		}

		if outcome.failure == nil {
			// 正常情况下不会发生；按内部错误收尾，避免空指针。
			failure := schedulingFailure(errors.New("gateway: 代理返回空结果"))
			p.finishFailure(w, requestID, info, attempts, startedAt, &failure, observability.StatusError)
			return
		}

		attempts = append(attempts, outcome.record)
		p.writeAttempt(outcome.record)
		lastFailure = outcome.failure
		excluded[candidate.Channel.ID] = struct{}{}

		if outcome.failure.Canceled {
			p.finishFailure(w, requestID, info, attempts, startedAt, outcome.failure, observability.StatusCancelled)
			return
		}
		if !outcome.failure.Retry {
			break
		}
	}

	if lastFailure == nil {
		failure := schedulingFailure(routing.ErrNoCandidates)
		lastFailure = &failure
	}
	p.finishFailure(w, requestID, info, attempts, startedAt, lastFailure, observability.StatusError)
}

// runAttempt 执行一次完整的渠道尝试，并在返回前释放槽位。
//
// 返回前已完成：Key 解密、上游请求发送、2xx body 全量读取或错误分类、
// 熔断/健康状态记录。调用方负责写 attempt/request 日志与客户端响应。
func (p *Proxy) runAttempt(
	ctx context.Context,
	requestID string,
	index int,
	candidate routing.Candidate,
	slot *routing.Slot,
	body []byte,
	headers http.Header,
) attemptOutcome {
	defer slot.Release()
	startedAt := p.now()

	key, err := p.decryptKey(candidate.Channel.KeyCiphertext)
	if err != nil || strings.TrimSpace(key) == "" {
		failure := upstreamConfigurationFailure()
		return attemptOutcome{
			record: newAttemptRecord(requestID, index, candidate,
				observability.StatusError, nil, &failure, false, startedAt, p.now()),
			failure: &failure,
		}
	}

	response, err := p.upstream.Send(ctx, upstream.Target{
		ChannelID:   candidate.Channel.ID,
		ChannelName: candidate.Channel.Name,
		BaseURL:     candidate.Channel.BaseURL,
		Protocol:    candidate.Channel.Protocol,
		APIKey:      key,
	}, upstream.Request{
		UpstreamModel: candidate.Route.UpstreamModel,
		Body:          body,
		Headers:       headers,
		Stream:        false,
	})
	if err != nil {
		failure := classifyUpstreamError(err)
		if !failure.Canceled {
			// 传输层错误没有上游状态码；不触发 5xx 熔断窗口。
			p.scheduler.RecordFailure(candidate.Channel.ID, 0, failure.Message)
		}
		return attemptOutcome{
			record: newAttemptRecord(requestID, index, candidate,
				attemptStatus(failure), nil, &failure, false, startedAt, p.now()),
			failure: &failure,
		}
	}
	if response == nil {
		failure := proxyFailure{
			Status:  http.StatusInternalServerError,
			Code:    errorCodeInternal,
			Type:    errorTypeServer,
			Message: "Internal server error.",
		}
		return attemptOutcome{
			record: newAttemptRecord(requestID, index, candidate,
				observability.StatusError, nil, &failure, false, startedAt, p.now()),
			failure: &failure,
		}
	}
	if response.Body == nil {
		response.Body = http.NoBody
	}
	// upstream.Client 已过滤一次；这里再次过滤，保证任何 UpstreamSender
	// 实现（包括测试 fake）都不会把 Set-Cookie 或 hop-by-hop 头带给客户端。
	response.Header = upstream.FilterResponseHeaders(response.Header)

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		data, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			failure := classifyUpstreamError(readErr)
			if !failure.Canceled {
				p.scheduler.RecordFailure(candidate.Channel.ID, 0, failure.Message)
			}
			return attemptOutcome{
				record: newAttemptRecord(requestID, index, candidate,
					attemptStatus(failure), nil, &failure, false, startedAt, p.now()),
				failure: &failure,
			}
		}

		p.scheduler.RecordSuccess(candidate.Channel.ID)
		statusCode := response.StatusCode
		record := newAttemptRecord(requestID, index, candidate,
			observability.StatusSuccess, &statusCode, nil, false, startedAt, p.now())
		return attemptOutcome{
			record: record,
			response: &upstreamResponse{
				status: statusCode,
				header: response.Header.Clone(),
				body:   data,
			},
		}
	}

	// 非 2xx：不读取上游 body，避免上游错误页拖慢切换或把内容带进日志。
	_ = response.Body.Close()
	failure := classifyUpstreamStatus(response.StatusCode)
	if retryAfter := strings.TrimSpace(response.Header.Get("Retry-After")); retryAfter != "" {
		failure.RetryAfter = retryAfter
	}
	p.scheduler.RecordFailure(candidate.Channel.ID, response.StatusCode, failure.Message)
	statusCode := response.StatusCode
	return attemptOutcome{
		record: newAttemptRecord(requestID, index, candidate,
			observability.StatusError, &statusCode, &failure, false, startedAt, p.now()),
		failure: &failure,
	}
}

// attemptStatus 把失败结果映射为 attempt.status。
func attemptStatus(failure proxyFailure) string {
	if failure.Canceled {
		return observability.StatusCancelled
	}
	return observability.StatusError
}

// finishFailure 写出归一化错误并记录最终请求日志。
func (p *Proxy) finishFailure(
	w http.ResponseWriter,
	requestID string,
	info RequestInfo,
	attempts []observability.RequestAttempt,
	startedAt time.Time,
	failure *proxyFailure,
	status string,
) {
	if failure == nil {
		empty := schedulingFailure(errors.New("gateway: 代理缺少错误结果"))
		failure = &empty
	}
	writeFailure(w, *failure)
	p.recordRequest(requestID, info, attempts, status, &failure.Status,
		failure, false, nil, startedAt)
}

// recordRequest 写最终 request_logs。日志失败不影响已写出的客户端响应。
func (p *Proxy) recordRequest(
	requestID string,
	info RequestInfo,
	attempts []observability.RequestAttempt,
	status string,
	httpStatus *int,
	failure *proxyFailure,
	semanticCommitted bool,
	responseBytes *int64,
	startedAt time.Time,
) {
	finishedAt := p.now()
	log := requestLogFromAttempts(requestID, info, attempts, status, httpStatus,
		failure, semanticCommitted, responseBytes, startedAt, finishedAt)

	ctx, cancel := context.WithTimeout(context.Background(), defaultLogWriteTimeout)
	defer cancel()
	if err := p.journal.WriteRequest(ctx, log); err != nil {
		// 不记录底层错误文本：SQLite 错误可能包含查询参数或行值。
		p.logger.Error("写入请求日志失败", "request_id", log.RequestID)
	}
}

// writeAttempt 写一条 attempt。写入失败不影响请求继续。
func (p *Proxy) writeAttempt(attempt observability.RequestAttempt) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultLogWriteTimeout)
	defer cancel()
	if err := p.journal.WriteAttempt(ctx, attempt); err != nil {
		p.logger.Error("写入渠道尝试失败",
			"request_id", attempt.RequestID,
			"attempt_index", attempt.AttemptIndex,
		)
	}
}

// writeFailure 写出固定错误 envelope；Retry-After 只用于容量/上游限流。
func writeFailure(w http.ResponseWriter, failure proxyFailure) {
	if failure.RetryAfter != "" {
		w.Header().Set("Retry-After", failure.RetryAfter)
	}
	writeAPIError(w, failure.Status, failure.apiError())
}

// writeUpstreamResponse 把上游成功响应写回客户端。
//
// response.header 已由 upstream.Client 过滤为安全白名单（Set-Cookie 与
// hop-by-hop 头不会出现）。
func writeUpstreamResponse(w http.ResponseWriter, response *upstreamResponse) error {
	if response == nil {
		return errors.New("gateway: 上游响应为空")
	}
	for name, values := range response.header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(response.status)
	if len(response.body) == 0 {
		return nil
	}
	_, err := w.Write(response.body)
	return err
}

// readProxyBody 读取并复位请求体，保证后续流式 handler 仍能看到原始字节。
func readProxyBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		r.Body = http.NoBody
		return []byte{}, nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// requestWantsStream 只解析 stream 字段；无效 JSON 交给后续错误路径。
func requestWantsStream(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Stream
}

// requestID 优先使用注入的生成器；返回值非法时回退内部随机 ID。
func (p *Proxy) requestID(r *http.Request) string {
	if p.newRequestID != nil {
		if id := strings.TrimSpace(p.newRequestID(r)); validRequestID(id) {
			return id
		}
	}
	return generateRequestID()
}

func generateRequestID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}
