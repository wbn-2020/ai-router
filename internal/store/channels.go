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
	last_health_latency_ms, last_health_at, last_error, created_at, updated_at,
	connection_revision, last_health_revision, last_health_model, last_health_protocol,
	last_health_kind, last_health_duration_ms`

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

	ConnectionRevision   int64
	LastUsedAt           *time.Time
	LastHealthStatus     string
	LastHealthLatencyMS  *int
	LastHealthAt         *time.Time
	LastError            string
	LastHealthRevision   int64
	LastHealthModel      string
	LastHealthProtocol   string
	LastHealthKind       string
	LastHealthDurationMS *int

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
	Status             string
	LatencyMS          *int
	At                 time.Time
	Error              string
	ConnectionRevision int64
	Model              string
	Protocol           string
	Kind               string
	DurationMS         *int
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

// maxChannelModels 限制一次创建渠道时自动生成的模型映射数量。
const maxChannelModels = 200

// CreateWithModels 在同一事务中创建渠道，并为每个模型创建一条
// client_model = upstream_model 的启用映射。
//
// 模型名会 trim、去重、跳过空项，并复用 ModelRouteInput 的校验规则；任一
// 映射写入失败都会回滚渠道，避免留下“渠道存在但模型只建了一半”的状态。
func (r *Channels) CreateWithModels(
	ctx context.Context,
	in ChannelInput,
	models []string,
) (Channel, []ModelRoute, error) {
	if r == nil || r.db == nil {
		return Channel{}, nil, errors.New("store: 渠道仓储未初始化")
	}
	config, err := normalizeChannelConfig(in.ChannelConfig)
	if err != nil {
		return Channel{}, nil, err
	}
	if strings.TrimSpace(in.KeyCiphertext) == "" || strings.TrimSpace(in.KeyMask) == "" {
		return Channel{}, nil, invalidChannel("key_ciphertext 与 key_mask 不能为空")
	}
	normalizedModels, err := normalizeChannelModelNames(models)
	if err != nil {
		return Channel{}, nil, err
	}

	tx, err := beginChannelTx(ctx, r.db)
	if err != nil {
		return Channel{}, nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	now := formatChannelTime(time.Now())
	row := tx.QueryRowContext(ctx, `
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
		return Channel{}, nil, wrapChannelWriteError("创建渠道失败", err)
	}

	routes := make([]ModelRoute, 0, len(normalizedModels))
	for _, model := range normalizedModels {
		route, insertErr := insertModelRoute(ctx, tx, ModelRouteInput{
			ClientModel:   model,
			ChannelID:     channel.ID,
			UpstreamModel: model,
			Priority:      0,
			Enabled:       true,
		}, now)
		if insertErr != nil {
			return Channel{}, nil, insertErr
		}
		routes = append(routes, route)
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, nil, fmt.Errorf("store: 提交渠道创建事务失败：%w", err)
	}
	return channel, routes, nil
}

// beginChannelTx 通过类型断言获取事务能力，避免给 ChannelDB 测试替身增加
// 不必要的方法；真实仓储传入的是 *sql.DB。
func beginChannelTx(ctx context.Context, db ChannelDB) (*sql.Tx, error) {
	beginner, ok := db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return nil, errors.New("store: 渠道仓储不支持事务")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: 开启渠道创建事务失败：%w", err)
	}
	return tx, nil
}

// normalizeChannelModelNames 校验并去重一次创建的模型名。
func normalizeChannelModelNames(models []string) ([]string, error) {
	normalized := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		input, err := normalizeModelRouteInput(ModelRouteInput{
			ClientModel:   model,
			ChannelID:     1,
			UpstreamModel: model,
			Priority:      0,
			Enabled:       true,
		})
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[input.ClientModel]; duplicate {
			continue
		}
		seen[input.ClientModel] = struct{}{}
		normalized = append(normalized, input.ClientModel)
	}
	if len(normalized) > maxChannelModels {
		return nil, invalidModelRoute(fmt.Sprintf("一次最多创建 %d 个模型", maxChannelModels))
	}
	return normalized, nil
}

