package adminapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ai-router/internal/secret"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

// T22 调试 API：渠道连通性测试与模型路由测试。
//
// 约束：
//   - 探针使用渠道协议的原生最小请求（ping，输出上限 16 tokens），会消耗
//     少量上游额度，因此只由管理员显式触发；
//   - 返回的错误一律经过脱敏，不含上游 Key、base URL 查询串或上游响应体；
//   - 测试结果写回 channels.last_health_status/last_health_latency_ms/
//     last_health_at/last_error；
//   - 模型测试只使用该模型已启用映射的渠道；没有候选时返回明确错误；
//   - 调试探针不是客户端请求，不写 request_logs；调试页通过渠道/模型筛选
//     链接跳转到最近的请求记录。
func init() {
	registerExtraRoutes((*API).registerDebugRoutes)
}

const (
	// debugMaxModelLength 限制调试请求中的模型名长度，避免异常输入放大查询。
	debugMaxModelLength = 256
	// debugRequestLinkPath 是管理端请求观测页路径（TanStack Router basepath=/admin）。
	debugRequestLinkPath = "/admin/requests"
)

var (
	// errDebugRouteNotFound 表示指定模型在目标渠道上没有启用的映射。
	errDebugRouteNotFound = errors.New("adminapi: 模型在此渠道没有启用的映射")
	// errDebugChannelNoMapping 表示渠道没有任何启用的模型映射。
	errDebugChannelNoMapping = errors.New("adminapi: 渠道没有启用的模型映射")
)

// newDebugProbeClient 构造调试探针使用的上游 HTTP client。
//
// 计划要求 adminapi 只依赖 upstream probe；管理 API 不持有运行时配置，因此
// 使用 upstream 默认超时。测试可替换该函数注入更短的超时来覆盖超时路径。
var newDebugProbeClient = func() (*upstream.Client, error) {
	return upstream.NewClient(upstream.DefaultOptions())
}

func (a *API) registerDebugRoutes() {
	a.mux.Handle("POST /admin/api/debug/channels/{id}/test",
		a.requireSession(http.HandlerFunc(a.handleDebugChannelTest)))
	a.mux.Handle("POST /admin/api/debug/models/{model}/test",
		a.requireSession(http.HandlerFunc(a.handleDebugModelTest)))
}

// debugChannelTestRequest 是渠道测试的可选请求体；model 是客户端模型名。
type debugChannelTestRequest struct {
	Model string `json:"model"`
}

// debugTestResponse 是一次调试探针的可展示结果。
//
// Error 已经过脱敏；RequestLink 指向按渠道/模型筛选的请求观测页。
type debugTestResponse struct {
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status"`
	LatencyMS  int    `json:"latency_ms"`
	Error      string `json:"error,omitempty"`

	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Model       string `json:"model,omitempty"`

	UpstreamModel string    `json:"upstream_model"`
	TestedAt      time.Time `json:"tested_at"`
	RequestLink   string    `json:"request_link"`
}

// debugCandidate 是一条可参与模型测试的映射及其渠道快照。
type debugCandidate struct {
	route   store.ModelRoute
	channel store.Channel
}

func (a *API) handleDebugChannelTest(w http.ResponseWriter, r *http.Request) {
	id, ok := channelIDFromRequest(w, r)
	if !ok {
		return
	}

	var req debugChannelTestRequest
	if err := decodeJSONBody(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, err)
		return
	}
	clientModel, ok := normalizeDebugModel(w, req.Model, false)
	if !ok {
		return
	}

	channelsRepo, envelope, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	channel, err := channelsRepo.Get(r.Context(), id)
	if err != nil {
		a.writeChannelError(w, err)
		return
	}

	upstreamModel, resolvedModel, err := a.debugChannelUpstreamModel(
		r.Context(), channel, clientModel)
	if err != nil {
		switch {
		case errors.Is(err, errDebugRouteNotFound):
			writeError(w, http.StatusBadRequest, "model_route_not_found",
				"该模型在此渠道没有启用的映射")
		case errors.Is(err, errDebugChannelNoMapping):
			writeError(w, http.StatusBadRequest, "channel_has_no_mapping",
				"该渠道没有启用的模型映射，请先在模型映射页配置")
		default:
			a.writeDebugInternalError(w, channel.ID, "查询渠道调试映射失败", err)
		}
		return
	}

	result, err := a.runDebugProbe(r.Context(), channelsRepo, envelope, channel, upstreamModel)
	if err != nil {
		a.writeDebugInternalError(w, channel.ID, "渠道调试探针失败", err)
		return
	}
	writeJSON(w, http.StatusOK, newDebugTestResponse(channel, resolvedModel, upstreamModel, result))
}

