package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-router/internal/store"
)

const (
	// probeMaxOutputTokens 把连通性探针的上游消耗限制在很小范围内。
	probeMaxOutputTokens = 16
	// probeDrainLimit 是探针读取响应体的上限；探针不需要 body 内容。
	probeDrainLimit = 64 << 10

	// HealthStatusHealthy 表示探针收到 2xx。
	HealthStatusHealthy = "healthy"
	// HealthStatusUnhealthy 表示探针失败或收到非 2xx。
	HealthStatusUnhealthy = "unhealthy"
)

// HealthRecorder 是探针写入渠道健康状态所需的最小能力。
//
// store.Channels 满足该接口；adminapi 传入对应渠道仓储即可。探针不直接
// 依赖数据库实现，便于测试替换。
type HealthRecorder interface {
	UpdateHealth(ctx context.Context, id int64, health store.ChannelHealth) error
}

var _ HealthRecorder = (*store.Channels)(nil)

// ProbeResult 是一次渠道探针的可展示结果。
//
// Error 已经过传输错误脱敏，不包含上游 Key、base URL 查询串或响应 body。
type ProbeResult struct {
	// Status 是 healthy 或 unhealthy。
	Status string
	// HTTPStatus 是上游 HTTP 状态码；连接失败/超时为 0。
	HTTPStatus int
	// Latency 是收到上游响应头的耗时。
	Latency time.Duration
	// LatencyMS 是 Latency 的毫秒截断值，用于写入渠道健康状态。
	LatencyMS int
	// Error 是脱敏失败原因；成功时为空。
	Error string
}

// Healthy 报告探针是否成功。
func (r ProbeResult) Healthy() bool {
	return r.Status == HealthStatusHealthy
}

// Prober 负责构造最小原生请求、执行探针并写回渠道健康状态。
type Prober struct {
	client   *Client
	recorder HealthRecorder
	now      func() time.Time
}

// NewProber 构造探针。client 必填；recorder 为 nil 时只返回结果，不写健康
// 状态（管理端测试或单元测试可使用该模式）。
func NewProber(client *Client, recorder HealthRecorder) (*Prober, error) {
	if client == nil {
		return nil, errors.New("upstream: prober 需要非 nil 的 client")
	}
	return &Prober{
		client:   client,
		recorder: recorder,
		now:      time.Now,
	}, nil
}

// Probe 对目标渠道执行一次最小原生请求。
//
// 上游非 2xx、连接失败、超时与协议不支持都体现在 ProbeResult.Error 中，
// 同时以 unhealthy 写入渠道健康状态。返回的 error 仅表示探针基础设施失败
// （Prober 未初始化、探针 body 构造失败或健康状态写入失败）。
func (p *Prober) Probe(ctx context.Context, target Target, upstreamModel string) (ProbeResult, error) {
	result := ProbeResult{Status: HealthStatusUnhealthy}
	if p == nil || p.client == nil {
		return result, errors.New("upstream: prober 未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	adapter, err := adapterFor(target.Protocol)
	if err != nil {
		result.Error = err.Error()
		return result, p.record(ctx, target, result)
	}
	body, err := adapter.probeBody(upstreamModel)
	if err != nil {
		result.Error = err.Error()
		return result, p.record(ctx, target, result)
	}

	start := p.nowTime()
	response, err := p.client.Send(ctx, target, Request{
		UpstreamModel: upstreamModel,
		Body:          body,
	})
	latency := p.nowTime().Sub(start)
	if latency < 0 {
		latency = 0
	}
	result.Latency = latency
	result.LatencyMS = int(latency / time.Millisecond)

	if err != nil {
		result.Error = err.Error()
		return result, p.record(ctx, target, result)
	}

	result.HTTPStatus = response.StatusCode
	// 只读取有限长度，避免探针把大响应完整载入内存；Close 同时释放
	// 请求 context 与连接。
	_, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, probeDrainLimit))
	_ = response.Body.Close()

	switch {
	case response.StatusCode < 200 || response.StatusCode >= 300:
		result.Error = probeStatusError(response.StatusCode)
	case copyErr != nil:
		// 2xx 但响应体读取失败（例如总时限内未结束）仍视为探测失败。
		result.Error = sanitizeTransportError(copyErr)
	default:
		result.Status = HealthStatusHealthy
	}
	return result, p.record(ctx, target, result)
}

func (p *Prober) nowTime() time.Time {
	if p.now == nil {
		return time.Now()
	}
	return p.now()
}

// record 把结果写入渠道健康状态。没有渠道 ID 或 recorder 时静默跳过。
func (p *Prober) record(ctx context.Context, target Target, result ProbeResult) error {
	if target.ChannelID <= 0 || p.recorder == nil {
		return nil
	}
	latency := result.LatencyMS
	health := store.ChannelHealth{
		Status:    result.Status,
		LatencyMS: &latency,
		At:        p.nowTime(),
		Error:     result.Error,
	}
	if err := p.recorder.UpdateHealth(ctx, target.ChannelID, health); err != nil {
		return fmt.Errorf("upstream: 更新渠道健康状态失败：%w", err)
	}
	return nil
}

// probeStatusError 生成只含状态码与标准短语的失败描述，不回显上游 body。
func probeStatusError(status int) string {
	if text := http.StatusText(status); text != "" {
		return fmt.Sprintf("上游返回 HTTP %d %s", status, text)
	}
	return fmt.Sprintf("上游返回 HTTP %d", status)
}
