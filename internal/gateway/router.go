// Package gateway 实现客户端数据面的 HTTP 入口：单客户端 Key 鉴权、入站协议
// 识别、模型可用性检查、错误 envelope 与模型发现。
//
// 本包只负责 HTTP 表面与路由决策，不直接访问 SQLite，也不做跨协议转换。
// 通过 RouteCatalog 读取启用映射，通过 Forwarder 把已鉴权且模型可用的请求
// 交给后续的代理/流式编排（T20/T21）。
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-router/internal/store"
)

// Endpoint 是入站数据面端点的稳定标识，值同时是对外 URL 路径。
type Endpoint string

const (
	// PathResponses 是 OpenAI Responses（Codex 自定义 provider）入口。
	PathResponses = "/v1/responses"
	// PathCodexResponses 是 Codex ChatGPT base URL 模式的 Responses 别名。
	PathCodexResponses = "/backend-api/codex/responses"
	// PathChatCompletions 是 OpenAI Chat Completions 入口。
	PathChatCompletions = "/v1/chat/completions"
	// PathModels 是 OpenAI 风格模型列表入口。
	PathModels = "/v1/models"
	// PathCodexModels 是 Codex manifest 入口。
	PathCodexModels = "/backend-api/codex/models"

	// ModelPathPrefix 用于匹配 /v1/models/{model}。模型名按剩余原始路径
	// 精确比较，允许包含斜杠（自定义模型名常见于聚合上游）。
	ModelPathPrefix = "/v1/models/"

	// EndpointResponses 等是交给 Forwarder 的稳定端点标识。
	EndpointResponses       Endpoint = PathResponses
	EndpointCodexResponses  Endpoint = PathCodexResponses
	EndpointChatCompletions Endpoint = PathChatCompletions
	EndpointModels          Endpoint = PathModels
	EndpointModel           Endpoint = ModelPathPrefix
	EndpointCodexModels     Endpoint = PathCodexModels
)

// 入站协议只支持渠道原生协议，网关不提供 Responses↔Chat 转换。
const (
	// ProtocolOpenAIResponses 对应 Responses 入站协议。
	ProtocolOpenAIResponses = store.ChannelProtocolOpenAIResponses
	// ProtocolOpenAIChat 对应 Chat Completions 入站协议。
	ProtocolOpenAIChat = store.ChannelProtocolOpenAIChat
)

// RouteCatalog 是网关需要的模型映射读取能力。
//
// store.ModelRoutes 满足该接口；gateway 不感知表结构。ListModels 只返回至少
// 有一条启用映射的客户端模型，ListByModelAndProtocol 只返回启用且渠道协议
// 匹配的候选。
type RouteCatalog interface {
	ListModels(ctx context.Context) ([]string, error)
	ListByModelAndProtocol(ctx context.Context, clientModel, protocol string) ([]store.ModelRoute, error)
}

var _ RouteCatalog = (*store.ModelRoutes)(nil)
var _ http.Handler = (*Handler)(nil)

// RequestInfo 是入站路由决策的结果，通过 context 传给 Forwarder。
type RequestInfo struct {
	Protocol string
	Endpoint Endpoint
	Model    string
}

type requestInfoContextKey struct{}

// RequestInfoFromContext 返回路由阶段确定的协议、端点与客户端模型。
func RequestInfoFromContext(ctx context.Context) (RequestInfo, bool) {
	if ctx == nil {
		return RequestInfo{}, false
	}
	info, ok := ctx.Value(requestInfoContextKey{}).(RequestInfo)
	return info, ok
}

// Options 是数据面 Handler 的构造参数。
type Options struct {
	// ClientKey 是唯一的客户端 API Key。New 只保存其 SHA-256 摘要。
	ClientKey string
	// Routes 提供启用模型映射；为 nil 时所有需要路由的请求返回 503。
	Routes RouteCatalog
	// Forwarder 接收已鉴权且存在同协议候选的请求；为 nil 时返回 503。
	// T20/T21 的代理与流式编排通过该 seam 接入。
	Forwarder http.Handler
	// CodexContextWindow 覆盖 Codex manifest 的 context_window 与
	// max_context_window。<=0 时回退到 GATEWAY_CODEX_CONTEXT_WINDOW，
	// 再回退到默认值 128000。
	CodexContextWindow int
}

