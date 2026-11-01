package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"ai-router/internal/secret"
	"ai-router/internal/upstream"
)

// 模型发现端点。
//
// 只做“读取上游模型列表”，不自动落库：创建渠道时由前端选中后随渠道一起
// 提交，编辑渠道时由前端做增量添加。这样管理员始终能看到并确认最终模型集合。
func init() {
	registerExtraRoutes((*API).registerModelDiscoveryRoutes)
}

func (a *API) registerModelDiscoveryRoutes() {
	a.mux.Handle("POST /admin/api/channels/preview-models",
		a.requireSession(http.HandlerFunc(a.handlePreviewChannelModels)))
	a.mux.Handle("POST /admin/api/channels/{id}/models/sync",
		a.requireSession(http.HandlerFunc(a.handleSyncChannelModels)))
}

type previewChannelModelsRequest struct {
	BaseURL  string `json:"base_url"`
	Protocol string `json:"protocol"`
	Key      string `json:"key"`
}

type channelModelsResponse struct {
	Models []string `json:"models"`
}

// handlePreviewChannelModels 用尚未保存的 Base URL/Key 拉取模型列表。
func (a *API) handlePreviewChannelModels(w http.ResponseWriter, r *http.Request) {
	var req previewChannelModelsRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeError(w, http.StatusBadRequest, "invalid_key", "上游 Key 不能为空")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.modelDiscoveryTimeout)
	defer cancel()
	models, err := a.models.ListModels(ctx, upstream.Target{
		BaseURL:  req.BaseURL,
		Protocol: req.Protocol,
		APIKey:   req.Key,
	})
	if err != nil {
		a.writeModelDiscoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelModelsResponse{Models: models})
}

// handleSyncChannelModels 使用已保存渠道的解密 Key 拉取模型列表。
func (a *API) handleSyncChannelModels(w http.ResponseWriter, r *http.Request) {
	id, ok := channelIDFromRequest(w, r)
	if !ok {
		return
	}
	repo, envelope, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	channel, err := repo.Get(r.Context(), id)
	if err != nil {
		a.writeChannelError(w, err)
		return
	}
	key, err := envelope.DecryptString(channel.KeyCiphertext, secret.PurposeChannelKey)
	if err != nil || strings.TrimSpace(key) == "" {
		a.logger.Error("渠道 Key 解密失败，无法同步模型", "channel_id", channel.ID)
		writeError(w, http.StatusInternalServerError, "channel_key_unavailable",
			"渠道 Key 暂不可用，请重新保存 Key 后再试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.modelDiscoveryTimeout)
	defer cancel()
	models, err := a.models.ListModels(ctx, upstream.Target{
		ConnectionRevision: channel.ConnectionRevision,
		ChannelID:          channel.ID,
		ChannelName:        channel.Name,
		BaseURL:            channel.BaseURL,
		Protocol:           channel.Protocol,
		APIKey:             key,
	})
	if err != nil {
		a.writeModelDiscoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelModelsResponse{Models: models})
}

func (a *API) writeModelDiscoveryError(w http.ResponseWriter, err error) {
	var statusErr *upstream.ModelsHTTPError
	switch {
	case errors.Is(err, upstream.ErrUnsupportedProtocol):
		writeError(w, http.StatusBadRequest, "unsupported_protocol",
			"该协议暂不支持自动获取模型，请手动添加。")
	case errors.Is(err, upstream.ErrInvalidBaseURL):
		writeError(w, http.StatusBadRequest, "invalid_channel_config",
			"Base URL 无效，请检查后重试。")
	case errors.Is(err, upstream.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_channel_config",
			"模型发现请求无效，请检查 Base URL、协议和 Key。")
	case errors.As(err, &statusErr):
		switch statusErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeError(w, http.StatusBadGateway, "upstream_auth_failed",
				"上游拒绝了模型列表请求，请检查 Key 与协议。")
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			writeError(w, http.StatusBadGateway, "model_discovery_failed",
				"上游没有可用的 /models 端点，请手动添加模型。")
		case http.StatusTooManyRequests:
			writeError(w, http.StatusBadGateway, "upstream_rate_limited",
				"上游限流，暂时无法获取模型列表。")
		default:
			writeError(w, http.StatusBadGateway, "model_discovery_failed",
				"上游返回错误，无法获取模型列表。")
		}
	case errors.Is(err, upstream.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "upstream_timeout",
			"获取模型列表超时，请稍后重试。")
	default:
		writeError(w, http.StatusBadGateway, "model_discovery_failed",
			"无法从上游获取模型列表，请检查 Base URL、协议和 Key。")
	}
}
