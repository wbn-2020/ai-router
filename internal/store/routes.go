package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// model_routes 仓储负责“客户端模型 → 渠道 + 上游模型”的手工映射。
//
// 约束（见计划 T09）：
//   - 同一 client_model + channel_id 只能有一行（数据库 UNIQUE 兜底）；
//   - 只有启用映射、启用渠道且渠道协议与入站协议匹配时才进入候选；
//   - 候选顺序固定为 route.priority → channel.priority → last_used_at → id；
//   - 删除渠道由 FK ON DELETE CASCADE 级联删除映射。
//
// 仓储不接触上游 Key，也不做协议转换；所有 SQL 均为参数化查询。
const modelRouteColumns = `id, client_model, channel_id, upstream_model,
	priority, enabled, created_at, updated_at`

const (
	modelRoutePriorityMin = 0
	modelRoutePriorityMax = 1000
)

var (
	// ErrModelRouteNotFound 表示模型映射不存在。
	ErrModelRouteNotFound = errors.New("store: 模型映射不存在")
	// ErrModelRouteConflict 表示同一客户端模型与渠道的映射已存在。
	ErrModelRouteConflict = errors.New("store: 同一客户端模型与渠道的映射已存在")
	// ErrModelRouteChannelNotFound 表示映射引用的渠道不存在。
	ErrModelRouteChannelNotFound = errors.New("store: 映射引用的渠道不存在")
	// ErrInvalidModelRoute 表示模型映射配置未通过校验。
	ErrInvalidModelRoute = errors.New("store: 模型映射配置无效")
)

// ModelRouteValidationError 携带可直接展示给管理员的校验原因。
//
// 原因只描述规则，不回显调用方输入，避免错误响应回显模型名或渠道参数。
type ModelRouteValidationError struct {
	Reason string
}

func (e *ModelRouteValidationError) Error() string {
	return "store: 模型映射配置无效：" + e.Reason
}

// Is 让 errors.Is(err, ErrInvalidModelRoute) 对校验错误成立。
func (e *ModelRouteValidationError) Is(target error) bool {
	return target == ErrInvalidModelRoute
}

