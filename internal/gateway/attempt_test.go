package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

func TestClassifyUpstreamStatus(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		wantStatus  int
		wantCode    string
		wantRetry   bool
		wantMessage string
	}{
		{
			name:        "400 不重试",
			status:      http.StatusBadRequest,
			wantStatus:  http.StatusBadRequest,
			wantCode:    errorCodeUpstreamBadRequest,
			wantRetry:   false,
			wantMessage: "Upstream rejected the request.",
		},
		{
			name:       "401 鉴权失败并重试",
			status:     http.StatusUnauthorized,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamAuthFailed,
			wantRetry:  true,
		},
		{
			name:       "403 鉴权失败并重试",
			status:     http.StatusForbidden,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamAuthFailed,
			wantRetry:  true,
		},
		{
			name:       "404 重试",
			status:     http.StatusNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   errorCodeUpstreamNotFound,
			wantRetry:  true,
		},
		{
			name:       "408 归一化为 504",
			status:     http.StatusRequestTimeout,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   errorCodeUpstreamTimeout,
			wantRetry:  true,
		},
		{
			name:       "413 不重试",
			status:     http.StatusRequestEntityTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   errorCodeBodyTooLarge,
			wantRetry:  false,
		},
		{
			name:       "415 不重试",
			status:     http.StatusUnsupportedMediaType,
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   errorCodeUnsupportedMediaType,
			wantRetry:  false,
		},
		{
			name:       "422 不重试",
			status:     http.StatusUnprocessableEntity,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   errorCodeUnprocessableEntity,
			wantRetry:  false,
		},
		{
			name:       "429 限流并重试",
			status:     http.StatusTooManyRequests,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   errorCodeUpstreamRateLimited,
			wantRetry:  true,
		},
		{
			name:       "500 归一化为 502",
			status:     http.StatusInternalServerError,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamError,
			wantRetry:  true,
		},
		{
			name:       "503 归一化为 503",
			status:     http.StatusServiceUnavailable,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   errorCodeUpstreamUnavailable,
			wantRetry:  true,
		},
		{
			name:       "529 视为过载并重试",
			status:     529,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   errorCodeUpstreamUnavailable,
			wantRetry:  true,
		},
		{
			name:       "418 保留状态但不重试",
			status:     http.StatusTeapot,
			wantStatus: http.StatusTeapot,
			wantCode:   errorCodeUpstreamClientError,
			wantRetry:  false,
		},
		{
			name:       "非预期 3xx 归一化为 502",
			status:     http.StatusNotModified,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamError,
			wantRetry:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUpstreamStatus(tt.status)
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %d, want %d", got.Status, tt.wantStatus)
			}
			if got.Code != tt.wantCode {
				t.Fatalf("Code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.Retry != tt.wantRetry {
				t.Fatalf("Retry = %v, want %v", got.Retry, tt.wantRetry)
			}
			if strings.TrimSpace(got.Message) == "" {
				t.Fatal("Message 不能为空")
			}
			if tt.wantMessage != "" && got.Message != tt.wantMessage {
				t.Fatalf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if strings.ContainsAny(got.Message, "\r\n") {
				t.Fatalf("Message 不能包含换行：%q", got.Message)
			}
		})
	}
}

