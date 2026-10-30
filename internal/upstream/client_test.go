package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-router/internal/store"
)

const testUpstreamKey = "sk-upstream-test-0123456789abcdef"

type capturedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

func newTestClient(t *testing.T, opts Options) *Client {
	t.Helper()
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() 失败：%v", err)
	}
	return client
}

func newCaptureServer(t *testing.T, status int, responseBody string) (*httptest.Server, <-chan capturedRequest) {
	t.Helper()
	requests := make(chan capturedRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusInternalServerError)
			return
		}
		requests <- capturedRequest{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Header:   r.Header.Clone(),
			Body:     body,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func testTarget(server *httptest.Server, protocol string) Target {
	return Target{
		ChannelID:   42,
		ChannelName: "test-channel",
		BaseURL:     server.URL + "/v1",
		Protocol:    protocol,
		APIKey:      testUpstreamKey,
	}
}

func validRequest() Request {
	return Request{
		UpstreamModel: "gpt-upstream",
		Body:          []byte(`{"model":"gpt-client","stream":false}`),
	}
}

func receiveCaptured(t *testing.T, requests <-chan capturedRequest) capturedRequest {
	t.Helper()
	select {
	case captured := <-requests:
		return captured
	case <-time.After(2 * time.Second):
		t.Fatal("等待上游请求超时")
		return capturedRequest{}
	}
}

func assertNoUpstreamCall(t *testing.T, requests <-chan capturedRequest) {
	t.Helper()
	select {
	case captured := <-requests:
		t.Fatalf("上游不应被调用，却收到请求 %s %s", captured.Method, captured.Path)
	default:
	}
}

func decodeJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("解码 JSON 失败：%v（body=%q）", err, data)
	}
	if value == nil {
		t.Fatalf("JSON 不是对象：%q", data)
	}
	return value
}

func readAllAndClose(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取响应 body 失败：%v", err)
	}
	return body
}

func TestDefaultOptionsFillFromConfig(t *testing.T) {
	client := newTestClient(t, Options{})
	if client.requestTimeout != DefaultOptions().RequestTimeout {
		t.Fatalf("RequestTimeout = %v，期望默认值 %v",
			client.requestTimeout, DefaultOptions().RequestTimeout)
	}
	if client.streamTimeout != DefaultOptions().StreamTimeout ||
		client.streamIdleTimeout != DefaultOptions().StreamIdleTimeout {
		t.Fatalf("流式超时默认值未生效：%+v", client)
	}
}

func TestNewClientRejectsNegativeTimeout(t *testing.T) {
	if _, err := NewClient(Options{RequestTimeout: -time.Second}); err == nil {
		t.Fatal("负数超时应被拒绝")
	}
}

func TestSendHitsNativePaths(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
		wantPath string
	}{
		{
			name:     "Responses",
			protocol: store.ChannelProtocolOpenAIResponses,
			body:     `{"model":"gpt-client","input":"hi","stream":true}`,
			wantPath: "/v1/responses",
		},
		{
			name:     "Chat",
			protocol: store.ChannelProtocolOpenAIChat,
			body:     `{"model":"gpt-client","messages":[{"role":"user","content":"hi"}]}`,
			wantPath: "/v1/chat/completions",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
			client := newTestClient(t, Options{})

			response, err := client.Send(context.Background(), testTarget(server, test.protocol), Request{
				UpstreamModel: "gpt-upstream",
				Body:          []byte(test.body),
			})
			if err != nil {
				t.Fatalf("Send() 失败：%v", err)
			}
			readAllAndClose(t, response)

			captured := receiveCaptured(t, requests)
			if captured.Method != http.MethodPost {
				t.Errorf("method = %s，期望 POST", captured.Method)
			}
			if captured.Path != test.wantPath {
				t.Errorf("path = %s，期望 %s", captured.Path, test.wantPath)
			}
			if got := captured.Header.Get("Authorization"); got != "Bearer "+testUpstreamKey {
				t.Errorf("Authorization = %q，期望 Bearer 上游 Key", got)
			}
			if got := captured.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q，期望 application/json", got)
			}
			if got := decodeJSONObject(t, captured.Body)["model"]; got != "gpt-upstream" {
				t.Errorf("model = %v，期望 gpt-upstream", got)
			}
		})
	}
}

