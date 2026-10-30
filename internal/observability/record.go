// Package observability 负责请求日志、统计查询与后续的诊断环/按需捕获。
//
// T14 只实现元数据日志与统计：request_logs/request_attempts 保存请求 ID、
// 协议、模型、渠道快照、状态、错误、延迟和可空 token；不保存 header、body、
// Authorization 或 prompt。usage 缺失时 token 列保持 NULL，统计查询沿用
// SQL SUM 的语义，不把“未知”当作 0。
package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"ai-router/internal/secret"
)

const (
	// TimeLayout 是日志时间列的存储格式：UTC RFC3339 文本，固定 9 位小数。
	//
	// 固定宽度保证字符串比较与时间顺序一致；时区始终为 UTC。
	TimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

	// StatusSuccess 表示请求完成且客户端收到成功响应。
	StatusSuccess = "success"
	// StatusError 表示请求以错误响应结束。
	StatusError = "error"
	// StatusCancelled 表示客户端在完成前断开（通常对应 499）。
	StatusCancelled = "cancelled"

	// DefaultRetentionDays 是请求日志的默认保留天数。
	DefaultRetentionDays = 90
	// DefaultRetention 是默认保留期。
	DefaultRetention = DefaultRetentionDays * 24 * time.Hour
	// DefaultRetentionInterval 是保留清理任务的默认执行间隔。
	DefaultRetentionInterval = 24 * time.Hour
	// DefaultPruneBatch 是单次清理事务删除的日志条数上限。
	DefaultPruneBatch = 500
)

var (
	// ErrRequestNotFound 表示指定 request_id 的日志不存在。
	ErrRequestNotFound = errors.New("observability: 请求日志不存在")
	// ErrInvalidRecord 表示待写入的日志记录不满足基本约束。
	ErrInvalidRecord = errors.New("observability: 日志记录无效")
	// ErrInvalidTimeRange 表示查询时间范围非法。
	ErrInvalidTimeRange = errors.New("observability: 时间范围无效")
)

// DB 是观测仓储需要的数据库操作集合；*sql.DB 与 adminauth.DB 均满足。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

var _ DB = (*sql.DB)(nil)

// Store 提供请求日志写入、查询、统计与保留期清理。
//
// Store 不执行迁移，也不持有连接池生命周期；调用方复用 store.Store.DB()。
type Store struct {
	db DB
}

// New 构造观测仓储。db 必须已经完成迁移；nil 时所有方法返回未初始化错误。
func New(db DB) *Store {
	return &Store{db: db}
}

// RequestLog 是一条请求的最终元数据。
//
// 该结构刻意不包含 header、body、prompt 或 Authorization 字段，避免调用方
// 误将敏感内容写入日志。Token 字段为 nil 表示上游未提供 usage，而不是 0。
type RequestLog struct {
	RequestID string
	Protocol  string
	Endpoint  string
	Model     string

	ChannelID   *int64
	ChannelName string

	Status     string
	HTTPStatus *int

	ErrorCode    string
	ErrorMessage string

	Stream            bool
	AttemptCount      int
	RetryCount        int
	TTFTMS            *int64
	LatencyMS         int64
	InputTokens       *int64
	OutputTokens      *int64
	TotalTokens       *int64
	UsageSource       string
	SemanticCommitted bool
	ResponseBytes     *int64

	StartedAt  time.Time
	FinishedAt *time.Time
}

// RequestAttempt 是请求过程中一次渠道尝试的元数据。
type RequestAttempt struct {
	RequestID    string
	AttemptIndex int

	ChannelID     *int64
	ChannelName   string
	UpstreamModel string

	Status     string
	HTTPStatus *int

	ErrorCode    string
	ErrorMessage string

	LatencyMS         int64
	SemanticCommitted bool

	StartedAt  time.Time
	FinishedAt *time.Time
}

const insertRequestLogSQL = `
INSERT INTO request_logs (
	request_id, protocol, endpoint, model, channel_id, channel_name, status,
	http_status, error_code, error_message, stream, attempt_count, retry_count,
	ttft_ms, latency_ms, input_tokens, output_tokens, total_tokens, usage_source,
	semantic_committed, response_bytes, started_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const insertRequestAttemptSQL = `
