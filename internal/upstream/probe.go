package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-router/internal/store"
)

const (
	// probeMaxOutputTokens 限制单次生成探针的上游消耗。
	probeMaxOutputTokens = 16
	probeDrainLimit      = 64 << 10

	HealthStatusHealthy      = "healthy"
	HealthStatusUnhealthy    = "unhealthy"
	HealthStatusInconclusive = "inconclusive"
)

// HealthRecorder 是探针写入渠道健康状态所需的最小能力。
type HealthRecorder interface {
	UpdateHealth(ctx context.Context, id int64, health store.ChannelHealth) error
}

var _ HealthRecorder = (*store.Channels)(nil)

// ProbeResult 是一次指定模型和协议的生成探针结果，不证明工具往返或任务质量。
type ProbeResult struct {
	// Status 是 healthy、unhealthy 或 inconclusive。
	Status string
	// HTTPStatus 在未收到响应头时为 0。
	HTTPStatus int
	// Latency 和 LatencyMS 只表示响应头耗时，不是首 token 延迟。
	Latency   time.Duration
	LatencyMS int
	// DurationMS 包含读取和校验响应体，不包含健康记录写入。
	DurationMS int
	// ErrorCode 是稳定原因；Error 不含上游响应正文或凭据。
	ErrorCode string
	Error     string
}

func (r ProbeResult) Healthy() bool {
	return r.Status == HealthStatusHealthy
}

// Prober 构造单次最小原生请求，并将证据绑定到连接配置版本。
type Prober struct {
	client   *Client
	recorder HealthRecorder
	now      func() time.Time
}

func NewProber(client *Client, recorder HealthRecorder) (*Prober, error) {
	if client == nil {
		return nil, errors.New("upstream: prober 需要非 nil 的 client")
	}
	return &Prober{client: client, recorder: recorder, now: time.Now}, nil
}

// Probe 不重试或增加输出预算。返回的 error 仅表示未初始化或健康记录写入失败。
func (p *Prober) Probe(ctx context.Context, target Target, upstreamModel string) (ProbeResult, error) {
	result := ProbeResult{Status: HealthStatusUnhealthy}
	if p == nil || p.client == nil {
		return result, errors.New("upstream: prober 未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := p.nowTime()
	finish := func() (ProbeResult, error) {
		result.DurationMS = int(max(p.nowTime().Sub(start), 0) / time.Millisecond)
		return result, p.record(ctx, target, upstreamModel, result)
	}

	adapter, err := adapterFor(target.Protocol)
	if err != nil {
		result.ErrorCode, result.Error = "unsupported_protocol", "上游协议不支持生成探针"
		return finish()
	}
	body, err := adapter.probeBody(upstreamModel)
	if err != nil {
		result.ErrorCode, result.Error = "invalid_request", "无法构造生成探针请求"
		return finish()
	}

	response, err := p.client.Send(ctx, target, Request{UpstreamModel: upstreamModel, Body: body})
	result.Latency = max(p.nowTime().Sub(start), 0)
	result.LatencyMS = int(result.Latency / time.Millisecond)
	if err != nil {
		result.ErrorCode = probeTransportCode(err, "transport_error")
		result.Error = sanitizeTransportError(err)
		return finish()
	}

	result.HTTPStatus = response.StatusCode
	// 多读一个字节才能区分恰好达到上限与被截断的响应。
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, probeDrainLimit+1))
	_ = response.Body.Close()
	switch {
	case response.StatusCode < 200 || response.StatusCode >= 300:
		result.ErrorCode, result.Error = "http_status", probeStatusError(response.StatusCode)
	case len(payload) > probeDrainLimit:
		result.ErrorCode, result.Error = "response_too_large", "上游探针响应超过读取上限"
	case readErr != nil:
		result.ErrorCode = probeTransportCode(readErr, "body_read_error")
		result.Error = sanitizeTransportError(readErr)
	default:
		result.ErrorCode = validateProbePayload(adapter.protocol(), payload)
		switch result.ErrorCode {
		case "":
			result.Status = HealthStatusHealthy
		case "response_incomplete", "output_limit":
			result.Status = HealthStatusInconclusive
		}
		result.Error = probeValidationMessage(result.ErrorCode)
	}
	return finish()
}

func probeTransportCode(err error, fallback string) string {
	switch {
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrCanceled), errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrInvalidBaseURL):
		return "invalid_base_url"
	case errors.Is(err, ErrCrossHostRedirect), errors.Is(err, ErrTooManyRedirects):
		return "redirect_rejected"
	default:
		return fallback
	}
}