func TestSendJoinsBaseURLPath(t *testing.T) {
	tests := []struct {
		name       string
		baseSuffix string
		wantPath   string
		wantQuery  string
	}{
		{name: "无路径", baseSuffix: "", wantPath: "/responses"},
		{name: "根路径", baseSuffix: "/", wantPath: "/responses"},
		{name: "v1", baseSuffix: "/v1", wantPath: "/v1/responses"},
		{name: "v1 尾斜杠", baseSuffix: "/v1/", wantPath: "/v1/responses"},
		{name: "多级路径", baseSuffix: "/proxy/v1/", wantPath: "/proxy/v1/responses"},
		{
			name:       "保留查询串",
			baseSuffix: "/v1?api-version=2026-01-01&flag=1",
			wantPath:   "/v1/responses",
			wantQuery:  "api-version=2026-01-01&flag=1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
			client := newTestClient(t, Options{})
			target := testTarget(server, store.ChannelProtocolOpenAIResponses)
			target.BaseURL = server.URL + test.baseSuffix

			response, err := client.Send(context.Background(), target, validRequest())
			if err != nil {
				t.Fatalf("Send() 失败：%v", err)
			}
			readAllAndClose(t, response)

			captured := receiveCaptured(t, requests)
			if captured.Path != test.wantPath {
				t.Errorf("path = %s，期望 %s", captured.Path, test.wantPath)
			}
			if captured.RawQuery != test.wantQuery {
				t.Errorf("query = %q，期望 %q", captured.RawQuery, test.wantQuery)
			}
		})
	}
}

func TestSendPreservesFieldsAndOnlyReplacesModel(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
	client := newTestClient(t, Options{})

	body := []byte(`{
		"model": "client-model",
		"input": [
			{"role": "user", "content": [
				{"type": "input_text", "text": "hello <world> & all"},
				{"type": "input_image", "image_url": "data:image/png;base64,iVBORw0KGgo="}
			]}
		],
		"tools": [
			{"type": "function", "name": "shell", "description": "run",
			 "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}},
			 "strict": false}
		],
		"reasoning": {"effort": "high", "summary": "auto"},
		"unknown_field": {"nested": [1, 2.50, true, null, {"x": "y"}]},
		"stream": true
	}`)

	response, err := client.Send(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses),
		Request{UpstreamModel: "gpt-upstream", Body: body, Stream: true})
	if err != nil {
		t.Fatalf("Send() 失败：%v", err)
	}
	readAllAndClose(t, response)

	captured := receiveCaptured(t, requests)
	got := decodeJSONObject(t, captured.Body)
	want := decodeJSONObject(t, body)
	want["model"] = "gpt-upstream"

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("请求体字段未原样保留：\n got = %#v\nwant = %#v", got, want)
	}
	if got["model"] != "gpt-upstream" {
		t.Errorf("model = %v，期望 gpt-upstream", got["model"])
	}
	if _, ok := got["tools"]; !ok {
		t.Error("tools 字段丢失")
	}
	if _, ok := got["reasoning"]; !ok {
		t.Error("reasoning 字段丢失")
	}
	if _, ok := got["unknown_field"]; !ok {
		t.Error("未知字段丢失")
	}
}

