package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/upstream"
)

const (
	// maxUpstreamAttempts 是单次请求最多尝试的不同渠道数的默认上限。
	//
	// 失败切换只按渠道计数：同一渠道失败后加入排除集合，不会再次尝试；
	// 达到上限后无论是否还有候选都返回最后一次归一化错误。
	maxUpstreamAttempts = 3

	// maxNonStreamResponseBytes 是非流式成功响应允许读入内存的上限。
	// 超限按不可重试的上游错误返回，避免单个响应耗尽进程内存。
	maxNonStreamResponseBytes int64 = 32 << 20

	// 上游错误归一化后的稳定 code。客户端只看到这些固定值，不看到上游
	// 响应 body、状态说明或 URL。
	errorCodeUpstreamAuthFailed   = "upstream_auth_failed"
	errorCodeUpstreamRateLimited  = "upstream_rate_limited"
	errorCodeUpstreamTimeout      = "upstream_timeout"
	errorCodeUpstreamUnavailable  = "upstream_unavailable"
	errorCodeUpstreamError        = "upstream_error"
	errorCodeUpstreamUnreachable  = "upstream_unreachable"
	errorCodeUpstreamConfig       = "upstream_configuration_error"
	errorCodeUpstreamNotFound     = "upstream_not_found"
	errorCodeUpstreamBadRequest   = "upstream_bad_request"
	errorCodeUpstreamClientError  = "upstream_client_error"
	errorCodeUnsupportedMediaType = "unsupported_media_type"
	errorCodeUnprocessableEntity  = "unprocessable_entity"
	errorCodeChannelCapacity      = "channel_capacity_exhausted"
	errorCodeClientCanceled       = "client_canceled"
	errorCodeInvalidRequest       = "invalid_request"
	errorTypeRateLimit            = "rate_limit_error"
	httpStatusClientClosedRequest = 499
)

// proxyFailure 是一次失败归一化后的客户端可见结果。
//
// Message 必须是固定文案，不得包含上游 body、Key、base_url 或查询串；
// Retry 只决定是否切换下一个渠道，不影响已经返回的 HTTP 状态。
type proxyFailure struct {
	Status      int
	Code        string
	Type        string
	Message     string
	Retry       bool
	RetryAfter  string
	Canceled    bool
	Scope       string
	FaultStatus int
	Cooldown    time.Duration
}

func (f proxyFailure) routingFailure(status int) routing.Failure {
	scope := f.Scope
	if scope == "" {
		scope = routing.FailureScopeChannel
		if status == http.StatusNotFound {
			scope = routing.FailureScopeModel
		}
		if status >= 400 && status < 500 && status != 401 && status != 403 && status != 404 && status != 408 && status != 429 {
			scope = routing.FailureScopeRequest
		}
	}
	if f.FaultStatus != 0 {
		status = f.FaultStatus
	}
	return routing.Failure{Scope: scope, Status: status, Message: f.Message, RetryAfter: f.Cooldown}
}

func (f proxyFailure) apiError() APIError {
	return APIError{
		Message: f.Message,
		Type:    f.Type,
		Code:    f.Code,
	}
}

// classifyUpstreamStatus 把上游 HTTP 状态码映射为客户端错误。
//
// 重试范围（见 T20 合同）：401/403/404/408/429/5xx/529；400/413/415/422
// 明确不重试；其余 4xx 视为客户端错误，不重试。映射后的状态码按
// R-017：401/403→502，429→429，408→504，503/529→503，其余 5xx→502。
func classifyUpstreamStatus(status int) proxyFailure {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamAuthFailed,
			Type:    errorTypeServer,
			Message: "Upstream authentication failed.",
			Retry:   true,
		}
	case status == http.StatusTooManyRequests:
		return proxyFailure{
			Status:  http.StatusTooManyRequests,
			Code:    errorCodeUpstreamRateLimited,
			Type:    errorTypeRateLimit,
			Message: "Upstream rate limit exceeded.",
			Retry:   true,
		}
	case status == http.StatusRequestTimeout:
		return proxyFailure{
			Status:  http.StatusGatewayTimeout,
			Code:    errorCodeUpstreamTimeout,
			Type:    errorTypeServer,
			Message: "Upstream request timed out.",
			Retry:   true,
		}
	case status == http.StatusNotFound:
		return proxyFailure{
			Status:  http.StatusNotFound,
			Code:    errorCodeUpstreamNotFound,
			Type:    errorTypeInvalidRequest,
			Message: "The upstream model or endpoint was not found.",
			Retry:   true,
		}
	case status >= 500 && status <= 599:
		if status == http.StatusServiceUnavailable || status == 529 {
			return proxyFailure{
				Status:  http.StatusServiceUnavailable,
				Code:    errorCodeUpstreamUnavailable,
				Type:    errorTypeServer,
				Message: "Upstream service is unavailable.",
				Retry:   true,
			}
		}
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamError,
			Type:    errorTypeServer,
			Message: "Upstream service returned an error.",
			Retry:   true,
		}
	case status == http.StatusBadRequest:
		return proxyFailure{
			Status:  http.StatusBadRequest,
			Code:    errorCodeUpstreamBadRequest,
			Type:    errorTypeInvalidRequest,
			Message: "Upstream rejected the request.",
			Retry:   false,
		}
	case status == http.StatusRequestEntityTooLarge:
		return proxyFailure{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    errorCodeBodyTooLarge,
			Type:    errorTypeInvalidRequest,
			Message: "Request body is too large.",
			Retry:   false,
		}
	case status == http.StatusUnsupportedMediaType:
		return proxyFailure{
			Status:  http.StatusUnsupportedMediaType,
			Code:    errorCodeUnsupportedMediaType,
			Type:    errorTypeInvalidRequest,
			Message: "Unsupported media type.",
			Retry:   false,
		}
	case status == http.StatusUnprocessableEntity:
		return proxyFailure{
			Status:  http.StatusUnprocessableEntity,
			Code:    errorCodeUnprocessableEntity,
			Type:    errorTypeInvalidRequest,
			Message: "Upstream could not process the request.",
			Retry:   false,
		}
	case status >= 400 && status <= 499:
		return proxyFailure{
			Status:  status,
			Code:    errorCodeUpstreamClientError,
			Type:    errorTypeInvalidRequest,
			Message: "Upstream rejected the request.",
			Retry:   false,
		}
	default:
		// 3xx（同 host 重定向已由 upstream 处理）与其他异常状态都视为
		// 上游协议错误，不重试。
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamError,
			Type:    errorTypeServer,
			Message: "Upstream returned an unexpected response.",
			Retry:   false,
		}
	}
}