func (a *API) handleDebugModelTest(w http.ResponseWriter, r *http.Request) {
	model, ok := normalizeDebugModel(w, r.PathValue("model"), true)
	if !ok {
		return
	}

	channelsRepo, envelope, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	candidates, err := a.debugModelCandidates(r.Context(), model)
	if err != nil {
		a.writeDebugInternalError(w, 0, "查询模型调试候选失败", err)
		return
	}
	if len(candidates) == 0 {
		writeError(w, http.StatusBadRequest, "model_not_available",
			"该模型没有启用的映射渠道，无法测试")
		return
	}

	candidate := candidates[0]
	result, err := a.runDebugProbe(
		r.Context(), channelsRepo, envelope, candidate.channel, candidate.route.UpstreamModel)
	if err != nil {
		a.writeDebugInternalError(w, candidate.channel.ID, "模型调试探针失败", err)
		return
	}
	writeJSON(w, http.StatusOK, newDebugTestResponse(
		candidate.channel, model, candidate.route.UpstreamModel, result))
}

// debugChannelUpstreamModel 解析渠道测试要使用的上游模型。
//
// 指定 clientModel 时，必须在该渠道上存在启用的映射；未指定时取该渠道
// 优先级最高的启用映射。返回的第二个值是展示用的客户端模型名。
func (a *API) debugChannelUpstreamModel(
	ctx context.Context,
	channel store.Channel,
	clientModel string,
) (upstreamModel, resolvedModel string, err error) {
	if a.db == nil {
		return "", "", errors.New("adminapi: 数据库未就绪")
	}
	routes, err := store.NewModelRoutes(a.db).List(ctx)
	if err != nil {
		return "", "", err
	}

	best := -1
	for i := range routes {
		route := routes[i]
		if route.ChannelID != channel.ID || !route.Enabled {
			continue
		}
		if clientModel != "" && route.ClientModel != clientModel {
			continue
		}
		if best == -1 || debugRouteBefore(route, routes[best]) {
			best = i
		}
	}
	if best == -1 {
		if clientModel != "" {
			return "", clientModel, errDebugRouteNotFound
		}
		return "", "", errDebugChannelNoMapping
	}
	route := routes[best]
	return route.UpstreamModel, route.ClientModel, nil
}

// debugModelCandidates 返回模型测试的候选渠道，顺序与数据面一致：
// route.priority → channel.priority → last_used_at（NULL 最先）→ route.id。
//
// 只考虑启用映射、启用渠道且协议有原生 adapter 的候选；anthropic_messages
// 仍是预留协议，不参与调试。
func (a *API) debugModelCandidates(
	ctx context.Context,
	clientModel string,
) ([]debugCandidate, error) {
	if a.db == nil {
		return nil, errors.New("adminapi: 数据库未就绪")
	}
	routesRepo := store.NewModelRoutes(a.db)
	channelsRepo := store.NewChannels(a.db)

	protocols := []string{
		store.ChannelProtocolOpenAIResponses,
		store.ChannelProtocolOpenAIChat,
	}
	candidates := make([]debugCandidate, 0)
	for _, protocol := range protocols {
		routes, err := routesRepo.ListByModelAndProtocol(ctx, clientModel, protocol)
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			// 仓储查询已经过滤了启用与协议；这里再校验一次，保证实现替换
			// 时也不会把不可用映射/渠道送进调试。
			if !route.Enabled {
				continue
			}
			channel, err := channelsRepo.Get(ctx, route.ChannelID)
			if errors.Is(err, store.ErrChannelNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !channel.Enabled || channel.Protocol != protocol {
				continue
			}
			candidates = append(candidates, debugCandidate{route: route, channel: channel})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return debugCandidateBefore(candidates[i], candidates[j])
	})
	return candidates, nil
}

