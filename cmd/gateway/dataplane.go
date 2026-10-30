package main

import (
	"fmt"

	"ai-router/internal/config"
	"ai-router/internal/gateway"
	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/secret"
	"ai-router/internal/server"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

// mountDataPlane 把所有已实现的数据面组件装配到管理端 Server。
//
// 这是 T17/T20/T21/T23/T24 的最终接线点：路由/鉴权 -> 非流式代理 ->
// 流式代理 -> usage/诊断环观测包装。组件本身不直接访问进程配置，主密钥
// 也只通过 secret.Envelope 的解密闭包进入代理。
func mountDataPlane(cfg config.Config, st *store.Store, srv *server.Server) error {
	if st == nil {
		return fmt.Errorf("数据面缺少数据库")
	}
	if srv == nil {
		return fmt.Errorf("数据面缺少 HTTP 服务")
	}

	envelope, err := secret.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("初始化密钥信封：%w", err)
	}
	client, err := upstream.NewClient(upstream.OptionsFromConfig(cfg))
	if err != nil {
		return fmt.Errorf("初始化上游客户端：%w", err)
	}

	routes := st.ModelRoutes()
	channels := st.Channels()
	scheduler, err := routing.New(routing.Options{
		Routes:       routes,
		Channels:     channels,
		LastUsed:     channels,
		CapacityWait: cfg.CapacityWait,
	})
	if err != nil {
		return fmt.Errorf("初始化调度器：%w", err)
	}

	journal := observability.New(st.DB())
	decryptKey := func(ciphertext string) (string, error) {
		return envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
	}
	stream, err := gateway.NewStreamProxy(gateway.StreamProxyOptions{
		Scheduler:  scheduler,
		Upstream:   client,
		Journal:    journal,
		DecryptKey: decryptKey,
	})
	if err != nil {
		return fmt.Errorf("初始化流式代理：%w", err)
	}
	proxy, err := gateway.NewProxy(gateway.ProxyOptions{
		Scheduler:     scheduler,
		Upstream:      client,
		Journal:       journal,
		DecryptKey:    decryptKey,
		StreamHandler: stream,
	})
	if err != nil {
		return fmt.Errorf("初始化非流式代理：%w", err)
	}
	router, err := gateway.New(gateway.Options{
		ClientKey:          cfg.ClientKey,
		Routes:             routes,
		Forwarder:          proxy,
		CodexContextWindow: cfg.CodexContextWindow,
	})
	if err != nil {
		return fmt.Errorf("初始化客户端路由：%w", err)
	}
	observed, err := gateway.ObserveGateway(router, proxy, stream, journal)
	if err != nil {
		return fmt.Errorf("初始化请求观测：%w", err)
	}

	srv.Handle("/v1/", observed)
	srv.Handle("/backend-api/codex/", observed)
	return nil
}