// classifyUpstreamError 把 upstream.Send / body 读取错误映射为客户端错误。
//
// 分类顺序必须是“取消 → 超时 → 网络/重定向 → 本地配置 → 兜底”，否则
// 总时限超时会被 context.Canceled 掩盖，或本地配置错误被当成网络错误重试。
func classifyUpstreamError(err error) proxyFailure {
	switch {
	case err == nil:
		return proxyFailure{
			Status:  http.StatusInternalServerError,
			Code:    errorCodeInternal,
			Type:    errorTypeServer,
			Message: "Internal server error.",
		}
	case errors.Is(err, context.Canceled), errors.Is(err, upstream.ErrCanceled):
		return proxyFailure{
			Status:   httpStatusClientClosedRequest,
			Code:     errorCodeClientCanceled,
			Type:     errorTypeInvalidRequest,
			Message:  "Request was canceled by the client.",
			Canceled: true,
		}
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, upstream.ErrTimeout),
		errors.Is(err, upstream.ErrStreamIdleTimeout):
		return proxyFailure{
			Status:  http.StatusGatewayTimeout,
			Code:    errorCodeUpstreamTimeout,
			Type:    errorTypeServer,
			Message: "Upstream request timed out.",
			Retry:   true,
		}
	case errors.Is(err, upstream.ErrCrossHostRedirect),
		errors.Is(err, upstream.ErrTooManyRedirects),
		errors.Is(err, upstream.ErrTransport):
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamUnreachable,
			Type:    errorTypeServer,
			Message: "Upstream is unreachable.",
			Retry:   true,
		}
	case errors.Is(err, upstream.ErrInvalidBaseURL),
		errors.Is(err, upstream.ErrUnsupportedProtocol):
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamConfig,
			Type:    errorTypeServer,
			Message: "Upstream configuration is invalid.",
			Retry:   false,
		}
	case errors.Is(err, upstream.ErrInvalidRequest):
		return proxyFailure{
			Status:  http.StatusBadRequest,
			Code:    errorCodeInvalidRequest,
			Type:    errorTypeInvalidRequest,
			Message: "The upstream request is invalid.",
			Retry:   false,
		}
	default:
		// 响应 body 读取中断等未包装的传输错误按网络错误处理；未发送任何
		// 客户端字节，因此可以安全切换渠道。
		return proxyFailure{
			Status:  http.StatusBadGateway,
			Code:    errorCodeUpstreamUnreachable,
			Type:    errorTypeServer,
			Message: "Upstream is unreachable.",
			Retry:   true,
		}
	}
}

