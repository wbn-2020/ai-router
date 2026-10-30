package adminapi

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/secret"
)

// T14 请求观测与统计端点。
//
// 约束：
//   - 只暴露 request_logs/request_attempts 中的元数据，不返回 header、body
//     或 prompt；
//   - token 未知时响应字段为 null，不返回 0；
//   - 统计默认 24h，支持 from/to（RFC3339）或 range=24h/7d/30d，并可按
//     模型与渠道过滤；
//   - 所有响应沿用管理 API 的 Cache-Control: no-store。
func init() {
	registerExtraRoutes((*API).registerObservabilityRoutes)
}

const (
	defaultObservabilityRange = 24 * time.Hour
	defaultRequestLimit       = 50
	maxRequestLimit           = 200
	// maxFilterValueLength 限制文本过滤条件长度，避免异常查询放大。
	maxFilterValueLength = 256
)

func (a *API) registerObservabilityRoutes() {
	a.mux.Handle("GET /admin/api/requests",
		a.requireSession(http.HandlerFunc(a.handleListObservabilityRequests)))
	a.mux.Handle("GET /admin/api/requests/{id}",
		a.requireSession(http.HandlerFunc(a.handleGetObservabilityRequest)))
	a.mux.Handle("GET /admin/api/stats",
		a.requireSession(http.HandlerFunc(a.handleObservabilityStats)))
}

