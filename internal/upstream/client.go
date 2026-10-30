// Package upstream 实现数据面唯一的出站边界：构造并发送原生 OpenAI
// Responses / Chat Completions 请求，以及渠道连通性探针。
//
// 边界与安全约束：
//   - 只做原生协议直通，不做 Responses↔Chat↔Anthropic 转换；协议不支持时
//     直接拒绝，不调用上游；
//   - 请求体只替换顶层 model，tools、input_image、reasoning 与未知字段
//     原样保留；
//   - 不转发客户端 Authorization、Cookie、x-api-key 与 hop-by-hop 头；
//   - 上游响应头只保留白名单中的安全字段，Set-Cookie 与 hop-by-hop 头不会
//     进入客户端响应；
//   - 拒绝跨主机/跨协议重定向；超时返回可 errors.Is 分类的稳定错误；
//   - 错误与 Target 的 String 输出不包含上游 Key、base URL 查询串或上游
//     响应 body。
//
// SSE 事件解析由 T21 的 sse.go 负责；本包只负责流式 body 的可读性、空闲
// 超时与总时限。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/secret"
	"ai-router/internal/store"
)

// maxRedirects 是同一 host 内允许跟随的最大重定向次数。
const maxRedirects = 5

// 稳定的错误分类。调用方（调度/失败切换）只依赖这些哨兵判断是否重试，
// 不解析错误文本。
var (
	// ErrInvalidBaseURL 表示渠道 base URL 不满足 http/https、无 userinfo、
	// 无 fragment、有主机名等约束。
	ErrInvalidBaseURL = errors.New("upstream: 上游 base URL 无效")
	// ErrUnsupportedProtocol 表示渠道协议没有对应的原生 adapter。
	ErrUnsupportedProtocol = errors.New("upstream: 不支持的上游渠道协议")
	// ErrInvalidRequest 表示上游请求本身不合法（空 Key、空模型、非 JSON 对象 body 等）。
	ErrInvalidRequest = errors.New("upstream: 上游请求无效")
	// ErrTimeout 表示连接、TLS、响应头、非流式总时限或流式总时限超时。
	ErrTimeout = errors.New("upstream: 上游请求超时")
	// ErrCanceled 表示调用方取消了请求（例如客户端断开）。
	ErrCanceled = errors.New("upstream: 上游请求已取消")
	// ErrStreamIdleTimeout 表示流式读取连续超过 StreamIdleTimeout 没有数据。
	ErrStreamIdleTimeout = errors.New("upstream: 上游流读取空闲超时")
	// ErrTransport 表示连接失败、DNS、TLS 或其他传输层错误。
	ErrTransport = errors.New("upstream: 上游连接失败")
	// ErrCrossHostRedirect 表示上游试图重定向到其他 host 或协议。
	ErrCrossHostRedirect = errors.New("upstream: 拒绝跨主机重定向")
	// ErrTooManyRedirects 表示同一 host 内重定向次数过多。
	ErrTooManyRedirects = errors.New("upstream: 上游重定向次数过多")
)

// Options 是上游 HTTP client 的超时配置。
//
// 零值表示使用 DefaultOptions 中对应的默认值；负数在 NewClient 中拒绝。
type Options struct {
	// ConnectTimeout 是 TCP 建连超时，默认 10s。
	ConnectTimeout time.Duration
	// TLSHandshakeTimeout 是 TLS 握手超时，默认 10s。
	TLSHandshakeTimeout time.Duration
	// ResponseHeaderTimeout 是等待上游响应头的超时，默认 180s。
	ResponseHeaderTimeout time.Duration
	// StreamIdleTimeout 是流式响应两次读到数据之间的最大空闲时间，默认 300s。
	StreamIdleTimeout time.Duration
	// RequestTimeout 是非流式请求从发送到读完 body 的总时限，默认 300s。
	RequestTimeout time.Duration
	// StreamTimeout 是流式请求从发送到读完 body 的总时限，默认 15m。
	StreamTimeout time.Duration
}