func TestSendFiltersRequestHeadersAndSetsUpstreamAuth(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
	client := newTestClient(t, Options{})

	headers := http.Header{
		"Authorization":       {"Bearer client-key-should-not-leak"},
		"Cookie":              {"session=client-cookie"},
		"X-Api-Key":           {"client-key-should-not-leak"},
		"Proxy-Authorization": {"Basic proxy-secret"},
		"X-Trace-Id":          {"trace-123"},
		"Accept":              {"text/event-stream"},
		"Content-Type":        {"application/json; charset=utf-8"},
		"Accept-Encoding":     {"br"},
		"Connection":          {"X-Drop-Me"},
		"X-Drop-Me":           {"drop-this"},
		"Keep-Alive":          {"timeout=5"},
	}

	response, err := client.Send(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses),
		Request{UpstreamModel: "gpt-upstream", Body: validRequest().Body, Headers: headers})
	if err != nil {
		t.Fatalf("Send() 失败：%v", err)
	}
	readAllAndClose(t, response)

	captured := receiveCaptured(t, requests)
	header := captured.Header
	if got := header.Get("Authorization"); got != "Bearer "+testUpstreamKey {
		t.Errorf("Authorization = %q，期望上游 Key", got)
	}
	for _, name := range []string{
		"Cookie", "X-Api-Key", "Proxy-Authorization", "X-Drop-Me", "Keep-Alive", "Connection",
	} {
		if got := header.Get(name); got != "" {
			t.Errorf("%s = %q，不应转发", name, got)
		}
	}
	if got := header.Get("X-Trace-Id"); got != "trace-123" {
		t.Errorf("X-Trace-Id = %q，普通头应保留", got)
	}
	if got := header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q，应保留", got)
	}
	if got := header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q，应保留客户端值", got)
	}
	if got := header.Get("Accept-Encoding"); strings.Contains(got, "br") {
		t.Errorf("Accept-Encoding = %q，不应转发客户端压缩偏好", got)
	}
}

func TestSendRejectsInvalidTargets(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Target, *Request)
		want     error
		leakText []string
	}{
		{
			name:   "空 base URL",
			mutate: func(target *Target, _ *Request) { target.BaseURL = "" },
			want:   ErrInvalidBaseURL,
		},
		{
			name:   "非 http scheme",
			mutate: func(target *Target, _ *Request) { target.BaseURL = "ftp://upstream.example/v1" },
			want:   ErrInvalidBaseURL,
		},
		{
			name:   "相对 URL",
			mutate: func(target *Target, _ *Request) { target.BaseURL = "upstream.example/v1" },
			want:   ErrInvalidBaseURL,
		},
		{
			name:   "缺少主机名",
			mutate: func(target *Target, _ *Request) { target.BaseURL = "https:///v1" },
			want:   ErrInvalidBaseURL,
		},
		{
			name:     "包含 userinfo 与查询串",
			mutate:   func(target *Target, _ *Request) { target.BaseURL = "https://user:pass@upstream.example/v1?token=abc" },
			want:     ErrInvalidBaseURL,
			leakText: []string{"user:pass", "token=abc"},
		},
		{
			name:     "包含 fragment",
			mutate:   func(target *Target, _ *Request) { target.BaseURL = "https://upstream.example/v1#secret" },
			want:     ErrInvalidBaseURL,
			leakText: []string{"secret"},
		},
		{
			name:   "不支持的协议",
			mutate: func(target *Target, _ *Request) { target.Protocol = store.ChannelProtocolAnthropicMessages },
			want:   ErrUnsupportedProtocol,
		},
		{
			name:   "空协议",
			mutate: func(target *Target, _ *Request) { target.Protocol = "" },
			want:   ErrUnsupportedProtocol,
		},
		{
			name:   "空上游 Key",
			mutate: func(target *Target, _ *Request) { target.APIKey = "  " },
			want:   ErrInvalidRequest,
		},
		{
			name:   "空上游模型",
			mutate: func(_ *Target, request *Request) { request.UpstreamModel = "" },
			want:   ErrInvalidRequest,
		},
		{
			name:   "空 body",
			mutate: func(_ *Target, request *Request) { request.Body = nil },
			want:   ErrInvalidRequest,
		},
		{
			name:   "body 为 null",
			mutate: func(_ *Target, request *Request) { request.Body = []byte("null") },
			want:   ErrInvalidRequest,
		},
		{
			name:   "body 为数组",
			mutate: func(_ *Target, request *Request) { request.Body = []byte("[]") },
			want:   ErrInvalidRequest,
		},
		{
			name:   "body 非法 JSON",
			mutate: func(_ *Target, request *Request) { request.Body = []byte("{") },
			want:   ErrInvalidRequest,
		},
		{
			name:   "body 含尾随内容",
			mutate: func(_ *Target, request *Request) { request.Body = []byte(`{"model":"a"}{"model":"b"}`) },
			want:   ErrInvalidRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
			client := newTestClient(t, Options{})
			target := testTarget(server, store.ChannelProtocolOpenAIResponses)
			request := validRequest()
			test.mutate(&target, &request)

			_, err := client.Send(context.Background(), target, request)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v，期望 errors.Is(_, %v)", err, test.want)
			}
			assertNoUpstreamCall(t, requests)

			message := err.Error()
			if strings.Contains(message, testUpstreamKey) {
				t.Errorf("错误包含上游 Key：%s", message)
			}
			for _, fragment := range test.leakText {
				if strings.Contains(message, fragment) {
					t.Errorf("错误包含敏感片段 %q：%s", fragment, message)
				}
			}
		})
	}
}