type requestLogView struct {
	RequestID string `json:"request_id"`
	Protocol  string `json:"protocol"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`

	ChannelID   *int64 `json:"channel_id"`
	ChannelName string `json:"channel_name"`

	Status     string `json:"status"`
	HTTPStatus *int   `json:"http_status"`

	ErrorCode    *string `json:"error_code"`
	ErrorMessage *string `json:"error_message"`

	Stream            bool    `json:"stream"`
	AttemptCount      int     `json:"attempt_count"`
	RetryCount        int     `json:"retry_count"`
	TTFTMS            *int64  `json:"ttft_ms"`
	LatencyMS         int64   `json:"latency_ms"`
	InputTokens       *int64  `json:"input_tokens"`
	OutputTokens      *int64  `json:"output_tokens"`
	TotalTokens       *int64  `json:"total_tokens"`
	UsageSource       *string `json:"usage_source"`
	SemanticCommitted bool    `json:"semantic_committed"`
	ResponseBytes     *int64  `json:"response_bytes"`

	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type requestAttemptView struct {
	AttemptIndex int `json:"attempt_index"`

	ChannelID     *int64 `json:"channel_id"`
	ChannelName   string `json:"channel_name"`
	UpstreamModel string `json:"upstream_model"`

	Status       string  `json:"status"`
	HTTPStatus   *int    `json:"http_status"`
	ErrorCode    *string `json:"error_code"`
	ErrorMessage *string `json:"error_message"`

	LatencyMS         int64      `json:"latency_ms"`
	SemanticCommitted bool       `json:"semantic_committed"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

type requestListResponse struct {
	Requests []requestLogView `json:"requests"`
	Total    int64            `json:"total"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

type requestDetailResponse struct {
	Request  requestLogView       `json:"request"`
	Attempts []requestAttemptView `json:"attempts"`
}

// statsPoint 既用于 totals，也用于 hourly/by_model/by_channel。
// 维度字段按分组类型填写，未使用的字段因 omitempty 不出现。
type statsPoint struct {
	Bucket      string `json:"bucket,omitempty"`
	Model       string `json:"model,omitempty"`
	ChannelID   *int64 `json:"channel_id,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`

	RequestCount int64    `json:"request_count"`
	SuccessCount int64    `json:"success_count"`
	ErrorCount   int64    `json:"error_count"`
	ErrorRate    *float64 `json:"error_rate"`
	InputTokens  *int64   `json:"input_tokens"`
	OutputTokens *int64   `json:"output_tokens"`
	TotalTokens  *int64   `json:"total_tokens"`
	UsageCount   int64    `json:"usage_count"`
	AvgLatencyMS *float64 `json:"avg_latency_ms"`
	P95LatencyMS *int64   `json:"p95_latency_ms"`
}

type statsResponse struct {
	From      time.Time    `json:"from"`
	To        time.Time    `json:"to"`
	Totals    statsPoint   `json:"totals"`
	Hourly    []statsPoint `json:"hourly"`
	ByModel   []statsPoint `json:"by_model"`
	ByChannel []statsPoint `json:"by_channel"`
}

func newRequestLogView(log observability.RequestLog) requestLogView {
	return requestLogView{
		RequestID:         log.RequestID,
		Protocol:          log.Protocol,
		Endpoint:          log.Endpoint,
		Model:             log.Model,
		ChannelID:         log.ChannelID,
		ChannelName:       log.ChannelName,
		Status:            log.Status,
		HTTPStatus:        log.HTTPStatus,
		ErrorCode:         nullableString(log.ErrorCode),
		ErrorMessage:      nullableString(log.ErrorMessage),
		Stream:            log.Stream,
		AttemptCount:      log.AttemptCount,
		RetryCount:        log.RetryCount,
		TTFTMS:            log.TTFTMS,
		LatencyMS:         log.LatencyMS,
		InputTokens:       log.InputTokens,
		OutputTokens:      log.OutputTokens,
		TotalTokens:       log.TotalTokens,
		UsageSource:       nullableString(log.UsageSource),
		SemanticCommitted: log.SemanticCommitted,
		ResponseBytes:     log.ResponseBytes,
		StartedAt:         log.StartedAt.UTC(),
		FinishedAt:        utcTime(log.FinishedAt),
	}
}

func newRequestAttemptView(attempt observability.RequestAttempt) requestAttemptView {
	return requestAttemptView{
		AttemptIndex:      attempt.AttemptIndex,
		ChannelID:         attempt.ChannelID,
		ChannelName:       attempt.ChannelName,
		UpstreamModel:     attempt.UpstreamModel,
		Status:            attempt.Status,
		HTTPStatus:        attempt.HTTPStatus,
		ErrorCode:         nullableString(attempt.ErrorCode),
		ErrorMessage:      nullableString(attempt.ErrorMessage),
		LatencyMS:         attempt.LatencyMS,
		SemanticCommitted: attempt.SemanticCommitted,
		StartedAt:         attempt.StartedAt.UTC(),
		FinishedAt:        utcTime(attempt.FinishedAt),
	}
}

func newStatsPoint(base statsPoint, metrics observability.StatsMetrics) statsPoint {
	base.RequestCount = metrics.RequestCount
	base.SuccessCount = metrics.SuccessCount
	base.ErrorCount = metrics.ErrorCount
	base.ErrorRate = metrics.ErrorRate
	base.InputTokens = metrics.InputTokens
	base.OutputTokens = metrics.OutputTokens
	base.TotalTokens = metrics.TotalTokens
	base.UsageCount = metrics.UsageCount
	base.AvgLatencyMS = roundLatency(metrics.AvgLatencyMS)
	base.P95LatencyMS = metrics.P95LatencyMS
	return base
}

func (a *API) handleListObservabilityRequests(w http.ResponseWriter, r *http.Request) {
	store, ok := a.observabilityStore(w)
	if !ok {
		return
	}
	values := r.URL.Query()
	from, to, err := parseObservabilityRange(values, time.Now())
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	channelID, err := parseChannelFilter(values)
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	model, err := parseTextFilter(values, "model")
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	status, err := parseTextFilter(values, "status")
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	errorCode, err := parseTextFilter(values, "error_code")
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	limit, offset, err := parsePagination(values)
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}

	page, err := store.ListRequests(r.Context(), observability.RequestFilter{
		From:      from,
		To:        to,
		Model:     model,
		ChannelID: channelID,
		Status:    status,
		ErrorCode: errorCode,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		a.writeObservabilityError(w, err)
		return
	}

	views := make([]requestLogView, 0, len(page.Requests))
	for _, log := range page.Requests {
		views = append(views, newRequestLogView(log))
	}
	writeJSON(w, http.StatusOK, requestListResponse{
		Requests: views,
		Total:    page.Total,
		Limit:    page.Limit,
		Offset:   page.Offset,
	})
}

func (a *API) handleGetObservabilityRequest(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.PathValue("id"))
	if requestID == "" || len(requestID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request_id", "请求 ID 无效")
		return
	}
	store, ok := a.observabilityStore(w)
	if !ok {
		return
	}

	detail, err := store.GetRequest(r.Context(), requestID)
	if err != nil {
		a.writeObservabilityError(w, err)
		return
	}

	attempts := make([]requestAttemptView, 0, len(detail.Attempts))
	for _, attempt := range detail.Attempts {
		attempts = append(attempts, newRequestAttemptView(attempt))
	}
	writeJSON(w, http.StatusOK, requestDetailResponse{
		Request:  newRequestLogView(detail.Request),
		Attempts: attempts,
	})
}

func (a *API) handleObservabilityStats(w http.ResponseWriter, r *http.Request) {
	store, ok := a.observabilityStore(w)
	if !ok {
		return
	}
	values := r.URL.Query()
	from, to, err := parseObservabilityRange(values, time.Now())
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	channelID, err := parseChannelFilter(values)
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}
	model, err := parseTextFilter(values, "model")
	if err != nil {
		writeObservabilityQueryError(w, err)
		return
	}

	stats, err := store.Stats(r.Context(), observability.StatsFilter{
		From:      from,
		To:        to,
		Model:     model,
		ChannelID: channelID,
	})
	if err != nil {
		a.writeObservabilityError(w, err)
		return
	}

	hourly := make([]statsPoint, 0, len(stats.Hourly))
	for _, point := range stats.Hourly {
		hourly = append(hourly, newStatsPoint(statsPoint{
			Bucket: point.Hour.UTC().Format(time.RFC3339),
		}, point.Metrics))
	}
	byModel := make([]statsPoint, 0, len(stats.ByModel))
	for _, point := range stats.ByModel {
		byModel = append(byModel, newStatsPoint(statsPoint{
			Model: point.Model,
		}, point.Metrics))
	}
	byChannel := make([]statsPoint, 0, len(stats.ByChannel))
	for _, point := range stats.ByChannel {
		byChannel = append(byChannel, newStatsPoint(statsPoint{
			ChannelID:   point.ChannelID,
			ChannelName: point.ChannelName,
		}, point.Metrics))
	}

	writeJSON(w, http.StatusOK, statsResponse{
		From:      from.UTC(),
		To:        to.UTC(),
		Totals:    newStatsPoint(statsPoint{}, stats.Totals),
		Hourly:    hourly,
		ByModel:   byModel,
		ByChannel: byChannel,
	})
}

