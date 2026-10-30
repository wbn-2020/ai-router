package observability

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-router/internal/secret"
)

func testCaptureEnvelope(t *testing.T) *secret.Envelope {
	t.Helper()
	master := make([]byte, secret.MasterKeySize)
	for i := range master {
		master[i] = 0x4b
	}
	envelope, err := secret.NewEnvelope(master)
	if err != nil {
		t.Fatalf("secret.NewEnvelope: %v", err)
	}
	return envelope
}

func commitCapture(t *testing.T, ring *CaptureRing, requestID string, request, response []byte) CaptureSnapshot {
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
	snapshot, ok := session.Commit()
	if !ok {
		t.Fatal("Commit 失败")
	}
	return snapshot
}

func captureRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM request_captures`).Scan(&count); err != nil {
		t.Fatalf("统计 request_captures: %v", err)
	}
	return count
}

// TestCaptureRingDefaultsAndTTL 验证默认上限与 5 分钟过期语义。
func TestCaptureRingDefaultsAndTTL(t *testing.T) {
	ring := NewCaptureRing(CaptureRingOptions{})
	if ring.ttl != DefaultCaptureTTL ||
		ring.maxRequests != DefaultCaptureMaxRequests ||
		ring.maxBytes != DefaultCaptureMaxBytes ||
		ring.bodyLimit != DefaultCaptureBodyLimit {
		t.Fatalf("默认配置 = ttl=%v maxRequests=%d maxBytes=%d bodyLimit=%d",
			ring.ttl, ring.maxRequests, ring.maxBytes, ring.bodyLimit)
	}

	now := testBase
	ring = NewCaptureRing(CaptureRingOptions{Now: func() time.Time { return now }})
	snapshot := commitCapture(t, ring, "req-ttl", []byte(`{"input":"hello"}`), []byte("data: hi\n\n"))
	if !snapshot.CapturedAt.Equal(now) {
		t.Errorf("CapturedAt = %v, want %v", snapshot.CapturedAt, now)
	}
	if !snapshot.ExpiresAt.Equal(now.Add(DefaultCaptureTTL)) {
		t.Errorf("ExpiresAt = %v, want %v", snapshot.ExpiresAt, now.Add(DefaultCaptureTTL))
	}

	now = now.Add(DefaultCaptureTTL - time.Second)
	if _, ok := ring.Get("req-ttl"); !ok {
		t.Fatal("TTL 内捕获应可用")
	}

	now = now.Add(2 * time.Second)
	if _, ok := ring.Get("req-ttl"); ok {
		t.Fatal("超过 TTL 的捕获应不可用")
	}
	if ring.Len() != 0 || ring.Bytes() != 0 {
		t.Fatalf("过期捕获未清理：len=%d bytes=%d", ring.Len(), ring.Bytes())
	}
}

// TestCaptureRingEvictsByRequestCount 验证最多保留 50（配置为 3）个请求。
func TestCaptureRingEvictsByRequestCount(t *testing.T) {
	ring := NewCaptureRing(CaptureRingOptions{
		MaxRequests: 3,
		MaxBytes:    1 << 20,
		BodyLimit:   64,
	})
	for _, id := range []string{"req-1", "req-2", "req-3", "req-4"} {
		commitCapture(t, ring, id, []byte("request"), []byte("response"))
	}

	if ring.Len() != 3 {
		t.Fatalf("Len = %d, want 3", ring.Len())
	}
	if _, ok := ring.Get("req-1"); ok {
		t.Fatal("最旧的捕获应被驱逐")
	}
	for _, id := range []string{"req-2", "req-3", "req-4"} {
		if _, ok := ring.Get(id); !ok {
			t.Fatalf("%s 应仍在环中", id)
		}
	}
}

// TestCaptureRingEvictsByBytesAndTruncates 验证总字节上限与单侧截断。
func TestCaptureRingEvictsByBytesAndTruncates(t *testing.T) {
	ring := NewCaptureRing(CaptureRingOptions{
		MaxBytes:  16,
		BodyLimit: 8,
	})
	commitCapture(t, ring, "req-a",
		[]byte("12345678"), []byte("abcdefgh"))
	if ring.Bytes() != 16 {
		t.Fatalf("Bytes = %d, want 16", ring.Bytes())
	}
	commitCapture(t, ring, "req-b",
		[]byte("87654321"), []byte("hgfedcba"))
	if ring.Bytes() != 16 {
		t.Fatalf("驱逐后 Bytes = %d, want 16", ring.Bytes())
	}
	if _, ok := ring.Get("req-a"); ok {
		t.Fatal("超出总字节上限时最旧捕获应被驱逐")
	}

	ring = NewCaptureRing(CaptureRingOptions{MaxBytes: 1 << 20, BodyLimit: 4})
	snapshot := commitCapture(t, ring, "req-truncated",
		[]byte("0123456789"), []byte("abcdefghij"))
	if !snapshot.Request.Truncated || !snapshot.Response.Truncated {
		t.Fatalf("两侧都应标记 truncated：%+v", snapshot)
	}
	if !bytes.Equal(snapshot.Request.Data, []byte("0123")) ||
		!bytes.Equal(snapshot.Response.Data, []byte("abcd")) {
		t.Fatalf("截断内容不符：req=%q resp=%q",
			snapshot.Request.Data, snapshot.Response.Data)
	}

	stored, ok := ring.Get("req-truncated")
	if !ok {
		t.Fatal("截断捕获应仍可读取")
	}
	if !stored.Request.Truncated || !stored.Response.Truncated {
		t.Fatalf("Get 丢失 truncated 标志：%+v", stored)
	}
}

type trackingReadCloser struct {
	reader io.Reader
	closed atomic.Bool
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *trackingReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}

// TestCaptureSessionTeeAndCommit 验证 tee 不改变字节、流式内容原样保存、
// Commit 幂等且 Close 透传。
func TestCaptureSessionTeeAndCommit(t *testing.T) {
	ring := NewCaptureRing(CaptureRingOptions{BodyLimit: 4096})
	session := ring.NewSession("req-tee")
	if session == nil {
		t.Fatal("NewSession 返回 nil")
	}

	requestPayload := []byte(`{"model":"gpt-test","input":"hello"}`)
	requestBody := &trackingReadCloser{reader: bytes.NewReader(requestPayload)}
	teeRequest := session.TeeRequest(requestBody)
	gotRequest, err := io.ReadAll(teeRequest)
	if err != nil {
		t.Fatalf("读取请求 tee: %v", err)
	}
	if !bytes.Equal(gotRequest, requestPayload) {
		t.Fatalf("tee 改变请求字节：%q", gotRequest)
	}
	if err := teeRequest.Close(); err != nil {
		t.Fatalf("关闭请求 tee: %v", err)
	}
	if !requestBody.closed.Load() {
		t.Fatal("关闭 tee 时未关闭底层请求体")
	}

	responsePayload := []byte("event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	responseBody := &trackingReadCloser{reader: bytes.NewReader(responsePayload)}
	teeResponse := session.TeeResponse(responseBody)
	gotResponse, err := io.ReadAll(teeResponse)
	if err != nil {
		t.Fatalf("读取响应 tee: %v", err)
	}
	if !bytes.Equal(gotResponse, responsePayload) {
		t.Fatalf("tee 改变响应字节：%q", gotResponse)
	}
	_ = teeResponse.Close()

	snapshot, ok := session.Commit()
	if !ok {
		t.Fatal("Commit 失败")
	}
	if !bytes.Equal(snapshot.Request.Data, requestPayload) {
		t.Fatalf("环内请求 body = %q", snapshot.Request.Data)
	}
	if !bytes.Equal(snapshot.Response.Data, responsePayload) {
		t.Fatalf("环内响应 SSE = %q", snapshot.Response.Data)
	}
	if snapshot.Request.Truncated || snapshot.Response.Truncated {
		t.Fatal("未超限的 body 不应标记 truncated")
	}
	if _, ok := session.Commit(); ok {
		t.Fatal("重复 Commit 应返回 false")
	}

	// 写入方在 Commit 后修改自己的切片不应影响环内副本。
	requestPayload[0] = 'X'
	stored, _ := ring.Get("req-tee")
	if stored.Request.Data[0] == 'X' {
		t.Fatal("环内数据被调用方切片修改")
	}
}

// TestCaptureTeeDoesNotTruncateForwarding 验证诊断环截断只影响内存副本，
// 不改变 tee 读取到的转发字节。
func TestCaptureTeeDoesNotTruncateForwarding(t *testing.T) {
	ring := NewCaptureRing(CaptureRingOptions{MaxBytes: 1 << 20, BodyLimit: 4})
	session := ring.NewSession("req-tee-truncated")
	if session == nil {
		t.Fatal("NewSession 返回 nil")
	}
	payload := []byte("0123456789")
	body := &trackingReadCloser{reader: bytes.NewReader(payload)}
	tee := session.TeeResponse(body)
	forwarded, err := io.ReadAll(tee)
	if err != nil {
		t.Fatalf("读取 tee: %v", err)
	}
	_ = tee.Close()
	if !bytes.Equal(forwarded, payload) {
		t.Fatalf("tee 截断了转发字节：%q", forwarded)
	}

	snapshot, ok := session.Commit()
	if !ok {
		t.Fatal("Commit 失败")
	}
	if !snapshot.Response.Truncated || string(snapshot.Response.Data) != "0123" {
		t.Fatalf("捕获侧截断不符：truncated=%v data=%q",
			snapshot.Response.Truncated, snapshot.Response.Data)
	}
}

// TestDefaultRequestLoggingDoesNotPersistCaptures 验证默认请求日志与诊断环
// 都不会产生 request_captures 行。
func TestDefaultRequestLoggingDoesNotPersistCaptures(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "capture-default")

	if err := obs.WriteRequest(context.Background(),
		testRequestLog("req-log", channelID, "gpt-test", testBase)); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	ring := NewCaptureRing(CaptureRingOptions{})
	commitCapture(t, ring, "req-log", []byte(`{"input":"secret"}`), []byte("data: ok\n\n"))
	if _, ok := ring.Get("req-log"); !ok {
		t.Fatal("诊断环应包含刚写入的 body")
	}

	if count := captureRowCount(t, st.DB()); count != 0 {
		t.Fatalf("默认请求产生 %d 条 request_captures 行，want 0", count)
	}
}

// TestCaptureStoreEncryptsAndReads 验证落库只有密文、解密内容一致，且请求/
// 响应密文不能互换用途解密。
func TestCaptureStoreEncryptsAndReads(t *testing.T) {
	_, st := newTestObservability(t)
	envelope := testCaptureEnvelope(t)
	now := testBase
	captures := NewCaptureStore(st.DB(), envelope, CaptureStoreOptions{
		Now: func() time.Time { return now },
	})

	requestBody := []byte(`{"model":"gpt-test","input":"secret prompt"}`)
	responseBody := []byte("event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	meta, err := captures.Save(context.Background(), CaptureSnapshot{
		RequestID: "cap-encrypt",
		Request:   CaptureBody{Data: requestBody},
		Response:  CaptureBody{Data: responseBody},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !meta.CreatedAt.Equal(now) ||
		!meta.ExpiresAt.Equal(now.Add(DefaultCapturePersistenceTTL)) {
		t.Fatalf("保存时间不符：%+v", meta)
	}
	if meta.RequestBytes == nil || *meta.RequestBytes != int64(len(requestBody)) {
		t.Fatalf("RequestBytes = %v", meta.RequestBytes)
	}
	if meta.ResponseBytes == nil || *meta.ResponseBytes != int64(len(responseBody)) {
		t.Fatalf("ResponseBytes = %v", meta.ResponseBytes)
	}

	var (
		requestCiphertext  sql.NullString
		responseCiphertext sql.NullString
		requestTruncated   int
		responseTruncated  int
		requestBytes       sql.NullInt64
		responseBytes      sql.NullInt64
		createdAt          string
		expiresAt          string
	)
	if err := st.DB().QueryRow(`
		SELECT request_body_ciphertext, response_body_ciphertext,
		       request_truncated, response_truncated, request_bytes,
		       response_bytes, created_at, expires_at
		FROM request_captures WHERE request_id = ?`, "cap-encrypt").Scan(
		&requestCiphertext, &responseCiphertext, &requestTruncated,
		&responseTruncated, &requestBytes, &responseBytes,
		&createdAt, &expiresAt,
	); err != nil {
		t.Fatalf("读取 request_captures: %v", err)
	}
	if !requestCiphertext.Valid || !strings.HasPrefix(requestCiphertext.String, secret.EnvelopeVersion+":") {
		t.Fatalf("请求 body 未加密：%q", requestCiphertext.String)
	}
	if !responseCiphertext.Valid || !strings.HasPrefix(responseCiphertext.String, secret.EnvelopeVersion+":") {
		t.Fatalf("响应 body 未加密：%q", responseCiphertext.String)
	}
	if strings.Contains(requestCiphertext.String, "secret prompt") ||
		strings.Contains(responseCiphertext.String, "output_text.delta") {
		t.Fatal("密文包含明文片段")
	}
	if requestTruncated != 0 || responseTruncated != 0 {
		t.Fatal("未超限的捕获不应标记 truncated")
	}
	if !requestBytes.Valid || requestBytes.Int64 != int64(len(requestBody)) ||
		!responseBytes.Valid || responseBytes.Int64 != int64(len(responseBody)) {
		t.Fatalf("落库字节数不符：req=%v resp=%v", requestBytes, responseBytes)
	}
	if createdAt != formatTime(now) ||
		expiresAt != formatTime(now.Add(DefaultCapturePersistenceTTL)) {
		t.Fatalf("落库时间不符：%s / %s", createdAt, expiresAt)
	}

	decryptedRequest, err := envelope.Decrypt(requestCiphertext.String, captureRequestPurpose)
	if err != nil {
		t.Fatalf("解密请求 body: %v", err)
	}
	if !bytes.Equal(decryptedRequest, requestBody) {
		t.Fatalf("解密请求 body 不一致：%q", decryptedRequest)
	}
	decryptedResponse, err := envelope.Decrypt(responseCiphertext.String, captureResponsePurpose)
	if err != nil {
		t.Fatalf("解密响应 body: %v", err)
	}
	if !bytes.Equal(decryptedResponse, responseBody) {
		t.Fatalf("解密响应 body 不一致：%q", decryptedResponse)
	}
	if _, err := envelope.Decrypt(requestCiphertext.String, captureResponsePurpose); !errors.Is(err, secret.ErrDecryptFailed) {
		t.Fatalf("请求密文按响应用途解密应失败，got %v", err)
	}
	if _, err := envelope.Decrypt(requestCiphertext.String, secret.PurposeRequestCapture); !errors.Is(err, secret.ErrDecryptFailed) {
		t.Fatalf("未绑定用途的密文解密应失败，got %v", err)
	}

	saved, err := captures.Get(context.Background(), "cap-encrypt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(saved.RequestBody, requestBody) ||
		!bytes.Equal(saved.ResponseBody, responseBody) {
		t.Fatal("Get 解密内容与原始 body 不一致")
	}
	if saved.RequestBytes == nil || *saved.RequestBytes != int64(len(requestBody)) {
		t.Fatalf("Get RequestBytes = %v", saved.RequestBytes)
	}

	// 同一 request_id 再次保存覆盖旧密文，不新增行。
	if _, err := captures.Save(context.Background(), CaptureSnapshot{
		RequestID: "cap-encrypt",
		Request:   CaptureBody{Data: []byte("second request")},
		Response:  CaptureBody{Data: []byte("second response")},
	}); err != nil {
		t.Fatalf("二次 Save: %v", err)
	}
	if count := captureRowCount(t, st.DB()); count != 1 {
		t.Fatalf("重复保存产生 %d 行，want 1", count)
	}
	saved, err = captures.Get(context.Background(), "cap-encrypt")
	if err != nil {
		t.Fatalf("二次 Get: %v", err)
	}
	if string(saved.RequestBody) != "second request" || string(saved.ResponseBody) != "second response" {
		t.Fatalf("覆盖后的内容不符：%q / %q", saved.RequestBody, saved.ResponseBody)
	}
}

// TestCaptureStoreTTLAndDelete 验证 24h TTL、过期读取 404 与显式删除。
func TestCaptureStoreTTLAndDelete(t *testing.T) {
	_, st := newTestObservability(t)
	envelope := testCaptureEnvelope(t)
	now := testBase
	captures := NewCaptureStore(st.DB(), envelope, CaptureStoreOptions{
		Now: func() time.Time { return now },
	})
	ctx := context.Background()

	if _, err := captures.Save(ctx, CaptureSnapshot{
		RequestID: "cap-ttl",
		Request:   CaptureBody{Data: []byte("request")},
		Response:  CaptureBody{Data: []byte("response")},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now = now.Add(DefaultCapturePersistenceTTL + time.Second)
	if _, err := captures.Get(ctx, "cap-ttl"); !errors.Is(err, ErrCaptureNotFound) {
		t.Fatalf("过期 Get = %v, want ErrCaptureNotFound", err)
	}
	if _, err := captures.Meta(ctx, "cap-ttl"); !errors.Is(err, ErrCaptureNotFound) {
		t.Fatalf("过期 Meta = %v, want ErrCaptureNotFound", err)
	}
	if count := captureRowCount(t, st.DB()); count != 1 {
		t.Fatalf("过期但未清理的行 = %d, want 1", count)
	}
	deleted, err := captures.PruneExpired(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("PruneExpired 删除 %d 行，want 1", deleted)
	}
	if count := captureRowCount(t, st.DB()); count != 0 {
		t.Fatalf("清理后行数 = %d, want 0", count)
	}

	if _, err := captures.Save(ctx, CaptureSnapshot{
		RequestID: "cap-delete",
		Request:   CaptureBody{Data: []byte("request")},
		Response:  CaptureBody{Data: []byte("response")},
	}); err != nil {
		t.Fatalf("重新 Save: %v", err)
	}
	wasDeleted, err := captures.Delete(ctx, "cap-delete")
	if err != nil || !wasDeleted {
		t.Fatalf("Delete = %v, %v, want true, nil", wasDeleted, err)
	}
	if _, err := captures.Get(ctx, "cap-delete"); !errors.Is(err, ErrCaptureNotFound) {
		t.Fatalf("删除后 Get = %v, want ErrCaptureNotFound", err)
	}
	wasDeleted, err = captures.Delete(ctx, "cap-delete")
	if err != nil || wasDeleted {
		t.Fatalf("二次 Delete = %v, %v, want false, nil", wasDeleted, err)
	}
}

// TestCaptureStoreTruncatedFlags 验证 truncated 标志与缺失一侧的持久化。
func TestCaptureStoreTruncatedFlags(t *testing.T) {
	_, st := newTestObservability(t)
	captures := NewCaptureStore(st.DB(), testCaptureEnvelope(t), CaptureStoreOptions{})
	ctx := context.Background()

	if _, err := captures.Save(ctx, CaptureSnapshot{
		RequestID: "cap-truncated",
		Request:   CaptureBody{Data: []byte("0123"), Truncated: true},
		Response:  CaptureBody{Data: []byte("abcd"), Truncated: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := captures.Get(ctx, "cap-truncated")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !saved.RequestTruncated || !saved.ResponseTruncated {
		t.Fatalf("truncated 标志丢失：%+v", saved.CaptureMeta)
	}
	if string(saved.RequestBody) != "0123" || string(saved.ResponseBody) != "abcd" {
		t.Fatalf("截断内容不符：%q / %q", saved.RequestBody, saved.ResponseBody)
	}

	// 请求侧未捕获时密文为 NULL，读取结果保持缺失。
	if _, err := captures.Save(ctx, CaptureSnapshot{
		RequestID: "cap-missing-request",
		Response:  CaptureBody{Data: []byte("response-only")},
	}); err != nil {
		t.Fatalf("Save 缺失请求侧: %v", err)
	}
	saved, err = captures.Get(ctx, "cap-missing-request")
	if err != nil {
		t.Fatalf("Get 缺失请求侧: %v", err)
	}
	if saved.RequestBody != nil || saved.RequestBytes != nil || saved.RequestTruncated {
		t.Fatalf("缺失请求侧不应有内容：%+v", saved.CaptureMeta)
	}
	if string(saved.ResponseBody) != "response-only" {
		t.Fatalf("响应 body = %q", saved.ResponseBody)
	}
}

// TestCaptureStoreRunCleanup 验证后台清理任务在启动时立即删除过期行并响应取消。
func TestCaptureStoreRunCleanup(t *testing.T) {
	_, st := newTestObservability(t)
	now := testBase
	captures := NewCaptureStore(st.DB(), testCaptureEnvelope(t), CaptureStoreOptions{
		TTL: time.Minute,
		Now: func() time.Time { return now },
	})
	if _, err := captures.Save(context.Background(), CaptureSnapshot{
		RequestID: "cap-cleanup",
		Request:   CaptureBody{Data: []byte("request")},
		Response:  CaptureBody{Data: []byte("response")},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	now = now.Add(2 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		captures.RunCleanup(ctx, time.Hour, func(err error) {
			select {
			case errs <- err:
			default:
			}
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for captureRowCount(t, st.DB()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("后台清理未删除过期捕获")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case err := <-errs:
		t.Fatalf("RunCleanup 报告错误: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunCleanup 未在取消后退出")
	}
}

// TestCaptureStoreEnforcesEightMiBBodyLimit 验证持久化层同样执行单侧 8MB 上限。
func TestCaptureStoreEnforcesEightMiBBodyLimit(t *testing.T) {
	_, st := newTestObservability(t)
	captures := NewCaptureStore(st.DB(), testCaptureEnvelope(t), CaptureStoreOptions{})
	ctx := context.Background()

	oversized := bytes.Repeat([]byte{'x'}, int(DefaultCaptureBodyLimit)+1)
	meta, err := captures.Save(ctx, CaptureSnapshot{
		RequestID: "cap-8mib",
		Request:   CaptureBody{Data: oversized},
		Response:  CaptureBody{Data: []byte("ok")},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !meta.RequestTruncated || meta.ResponseTruncated {
		t.Fatalf("truncated 标志不符：%+v", meta)
	}
	if meta.RequestBytes == nil || *meta.RequestBytes != DefaultCaptureBodyLimit {
		t.Fatalf("RequestBytes = %v, want %d", meta.RequestBytes, DefaultCaptureBodyLimit)
	}

	saved, err := captures.Get(ctx, "cap-8mib")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !saved.RequestTruncated {
		t.Fatal("读取丢失 truncated 标志")
	}
	if int64(len(saved.RequestBody)) != DefaultCaptureBodyLimit {
		t.Fatalf("保存的请求 body 长度 = %d, want %d",
			len(saved.RequestBody), DefaultCaptureBodyLimit)
	}
	if string(saved.ResponseBody) != "ok" {
		t.Fatalf("响应 body = %q", saved.ResponseBody)
	}
}

// TestCaptureSchemaHasNoHeaderColumns 验证捕获表没有 header/Authorization/Cookie
// 列，且具备加密字段。
func TestCaptureSchemaHasNoHeaderColumns(t *testing.T) {
	_, st := newTestObservability(t)
	rows, err := st.DB().Query(`PRAGMA table_info(request_captures)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("读取列信息: %v", err)
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列信息: %v", err)
	}

	for _, forbidden := range []string{"header", "headers", "authorization", "cookie", "prompt", "body"} {
		if columns[forbidden] {
			t.Fatalf("request_captures 含禁用列 %q", forbidden)
		}
	}
	for _, required := range []string{
		"request_body_ciphertext", "response_body_ciphertext",
		"request_truncated", "response_truncated",
		"request_bytes", "response_bytes", "created_at", "expires_at",
	} {
		if !columns[required] {
			t.Fatalf("request_captures 缺少列 %q", required)
		}
	}
}