func TestSendRejectsCrossHostRedirect(t *testing.T) {
	var redirectedHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/v1/responses", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	client := newTestClient(t, Options{})
	target := testTarget(origin, store.ChannelProtocolOpenAIResponses)
	target.BaseURL = origin.URL + "/v1?token=secret-query"

	_, err := client.Send(context.Background(), target, validRequest())
	if !errors.Is(err, ErrCrossHostRedirect) {
		t.Fatalf("err = %v，期望 ErrCrossHostRedirect", err)
	}
	if redirectedHits.Load() != 0 {
		t.Fatalf("跨主机重定向不应发起请求，实际命中 %d 次", redirectedHits.Load())
	}
	message := err.Error()
	if strings.Contains(message, testUpstreamKey) || strings.Contains(message, "secret-query") {
		t.Errorf("重定向错误泄漏敏感信息：%s", message)
	}
}

func TestSendRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, Options{RequestTimeout: 60 * time.Millisecond})
	target := testTarget(server, store.ChannelProtocolOpenAIResponses)
	target.BaseURL = server.URL + "/v1?token=secret-query"

	start := time.Now()
	_, err := client.Send(context.Background(), target, validRequest())
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v，期望 ErrTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("超时耗时 %v，疑似未生效", elapsed)
	}
	if strings.Contains(err.Error(), testUpstreamKey) || strings.Contains(err.Error(), "secret-query") {
		t.Errorf("超时错误泄漏敏感信息：%s", err)
	}
}

func TestSendResponseHeaderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, Options{
		ResponseHeaderTimeout: 60 * time.Millisecond,
		RequestTimeout:        5 * time.Second,
	})

	start := time.Now()
	_, err := client.Send(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses), validRequest())
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v，期望 ErrTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("响应头超时耗时 %v，疑似未生效", elapsed)
	}
}

func TestSendTransportErrorDoesNotLeakURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := server.URL + "/v1?token=secret-query"
	server.Close()

	client := newTestClient(t, Options{RequestTimeout: 2 * time.Second})
	target := Target{
		ChannelID: 1,
		BaseURL:   baseURL,
		Protocol:  store.ChannelProtocolOpenAIResponses,
		APIKey:    testUpstreamKey,
	}

	_, err := client.Send(context.Background(), target, validRequest())
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v，期望 ErrTransport", err)
	}
	message := err.Error()
	for _, fragment := range []string{testUpstreamKey, "secret-query", "http://"} {
		if strings.Contains(message, fragment) {
			t.Errorf("传输错误包含 %q：%s", fragment, message)
		}
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("httptest ResponseWriter 不支持 Flush")
			return
		}
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, Options{
		StreamIdleTimeout: 100 * time.Millisecond,
		StreamTimeout:     5 * time.Second,
	})
	response, err := client.Send(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses),
		Request{UpstreamModel: "gpt-upstream", Body: []byte(`{"model":"c","stream":true}`), Stream: true})
	if err != nil {
		t.Fatalf("Send() 失败：%v", err)
	}
	defer response.Body.Close()

	buffer := make([]byte, 128)
	var readErr error
	for {
		if _, readErr = response.Body.Read(buffer); readErr != nil {
			break
		}
	}
	if !errors.Is(readErr, ErrStreamIdleTimeout) {
		t.Fatalf("读取错误 = %v，期望 ErrStreamIdleTimeout", readErr)
	}
}

func TestStreamTotalTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("httptest ResponseWriter 不支持 Flush")
			return
		}
		flusher.Flush()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := io.WriteString(w, "data: tick\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, Options{
		StreamIdleTimeout: 2 * time.Second,
		StreamTimeout:     300 * time.Millisecond,
	})
	response, err := client.Send(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses),
		Request{UpstreamModel: "gpt-upstream", Body: []byte(`{"model":"c","stream":true}`), Stream: true})
	if err != nil {
		t.Fatalf("Send() 失败：%v", err)
	}
	defer response.Body.Close()

	buffer := make([]byte, 128)
	var readErr error
	for {
		if _, readErr = response.Body.Read(buffer); readErr != nil {
			break
		}
	}
	if !errors.Is(readErr, ErrTimeout) {
		t.Fatalf("读取错误 = %v，期望 ErrTimeout", readErr)
	}
}

func TestFilterResponseHeaders(t *testing.T) {
	source := http.Header{
		"Content-Type":       {"text/event-stream"},
		"Cache-Control":      {"no-cache"},
		"Retry-After":        {"1"},
		"X-Request-Id":       {"req-1"},
		"Set-Cookie":         {"upstream_session=secret"},
		"Connection":         {"keep-alive"},
		"Transfer-Encoding":  {"chunked"},
		"Upgrade":            {"websocket"},
		"Keep-Alive":         {"timeout=5"},
		"Trailer":            {"X-Trailer"},
		"Proxy-Authenticate": {"Basic"},
		"Content-Encoding":   {"gzip"},
		"Content-Length":     {"123"},
		"X-Upstream-Secret":  {"leak"},
	}

	filtered := FilterResponseHeaders(source)
	want := map[string]string{
		"Content-Type":  "text/event-stream",
		"Cache-Control": "no-cache",
		"Retry-After":   "1",
		"X-Request-Id":  "req-1",
	}
	if len(filtered) != len(want) {
		t.Fatalf("过滤后字段数 = %d，期望 %d：%#v", len(filtered), len(want), filtered)
	}
	for name, value := range want {
		if got := filtered.Get(name); got != value {
			t.Errorf("%s = %q，期望 %q", name, got, value)
		}
	}
	for _, name := range []string{
		"Set-Cookie", "Connection", "Transfer-Encoding", "Upgrade",
		"Keep-Alive", "Trailer", "Proxy-Authenticate", "Content-Encoding",
		"Content-Length", "X-Upstream-Secret",
	} {
		if got := filtered.Get(name); got != "" {
			t.Errorf("%s = %q，不应保留", name, got)
		}
	}

	filtered.Set("Content-Type", "mutated")
	if source.Get("Content-Type") != "text/event-stream" {
		t.Error("FilterResponseHeaders 返回的 Header 不应与入参共享底层存储")
	}
}

func TestSendFiltersUpstreamResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Set-Cookie", "upstream_session=secret")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Upstream-Secret", "leak")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, Options{})
	response, err := client.Send(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses), validRequest())
	if err != nil {
		t.Fatalf("Send() 失败：%v", err)
	}
	readAllAndClose(t, response)

	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q，应保留", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q，应保留", got)
	}
	for _, name := range []string{"Set-Cookie", "Keep-Alive", "X-Upstream-Secret", "Connection"} {
		if got := response.Header.Get(name); got != "" {
			t.Errorf("%s = %q，不应转发", name, got)
		}
	}
}

func TestTargetStringRedactsSecrets(t *testing.T) {
	target := Target{
		ChannelID:   7,
		ChannelName: "secret-channel",
		BaseURL:     "https://user:pass@upstream.example/v1?token=secret",
		Protocol:    store.ChannelProtocolOpenAIResponses,
		APIKey:      testUpstreamKey,
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		text := fmt.Sprintf(format, target)
		if strings.Contains(text, testUpstreamKey) {
			t.Errorf("%s 输出包含上游 Key：%s", format, text)
		}
		for _, fragment := range []string{"user:pass", "token=secret"} {
			if strings.Contains(text, fragment) {
				t.Errorf("%s 输出包含 %q：%s", format, fragment, text)
			}
		}
	}

	encoded, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("json.Marshal(Target) 失败：%v", err)
	}
	if strings.Contains(string(encoded), testUpstreamKey) {
		t.Errorf("JSON 序列化包含上游 Key：%s", encoded)
	}
}

type probeCall struct {
	ChannelID int64
	Health    store.ChannelHealth
}

type fakeHealthRecorder struct {
	mu    sync.Mutex
	calls []probeCall
	err   error
}

