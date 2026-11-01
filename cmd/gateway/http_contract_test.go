package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/observability"
	"ai-router/internal/secret"
	"ai-router/internal/store"
)

func contractStore(t *testing.T) (*store.Store, config.Config, *secret.Envelope) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Paths{DataDir: dir, DBPath: filepath.Join(dir, "gateway.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ListenAddr: "127.0.0.1:0", MasterKey: bytes.Repeat([]byte{7}, 32),
		ClientKey: "contract-client-key-0123456789", AdminPassword: "contract-admin-password",
		RequestTimeout: time.Second, StreamTimeout: time.Second,
		FirstContentTimeout: time.Second, DownstreamWriteTimeout: time.Second,
		MaxAttempts: 3,
	}
	envelope, err := secret.NewEnvelope(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	return st, cfg, envelope
}

func contractChannel(t *testing.T, st *store.Store, envelope *secret.Envelope, name, url string, priority int) store.Channel {
	t.Helper()
	ciphertext, err := envelope.EncryptString("contract-upstream-key", secret.PurposeChannelKey)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.Channels().Create(context.Background(), store.ChannelInput{
		ChannelConfig: store.ChannelConfig{Name: name, BaseURL: url + "/v1", Protocol: store.ChannelProtocolOpenAIResponses,
			MaxConcurrency: 2, Priority: priority, Enabled: true},
		KeyCiphertext: ciphertext, KeyMask: "masked",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ModelRoutes().Create(context.Background(), store.ModelRouteInput{
		ClientModel: "contract-model", UpstreamModel: "wire-model", ChannelID: channel.ID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func contractRequest(t *testing.T, client *http.Client, url, key string, stream bool) (*http.Response, string) {
	t.Helper()
	body := `{"model":"contract-model","input":"hello"}`
	if stream {
		body = `{"model":"contract-model","input":"hello","stream":true}`
	}
	req, err := http.NewRequest(http.MethodPost, url+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(data)
}

func contractLog(t *testing.T, st *store.Store, id string) observability.RequestDetail {
	t.Helper()
	journal := observability.New(st.DB())
	deadline := time.Now().Add(2 * time.Second)
	for {
		detail, err := journal.GetRequest(context.Background(), id)
		if err == nil {
			return detail
		}
		if time.Now().After(deadline) {
			t.Fatalf("请求日志未完成：%v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProductionHTTPStreamFallbackAndRuntime(t *testing.T) {
	st, cfg, envelope := contractStore(t)
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\n\n")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer contract-upstream-key" {
			t.Error("上游请求路径或认证不正确")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_contract\",\"object\":\"response\",\"created_at\":1,\"model\":\"wire-model\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer second.Close()
	bad := contractChannel(t, st, envelope, "bad", first.URL, 0)
	good := contractChannel(t, st, envelope, "good", second.URL, 1)
	srv, plane, err := buildServer(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(srv.Handler())
	defer live.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	response, body := contractRequest(t, client, live.URL, cfg.ClientKey, true)
	if response.StatusCode != 200 || !strings.Contains(body, "resp_contract") || strings.Count(body, "event: response.completed") != 1 || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("客户端终态错误：%d %s", response.StatusCode, body)
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("尝试次数错误：%d %d", firstHits.Load(), secondHits.Load())
	}
	detail := contractLog(t, st, response.Header.Get("X-Request-ID"))
	if detail.Request.Status != observability.StatusSuccess || len(detail.Attempts) != 2 || !detail.Request.SemanticCommitted {
		t.Fatalf("生产日志不一致：%+v", detail)
	}
	if plane.Runtime(bad).State != "cooldown" || plane.Runtime(good).InUse != 0 {
		t.Fatalf("生产运行状态错误：bad=%+v good=%+v", plane.Runtime(bad), plane.Runtime(good))
	}

	loginRequest, _ := http.NewRequest(http.MethodPost, live.URL+"/admin/api/auth/login", strings.NewReader(`{"password":"contract-admin-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("Origin", live.URL)
	login, err := client.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, login.Body)
	_ = login.Body.Close()
	if login.StatusCode != 200 || len(login.Cookies()) == 0 {
		t.Fatalf("测试管理登录：%d", login.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, live.URL+"/admin/api/channels", nil)
	for _, cookie := range login.Cookies() {
		req.AddCookie(cookie)
	}
	adminResponse, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer adminResponse.Body.Close()
	var list struct {
		Channels []struct {
			ID      int64 `json:"id"`
			Runtime struct {
				State string `json:"state"`
			} `json:"runtime"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(adminResponse.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, channel := range list.Channels {
		if channel.ID == bad.ID {
			found = channel.Runtime.State == "cooldown"
		}
	}
	if !found {
		t.Fatalf("管理API未接入实际调度状态：%+v", list)
	}
}

func TestProductionHTTPBudgetDelivers504AndReleasesSlot(t *testing.T) {
	st, cfg, envelope := contractStore(t)
	cfg.RequestTimeout = 60 * time.Millisecond
	cfg.MaxAttempts = 1
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			t.Error("上游未收到取消")
		}
	}))
	defer fake.Close()
	channel := contractChannel(t, st, envelope, "slow", fake.URL, 0)
	srv, plane, err := buildServer(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(srv.Handler())
	defer live.Close()
	start := time.Now()
	response, body := contractRequest(t, &http.Client{Timeout: 3 * time.Second}, live.URL, cfg.ClientKey, false)
	if response.StatusCode != 504 || !strings.Contains(body, "gateway_request_timeout") || time.Since(start) > 2*time.Second {
		t.Fatalf("总预算错误响应：%d %s %s", response.StatusCode, body, time.Since(start))
	}
	detail := contractLog(t, st, response.Header.Get("X-Request-ID"))
	if detail.Request.Status == observability.StatusCancelled || detail.Request.ErrorCode != "gateway_request_timeout" {
		t.Fatalf("总预算误分类：%+v", detail.Request)
	}
	if runtime := plane.Runtime(channel); runtime.InUse != 0 || runtime.State != "ready" {
		t.Fatalf("预算误罚渠道或泄漏槽位：%+v", runtime)
	}
}
