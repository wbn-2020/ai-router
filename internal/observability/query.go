package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const requestLogColumns = `request_id, protocol, endpoint, model, channel_id, channel_name,
	status, http_status, error_code, error_message, stream, attempt_count, retry_count,
	ttft_ms, latency_ms, input_tokens, output_tokens, total_tokens, usage_source,
	semantic_committed, response_bytes, started_at, finished_at`

const requestAttemptColumns = `request_id, attempt_index, channel_id, channel_name,
	upstream_model, status, http_status, error_code, error_message, latency_ms,
	semantic_committed, started_at, finished_at`

// RequestFilter 是请求日志列表的查询条件。
//
// From/To 为零值时表示不限制该端；ChannelID 为 nil 表示不按渠道过滤。
// Limit <= 0 使用默认值 50，最大 200；Offset < 0 视为 0。
type RequestFilter struct {
	From      time.Time
	To        time.Time
	Model     string
	ChannelID *int64
	Status    string
	ErrorCode string
	Limit     int
	Offset    int
}

// RequestPage 是一页请求日志与满足过滤条件的总行数。
type RequestPage struct {
	Requests []RequestLog
	Total    int64
	Limit    int
	Offset   int
}

// RequestDetail 是单条请求日志及其渠道尝试。
type RequestDetail struct {
	Request  RequestLog
	Attempts []RequestAttempt
}

// ListRequests 按 started_at 倒序返回请求日志。
func (s *Store) ListRequests(ctx context.Context, filter RequestFilter) (RequestPage, error) {
	if err := s.ready(); err != nil {
		return RequestPage{}, err
	}
	if err := validateTimeRange(filter.From, filter.To); err != nil {
		return RequestPage{}, err
	}

	where, args := requestWhere(filter)
	var total int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM request_logs WHERE `+where, args...).Scan(&total); err != nil {
		return RequestPage{}, fmt.Errorf("observability: 统计请求日志失败：%w", err)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	queryArgs := make([]any, 0, len(args)+2)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit, offset)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestLogColumns+`
		 FROM request_logs
		 WHERE `+where+`
		 ORDER BY started_at DESC, id DESC
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return RequestPage{}, fmt.Errorf("observability: 查询请求日志失败：%w", err)
	}
	defer rows.Close()

	logs := make([]RequestLog, 0, limit)
	for rows.Next() {
		log, err := scanRequestLog(rows)
		if err != nil {
			return RequestPage{}, fmt.Errorf("observability: 读取请求日志失败：%w", err)
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		return RequestPage{}, fmt.Errorf("observability: 遍历请求日志失败：%w", err)
	}

	page := RequestPage{Requests: logs, Total: total, Limit: limit, Offset: offset}
	return page, nil
}