// DefaultOptions 返回计划约定的上游超时默认值。
func DefaultOptions() Options {
	return Options{
		ConnectTimeout:        config.DefaultUpstreamConnectTimeout,
		TLSHandshakeTimeout:   config.DefaultUpstreamTLSHandshakeTimeout,
		ResponseHeaderTimeout: config.DefaultUpstreamResponseHeaderTimeout,
		StreamIdleTimeout:     config.DefaultUpstreamStreamIdleTimeout,
		RequestTimeout:        config.DefaultUpstreamRequestTimeout,
		StreamTimeout:         config.DefaultUpstreamStreamTimeout,
	}
}

// OptionsFromConfig 从进程配置提取上游超时选项，避免调用方逐字段映射时
// 漏掉某一项。
func OptionsFromConfig(cfg config.Config) Options {
	return Options{
		ConnectTimeout:        cfg.UpstreamConnectTimeout,
		TLSHandshakeTimeout:   cfg.UpstreamTLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.UpstreamResponseHeaderTimeout,
		StreamIdleTimeout:     cfg.UpstreamStreamIdleTimeout,
		RequestTimeout:        cfg.UpstreamRequestTimeout,
		StreamTimeout:         cfg.UpstreamStreamTimeout,
	}
}

// Client 是上游 HTTP 客户端。构造后字段不可变，可被并发使用。
type Client struct {
	httpClient *http.Client

	streamIdleTimeout time.Duration
	requestTimeout    time.Duration
	streamTimeout     time.Duration
}

