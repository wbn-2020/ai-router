package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// channels 仓储负责渠道配置的持久化。
//
// 上游 Key 只以 AES-256-GCM 信封（key_ciphertext）和脱敏形式（key_mask）
// 落库；明文只在 adminapi 的创建/更新请求处理期间存在于内存中。仓储本身
// 不接触主密钥，也不做加解密，便于 adminapi 与后续探针/调度复用同一份
// 数据访问逻辑。
const channelColumns = `id, name, base_url, protocol, key_ciphertext, key_mask,
	priority, max_concurrency, enabled, last_used_at, last_health_status,
	last_health_latency_ms, last_health_at, last_error, created_at, updated_at`

// 渠道协议枚举与 0001 迁移中的 CHECK 约束保持一致。
const (
	// ChannelProtocolOpenAIResponses 表示原生 OpenAI Responses 渠道。
	ChannelProtocolOpenAIResponses = "openai_responses"
	// ChannelProtocolOpenAIChat 表示原生 OpenAI Chat Completions 渠道。
	ChannelProtocolOpenAIChat = "openai_chat"
	// ChannelProtocolAnthropicMessages 表示 Anthropic Messages 渠道（预留）。
	ChannelProtocolAnthropicMessages = "anthropic_messages"
)

const (
	channelPriorityMin     = 0
	channelPriorityMax     = 1000
	channelConcurrencyMin  = 1
	channelConcurrencyMax  = 64
	channelTimeLayout      = time.RFC3339Nano
	channelNameColumnLabel = "channels.name"
)

var (
	// ErrChannelNotFound 表示渠道不存在。
	ErrChannelNotFound = errors.New("store: 渠道不存在")
	// ErrChannelNameConflict 表示渠道名称违反 UNIQUE 约束。
	ErrChannelNameConflict = errors.New("store: 渠道名称已存在")
	// ErrInvalidChannel 表示渠道配置未通过校验。
	ErrInvalidChannel = errors.New("store: 渠道配置无效")
)

// ChannelValidationError 携带可直接展示给管理员的校验原因。
//
// 原因只描述规则（如“priority 必须为 0-1000”），不回显调用方输入，
// 因此 base URL 中的查询串或 Key 片段不会进入 HTTP 错误响应。
type ChannelValidationError struct {
	Reason string
}

func (e *ChannelValidationError) Error() string {
	return "store: 渠道配置无效：" + e.Reason
}

// Is 让 errors.Is(err, ErrInvalidChannel) 对校验错误成立。
func (e *ChannelValidationError) Is(target error) bool {
	return target == ErrInvalidChannel
}