func TestClassifyUpstreamError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantRetry  bool
		canceled   bool
	}{
		{
			name:       "客户端取消",
			err:        context.Canceled,
			wantStatus: httpStatusClientClosedRequest,
			wantCode:   errorCodeClientCanceled,
			canceled:   true,
		},
		{
			name:       "upstream 取消",
			err:        upstream.ErrCanceled,
			wantStatus: httpStatusClientClosedRequest,
			wantCode:   errorCodeClientCanceled,
			canceled:   true,
		},
		{
			name:       "context 超时",
			err:        context.DeadlineExceeded,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   errorCodeUpstreamTimeout,
			wantRetry:  true,
		},
		{
			name:       "upstream 超时",
			err:        upstream.ErrTimeout,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   errorCodeUpstreamTimeout,
			wantRetry:  true,
		},
		{
			name:       "传输错误",
			err:        upstream.ErrTransport,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamUnreachable,
			wantRetry:  true,
		},
		{
			name:       "跨主机重定向",
			err:        upstream.ErrCrossHostRedirect,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamUnreachable,
			wantRetry:  true,
		},
		{
			name:       "base URL 非法",
			err:        upstream.ErrInvalidBaseURL,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamConfig,
			wantRetry:  false,
		},
		{
			name:       "协议不支持",
			err:        upstream.ErrUnsupportedProtocol,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamConfig,
			wantRetry:  false,
		},
		{
			name:       "请求非法",
			err:        upstream.ErrInvalidRequest,
			wantStatus: http.StatusBadRequest,
			wantCode:   errorCodeInvalidRequest,
			wantRetry:  false,
		},
		{
			name:       "body 读取中断按网络错误重试",
			err:        io.ErrUnexpectedEOF,
			wantStatus: http.StatusBadGateway,
			wantCode:   errorCodeUpstreamUnreachable,
			wantRetry:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUpstreamError(tt.err)
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %d, want %d", got.Status, tt.wantStatus)
			}
			if got.Code != tt.wantCode {
				t.Fatalf("Code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.Retry != tt.wantRetry {
				t.Fatalf("Retry = %v, want %v", got.Retry, tt.wantRetry)
			}
			if got.Canceled != tt.canceled {
				t.Fatalf("Canceled = %v, want %v", got.Canceled, tt.canceled)
			}
		})
	}
}

func TestSchedulingFailure(t *testing.T) {
	notAvailable := schedulingFailure(routing.ErrNoCandidates)
	if notAvailable.Status != http.StatusNotFound ||
		notAvailable.Code != errorCodeModelNotAvailable {
		t.Fatalf("无候选映射 = %+v", notAvailable)
	}

	capacity := schedulingFailure(routing.ErrChannelCapacity)
	if capacity.Status != http.StatusTooManyRequests ||
		capacity.Code != errorCodeChannelCapacity ||
		capacity.RetryAfter != "1" {
		t.Fatalf("容量耗尽映射 = %+v", capacity)
	}

	internal := schedulingFailure(errors.New("sqlite: boom"))
	if internal.Status != http.StatusInternalServerError ||
		internal.Code != errorCodeInternal ||
		strings.Contains(internal.Message, "sqlite") {
		t.Fatalf("内部错误泄露细节 = %+v", internal)
	}
}

func TestNewAttemptRecord(t *testing.T) {
	candidate := routing.Candidate{
		Route: store.ModelRoute{
			ID:            9,
			ChannelID:     7,
			UpstreamModel: "gpt-upstream",
		},
		Channel: store.Channel{
			ID:   7,
			Name: "channel-a",
		},
	}
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	finished := started.Add(1500 * time.Millisecond)
	status := http.StatusServiceUnavailable
	failure := &proxyFailure{
		Code:    errorCodeUpstreamUnavailable,
		Message: "Upstream service is unavailable.",
	}

	record := newAttemptRecord("req-1", 2, candidate,
		observability.StatusError, &status, failure, false, started, finished)

	if record.RequestID != "req-1" || record.AttemptIndex != 2 {
		t.Fatalf("请求 ID/序号错误：%+v", record)
	}
	if record.ChannelID == nil || *record.ChannelID != 7 {
		t.Fatalf("ChannelID = %v, want 7", record.ChannelID)
	}
	if record.ChannelName != "channel-a" || record.UpstreamModel != "gpt-upstream" {
		t.Fatalf("渠道快照错误：%+v", record)
	}
	if record.Status != observability.StatusError {
		t.Fatalf("Status = %q", record.Status)
	}
	if record.HTTPStatus == nil || *record.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("HTTPStatus = %v", record.HTTPStatus)
	}
	if record.ErrorCode != errorCodeUpstreamUnavailable ||
		record.ErrorMessage != failure.Message {
		t.Fatalf("错误字段错误：%+v", record)
	}
	if record.LatencyMS != 1500 {
		t.Fatalf("LatencyMS = %d, want 1500", record.LatencyMS)
	}
	if record.SemanticCommitted {
		t.Fatal("失败 attempt 不能标记 semantic_committed")
	}
	if record.FinishedAt == nil || !record.FinishedAt.Equal(finished) {
		t.Fatalf("FinishedAt = %v", record.FinishedAt)
	}
}