// schedulingFailure 把调度器的稳定错误映射为客户端错误。
//
// 无候选（协议不匹配/映射或渠道禁用）返回 404 model_not_available；全部
// 候选熔断返回 503 + Retry-After；全部候选满载返回 429 + Retry-After: 1；
// 其他仓储/内部错误只返回固定的 500，不泄露 SQL。
func schedulingFailure(err error) proxyFailure {
	switch {
	case errors.Is(err, routing.ErrNoCandidates):
		return proxyFailure{
			Status:  http.StatusNotFound,
			Code:    errorCodeModelNotAvailable,
			Type:    errorTypeInvalidRequest,
			Message: "The requested model does not exist or is not available.",
		}
	case errors.Is(err, routing.ErrAllCircuitsOpen):
		return proxyFailure{
			Status:     http.StatusServiceUnavailable,
			Code:       errorCodeUpstreamUnavailable,
			Type:       errorTypeServer,
			Message:    "All candidate channels are temporarily unavailable.",
			RetryAfter: "1",
		}
	case errors.Is(err, routing.ErrChannelCapacity):
		return proxyFailure{
			Status:     http.StatusTooManyRequests,
			Code:       errorCodeChannelCapacity,
			Type:       errorTypeRateLimit,
			Message:    "All candidate channels are at capacity.",
			RetryAfter: "1",
		}
	default:
		return proxyFailure{
			Status:  http.StatusInternalServerError,
			Code:    errorCodeInternal,
			Type:    errorTypeServer,
			Message: "Internal server error.",
		}
	}
}

// upstreamConfigurationFailure 表示渠道 Key 无法解密或为空。此时尚未调用
// 上游，错误文案只说明配置无效。
func upstreamConfigurationFailure() proxyFailure {
	return proxyFailure{
		Status:  http.StatusBadGateway,
		Code:    errorCodeUpstreamConfig,
		Type:    errorTypeServer,
		Message: "Upstream configuration is invalid.",
		Retry:   false,
	}
}

// newAttemptRecord 构造一条 request_attempts 记录。
//
// httpStatus 为 nil 表示没有拿到上游 HTTP 响应（网络错误/解密失败/取消）。
// 失败记录的 ErrorCode/ErrorMessage 只来自归一化结果，绝不写入上游 body。
func newAttemptRecord(
	requestID string,
	index int,
	candidate routing.Candidate,
	status string,
	httpStatus *int,
	failure *proxyFailure,
	semanticCommitted bool,
	startedAt, finishedAt time.Time,
) observability.RequestAttempt {
	channelID := candidate.Channel.ID
	record := observability.RequestAttempt{
		RequestID:         requestID,
		AttemptIndex:      index,
		ChannelID:         &channelID,
		ChannelName:       candidate.Channel.Name,
		UpstreamModel:     candidate.Route.UpstreamModel,
		Status:            status,
		HTTPStatus:        httpStatus,
		LatencyMS:         durationMillis(startedAt, finishedAt),
		SemanticCommitted: semanticCommitted,
		StartedAt:         startedAt.UTC(),
		FinishedAt:        utcTimePointer(finishedAt),
	}
	if failure != nil {
		record.ErrorCode = failure.Code
		record.ErrorMessage = failure.Message
	}
	return record
}

// requestLogFromAttempts 构造最终 request_logs 记录。
//
// 最终渠道快照取最后一次尝试；AttemptCount/RetryCount 与真实尝试次数一致。
// T20 只服务非流式请求，Stream 固定为 false；token/usage 由 T23 补齐。
func requestLogFromAttempts(
	requestID string,
	info RequestInfo,
	attempts []observability.RequestAttempt,
	status string,
	httpStatus *int,
	failure *proxyFailure,
	semanticCommitted bool,
	responseBytes *int64,
	startedAt, finishedAt time.Time,
) observability.RequestLog {
	log := observability.RequestLog{
		RequestID:         requestID,
		Protocol:          info.Protocol,
		Endpoint:          string(info.Endpoint),
		Model:             info.Model,
		Status:            status,
		HTTPStatus:        httpStatus,
		Stream:            false,
		AttemptCount:      len(attempts),
		RetryCount:        retryCount(len(attempts)),
		LatencyMS:         durationMillis(startedAt, finishedAt),
		SemanticCommitted: semanticCommitted,
		ResponseBytes:     responseBytes,
		StartedAt:         startedAt.UTC(),
		FinishedAt:        utcTimePointer(finishedAt),
	}
	if len(attempts) > 0 {
		last := attempts[len(attempts)-1]
		if last.ChannelID != nil {
			channelID := *last.ChannelID
			log.ChannelID = &channelID
		}
		log.ChannelName = last.ChannelName
	}
	if failure != nil {
		log.ErrorCode = failure.Code
		log.ErrorMessage = failure.Message
	}
	return log
}

func retryCount(attempts int) int {
	if attempts <= 1 {
		return 0
	}
	return attempts - 1
}

// normalizeMaxAttempts 把未配置/非法值收敛到默认上限，并拒绝超过
// maxUpstreamAttempts 的配置，避免单请求无限放大上游调用。
func normalizeMaxAttempts(attempts int) int {
	if attempts <= 0 {
		return maxUpstreamAttempts
	}
	if attempts > maxUpstreamAttempts {
		return maxUpstreamAttempts
	}
	return attempts
}

func durationMillis(start, end time.Time) int64 {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func utcTimePointer(at time.Time) *time.Time {
	utc := at.UTC()
	return &utc
}

// validRequestID 与 observability 的写入校验保持一致，供 T20 在注入的
// RequestID 生成器返回非法值时回退到内部生成值。
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	return strings.IndexFunc(id, unicode.IsControl) < 0
}