// Handler 是客户端数据面的 http.Handler。
//
// 构造后字段不可变，可被并发使用。Handler 期望收到完整路径（/v1/... 或
// /backend-api/codex/...），不依赖 ServeMux 的路径参数。
type Handler struct {
	auth          clientKeyAuth
	routes        RouteCatalog
	forwarder     http.Handler
	codexManifest codexManifestBuilder
}

// New 构造数据面 Handler。客户端 Key 为空时拒绝构造；错误不回显 Key。
func New(opts Options) (*Handler, error) {
	if strings.TrimSpace(opts.ClientKey) == "" {
		return nil, errors.New("gateway: 客户端 API Key 不能为空")
	}
	return &Handler{
		auth:          newClientKeyAuth(opts.ClientKey),
		routes:        opts.Routes,
		forwarder:     opts.Forwarder,
		codexManifest: newCodexManifestBuilder(opts.CodexContextWindow),
	}, nil
}

// ServeHTTP 实现路由表。
//
// 未注册的路径一律 404，包括 embeddings/images/audio/video；不做模糊匹配，
// 也不把未知路径交给管理端或 SPA。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil {
		writeNotFound(w)
		return
	}

	switch r.URL.Path {
	case PathResponses:
		h.serveData(w, r, http.MethodPost, EndpointResponses, ProtocolOpenAIResponses)
	case PathCodexResponses:
		h.serveData(w, r, http.MethodPost, EndpointCodexResponses, ProtocolOpenAIResponses)
	case PathChatCompletions:
		h.serveData(w, r, http.MethodPost, EndpointChatCompletions, ProtocolOpenAIChat)
	case PathModels:
		h.serveModels(w, r)
	case PathCodexModels:
		h.serveCodexModels(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, ModelPathPrefix) {
			h.serveModel(w, r)
			return
		}
		writeNotFound(w)
	}
}

// serveData 完成鉴权、方法校验、模型解析与同协议候选检查后交给 Forwarder。
//
// 映射不存在、映射被禁用、渠道被禁用或渠道协议不匹配都返回 404
// model_not_available，且不会调用 Forwarder（上游调用次数为 0）。
func (h *Handler) serveData(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	endpoint Endpoint,
	protocol string,
) {
	if !h.requireClientKey(w, r) {
		return
	}
	if r.Method != method {
		writeMethodNotAllowed(w, method)
		return
	}

	body, ok := readRequestBody(w, r)
	if !ok {
		return
	}
	model, ok := decodeModel(w, body)
	if !ok {
		return
	}

	if h.routes == nil {
		writeUnavailable(w)
		return
	}
	candidates, err := h.routes.ListByModelAndProtocol(r.Context(), model, protocol)
	if err != nil {
		// 数据库/仓储错误只返回固定文案，绝不把 SQL 或上游信息写回客户端。
		writeInternalError(w)
		return
	}
	if len(candidates) == 0 {
		writeModelNotAvailable(w)
		return
	}
	if h.forwarder == nil {
		writeUnavailable(w)
		return
	}

	info := RequestInfo{
		Protocol: protocol,
		Endpoint: endpoint,
		Model:    model,
	}
	ctx := context.WithValue(r.Context(), requestInfoContextKey{}, info)
	h.forwarder.ServeHTTP(w, r.WithContext(ctx))
}

func (h *Handler) requireClientKey(w http.ResponseWriter, r *http.Request) bool {
	if h.auth.authorize(r) {
		return true
	}
	writeInvalidAPIKey(w)
	return false
}

// readRequestBody 读取一次请求体并把字节放回 r.Body，确保 Forwarder 看到
// 原始 JSON（tools/input_image/未知字段不被重编码丢失）。
func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		r.Body = http.NoBody
		return []byte{}, true
	}

	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeBodyTooLarge(w)
		} else {
			writeInvalidJSON(w)
		}
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, true
}

// decodeModel 只解析 model 字段；其余字段保持原始字节交给上游。
func decodeModel(w http.ResponseWriter, body []byte) (string, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		writeMissingModel(w)
		return "", false
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeInvalidJSON(w)
		return "", false
	}

	model := strings.TrimSpace(payload.Model)
	if model == "" {
		writeMissingModel(w)
		return "", false
	}
	return model, true
}
