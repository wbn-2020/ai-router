package gateway

import (
	"encoding/json"
	"net/http"
)

// APIError 是数据面对外暴露的唯一错误结构。
//
// 只包含稳定字段，禁止把上游响应 body、上游 URL、Authorization、x-api-key
// 或数据库错误原文放入 Message；上游错误归一化由调用方按状态码映射到固定
// 文案后再写入。
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// errorEnvelope 对应 OpenAI / Responses 的 {error:{message,type,code}} 形状。
type errorEnvelope struct {
	Error APIError `json:"error"`
}

const (
	errorTypeInvalidRequest = "invalid_request_error"
	errorTypeServer         = "server_error"

	errorCodeInvalidAPIKey     = "invalid_api_key"
	errorCodeInvalidJSON       = "invalid_json"
	errorCodeMissingModel      = "missing_model"
	errorCodeModelNotAvailable = "model_not_available"
	errorCodeNotFound          = "not_found"
	errorCodeMethodNotAllowed  = "method_not_allowed"
	errorCodeBodyTooLarge      = "request_body_too_large"
	errorCodeInternal          = "internal_error"
	errorCodeUnavailable       = "gateway_unavailable"
)

// writeAPIError 写出协议固定的错误 envelope 并禁用缓存。
func writeAPIError(w http.ResponseWriter, status int, apiErr APIError) {
	writeJSON(w, status, errorEnvelope{Error: apiErr})
}

func writeInvalidAPIKey(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeAPIError(w, http.StatusUnauthorized, APIError{
		Message: "Missing or invalid API key.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeInvalidAPIKey,
	})
}

func writeNotFound(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, APIError{
		Message: "The requested endpoint does not exist.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeNotFound,
	})
}

func writeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeAPIError(w, http.StatusMethodNotAllowed, APIError{
		Message: "The requested method is not allowed for this endpoint.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeMethodNotAllowed,
	})
}

func writeModelNotAvailable(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, APIError{
		Message: "The requested model does not exist or is not available.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeModelNotAvailable,
	})
}

func writeInvalidJSON(w http.ResponseWriter) {
	writeAPIError(w, http.StatusBadRequest, APIError{
		Message: "Request body must be valid JSON.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeInvalidJSON,
	})
}

func writeMissingModel(w http.ResponseWriter) {
	writeAPIError(w, http.StatusBadRequest, APIError{
		Message: "The 'model' field is required.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeMissingModel,
	})
}

func writeModelTooLong(w http.ResponseWriter) {
	writeAPIError(w, http.StatusBadRequest, APIError{
		Message: "The 'model' field is too long.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeInvalidRequest,
	})
}

func writeBodyTooLarge(w http.ResponseWriter) {
	writeAPIError(w, http.StatusRequestEntityTooLarge, APIError{
		Message: "Request body is too large.",
		Type:    errorTypeInvalidRequest,
		Code:    errorCodeBodyTooLarge,
	})
}

func writeInternalError(w http.ResponseWriter) {
	writeAPIError(w, http.StatusInternalServerError, APIError{
		Message: "Internal server error.",
		Type:    errorTypeServer,
		Code:    errorCodeInternal,
	})
}

func writeUnavailable(w http.ResponseWriter) {
	writeAPIError(w, http.StatusServiceUnavailable, APIError{
		Message: "Gateway is not ready to serve this request.",
		Type:    errorTypeServer,
		Code:    errorCodeUnavailable,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	header := w.Header()
	header.Set("Content-Type", "application/json; charset=utf-8")
	setNoStore(header)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func setNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
}