func TestRequestLogFromAttempts(t *testing.T) {
	info := RequestInfo{
		Protocol: ProtocolOpenAIResponses,
		Endpoint: EndpointResponses,
		Model:    "gpt-client",
	}
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	finished := started.Add(250 * time.Millisecond)
	attempts := []observability.RequestAttempt{
		{
			AttemptIndex: 1,
			ChannelID:    testInt64(11),
			ChannelName:  "channel-a",
			Status:       observability.StatusError,
		},
		{
			AttemptIndex: 2,
			ChannelID:    testInt64(12),
			ChannelName:  "channel-b",
			Status:       observability.StatusSuccess,
		},
	}
	status := http.StatusOK
	responseBytes := int64(128)

	log := requestLogFromAttempts("req-2", info, attempts, observability.StatusSuccess,
		&status, nil, true, &responseBytes, started, finished)

	if log.RequestID != "req-2" || log.Protocol != ProtocolOpenAIResponses ||
		log.Endpoint != string(EndpointResponses) || log.Model != "gpt-client" {
		t.Fatalf("请求元数据错误：%+v", log)
	}
	if log.ChannelID == nil || *log.ChannelID != 12 || log.ChannelName != "channel-b" {
		t.Fatalf("最终渠道快照错误：%+v", log)
	}
	if log.AttemptCount != 2 || log.RetryCount != 1 {
		t.Fatalf("attempt/retry = %d/%d", log.AttemptCount, log.RetryCount)
	}
	if log.Stream {
		t.Fatal("T20 非流式日志不能标记 stream")
	}
	if !log.SemanticCommitted {
		t.Fatal("成功请求应标记 semantic_committed")
	}
	if log.ResponseBytes == nil || *log.ResponseBytes != 128 {
		t.Fatalf("ResponseBytes = %v", log.ResponseBytes)
	}
	if log.ErrorCode != "" || log.ErrorMessage != "" {
		t.Fatalf("成功请求不应带错误：%+v", log)
	}

	failure := proxyFailure{
		Status:  http.StatusNotFound,
		Code:    errorCodeModelNotAvailable,
		Message: "The requested model does not exist or is not available.",
	}
	failed := requestLogFromAttempts("req-3", info, nil, observability.StatusError,
		&failure.Status, &failure, false, nil, started, finished)
	if failed.ChannelID != nil || failed.ChannelName != "" {
		t.Fatalf("无尝试时不应有渠道快照：%+v", failed)
	}
	if failed.AttemptCount != 0 || failed.RetryCount != 0 {
		t.Fatalf("attempt/retry = %d/%d", failed.AttemptCount, failed.RetryCount)
	}
	if failed.ErrorCode != errorCodeModelNotAvailable {
		t.Fatalf("ErrorCode = %q", failed.ErrorCode)
	}
}

func TestValidRequestID(t *testing.T) {
	valid := []string{"req-1", strings.Repeat("a", 128)}
	for _, id := range valid {
		if !validRequestID(id) {
			t.Fatalf("validRequestID(%q) = false", id)
		}
	}

	invalid := []string{"", strings.Repeat("a", 129), "req\x00bad", "req\nbad"}
	for _, id := range invalid {
		if validRequestID(id) {
			t.Fatalf("validRequestID(%q) = true", id)
		}
	}
}

func testInt64(value int64) *int64 {
	return &value
}
