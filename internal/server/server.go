// Package server 组装网关的 HTTP 服务：运维健康检查、管理 API、嵌入式
// 管理端、通用中间件与优雅退出。
//
// T06 起 /admin/api/ 挂载 internal/adminapi 提供的管理 API；数据面路由
// 由后续任务通过 Options 或 Handle 接入。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-router/internal/adminapi"
	"ai-router/internal/adminauth"
	"ai-router/internal/config"
	"ai-router/internal/requestid"
	"ai-router/internal/secret"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
	"ai-router/internal/webassets"
)

const (
	// DefaultMaxBodyBytes 是单个入站请求体的默认上限（32 MiB）。
	// 它只限制读取量，不预分配内存；按需 body 捕获另有 8 MiB 上限。
	DefaultMaxBodyBytes int64 = 32 << 20
	// DefaultShutdownTimeout 是优雅退出的最长等待时间。
	DefaultShutdownTimeout = 30 * time.Second

	requestIDHeader = "X-Request-ID"

	// readinessCheckTimeout 限制单次 readiness 探测访问数据库的时间。
	readinessCheckTimeout = 2 * time.Second

	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
	maxHeaderBytes    = 1 << 20

	// contentSecurityPolicy 只用于管理端 SPA；default-src 'self' 不允许
	// 内联脚本，样式允许 Vite 产物与图表的内联 style 属性。
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; " +
		"base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'"

	// adminInitTimeout 限制启动时创建初始管理员凭据的数据库等待时间。
	adminInitTimeout = 5 * time.Second
)

// Options 是 Server 的构造参数。
type Options struct {
	// ListenAddr 为空时使用 config.DefaultListenAddr（127.0.0.1:8080）。
	ListenAddr string
	// Store 用于 readiness 的连通性与 schema 版本检查；nil 表示数据库未就绪。
	Store *store.Store
	// MasterKeyLoaded 表示启动时主密钥已成功加载。
	MasterKeyLoaded bool
	// MasterKey 是已解析的 32 字节主密钥；非空时管理 API 直接使用它，
	// 不再从进程环境变量重复解析。
	MasterKey []byte
	// RequiredSchemaVersion 是二进制期望的最高迁移版本；<= 0 时取内嵌迁移的最高版本。
	RequiredSchemaVersion int
	// MaxBodyBytes 为 <= 0 时使用 DefaultMaxBodyBytes。
	MaxBodyBytes int64
	// CaptureMaxBytes 是单侧请求/响应 body 捕获上限；<= 0 时使用默认 8 MiB。
	CaptureMaxBytes int64
	// ShutdownTimeout 为 <= 0 时使用 DefaultShutdownTimeout。
	ShutdownTimeout time.Duration
	// Logger 为 nil 时使用 slog.Default()。
	Logger *slog.Logger

	// AdminAuth 允许调用方注入已构造的 adminauth.Manager。nil 且 Store 非空时，
	// Server 会用 Store 的数据库自动创建管理器。
	AdminAuth *adminauth.Manager
	// AdminPassword 用于首次创建管理员；已有管理员记录时忽略。为空时回退读取
	// GATEWAY_ADMIN_PASSWORD，兼容直接构造 Server 的旧入口。
	AdminPassword string
	// CookieSecure 控制管理端 session cookie 的 Secure 标志；false 时回退读取
	// GATEWAY_COOKIE_SECURE（未设置则保持 false）。
	CookieSecure          bool
	UpstreamClient        *upstream.Client
	ProbeTimeout          time.Duration
	ModelDiscoveryTimeout time.Duration
	Runtime               adminapi.RuntimeReader
}

// Server 持有 HTTP 处理器、监听配置与退出状态。
//
// 除 Handle 外，Server 的方法都可在启动后并发调用；Handle 必须在开始
// 服务前调用。
type Server struct {
	store                 *store.Store
	masterKeyLoaded       bool
	requiredSchemaVersion int
	maxBodyBytes          int64
	shutdownTimeout       time.Duration
	logger                *slog.Logger

	adminAPI   *adminapi.API
	mux        *http.ServeMux
	handler    http.Handler
	httpServer *http.Server

	draining   atomic.Bool
	cancelBase context.CancelFunc
	cancelOnce sync.Once
}

