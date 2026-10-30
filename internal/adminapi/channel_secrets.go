package adminapi

import (
	"errors"
	"net/http"

	"ai-router/internal/secret"
	"ai-router/internal/store"
)

// T08 上游 Key 明文揭示端点。
//
// 安全约束：
//   - 必须有有效 admin session；不做二次密码（需求 R-003 明确要求点击即显示）；
//   - 响应设置 no-store，禁止浏览器与中间代理缓存完整 Key；
//   - 不写请求/审计日志；失败时只记录渠道 ID 与稳定分类错误；
//   - 响应与日志都不包含 key_ciphertext 或解密失败的底层细节。
func init() {
	registerExtraRoutes((*API).registerChannelSecretRoutes)
}

func (a *API) registerChannelSecretRoutes() {
	a.mux.Handle("GET /admin/api/channels/{id}/key",
		a.requireSession(http.HandlerFunc(a.handleRevealChannelKey)))
}

// channelKeyResponse 是揭示接口的响应体：完整明文与脱敏展示值。
type channelKeyResponse struct {
	Key  string `json:"key"`
	Mask string `json:"mask"`
}

func (a *API) handleRevealChannelKey(w http.ResponseWriter, r *http.Request) {
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
		if errors.Is(err, store.ErrChannelNotFound) {
			writeError(w, http.StatusNotFound, "channel_not_found", "渠道不存在")
			return
		}
		a.logger.Error("读取渠道失败", "channel_id", id,
			"error", secret.RedactValue(err.Error()))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
		return
	}

	plaintext, err := envelope.DecryptString(channel.KeyCiphertext, secret.PurposeChannelKey)
	if err != nil {
		// err 是稳定分类错误，不含明文或密文；日志只用于定位解密失败，
		// 不记录 key_ciphertext，避免密文进入日志系统。
		a.logger.Error("解密渠道上游 Key 失败", "channel_id", channel.ID,
			"error", secret.RedactValue(err.Error()))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
		return
	}

	// 显式设置缓存头；writeJSON 也会再次设置，避免后续改动遗漏。
	setNoStore(w.Header())
	writeJSON(w, http.StatusOK, channelKeyResponse{
		Key:  plaintext,
		Mask: channel.KeyMask,
	})
}
