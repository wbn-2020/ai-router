// Package adminapi 实现管理端 REST API：管理员登录、登出、session 管理与
// 同源 Origin 校验。后续任务在同一 Handler 上追加渠道、模型映射、统计等端点。
//
// 安全约束（见计划 T06）：
//   - 所有 POST/PUT/PATCH/DELETE 必须携带同源 Origin，跨源请求返回 403；
//   - 所有响应 Cache-Control: no-store；
//   - session cookie 为 HttpOnly + SameSite=Lax，Secure 可配，Max-Age 12h；
//   - 错误与日志不包含密码、session token 或上游 Key。
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-router/internal/adminauth"
)

const (
	// sessionCookieName 是管理端 session cookie 的名称。cookie 为 host-only，
	// 不设置 Domain，Path=/ 以便整个管理端共享。
	sessionCookieName = "gateway_admin_session"
	// maxAdminJSONBody 限制管理端 JSON 请求体大小；管理端只接收小型表单。
	maxAdminJSONBody int64 = 64 << 10
	// sessionTimeLayout 与 adminauth/0001 迁移中的时间格式一致。
	sessionTimeLayout = time.RFC3339Nano
)

// Options 是管理 API 的构造参数。
type Options struct {
	// Auth 是必需的管理员鉴权管理器。
	Auth *adminauth.Manager
	// DB 用于记录和展示 admin_sessions.last_seen_at；nil 时跳过该功能。
	// 应与 Auth 使用同一个数据库。
	DB adminauth.DB
	// CookieSecure 控制 session cookie 的 Secure 标志；HTTPS 部署必须为 true。
	CookieSecure bool
	// Logger 为 nil 时使用 slog.Default()。
	Logger *slog.Logger
}

// API 是管理端 REST API 的 http.Handler。
//
// API 在构造后不可变，可被并发使用；后续任务通过同包文件注册更多路由。
type API struct {
	auth         *adminauth.Manager
	db           adminauth.DB
	cookieSecure bool
	logger       *slog.Logger
	mux          *http.ServeMux
}

// New 构造管理 API；不会执行数据库迁移或创建管理员。
func New(opts Options) (*API, error) {
	if opts.Auth == nil {
		return nil, errors.New("adminapi: adminauth.Manager 不能为空")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	a := &API{
		auth:         opts.Auth,
		db:           opts.DB,
		cookieSecure: opts.CookieSecure,
		logger:       opts.Logger,
		mux:          http.NewServeMux(),
	}
	a.registerAuthRoutes()
	for _, register := range extraRouteRegistrars {
		register(a)
	}
	a.registerFallbackRoutes()
	return a, nil
}

// extraRouteRegistrars 允许同包的其他任务文件通过 init 注册管理端点。
//
// 计划把 adminapi 拆成多个互不改写的文件（channels.go、routes.go 等）；
// registerExtraRoutes 让后续文件无需修改本文件即可挂载路由。注册在包初始化
// 阶段完成，New 时按声明顺序应用，最后统一注册 JSON 404 fallback。
var extraRouteRegistrars []func(*API)

// registerExtraRoutes 登记一个路由构造器；仅供同包文件在 init 中使用。
func registerExtraRoutes(register func(*API)) {
	if register == nil {
		return
	}
	extraRouteRegistrars = append(extraRouteRegistrars, register)
}

// ServeHTTP 实现 http.Handler。
//
// Origin 校验位于路由之前：所有写方法（包括未注册的路径）先经过同源检查，
// 避免跨源请求探测端点或触发副作用。
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setNoStore(w.Header())
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if isWriteMethod(r.Method) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross_origin_request",
			"写请求必须来自同源 Origin")
		return
	}
	a.mux.ServeHTTP(w, r)
}

// isWriteMethod 返回请求方法是否会改变服务端状态。
//
// OPTIONS 是浏览器预检/只读请求，不属于写方法；本服务不启用 CORS。
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// sameOrigin 判断请求的 Origin 是否与请求 Host 同源。
//
// 只比较主机名与显式非默认端口，不比较 scheme：生产环境由 TLS 反向代理终止，
// 后端看到的 r.Host 与浏览器 Origin 的 scheme 可能不同，但主机必须一致。
// Origin 缺失、为 "null"、包含路径/用户信息或非法 scheme 时一律拒绝。
func sameOrigin(r *http.Request) bool {
	originHost, ok := parseOriginHost(r.Header.Get("Origin"))
	if !ok {
		return false
	}
	return originHost == normalizeHostPort(r.Host)
}