// NewClient 构造上游客户端并配置 Transport 超时与重定向策略。
func NewClient(opts Options) (*Client, error) {
	defaults := DefaultOptions()

	connect, err := resolveTimeout("ConnectTimeout", opts.ConnectTimeout, defaults.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	tlsHandshake, err := resolveTimeout("TLSHandshakeTimeout", opts.TLSHandshakeTimeout, defaults.TLSHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	header, err := resolveTimeout("ResponseHeaderTimeout", opts.ResponseHeaderTimeout, defaults.ResponseHeaderTimeout)
	if err != nil {
		return nil, err
	}
	streamIdle, err := resolveTimeout("StreamIdleTimeout", opts.StreamIdleTimeout, defaults.StreamIdleTimeout)
	if err != nil {
		return nil, err
	}
	request, err := resolveTimeout("RequestTimeout", opts.RequestTimeout, defaults.RequestTimeout)
	if err != nil {
		return nil, err
	}
	stream, err := resolveTimeout("StreamTimeout", opts.StreamTimeout, defaults.StreamTimeout)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:   connect,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   tlsHandshake,
		ResponseHeaderTimeout: header,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		httpClient: &http.Client{
			Transport:     transport,
			CheckRedirect: rejectCrossHostRedirect,
		},
		streamIdleTimeout: streamIdle,
		requestTimeout:    request,
		streamTimeout:     stream,
	}, nil
}

func resolveTimeout(name string, value, fallback time.Duration) (time.Duration, error) {
	if value < 0 {
		return 0, fmt.Errorf("upstream: %s 不能为负数", name)
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}

// adapter 是原生渠道协议的静态描述。
//
// 第一版只有 openai_responses 与 openai_chat；anthropic_messages 仅作为
// 渠道枚举预留，adapterFor 会拒绝，避免在协议证据不足时臆测实现。
type adapter interface {
	protocol() string
	path() string
	probeBody(upstreamModel string) ([]byte, error)
}

// adapterFor 返回协议对应的原生适配器；不支持时返回 ErrUnsupportedProtocol。
func adapterFor(protocol string) (adapter, error) {
	switch strings.TrimSpace(protocol) {
	case store.ChannelProtocolOpenAIResponses:
		return responsesAdapter{}, nil
	case store.ChannelProtocolOpenAIChat:
		return chatAdapter{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedProtocol, protocol)
	}
}

// Target 描述一次上游调用的渠道目标。
//
// APIKey 是解密后的明文，只在调用期间存在于内存。Target 未导出任何会
// 输出明文的格式化路径：String/GoString 永远脱敏 Key 并去掉 base URL 的
// 查询串与 userinfo。
type Target struct {
	ChannelID   int64
	ChannelName string
	BaseURL     string
	Protocol    string
	// APIKey 永远不参与 JSON 序列化；String/GoString 也会脱敏。
	APIKey string `json:"-"`
}

// String 实现 fmt.Stringer，避免日志或错误格式化时泄漏 Key 与查询串。
func (t Target) String() string {
	return fmt.Sprintf(
		"upstream.Target{channel_id:%d channel_name:%q protocol:%q base_url:%q api_key:%s}",
		t.ChannelID,
		t.ChannelName,
		t.Protocol,
		redactBaseURL(t.BaseURL),
		secret.RedactedMarker,
	)
}

// GoString 实现 fmt.GoStringer，覆盖 %#v 输出。
func (t Target) GoString() string {
	return t.String()
}

// Request 是一次原生上游请求的输入。
//
// Body 是客户端原始 JSON；Send 只替换顶层 model，其余字段保持语义不变。
// Headers 是入站请求头，Send 会过滤敏感头与 hop-by-hop 头后再转发。
type Request struct {
	// UpstreamModel 是渠道映射后的上游模型名。
	UpstreamModel string
	// Body 是客户端原始 JSON 请求体。
	Body []byte
	// Headers 是客户端请求头；可为 nil。
	Headers http.Header
	// Stream 决定使用流式总时限与读空闲超时；调用方必须与 body 中的
	// stream 字段保持一致。
	Stream bool
}

// Send 构造并发送一次上游 POST 请求。
//
// 返回的 *http.Response.Body 已包装：
//   - 非流式请求受 RequestTimeout 总时限约束；
//   - 流式请求受 StreamTimeout 总时限与 StreamIdleTimeout 空闲超时约束；
//   - 响应头已过滤为安全白名单；
//   - 调用方必须关闭 Body，以释放 context 与底层连接。
//
// 非 2xx 响应不是错误，由调用方按状态码分类与重试。
func (c *Client) Send(ctx context.Context, target Target, req Request) (*http.Response, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("upstream: client 未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	adapter, err := adapterFor(target.Protocol)
	if err != nil {
		return nil, err
	}

	key := strings.TrimSpace(target.APIKey)
	if key == "" {
		return nil, fmt.Errorf("%w: 上游 Key 为空", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.UpstreamModel) == "" {
		return nil, fmt.Errorf("%w: 上游模型为空", ErrInvalidRequest)
	}

	endpoint, err := buildEndpointURL(target.BaseURL, adapter.path())
	if err != nil {
		return nil, err
	}
	body, err := rewriteModel(req.Body, req.UpstreamModel)
	if err != nil {
		return nil, err
	}

	timeout := c.requestTimeout
	if req.Stream {
		timeout = c.streamTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: 无法构造上游请求", ErrInvalidRequest)
	}
	httpReq.Header = filterRequestHeaders(req.Headers)
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	// 始终用上游 Key 覆盖任何客户端凭据；filterRequestHeaders 已移除
	// Authorization，这里再做一次显式设置，避免未来过滤逻辑改动引入泄漏。
	httpReq.Header.Set("Authorization", "Bearer "+key)

	response, err := c.httpClient.Do(httpReq)
	if err != nil {
		// 必须在 cancel 之前分类，否则 requestCtx.Err() 会变成 Canceled，
		// 把真实的总时限超时误判为调用方取消。
		classified := classifyDoError(requestCtx, err)
		cancel()
		return nil, classified
	}

	response.Header = FilterResponseHeaders(response.Header)

	// 非流式请求没有读空闲限制，但仍需要让 context 随 Body.Close 释放。
	idle := time.Duration(0)
	if req.Stream {
		idle = c.streamIdleTimeout
	}
	response.Body = newManagedBody(requestCtx, response.Body, cancel, idle)
	return response, nil
}

// FilterResponseHeaders 返回只包含安全白名单字段的响应头副本。
//
// 白名单之外的字段（包括 Set-Cookie 与 Connection、Transfer-Encoding、
// Upgrade、Keep-Alive、Trailer 等 hop-by-hop 头）全部丢弃。返回值是新的
// Header，修改它不会影响入参。
func FilterResponseHeaders(src http.Header) http.Header {
	filtered := make(http.Header)
	for name, values := range src {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if _, ok := forwardedResponseHeaders[canonical]; !ok {
			continue
		}
		filtered[canonical] = append([]string(nil), values...)
	}
	return filtered
}

// forwardedResponseHeaders 是允许回传客户端的响应头白名单。
//
// Content-Type 决定 SSE/JSON 的解析方式；Cache-Control 与 Retry-After 是
// 客户端可安全使用的缓存/重试提示；X-Request-Id 用于排障关联。
var forwardedResponseHeaders = map[string]struct{}{
	"Content-Type":  {},
	"Cache-Control": {},
	"Retry-After":   {},
	"X-Request-Id":  {},
}

// blockedRequestHeaders 是永不转发到上游的请求头。
//
// 除客户端凭据外，这里覆盖 RFC 7230 的 hop-by-hop 头；Accept-Encoding 也
// 被移除，让 Go Transport 自行协商 gzip 并透明解压，避免把压缩后的
// body 当作原文回传。
var blockedRequestHeaders = map[string]struct{}{
	"Authorization":       {},
	"Proxy-Authorization": {},
	"Proxy-Authenticate":  {},
	"X-Api-Key":           {},
	"Api-Key":             {},
	"Cookie":              {},
	"Cookie2":             {},
	"Set-Cookie":          {},
	"Host":                {},
	"Content-Length":      {},
	"Content-Encoding":    {},
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Connection":    {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"Accept-Encoding":     {},
}

// filterRequestHeaders 复制入站请求头并移除敏感头、hop-by-hop 头以及
// Connection 头中列出的扩展字段。
func filterRequestHeaders(src http.Header) http.Header {
	filtered := make(http.Header, len(src))
	if len(src) == 0 {
		return filtered
	}

	connectionFields := make(map[string]struct{})
	for name, values := range src {
		if textproto.CanonicalMIMEHeaderKey(name) != "Connection" {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(token))
				if canonical != "" {
					connectionFields[canonical] = struct{}{}
				}
			}
		}
	}

	for name, values := range src {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if _, blocked := blockedRequestHeaders[canonical]; blocked {
			continue
		}
		if _, blocked := connectionFields[canonical]; blocked {
			continue
		}
		filtered[canonical] = append([]string(nil), values...)
	}
	return filtered
}

// buildEndpointURL 校验渠道 base URL 并把 adapter 路径拼到已有路径之后。
//
// base URL 可以带查询串（部分中转站用查询串携带 api-version），查询串会
// 原样保留；fragment、userinfo、非 http/https 与缺失主机名均拒绝。错误
// 信息只描述规则，不回显原始 URL。
func buildEndpointURL(baseURL, endpointPath string) (string, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return "", fmt.Errorf("%w: 不能为空", ErrInvalidBaseURL)
	}
	if endpointPath == "" || !strings.HasPrefix(endpointPath, "/") {
		return "", errors.New("upstream: adapter 路径非法")
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" {
		return "", fmt.Errorf("%w: 不是合法的 URL", ErrInvalidBaseURL)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: 只允许 http/https", ErrInvalidBaseURL)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%w: 缺少主机名", ErrInvalidBaseURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: 不能包含用户信息", ErrInvalidBaseURL)
	}
	// url.Parse 会把 “...#” 解析为空 fragment；按字符检查保证任何 # 都被拒绝。
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.ContainsRune(raw, '#') {
		return "", fmt.Errorf("%w: 不能包含 fragment", ErrInvalidBaseURL)
	}

	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/") + endpointPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

// rewriteModel 用 map[string]json.RawMessage 解码请求体，只替换顶层 model。
//
// RawMessage 保留每个字段的原始 JSON 值（含 tools、input_image、reasoning
// 与未知字段）；重新序列化会丢失字段顺序与空白，但语义不变。非 JSON 对象、
// null 与空 body 均返回 ErrInvalidRequest。
func rewriteModel(body []byte, upstreamModel string) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("%w: 请求体为空", ErrInvalidRequest)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%w: 请求体必须是 JSON 对象", ErrInvalidRequest)
	}

	encodedModel, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("%w: 无法序列化 model", ErrInvalidRequest)
	}
	fields["model"] = json.RawMessage(encodedModel)

	rewritten, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%w: 无法序列化请求体", ErrInvalidRequest)
	}
	return rewritten, nil
}

