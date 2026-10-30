package adminapi

import (
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/observability"
	"ai-router/internal/secret"
	"ai-router/internal/store"
)

func newCaptureTestAPI(t *testing.T, ring *observability.CaptureRing) (*API, *store.Store, *http.Cookie) {
	t.Helper()

	master := make([]byte, secret.MasterKeySize)
	for i := range master {
		master[i] = 0x6d
	}
	t.Setenv(config.EnvMasterKey, base64.StdEncoding.EncodeToString(master))

	previous := captureRingProvider
	captureRingProvider = func() *observability.CaptureRing { return ring }
	t.Cleanup(func() {
		captureRingProvider = previous
	})

	api, st := newTestAPI(t)
	cookie := loginCookie(t, api, testAdminPassword)
	return api, st, cookie
}

func seedCaptureRing(t *testing.T, ring *observability.CaptureRing, requestID string, request, response []byte) {
	t.Helper()
	session := ring.NewSession(requestID)
	if session == nil {
		t.Fatal("NewSession 返回 nil")
	}
	if _, err := session.RequestBuffer().Write(request); err != nil {
		t.Fatalf("写入请求 body: %v", err)
	}
	if _, err := session.ResponseBuffer().Write(response); err != nil {
		t.Fatalf("写入响应 body: %v", err)
	}
	if _, ok := session.Commit(); !ok {
		t.Fatal("诊断环 Commit 失败")
	}
}

func captureTestPath(requestID, suffix string) string {
	return "/admin/api/requests/" + url.PathEscape(requestID) + "/capture" + suffix
}

func captureCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM request_captures`).Scan(&count); err != nil {
		t.Fatalf("统计 request_captures: %v", err)
	}
	return count
}

// TestCaptureStatusSaveReadDeleteFlow 覆盖 status → 风险确认 → 保存 → 读取 →
// 删除的完整生命周期，并确认落库只有密文。
func TestCaptureStatusSaveReadDeleteFlow(t *testing.T) {
	ring := observability.NewCaptureRing(observability.CaptureRingOptions{})
	api, st, cookie := newCaptureTestAPI(t, ring)
	requestID := "req-capture-flow"
	requestBody := []byte(`{"model":"gpt-test","input":"secret prompt"}`)
	responseBody := []byte("event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	seedCaptureRing(t, ring, requestID, requestBody, responseBody)
	statusPath := captureTestPath(requestID, "/status")
	savePath := captureTestPath(requestID, "")
	readPath := captureTestPath(requestID, "")

	status := apiRequest(t, api, http.MethodGet, statusPath, nil, "", cookie)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（body=%s）", status.Code, status.Body.String())
	}
	assertNoStore(t, status)
	statusBody := decodeBody[captureStatusResponse](t, status)
	if !statusBody.Available || !statusBody.CanSave || statusBody.Saved {
		t.Fatalf("初始 status 不符：%+v", statusBody)
	}
	if statusBody.RequestBytes == nil || *statusBody.RequestBytes != int64(len(requestBody)) {
		t.Fatalf("status RequestBytes = %v", statusBody.RequestBytes)
	}
	if statusBody.CapturedAt == nil || statusBody.ExpiresAt == nil {
		t.Fatalf("status 缺少诊断环时间：%+v", statusBody)
	}
	if statusBody.ExpiresAt.Sub(*statusBody.CapturedAt) != observability.DefaultCaptureTTL {
		t.Fatalf("诊断环 TTL = %v", statusBody.ExpiresAt.Sub(*statusBody.CapturedAt))
	}

	// 未确认风险时拒绝保存，且不落库。
	unconfirmed := apiRequest(t, api, http.MethodPost, savePath,
		map[string]any{"confirm_risk": false}, testOrigin, cookie)
	if unconfirmed.Code != http.StatusBadRequest {
		t.Fatalf("未确认保存 = %d, want 400（body=%s）",
			unconfirmed.Code, unconfirmed.Body.String())
	}
	if errResp := decodeBody[errorResponse](t, unconfirmed); errResp.Error != "capture_confirmation_required" {
		t.Fatalf("错误码 = %q", errResp.Error)
	}
	if count := captureCount(t, st); count != 0 {
		t.Fatalf("未确认时落库 %d 行，want 0", count)
	}

	saved := apiRequest(t, api, http.MethodPost, savePath,
		map[string]any{"confirm_risk": true}, testOrigin, cookie)
	if saved.Code != http.StatusCreated {
		t.Fatalf("保存 = %d, want 201（body=%s）", saved.Code, saved.Body.String())
	}
	assertNoStore(t, saved)
	if strings.Contains(saved.Body.String(), "secret prompt") {
		t.Fatalf("保存响应泄漏 body 明文：%s", saved.Body.String())
	}
	saveBody := decodeBody[captureSaveResponse](t, saved)
	if !saveBody.Saved || saveBody.RequestID != requestID {
		t.Fatalf("保存响应不符：%+v", saveBody)
	}
	if saveBody.ExpiresAt.Sub(saveBody.CreatedAt) != observability.DefaultCapturePersistenceTTL {
		t.Fatalf("保存 TTL = %v", saveBody.ExpiresAt.Sub(saveBody.CreatedAt))
	}

	var requestCiphertext, responseCiphertext sql.NullString
	if err := st.DB().QueryRow(`
		SELECT request_body_ciphertext, response_body_ciphertext
		FROM request_captures WHERE request_id = ?`, requestID).Scan(
		&requestCiphertext, &responseCiphertext,
	); err != nil {
		t.Fatalf("读取落库密文: %v", err)
	}
	if !requestCiphertext.Valid ||
		!strings.HasPrefix(requestCiphertext.String, secret.EnvelopeVersion+":") ||
		!responseCiphertext.Valid ||
		!strings.HasPrefix(responseCiphertext.String, secret.EnvelopeVersion+":") {
		t.Fatalf("落库内容不是 v1 密文：req=%q resp=%q",
			requestCiphertext.String, responseCiphertext.String)
	}
	if strings.Contains(requestCiphertext.String, "secret prompt") ||
		strings.Contains(responseCiphertext.String, "output_text.delta") {
		t.Fatal("数据库密文包含 body 明文")
	}

	afterSave := apiRequest(t, api, http.MethodGet, statusPath, nil, "", cookie)
	afterSaveBody := decodeBody[captureStatusResponse](t, afterSave)
	if !afterSaveBody.Available || !afterSaveBody.Saved || afterSaveBody.SavedExpiresAt == nil {
		t.Fatalf("保存后 status 不符：%+v", afterSaveBody)
	}
	assertNoStore(t, afterSave)

	read := apiRequest(t, api, http.MethodGet, readPath, nil, "", cookie)
	if read.Code != http.StatusOK {
		t.Fatalf("读取 = %d, want 200（body=%s）", read.Code, read.Body.String())
	}
	assertNoStore(t, read)
	readBody := decodeBody[captureReadResponse](t, read)
	if readBody.RequestBody != string(requestBody) || readBody.ResponseBody != string(responseBody) {
		t.Fatalf("读取内容与原始 body 不一致：req=%q resp=%q",
			readBody.RequestBody, readBody.ResponseBody)
	}
	if readBody.RequestTruncated || readBody.ResponseTruncated {
		t.Fatalf("未超限 body 不应 truncated：%+v", readBody)
	}
	if readBody.RequestBytes == nil || *readBody.RequestBytes != int64(len(requestBody)) {
		t.Fatalf("读取 RequestBytes = %v", readBody.RequestBytes)
	}

	deleted := apiRequest(t, api, http.MethodDelete, savePath, nil, testOrigin, cookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除 = %d, want 204（body=%s）", deleted.Code, deleted.Body.String())
	}
	missing := apiRequest(t, api, http.MethodGet, readPath, nil, "", cookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("删除后读取 = %d, want 404（body=%s）",
			missing.Code, missing.Body.String())
	}
	if errResp := decodeBody[errorResponse](t, missing); errResp.Error != "capture_not_found" {
		t.Fatalf("删除后错误码 = %q", errResp.Error)
	}
	assertNoStore(t, missing)

	afterDelete := apiRequest(t, api, http.MethodGet, statusPath, nil, "", cookie)
	afterDeleteBody := decodeBody[captureStatusResponse](t, afterDelete)
	if afterDeleteBody.Available || afterDeleteBody.Saved || afterDeleteBody.CanSave {
		t.Fatalf("删除后 status 仍可用：%+v", afterDeleteBody)
	}

	// DELETE 幂等：已删除的捕获再次删除仍返回 204。
	again := apiRequest(t, api, http.MethodDelete, savePath, nil, testOrigin, cookie)
	if again.Code != http.StatusNoContent {
		t.Fatalf("重复删除 = %d, want 204", again.Code)
	}
}

// TestCaptureTruncatedRoundTrip 验证超过单侧上限的 body 在保存与读取时保留
// truncated 标志。
func TestCaptureTruncatedRoundTrip(t *testing.T) {
	ring := observability.NewCaptureRing(observability.CaptureRingOptions{
		MaxBytes:  1 << 20,
		BodyLimit: 4,
	})
	api, _, cookie := newCaptureTestAPI(t, ring)
	requestID := "req-capture-truncated"
	seedCaptureRing(t, ring, requestID,
		[]byte("0123456789"), []byte("abcdefghij"))

	status := decodeBody[captureStatusResponse](t,
		apiRequest(t, api, http.MethodGet, captureTestPath(requestID, "/status"), nil, "", cookie))
	if !status.Available || !status.RequestTruncated || !status.ResponseTruncated {
		t.Fatalf("status truncated 不符：%+v", status)
	}
	if status.RequestBytes == nil || *status.RequestBytes != 4 {
		t.Fatalf("status RequestBytes = %v, want 4", status.RequestBytes)
	}

	savePath := captureTestPath(requestID, "")
	saved := apiRequest(t, api, http.MethodPost, savePath,
		map[string]any{"confirm_risk": true}, testOrigin, cookie)
	if saved.Code != http.StatusCreated {
		t.Fatalf("保存截断捕获 = %d, want 201（body=%s）",
			saved.Code, saved.Body.String())
	}
	saveBody := decodeBody[captureSaveResponse](t, saved)
	if !saveBody.RequestTruncated || !saveBody.ResponseTruncated {
		t.Fatalf("保存响应 truncated 不符：%+v", saveBody)
	}

	read := apiRequest(t, api, http.MethodGet, savePath, nil, "", cookie)
	if read.Code != http.StatusOK {
		t.Fatalf("读取截断捕获 = %d（body=%s）", read.Code, read.Body.String())
	}
	assertNoStore(t, read)
	readBody := decodeBody[captureReadResponse](t, read)
	if !readBody.RequestTruncated || !readBody.ResponseTruncated {
		t.Fatalf("读取 truncated 不符：%+v", readBody)
	}
	if readBody.RequestBody != "0123" || readBody.ResponseBody != "abcd" {
		t.Fatalf("截断内容 = %q / %q", readBody.RequestBody, readBody.ResponseBody)
	}
}

// TestCaptureExpiredRingAndSavedCapture 覆盖诊断环 5 分钟过期与保存后
// 24h 过期均导致读取 404。
func TestCaptureExpiredRingAndSavedCapture(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ring := observability.NewCaptureRing(observability.CaptureRingOptions{
		Now: func() time.Time { return now },
	})
	api, st, cookie := newCaptureTestAPI(t, ring)

	expiredID := "req-capture-ring-expired"
	seedCaptureRing(t, ring, expiredID, []byte("request"), []byte("response"))
	now = now.Add(observability.DefaultCaptureTTL + time.Second)

	status := decodeBody[captureStatusResponse](t,
		apiRequest(t, api, http.MethodGet, captureTestPath(expiredID, "/status"), nil, "", cookie))
	if status.Available || status.CanSave || status.Saved {
		t.Fatalf("过期诊断环仍可用：%+v", status)
	}

	unavailable := apiRequest(t, api, http.MethodPost, captureTestPath(expiredID, ""),
		map[string]any{"confirm_risk": true}, testOrigin, cookie)
	if unavailable.Code != http.StatusNotFound {
		t.Fatalf("过期后保存 = %d, want 404（body=%s）",
			unavailable.Code, unavailable.Body.String())
	}
	if count := captureCount(t, st); count != 0 {
		t.Fatalf("过期后落库 %d 行，want 0", count)
	}

	savedID := "req-capture-saved-expired"
	seedCaptureRing(t, ring, savedID, []byte("request"), []byte("response"))
	saved := apiRequest(t, api, http.MethodPost, captureTestPath(savedID, ""),
		map[string]any{"confirm_risk": true}, testOrigin, cookie)
	if saved.Code != http.StatusCreated {
		t.Fatalf("保存 = %d, want 201（body=%s）", saved.Code, saved.Body.String())
	}

	// 诊断环过期后，已保存的捕获仍应可读，status 回退到保存的元数据。
	now = now.Add(observability.DefaultCaptureTTL + time.Second)
	status = decodeBody[captureStatusResponse](t,
		apiRequest(t, api, http.MethodGet, captureTestPath(savedID, "/status"), nil, "", cookie))
	if status.Available || !status.Saved || status.CanSave {
		t.Fatalf("诊断环过期后 status 不符：%+v", status)
	}
	if status.RequestBytes == nil || *status.RequestBytes != int64(len("request")) {
		t.Fatalf("status 未回退到保存的字节数：%+v", status)
	}
	read := apiRequest(t, api, http.MethodGet, captureTestPath(savedID, ""), nil, "", cookie)
	if read.Code != http.StatusOK {
		t.Fatalf("诊断环过期后读取保存 = %d, want 200（body=%s）",
			read.Code, read.Body.String())
	}

	past := "2000-01-01T00:00:00.000000000Z"
	if _, err := st.DB().Exec(
		`UPDATE request_captures SET expires_at = ? WHERE request_id = ?`, past, savedID,
	); err != nil {
		t.Fatalf("修改过期时间: %v", err)
	}

	read = apiRequest(t, api, http.MethodGet, captureTestPath(savedID, ""), nil, "", cookie)
	if read.Code != http.StatusNotFound {
		t.Fatalf("保存过期后读取 = %d, want 404（body=%s）",
			read.Code, read.Body.String())
	}
	status = decodeBody[captureStatusResponse](t,
		apiRequest(t, api, http.MethodGet, captureTestPath(savedID, "/status"), nil, "", cookie))
	if status.Saved || status.SavedExpiresAt != nil {
		t.Fatalf("过期保存仍标记 saved：%+v", status)
	}
}

// TestCaptureEndpointsRequireSessionAndSameOrigin 覆盖鉴权、同源与 ID 校验。
func TestCaptureEndpointsRequireSessionAndSameOrigin(t *testing.T) {
	ring := observability.NewCaptureRing(observability.CaptureRingOptions{})
	api, st, cookie := newCaptureTestAPI(t, ring)
	requestID := "req-capture-auth"
	seedCaptureRing(t, ring, requestID, []byte("request"), []byte("response"))

	statusPath := captureTestPath(requestID, "/status")
	savePath := captureTestPath(requestID, "")

	unauthorized := apiRequest(t, api, http.MethodGet, statusPath, nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status = %d, want 401", unauthorized.Code)
	}
	assertNoStore(t, unauthorized)

	unauthorized = apiRequest(t, api, http.MethodPost, savePath,
		map[string]any{"confirm_risk": true}, testOrigin)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录保存 = %d, want 401", unauthorized.Code)
	}

	crossOrigin := apiRequest(t, api, http.MethodPost, savePath,
		map[string]any{"confirm_risk": true}, "http://evil.example", cookie)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("跨源保存 = %d, want 403（body=%s）",
			crossOrigin.Code, crossOrigin.Body.String())
	}
	if count := captureCount(t, st); count != 0 {
		t.Fatalf("跨源请求落库 %d 行，want 0", count)
	}

	longID := strings.Repeat("a", 129)
	invalid := apiRequest(t, api, http.MethodGet,
		captureTestPath(longID, "/status"), nil, "", cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("超长 request_id = %d, want 400（body=%s）",
			invalid.Code, invalid.Body.String())
	}
}

// TestCaptureSaveWithoutRing 验证诊断环无数据时保存返回 404 且不落库。
func TestCaptureSaveWithoutRing(t *testing.T) {
	ring := observability.NewCaptureRing(observability.CaptureRingOptions{})
	api, st, cookie := newCaptureTestAPI(t, ring)

	rec := apiRequest(t, api, http.MethodPost, captureTestPath("req-missing", ""),
		map[string]any{"confirm_risk": true}, testOrigin, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无诊断环数据保存 = %d, want 404（body=%s）",
			rec.Code, rec.Body.String())
	}
	if count := captureCount(t, st); count != 0 {
		t.Fatalf("无诊断环数据却落库 %d 行", count)
	}
}

// TestCaptureRequiresMasterKey 验证主密钥缺失时捕获端点返回 503，不暴露细节。
func TestCaptureRequiresMasterKey(t *testing.T) {
	ring := observability.NewCaptureRing(observability.CaptureRingOptions{})
	api, _, cookie := newCaptureTestAPI(t, ring)
	t.Setenv(config.EnvMasterKey, "")

	rec := apiRequest(t, api, http.MethodGet,
		captureTestPath("req-no-master", "/status"), nil, "", cookie)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("主密钥缺失 status = %d, want 503（body=%s）",
			rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "GATEWAY_MASTER_KEY") {
		t.Fatalf("错误响应泄漏环境变量内容：%s", rec.Body.String())
	}
	assertNoStore(t, rec)
}
