// Package dataplane 装配数据面：路由/鉴权、调度器、上游客户端、非流式与
// 流式代理、usage/诊断观测。
//
// 生产入口（cmd/gateway）与 E2E 测试共用这里的装配，避免测试手写一套与
// 生产逐渐漂移的依赖图。本包不启动 HTTP 服务，也不读取进程环境变量。
package dataplane

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"ai-router/internal/config"
	"ai-router/internal/gateway"
	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/secret"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

// Options 是数据面的构造参数。
type Options struct {
	Config config.Config
	Store  *store.Store
	// Envelope 为零时用 Config.MasterKey 构造；调用方已构造时可复用。
	Envelope *secret.Envelope
	Client   *upstream.Client
	Logger   *slog.Logger
}

// DataPlane 是已装配的数据面。Handler 可由调用方按需包装（例如 E2E 的
// 请求记录器），也可通过 Mount 挂到管理端 Server。
type DataPlane struct {
	handler   http.Handler
	journal   *observability.Store
	scheduler *routing.Scheduler
}

// Mounter 是 Mount 需要的最小接口；*server.Server 满足该接口。
type Mounter interface {
	Handle(pattern string, handler http.Handler)
}

// Build 构造数据面；缺少数据库/主密钥或任一组件初始化失败时返回错误。
func Build(opts Options) (*DataPlane, error) {
	if opts.Store == nil {
		return nil, errors.New("dataplane: 缺少数据库")
	}
	cfg := opts.Config
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	envelope := opts.Envelope
	if envelope == nil {
		var err error
		envelope, err = secret.NewEnvelope(cfg.MasterKey)
		if err != nil {
			return nil, fmt.Errorf("dataplane: 初始化密钥信封：%w", err)
		}
	}
	client := opts.Client
	if client == nil {
		var err error
		client, err = upstream.NewClient(upstream.OptionsFromConfig(cfg))
		if err != nil {
			return nil, fmt.Errorf("dataplane: 初始化上游客户端：%w", err)
		}
	}

	routes := opts.Store.ModelRoutes()
	channels := opts.Store.Channels()
	scheduler, err := routing.New(routing.Options{
		Routes:              routes,
		Channels:            channels,
		LastUsed:            channels,
		CapacityWait:        cfg.CapacityWait,
		DisableCapacityWait: cfg.CapacityWait == 0,
	})
	if err != nil {
		return nil, fmt.Errorf("dataplane: 初始化调度器：%w", err)
	}

	journal := observability.New(opts.Store.DB())
	decryptKey := func(ciphertext string) (string, error) {
		return envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
	}
	stream, err := gateway.NewStreamProxy(gateway.StreamProxyOptions{
		Scheduler:           scheduler,
		Upstream:            client,
		Journal:             journal,
		DecryptKey:          decryptKey,
		MaxAttempts:         cfg.MaxAttempts,
		RequestTimeout:      cfg.StreamTimeout,
		FirstContentTimeout: cfg.FirstContentTimeout,
		WriteTimeout:        cfg.DownstreamWriteTimeout,
		Logger:              logger,
	})
	if err != nil {
		return nil, fmt.Errorf("dataplane: 初始化流式代理：%w", err)
	}
	proxy, err := gateway.NewProxy(gateway.ProxyOptions{
		Scheduler:      scheduler,
		Upstream:       client,
		Journal:        journal,
		DecryptKey:     decryptKey,
		StreamHandler:  stream,
		MaxAttempts:    cfg.MaxAttempts,
		RequestTimeout: cfg.RequestTimeout,
		WriteTimeout:   cfg.DownstreamWriteTimeout,
		Logger:         logger,
	})
	if err != nil {
		return nil, fmt.Errorf("dataplane: 初始化非流式代理：%w", err)
	}
	router, err := gateway.New(gateway.Options{
		ClientKey:          cfg.ClientKey,
		Routes:             routes,
		Forwarder:          proxy,
		CodexContextWindow: cfg.CodexContextWindow,
	})
	if err != nil {
		return nil, fmt.Errorf("dataplane: 初始化客户端路由：%w", err)
	}
	observed, err := gateway.ObserveGateway(router, proxy, stream, journal)
	if err != nil {
		return nil, fmt.Errorf("dataplane: 初始化请求观测：%w", err)
	}

	return &DataPlane{handler: observed, journal: journal, scheduler: scheduler}, nil
}

// Handler 返回数据面 http.Handler，路由包含 /v1/ 与 /backend-api/codex/。
func (d *DataPlane) Handler() http.Handler {
	if d == nil {
		return http.NotFoundHandler()
	}
	return d.handler
}

// Journal 是数据面使用的请求日志仓储，供后台保留清理复用。
func (d *DataPlane) Journal() *observability.Store {
	if d == nil {
		return nil
	}
	return d.journal
}

// Runtime 返回调度器的只读运行状态，不执行探针或改变恢复准入。
func (d *DataPlane) Runtime(channel store.Channel) routing.RuntimeSnapshot {
	if d == nil || d.scheduler == nil {
		return routing.RuntimeSnapshot{State: "unknown", Limit: channel.MaxConcurrency}
	}
	return d.scheduler.Runtime(channel)
}

// Mount 把数据面挂到给定 mux（通常是 *server.Server）。
func (d *DataPlane) Mount(m Mounter) {
	if d == nil || d.handler == nil || m == nil {
		return
	}
	m.Handle("/v1/", d.handler)
	m.Handle("/backend-api/codex/", d.handler)
}