// ChannelDB 是渠道仓储需要的数据库操作集合；*sql.DB 与 adminauth.DB
// 都满足该接口。
type ChannelDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Channel 是一个渠道的完整持久化视图。
//
// KeyCiphertext 只允许在服务端内部使用；管理 API 的列表/详情视图不得
// 包含该字段。
type Channel struct {
	ID             int64
	Name           string
	BaseURL        string
	Protocol       string
	KeyCiphertext  string
	KeyMask        string
	Priority       int
	MaxConcurrency int
	Enabled        bool

	LastUsedAt          *time.Time
	LastHealthStatus    string
	LastHealthLatencyMS *int
	LastHealthAt        *time.Time
	LastError           string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChannelConfig 是渠道中可编辑的非秘密字段。
type ChannelConfig struct {
	Name           string
	BaseURL        string
	Protocol       string
	Priority       int
	MaxConcurrency int
	Enabled        bool
}

// ChannelInput 是创建渠道所需的完整输入。
type ChannelInput struct {
	ChannelConfig
	KeyCiphertext string
	KeyMask       string
}

// ChannelUpdate 是全量更新渠道配置的输入。
//
// KeyCiphertext/KeyMask 同时为 nil 表示保留现有上游 Key；同时提供时
// 覆盖密文与脱敏值。只提供其中一个属于非法输入。
type ChannelUpdate struct {
	ChannelConfig
	KeyCiphertext *string
	KeyMask       *string
}

// ChannelHealth 是连通性探针写入的健康状态。
type ChannelHealth struct {
	Status    string
	LatencyMS *int
	At        time.Time
	Error     string
}

// Channels 提供 channels 表的 CRUD 与运行态辅助写入。
//
// Channels 可被并发使用；所有 SQL 均为参数化查询。
type Channels struct {
	db ChannelDB
}

// NewChannels 构造渠道仓储。db 为 nil 时调用方会在方法内得到可读错误。
func NewChannels(db ChannelDB) *Channels {
	return &Channels{db: db}
}

// Channels 返回绑定到当前数据库的渠道仓储。
func (s *Store) Channels() *Channels {
	return NewChannels(s.db)
}

// Create 插入渠道并返回落库后的完整记录。
func (r *Channels) Create(ctx context.Context, in ChannelInput) (Channel, error) {
	if r == nil || r.db == nil {
		return Channel{}, errors.New("store: 渠道仓储未初始化")
	}
	config, err := normalizeChannelConfig(in.ChannelConfig)
	if err != nil {
		return Channel{}, err
	}
	if strings.TrimSpace(in.KeyCiphertext) == "" || strings.TrimSpace(in.KeyMask) == "" {
		return Channel{}, invalidChannel("key_ciphertext 与 key_mask 不能为空")
	}

	now := formatChannelTime(time.Now())
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO channels (
			name, base_url, protocol, key_ciphertext, key_mask,
			priority, max_concurrency, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING `+channelColumns,
		config.Name,
		config.BaseURL,
		config.Protocol,
		in.KeyCiphertext,
		in.KeyMask,
		config.Priority,
		config.MaxConcurrency,
		boolToInt(config.Enabled),
		now,
		now,
	)
	channel, err := scanChannel(row)
	if err != nil {
		return Channel{}, wrapChannelWriteError("创建渠道失败", err)
	}
	return channel, nil
}

// Get 按 ID 返回渠道；不存在时返回 ErrChannelNotFound。
func (r *Channels) Get(ctx context.Context, id int64) (Channel, error) {
	if r == nil || r.db == nil {
		return Channel{}, errors.New("store: 渠道仓储未初始化")
	}
	row := r.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE id = ?`, id)
	channel, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("store: 读取渠道失败：%w", err)
	}
	return channel, nil
}

// List 返回全部渠道，按 priority ASC、id ASC 排序。
func (r *Channels) List(ctx context.Context) ([]Channel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("store: 渠道仓储未初始化")
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM channels ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出渠道失败：%w", err)
	}
	defer rows.Close()

	channels := make([]Channel, 0)
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 解析渠道记录失败：%w", err)
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历渠道失败：%w", err)
	}
	return channels, nil
}

// Update 全量更新渠道配置；KeyCiphertext/KeyMask 为 nil 时保留原 Key。
func (r *Channels) Update(ctx context.Context, id int64, in ChannelUpdate) (Channel, error) {
	if r == nil || r.db == nil {
		return Channel{}, errors.New("store: 渠道仓储未初始化")
	}
	config, err := normalizeChannelConfig(in.ChannelConfig)
	if err != nil {
		return Channel{}, err
	}
	if (in.KeyCiphertext == nil) != (in.KeyMask == nil) {
		return Channel{}, invalidChannel("key_ciphertext 与 key_mask 必须同时提供")
	}

	now := formatChannelTime(time.Now())
	var row *sql.Row
	if in.KeyCiphertext == nil {
		row = r.db.QueryRowContext(ctx, `
			UPDATE channels SET
				name = ?, base_url = ?, protocol = ?, priority = ?,
				max_concurrency = ?, enabled = ?, updated_at = ?
			WHERE id = ?
			RETURNING `+channelColumns,
			config.Name,
			config.BaseURL,
			config.Protocol,
			config.Priority,
			config.MaxConcurrency,
			boolToInt(config.Enabled),
			now,
			id,
		)
	} else {
		if strings.TrimSpace(*in.KeyCiphertext) == "" || strings.TrimSpace(*in.KeyMask) == "" {
			return Channel{}, invalidChannel("key_ciphertext 与 key_mask 不能为空")
		}
		row = r.db.QueryRowContext(ctx, `
			UPDATE channels SET
				name = ?, base_url = ?, protocol = ?, priority = ?,
				max_concurrency = ?, enabled = ?,
				key_ciphertext = ?, key_mask = ?, updated_at = ?
			WHERE id = ?
			RETURNING `+channelColumns,
			config.Name,
			config.BaseURL,
			config.Protocol,
			config.Priority,
			config.MaxConcurrency,
			boolToInt(config.Enabled),
			*in.KeyCiphertext,
			*in.KeyMask,
			now,
			id,
		)
	}

	channel, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, wrapChannelWriteError("更新渠道失败", err)
	}
	return channel, nil
}