func (f *fakeHealthRecorder) UpdateHealth(ctx context.Context, id int64, health store.ChannelHealth) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, probeCall{ChannelID: id, Health: health})
	return nil
}

func (f *fakeHealthRecorder) snapshot(t *testing.T) []probeCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]probeCall, len(f.calls))
	copy(calls, f.calls)
	return calls
}

func TestProbeSuccessRecordsHealth(t *testing.T) {
	tests := []struct {
		name      string
		protocol  string
		wantPath  string
		wantInput string
		wantRole  string
	}{
		{
			name:      "Responses",
			protocol:  store.ChannelProtocolOpenAIResponses,
			wantPath:  "/v1/responses",
			wantInput: "ping",
		},
		{
			name:     "Chat",
			protocol: store.ChannelProtocolOpenAIChat,
			wantPath: "/v1/chat/completions",
			wantRole: "user",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
			recorder := &fakeHealthRecorder{}
			prober, err := NewProber(newTestClient(t, Options{}), recorder)
			if err != nil {
				t.Fatalf("NewProber() 失败：%v", err)
			}

			result, err := prober.Probe(context.Background(),
				testTarget(server, test.protocol), "gpt-upstream")
			if err != nil {
				t.Fatalf("Probe() 返回错误：%v", err)
			}
			if !result.Healthy() || result.Status != HealthStatusHealthy {
				t.Errorf("status = %q，期望 healthy", result.Status)
			}
			if result.HTTPStatus != http.StatusOK {
				t.Errorf("HTTPStatus = %d，期望 200", result.HTTPStatus)
			}
			if result.Error != "" {
				t.Errorf("Error = %q，期望为空", result.Error)
			}
			if result.Latency < 0 || result.LatencyMS < 0 {
				t.Errorf("latency 不能为负：%v / %d", result.Latency, result.LatencyMS)
			}

			captured := receiveCaptured(t, requests)
			if captured.Path != test.wantPath {
				t.Errorf("path = %s，期望 %s", captured.Path, test.wantPath)
			}
			if got := captured.Header.Get("Authorization"); got != "Bearer "+testUpstreamKey {
				t.Errorf("Authorization = %q，期望上游 Key", got)
			}
			payload := decodeJSONObject(t, captured.Body)
			if payload["model"] != "gpt-upstream" {
				t.Errorf("model = %v，期望 gpt-upstream", payload["model"])
			}
			if test.wantInput != "" {
				if payload["input"] != test.wantInput {
					t.Errorf("input = %v，期望 %q", payload["input"], test.wantInput)
				}
			}
			if test.wantRole != "" {
				messages, ok := payload["messages"].([]any)
				if !ok || len(messages) != 1 {
					t.Fatalf("messages = %#v，期望单条消息", payload["messages"])
				}
				message, _ := messages[0].(map[string]any)
				if message["role"] != test.wantRole || message["content"] != "ping" {
					t.Errorf("messages[0] = %#v", message)
				}
			}

			calls := recorder.snapshot(t)
			if len(calls) != 1 {
				t.Fatalf("健康状态写入次数 = %d，期望 1", len(calls))
			}
			call := calls[0]
			if call.ChannelID != 42 {
				t.Errorf("channel_id = %d，期望 42", call.ChannelID)
			}
			if call.Health.Status != HealthStatusHealthy || call.Health.Error != "" {
				t.Errorf("health = %+v，期望 healthy 且无错误", call.Health)
			}
			if call.Health.LatencyMS == nil || *call.Health.LatencyMS < 0 {
				t.Errorf("latency_ms = %v，期望非负指针", call.Health.LatencyMS)
			}
			if call.Health.At.IsZero() {
				t.Error("health.At 不能为零值")
			}
		})
	}
}