// ModelRouteDB 是模型映射仓储需要的数据库操作集合；*sql.DB 与 adminapi
// 使用的 adminauth.DB 都满足该接口。
type ModelRouteDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ModelRoute 是一条模型映射的完整持久化视图。
type ModelRoute struct {
	ID            int64
	ClientModel   string
	ChannelID     int64
	UpstreamModel string
	Priority      int
	Enabled       bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ModelRouteInput 是创建或全量更新模型映射的输入。
type ModelRouteInput struct {
	ClientModel   string
	ChannelID     int64
	UpstreamModel string
	Priority      int
	Enabled       bool
}

// ModelRoutes 提供 model_routes 表的 CRUD 与候选查询。
//
// ModelRoutes 可被并发使用；调用方应保证 db 已启用 foreign_keys。
type ModelRoutes struct {
	db ModelRouteDB
}

// NewModelRoutes 构造模型映射仓储。db 为 nil 时调用方会在方法内得到可读错误。
func NewModelRoutes(db ModelRouteDB) *ModelRoutes {
	return &ModelRoutes{db: db}
}

// ModelRoutes 返回绑定到当前数据库的模型映射仓储。
func (s *Store) ModelRoutes() *ModelRoutes {
	return NewModelRoutes(s.db)
}

// Create 插入模型映射并返回落库后的完整记录。
func (r *ModelRoutes) Create(ctx context.Context, in ModelRouteInput) (ModelRoute, error) {
	if r == nil || r.db == nil {
		return ModelRoute{}, errors.New("store: 模型映射仓储未初始化")
	}
	input, err := normalizeModelRouteInput(in)
	if err != nil {
		return ModelRoute{}, err
	}
	if err := r.ensureChannelExists(ctx, input.ChannelID); err != nil {
		return ModelRoute{}, err
	}

	now := formatChannelTime(time.Now())
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO model_routes (
			client_model, channel_id, upstream_model, priority, enabled,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING `+modelRouteColumns,
		input.ClientModel,
		input.ChannelID,
		input.UpstreamModel,
		input.Priority,
		boolToInt(input.Enabled),
		now,
		now,
	)
	route, err := scanModelRoute(row)
	if err != nil {
		return ModelRoute{}, wrapModelRouteWriteError("创建模型映射失败", err)
	}
	return route, nil
}

// Get 按 ID 返回模型映射；不存在时返回 ErrModelRouteNotFound。
func (r *ModelRoutes) Get(ctx context.Context, id int64) (ModelRoute, error) {
	if r == nil || r.db == nil {
		return ModelRoute{}, errors.New("store: 模型映射仓储未初始化")
	}
	row := r.db.QueryRowContext(ctx,
		`SELECT `+modelRouteColumns+` FROM model_routes WHERE id = ?`, id)
	route, err := scanModelRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelRoute{}, ErrModelRouteNotFound
	}
	if err != nil {
		return ModelRoute{}, fmt.Errorf("store: 读取模型映射失败：%w", err)
	}
	return route, nil
}

// List 返回全部映射（含禁用行），按 client_model、priority、id 排序，便于
// 管理端按模型分组展示。
func (r *ModelRoutes) List(ctx context.Context) ([]ModelRoute, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("store: 模型映射仓储未初始化")
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+modelRouteColumns+` FROM model_routes
		ORDER BY client_model ASC, priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出模型映射失败：%w", err)
	}
	defer rows.Close()
	return collectModelRoutes(rows)
}

// ListByModelAndProtocol 返回某客户端模型在指定入站协议下的候选映射。
//
// 只包含启用映射且渠道启用、渠道协议匹配的行；排序为
// route.priority ASC → channel.priority ASC → channel.last_used_at ASC
// （按真实时间比较，NULL 最先，即从未使用的渠道优先）→ route.id ASC。
// 客户端模型或协议为空白时返回空集合，不视为错误。
func (r *ModelRoutes) ListByModelAndProtocol(
	ctx context.Context,
	clientModel, protocol string,
) ([]ModelRoute, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("store: 模型映射仓储未初始化")
	}
	clientModel = strings.TrimSpace(clientModel)
	protocol = strings.TrimSpace(protocol)
	if clientModel == "" || protocol == "" {
		return make([]ModelRoute, 0), nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.client_model, r.channel_id, r.upstream_model,
			r.priority, r.enabled, r.created_at, r.updated_at
		FROM model_routes AS r
		JOIN channels AS c ON c.id = r.channel_id
		WHERE r.client_model = ?
			AND r.enabled = 1
			AND c.enabled = 1
			AND c.protocol = ?
		ORDER BY r.priority ASC, c.priority ASC,
			julianday(c.last_used_at) ASC, r.id ASC`,
		clientModel, protocol)
	if err != nil {
		return nil, fmt.Errorf("store: 查询候选模型映射失败：%w", err)
	}
	defer rows.Close()
	return collectModelRoutes(rows)
}

// ListModels 返回至少有一条启用映射的客户端模型（去重、升序）。
//
// 这里只按映射自身的 enabled 过滤：模型是否可服务还取决于渠道启用状态与
// 入站协议，由 ListByModelAndProtocol 在候选查询中处理。
func (r *ModelRoutes) ListModels(ctx context.Context) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("store: 模型映射仓储未初始化")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT client_model FROM model_routes
		WHERE enabled = 1
		ORDER BY client_model ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出客户端模型失败：%w", err)
	}
	defer rows.Close()

	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, fmt.Errorf("store: 解析客户端模型失败：%w", err)
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历客户端模型失败：%w", err)
	}
	return models, nil
}

// Update 全量更新模型映射并返回落库后的记录。
func (r *ModelRoutes) Update(ctx context.Context, id int64, in ModelRouteInput) (ModelRoute, error) {
	if r == nil || r.db == nil {
		return ModelRoute{}, errors.New("store: 模型映射仓储未初始化")
	}
	input, err := normalizeModelRouteInput(in)
	if err != nil {
		return ModelRoute{}, err
	}
	if err := r.ensureChannelExists(ctx, input.ChannelID); err != nil {
		return ModelRoute{}, err
	}

	row := r.db.QueryRowContext(ctx, `
		UPDATE model_routes SET
			client_model = ?, channel_id = ?, upstream_model = ?,
			priority = ?, enabled = ?, updated_at = ?
		WHERE id = ?
		RETURNING `+modelRouteColumns,
		input.ClientModel,
		input.ChannelID,
		input.UpstreamModel,
		input.Priority,
		boolToInt(input.Enabled),
		formatChannelTime(time.Now()),
		id,
	)
	route, err := scanModelRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelRoute{}, ErrModelRouteNotFound
	}
	if err != nil {
		return ModelRoute{}, wrapModelRouteWriteError("更新模型映射失败", err)
	}
	return route, nil
}

// Delete 删除模型映射；不存在时返回 ErrModelRouteNotFound。
func (r *ModelRoutes) Delete(ctx context.Context, id int64) error {
	if r == nil || r.db == nil {
		return errors.New("store: 模型映射仓储未初始化")
	}
	var deletedID int64
	err := r.db.QueryRowContext(ctx,
		`DELETE FROM model_routes WHERE id = ? RETURNING id`, id).Scan(&deletedID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrModelRouteNotFound
	}
	if err != nil {
		return fmt.Errorf("store: 删除模型映射失败：%w", err)
	}
	return nil
}

// ensureChannelExists 校验映射引用的渠道存在；并发删除产生的 FK 冲突由
// wrapModelRouteWriteError 兜底映射为同一错误。
func (r *ModelRoutes) ensureChannelExists(ctx context.Context, channelID int64) error {
	var id int64
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM channels WHERE id = ?`, channelID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrModelRouteChannelNotFound
	}
	if err != nil {
		return fmt.Errorf("store: 校验映射渠道失败：%w", err)
	}
	return nil
}

