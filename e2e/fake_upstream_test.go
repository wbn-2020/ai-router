package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件实现 T27 使用的本地假上游。
//
// 假上游只监听 127.0.0.1 的 httptest 端口，不访问外网；它按请求体中的上游
// 模型名选择场景，并记录完整请求（method/path/header/body）供断言使用。
// SSE 形状按 Codex 0.164.0 能解析的 Responses 事件构造。
const (
	modelText       = "e2e-text"
	modelImage      = "e2e-image"
	modelTool       = "e2e-tool"
	modelDisconnect = "e2e-disconnect"

	fakeTextOutput      = "FAKE_TEXT_OK"
	fakeImageOutput     = "FAKE_IMAGE_OK"
	fakeToolFinalOutput = "FAKE_TOOL_FINAL"
	fakePartialOutput   = "PARTIAL_BEFORE_DISCONNECT"
	fakeToolCallID      = "call_e2e_tool_1"
	fakeToolArguments   = `{"cmd":"echo E2E_TOOL_OUTPUT"}`
)

// fakeRequest 是假上游收到的一次完整请求快照。
type fakeRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
	Model  string
}

// fakeUpstream 是本地假上游。
type fakeUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []fakeRequest
	ids      atomic.Int64
}

// newFakeUpstream 启动一个只注册 /v1/responses 的本地假上游。
func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()

	fake := &fakeUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/responses", fake.handleResponses)
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeUpstream) url() string {
	return f.server.URL
}

