// capture.go 实现 R-012 的后端：进程内诊断环与按需 body 加密持久化。
//
// 默认请求路径只把 request/response body 写入 CaptureRing，最多保留
// 5 分钟/50 个请求/32MB；单侧 body 最多 8MB，超出只保留前 8MB 并标记
// truncated。诊断环从不写 SQLite，也不保存 header、Authorization 或 Cookie。
//
// 管理员显式确认风险后，CaptureStore 使用 AES-256-GCM 加密 body 并写入
// request_captures，TTL 24h；读取与写入都由本包完成，管理 API 只看到
// 明文的短暂视图。过期捕获由 RunCleanup 每 10 分钟清理一次。
package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"ai-router/internal/secret"
)

const (
	// DefaultCaptureTTL 是诊断环中请求 body 的默认存活时间。
	DefaultCaptureTTL = 5 * time.Minute
	// DefaultCaptureMaxRequests 是诊断环保留的最大请求数。
	DefaultCaptureMaxRequests = 50
	// DefaultCaptureMaxBytes 是诊断环保留的 request+response 总字节上限。
	DefaultCaptureMaxBytes int64 = 32 << 20
	// DefaultCaptureBodyLimit 是单侧 body（请求或响应）的默认上限。
	DefaultCaptureBodyLimit int64 = 8 << 20
	// DefaultCapturePersistenceTTL 是管理员保存后的捕获 TTL。
	DefaultCapturePersistenceTTL = 24 * time.Hour
	// DefaultCaptureCleanupInterval 是过期捕获的后台清理间隔。
	DefaultCaptureCleanupInterval = 10 * time.Minute
)

const (
	// captureRequestPurpose/captureResponsePurpose 把同一主密钥下的请求与
	// 响应密文绑定到不同用途，防止两侧密文被互换后仍能解密。
	captureRequestPurpose  = secret.PurposeRequestCapture + ":request"
	captureResponsePurpose = secret.PurposeRequestCapture + ":response"
)

var (
	// ErrCaptureNotFound 表示捕获不存在或已过期。
	ErrCaptureNotFound = errors.New("observability: 捕获不存在或已过期")
	// ErrCaptureStoreNotReady 表示捕获持久化缺少数据库或加密信封。
	ErrCaptureStoreNotReady = errors.New("observability: 捕获存储未初始化")
)

// CaptureBody 是一侧 body 的诊断快照。
//
// Data 为 nil 表示该侧没有捕获到任何内容（例如还没有读取请求体）；
// Truncated 为 true 表示原始 body 超过单侧上限，Data 只包含前缀。
type CaptureBody struct {
	Data      []byte
	Truncated bool
}

func (b CaptureBody) bytes() *int64 {
	if b.Data == nil {
		return nil
	}
	size := int64(len(b.Data))
	return &size
}

// CaptureSnapshot 是一次诊断捕获的完整内存视图。
type CaptureSnapshot struct {
	RequestID string
	Request   CaptureBody
	Response  CaptureBody

	CapturedAt time.Time
	ExpiresAt  time.Time
}

// CaptureRingOptions 是诊断环的配置；零值使用默认 5 分钟/50 请求/32MB/8MB。
type CaptureRingOptions struct {
	TTL         time.Duration
	MaxRequests int
	MaxBytes    int64
	BodyLimit   int64
	// Now 用于测试注入时钟；nil 时使用 time.Now。
	Now func() time.Time
}

type captureEntry struct {
	snapshot CaptureSnapshot
	bytes    int64
	seq      uint64
}

// CaptureRing 是按 request_id 索引的内存诊断环。
//
// 所有捕获在 TTL 后不可读；新捕获会先清理过期项，再按最旧优先驱逐，
// 保证请求数与总字节数不超过上限。Ring 可被并发使用。
type CaptureRing struct {
	mu         sync.Mutex
	entries    map[string]*captureEntry
	totalBytes int64
	sequence   uint64

	ttl         time.Duration
	maxRequests int
	maxBytes    int64
	bodyLimit   int64
	now         func() time.Time
}