INSERT INTO request_attempts (
	request_id, attempt_index, channel_id, channel_name, upstream_model,
	status, http_status, error_code, error_message, latency_ms,
	semantic_committed, started_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// WriteRequest 写入一条最终请求日志。
//
// 写入使用单条 INSERT（自动提交的短事务），不占用流式连接。同一 request_id
// 只允许写入一次；重复写入会返回数据库唯一约束错误。
func (s *Store) WriteRequest(ctx context.Context, log RequestLog) error {
	if err := s.ready(); err != nil {
		return err
	}
	log = normalizeRequestLog(log)
	if err := validateRequestLog(log); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, insertRequestLogSQL,
		log.RequestID,
		log.Protocol,
		log.Endpoint,
		log.Model,
		optionalInt64(log.ChannelID),
		log.ChannelName,
		log.Status,
		optionalInt(log.HTTPStatus),
		optionalString(log.ErrorCode),
		optionalString(log.ErrorMessage),
		boolToInt(log.Stream),
		log.AttemptCount,
		log.RetryCount,
		optionalInt64(log.TTFTMS),
		log.LatencyMS,
		optionalInt64(log.InputTokens),
		optionalInt64(log.OutputTokens),
		optionalInt64(log.TotalTokens),
		optionalString(log.UsageSource),
		boolToInt(log.SemanticCommitted),
		optionalInt64(log.ResponseBytes),
		formatTime(log.StartedAt),
		optionalTime(log.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("observability: 写入请求日志失败：%w", err)
	}
	return nil
}

// WriteAttempt 写入一次渠道尝试。
//
// attempt_index 从 1 开始；request_id 不设外键，允许在最终 request_log 之前
// 先写 attempts（见 0001_init.sql 的注释）。
func (s *Store) WriteAttempt(ctx context.Context, attempt RequestAttempt) error {
	if err := s.ready(); err != nil {
		return err
	}
	attempt = normalizeRequestAttempt(attempt)
	if err := validateRequestAttempt(attempt); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, insertRequestAttemptSQL,
		attempt.RequestID,
		attempt.AttemptIndex,
		optionalInt64(attempt.ChannelID),
		attempt.ChannelName,
		attempt.UpstreamModel,
		attempt.Status,
		optionalInt(attempt.HTTPStatus),
		optionalString(attempt.ErrorCode),
		optionalString(attempt.ErrorMessage),
		attempt.LatencyMS,
		boolToInt(attempt.SemanticCommitted),
		formatTime(attempt.StartedAt),
		optionalTime(attempt.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("observability: 写入渠道尝试失败：%w", err)
	}
	return nil
}

// PruneResult 汇总一次保留期清理删除的行数。
type PruneResult struct {
	Requests int64
	Attempts int64
}

// PruneRequestLogs 删除 started_at 早于 before 的请求日志及其 attempts。
//
// 按 request_id 分批删除，每批一个短事务；request_captures 不在清理范围内，
// 捕获由 T24 的 TTL 任务独立管理。batchSize <= 0 时使用 DefaultPruneBatch。
func (s *Store) PruneRequestLogs(ctx context.Context, before time.Time, batchSize int) (PruneResult, error) {
	if err := s.ready(); err != nil {
		return PruneResult{}, err
	}
	if before.IsZero() {
		return PruneResult{}, fmt.Errorf("%w: 清理截止时间不能为空", ErrInvalidTimeRange)
	}
	if batchSize <= 0 {
		batchSize = DefaultPruneBatch
	}

	var result PruneResult
	cutoff := formatTime(before)
	for {
		ids, err := s.oldRequestIDs(ctx, cutoff, batchSize)
		if err != nil {
			return result, err
		}
		if len(ids) == 0 {
			return result, nil
		}

		attempts, requests, err := s.deleteLogBatch(ctx, ids)
		if err != nil {
			return result, err
		}
		result.Attempts += attempts
		result.Requests += requests

		// 正常情况下每批都会删除请求行；若没有删除任何请求，提前退出避免
		// 在异常数据上无限循环。
		if requests == 0 {
			return result, nil
		}
		if len(ids) < batchSize {
			return result, nil
		}
	}
}

// RunRetention 运行按固定间隔执行的日志保留清理，直到 ctx 取消。
//
// 任务启动时立即清理一次，之后每个 interval 清理一次。调用方应放在独立
// goroutine 中运行；onError 可为 nil，非 nil 时接收清理错误（不包含敏感值）。
func (s *Store) RunRetention(
	ctx context.Context,
	retention time.Duration,
	interval time.Duration,
	onError func(error),
) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}

	prune := func() {
		if ctx.Err() != nil {
			return
		}
		cutoff := time.Now().UTC().Add(-retention)
		if _, err := s.PruneRequestLogs(ctx, cutoff, DefaultPruneBatch); err != nil &&
			ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}

	prune()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func (s *Store) oldRequestIDs(ctx context.Context, cutoff string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT request_id
		FROM request_logs
		WHERE started_at < ?
		ORDER BY started_at ASC, id ASC
		LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("observability: 查询待清理日志失败：%w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("observability: 读取待清理日志失败：%w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observability: 遍历待清理日志失败：%w", err)
	}
	return ids, nil
}

func (s *Store) deleteLogBatch(ctx context.Context, ids []string) (attempts, requests int64, err error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("observability: 开启清理事务失败：%w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	attemptResult, err := tx.ExecContext(ctx,
		`DELETE FROM request_attempts WHERE request_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("observability: 删除历史 attempts 失败：%w", err)
	}
	attempts, err = attemptResult.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("observability: 读取 attempts 删除结果失败：%w", err)
	}

	requestResult, err := tx.ExecContext(ctx,
		`DELETE FROM request_logs WHERE request_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("observability: 删除历史请求日志失败：%w", err)
	}
	requests, err = requestResult.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("observability: 读取日志删除结果失败：%w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("observability: 提交清理事务失败：%w", err)
	}
	return attempts, requests, nil
}

