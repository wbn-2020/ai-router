package adminapi

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/secret"
	"ai-router/internal/store"
)

// T07 渠道管理端点。
//
// 安全约束：
//   - 列表与详情响应只包含 key_mask，永不返回明文或密文；
//   - 上游 Key 只在创建/更新请求体中接收一次，立即用 AES-256-GCM 加密；
//   - 更新时未提供 key 字段则保留原密文；
//   - 校验错误只描述规则，不回显 base URL 查询串或 Key。
func init() {
	registerExtraRoutes((*API).registerChannelRoutes)
}

func (a *API) registerChannelRoutes() {
	a.mux.Handle("GET /admin/api/channels",
		a.requireSession(http.HandlerFunc(a.handleListChannels)))
	a.mux.Handle("POST /admin/api/channels",
		a.requireSession(http.HandlerFunc(a.handleCreateChannel)))
	a.mux.Handle("GET /admin/api/channels/{id}",
		a.requireSession(http.HandlerFunc(a.handleGetChannel)))
	a.mux.Handle("PATCH /admin/api/channels/{id}",
		a.requireSession(http.HandlerFunc(a.handleUpdateChannel)))
	a.mux.Handle("DELETE /admin/api/channels/{id}",
		a.requireSession(http.HandlerFunc(a.handleDeleteChannel)))
}

// createChannelRequest 是创建渠道的请求体。
//
// 指针字段用于区分“未提供”与显式零值：priority 省略为 0，
// max_concurrency 省略为 1，enabled 省略为 true。
type createChannelRequest struct {
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	Key            string `json:"key"`
	Protocol       string `json:"protocol"`
	Priority       *int   `json:"priority"`
	MaxConcurrency *int   `json:"max_concurrency"`
	Enabled        *bool  `json:"enabled"`
}

// updateChannelRequest 是更新渠道的请求体；省略字段保持原值。
type updateChannelRequest struct {
	Name           *string `json:"name"`
	BaseURL        *string `json:"base_url"`
	Key            *string `json:"key"`
	Protocol       *string `json:"protocol"`
	Priority       *int    `json:"priority"`
	MaxConcurrency *int    `json:"max_concurrency"`
	Enabled        *bool   `json:"enabled"`
}

