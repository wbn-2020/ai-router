package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-router/internal/store"
)

func newModelsTestClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(DefaultOptions())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestListModelsRequestsModelsEndpointAndParsesIDs(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"id":"gpt-5"},
			{"id":"gpt-5-mini"},
			{"id":" gpt-5 "},
			{"id":""},
			{"id":"o3"}
		]}`)
	}))
	t.Cleanup(server.Close)

	models, err := newModelsTestClient(t).ListModels(context.Background(), Target{
		BaseURL:  server.URL + "/v1",
		Protocol: store.ChannelProtocolOpenAIResponses,
		APIKey:   "sk-models-test",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q，期望 /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-models-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	want := []string{"gpt-5", "gpt-5-mini", "o3"}
	if len(models) != len(want) {
		t.Fatalf("models = %v，期望 %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models[%d] = %q，期望 %q", i, models[i], want[i])
		}
	}
}

func TestListModelsMapsUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"secret upstream body"}`)
	}))
	t.Cleanup(server.Close)

	_, err := newModelsTestClient(t).ListModels(context.Background(), Target{
		BaseURL:  server.URL,
		Protocol: store.ChannelProtocolOpenAIChat,
		APIKey:   "sk-should-not-leak",
	})
	if !errors.Is(err, ErrModelsUnavailable) {
		t.Fatalf("错误 = %v，期望 ErrModelsUnavailable", err)
	}
	var statusErr *ModelsHTTPError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误 = %v，期望 ModelsHTTPError(401)", err)
	}
	if strings.Contains(err.Error(), "sk-should-not-leak") ||
		strings.Contains(err.Error(), "secret upstream body") {
		t.Fatalf("错误泄漏了 Key 或上游 body：%v", err)
	}
}

func TestListModelsRejectsUnsupportedProtocolAndInvalidResponses(t *testing.T) {
	client := newModelsTestClient(t)
	if _, err := client.ListModels(context.Background(), Target{
		BaseURL:  "https://upstream.example.com/v1",
		Protocol: store.ChannelProtocolAnthropicMessages,
		APIKey:   "sk-test",
	}); !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("Anthropic 错误 = %v，期望 ErrUnsupportedProtocol", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not-json`)
	}))
	t.Cleanup(server.Close)
	if _, err := client.ListModels(context.Background(), Target{
		BaseURL:  server.URL,
		Protocol: store.ChannelProtocolOpenAIResponses,
		APIKey:   "sk-test",
	}); !errors.Is(err, ErrModelsUnavailable) {
		t.Fatalf("非 JSON 错误 = %v，期望 ErrModelsUnavailable", err)
	}

	tooLarge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", int(maxModelsResponseBytes)+1))
	}))
	t.Cleanup(tooLarge.Close)
	if _, err := client.ListModels(context.Background(), Target{
		BaseURL:  tooLarge.URL,
		Protocol: store.ChannelProtocolOpenAIResponses,
		APIKey:   "sk-test",
	}); !errors.Is(err, ErrModelsUnavailable) {
		t.Fatalf("超大响应错误 = %v，期望 ErrModelsUnavailable", err)
	}
}