func validateProbePayload(protocol string, payload []byte) string {
	if !json.Valid(payload) {
		return "invalid_json"
	}
	var envelope struct {
		Object string          `json:"object"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "invalid_protocol"
	}
	if probeHasError(envelope.Error) {
		return "upstream_error"
	}
	switch protocol {
	case store.ChannelProtocolOpenAIResponses:
		if envelope.Object != "response" {
			return "invalid_protocol"
		}
		return validateResponsesProbe(payload)
	case store.ChannelProtocolOpenAIChat:
		if envelope.Object != "chat.completion" {
			return "invalid_protocol"
		}
		return validateChatProbe(payload)
	default:
		return "unsupported_protocol"
	}
}

func probeHasError(value json.RawMessage) bool {
	return len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func validateResponsesProbe(payload []byte) string {
	var response struct {
		Status string `json:"status"`
		Output []struct {
			Type      string          `json:"type"`
			Status    string          `json:"status"`
			Role      string          `json:"role"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Error     json.RawMessage `json:"error"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.Output == nil {
		return "invalid_protocol"
	}
	for _, item := range response.Output {
		if probeHasError(item.Error) {
			return "upstream_error"
		}
	}
	if response.Status == "incomplete" {
		return "response_incomplete"
	}
	if response.Status != "completed" {
		return "invalid_terminal"
	}
	validOutput := false
	for _, item := range response.Output {
		if item.Status != "" && item.Status != "completed" {
			return "invalid_terminal"
		}
		switch item.Type {
		case "message":
			if item.Role != "assistant" {
				return "invalid_protocol"
			}
			for _, content := range item.Content {
				if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
					validOutput = true
				}
			}
		case "function_call":
			if strings.TrimSpace(item.CallID) == "" || !validProbeFunction(item.Name, item.Arguments) {
				return "invalid_tool_output"
			}
			validOutput = true
		}
	}
	if !validOutput {
		return "empty_output"
	}
	return ""
}

func validateChatProbe(payload []byte) string {
	type function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	var response struct {
		Choices []struct {
			FinishReason string          `json:"finish_reason"`
			Error        json.RawMessage `json:"error"`
			Message      struct {
				Role         string          `json:"role"`
				Content      string          `json:"content"`
				Error        json.RawMessage `json:"error"`
				FunctionCall *function       `json:"function_call"`
				ToolCalls    []struct {
					ID       string   `json:"id"`
					Type     string   `json:"type"`
					Function function `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || len(response.Choices) == 0 {
		return "invalid_protocol"
	}
	incomplete := false
	for _, choice := range response.Choices {
		if probeHasError(choice.Error) || probeHasError(choice.Message.Error) {
			return "upstream_error"
		}
		if choice.Message.Role != "assistant" {
			return "invalid_protocol"
		}
		switch choice.FinishReason {
		case "length":
			incomplete = true
		case "stop":
			if strings.TrimSpace(choice.Message.Content) == "" {
				return "empty_output"
			}
		case "tool_calls":
			if len(choice.Message.ToolCalls) == 0 {
				return "invalid_tool_output"
			}
			for _, tool := range choice.Message.ToolCalls {
				if strings.TrimSpace(tool.ID) == "" || tool.Type != "function" || !validProbeFunction(tool.Function.Name, tool.Function.Arguments) {
					return "invalid_tool_output"
				}
			}
		case "function_call":
			call := choice.Message.FunctionCall
			if call == nil || !validProbeFunction(call.Name, call.Arguments) {
				return "invalid_tool_output"
			}
		default:
			return "invalid_terminal"
		}
	}
	if incomplete {
		return "output_limit"
	}
	return ""
}

func validProbeFunction(name, arguments string) bool {
	var object map[string]json.RawMessage
	return strings.TrimSpace(name) != "" && json.Unmarshal([]byte(arguments), &object) == nil && object != nil
}

func probeValidationMessage(code string) string {
	switch code {
	case "":
		return ""
	case "invalid_json":
		return "上游探针响应不是完整 JSON"
	case "upstream_error":
		return "上游探针响应包含错误"
	case "invalid_terminal":
		return "上游探针响应缺少成功终态"
	case "empty_output":
		return "上游探针响应缺少非空有效输出"
	case "invalid_tool_output":
		return "上游探针工具输出结构无效"
	case "response_incomplete":
		return "上游响应未完成，无法确定生成是否有效"
	case "output_limit":
		return "探针输出预算耗尽，无法确定生成是否有效"
	default:
		return "上游探针响应不符合所选协议结构"
	}
}

func (p *Prober) nowTime() time.Time {
	if p.now == nil {
		return time.Now()
	}
	return p.now()
}

func (p *Prober) record(ctx context.Context, target Target, model string, result ProbeResult) error {
	if target.ChannelID <= 0 || p.recorder == nil {
		return nil
	}
	latency, duration := result.LatencyMS, result.DurationMS
	health := store.ChannelHealth{
		ConnectionRevision: target.ConnectionRevision,
		Model:              model,
		Protocol:           strings.TrimSpace(target.Protocol),
		Kind:               "generation",
		Status:             result.Status,
		LatencyMS:          &latency,
		DurationMS:         &duration,
		At:                 p.nowTime(),
		Error:              result.Error,
	}
	// 检查超时也是有效结果；记录使用独立短预算，不延长上游调用。
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := p.recorder.UpdateHealth(recordCtx, target.ChannelID, health); err != nil {
		return fmt.Errorf("upstream: 更新渠道健康状态失败：%w", err)
	}
	return nil
}

func probeStatusError(status int) string {
	if text := http.StatusText(status); text != "" {
		return fmt.Sprintf("上游返回 HTTP %d %s", status, text)
	}
	return fmt.Sprintf("上游返回 HTTP %d", status)
}