func debugCandidateBefore(left, right debugCandidate) bool {
	if left.route.Priority != right.route.Priority {
		return left.route.Priority < right.route.Priority
	}
	if left.channel.Priority != right.channel.Priority {
		return left.channel.Priority < right.channel.Priority
	}
	if cmp := debugCompareLastUsed(left.channel.LastUsedAt, right.channel.LastUsedAt); cmp != 0 {
		return cmp < 0
	}
	return left.route.ID < right.route.ID
}

// debugRouteBefore 按 route.priority → route.id 比较同一渠道上的映射。
func debugRouteBefore(left, right store.ModelRoute) bool {
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	return left.ID < right.ID
}

// debugCompareLastUsed 比较 last_used_at；nil（从未使用）排在最前。
func debugCompareLastUsed(left, right *time.Time) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return -1
	case right == nil:
		return 1
	case left.Before(*right):
		return -1
	case right.Before(*left):
		return 1
	default:
		return 0
	}
}

// runDebugProbe 解密渠道 Key 并执行一次原生最小请求。
//
// 返回的 error 只表示调试基础设施失败（解密、client 构造、健康状态写入）；
// 上游失败体现在 ProbeResult.Error 中。错误文本不包含 Key 或密文。
func (a *API) runDebugProbe(
	ctx context.Context,
	recorder *store.Channels,
	envelope *secret.Envelope,
	channel store.Channel,
	upstreamModel string,
) (upstream.ProbeResult, error) {
	if envelope == nil {
		return upstream.ProbeResult{}, errors.New("adminapi: 主密钥未就绪")
	}
	key, err := envelope.DecryptString(channel.KeyCiphertext, secret.PurposeChannelKey)
	if err != nil {
		return upstream.ProbeResult{}, fmt.Errorf("解密渠道上游 Key 失败：%w", err)
	}
	client, err := newDebugProbeClient()
	if err != nil {
		return upstream.ProbeResult{}, fmt.Errorf("构造上游探针 client 失败：%w", err)
	}
	prober, err := upstream.NewProber(client, recorder)
	if err != nil {
		return upstream.ProbeResult{}, fmt.Errorf("构造上游探针失败：%w", err)
	}
	return prober.Probe(ctx, upstream.Target{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		BaseURL:     channel.BaseURL,
		Protocol:    channel.Protocol,
		APIKey:      key,
	}, upstreamModel)
}

func newDebugTestResponse(
	channel store.Channel,
	clientModel, upstreamModel string,
	result upstream.ProbeResult,
) debugTestResponse {
	return debugTestResponse{
		Status:        result.Status,
		HTTPStatus:    result.HTTPStatus,
		LatencyMS:     result.LatencyMS,
		Error:         secret.RedactValue(result.Error),
		ChannelID:     channel.ID,
		ChannelName:   channel.Name,
		Model:         clientModel,
		UpstreamModel: upstreamModel,
		TestedAt:      time.Now().UTC(),
		RequestLink:   debugRequestLink(channel.ID, clientModel),
	}
}

// debugRequestLink 构造请求观测页的筛选链接；不包含任何上游凭据。
func debugRequestLink(channelID int64, model string) string {
	values := url.Values{}
	values.Set("channel_id", strconv.FormatInt(channelID, 10))
	if model != "" {
		values.Set("model", model)
	}
	return debugRequestLinkPath + "?" + values.Encode()
}

// normalizeDebugModel 校验模型名；required=false 时允许空字符串（渠道测试
// 可以不带 model）。控制字符与超长输入返回 400，错误信息不回显输入。
func normalizeDebugModel(w http.ResponseWriter, raw string, required bool) (string, bool) {
	model := strings.TrimSpace(raw)
	if model == "" {
		if required {
			writeError(w, http.StatusBadRequest, "invalid_model", "模型名不能为空")
			return "", false
		}
		return "", true
	}
	if len(model) > debugMaxModelLength ||
		strings.IndexFunc(model, unicode.IsControl) >= 0 {
		writeError(w, http.StatusBadRequest, "invalid_model", "模型名无效")
		return "", false
	}
	return model, true
}

// writeDebugInternalError 记录脱敏日志并返回通用 500；不回显底层细节。
func (a *API) writeDebugInternalError(w http.ResponseWriter, channelID int64, action string, err error) {
	if channelID > 0 {
		a.logger.Error(action, "channel_id", channelID,
			"error", secret.RedactValue(err.Error()))
	} else {
		a.logger.Error(action, "error", secret.RedactValue(err.Error()))
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
}