// ValidateChannelKeys 只读验证全部渠道密文，包括停用渠道；不泄漏解密细节。
func (s *Store) ValidateChannelKeys(ctx context.Context, decrypt func(string) (string, error)) error {
	invalid := errors.New("store: 渠道密钥校验失败")
	if s == nil || s.db == nil || decrypt == nil {
		return invalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key_ciphertext FROM channels`)
	if err != nil {
		return invalid
	}
	defer rows.Close()
	for rows.Next() {
		var ciphertext string
		if err := rows.Scan(&ciphertext); err != nil {
			return invalid
		}
		plaintext, err := decrypt(ciphertext)
		if err != nil || strings.TrimSpace(plaintext) == "" {
			return invalid
		}
	}
	if rows.Err() != nil {
		return invalid
	}
	return nil
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

	var ciphertext, mask any
	if in.KeyCiphertext != nil {
		if strings.TrimSpace(*in.KeyCiphertext) == "" || strings.TrimSpace(*in.KeyMask) == "" {
			return Channel{}, invalidChannel("key_ciphertext 与 key_mask 不能为空")
		}
		ciphertext, mask = *in.KeyCiphertext, *in.KeyMask
	}
	// 所有赋值读取同一行的旧值，连接变化与检查结果失效在一次 UPDATE 中完成。
	row := r.db.QueryRowContext(ctx, `
		WITH incoming(name, base_url, protocol, priority, max_concurrency, enabled, key_ciphertext, key_mask, updated_at) AS
			(VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?))
		UPDATE channels SET
			connection_revision = connection_revision + CASE WHEN
				base_url != (SELECT base_url FROM incoming) OR protocol != (SELECT protocol FROM incoming)
				OR (SELECT key_ciphertext FROM incoming) IS NOT NULL THEN 1 ELSE 0 END,
			last_health_status = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_status END,
			last_health_latency_ms = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_latency_ms END,
			last_health_at = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_at END,
			last_error = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_error END,
			last_health_revision = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_revision ELSE 0 END,
			last_health_model = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_model ELSE '' END,
			last_health_protocol = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_protocol ELSE '' END,
			last_health_kind = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_kind ELSE '' END,
			last_health_duration_ms = CASE WHEN base_url = (SELECT base_url FROM incoming) AND protocol = (SELECT protocol FROM incoming) AND (SELECT key_ciphertext FROM incoming) IS NULL THEN last_health_duration_ms END,
			name = (SELECT name FROM incoming), base_url = (SELECT base_url FROM incoming),
			protocol = (SELECT protocol FROM incoming), priority = (SELECT priority FROM incoming),
			max_concurrency = (SELECT max_concurrency FROM incoming), enabled = (SELECT enabled FROM incoming),
			key_ciphertext = COALESCE((SELECT key_ciphertext FROM incoming), key_ciphertext),
			key_mask = COALESCE((SELECT key_mask FROM incoming), key_mask), updated_at = (SELECT updated_at FROM incoming)
		WHERE id = ? RETURNING `+channelColumns,
		config.Name, config.BaseURL, config.Protocol, config.Priority, config.MaxConcurrency,
		boolToInt(config.Enabled), ciphertext, mask, formatChannelTime(time.Now()), id,
	)

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
	result, err := r.db.ExecContext(ctx, `
		UPDATE channels SET
			last_health_status = ?, last_health_latency_ms = ?, last_health_at = ?, last_error = ?,
			last_health_revision = ?, last_health_model = ?, last_health_protocol = ?,
			last_health_kind = ?, last_health_duration_ms = ?
		WHERE id = ? AND connection_revision = ?`,
		strings.TrimSpace(health.Status), health.LatencyMS, formatChannelTime(health.At), health.Error,
		health.ConnectionRevision, health.Model, health.Protocol, health.Kind, health.DurationMS,
		id, health.ConnectionRevision,
	)
	if err != nil {
		return fmt.Errorf("store: 更新渠道健康状态失败：%w", err)
	}
	if err := ensureChannelAffected(result); !errors.Is(err, ErrChannelNotFound) {
		return err
	}
	// 旧代次探针晚到不属于写入失败；仍保留不存在渠道的错误契约。
	var exists int
	err = r.db.QueryRowContext(ctx, `SELECT 1 FROM channels WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrChannelNotFound
	}
	if err != nil {
		return fmt.Errorf("store: 检查渠道健康状态失败：%w", err)
	}
	return nil
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
		duration         sql.NullInt64
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
		&channel.ConnectionRevision,
		&channel.LastHealthRevision,
		&channel.LastHealthModel,
		&channel.LastHealthProtocol,
		&channel.LastHealthKind,
		&duration,
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
	if duration.Valid {
		value := int(duration.Int64)
		channel.LastHealthDurationMS = &value
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
