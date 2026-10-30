package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ai-router/internal/adminauth"
)

// 管理端 auth 端点使用的 SQL。全部参数化，不拼接外部输入。
const (
	// updateSessionLastSeenSQL 由 requireSession 在每次认证成功后调用。
	updateSessionLastSeenSQL = `UPDATE admin_sessions SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`
	// selectSessionLastSeenByIDSQL 用于 GET /auth/session 返回当前 session 的元数据。
	selectSessionLastSeenByIDSQL = `SELECT last_seen_at FROM admin_sessions WHERE id = ?`
	// selectSessionsLastSeenSQL 一次取出所有活跃 session 的 last_seen_at，避免 N+1。
	selectSessionsLastSeenSQL = `SELECT id, last_seen_at FROM admin_sessions WHERE last_seen_at IS NOT NULL AND revoked_at IS NULL`
)

// defaultAdminIdentity 是单管理员身份；schema 中只有 id=1 一行，没有用户名。
var defaultAdminIdentity = adminIdentity{ID: 1, Role: "admin"}

type adminIdentity struct {
	ID   int    `json:"id"`
	Role string `json:"role"`
}

type sessionView struct {
	ID         string     `json:"id"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Current    bool       `json:"current"`
}

type sessionEnvelope struct {
	Authenticated bool          `json:"authenticated"`
	Admin         adminIdentity `json:"admin"`
	Session       sessionView   `json:"session"`
}

type sessionsEnvelope struct {
	Sessions []sessionView `json:"sessions"`
}

type revokeResponse struct {
	Revoked               int  `json:"revoked"`
	CurrentSessionRevoked bool `json:"current_session_revoked,omitempty"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// registerAuthRoutes 注册 T06 的管理认证端点。
//
// 写端点全部经过 ServeHTTP 的 Origin 校验；受保护端点再经过 requireSession。
// 其他任务文件通过 registerExtraRoutes 追加端点，fallback 在 New 中最后注册。
func (a *API) registerAuthRoutes() {
	a.mux.Handle("POST /admin/api/auth/login", http.HandlerFunc(a.handleLogin))
	a.mux.Handle("POST /admin/api/auth/logout", http.HandlerFunc(a.handleLogout))
	a.mux.Handle("GET /admin/api/auth/session",
		a.requireSession(http.HandlerFunc(a.handleSession)))
	a.mux.Handle("POST /admin/api/auth/password",
		a.requireSession(http.HandlerFunc(a.handleChangePassword)))
	a.mux.Handle("GET /admin/api/auth/sessions",
		a.requireSession(http.HandlerFunc(a.handleListSessions)))
	a.mux.Handle("DELETE /admin/api/auth/sessions",
		a.requireSession(http.HandlerFunc(a.handleRevokeOtherSessions)))
	a.mux.Handle("DELETE /admin/api/auth/sessions/{id}",
		a.requireSession(http.HandlerFunc(a.handleRevokeSession)))
}

// registerFallbackRoutes 让未命中的 /admin/api 路径返回 JSON 404，而不是 SPA
// 的 index.html。必须在所有具体路由之后调用，保持路由表面稳定。
func (a *API) registerFallbackRoutes() {
	a.mux.HandleFunc("/admin/api", a.handleNotFound)
	a.mux.HandleFunc("/admin/api/", a.handleNotFound)
}

func (a *API) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "管理端 API 路径不存在")
}

// handleLogin 校验管理员密码并签发新的 opaque session。
//
// 错误密码与“尚未初始化管理员”都返回同一个 401 文案，不提供账号枚举信息。
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "密码不能为空")
		return
	}

	result, err := a.auth.Login(r.Context(), clientSource(r), req.Password)
	if err != nil {
		a.writeLoginError(w, err)
		return
	}

	a.setSessionCookie(w, result)
	createdAt := result.ExpiresAt.Add(-adminauth.DefaultSessionTTL).UTC()
	writeJSON(w, http.StatusOK, sessionEnvelope{
		Authenticated: true,
		Admin:         defaultAdminIdentity,
		Session: sessionView{
			ID:        result.SessionID,
			CreatedAt: &createdAt,
			ExpiresAt: result.ExpiresAt.UTC(),
			Current:   true,
		},
	})
}

func (a *API) writeLoginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminauth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "凭据无效")
	case errors.Is(err, adminauth.ErrRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(int(adminauth.LoginFailureWindow/time.Second)))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试")
	default:
		a.logger.Error("管理员登录失败", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

// handleLogout 撤销当前 token 并清除 cookie。
//
// 没有 cookie 或 token 已失效时仍返回成功，避免登出接口成为 token 探测入口。
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, err := a.sessionToken(r); err == nil {
		if err := a.auth.Logout(r.Context(), token); err != nil {
			a.logger.Error("管理员登出失败", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
			return
		}
	}
	a.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// handleSession 返回当前管理员身份与 session 元数据。
func (a *API) handleSession(w http.ResponseWriter, r *http.Request) {
	session, ok := SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "需要管理员登录")
		return
	}
	writeJSON(w, http.StatusOK, sessionEnvelope{
		Authenticated: true,
		Admin:         defaultAdminIdentity,
		Session:       a.currentSessionView(r.Context(), session),
	})
}