// NewCaptureRing 构造诊断环；非法或零值配置回退到默认值。
func NewCaptureRing(opts CaptureRingOptions) *CaptureRing {
	if opts.TTL <= 0 {
		opts.TTL = DefaultCaptureTTL
	}
	if opts.MaxRequests <= 0 {
		opts.MaxRequests = DefaultCaptureMaxRequests
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultCaptureMaxBytes
	}
	if opts.BodyLimit <= 0 {
		opts.BodyLimit = DefaultCaptureBodyLimit
	}
	// 一条捕获最多包含请求与响应两侧；限制单侧上限，保证任何单个
	// 请求都不会独自超过诊断环总上限。
	if maxSide := opts.MaxBytes / 2; maxSide >= 1 && opts.BodyLimit > maxSide {
		opts.BodyLimit = maxSide
	}
	if opts.BodyLimit > opts.MaxBytes {
		opts.BodyLimit = opts.MaxBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &CaptureRing{
		entries:     make(map[string]*captureEntry),
		ttl:         opts.TTL,
		maxRequests: opts.MaxRequests,
		maxBytes:    opts.MaxBytes,
		bodyLimit:   opts.BodyLimit,
		now:         opts.Now,
	}
}

var (
	defaultCaptureRingMu sync.Mutex
	defaultCaptureRing   *CaptureRing
)

// DefaultCaptureRing 返回进程级诊断环。
//
// 数据面在请求结束时把 body 写入该环；管理 API 从同一个实例读取。
// 首次调用时按默认上限创建，之后保持不变。
func DefaultCaptureRing() *CaptureRing {
	defaultCaptureRingMu.Lock()
	defer defaultCaptureRingMu.Unlock()
	if defaultCaptureRing == nil {
		defaultCaptureRing = NewCaptureRing(CaptureRingOptions{})
	}
	return defaultCaptureRing
}

// Capture 写入一条完整捕获并返回实际保存的快照。
//
// 非法 request_id 返回 false；body 超过单侧上限时只保留前缀并置
// Truncated。返回的 Data 是副本，调用方修改不会影响环内数据。
func (r *CaptureRing) Capture(requestID string, request, response CaptureBody) (CaptureSnapshot, bool) {
	if r == nil {
		return CaptureSnapshot{}, false
	}
	requestID = strings.TrimSpace(requestID)
	if err := validateRequestID(requestID); err != nil {
		return CaptureSnapshot{}, false
	}

	now := r.now().UTC()
	request = trimCaptureBody(request, r.bodyLimit)
	response = trimCaptureBody(response, r.bodyLimit)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.pruneExpiredLocked(now)
	if existing, ok := r.entries[requestID]; ok {
		r.totalBytes -= existing.bytes
		delete(r.entries, requestID)
	}

	r.sequence++
	entry := &captureEntry{
		snapshot: CaptureSnapshot{
			RequestID:  requestID,
			Request:    request,
			Response:   response,
			CapturedAt: now,
			ExpiresAt:  now.Add(r.ttl),
		},
		bytes: int64(len(request.Data) + len(response.Data)),
		seq:   r.sequence,
	}
	r.entries[requestID] = entry
	r.totalBytes += entry.bytes
	r.evictLocked(requestID)

	return cloneCaptureSnapshot(entry.snapshot), true
}

// Get 返回未过期的捕获副本；不存在或已过期时返回 false。
func (r *CaptureRing) Get(requestID string) (CaptureSnapshot, bool) {
	if r == nil {
		return CaptureSnapshot{}, false
	}
	requestID = strings.TrimSpace(requestID)
	if err := validateRequestID(requestID); err != nil {
		return CaptureSnapshot{}, false
	}

	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.entries[requestID]
	if !ok {
		return CaptureSnapshot{}, false
	}
	if !now.Before(entry.snapshot.ExpiresAt) {
		r.removeLocked(requestID)
		return CaptureSnapshot{}, false
	}
	return cloneCaptureSnapshot(entry.snapshot), true
}

// Delete 删除指定捕获；返回是否确实删除。
func (r *CaptureRing) Delete(requestID string) bool {
	if r == nil {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[requestID]; !ok {
		return false
	}
	r.removeLocked(requestID)
	return true
}

// Len 返回当前未过期的请求数。
func (r *CaptureRing) Len() int {
	if r == nil {
		return 0
	}
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	return len(r.entries)
}

// Bytes 返回当前未过期捕获的 request+response 字节总数。
func (r *CaptureRing) Bytes() int64 {
	if r == nil {
		return 0
	}
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneExpiredLocked(now)
	return r.totalBytes
}

func (r *CaptureRing) pruneExpiredLocked(now time.Time) {
	for requestID, entry := range r.entries {
		if !now.Before(entry.snapshot.ExpiresAt) {
			r.totalBytes -= entry.bytes
			delete(r.entries, requestID)
		}
	}
}

func (r *CaptureRing) removeLocked(requestID string) {
	entry, ok := r.entries[requestID]
	if !ok {
		return
	}
	r.totalBytes -= entry.bytes
	delete(r.entries, requestID)
}

func (r *CaptureRing) evictLocked(protected string) {
	for len(r.entries) > r.maxRequests {
		if !r.evictOldestLocked(protected) {
			return
		}
	}
	for r.totalBytes > r.maxBytes && len(r.entries) > 1 {
		if !r.evictOldestLocked(protected) {
			return
		}
	}
}

func (r *CaptureRing) evictOldestLocked(protected string) bool {
	var (
		oldestID string
		oldest   *captureEntry
	)
	for requestID, entry := range r.entries {
		if requestID == protected {
			continue
		}
		if oldest == nil ||
			entry.snapshot.CapturedAt.Before(oldest.snapshot.CapturedAt) ||
			(entry.snapshot.CapturedAt.Equal(oldest.snapshot.CapturedAt) && entry.seq < oldest.seq) {
			oldestID = requestID
			oldest = entry
		}
	}
	if oldest == nil {
		return false
	}
	r.totalBytes -= oldest.bytes
	delete(r.entries, oldestID)
	return true
}

func trimCaptureBody(body CaptureBody, limit int64) CaptureBody {
	if limit < 0 {
		limit = 0
	}
	if body.Data == nil {
		return CaptureBody{Truncated: body.Truncated}
	}
	if int64(len(body.Data)) > limit {
		data := make([]byte, limit)
		copy(data, body.Data[:limit])
		return CaptureBody{Data: data, Truncated: true}
	}
	data := make([]byte, len(body.Data))
	copy(data, body.Data)
	return CaptureBody{Data: data, Truncated: body.Truncated}
}

func cloneCaptureSnapshot(snapshot CaptureSnapshot) CaptureSnapshot {
	return CaptureSnapshot{
		RequestID:  snapshot.RequestID,
		Request:    cloneCaptureBody(snapshot.Request),
		Response:   cloneCaptureBody(snapshot.Response),
		CapturedAt: snapshot.CapturedAt,
		ExpiresAt:  snapshot.ExpiresAt,
	}
}

func cloneCaptureBody(body CaptureBody) CaptureBody {
	if body.Data == nil {
		return CaptureBody{Truncated: body.Truncated}
	}
	data := make([]byte, len(body.Data))
	copy(data, body.Data)
	return CaptureBody{Data: data, Truncated: body.Truncated}
}

// CaptureBuffer 收集一侧 body，最多保留 limit 字节。
//
// 它实现 io.Writer，可直接交给 io.TeeReader 或 Session.TeeRequest/
// TeeResponse；超出上限后继续吞掉字节（Write 永远返回 len(p)），不阻塞
// 转发，只把 Truncated 置为 true。
type CaptureBuffer struct {
	mu        sync.Mutex
	limit     int64
	data      []byte
	written   bool
	truncated bool
}

// NewCaptureBuffer 构造有上限的 body 缓冲；limit < 0 视为 0。
func NewCaptureBuffer(limit int64) *CaptureBuffer {
	if limit < 0 {
		limit = 0
	}
	return &CaptureBuffer{limit: limit}
}

// Write 实现 io.Writer；不返回短写，避免影响调用方的转发逻辑。
func (b *CaptureBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	b.written = true
	remaining := b.limit - int64(len(b.data))
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	toCopy := int64(len(p))
	if toCopy > remaining {
		toCopy = remaining
		b.truncated = true
	}
	b.data = append(b.data, p[:toCopy]...)
	return len(p), nil
}

// Snapshot 返回当前内容副本。
func (b *CaptureBuffer) Snapshot() CaptureBody {
	if b == nil {
		return CaptureBody{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.written {
		return CaptureBody{Truncated: b.truncated}
	}
	data := make([]byte, len(b.data))
	copy(data, b.data)
	return CaptureBody{Data: data, Truncated: b.truncated}
}

// Len 返回已保留的字节数。
func (b *CaptureBuffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}

// CaptureSession 为一次请求收集 request/response body。
//
// 调用方在请求开始时创建会话，把客户端请求体与上游响应体分别用
// TeeRequest/TeeResponse 包装，请求结束时调用 Commit 把原始字节写入
// 诊断环。Tee 不改变读取到的字节或错误；流式 SSE 按读取顺序原样保存。
type CaptureSession struct {
	ring      *CaptureRing
	requestID string
	request   *CaptureBuffer
	response  *CaptureBuffer

	mu        sync.Mutex
	committed bool
}

// NewSession 为 requestID 创建捕获会话；ring 为 nil 时返回 nil。
//
// requestID 必须与最终写入 request_logs 的 ID 一致，管理端才能按请求 ID
// 找到诊断环中的 body。
func (r *CaptureRing) NewSession(requestID string) *CaptureSession {
	if r == nil {
		return nil
	}
	return &CaptureSession{
		ring:      r,
		requestID: strings.TrimSpace(requestID),
		request:   NewCaptureBuffer(r.bodyLimit),
		response:  NewCaptureBuffer(r.bodyLimit),
	}
}

// RequestBuffer 返回请求侧缓冲，供已经直接读取 body 的调用方写入。
func (s *CaptureSession) RequestBuffer() *CaptureBuffer {
	if s == nil {
		return nil
	}
	return s.request
}

// ResponseBuffer 返回响应侧缓冲，供已经直接读取 body 的调用方写入。
func (s *CaptureSession) ResponseBuffer() *CaptureBuffer {
	if s == nil {
		return nil
	}
	return s.response
}

// TeeRequest 包装客户端请求体，读取时同步写入请求侧缓冲。
func (s *CaptureSession) TeeRequest(body io.ReadCloser) io.ReadCloser {
	if s == nil {
		return body
	}
	return teeReadCloser(body, s.request)
}

// TeeResponse 包装上游响应体，读取时同步写入响应侧缓冲。
func (s *CaptureSession) TeeResponse(body io.ReadCloser) io.ReadCloser {
	if s == nil {
		return body
	}
	return teeReadCloser(body, s.response)
}

// Commit 把当前缓冲写入诊断环；重复调用返回 false。
func (s *CaptureSession) Commit() (CaptureSnapshot, bool) {
	if s == nil || s.ring == nil {
		return CaptureSnapshot{}, false
	}
	s.mu.Lock()
	if s.committed {
		s.mu.Unlock()
		return CaptureSnapshot{}, false
	}
	s.committed = true
	s.mu.Unlock()

	return s.ring.Capture(s.requestID, s.request.Snapshot(), s.response.Snapshot())
}

type teeBodyReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (t *teeBodyReadCloser) Read(p []byte) (int, error) {
	return t.reader.Read(p)
}

func (t *teeBodyReadCloser) Close() error {
	return t.closer.Close()
}

func teeReadCloser(body io.ReadCloser, dst io.Writer) io.ReadCloser {
	if body == nil || dst == nil {
		return body
	}
	return &teeBodyReadCloser{reader: io.TeeReader(body, dst), closer: body}
}

// CaptureStoreOptions 是持久化捕获的配置。
type CaptureStoreOptions struct {
	// TTL 是保存后的存活时间；<=0 时使用 24h。
	TTL time.Duration
	// Now 用于测试注入时钟；nil 时使用 time.Now。
	Now func() time.Time
}

// CaptureStore 负责 request_captures 的加密写入、解密读取、删除与过期清理。
//
// 它复用 secret.Envelope：request/response 两侧使用不同 purpose，
// 同一 request_id 重复保存会覆盖旧捕获并重置 TTL。
type CaptureStore struct {
	db       DB
	envelope *secret.Envelope
	ttl      time.Duration
	now      func() time.Time
}

// NewCaptureStore 构造捕获仓储；envelope 为 nil 时只允许元数据/删除/清理
// 操作，Save/Get 返回 ErrCaptureStoreNotReady。
func NewCaptureStore(db DB, envelope *secret.Envelope, opts CaptureStoreOptions) *CaptureStore {
	if opts.TTL <= 0 {
		opts.TTL = DefaultCapturePersistenceTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &CaptureStore{
		db:       db,
		envelope: envelope,
		ttl:      opts.TTL,
		now:      opts.Now,
	}
}

// CaptureMeta 是 request_captures 的元数据视图，不含密文或明文。
type CaptureMeta struct {
	RequestID string

	RequestTruncated  bool
	ResponseTruncated bool
	RequestBytes      *int64
	ResponseBytes     *int64

	CreatedAt time.Time
	ExpiresAt time.Time
}

// SavedCapture 是解密后的捕获视图；Data nil 表示该侧未保存。
type SavedCapture struct {
	CaptureMeta

	RequestBody  []byte
	ResponseBody []byte
}

const upsertCaptureSQL = `
INSERT INTO request_captures (
	request_id, request_body_ciphertext, response_body_ciphertext,
	request_truncated, response_truncated, request_bytes, response_bytes,
	created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(request_id) DO UPDATE SET
	request_body_ciphertext = excluded.request_body_ciphertext,
	response_body_ciphertext = excluded.response_body_ciphertext,
	request_truncated = excluded.request_truncated,
	response_truncated = excluded.response_truncated,
	request_bytes = excluded.request_bytes,
	response_bytes = excluded.response_bytes,
	created_at = excluded.created_at,
	expires_at = excluded.expires_at`

const selectCaptureSQL = `
SELECT request_truncated, response_truncated, request_bytes, response_bytes,
       request_body_ciphertext, response_body_ciphertext, created_at, expires_at
FROM request_captures
WHERE request_id = ? AND expires_at > ?`

const selectCaptureMetaSQL = `
SELECT request_truncated, response_truncated, request_bytes, response_bytes,
       created_at, expires_at
FROM request_captures
WHERE request_id = ? AND expires_at > ?`

// Save 加密并保存一次诊断快照。
func (s *CaptureStore) Save(ctx context.Context, snapshot CaptureSnapshot) (CaptureMeta, error) {
	if err := s.ready(true); err != nil {
		return CaptureMeta{}, err
	}
	requestID := strings.TrimSpace(snapshot.RequestID)
	if err := validateRequestID(requestID); err != nil {
		return CaptureMeta{}, err
	}
	// 即使调用方绕过了诊断环，也保证落库单侧不超过 8MB；超出的部分
	// 不写入密文，只保留 truncated 标志。
	snapshot.Request = trimCaptureBody(snapshot.Request, DefaultCaptureBodyLimit)
	snapshot.Response = trimCaptureBody(snapshot.Response, DefaultCaptureBodyLimit)

	requestCiphertext, requestBytes, err := s.encryptSide(snapshot.Request, captureRequestPurpose)
	if err != nil {
		return CaptureMeta{}, err
	}
	responseCiphertext, responseBytes, err := s.encryptSide(snapshot.Response, captureResponsePurpose)
	if err != nil {
		return CaptureMeta{}, err
	}

	now := s.now().UTC()
	meta := CaptureMeta{
		RequestID:         requestID,
		RequestTruncated:  snapshot.Request.Truncated,
		ResponseTruncated: snapshot.Response.Truncated,
		RequestBytes:      snapshot.Request.bytes(),
		ResponseBytes:     snapshot.Response.bytes(),
		CreatedAt:         now,
		ExpiresAt:         now.Add(s.ttl),
	}

	_, err = s.db.ExecContext(ctx, upsertCaptureSQL,
		requestID,
		requestCiphertext,
		responseCiphertext,
		boolToInt(meta.RequestTruncated),
		boolToInt(meta.ResponseTruncated),
		requestBytes,
		responseBytes,
		formatTime(meta.CreatedAt),
		formatTime(meta.ExpiresAt),
	)
	if err != nil {
		return CaptureMeta{}, fmt.Errorf("observability: 保存请求捕获失败：%w", err)
	}
	return meta, nil
}

func (s *CaptureStore) encryptSide(body CaptureBody, purpose string) (any, any, error) {
	if body.Data == nil {
		return nil, nil, nil
	}
	ciphertext, err := s.envelope.Encrypt(body.Data, purpose)
	if err != nil {
		return nil, nil, fmt.Errorf("observability: 加密请求捕获失败：%w", err)
	}
	return ciphertext, int64(len(body.Data)), nil
}

// Meta 读取未过期捕获的元数据；不存在或已过期返回 ErrCaptureNotFound。
func (s *CaptureStore) Meta(ctx context.Context, requestID string) (CaptureMeta, error) {
	if err := s.ready(false); err != nil {
		return CaptureMeta{}, err
	}
	requestID = strings.TrimSpace(requestID)
	if err := validateRequestID(requestID); err != nil {
		return CaptureMeta{}, err
	}

	row := s.db.QueryRowContext(ctx, selectCaptureMetaSQL,
		requestID, formatTime(s.now().UTC()))
	meta := CaptureMeta{RequestID: requestID}
	if err := scanCaptureMeta(row, &meta); err != nil {
		return CaptureMeta{}, err
	}
	return meta, nil
}

// Get 解密读取未过期捕获；不存在或已过期返回 ErrCaptureNotFound。
func (s *CaptureStore) Get(ctx context.Context, requestID string) (SavedCapture, error) {
	if err := s.ready(true); err != nil {
		return SavedCapture{}, err
	}
	requestID = strings.TrimSpace(requestID)
	if err := validateRequestID(requestID); err != nil {
		return SavedCapture{}, err
	}

	row := s.db.QueryRowContext(ctx, selectCaptureSQL,
		requestID, formatTime(s.now().UTC()))

	var (
		requestCiphertext  sql.NullString
		responseCiphertext sql.NullString
		meta               = CaptureMeta{RequestID: requestID}
	)
	if err := scanCaptureMetaWithCiphertext(row, &meta, &requestCiphertext, &responseCiphertext); err != nil {
		return SavedCapture{}, err
	}

	saved := SavedCapture{CaptureMeta: meta}
	if requestCiphertext.Valid {
		plaintext, err := s.envelope.Decrypt(requestCiphertext.String, captureRequestPurpose)
		if err != nil {
			return SavedCapture{}, fmt.Errorf("observability: 解密请求 body 失败：%w", err)
		}
		saved.RequestBody = plaintext
	}
	if responseCiphertext.Valid {
		plaintext, err := s.envelope.Decrypt(responseCiphertext.String, captureResponsePurpose)
		if err != nil {
			return SavedCapture{}, fmt.Errorf("observability: 解密响应 body 失败：%w", err)
		}
		saved.ResponseBody = plaintext
	}
	return saved, nil
}

// Delete 删除指定捕获；返回是否确实删除。已过期但尚未清理的行也会被删除。
func (s *CaptureStore) Delete(ctx context.Context, requestID string) (bool, error) {
	if err := s.ready(false); err != nil {
		return false, err
	}
	requestID = strings.TrimSpace(requestID)
	if err := validateRequestID(requestID); err != nil {
		return false, err
	}

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM request_captures WHERE request_id = ?`, requestID)
	if err != nil {
		return false, fmt.Errorf("observability: 删除请求捕获失败：%w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("observability: 读取请求捕获删除结果失败：%w", err)
	}
	return deleted > 0, nil
}

// PruneExpired 删除 expires_at <= now 的捕获，返回删除行数。
func (s *CaptureStore) PruneExpired(ctx context.Context, now time.Time) (int64, error) {
	if err := s.ready(false); err != nil {
		return 0, err
	}
	if now.IsZero() {
		now = s.now()
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM request_captures WHERE expires_at <= ?`, formatTime(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("observability: 清理过期请求捕获失败：%w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("observability: 读取捕获清理结果失败：%w", err)
	}
	return deleted, nil
}

// RunCleanup 周期清理过期捕获，直到 ctx 取消。
//
// 启动时立即清理一次，之后每个 interval 清理一次；interval <= 0 时使用
// 默认 10 分钟。调用方应放在独立 goroutine 中；onError 可为 nil。
func (s *CaptureStore) RunCleanup(ctx context.Context, interval time.Duration, onError func(error)) {
	if s == nil || s.db == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = DefaultCaptureCleanupInterval
	}

	prune := func() {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.PruneExpired(ctx, s.now()); err != nil &&
			ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}

	prune()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

type captureScanner interface {
	Scan(dest ...any) error
}

func scanCaptureMeta(scanner captureScanner, meta *CaptureMeta) error {
	var (
		requestTruncated  int
		responseTruncated int
		requestBytes      sql.NullInt64
		responseBytes     sql.NullInt64
		createdAt         string
		expiresAt         string
	)
	if err := scanner.Scan(&requestTruncated, &responseTruncated,
		&requestBytes, &responseBytes, &createdAt, &expiresAt); err != nil {
		return wrapCaptureScanError(err)
	}
	return applyCaptureMeta(meta, requestTruncated, responseTruncated,
		requestBytes, responseBytes, createdAt, expiresAt)
}

func scanCaptureMetaWithCiphertext(
	scanner captureScanner,
	meta *CaptureMeta,
	requestCiphertext, responseCiphertext *sql.NullString,
) error {
	var (
		requestTruncated  int
		responseTruncated int
		requestBytes      sql.NullInt64
		responseBytes     sql.NullInt64
		createdAt         string
		expiresAt         string
	)
	if err := scanner.Scan(&requestTruncated, &responseTruncated,
		&requestBytes, &responseBytes, requestCiphertext, responseCiphertext,
		&createdAt, &expiresAt); err != nil {
		return wrapCaptureScanError(err)
	}
	return applyCaptureMeta(meta, requestTruncated, responseTruncated,
		requestBytes, responseBytes, createdAt, expiresAt)
}

func applyCaptureMeta(
	meta *CaptureMeta,
	requestTruncated, responseTruncated int,
	requestBytes, responseBytes sql.NullInt64,
	createdAt, expiresAt string,
) error {
	created, err := parseCaptureTime(createdAt)
	if err != nil {
		return err
	}
	expires, err := parseCaptureTime(expiresAt)
	if err != nil {
		return err
	}

	meta.RequestTruncated = requestTruncated != 0
	meta.ResponseTruncated = responseTruncated != 0
	if requestBytes.Valid {
		value := requestBytes.Int64
		meta.RequestBytes = &value
	}
	if responseBytes.Valid {
		value := responseBytes.Int64
		meta.ResponseBytes = &value
	}
	meta.CreatedAt = created
	meta.ExpiresAt = expires
	return nil
}

func wrapCaptureScanError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCaptureNotFound
	}
	return fmt.Errorf("observability: 读取请求捕获失败：%w", err)
}

func parseCaptureTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(TimeLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("observability: 请求捕获时间格式非法：%w", err)
	}
	return parsed.UTC(), nil
}

func (s *CaptureStore) ready(requireEnvelope bool) error {
	if s == nil || s.db == nil {
		return ErrCaptureStoreNotReady
	}
	if requireEnvelope && s.envelope == nil {
		return ErrCaptureStoreNotReady
	}
	return nil
}