func (f *fakeUpstream) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	var meta struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &meta)

	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
		Model:  meta.Model,
	})
	f.mu.Unlock()

	switch meta.Model {
	case modelText:
		f.writeTextStream(w, meta.Model, fakeTextOutput)
	case modelImage:
		f.writeTextStream(w, meta.Model, fakeImageOutput)
	case modelTool:
		if strings.Contains(string(body), `"function_call_output"`) {
			f.writeTextStream(w, meta.Model, fakeToolFinalOutput)
			return
		}
		f.writeToolCallStream(w, meta.Model)
	case modelDisconnect:
		f.writeDisconnectStream(w, meta.Model)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"fake upstream: unknown model"}}`)
	}
}

func (f *fakeUpstream) writeTextStream(w http.ResponseWriter, model, text string) {
	sse := newSSEWriter(w)
	respID := f.nextResponseID("text")
	createdAt := time.Now().Unix()
	seq := int64(0)

	response := responseObject(respID, model, "in_progress", nil, createdAt)
	sse.event("response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": seq,
		"response":        response,
	})
	seq++

	itemID := "msg_" + respID
	sse.event("response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": seq,
		"output_index":    0,
		"item": map[string]any{
			"type":    "message",
			"id":      itemID,
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
	seq++

	sse.event("response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": seq,
		"item_id":         itemID,
		"output_index":    0,
		"content_index":   0,
		"part":            outputTextPart(""),
	})
	seq++

	sse.event("response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": seq,
		"item_id":         itemID,
		"output_index":    0,
		"content_index":   0,
		"delta":           text,
	})
	seq++

	sse.event("response.output_text.done", map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": seq,
		"item_id":         itemID,
		"output_index":    0,
		"content_index":   0,
		"text":            text,
	})
	seq++

	sse.event("response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": seq,
		"item_id":         itemID,
		"output_index":    0,
		"content_index":   0,
		"part":            outputTextPart(text),
	})
	seq++

	completedItem := map[string]any{
		"type":    "message",
		"id":      itemID,
		"status":  "completed",
		"role":    "assistant",
		"content": []any{outputTextPart(text)},
	}
	sse.event("response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": seq,
		"output_index":    0,
		"item":            completedItem,
	})
	seq++

	sse.event("response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": seq,
		"response":        responseObject(respID, model, "completed", []any{completedItem}, createdAt),
	})
}

func (f *fakeUpstream) writeToolCallStream(w http.ResponseWriter, model string) {
	sse := newSSEWriter(w)
	respID := f.nextResponseID("tool")
	createdAt := time.Now().Unix()
	seq := int64(0)

	sse.event("response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": seq,
		"response":        responseObject(respID, model, "in_progress", nil, createdAt),
	})
	seq++

	itemID := "fc_" + respID
	sse.event("response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": seq,
		"output_index":    0,
		"item": map[string]any{
			"type":      "function_call",
			"id":        itemID,
			"call_id":   fakeToolCallID,
			"name":      "exec_command",
			"arguments": "",
			"status":    "in_progress",
		},
	})
	seq++

	sse.event("response.function_call_arguments.delta", map[string]any{
		"type":            "response.function_call_arguments.delta",
		"sequence_number": seq,
		"item_id":         itemID,
		"output_index":    0,
		"call_id":         fakeToolCallID,
		"name":            "exec_command",
		"delta":           fakeToolArguments,
	})
	seq++

	sse.event("response.function_call_arguments.done", map[string]any{
		"type":            "response.function_call_arguments.done",
		"sequence_number": seq,
		"item_id":         itemID,
		"output_index":    0,
		"call_id":         fakeToolCallID,
		"name":            "exec_command",
		"arguments":       fakeToolArguments,
	})
	seq++

	completedItem := map[string]any{
		"type":      "function_call",
		"id":        itemID,
		"call_id":   fakeToolCallID,
		"name":      "exec_command",
		"arguments": fakeToolArguments,
		"status":    "completed",
	}
	sse.event("response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": seq,
		"output_index":    0,
		"item":            completedItem,
	})
	seq++

	sse.event("response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": seq,
		"response":        responseObject(respID, model, "completed", []any{completedItem}, createdAt),
	})
}

// writeDisconnectStream 在写出首段语义内容后直接关闭响应，不发送任何终止
// 事件；网关应在提交后自行补发唯一的 response.failed。
func (f *fakeUpstream) writeDisconnectStream(w http.ResponseWriter, model string) {
	sse := newSSEWriter(w)
	respID := f.nextResponseID("disconnect")
	createdAt := time.Now().Unix()

	sse.event("response.created", map[string]any{
		"type":            "response.created",
		"sequence_number": 0,
		"response":        responseObject(respID, model, "in_progress", nil, createdAt),
	})

	itemID := "msg_" + respID
	sse.event("response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": 1,
		"output_index":    0,
		"item": map[string]any{
			"type":    "message",
			"id":      itemID,
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})

	sse.event("response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": 2,
		"item_id":         itemID,
		"output_index":    0,
		"content_index":   0,
		"delta":           fakePartialOutput,
	})
	// 直接返回：连接关闭，没有 response.completed。
}

func (f *fakeUpstream) nextResponseID(kind string) string {
	return fmt.Sprintf("resp_%s_%d", kind, f.ids.Add(1))
}

func (f *fakeUpstream) requestsForModel(model string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]fakeRequest, 0)
	for _, req := range f.requests {
		if req.Model == model {
			out = append(out, req)
		}
	}
	return out
}

// sseWriter 把事件写成标准 SSE 并立即 flush。
type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	fl, ok := w.(http.Flusher)
	if !ok {
		panic("e2e: ResponseWriter 不支持 Flush")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return &sseWriter{w: w, fl: fl}
}

func (s *sseWriter) event(kind string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic("e2e: 无法序列化假上游 SSE 事件: " + err.Error())
	}
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", kind, data)
	s.fl.Flush()
}

func outputTextPart(text string) map[string]any {
	return map[string]any{
		"type":        "output_text",
		"text":        text,
		"annotations": []any{},
		"logprobs":    []any{},
	}
}

func responseObject(id, model, status string, output []any, createdAt int64) map[string]any {
	if output == nil {
		output = []any{}
	}
	return map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": createdAt,
		"model":      model,
		"status":     status,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  24,
			"output_tokens": 6,
			"total_tokens":  30,
		},
	}
}
