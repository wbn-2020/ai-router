package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-router/internal/store"
)

func setAliasCapabilities(t *testing.T, st *store.Store, model, modalities string, window int64, levels []string) {
	t.Helper()
	ctx := context.Background()
	routes, err := st.ModelRoutes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.ClientModel != model {
			continue
		}
		if _, err := st.ModelRoutes().Update(ctx, route.ID, store.ModelRouteInput{
			ClientModel:     route.ClientModel,
			ChannelID:       route.ChannelID,
			UpstreamModel:   route.UpstreamModel,
			Priority:        route.Priority,
			Enabled:         route.Enabled,
			InputModalities: modalities,
			ContextWindow:   window,
			ReasoningLevels: levels,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProductionManifestUsesDeclaredCapabilities(t *testing.T) {
	st, cfg, envelope := contractStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","status":"completed","output":[]}`)
	}))
	defer upstream.Close()
	contractChannel(t, st, envelope, "cap", upstream.URL, 0)
	setAliasCapabilities(t, st, "contract-model", store.ModelModalitiesText, 32000, []string{"low", "high"})

	srv, _, err := buildServer(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(srv.Handler())
	defer live.Close()
	client := &http.Client{}

	manifestReq, _ := http.NewRequest(http.MethodGet, live.URL+"/backend-api/codex/models", nil)
	manifestReq.Header.Set("Authorization", "Bearer "+cfg.ClientKey)
	manifestResp, err := client.Do(manifestReq)
	if err != nil {
		t.Fatal(err)
	}
	defer manifestResp.Body.Close()
	var manifest struct {
		Models []struct {
			InputModalities []string `json:"input_modalities"`
			ContextWindow   int64    `json:"context_window"`
			Levels          []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.NewDecoder(manifestResp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifestResp.StatusCode != http.StatusOK || len(manifest.Models) != 1 {
		t.Fatalf("manifest = %d %+v", manifestResp.StatusCode, manifest)
	}
	model := manifest.Models[0]
	if len(model.InputModalities) != 1 || model.InputModalities[0] != "text" || model.ContextWindow != 32000 {
		t.Fatalf("manifest capabilities = %+v", model)
	}
	if len(model.Levels) != 2 || model.Levels[0].Effort != "low" {
		t.Fatalf("levels = %+v", model.Levels)
	}
}

func TestProductionStatefulBoundary(t *testing.T) {
	st, cfg, envelope := contractStore(t)
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","status":"completed","output":[]}`)
	}))
	defer upstream.Close()
	contractChannel(t, st, envelope, "single", upstream.URL, 0)

	srv, _, err := buildServer(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(srv.Handler())
	defer live.Close()

	post := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, live.URL+"/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+cfg.ClientKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post(`{"model":"contract-model","background":true}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("background = %d", resp.StatusCode)
	}

	resp = post(`{"model":"contract-model","input":"hi"}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plain = %d %s", resp.StatusCode, body)
	}
	hitsAfterPlain := upstreamHits

	resp = post(`{"model":"contract-model","previous_response_id":"resp_1","input":"hi"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("single stateful = %d", resp.StatusCode)
	}
	if upstreamHits != hitsAfterPlain+1 {
		t.Fatalf("stateful upstream hits = %d, want %d", upstreamHits, hitsAfterPlain+1)
	}
}