// New 构造 HTTP 服务，但不会开始监听。
func New(opts Options) (*Server, error) {
	if opts.ListenAddr == "" {
		opts.ListenAddr = config.DefaultListenAddr
	}
	_, port, err := net.SplitHostPort(opts.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("server: 监听地址无效：%w", err)
	}
	if portNumber, err := strconv.Atoi(port); err != nil || portNumber < 0 || portNumber > 65535 {
		return nil, fmt.Errorf("server: 监听端口无效：%q", port)
	}

	requiredVersion := opts.RequiredSchemaVersion
	if requiredVersion <= 0 {
		migrations, err := store.EmbeddedMigrations()
		if err != nil {
			return nil, fmt.Errorf("server: 加载内嵌迁移失败：%w", err)
		}
		for _, migration := range migrations {
			if migration.Version > requiredVersion {
				requiredVersion = migration.Version
			}
		}
	}

	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = DefaultShutdownTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	adminAPI, err := newAdminAPI(opts)
	if err != nil {
		return nil, err
	}

	dist, err := webassets.Dist()
	if err != nil {
		return nil, err
	}
	index, err := webassets.Index()
	if err != nil {
		return nil, err
	}

	baseCtx, cancelBase := context.WithCancel(context.Background())

	s := &Server{
		store:                 opts.Store,
		masterKeyLoaded:       opts.MasterKeyLoaded,
		requiredSchemaVersion: requiredVersion,
		maxBodyBytes:          opts.MaxBodyBytes,
		shutdownTimeout:       opts.ShutdownTimeout,
		logger:                opts.Logger,
		adminAPI:              adminAPI,
		mux:                   http.NewServeMux(),
		cancelBase:            cancelBase,
	}
	s.registerRoutes(dist, index)
	s.handler = s.withMiddleware(s.mux)

	s.httpServer = &http.Server{
		Addr:              opts.ListenAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
	}
	return s, nil
}

// newAdminAPI 构造管理 API，并在数据库可用时完成首次管理员初始化。
//
// 初始化失败不会阻止进程启动：/readyz 会暴露数据库/迁移问题，登录接口会
// 返回安全错误。只有“配置了过短的初始密码”属于启动配置错误，直接失败。
func newAdminAPI(opts Options) (*adminapi.API, error) {
	var db adminauth.DB
	if opts.Store != nil {
		db = opts.Store.DB()
	}

	manager := opts.AdminAuth
	if manager == nil {
		if db == nil {
			return nil, nil
		}
		var err error
		manager, err = adminauth.NewManager(db)
		if err != nil {
			return nil, fmt.Errorf("server: 初始化管理员鉴权失败：%w", err)
		}

		// T06 的历史入口允许不传 AdminPassword；main 现在显式传入配置值，
		// 环境变量回退仅用于直接构造 Server 的测试/嵌入场景。
		password := opts.AdminPassword
		if password == "" {
			password = os.Getenv(config.EnvAdminPassword)
		}
		ctx, cancel := context.WithTimeout(context.Background(), adminInitTimeout)
		created, initErr := manager.EnsureAdmin(ctx, password)
		cancel()
		switch {
		case initErr == nil:
			if created {
				opts.Logger.Info("已创建初始管理员凭据")
			}
		case errors.Is(initErr, adminauth.ErrAdminNotInitialized):
			opts.Logger.Warn("尚无管理员凭据；设置 GATEWAY_ADMIN_PASSWORD 后重启即可登录")
		case errors.Is(initErr, adminauth.ErrPasswordTooShort):
			return nil, fmt.Errorf(
				"server: 初始管理员密码不满足长度要求（至少 %d 个字符）",
				adminauth.MinPasswordLength,
			)
		default:
			// 数据库尚未迁移或暂不可用：交给 /readyz 与登录接口报告。
			opts.Logger.Warn("初始化管理员凭据失败，管理端登录暂不可用",
				"error", secret.RedactValue(initErr.Error()))
		}
	}

	cookieSecure := opts.CookieSecure
	if !cookieSecure {
		if raw := os.Getenv(config.EnvCookieSecure); raw != "" {
			if parsed, err := strconv.ParseBool(raw); err == nil {
				cookieSecure = parsed
			}
		}
	}

	var envelope *secret.Envelope
	if len(opts.MasterKey) > 0 {
		if len(opts.MasterKey) != secret.MasterKeySize {
			return nil, fmt.Errorf("server: 主密钥长度必须为 %d 字节", secret.MasterKeySize)
		}
		var err error
		envelope, err = secret.NewEnvelope(opts.MasterKey)
		if err != nil {
			return nil, fmt.Errorf("server: 初始化主密钥信封失败：%w", err)
		}
	}

	var models adminapi.ModelLister
	if opts.UpstreamClient != nil {
		models = opts.UpstreamClient
	}
	api, err := adminapi.New(adminapi.Options{
		Auth:                  manager,
		DB:                    db,
		Envelope:              envelope,
		CookieSecure:          cookieSecure,
		CaptureMaxBytes:       opts.CaptureMaxBytes,
		Models:                models,
		ProbeClient:           opts.UpstreamClient,
		ProbeTimeout:          opts.ProbeTimeout,
		ModelDiscoveryTimeout: opts.ModelDiscoveryTimeout,
		Runtime:               opts.Runtime,
		Logger:                opts.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("server: 初始化管理 API 失败：%w", err)
	}
	return api, nil
}

// Handler 返回带通用中间件的根处理器，便于 httptest 或上层复用。
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Addr 返回监听地址。
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// Handle 在服务启动前注册额外路由。
//
// 中间件（request ID、panic recover、安全头、管理面 no-store、请求体
// 上限）自动作用于新路由。
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

// ListenAndServe 在 Options.ListenAddr 上开始服务。
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Serve 在给定 listener 上开始服务。
func (s *Server) Serve(listener net.Listener) error {
	return s.httpServer.Serve(listener)
}

// Run 开始服务并等待 ctx 取消。
//
// ctx 取消后先进入 draining 状态（/readyz 立即 503），再调用 Shutdown
// 最多等待 ShutdownTimeout；超时后取消 BaseContext，强制进行中的 handler
// 退出。
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return ignoreServerClosed(err)
	case <-ctx.Done():
	}

	shutdownErr := s.Shutdown()
	err := ignoreServerClosed(<-serveErr)
	if shutdownErr != nil && err != nil {
		return errors.Join(shutdownErr, err)
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return err
}

