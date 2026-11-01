package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-router/internal/routing"
)

const (
	maxErrorBodyBytes  = 16 << 10
	maxErrorFieldBytes = 128
	maxRetryAfter      = 5 * time.Minute
)

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 128 {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		d := deadline.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > maxRetryAfter {
			return maxRetryAfter
		}
		return d
	}
	return 0
}

func classifyHTTPFailure(response *http.Response, now time.Time) proxyFailure {
	failure := classifyUpstreamStatus(response.StatusCode)
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes+1))
	if err == nil && len(body) <= maxErrorBodyBytes {
		failure = classifyStructuredFailure(failure, body)
	}
	failure.Cooldown = parseRetryAfter(response.Header.Get("Retry-After"), now)
	if failure.Cooldown > 0 {
		failure.RetryAfter = strconv.FormatInt(int64((failure.Cooldown+time.Second-1)/time.Second), 10)
	}
	return failure
}

// 仅限长的已知协议码影响调度，自由错误文本不参与推断或回传。
func classifyStructuredFailure(fallback proxyFailure, body []byte) proxyFailure {
	if fallback.Scope == "" {
		fallback.Scope = fallback.routingFailure(fallback.Status).Scope
	}
	if len(body) == 0 || len(body) > maxErrorBodyBytes {
		return fallback
	}
	type detail struct {
		Type string `json:"type"`
		Code string `json:"code"`
	}
	var envelope struct {
		Type     string  `json:"type"`
		Code     string  `json:"code"`
		Error    *detail `json:"error"`
		Response *struct {
			Error *detail `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return fallback
	}
	value := detail{Type: envelope.Type, Code: envelope.Code}
	if envelope.Error != nil {
		value = *envelope.Error
	}
	if envelope.Response != nil && envelope.Response.Error != nil {
		value = *envelope.Response.Error
	}
	if len(value.Code) > maxErrorFieldBytes || len(value.Type) > maxErrorFieldBytes {
		return fallback
	}
	for _, code := range []string{value.Code, value.Type} {
		switch code {
		case "invalid_request_error", "context_length_exceeded", "invalid_parameter", "invalid_value", "unsupported_parameter", "content_policy_violation":
			f := classifyUpstreamStatus(400)
			f.Scope = routing.FailureScopeRequest
			f.FaultStatus = 400
			return f
		case "model_not_found", "model_not_available", "model_access_denied", "model_not_supported":
			f := classifyUpstreamStatus(404)
			f.Scope = routing.FailureScopeModel
			f.FaultStatus = 404
			return f
		case "invalid_api_key", "authentication_error", "invalid_authentication", "insufficient_permissions":
			f := classifyUpstreamStatus(401)
			f.Scope = routing.FailureScopeChannel
			f.FaultStatus = 401
			return f
		case "rate_limit_exceeded", "rate_limit_error", "insufficient_quota":
			f := classifyUpstreamStatus(429)
			f.Scope = routing.FailureScopeChannel
			f.FaultStatus = 429
			return f
		case "server_error", "internal_error":
			f := classifyUpstreamStatus(500)
			f.Scope = routing.FailureScopeChannel
			f.FaultStatus = 500
			return f
		case "overloaded_error":
			f := classifyUpstreamStatus(503)
			f.Scope = routing.FailureScopeChannel
			f.FaultStatus = 503
			return f
		}
	}
	return fallback
}