// rejectCrossHostRedirect 只允许同 scheme 同 host（含端口）的重定向，并限制
// 总次数。跨主机重定向被拒绝，Authorization 不会离开原 host。
func rejectCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= maxRedirects {
		return ErrTooManyRedirects
	}
	origin := via[0].URL
	target := req.URL
	if origin == nil || target == nil {
		return ErrCrossHostRedirect
	}
	if !strings.EqualFold(origin.Scheme, target.Scheme) ||
		!strings.EqualFold(origin.Host, target.Host) {
		return ErrCrossHostRedirect
	}
	return nil
}

// classifyDoError 把 http.Client.Do 的错误映射为稳定分类，并去除可能包含
// 查询串的 URL 详情。
func classifyDoError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrCrossHostRedirect):
		return fmt.Errorf("%w: 上游重定向到其他主机或协议", ErrCrossHostRedirect)
	case errors.Is(err, ErrTooManyRedirects):
		return fmt.Errorf("%w: 上游重定向次数超过 %d 次", ErrTooManyRedirects, maxRedirects)
	}

	if errors.Is(err, context.DeadlineExceeded) ||
		(ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) ||
		isTimeout(err) {
		return fmt.Errorf("%w: 等待上游响应超时", ErrTimeout)
	}
	if errors.Is(err, context.Canceled) ||
		(ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return fmt.Errorf("%w: 上游请求已取消", ErrCanceled)
	}
	return fmt.Errorf("%w: %s", ErrTransport, sanitizeTransportError(err))
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// sanitizeTransportError 去掉 *url.Error 包装（其 Error 含完整 URL 与查询串），
// 再对剩余文本做统一脱敏。
func sanitizeTransportError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	message := strings.Join(strings.Fields(secret.RedactValue(err.Error())), " ")
	if message == "" {
		return "未知传输错误"
	}
	return message
}

