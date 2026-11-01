package gateway

import (
	"context"
	"net/http"
	"strings"

	"ai-router/internal/store"
)

const (
	// modelCreatedUnix 是模型列表条目的稳定 created 值（2024-01-01T00:00:00Z）。
	modelCreatedUnix int64 = 1704067200
	// modelOwnedBy 是网关自有模型的固定 owner；不暴露上游渠道信息。
	modelOwnedBy = "gateway"
)

// modelCard 是 OpenAI 风格 /v1/models 的单个条目。
type modelCard struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// modelListResponse 是 GET /v1/models 的普通列表响应。
type modelListResponse struct {
	Object string      `json:"object"`
	Data   []modelCard `json:"data"`
}

// codexManifestPlaceholder 是 T17 的最小 Codex manifest 外层。
//
// T26 会用完整 ModelInfo descriptor 替换；这里的唯一约束是提供顶层 models
// 数组并让 /v1/models?client_version=... 与 /backend-api/codex/models 走同一
// 分支，不冒充完整模型合同。
type codexManifestPlaceholder struct {
	Models []codexModelPlaceholder `json:"models"`
}

type codexModelPlaceholder struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// codexManifestBuilder 允许 T26 在 router.go 组装 Handler 时注入完整 manifest
// 构造器，而不需要修改本文件的普通模型列表逻辑。
type codexManifestBuilder func(models []string) any

// serveModels 处理 GET /v1/models。
//
// 带非空 client_version 时返回 Codex manifest；否则返回 OpenAI 风格列表。
func (h *Handler) serveModels(w http.ResponseWriter, r *http.Request) {
	if !h.requireClientKey(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	if strings.TrimSpace(r.URL.Query().Get("client_version")) != "" {
		h.writeCodexManifest(w, r)
		return
	}

	models, ok := h.listEnabledModels(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, modelListResponse{
		Object: "list",
		Data:   newModelCards(models),
	})
}

// serveCodexModels 处理 GET /backend-api/codex/models，始终返回 manifest。
func (h *Handler) serveCodexModels(w http.ResponseWriter, r *http.Request) {
	if !h.requireClientKey(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	h.writeCodexManifest(w, r)
}

// serveModel 处理 GET /v1/models/{model}，返回列表中的同一模型条目。
//
// 未知或未启用映射的模型返回 404 model_not_available，与数据面错误码一致。
func (h *Handler) serveModel(w http.ResponseWriter, r *http.Request) {
	if !h.requireClientKey(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	model := strings.TrimPrefix(r.URL.Path, ModelPathPrefix)
	if model == "" {
		writeNotFound(w)
		return
	}

	models, ok := h.listEnabledModels(w, r)
	if !ok {
		return
	}
	for _, candidate := range models {
		if candidate == model {
			writeJSON(w, http.StatusOK, newModelCard(model))
			return
		}
	}
	writeModelNotAvailable(w)
}

// writeCodexManifest 生成 Codex 模型清单。
//
// Codex 只使用原生 Responses 渠道；即使同一客户端模型还映射到 Chat 渠道，
// 也不能出现在这里，否则客户端会选中一个数据面无法用该协议服务的模型。
func (h *Handler) writeCodexManifest(w http.ResponseWriter, r *http.Request) {
	models, ok := h.listModelsForProtocol(w, r, store.ChannelProtocolOpenAIResponses)
	if !ok {
		return
	}
	builder := h.codexManifest
	if builder == nil {
		builder = buildCodexManifestPlaceholder
	}
	writeJSON(w, http.StatusOK, builder(models))
}

// listModelsForProtocol 优先按入站协议过滤模型；旧 RouteCatalog 实现没有
// 该能力时回退到全量列表，保持兼容。
func (h *Handler) listModelsForProtocol(
	w http.ResponseWriter,
	r *http.Request,
	protocol string,
) ([]string, bool) {
	if h.routes == nil {
		writeUnavailable(w)
		return nil, false
	}
	lister, ok := h.routes.(interface {
		ListModelsByProtocol(context.Context, string) ([]string, error)
	})
	if !ok {
		return h.listEnabledModels(w, r)
	}
	models, err := lister.ListModelsByProtocol(r.Context(), protocol)
	if err != nil {
		writeInternalError(w)
		return nil, false
	}
	if models == nil {
		models = make([]string, 0)
	}
	return models, true
}

// listEnabledModels 从启用映射读取客户端模型；空集合返回空切片而非 nil。
func (h *Handler) listEnabledModels(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if h.routes == nil {
		writeUnavailable(w)
		return nil, false
	}
	models, err := h.routes.ListModels(r.Context())
	if err != nil {
		// 仓储错误不向客户端回显。
		writeInternalError(w)
		return nil, false
	}
	if models == nil {
		models = make([]string, 0)
	}
	return models, true
}

func newModelCards(models []string) []modelCard {
	cards := make([]modelCard, 0, len(models))
	for _, model := range models {
		cards = append(cards, newModelCard(model))
	}
	return cards
}

func newModelCard(model string) modelCard {
	return modelCard{
		ID:      model,
		Object:  "model",
		Created: modelCreatedUnix,
		OwnedBy: modelOwnedBy,
	}
}

func buildCodexManifestPlaceholder(models []string) any {
	entries := make([]codexModelPlaceholder, 0, len(models))
	for _, model := range models {
		entries = append(entries, codexModelPlaceholder{
			Slug:        model,
			DisplayName: model,
		})
	}
	return codexManifestPlaceholder{Models: entries}
}
