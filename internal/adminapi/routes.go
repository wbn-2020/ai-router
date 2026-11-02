package adminapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"ai-router/internal/secret"
	"ai-router/internal/store"
)

// T09 模型映射管理端点。
//
// 约束：
//   - 渠道创建会自动生成同名映射；本组端点负责别名、多渠道分流与手工补充；
//   - 同一 client_model + channel_id 唯一，冲突返回 409；
//   - channel_id 必须指向已存在的渠道，priority 0..1000，模型名不得为空；
//   - /admin/api/models 只从启用映射去重得到客户端模型列表。
func init() {
	registerExtraRoutes((*API).registerModelRouteRoutes)
}

func (a *API) registerModelRouteRoutes() {
	a.mux.Handle("GET /admin/api/model-routes",
		a.requireSession(http.HandlerFunc(a.handleListModelRoutes)))
	a.mux.Handle("POST /admin/api/model-routes",
		a.requireSession(http.HandlerFunc(a.handleCreateModelRoute)))
	a.mux.Handle("GET /admin/api/model-routes/{id}",
		a.requireSession(http.HandlerFunc(a.handleGetModelRoute)))
	a.mux.Handle("PATCH /admin/api/model-routes/{id}",
		a.requireSession(http.HandlerFunc(a.handleUpdateModelRoute)))
	a.mux.Handle("DELETE /admin/api/model-routes/{id}",
		a.requireSession(http.HandlerFunc(a.handleDeleteModelRoute)))
	a.mux.Handle("GET /admin/api/models",
		a.requireSession(http.HandlerFunc(a.handleListModels)))
}

// createModelRouteRequest 是创建映射的请求体。
//
// priority 省略为 0，enabled 省略为 true。
type createModelRouteRequest struct {
	ClientModel     string   `json:"client_model"`
	ChannelID       int64    `json:"channel_id"`
	UpstreamModel   string   `json:"upstream_model"`
	Priority        *int     `json:"priority"`
	Enabled         *bool    `json:"enabled"`
	InputModalities string   `json:"input_modalities"`
	ContextWindow   *int64   `json:"context_window"`
	ReasoningLevels []string `json:"reasoning_levels"`
}

// updateModelRouteRequest 是更新映射的请求体；省略字段保持原值。
//
// input_modalities/context_window/reasoning_levels 用指针区分“省略”与
// “显式清空”：context_window=0 与 reasoning_levels=[] 都是清空声明。
type updateModelRouteRequest struct {
	ClientModel     *string   `json:"client_model"`
	ChannelID       *int64    `json:"channel_id"`
	UpstreamModel   *string   `json:"upstream_model"`
	Priority        *int      `json:"priority"`
	Enabled         *bool     `json:"enabled"`
	InputModalities *string   `json:"input_modalities"`
	ContextWindow   *int64    `json:"context_window"`
	ReasoningLevels *[]string `json:"reasoning_levels"`
}