// observabilityStore 从管理 API 已持有的数据库连接构造观测仓储。
// 观测端点不需要主密钥：日志与统计不包含上游凭据。
func (a *API) observabilityStore(w http.ResponseWriter) (*observability.Store, bool) {
	if a.db == nil {
		writeError(w, http.StatusServiceUnavailable, "observability_unavailable",
			"请求观测暂不可用：数据库未就绪")
		return nil, false
	}
	return observability.New(a.db), true
}

func (a *API) writeObservabilityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, observability.ErrRequestNotFound):
		writeError(w, http.StatusNotFound, "request_not_found", "请求日志不存在")
	case errors.Is(err, observability.ErrInvalidTimeRange):
		writeError(w, http.StatusBadRequest, "invalid_time_range", "时间范围无效")
	case errors.Is(err, observability.ErrInvalidRecord):
		writeError(w, http.StatusBadRequest, "invalid_request_log", "日志记录无效")
	default:
		a.logger.Error("请求观测操作失败", "error", secret.RedactValue(err.Error()))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

// queryParamError 让查询参数错误带上稳定的错误码。
type queryParamError struct {
	code    string
	message string
}

func (e *queryParamError) Error() string {
	return e.message
}

func invalidQueryParam(code, message string) error {
	return &queryParamError{code: code, message: message}
}