// channelView 是管理端可见的渠道视图：不包含 key 或 key_ciphertext。
type channelView struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	BaseURL             string     `json:"base_url"`
	Protocol            string     `json:"protocol"`
	KeyMask             string     `json:"key_mask"`
	Priority            int        `json:"priority"`
	MaxConcurrency      int        `json:"max_concurrency"`
	Enabled             bool       `json:"enabled"`
	LastUsedAt          *time.Time `json:"last_used_at,omitempty"`
	LastHealthStatus    string     `json:"last_health_status,omitempty"`
	LastHealthLatencyMS *int       `json:"last_health_latency_ms,omitempty"`
	LastHealthAt        *time.Time `json:"last_health_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type channelListResponse struct {
	Channels []channelView `json:"channels"`
}

func newChannelView(channel store.Channel) channelView {
	return channelView{
		ID:                  channel.ID,
		Name:                channel.Name,
		BaseURL:             channel.BaseURL,
		Protocol:            channel.Protocol,
		KeyMask:             channel.KeyMask,
		Priority:            channel.Priority,
		MaxConcurrency:      channel.MaxConcurrency,
		Enabled:             channel.Enabled,
		LastUsedAt:          utcTime(channel.LastUsedAt),
		LastHealthStatus:    channel.LastHealthStatus,
		LastHealthLatencyMS: channel.LastHealthLatencyMS,
		LastHealthAt:        utcTime(channel.LastHealthAt),
		LastError:           channel.LastError,
		CreatedAt:           channel.CreatedAt.UTC(),
		UpdatedAt:           channel.UpdatedAt.UTC(),
	}
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func (a *API) handleListChannels(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	channels, err := repo.List(r.Context())
	if err != nil {
		a.writeChannelError(w, err)
		return
	}
	views := make([]channelView, 0, len(channels))
	for _, channel := range channels {
		views = append(views, newChannelView(channel))
	}
	writeJSON(w, http.StatusOK, channelListResponse{Channels: views})
}

func (a *API) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := channelIDFromRequest(w, r)
	if !ok {
		return
	}
	repo, _, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	channel, err := repo.Get(r.Context(), id)
	if err != nil {
		a.writeChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newChannelView(channel))
}

func (a *API) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := validateNewChannelKey(req.Key); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_key", "上游 Key 不能为空")
		return
	}

	repo, envelope, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	ciphertext, mask, err := encryptChannelKey(envelope, req.Key)
	if err != nil {
		a.writeChannelKeyError(w, err)
		return
	}

	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
	}
	maxConcurrency := 1
	if req.MaxConcurrency != nil {
		maxConcurrency = *req.MaxConcurrency
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	channel, err := repo.Create(r.Context(), store.ChannelInput{
		ChannelConfig: store.ChannelConfig{
			Name:           req.Name,
			BaseURL:        req.BaseURL,
			Protocol:       req.Protocol,
			Priority:       priority,
			MaxConcurrency: maxConcurrency,
			Enabled:        enabled,
		},
		KeyCiphertext: ciphertext,
		KeyMask:       mask,
	})
	if err != nil {
		a.writeChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newChannelView(channel))
}

func (a *API) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := channelIDFromRequest(w, r)
	if !ok {
		return
	}
	var req updateChannelRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}

	repo, envelope, ok := a.channelDependencies(w)
	if !ok {
		return
	}

	// 先取现状再合并，保证 PATCH 的省略字段保持原值；key 字段缺失时
	// 完全不触碰 key_ciphertext/key_mask。
	existing, err := repo.Get(r.Context(), id)
	if err != nil {
		a.writeChannelError(w, err)
		return
	}
	config := store.ChannelConfig{
		Name:           existing.Name,
		BaseURL:        existing.BaseURL,
		Protocol:       existing.Protocol,
		Priority:       existing.Priority,
		MaxConcurrency: existing.MaxConcurrency,
		Enabled:        existing.Enabled,
	}
	if req.Name != nil {
		config.Name = *req.Name
	}
	if req.BaseURL != nil {
		config.BaseURL = *req.BaseURL
	}
	if req.Protocol != nil {
		config.Protocol = *req.Protocol
	}
	if req.Priority != nil {
		config.Priority = *req.Priority
	}
	if req.MaxConcurrency != nil {
		config.MaxConcurrency = *req.MaxConcurrency
	}
	if req.Enabled != nil {
		config.Enabled = *req.Enabled
	}

	update := store.ChannelUpdate{ChannelConfig: config}
	if req.Key != nil {
		if err := validateNewChannelKey(*req.Key); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_key", "上游 Key 不能为空")
			return
		}
		ciphertext, mask, err := encryptChannelKey(envelope, *req.Key)
		if err != nil {
			a.writeChannelKeyError(w, err)
			return
		}
		update.KeyCiphertext = &ciphertext
		update.KeyMask = &mask
	}

	channel, err := repo.Update(r.Context(), id, update)
	if err != nil {
		a.writeChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newChannelView(channel))
}

func (a *API) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := channelIDFromRequest(w, r)
	if !ok {
		return
	}
	repo, _, ok := a.channelDependencies(w)
	if !ok {
		return
	}
	if err := repo.Delete(r.Context(), id); err != nil {
		a.writeChannelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// channelDependencies 从管理 API 已持有的数据库连接构造渠道仓储，并解析
// GATEWAY_MASTER_KEY。
//
// 主密钥由内部 config 层在启动时校验；这里复用同一环境变量，避免在
// adminapi.Options 中额外传递秘密值。未配置主密钥时渠道端点返回 503，
// /readyz 同时会报告 master_key_not_loaded。
func (a *API) channelDependencies(w http.ResponseWriter) (*store.Channels, *secret.Envelope, bool) {
	if a.db == nil {
		writeError(w, http.StatusServiceUnavailable, "channels_unavailable",
			"渠道管理暂不可用：数据库或主密钥未就绪")
		return nil, nil, false
	}
	db, ok := a.db.(store.ChannelDB)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "channels_unavailable",
			"渠道管理暂不可用：数据库或主密钥未就绪")
		return nil, nil, false
	}
	envelope, err := channelMasterEnvelope()
	if err != nil {
		a.logger.Error("渠道管理需要主密钥，但 GATEWAY_MASTER_KEY 不可用")
		writeError(w, http.StatusServiceUnavailable, "channels_unavailable",
			"渠道管理暂不可用：数据库或主密钥未就绪")
		return nil, nil, false
	}
	return store.NewChannels(db), envelope, true
}

// channelMasterEnvelope 从进程环境解析主密钥并构造加密信封。
//
// 返回值错误不包含密钥内容；所有 secret 错误都是稳定分类错误。
func channelMasterEnvelope() (*secret.Envelope, error) {
	masterKey, err := secret.ParseMasterKey(os.Getenv(config.EnvMasterKey))
	if err != nil {
		return nil, err
	}
	return secret.NewEnvelope(masterKey)
}

// encryptChannelKey 加密上游 Key 并生成脱敏展示值。
func encryptChannelKey(envelope *secret.Envelope, key string) (string, string, error) {
	ciphertext, err := envelope.EncryptString(key, secret.PurposeChannelKey)
	if err != nil {
		return "", "", err
	}
	return ciphertext, secret.MaskKey(key), nil
}

func validateNewChannelKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("empty key")
	}
	return nil
}

func (a *API) writeChannelKeyError(w http.ResponseWriter, err error) {
	a.logger.Error("渠道上游 Key 加密失败", "error", secret.RedactValue(err.Error()))
	writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
}

// writeChannelError 把仓储错误映射为稳定 HTTP 状态码。
func (a *API) writeChannelError(w http.ResponseWriter, err error) {
	var validation *store.ChannelValidationError
	switch {
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, "invalid_channel_config", validation.Reason)
	case errors.Is(err, store.ErrInvalidChannel):
		writeError(w, http.StatusBadRequest, "invalid_channel_config", "渠道配置无效")
	case errors.Is(err, store.ErrChannelNameConflict):
		writeError(w, http.StatusConflict, "channel_name_conflict", "渠道名称已存在")
	case errors.Is(err, store.ErrChannelNotFound):
		writeError(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
	default:
		a.logger.Error("渠道操作失败", "error", secret.RedactValue(err.Error()))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

func channelIDFromRequest(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_channel_id", "渠道 ID 无效")
		return 0, false
	}
	return id, true
}