// GetRequest 按 request_id 读取一条日志及其 attempts，attempts 按序号升序。
func (s *Store) GetRequest(ctx context.Context, requestID string) (RequestDetail, error) {
	if err := s.ready(); err != nil {
		return RequestDetail{}, err
	}
	id := strings.TrimSpace(requestID)
	if id == "" {
		return RequestDetail{}, ErrRequestNotFound
	}

	log, err := scanRequestLog(s.db.QueryRowContext(ctx,
		`SELECT `+requestLogColumns+` FROM request_logs WHERE request_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDetail{}, ErrRequestNotFound
	}
	if err != nil {
		return RequestDetail{}, fmt.Errorf("observability: 读取请求日志失败：%w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestAttemptColumns+`
		 FROM request_attempts
		 WHERE request_id = ?
		 ORDER BY attempt_index ASC`, id)
	if err != nil {
		return RequestDetail{}, fmt.Errorf("observability: 查询渠道尝试失败：%w", err)
	}
	defer rows.Close()

	attempts := make([]RequestAttempt, 0, 4)
	for rows.Next() {
		attempt, err := scanRequestAttempt(rows)
		if err != nil {
			return RequestDetail{}, fmt.Errorf("observability: 读取渠道尝试失败：%w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return RequestDetail{}, fmt.Errorf("observability: 遍历渠道尝试失败：%w", err)
	}

	return RequestDetail{Request: log, Attempts: attempts}, nil
}

// StatsFilter 是统计查询的时间与维度过滤条件。
type StatsFilter struct {
	From      time.Time
	To        time.Time
	Model     string
	ChannelID *int64
}

// StatsMetrics 是一组请求的聚合指标。
//
// InputTokens/OutputTokens/TotalTokens 为 nil 表示过滤范围内没有任何已知
// usage，而不是 0。ErrorCount 包含非 success 的全部状态（error、cancelled）。
type StatsMetrics struct {
	RequestCount int64
	SuccessCount int64
	ErrorCount   int64
	ErrorRate    *float64
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
	UsageCount   int64
	AvgLatencyMS *float64
	P95LatencyMS *int64
}

// HourlyStats 是按小时聚合的一个桶；Hour 为桶起点（UTC）。
type HourlyStats struct {
	Hour    time.Time
	Metrics StatsMetrics
}

// ModelStats 是按客户端模型聚合的一行。
type ModelStats struct {
	Model   string
	Metrics StatsMetrics
}

// ChannelStats 是按渠道快照聚合的一行；渠道删除后 ChannelID 为 nil，
// ChannelName 仍保留历史快照。
type ChannelStats struct {
	ChannelID   *int64
	ChannelName string
	Metrics     StatsMetrics
}

// Stats 是总量、按小时、按模型与按渠道的聚合结果。
type Stats struct {
	Totals    StatsMetrics
	Hourly    []HourlyStats
	ByModel   []ModelStats
	ByChannel []ChannelStats
}

// Stats 计算过滤范围内的请求总数、成功率/错误率、token、平均与 P95 延迟。
//
// P95 使用 nearest-rank 定义：升序第 ceil(0.95*n) 个延迟值。延迟与 token
// 都来自 request_logs，不估算缺失值。
func (s *Store) Stats(ctx context.Context, filter StatsFilter) (Stats, error) {
	if err := s.ready(); err != nil {
		return Stats{}, err
	}
	if err := validateTimeRange(filter.From, filter.To); err != nil {
		return Stats{}, err
	}

	where, args := statsWhere(filter)

	totals, err := s.queryTotals(ctx, where, args)
	if err != nil {
		return Stats{}, err
	}
	hourly, err := s.queryHourlyStats(ctx, where, args)
	if err != nil {
		return Stats{}, err
	}
	byModel, err := s.queryModelStats(ctx, where, args)
	if err != nil {
		return Stats{}, err
	}
	byChannel, err := s.queryChannelStats(ctx, where, args)
	if err != nil {
		return Stats{}, err
	}

	return Stats{
		Totals:    totals,
		Hourly:    hourly,
		ByModel:   byModel,
		ByChannel: byChannel,
	}, nil
}

const totalsSQL = `
WITH filtered AS (
	SELECT status, input_tokens, output_tokens, total_tokens, latency_ms
	FROM request_logs
	WHERE %s
),
agg AS (
	SELECT
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) AS success_count,
		SUM(input_tokens) AS input_tokens,
		SUM(output_tokens) AS output_tokens,
		SUM(total_tokens) AS total_tokens,
		COUNT(CASE WHEN input_tokens IS NOT NULL OR output_tokens IS NOT NULL OR total_tokens IS NOT NULL THEN 1 END) AS usage_count,
		AVG(latency_ms) AS avg_latency_ms
	FROM filtered
),
pct AS (
	SELECT latency_ms
	FROM (
		SELECT latency_ms,
			ROW_NUMBER() OVER (ORDER BY latency_ms) AS rn,
			COUNT(*) OVER () AS cnt
		FROM filtered
	)
	WHERE rn = CAST((cnt * 95 + 99) / 100 AS INTEGER)
)
SELECT
	request_count,
	success_count,
	input_tokens,
	output_tokens,
	total_tokens,
	usage_count,
	avg_latency_ms,
	(SELECT latency_ms FROM pct) AS p95_latency_ms
FROM agg`

func (s *Store) queryTotals(ctx context.Context, where string, args []any) (StatsMetrics, error) {
	var (
		requestCount int64
		successCount int64
		inputTokens  sql.NullInt64
		outputTokens sql.NullInt64
		totalTokens  sql.NullInt64
		usageCount   int64
		avgLatency   sql.NullFloat64
		p95Latency   sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(totalsSQL, where), args...).Scan(
		&requestCount,
		&successCount,
		&inputTokens,
		&outputTokens,
		&totalTokens,
		&usageCount,
		&avgLatency,
		&p95Latency,
	)
	if err != nil {
		return StatsMetrics{}, fmt.Errorf("observability: 统计请求总量失败：%w", err)
	}
	return newStatsMetrics(requestCount, successCount, inputTokens, outputTokens,
		totalTokens, usageCount, avgLatency, p95Latency), nil
}

// groupedStatsSpec 描述一次分组聚合查询。所有字段都来自包内常量，
// 不包含外部输入；外部值只通过 WHERE 的占位符传入。
type groupedStatsSpec struct {
	// selectDims 是 filtered CTE 中的维度表达式（必须带稳定别名）。
	selectDims string
	// dims 是后续 CTE 引用的别名列表。
	dims string
	// selectAgg 是最终投影中的维度列。
	selectAgg string
	// join 是 agg 与 pct 的维度连接条件。
	join string
	// orderBy 是最终排序。
	orderBy string
}

var (
	hourlyStatsSpec = groupedStatsSpec{
		selectDims: `substr(started_at, 1, 13) || ':00:00Z' AS bucket`,
		dims:       `bucket`,
		selectAgg:  `agg.bucket`,
		join:       `agg.bucket = pct.bucket`,
		orderBy:    `agg.bucket ASC`,
	}
	modelStatsSpec = groupedStatsSpec{
		selectDims: `model AS model`,
		dims:       `model`,
		selectAgg:  `agg.model`,
		join:       `agg.model = pct.model`,
		orderBy:    `request_count DESC, agg.model ASC`,
	}
	channelStatsSpec = groupedStatsSpec{
		selectDims: `channel_id AS channel_id, channel_name AS channel_name`,
		dims:       `channel_id, channel_name`,
		selectAgg:  `agg.channel_id, agg.channel_name`,
		join:       `agg.channel_id IS pct.channel_id AND agg.channel_name = pct.channel_name`,
		orderBy:    `request_count DESC, agg.channel_id ASC, agg.channel_name ASC`,
	}
)

const groupedStatsTemplate = `
WITH filtered AS (
	SELECT
		%s,
		status, latency_ms, input_tokens, output_tokens, total_tokens
	FROM request_logs
	WHERE %s
),
agg AS (
	SELECT
		%s,
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) AS success_count,
		SUM(input_tokens) AS input_tokens,
		SUM(output_tokens) AS output_tokens,
		SUM(total_tokens) AS total_tokens,
		COUNT(CASE WHEN input_tokens IS NOT NULL OR output_tokens IS NOT NULL OR total_tokens IS NOT NULL THEN 1 END) AS usage_count,
		AVG(latency_ms) AS avg_latency_ms
	FROM filtered
	GROUP BY %s
),
pct AS (
	SELECT %s, latency_ms AS p95_latency_ms
	FROM (
		SELECT
			%s,
			latency_ms,
			ROW_NUMBER() OVER (PARTITION BY %s ORDER BY latency_ms) AS rn,
			COUNT(*) OVER (PARTITION BY %s) AS cnt
		FROM filtered
	)
	WHERE rn = CAST((cnt * 95 + 99) / 100 AS INTEGER)
)
SELECT
	%s,
	request_count,
	success_count,
	input_tokens,
	output_tokens,
	total_tokens,
	usage_count,
	avg_latency_ms,
	pct.p95_latency_ms
FROM agg
LEFT JOIN pct ON %s
ORDER BY %s`

func (s *Store) queryGroupedStats(
	ctx context.Context,
	where string,
	args []any,
	spec groupedStatsSpec,
	scan func(*sql.Rows) error,
) error {
	query := fmt.Sprintf(groupedStatsTemplate,
		spec.selectDims,
		where,
		spec.dims,
		spec.dims,
		spec.dims,
		spec.dims,
		spec.dims,
		spec.dims,
		spec.selectAgg,
		spec.join,
		spec.orderBy,
	)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("observability: 分组统计失败：%w", err)
	}
	defer rows.Close()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("observability: 读取分组统计失败：%w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("observability: 遍历分组统计失败：%w", err)
	}
	return nil
}

func (s *Store) queryHourlyStats(ctx context.Context, where string, args []any) ([]HourlyStats, error) {
	result := make([]HourlyStats, 0, 24)
	err := s.queryGroupedStats(ctx, where, args, hourlyStatsSpec, func(rows *sql.Rows) error {
		var bucket string
		metrics, err := scanStatsMetrics(rows, &bucket)
		if err != nil {
			return err
		}
		hour, err := time.Parse(time.RFC3339, bucket)
		if err != nil {
			return fmt.Errorf("解析小时桶 %q 失败：%w", bucket, err)
		}
		result = append(result, HourlyStats{Hour: hour.UTC(), Metrics: metrics})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) queryModelStats(ctx context.Context, where string, args []any) ([]ModelStats, error) {
	result := make([]ModelStats, 0, 8)
	err := s.queryGroupedStats(ctx, where, args, modelStatsSpec, func(rows *sql.Rows) error {
		var model string
		metrics, err := scanStatsMetrics(rows, &model)
		if err != nil {
			return err
		}
		result = append(result, ModelStats{Model: model, Metrics: metrics})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) queryChannelStats(ctx context.Context, where string, args []any) ([]ChannelStats, error) {
	result := make([]ChannelStats, 0, 8)
	err := s.queryGroupedStats(ctx, where, args, channelStatsSpec, func(rows *sql.Rows) error {
		var (
			channelID   sql.NullInt64
			channelName string
		)
		metrics, err := scanStatsMetrics(rows, &channelID, &channelName)
		if err != nil {
			return err
		}
		result = append(result, ChannelStats{
			ChannelID:   int64Pointer(channelID),
			ChannelName: channelName,
			Metrics:     metrics,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func scanStatsMetrics(rows *sql.Rows, prefix ...any) (StatsMetrics, error) {
	var (
		requestCount int64
		successCount int64
		inputTokens  sql.NullInt64
		outputTokens sql.NullInt64
		totalTokens  sql.NullInt64
		usageCount   int64
		avgLatency   sql.NullFloat64
		p95Latency   sql.NullInt64
	)
	dest := make([]any, 0, len(prefix)+8)
	dest = append(dest, prefix...)
	dest = append(dest,
		&requestCount,
		&successCount,
		&inputTokens,
		&outputTokens,
		&totalTokens,
		&usageCount,
		&avgLatency,
		&p95Latency,
	)
	if err := rows.Scan(dest...); err != nil {
		return StatsMetrics{}, err
	}
	return newStatsMetrics(requestCount, successCount, inputTokens, outputTokens,
		totalTokens, usageCount, avgLatency, p95Latency), nil
}

func newStatsMetrics(
	requestCount, successCount int64,
	inputTokens, outputTokens, totalTokens sql.NullInt64,
	usageCount int64,
	avgLatency sql.NullFloat64,
	p95Latency sql.NullInt64,
) StatsMetrics {
	errorCount := requestCount - successCount
	if errorCount < 0 {
		errorCount = 0
	}
	metrics := StatsMetrics{
		RequestCount: requestCount,
		SuccessCount: successCount,
		ErrorCount:   errorCount,
		UsageCount:   usageCount,
	}
	if requestCount > 0 {
		rate := float64(errorCount) / float64(requestCount)
		metrics.ErrorRate = &rate
	}
	if inputTokens.Valid {
		value := inputTokens.Int64
		metrics.InputTokens = &value
	}
	if outputTokens.Valid {
		value := outputTokens.Int64
		metrics.OutputTokens = &value
	}
	if totalTokens.Valid {
		value := totalTokens.Int64
		metrics.TotalTokens = &value
	}
	if avgLatency.Valid {
		value := avgLatency.Float64
		metrics.AvgLatencyMS = &value
	}
	if p95Latency.Valid {
		value := p95Latency.Int64
		metrics.P95LatencyMS = &value
	}
	return metrics
}

func requestWhere(filter RequestFilter) (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if !filter.From.IsZero() {
		clauses = append(clauses, "started_at >= ?")
		args = append(args, formatTime(filter.From))
	}
	if !filter.To.IsZero() {
		clauses = append(clauses, "started_at < ?")
		args = append(args, formatTime(filter.To))
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, model)
	}
	if filter.ChannelID != nil {
		clauses = append(clauses, "channel_id = ?")
		args = append(args, *filter.ChannelID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if errorCode := strings.TrimSpace(filter.ErrorCode); errorCode != "" {
		clauses = append(clauses, "error_code = ?")
		args = append(args, errorCode)
	}
	if len(clauses) == 0 {
		return "1 = 1", nil
	}
	return strings.Join(clauses, " AND "), args
}

func statsWhere(filter StatsFilter) (string, []any) {
	return requestWhere(RequestFilter{
		From:      filter.From,
		To:        filter.To,
		Model:     filter.Model,
		ChannelID: filter.ChannelID,
	})
}

func validateTimeRange(from, to time.Time) error {
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return ErrInvalidTimeRange
	}
	return nil
}

func scanRequestLog(scanner interface{ Scan(dest ...any) error }) (RequestLog, error) {
	var (
		log               RequestLog
		channelID         sql.NullInt64
		httpStatus        sql.NullInt64
		errorCode         sql.NullString
		errorMessage      sql.NullString
		stream            int
		ttftMS            sql.NullInt64
		inputTokens       sql.NullInt64
		outputTokens      sql.NullInt64
		totalTokens       sql.NullInt64
		usageSource       sql.NullString
		semanticCommitted int
		responseBytes     sql.NullInt64
		startedAt         string
		finishedAt        sql.NullString
	)
	if err := scanner.Scan(
		&log.RequestID,
		&log.Protocol,
		&log.Endpoint,
		&log.Model,
		&channelID,
		&log.ChannelName,
		&log.Status,
		&httpStatus,
		&errorCode,
		&errorMessage,
		&stream,
		&log.AttemptCount,
		&log.RetryCount,
		&ttftMS,
		&log.LatencyMS,
		&inputTokens,
		&outputTokens,
		&totalTokens,
		&usageSource,
		&semanticCommitted,
		&responseBytes,
		&startedAt,
		&finishedAt,
	); err != nil {
		return RequestLog{}, err
	}

	log.ChannelID = int64Pointer(channelID)
	log.HTTPStatus = intPointer(httpStatus)
	log.ErrorCode = errorCode.String
	log.ErrorMessage = errorMessage.String
	log.Stream = stream != 0
	log.TTFTMS = int64Pointer(ttftMS)
	log.InputTokens = int64Pointer(inputTokens)
	log.OutputTokens = int64Pointer(outputTokens)
	log.TotalTokens = int64Pointer(totalTokens)
	log.UsageSource = usageSource.String
	log.SemanticCommitted = semanticCommitted != 0
	log.ResponseBytes = int64Pointer(responseBytes)

	parsedStartedAt, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return RequestLog{}, fmt.Errorf("解析 started_at %q 失败：%w", startedAt, err)
	}
	log.StartedAt = parsedStartedAt.UTC()
	if finishedAt.Valid && finishedAt.String != "" {
		parsedFinishedAt, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return RequestLog{}, fmt.Errorf("解析 finished_at %q 失败：%w", finishedAt.String, err)
		}
		utc := parsedFinishedAt.UTC()
		log.FinishedAt = &utc
	}
	return log, nil
}

func scanRequestAttempt(scanner interface{ Scan(dest ...any) error }) (RequestAttempt, error) {
	var (
		attempt           RequestAttempt
		channelID         sql.NullInt64
		httpStatus        sql.NullInt64
		errorCode         sql.NullString
		errorMessage      sql.NullString
		semanticCommitted int
		startedAt         string
		finishedAt        sql.NullString
	)
	if err := scanner.Scan(
		&attempt.RequestID,
		&attempt.AttemptIndex,
		&channelID,
		&attempt.ChannelName,
		&attempt.UpstreamModel,
		&attempt.Status,
		&httpStatus,
		&errorCode,
		&errorMessage,
		&attempt.LatencyMS,
		&semanticCommitted,
		&startedAt,
		&finishedAt,
	); err != nil {
		return RequestAttempt{}, err
	}

	attempt.ChannelID = int64Pointer(channelID)
	attempt.HTTPStatus = intPointer(httpStatus)
	attempt.ErrorCode = errorCode.String
	attempt.ErrorMessage = errorMessage.String
	attempt.SemanticCommitted = semanticCommitted != 0

	parsedStartedAt, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return RequestAttempt{}, fmt.Errorf("解析 attempt started_at %q 失败：%w", startedAt, err)
	}
	attempt.StartedAt = parsedStartedAt.UTC()
	if finishedAt.Valid && finishedAt.String != "" {
		parsedFinishedAt, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return RequestAttempt{}, fmt.Errorf("解析 attempt finished_at %q 失败：%w", finishedAt.String, err)
		}
		utc := parsedFinishedAt.UTC()
		attempt.FinishedAt = &utc
	}
	return attempt, nil
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func intPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}