// redactBaseURL 返回去掉 userinfo、查询串与 fragment 的 URL，用于日志。
func redactBaseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "<invalid>"
	}
	copyURL := *parsed
	copyURL.User = nil
	copyURL.RawQuery = ""
	copyURL.ForceQuery = false
	copyURL.Fragment = ""
	copyURL.RawFragment = ""
	return copyURL.String()
}

// managedBody 负责：
//   - 响应 body 关闭时释放请求 context；
//   - 流式 body 的读空闲超时（空闲时主动关闭底层连接）；
//   - 把总时限/取消导致的读取错误映射为 ErrTimeout / ErrCanceled。
type managedBody struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	ctx    context.Context
	idle   time.Duration
	timer  *time.Timer

	mu       sync.Mutex
	timedOut bool
	closed   bool
}

func newManagedBody(ctx context.Context, body io.ReadCloser, cancel context.CancelFunc, idle time.Duration) *managedBody {
	managed := &managedBody{
		body:   body,
		cancel: cancel,
		ctx:    ctx,
		idle:   idle,
	}
	if idle > 0 {
		managed.timer = time.AfterFunc(idle, managed.expire)
	}
	return managed
}

// expire 在流式读取空闲超时后关闭底层 body，使阻塞中的 Read 立即返回。
func (b *managedBody) expire() {
	b.mu.Lock()
	if b.closed || b.timedOut {
		b.mu.Unlock()
		return
	}
	b.timedOut = true
	b.mu.Unlock()

	_ = b.body.Close()
}

func (b *managedBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)

	b.mu.Lock()
	timedOut := b.timedOut
	closed := b.closed
	if err == nil && !timedOut && !closed && b.timer != nil {
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()

	if err == nil || closed {
		return n, err
	}
	if timedOut {
		return n, fmt.Errorf("%w: 连续 %s 未收到数据", ErrStreamIdleTimeout, b.idle)
	}
	if b.ctx != nil {
		switch {
		case errors.Is(b.ctx.Err(), context.DeadlineExceeded):
			return n, fmt.Errorf("%w: 上游请求超过总时限", ErrTimeout)
		case errors.Is(b.ctx.Err(), context.Canceled):
			return n, fmt.Errorf("%w: 上游请求已取消", ErrCanceled)
		}
	}
	return n, err
}

// Close 幂等；关闭底层 body 并释放请求 context。
func (b *managedBody) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	timer := b.timer
	b.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	err := b.body.Close()
	b.cancel()
	return err
}