// modelRouteView 是管理端可见的映射视图；不包含任何上游凭据。
type modelRouteView struct {
	ID              int64     `json:"id"`
	ClientModel     string    `json:"client_model"`
	ChannelID       int64     `json:"channel_id"`
	UpstreamModel   string    `json:"upstream_model"`
	Priority        int       `json:"priority"`
	Enabled         bool      `json:"enabled"`
	InputModalities string    `json:"input_modalities"`
	ContextWindow   *int64    `json:"context_window"`
	ReasoningLevels []string  `json:"reasoning_levels"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type modelRouteListResponse struct {
	ModelRoutes []modelRouteView `json:"model_routes"`
}

// modelsResponse 是客户端模型列表响应；models 始终为数组，空集合为 []。
type modelsResponse struct {
	Models []string `json:"models"`
}

func newModelRouteView(route store.ModelRoute) modelRouteView {
	levels := route.ReasoningLevels
	if levels == nil {
		levels = []string{}
	}
	return modelRouteView{
		ID:              route.ID,
		ClientModel:     route.ClientModel,
		ChannelID:       route.ChannelID,
		UpstreamModel:   route.UpstreamModel,
		Priority:        route.Priority,
		Enabled:         route.Enabled,
		InputModalities: route.InputModalities,
		ContextWindow:   route.ContextWindow,
		ReasoningLevels: levels,
		CreatedAt:       route.CreatedAt.UTC(),
		UpdatedAt:       route.UpdatedAt.UTC(),
	}
}

func (a *API) handleListModelRoutes(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.modelRouteDependencies(w)
	if !ok {
		return
	}
	routes, err := repo.List(r.Context())
	if err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	views := make([]modelRouteView, 0, len(routes))
	for _, route := range routes {
		views = append(views, newModelRouteView(route))
	}
	writeJSON(w, http.StatusOK, modelRouteListResponse{ModelRoutes: views})
}

func (a *API) handleGetModelRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := modelRouteIDFromRequest(w, r)
	if !ok {
		return
	}
	repo, ok := a.modelRouteDependencies(w)
	if !ok {
		return
	}
	route, err := repo.Get(r.Context(), id)
	if err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newModelRouteView(route))
}

func (a *API) handleCreateModelRoute(w http.ResponseWriter, r *http.Request) {
	var req createModelRouteRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	repo, ok := a.modelRouteDependencies(w)
	if !ok {
		return
	}

	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	route, err := repo.Create(r.Context(), store.ModelRouteInput{
		ClientModel:     req.ClientModel,
		ChannelID:       req.ChannelID,
		UpstreamModel:   req.UpstreamModel,
		Priority:        priority,
		Enabled:         enabled,
		InputModalities: req.InputModalities,
		ContextWindow:   contextWindowValue(req.ContextWindow),
		ReasoningLevels: req.ReasoningLevels,
	})
	if err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newModelRouteView(route))
}

func (a *API) handleUpdateModelRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := modelRouteIDFromRequest(w, r)
	if !ok {
		return
	}
	var req updateModelRouteRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	repo, ok := a.modelRouteDependencies(w)
	if !ok {
		return
	}

	// 先取现状再合并，保证 PATCH 的省略字段保持原值。
	existing, err := repo.Get(r.Context(), id)
	if err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	input := store.ModelRouteInput{
		ClientModel:     existing.ClientModel,
		ChannelID:       existing.ChannelID,
		UpstreamModel:   existing.UpstreamModel,
		Priority:        existing.Priority,
		Enabled:         existing.Enabled,
		InputModalities: existing.InputModalities,
		ContextWindow:   contextWindowValue(existing.ContextWindow),
		ReasoningLevels: existing.ReasoningLevels,
	}
	if req.ClientModel != nil {
		input.ClientModel = *req.ClientModel
	}
	if req.ChannelID != nil {
		input.ChannelID = *req.ChannelID
	}
	if req.UpstreamModel != nil {
		input.UpstreamModel = *req.UpstreamModel
	}
	if req.Priority != nil {
		input.Priority = *req.Priority
	}
	if req.Enabled != nil {
		input.Enabled = *req.Enabled
	}
	if req.InputModalities != nil {
		input.InputModalities = *req.InputModalities
	}
	if req.ContextWindow != nil {
		input.ContextWindow = *req.ContextWindow
	}
	if req.ReasoningLevels != nil {
		input.ReasoningLevels = *req.ReasoningLevels
	}

	route, err := repo.Update(r.Context(), id, input)
	if err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newModelRouteView(route))
}

func contextWindowValue(window *int64) int64 {
	if window == nil {
		return 0
	}
	return *window
}

func (a *API) handleDeleteModelRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := modelRouteIDFromRequest(w, r)
	if !ok {
		return
	}
	repo, ok := a.modelRouteDependencies(w)
	if !ok {
		return
	}
	if err := repo.Delete(r.Context(), id); err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListModels(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.modelRouteDependencies(w)
	if !ok {
		return
	}
	models, err := repo.ListModels(r.Context())
	if err != nil {
		a.writeModelRouteError(w, err)
		return
	}
	if models == nil {
		models = make([]string, 0)
	}
	writeJSON(w, http.StatusOK, modelsResponse{Models: models})
}

// modelRouteDependencies 从管理 API 已持有的数据库连接构造模型映射仓储。
//
// 映射端点不需要主密钥：它们不接触上游 Key，也不返回任何凭据。
func (a *API) modelRouteDependencies(w http.ResponseWriter) (*store.ModelRoutes, bool) {
	if a.db == nil {
		writeError(w, http.StatusServiceUnavailable, "model_routes_unavailable",
			"模型映射管理暂不可用：数据库未就绪")
		return nil, false
	}
	return store.NewModelRoutes(a.db), true
}

// writeModelRouteError 把仓储错误映射为稳定 HTTP 状态码。
func (a *API) writeModelRouteError(w http.ResponseWriter, err error) {
	var validation *store.ModelRouteValidationError
	switch {
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, "invalid_model_route", validation.Reason)
	case errors.Is(err, store.ErrInvalidModelRoute):
		writeError(w, http.StatusBadRequest, "invalid_model_route", "模型映射配置无效")
	case errors.Is(err, store.ErrModelRouteChannelNotFound):
		writeError(w, http.StatusBadRequest, "invalid_model_route", "channel_id 必须指向已存在的渠道")
	case errors.Is(err, store.ErrModelRouteConflict):
		writeError(w, http.StatusConflict, "model_route_conflict",
			"同一客户端模型与渠道的映射已存在")
	case errors.Is(err, store.ErrModelRouteNotFound):
		writeError(w, http.StatusNotFound, "model_route_not_found", "模型映射不存在")
	default:
		a.logger.Error("模型映射操作失败", "error", secret.RedactValue(err.Error()))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

func modelRouteIDFromRequest(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_model_route_id", "模型映射 ID 无效")
		return 0, false
	}
	return id, true
}
