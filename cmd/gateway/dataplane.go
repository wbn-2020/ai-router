package main

import (
	"context"
	"fmt"

	"ai-router/internal/config"
	"ai-router/internal/dataplane"
	"ai-router/internal/secret"
	"ai-router/internal/server"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

func buildServer(cfg config.Config, st *store.Store) (*server.Server, *dataplane.DataPlane, error) {
	envelope, err := secret.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), maintenanceTimeout)
	err = st.ValidateChannelKeys(ctx, func(ciphertext string) (string, error) {
		return envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
	})
	cancel()
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: 加密状态不可读：%w", err)
	}
	client, err := upstream.NewClient(upstream.OptionsFromConfig(cfg))
	if err != nil {
		return nil, nil, err
	}
	plane, err := dataplane.Build(dataplane.Options{
		Config: cfg, Store: st, Envelope: envelope, Client: client,
	})
	if err != nil {
		return nil, nil, err
	}
	srv, err := server.New(server.Options{
		ListenAddr:            cfg.ListenAddr,
		Store:                 st,
		MasterKey:             cfg.MasterKey,
		MasterKeyLoaded:       true,
		CaptureMaxBytes:       cfg.CaptureMaxBytes,
		AdminPassword:         cfg.AdminPassword,
		CookieSecure:          cfg.CookieSecure,
		UpstreamClient:        client,
		ProbeTimeout:          cfg.ProbeTimeout,
		ModelDiscoveryTimeout: cfg.ModelDiscoveryTimeout,
		Runtime:               plane,
	})
	if err != nil {
		return nil, nil, err
	}
	plane.Mount(srv)
	return srv, plane, nil
}