func writeObservabilityQueryError(w http.ResponseWriter, err error) {
	var paramErr *queryParamError
	if errors.As(err, &paramErr) {
		writeError(w, http.StatusBadRequest, paramErr.code, paramErr.message)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_query", "查询参数无效")
}

// parseObservabilityRange 解析 from/to 或 range。
//
// 未提供时间参数时默认最近 24h；只提供 from 时 to=now，只提供 to 时
// from=to-24h。from 必须早于 to。range 支持 Go duration 以及 Nd（天）。
func parseObservabilityRange(values url.Values, now time.Time) (time.Time, time.Time, error) {
	rawFrom := strings.TrimSpace(values.Get("from"))
	rawTo := strings.TrimSpace(values.Get("to"))

	if rawFrom == "" && rawTo == "" {
		duration := defaultObservabilityRange
		if rawRange := strings.TrimSpace(values.Get("range")); rawRange != "" {
			parsed, err := parseRangeDuration(rawRange)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			duration = parsed
		}
		to := now.UTC()
		return to.Add(-duration), to, nil
	}

	to := now.UTC()
	if rawTo != "" {
		parsed, err := time.Parse(time.RFC3339, rawTo)
		if err != nil {
			return time.Time{}, time.Time{}, invalidQueryParam(
				"invalid_time_range", "to 必须是 RFC3339 时间")
		}
		to = parsed.UTC()
	}
	from := to.Add(-defaultObservabilityRange)
	if rawFrom != "" {
		parsed, err := time.Parse(time.RFC3339, rawFrom)
		if err != nil {
			return time.Time{}, time.Time{}, invalidQueryParam(
				"invalid_time_range", "from 必须是 RFC3339 时间")
		}
		from = parsed.UTC()
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, invalidQueryParam(
			"invalid_time_range", "from 必须早于 to")
	}
	return from, to, nil
}

func parseRangeDuration(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days <= 0 {
			return 0, invalidQueryParam("invalid_range", "range 必须为正数时间范围")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, invalidQueryParam("invalid_range", "range 必须为正数时间范围")
	}
	return duration, nil
}

func parseChannelFilter(values url.Values) (*int64, error) {
	raw := strings.TrimSpace(values.Get("channel_id"))
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, invalidQueryParam("invalid_channel_filter", "channel_id 必须为正整数")
	}
	return &id, nil
}

func parseTextFilter(values url.Values, key string) (string, error) {
	value := strings.TrimSpace(values.Get(key))
	if len(value) > maxFilterValueLength {
		return "", invalidQueryParam("invalid_filter", key+" 过滤条件过长")
	}
	return value, nil
}

func parsePagination(values url.Values) (int, int, error) {
	limit := defaultRequestLimit
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxRequestLimit {
			return 0, 0, invalidQueryParam("invalid_pagination",
				"limit 必须为 1-200")
		}
		limit = parsed
	}

	offset := 0
	if raw := strings.TrimSpace(values.Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return 0, 0, invalidQueryParam("invalid_pagination",
				"offset 必须为非负整数")
		}
		offset = parsed
	}
	return limit, offset, nil
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// roundLatency 将平均延迟保留两位小数，避免 JSON 浮点噪声影响展示。
func roundLatency(value *float64) *float64 {
	if value == nil {
		return nil
	}
	rounded := math.Round(*value*100) / 100
	return &rounded
}