// parseOriginHost 解析 Origin 头并返回规范化后的 host[:port]。
func parseOriginHost(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	// Origin 序列化后不带 path/query/fragment；带这些内容说明不是标准 Origin。
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return normalizeHostPort(parsed.Host), true
}

// normalizeHostPort 统一 host 大小写并去掉默认端口。
//
// 无法拆出端口时按裸主机处理（覆盖无端口 Host 与 IPv6 字面量）。
func normalizeHostPort(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.ToLower(strings.Trim(hostport, "[]"))
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if port == "" || port == "80" || port == "443" {
		return host
	}
	return net.JoinHostPort(host, port)
}

// requireSession 校验 admin session cookie，并把 session 与 token 放入请求上下文。
//
// 认证失败统一返回 401，不区分 token 不存在、过期或已撤销，避免成为 token
// 探测入口；认证成功时顺带更新 last_seen_at（尽力而为，不影响请求）。
// 失败时不改写 cookie：401 可能来自跨站顶层导航触发的 GET，写 Set-Cookie
// 会变成强制登出；过期 cookie 由登录或显式登出覆盖。
func (a *API) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := a.sessionToken(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "需要管理员登录")
			return
		}

		session, err := a.auth.ValidateSession(r.Context(), token)
		if err != nil {
			if isSessionAuthError(err) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "session 无效或已过期")
				return
			}
			a.logger.Error("校验管理员 session 失败", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
			return
		}

		a.touchSession(r.Context(), session.ID)
		session.Current = true
		ctx := context.WithValue(r.Context(), sessionContextKey{}, sessionContext{
			session: session,
			token:   token,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sessionToken 读取并做基本校验的 session token。
func (a *API) sessionToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", adminauth.ErrInvalidSession
	}
	return cookie.Value, nil
}

// isSessionAuthError 判断错误是否属于“客户端凭据无效”而非数据库故障。
func isSessionAuthError(err error) bool {
	return errors.Is(err, adminauth.ErrInvalidSession) ||
		errors.Is(err, adminauth.ErrSessionExpired) ||
		errors.Is(err, adminauth.ErrSessionRevoked)
}

// touchSession 尽力更新 session 的 last_seen_at；失败只记 debug 日志。
func (a *API) touchSession(ctx context.Context, sessionID string) {
	if a.db == nil || sessionID == "" {
		return
	}
	updateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := a.db.ExecContext(updateCtx, updateSessionLastSeenSQL,
		time.Now().UTC().Format(sessionTimeLayout), sessionID)
	if err != nil {
		a.logger.Debug("更新管理员 session last_seen_at 失败", "error", err)
	}
}

type sessionContextKey struct{}

type sessionContext struct {
	session adminauth.Session
	token   string
}

// SessionFromContext 返回 requireSession 写入的当前 session。
//
// 供后续 adminapi 端点复用于审计或展示；未经过 session 中间件时返回 false。
func SessionFromContext(ctx context.Context) (adminauth.Session, bool) {
	value, ok := ctx.Value(sessionContextKey{}).(sessionContext)
	if !ok {
		return adminauth.Session{}, false
	}
	return value.session, true
}

// sessionTokenFromContext 返回当前请求的原始 token（只用于撤销/改密）。
func sessionTokenFromContext(ctx context.Context) string {
	value, _ := ctx.Value(sessionContextKey{}).(sessionContext)
	return value.token
}

// setSessionCookie 写入登录成功后的 session cookie。
func (a *API) setSessionCookie(w http.ResponseWriter, result adminauth.LoginResult) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    result.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(adminauth.DefaultSessionTTL / time.Second),
		Expires:  result.ExpiresAt,
	})
}

// clearSessionCookie 删除浏览器端 session cookie。
func (a *API) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
	})
}

// clientSource 返回登录限流使用来源（客户端 IP）。
//
// 反向代理部署时所有请求的来源都是代理地址，限流退化为全局限流；单管理员
// 场景下仍可接受，且不会信任可伪造的 X-Forwarded-For。
func clientSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		if r.RemoteAddr == "" {
			return "unknown"
		}
		return r.RemoteAddr
	}
	if host == "" {
		return "unknown"
	}
	return host
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

// setNoStore 禁止浏览器或中间代理缓存管理端响应。
func setNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	header := w.Header()
	header.Set("Content-Type", "application/json; charset=utf-8")
	setNoStore(header)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: code, Message: message})
}

// decodeJSONBody 解码单个 JSON 值并应用管理端请求体上限。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminJSONBody))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求体包含多个 JSON 值")
	}
	return nil
}

// writeDecodeError 把 JSON 解码失败映射为稳定错误码，不回显请求体内容。
func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "请求体过大")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "请求体必须为合法 JSON")
}
