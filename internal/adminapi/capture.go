// capture.go 实现 R-012 的管理端点：查看诊断环可用性、显式确认后保存
// body、读取解密内容以及删除捕获。
//
// 约束：
//   - 只有持有有效 admin session 才能访问；写操作沿用管理 API 的同源检查；
//   - 默认不保存 body；POST 必须携带 confirm_risk=true，避免误存 prompt；
//   - 保存使用与渠道 Key 相同的主密钥信封，request/response 密文分用途；
//   - status/read 响应显式设置 no-store，响应不包含 header 或上游 Key；
//   - 删除同时移除 SQLite 中的密文与内存诊断环中的副本。
package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"ai-router/internal/observability"
	"ai-router/internal/secret"
)

func init() {
	registerExtraRoutes((*API).registerCaptureRoutes)
}

// captureRingProvider 让测试注入独立的诊断环；生产中始终使用进程级实例，
// 数据面后续通过 observability.DefaultCaptureRing() 写入同一个环。
var captureRingProvider = observability.DefaultCaptureRing

func (a *API) registerCaptureRoutes() {
	a.startCaptureCleanup()

	a.mux.Handle("GET /admin/api/requests/{id}/capture/status",
		a.requireSession(http.HandlerFunc(a.handleCaptureStatus)))
	a.mux.Handle("POST /admin/api/requests/{id}/capture",
		a.requireSession(http.HandlerFunc(a.handleCaptureSave)))
	a.mux.Handle("GET /admin/api/requests/{id}/capture",
		a.requireSession(http.HandlerFunc(a.handleCaptureRead)))
	a.mux.Handle("DELETE /admin/api/requests/{id}/capture",
		a.requireSession(http.HandlerFunc(a.handleCaptureDelete)))
}

// startCaptureCleanup 启动每 10 分钟一次的过期捕获清理。
//
// 单进程部署下生命周期与进程一致；数据库未就绪时跳过，端点会返回 503。
func (a *API) startCaptureCleanup() {
	if a.db == nil {
		return
	}
	db, ok := a.db.(observability.DB)
	if !ok {
		return
	}
	cleaner := observability.NewCaptureStore(db, nil, observability.CaptureStoreOptions{})
	go cleaner.RunCleanup(context.Background(), observability.DefaultCaptureCleanupInterval,
		func(err error) {
			a.logger.Error("清理过期请求捕获失败", "error", secret.RedactValue(err.Error()))
		})
}

// captureSaveRequest 是保存捕获的请求体；必须显式确认隐私风险。
type captureSaveRequest struct {
	ConfirmRisk bool `json:"confirm_risk"`
}