// Delete 删除渠道。
//
// model_routes 由 FK ON DELETE CASCADE 一并删除；request_logs 与
// request_attempts 的 channel_id 由 FK ON DELETE SET NULL 置空，
// channel_name 快照保留。
func (r *Channels) Delete(ctx context.Context, id int64) error {
	if r == nil || r.db == nil {
		return errors.New("store: 渠道仓储未初始化")
	}
	var deletedID int64
	err := r.db.QueryRowContext(ctx,
		`DELETE FROM channels WHERE id = ? RETURNING id`, id).Scan(&deletedID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrChannelNotFound
	}
	if err != nil {
		return fmt.Errorf("store: 删除渠道失败：%w", err)
	}
	return nil
}

// SetEnabled 切换渠道启停状态并返回更新后的记录。
func (r *Channels) SetEnabled(ctx context.Context, id int64, enabled bool) (Channel, error) {
	if r == nil || r.db == nil {
		return Channel{}, errors.New("store: 渠道仓储未初始化")
	}
	row := r.db.QueryRowContext(ctx, `
		UPDATE channels SET enabled = ?, updated_at = ?
		WHERE id = ?
		RETURNING `+channelColumns,
		boolToInt(enabled),
		formatChannelTime(time.Now()),
		id,
	)
	channel, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("store: 更新渠道启停失败：%w", err)
	}
	return channel, nil
}