// Shutdown 停止接受新连接并等待进行中的请求，最长 ShutdownTimeout。
//
// draining 标志在调用 http.Server.Shutdown 之前置位，因此已有连接上的
// /readyz 会立即返回 503。等待超时或完成后取消 BaseContext：成功关闭时
// 没有进行中的 handler；超时时用它中断仍在运行的请求。
func (s *Server) Shutdown() error {
	s.draining.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	err := s.httpServer.Shutdown(ctx)
	s.cancelOnce.Do(s.cancelBase)
	return err
}

func ignoreServerClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) registerRoutes(dist fs.FS, index []byte) {
	s.mux.HandleFunc(healthzPath, s.handleHealthz)
	s.mux.HandleFunc(readyzPath, s.handleReadyz)

	if s.adminAPI != nil {
		// 比 SPA 的 /admin/ 更具体，优先匹配；管理 API 返回 JSON，不回退 index.html。
		s.mux.Handle("/admin/api/", s.adminAPI)
		s.mux.Handle("/admin/api", s.adminAPI)
	}

	fileServer := http.FileServer(http.FS(dist))
	s.mux.Handle("/assets/", fileServer)
	// 兼容 Vite base=/admin/ 的构建：把 /admin/assets/* 映射到 dist/assets/*。
	s.mux.Handle("/admin/assets/", http.StripPrefix("/admin", fileServer))

	spa := &spaHandler{index: index}
	s.mux.Handle("/admin", spa)
	s.mux.Handle("/admin/", spa)
	s.mux.HandleFunc("/", s.handleNotFound)
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())
		if isAdminPath(r.URL.Path) {
			setNoStore(w.Header())
		}

		requestID := newRequestID()
		w.Header().Set(requestIDHeader, requestID)
		r = r.WithContext(requestid.With(r.Context(), requestID))

		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}
			s.logger.Error("http 请求 panic",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"panic", secret.RedactValue(fmt.Sprint(recovered)),
				"stack", string(debug.Stack()),
			)
			writeJSON(w, http.StatusInternalServerError, errorBody{
				Error:     "internal_server_error",
				RequestID: requestID,
			})
		}()

		if r.Body != nil {
			if r.ContentLength > s.maxBodyBytes {
				// 不读取超大请求体，直接关闭连接，避免无谓的带宽与内存消耗。
				w.Header().Set("Connection", "close")
				writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{
					Error:     "request_body_too_large",
					RequestID: requestID,
				})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errorBody{
		Error:     "not_found",
		RequestID: RequestIDFromContext(r.Context()),
	})
}

func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func isAdminPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/")
}

func setNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	header := w.Header()
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeMethodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeJSON(w, http.StatusMethodNotAllowed, errorBody{
		Error:     "method_not_allowed",
		RequestID: RequestIDFromContext(r.Context()),
	})
}

func methodAllowed(r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	return false
}

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// RequestIDFromContext 返回中间件为请求生成的 ID；不存在时返回空串。
func RequestIDFromContext(ctx context.Context) string {
	return requestid.From(ctx)
}

func newRequestID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败在实践中不可见；仍返回可区分且不含敏感值的 ID。
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

type spaHandler struct {
	index []byte
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !methodAllowed(r, http.MethodGet, http.MethodHead) {
		writeMethodNotAllowed(w, r, "GET, HEAD")
		return
	}

	// 管理 API 未命中具体端点时不能返回 index.html，否则前端 fetch 会把
	// HTML 当成 JSON 解析。T06 起具体 API 路由会以更长模式优先匹配。
	if r.URL.Path == "/admin/api" || strings.HasPrefix(r.URL.Path, "/admin/api/") {
		writeJSON(w, http.StatusNotFound, errorBody{
			Error:     "not_found",
			RequestID: RequestIDFromContext(r.Context()),
		})
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("Content-Length", strconv.Itoa(len(h.index)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(h.index)
}