func (s *Store) ready() error {
	if s == nil || s.db == nil {
		return errors.New("observability: 日志仓储未初始化")
	}
	return nil
}

func normalizeRequestLog(log RequestLog) RequestLog {
	log.RequestID = strings.TrimSpace(log.RequestID)
	log.Protocol = strings.TrimSpace(log.Protocol)
	log.Endpoint = strings.TrimSpace(log.Endpoint)
	log.Model = strings.TrimSpace(log.Model)
	log.ChannelName = strings.TrimSpace(log.ChannelName)
	log.Status = strings.TrimSpace(log.Status)
	log.ErrorCode = strings.TrimSpace(log.ErrorCode)
	// 错误详情可能夹带上游响应片段；落库前统一去掉 Bearer/Key 等敏感值。
	log.ErrorMessage = secret.RedactValue(strings.TrimSpace(log.ErrorMessage))
	log.UsageSource = strings.TrimSpace(log.UsageSource)
	return log
}

func normalizeRequestAttempt(attempt RequestAttempt) RequestAttempt {
	attempt.RequestID = strings.TrimSpace(attempt.RequestID)
	attempt.ChannelName = strings.TrimSpace(attempt.ChannelName)
	attempt.UpstreamModel = strings.TrimSpace(attempt.UpstreamModel)
	attempt.Status = strings.TrimSpace(attempt.Status)
	attempt.ErrorCode = strings.TrimSpace(attempt.ErrorCode)
	attempt.ErrorMessage = secret.RedactValue(strings.TrimSpace(attempt.ErrorMessage))
	return attempt
}

func validateRequestLog(log RequestLog) error {
	if err := validateRequestID(log.RequestID); err != nil {
		return err
	}
	if log.Protocol == "" {
		return fmt.Errorf("%w: protocol 不能为空", ErrInvalidRecord)
	}
	if log.Status == "" {
		return fmt.Errorf("%w: status 不能为空", ErrInvalidRecord)
	}
	if log.AttemptCount < 0 {
		return fmt.Errorf("%w: attempt_count 不能为负", ErrInvalidRecord)
	}
	if log.RetryCount < 0 {
		return fmt.Errorf("%w: retry_count 不能为负", ErrInvalidRecord)
	}
	if log.LatencyMS < 0 {
		return fmt.Errorf("%w: latency_ms 不能为负", ErrInvalidRecord)
	}
	if err := validateOptionalNonNegative(log.TTFTMS, "ttft_ms"); err != nil {
		return err
	}
	if err := validateOptionalNonNegative(log.InputTokens, "input_tokens"); err != nil {
		return err
	}
	if err := validateOptionalNonNegative(log.OutputTokens, "output_tokens"); err != nil {
		return err
	}
	if err := validateOptionalNonNegative(log.TotalTokens, "total_tokens"); err != nil {
		return err
	}
	if err := validateOptionalNonNegative(log.ResponseBytes, "response_bytes"); err != nil {
		return err
	}
	if log.StartedAt.IsZero() {
		return fmt.Errorf("%w: started_at 不能为空", ErrInvalidRecord)
	}
	if log.FinishedAt != nil && log.FinishedAt.Before(log.StartedAt) {
		return fmt.Errorf("%w: finished_at 不能早于 started_at", ErrInvalidRecord)
	}
	return nil
}

func validateRequestAttempt(attempt RequestAttempt) error {
	if err := validateRequestID(attempt.RequestID); err != nil {
		return err
	}
	if attempt.AttemptIndex < 1 {
		return fmt.Errorf("%w: attempt_index 必须从 1 开始", ErrInvalidRecord)
	}
	if attempt.Status == "" {
		return fmt.Errorf("%w: status 不能为空", ErrInvalidRecord)
	}
	if attempt.LatencyMS < 0 {
		return fmt.Errorf("%w: latency_ms 不能为负", ErrInvalidRecord)
	}
	if attempt.StartedAt.IsZero() {
		return fmt.Errorf("%w: started_at 不能为空", ErrInvalidRecord)
	}
	if attempt.FinishedAt != nil && attempt.FinishedAt.Before(attempt.StartedAt) {
		return fmt.Errorf("%w: finished_at 不能早于 started_at", ErrInvalidRecord)
	}
	return nil
}

func validateRequestID(requestID string) error {
	if requestID == "" {
		return fmt.Errorf("%w: request_id 不能为空", ErrInvalidRecord)
	}
	if len(requestID) > 128 {
		return fmt.Errorf("%w: request_id 过长", ErrInvalidRecord)
	}
	if strings.IndexFunc(requestID, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: request_id 不能包含控制字符", ErrInvalidRecord)
	}
	return nil
}

func validateOptionalNonNegative(value *int64, name string) error {
	if value != nil && *value < 0 {
		return fmt.Errorf("%w: %s 不能为负", ErrInvalidRecord, name)
	}
	return nil
}

func formatTime(at time.Time) string {
	return at.UTC().Format(TimeLayout)
}

func optionalTime(at *time.Time) any {
	if at == nil {
		return nil
	}
	return formatTime(*at)
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