// captureStatusResponse 描述诊断环与已保存捕获的可用性。
//
// RequestTruncated/RequestBytes 等字段优先来自诊断环；诊断环过期但 SQLite
// 仍有保存时，回退为已保存捕获的元数据。SavedExpiresAt 只在 saved=true 时存在。
type captureStatusResponse struct {
	RequestID string `json:"request_id"`
	Available bool   `json:"available"`
	CanSave   bool   `json:"can_save"`
	Saved     bool   `json:"saved"`

	RequestTruncated  bool   `json:"request_truncated"`
	ResponseTruncated bool   `json:"response_truncated"`
	RequestBytes      *int64 `json:"request_bytes"`
	ResponseBytes     *int64 `json:"response_bytes"`

	CapturedAt     *time.Time `json:"captured_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	SavedExpiresAt *time.Time `json:"saved_expires_at,omitempty"`
}

// captureSaveResponse 是保存成功后的元数据；不回显 body 明文。
type captureSaveResponse struct {
	RequestID string `json:"request_id"`
	Saved     bool   `json:"saved"`

	RequestTruncated  bool   `json:"request_truncated"`
	ResponseTruncated bool   `json:"response_truncated"`
	RequestBytes      *int64 `json:"request_bytes"`
	ResponseBytes     *int64 `json:"response_bytes"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// captureReadResponse 是解密后的捕获内容；body 以纯文本返回，前端不得
// 使用 dangerouslySetInnerHTML 渲染。
type captureReadResponse struct {
	RequestID string `json:"request_id"`

	RequestBody  string `json:"request_body"`
	ResponseBody string `json:"response_body"`

	RequestTruncated  bool   `json:"request_truncated"`
	ResponseTruncated bool   `json:"response_truncated"`
	RequestBytes      *int64 `json:"request_bytes"`
	ResponseBytes     *int64 `json:"response_bytes"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *API) handleCaptureStatus(w http.ResponseWriter, r *http.Request) {
	requestID, ok := captureRequestID(w, r)
	if !ok {
		return
	}
	store, ok := a.captureDependencies(w)
	if !ok {
		return
	}

	snapshot, available := captureRingProvider().Get(requestID)
	meta, err := store.Meta(r.Context(), requestID)
	saved := true
	switch {
	case errors.Is(err, observability.ErrCaptureNotFound):
		saved = false
	case err != nil:
		a.writeCaptureError(w, err)
		return
	}

	response := captureStatusResponse{
		RequestID: requestID,
		Available: available,
		CanSave:   available,
		Saved:     saved,
	}
	if available {
		response.RequestTruncated = snapshot.Request.Truncated
		response.ResponseTruncated = snapshot.Response.Truncated
		response.RequestBytes = captureBodyBytes(snapshot.Request)
		response.ResponseBytes = captureBodyBytes(snapshot.Response)
		capturedAt := snapshot.CapturedAt.UTC()
		expiresAt := snapshot.ExpiresAt.UTC()
		response.CapturedAt = &capturedAt
		response.ExpiresAt = &expiresAt
	} else if saved {
		response.RequestTruncated = meta.RequestTruncated
		response.ResponseTruncated = meta.ResponseTruncated
		response.RequestBytes = meta.RequestBytes
		response.ResponseBytes = meta.ResponseBytes
	}
	if saved {
		expiresAt := meta.ExpiresAt.UTC()
		response.SavedExpiresAt = &expiresAt
	}

	setNoStore(w.Header())
	writeJSON(w, http.StatusOK, response)
}

func (a *API) handleCaptureSave(w http.ResponseWriter, r *http.Request) {
	requestID, ok := captureRequestID(w, r)
	if !ok {
		return
	}
	var req captureSaveRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if !req.ConfirmRisk {
		writeError(w, http.StatusBadRequest, "capture_confirmation_required",
			"保存请求/响应 body 前必须确认隐私风险")
		return
	}

	store, ok := a.captureDependencies(w)
	if !ok {
		return
	}
	snapshot, available := captureRingProvider().Get(requestID)
	if !available {
		writeError(w, http.StatusNotFound, "capture_not_found",
			"请求 body 已过期或未被诊断环捕获")
		return
	}

	meta, err := store.Save(r.Context(), snapshot)
	if err != nil {
		a.writeCaptureError(w, err)
		return
	}

	setNoStore(w.Header())
	writeJSON(w, http.StatusCreated, captureSaveResponse{
		RequestID:         meta.RequestID,
		Saved:             true,
		RequestTruncated:  meta.RequestTruncated,
		ResponseTruncated: meta.ResponseTruncated,
		RequestBytes:      meta.RequestBytes,
		ResponseBytes:     meta.ResponseBytes,
		CreatedAt:         meta.CreatedAt.UTC(),
		ExpiresAt:         meta.ExpiresAt.UTC(),
	})
}

func (a *API) handleCaptureRead(w http.ResponseWriter, r *http.Request) {
	requestID, ok := captureRequestID(w, r)
	if !ok {
		return
	}
	store, ok := a.captureDependencies(w)
	if !ok {
		return
	}

	saved, err := store.Get(r.Context(), requestID)
	if err != nil {
		a.writeCaptureError(w, err)
		return
	}

	setNoStore(w.Header())
	writeJSON(w, http.StatusOK, captureReadResponse{
		RequestID:         saved.RequestID,
		RequestBody:       string(saved.RequestBody),
		ResponseBody:      string(saved.ResponseBody),
		RequestTruncated:  saved.RequestTruncated,
		ResponseTruncated: saved.ResponseTruncated,
		RequestBytes:      saved.RequestBytes,
		ResponseBytes:     saved.ResponseBytes,
		CreatedAt:         saved.CreatedAt.UTC(),
		ExpiresAt:         saved.ExpiresAt.UTC(),
	})
}

func (a *API) handleCaptureDelete(w http.ResponseWriter, r *http.Request) {
	requestID, ok := captureRequestID(w, r)
	if !ok {
		return
	}
	store, ok := a.captureDependencies(w)
	if !ok {
		return
	}

	if _, err := store.Delete(r.Context(), requestID); err != nil {
		a.writeCaptureError(w, err)
		return
	}
	// 删除已保存的密文后同时丢弃内存副本，状态立即回到不可用。
	captureRingProvider().Delete(requestID)
	w.WriteHeader(http.StatusNoContent)
}

// captureDependencies 从管理 API 已持有的数据库连接构造捕获仓储，并解析
// GATEWAY_MASTER_KEY。主密钥缺失时返回 503，与渠道管理端点的行为一致。
func (a *API) captureDependencies(w http.ResponseWriter) (*observability.CaptureStore, bool) {
	if a.db == nil {
		writeError(w, http.StatusServiceUnavailable, "capture_unavailable",
			"请求捕获暂不可用：数据库未就绪")
		return nil, false
	}
	db, ok := a.db.(observability.DB)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "capture_unavailable",
			"请求捕获暂不可用：数据库未就绪")
		return nil, false
	}
	envelope, err := channelMasterEnvelope()
	if err != nil {
		a.logger.Error("请求捕获需要主密钥，但 GATEWAY_MASTER_KEY 不可用")
		writeError(w, http.StatusServiceUnavailable, "capture_unavailable",
			"请求捕获暂不可用：主密钥未就绪")
		return nil, false
	}
	return observability.NewCaptureStore(db, envelope, observability.CaptureStoreOptions{}), true
}

func (a *API) writeCaptureError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, observability.ErrCaptureNotFound):
		writeError(w, http.StatusNotFound, "capture_not_found", "捕获不存在或已过期")
	case errors.Is(err, observability.ErrCaptureStoreNotReady):
		writeError(w, http.StatusServiceUnavailable, "capture_unavailable",
			"请求捕获暂不可用")
	case errors.Is(err, observability.ErrInvalidRecord):
		writeError(w, http.StatusBadRequest, "invalid_capture", "捕获内容无效")
	default:
		a.logger.Error("请求捕获操作失败", "error", secret.RedactValue(err.Error()))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

// captureRequestID 校验路径中的 request_id，规则与请求观测端点一致。
func captureRequestID(w http.ResponseWriter, r *http.Request) (string, bool) {
	requestID := strings.TrimSpace(r.PathValue("id"))
	if requestID == "" || len(requestID) > 128 ||
		strings.IndexFunc(requestID, unicode.IsControl) >= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_id", "请求 ID 无效")
		return "", false
	}
	return requestID, true
}

func captureBodyBytes(body observability.CaptureBody) *int64 {
	if body.Data == nil {
		return nil
	}
	size := int64(len(body.Data))
	return &size
}