// UpdateHealth 写入一次连通性探测结果。
//
// 健康状态属于运行态观测，不改变 updated_at（配置变更时间）。
func (r *Channels) UpdateHealth(ctx context.Context, id int64, health ChannelHealth) error {
	if r == nil || r.db == nil {
		return errors.New("store: 渠道仓储未初始化")
	}
	var latency any
	if health.LatencyMS != nil {
		latency = *health.LatencyMS
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE channels SET
			last_health_status = ?,
			last_health_latency_ms = ?,
			last_health_at = ?,
			last_error = ?
		WHERE id = ?`,
		strings.TrimSpace(health.Status),
		latency,
		formatChannelTime(health.At),
		health.Error,
		id,
	)
	if err != nil {
		return fmt.Errorf("store: 更新渠道健康状态失败：%w", err)
	}
	return ensureChannelAffected(result)
}

// TouchLastUsed 用一次短 UPDATE 记录渠道最近被调度的时间。
func (r *Channels) TouchLastUsed(ctx context.Context, id int64, at time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("store: 渠道仓储未初始化")
	}
	result, err := r.db.ExecContext(ctx,
		`UPDATE channels SET last_used_at = ? WHERE id = ?`,
		formatChannelTime(at), id)
	if err != nil {
		return fmt.Errorf("store: 更新渠道 last_used_at 失败：%w", err)
	}
	return ensureChannelAffected(result)
}

func ensureChannelAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取渠道更新结果失败：%w", err)
	}
	if affected == 0 {
		return ErrChannelNotFound
	}
	return nil
}

// normalizeChannelConfig 校验并规范化渠道配置。
func normalizeChannelConfig(config ChannelConfig) (ChannelConfig, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		return ChannelConfig{}, invalidChannel("name 不能为空")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ChannelConfig{}, invalidChannel("name 不能包含控制字符")
	}

	baseURL, err := normalizeChannelBaseURL(config.BaseURL)
	if err != nil {
		return ChannelConfig{}, err
	}

	protocol := strings.TrimSpace(config.Protocol)
	switch protocol {
	case ChannelProtocolOpenAIResponses, ChannelProtocolOpenAIChat, ChannelProtocolAnthropicMessages:
	default:
		return ChannelConfig{}, invalidChannel(
			"protocol 必须为 openai_responses、openai_chat 或 anthropic_messages")
	}

	if config.Priority < channelPriorityMin || config.Priority > channelPriorityMax {
		return ChannelConfig{}, invalidChannel("priority 必须为 0-1000")
	}
	if config.MaxConcurrency < channelConcurrencyMin || config.MaxConcurrency > channelConcurrencyMax {
		return ChannelConfig{}, invalidChannel("max_concurrency 必须为 1-64")
	}

	config.Name = name
	config.BaseURL = baseURL
	config.Protocol = protocol
	return config, nil
}

// normalizeChannelBaseURL 只接受无 userinfo、无 fragment 的 http/https URL。
func normalizeChannelBaseURL(raw string) (string, error) {
	baseURL := strings.TrimSpace(raw)
	if baseURL == "" {
		return "", invalidChannel("base_url 不能为空")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Opaque != "" {
		return "", invalidChannel("base_url 不是合法的 URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalidChannel("base_url 只允许 http/https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", invalidChannel("base_url 缺少主机名")
	}
	if parsed.User != nil {
		return "", invalidChannel("base_url 不能包含用户信息")
	}
	// url.Parse 会把 “...#” 解析为空 fragment；按字符检查保证任何 # 都被拒绝。
	if parsed.Fragment != "" || strings.ContainsRune(baseURL, '#') {
		return "", invalidChannel("base_url 不能包含 fragment")
	}
	return baseURL, nil
}

func invalidChannel(reason string) error {
	return &ChannelValidationError{Reason: reason}
}

// wrapChannelWriteError 把 SQLite UNIQUE 冲突映射为稳定错误；其余错误保留
// 底层原因，错误信息不含 Key 或明文。
func wrapChannelWriteError(action string, err error) error {
	if isChannelNameConflict(err) {
		return ErrChannelNameConflict
	}
	return fmt.Errorf("store: %s：%w", action, err)
}

func isChannelNameConflict(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		if code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT {
			return strings.Contains(sqliteErr.Error(), channelNameColumnLabel)
		}
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: "+channelNameColumnLabel)
}

type channelScanner interface {
	Scan(dest ...any) error
}

func scanChannel(scanner channelScanner) (Channel, error) {
	var (
		channel          Channel
		enabled          int
		lastUsedAt       sql.NullString
		lastHealthStatus sql.NullString
		latency          sql.NullInt64
		lastHealthAt     sql.NullString
		lastError        sql.NullString
		createdAt        string
		updatedAt        string
	)
	if err := scanner.Scan(
		&channel.ID,
		&channel.Name,
		&channel.BaseURL,
		&channel.Protocol,
		&channel.KeyCiphertext,
		&channel.KeyMask,
		&channel.Priority,
		&channel.MaxConcurrency,
		&enabled,
		&lastUsedAt,
		&lastHealthStatus,
		&latency,
		&lastHealthAt,
		&lastError,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Channel{}, err
	}

	channel.Enabled = enabled != 0
	channel.LastHealthStatus = lastHealthStatus.String
	channel.LastError = lastError.String
	if latency.Valid {
		value := int(latency.Int64)
		channel.LastHealthLatencyMS = &value
	}
	if lastUsedAt.Valid && lastUsedAt.String != "" {
		parsed, err := time.Parse(channelTimeLayout, lastUsedAt.String)
		if err != nil {
			return Channel{}, fmt.Errorf("解析 last_used_at 失败：%w", err)
		}
		channel.LastUsedAt = &parsed
	}
	if lastHealthAt.Valid && lastHealthAt.String != "" {
		parsed, err := time.Parse(channelTimeLayout, lastHealthAt.String)
		if err != nil {
			return Channel{}, fmt.Errorf("解析 last_health_at 失败：%w", err)
		}
		channel.LastHealthAt = &parsed
	}
	created, err := time.Parse(channelTimeLayout, createdAt)
	if err != nil {
		return Channel{}, fmt.Errorf("解析 created_at 失败：%w", err)
	}
	updated, err := time.Parse(channelTimeLayout, updatedAt)
	if err != nil {
		return Channel{}, fmt.Errorf("解析 updated_at 失败：%w", err)
	}
	channel.CreatedAt = created
	channel.UpdatedAt = updated
	return channel, nil
}

func formatChannelTime(at time.Time) string {
	return at.UTC().Format(channelTimeLayout)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