// normalizeModelRouteInput 校验并规范化模型映射配置。
func normalizeModelRouteInput(in ModelRouteInput) (ModelRouteInput, error) {
	clientModel := strings.TrimSpace(in.ClientModel)
	if clientModel == "" {
		return ModelRouteInput{}, invalidModelRoute("client_model 不能为空")
	}
	if strings.IndexFunc(clientModel, unicode.IsControl) >= 0 {
		return ModelRouteInput{}, invalidModelRoute("client_model 不能包含控制字符")
	}

	upstreamModel := strings.TrimSpace(in.UpstreamModel)
	if upstreamModel == "" {
		return ModelRouteInput{}, invalidModelRoute("upstream_model 不能为空")
	}
	if strings.IndexFunc(upstreamModel, unicode.IsControl) >= 0 {
		return ModelRouteInput{}, invalidModelRoute("upstream_model 不能包含控制字符")
	}

	if in.ChannelID <= 0 {
		return ModelRouteInput{}, invalidModelRoute("channel_id 必须为正整数")
	}
	if in.Priority < modelRoutePriorityMin || in.Priority > modelRoutePriorityMax {
		return ModelRouteInput{}, invalidModelRoute("priority 必须为 0-1000")
	}

	in.ClientModel = clientModel
	in.UpstreamModel = upstreamModel
	return in, nil
}

func invalidModelRoute(reason string) error {
	return &ModelRouteValidationError{Reason: reason}
}

// wrapModelRouteWriteError 把 SQLite 唯一/外键冲突映射为稳定错误；其余错误
// 保留底层原因，错误信息不含调用方输入。
func wrapModelRouteWriteError(action string, err error) error {
	if isModelRouteConflict(err) {
		return ErrModelRouteConflict
	}
	if isModelRouteForeignKeyViolation(err) {
		return ErrModelRouteChannelNotFound
	}
	return fmt.Errorf("store: %s：%w", action, err)
}

func isModelRouteConflict(err error) bool {
	if err == nil {
		return false
	}
	const columns = "model_routes.client_model, model_routes.channel_id"
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		if code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT {
			return strings.Contains(sqliteErr.Error(), "model_routes.client_model") &&
				strings.Contains(sqliteErr.Error(), "model_routes.channel_id")
		}
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed: "+columns)
}

func isModelRouteForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		if sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
			return true
		}
	}
	return strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

type modelRouteScanner interface {
	Scan(dest ...any) error
}

func scanModelRoute(scanner modelRouteScanner) (ModelRoute, error) {
	var (
		route     ModelRoute
		enabled   int
		createdAt string
		updatedAt string
	)
	if err := scanner.Scan(
		&route.ID,
		&route.ClientModel,
		&route.ChannelID,
		&route.UpstreamModel,
		&route.Priority,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ModelRoute{}, err
	}
	route.Enabled = enabled != 0

	created, err := time.Parse(channelTimeLayout, createdAt)
	if err != nil {
		return ModelRoute{}, fmt.Errorf("解析 created_at 失败：%w", err)
	}
	updated, err := time.Parse(channelTimeLayout, updatedAt)
	if err != nil {
		return ModelRoute{}, fmt.Errorf("解析 updated_at 失败：%w", err)
	}
	route.CreatedAt = created
	route.UpdatedAt = updated
	return route, nil
}

func collectModelRoutes(rows *sql.Rows) ([]ModelRoute, error) {
	routes := make([]ModelRoute, 0)
	for rows.Next() {
		route, err := scanModelRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 解析模型映射记录失败：%w", err)
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历模型映射失败：%w", err)
	}
	return routes, nil
}