func TestProbeFailureReturnsRedactedError(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusServiceUnavailable,
		`{"error":{"message":"upstream leaked sk-live-0123456789abcdef"}}`)
	recorder := &fakeHealthRecorder{}
	prober, err := NewProber(newTestClient(t, Options{}), recorder)
	if err != nil {
		t.Fatalf("NewProber() 失败：%v", err)
	}

	result, err := prober.Probe(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses), "gpt-upstream")
	if err != nil {
		t.Fatalf("Probe() 返回错误：%v", err)
	}
	receiveCaptured(t, requests)

	if result.Healthy() || result.Status != HealthStatusUnhealthy {
		t.Errorf("status = %q，期望 unhealthy", result.Status)
	}
	if result.HTTPStatus != http.StatusServiceUnavailable {
		t.Errorf("HTTPStatus = %d，期望 503", result.HTTPStatus)
	}
	if !strings.Contains(result.Error, "503") {
		t.Errorf("Error = %q，期望包含状态码", result.Error)
	}
	for _, fragment := range []string{testUpstreamKey, "sk-live-0123456789abcdef", "leaked"} {
		if strings.Contains(result.Error, fragment) {
			t.Errorf("探针错误包含 %q：%s", fragment, result.Error)
		}
	}

	calls := recorder.snapshot(t)
	if len(calls) != 1 {
		t.Fatalf("健康状态写入次数 = %d，期望 1", len(calls))
	}
	if calls[0].Health.Status != HealthStatusUnhealthy ||
		!strings.Contains(calls[0].Health.Error, "503") {
		t.Errorf("health = %+v，期望 unhealthy/503", calls[0].Health)
	}
}

func TestProbeTimeoutReturnsUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	recorder := &fakeHealthRecorder{}
	prober, err := NewProber(newTestClient(t, Options{RequestTimeout: 60 * time.Millisecond}), recorder)
	if err != nil {
		t.Fatalf("NewProber() 失败：%v", err)
	}

	result, err := prober.Probe(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses), "gpt-upstream")
	if err != nil {
		t.Fatalf("Probe() 返回错误：%v", err)
	}
	if result.Healthy() || result.HTTPStatus != 0 || !strings.Contains(result.Error, "超时") {
		t.Errorf("result = %+v，期望 unhealthy/超时", result)
	}
	calls := recorder.snapshot(t)
	if len(calls) != 1 || calls[0].Health.Status != HealthStatusUnhealthy {
		t.Fatalf("健康状态 = %+v，期望一次 unhealthy 写入", calls)
	}
	if strings.Contains(calls[0].Health.Error, testUpstreamKey) {
		t.Errorf("健康状态错误包含上游 Key：%s", calls[0].Health.Error)
	}
}

func TestProbeHealthRecorderErrorIsReturned(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
	recorder := &fakeHealthRecorder{err: errors.New("database unavailable")}
	prober, err := NewProber(newTestClient(t, Options{}), recorder)
	if err != nil {
		t.Fatalf("NewProber() 失败：%v", err)
	}

	result, err := prober.Probe(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses), "gpt-upstream")
	receiveCaptured(t, requests)
	if err == nil || !strings.Contains(err.Error(), "更新渠道健康状态失败") {
		t.Fatalf("err = %v，期望健康状态写入错误", err)
	}
	if !result.Healthy() {
		t.Errorf("result = %+v，探针本身应成功", result)
	}
}

func TestProbeWithoutRecorderStillReturnsResult(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
	prober, err := NewProber(newTestClient(t, Options{}), nil)
	if err != nil {
		t.Fatalf("NewProber() 失败：%v", err)
	}

	result, err := prober.Probe(context.Background(),
		testTarget(server, store.ChannelProtocolOpenAIResponses), "gpt-upstream")
	if err != nil {
		t.Fatalf("Probe() 返回错误：%v", err)
	}
	if !result.Healthy() {
		t.Errorf("result = %+v，期望 healthy", result)
	}
	receiveCaptured(t, requests)
}

func TestProbeRejectsUnsupportedProtocol(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusOK, `{"ok":true}`)
	recorder := &fakeHealthRecorder{}
	prober, err := NewProber(newTestClient(t, Options{}), recorder)
	if err != nil {
		t.Fatalf("NewProber() 失败：%v", err)
	}

	result, err := prober.Probe(context.Background(),
		testTarget(server, store.ChannelProtocolAnthropicMessages), "gpt-upstream")
	if err != nil {
		t.Fatalf("Probe() 返回错误：%v", err)
	}
	if result.Healthy() || !strings.Contains(result.Error, "不支持") {
		t.Errorf("result = %+v，期望 unhealthy 且提示协议不支持", result)
	}
	assertNoUpstreamCall(t, requests)
}