// handleChangePassword 校验旧密码后改密，并撤销除当前 session 外的全部 session。
func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "当前密码与新密码不能为空")
		return
	}

	err := a.auth.ChangePassword(
		r.Context(),
		sessionTokenFromContext(r.Context()),
		req.CurrentPassword,
		req.NewPassword,
	)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, okResponse{OK: true})
	case errors.Is(err, adminauth.ErrInvalidCredentials):
		// 已通过 session 认证；这里只可能是旧密码错误，返回 400 让前端停留在设置页，
		// 不要用 401 触发全局跳登录。
		writeError(w, http.StatusBadRequest, "invalid_current_password", "当前密码不正确")
	case errors.Is(err, adminauth.ErrPasswordTooShort):
		writeError(w, http.StatusBadRequest, "password_too_short",
			fmt.Sprintf("新密码至少 %d 个字符", adminauth.MinPasswordLength))
	case isSessionAuthError(err):
		a.clearSessionCookie(w)
		writeError(w, http.StatusUnauthorized, "unauthorized", "session 无效或已过期")
	default:
		a.logger.Error("修改管理员密码失败", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

// handleListSessions 返回所有未撤销、未过期的 session，供设置页展示。
func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.auth.ListSessions(r.Context(), sessionTokenFromContext(r.Context()))
	if err != nil {
		a.writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionsEnvelope{Sessions: a.sessionViews(r.Context(), sessions)})
}

// handleRevokeSession 按 session ID 撤销单个 session；撤销当前 session 时同时清除 cookie。
func (a *API) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusNotFound, "session_not_found", "session 不存在")
		return
	}

	current, _ := SessionFromContext(r.Context())
	err := a.auth.RevokeSession(r.Context(), id)
	switch {
	case err == nil:
		response := revokeResponse{Revoked: 1}
		if id == current.ID {
			a.clearSessionCookie(w)
			response.CurrentSessionRevoked = true
		}
		writeJSON(w, http.StatusOK, response)
	case errors.Is(err, adminauth.ErrInvalidSession):
		writeError(w, http.StatusNotFound, "session_not_found", "session 不存在")
	default:
		a.logger.Error("撤销管理员 session 失败", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

// handleRevokeOtherSessions 撤销除当前 session 外的全部活跃 session。
//
// 设置页的“撤销全部”使用该端点；当前 session 保留，避免操作者被立即登出。
func (a *API) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	current, _ := SessionFromContext(r.Context())
	sessions, err := a.auth.ListSessions(r.Context(), sessionTokenFromContext(r.Context()))
	if err != nil {
		a.writeSessionError(w, err)
		return
	}

	revoked := 0
	for _, session := range sessions {
		if session.ID == current.ID {
			continue
		}
		if err := a.auth.RevokeSession(r.Context(), session.ID); err != nil {
			if errors.Is(err, adminauth.ErrInvalidSession) {
				// 并发登出/过期：目标已经失效，不计入撤销数。
				continue
			}
			a.logger.Error("批量撤销管理员 session 失败", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
			return
		}
		revoked++
	}
	writeJSON(w, http.StatusOK, revokeResponse{Revoked: revoked})
}

func (a *API) writeSessionError(w http.ResponseWriter, err error) {
	if isSessionAuthError(err) {
		a.clearSessionCookie(w)
		writeError(w, http.StatusUnauthorized, "unauthorized", "session 无效或已过期")
		return
	}
	a.logger.Error("管理员 session 操作失败", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
}

// currentSessionView 组装当前 session 的展示数据，包含 last_seen_at。
func (a *API) currentSessionView(ctx context.Context, session adminauth.Session) sessionView {
	session.Current = true
	var lastSeen *time.Time
	if a.db != nil {
		var raw sql.NullString
		err := a.db.QueryRowContext(ctx, selectSessionLastSeenByIDSQL, session.ID).Scan(&raw)
		if err == nil && raw.Valid {
			if parsed, parseErr := time.Parse(sessionTimeLayout, raw.String); parseErr == nil {
				utc := parsed.UTC()
				lastSeen = &utc
			}
		}
	}
	return makeSessionView(session, lastSeen)
}

// sessionViews 批量组装 session 展示数据；DB 不可用时省略 last_seen_at。
func (a *API) sessionViews(ctx context.Context, sessions []adminauth.Session) []sessionView {
	lastSeen := a.lastSeenTimes(ctx)
	views := make([]sessionView, 0, len(sessions))
	for _, session := range sessions {
		views = append(views, makeSessionView(session, lastSeen[session.ID]))
	}
	return views
}

func makeSessionView(session adminauth.Session, lastSeen *time.Time) sessionView {
	createdAt := session.CreatedAt.UTC()
	view := sessionView{
		ID:        session.ID,
		CreatedAt: &createdAt,
		ExpiresAt: session.ExpiresAt.UTC(),
		Current:   session.Current,
	}
	if lastSeen != nil {
		utc := lastSeen.UTC()
		view.LastSeenAt = &utc
	}
	return view
}

// lastSeenTimes 读取全部 session 的 last_seen_at；失败时降级为 nil。
func (a *API) lastSeenTimes(ctx context.Context) map[string]*time.Time {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.QueryContext(ctx, selectSessionsLastSeenSQL)
	if err != nil {
		a.logger.Debug("读取管理员 session last_seen_at 失败", "error", err)
		return nil
	}
	defer rows.Close()

	result := make(map[string]*time.Time)
	for rows.Next() {
		var (
			id  string
			raw sql.NullString
		)
		if err := rows.Scan(&id, &raw); err != nil {
			a.logger.Debug("解析管理员 session last_seen_at 失败", "error", err)
			return result
		}
		if !raw.Valid {
			continue
		}
		parsed, err := time.Parse(sessionTimeLayout, raw.String)
		if err != nil {
			continue
		}
		utc := parsed.UTC()
		result[id] = &utc
	}
	if err := rows.Err(); err != nil {
		a.logger.Debug("遍历管理员 session last_seen_at 失败", "error", err)
	}
	return result
}
