package observability

import (
	"math"
	"strings"
	"testing"
)

func usageIntPtr(value int64) *int64 {
	return &value
}

func requireUsageInt(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil，期望 %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %d，期望 %d", name, *got, want)
	}
}

func requireNoUsage(t *testing.T, result UsageResult) {
	t.Helper()
	if result.Found() {
		t.Fatalf("不应提取到 usage，实际 %+v", result)
	}
	if result.Source != "" || !result.Usage.Empty() {
		t.Fatalf("缺失 usage 必须为空，实际 %+v", result)
	}
}

func TestExtractResponsesUsage(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		check func(*testing.T, UsageResult)
	}{
		{
			name: "完整 usage",
			body: `{"id":"resp_1","status":"completed","usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesResponse {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 12)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 34)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 46)
			},
		},
		{
			name: "只有 input/output 时 total 保持缺失",
			body: `{"usage":{"input_tokens":7,"output_tokens":3}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesResponse {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 7)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 3)
				if result.Usage.TotalTokens != nil {
					t.Fatalf("total 不应估算，实际 %d", *result.Usage.TotalTokens)
				}
			},
		},
		{
			name: "已知 0 与缺失不同",
			body: `{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
			check: func(t *testing.T, result UsageResult) {
				requireUsageInt(t, "input", result.Usage.InputTokens, 0)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 0)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 0)
				if !result.Found() {
					t.Fatal("显式 0 usage 应判定为已知")
				}
			},
		},
		{
			name:  "缺少 usage",
			body:  `{"id":"resp_1","status":"completed"}`,
			check: requireNoUsage,
		},
		{
			name:  "usage 为 null",
			body:  `{"usage":null}`,
			check: requireNoUsage,
		},
		{
			name:  "usage 为空对象",
			body:  `{"usage":{}}`,
			check: requireNoUsage,
		},
		{
			name:  "非法字段忽略",
			body:  `{"usage":{"input_tokens":"12","output_tokens":-1,"total_tokens":12.5}}`,
			check: requireNoUsage,
		},
		{
			name: "等值整数浮点可接受",
			body: `{"usage":{"input_tokens":12.0,"output_tokens":34.0}}`,
			check: func(t *testing.T, result UsageResult) {
				requireUsageInt(t, "input", result.Usage.InputTokens, 12)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 34)
			},
		},
		{
			name: "事件形状的兜底",
			body: `{"response":{"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesResponse {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 2)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 3)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 5)
			},
		},
		{
			name:  "空 body",
			body:  ``,
			check: requireNoUsage,
		},
		{
			name:  "非法 JSON",
			body:  `{not-json`,
			check: requireNoUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, ExtractResponsesUsage([]byte(tt.body)))
		})
	}
}

func TestExtractChatUsage(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		check func(*testing.T, UsageResult)
	}{
		{
			name: "标准 Chat usage",
			body: `{"id":"chatcmpl_1","usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceChatCompletion {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 9)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 4)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 13)
			},
		},
		{
			name: "input/output 别名",
			body: `{"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`,
			check: func(t *testing.T, result UsageResult) {
				requireUsageInt(t, "input", result.Usage.InputTokens, 5)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 2)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 7)
			},
		},
		{
			name:  "没有 usage",
			body:  `{"id":"chatcmpl_1","choices":[]}`,
			check: requireNoUsage,
		},
		{
			name:  "空 usage",
			body:  `{"usage":{}}`,
			check: requireNoUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, ExtractChatUsage([]byte(tt.body)))
		})
	}
}

func TestResponsesUsageFromEvent(t *testing.T) {
	tests := []struct {
		name  string
		event string
		data  string
		check func(*testing.T, UsageResult)
	}{
		{
			name:  "completed 带 usage",
			event: "response.completed",
			data:  `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesCompleted {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 12)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 34)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 46)
			},
		},
		{
			name:  "failed 带 usage",
			event: "response.failed",
			data:  `{"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesFailed {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "total", result.Usage.TotalTokens, 13)
			},
		},
		{
			name:  "incomplete 带 usage",
			event: "response.incomplete",
			data:  `{"type":"response.incomplete","response":{"usage":{"input_tokens":3}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesIncomplete {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 3)
			},
		},
		{
			name:  "cancelled 带 usage",
			event: "response.cancelled",
			data:  `{"type":"response.cancelled","response":{"usage":{"output_tokens":2}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesCancelled {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "output", result.Usage.OutputTokens, 2)
			},
		},
		{
			name:  "completed 没有 usage",
			event: "response.completed",
			data:  `{"type":"response.completed","response":{"status":"completed"}}`,
			check: requireNoUsage,
		},
		{
			name:  "非终止事件不采用 usage",
			event: "response.created",
			data:  `{"type":"response.created","response":{"usage":{"input_tokens":99}}}`,
			check: requireNoUsage,
		},
		{
			name:  "event 为空回退 data.type",
			event: "",
			data:  `{"type":"response.completed","response":{"usage":{"input_tokens":1}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesCompleted {
					t.Fatalf("source = %q", result.Source)
				}
			},
		},
		{
			name:  "未知 event 名回退 data.type",
			event: "message",
			data:  `{"type":"response.completed","response":{"usage":{"input_tokens":1}}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceResponsesCompleted {
					t.Fatalf("source = %q", result.Source)
				}
			},
		},
		{
			name:  "事件顶层 usage 兜底",
			event: "response.completed",
			data:  `{"type":"response.completed","usage":{"input_tokens":4,"output_tokens":6}}`,
			check: func(t *testing.T, result UsageResult) {
				requireUsageInt(t, "input", result.Usage.InputTokens, 4)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 6)
			},
		},
		{
			name:  "非法 JSON",
			event: "response.completed",
			data:  `{bad`,
			check: requireNoUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, ResponsesUsageFromEvent([]byte(tt.event), []byte(tt.data)))
		})
	}
}

func TestChatUsageFromChunk(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		check func(*testing.T, UsageResult)
	}{
		{
			name: "final chunk usage",
			data: `{"id":"chatcmpl_1","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
			check: func(t *testing.T, result UsageResult) {
				if result.Source != UsageSourceChatStream {
					t.Fatalf("source = %q", result.Source)
				}
				requireUsageInt(t, "input", result.Usage.InputTokens, 9)
				requireUsageInt(t, "output", result.Usage.OutputTokens, 4)
				requireUsageInt(t, "total", result.Usage.TotalTokens, 13)
			},
		},
		{
			name:  "DONE 没有 usage",
			data:  `[DONE]`,
			check: requireNoUsage,
		},
		{
			name:  "普通内容块没有 usage",
			data:  `{"choices":[{"delta":{"content":"hi"}}]}`,
			check: requireNoUsage,
		},
		{
			name:  "非法 JSON",
			data:  `{bad`,
			check: requireNoUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, ChatUsageFromChunk([]byte(tt.data)))
		})
	}
}

func TestStreamUsageObserverResponses(t *testing.T) {
	stream := "event: response.created\r\n" +
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\r\n\r\n" +
		"event: response.output_text.delta\r\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\r\n\r\n" +
		"event: response.completed\r\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}}` + "\r\n\r\n"

	// 按任意边界切块都应得到同一结果，覆盖跨行/跨事件的增量解析。
	for split := 1; split < len(stream); split++ {
		observer := NewResponsesStreamUsageObserver()
		observer.Write([]byte(stream[:split]))
		observer.Write([]byte(stream[split:]))
		result := observer.Result()
		if result.Source != UsageSourceResponsesCompleted {
			t.Fatalf("split=%d source = %q", split, result.Source)
		}
		requireUsageInt(t, "input", result.Usage.InputTokens, 12)
		requireUsageInt(t, "output", result.Usage.OutputTokens, 34)
		requireUsageInt(t, "total", result.Usage.TotalTokens, 46)
	}
}

func TestStreamUsageObserverKeepsLastTerminalUsage(t *testing.T) {
	observer := NewResponsesStreamUsageObserver()
	observer.Write([]byte(
		"event: response.created\n" +
			`data: {"type":"response.created","response":{"usage":{"input_tokens":99}}}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":34,"total_tokens":46}}}` + "\n\n"))
	result := observer.Result()
	if result.Source != UsageSourceResponsesCompleted {
		t.Fatalf("source = %q", result.Source)
	}
	requireUsageInt(t, "input", result.Usage.InputTokens, 12)
	requireUsageInt(t, "output", result.Usage.OutputTokens, 34)
	requireUsageInt(t, "total", result.Usage.TotalTokens, 46)
}

func TestStreamUsageObserverChat(t *testing.T) {
	observer := NewChatStreamUsageObserver()
	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, chunk := range chunks {
		observer.Write([]byte(chunk))
	}
	result := observer.Result()
	if result.Source != UsageSourceChatStream {
		t.Fatalf("source = %q", result.Source)
	}
	requireUsageInt(t, "input", result.Usage.InputTokens, 9)
	requireUsageInt(t, "output", result.Usage.OutputTokens, 4)
	requireUsageInt(t, "total", result.Usage.TotalTokens, 13)
}

func TestStreamUsageObserverMissingAndTruncated(t *testing.T) {
	t.Run("没有 usage", func(t *testing.T) {
		observer := NewResponsesStreamUsageObserver()
		observer.Write([]byte(
			"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"))
		requireNoUsage(t, observer.Result())
	})

	t.Run("最后一个事件没有空行", func(t *testing.T) {
		observer := NewResponsesStreamUsageObserver()
		observer.Write([]byte(
			"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":3}}}`))
		result := observer.Result()
		if result.Source != UsageSourceResponsesCompleted {
			t.Fatalf("source = %q", result.Source)
		}
		requireUsageInt(t, "input", result.Usage.InputTokens, 2)
		requireUsageInt(t, "output", result.Usage.OutputTokens, 3)
	})

	t.Run("超限事件被跳过但不影响后续事件", func(t *testing.T) {
		observer := NewResponsesStreamUsageObserver()
		observer.Write([]byte("data: "))
		huge := strings.Repeat("x", maxUsageEventBytes+64)
		observer.Write([]byte(huge))
		observer.Write([]byte("\n\n"))
		observer.Write([]byte(
			"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"total_tokens":1}}}` + "\n\n"))
		result := observer.Result()
		if result.Source != UsageSourceResponsesCompleted {
			t.Fatalf("source = %q", result.Source)
		}
		requireUsageInt(t, "input", result.Usage.InputTokens, 1)
	})
}

func TestUsageResultFound(t *testing.T) {
	if (UsageResult{}).Found() {
		t.Fatal("空结果不应 Found")
	}
	if (UsageResult{Usage: Usage{InputTokens: usageIntPtr(0)}, Source: UsageSourceChatStream}).Found() == false {
		t.Fatal("显式 0 usage 应 Found")
	}
	if (UsageResult{Usage: Usage{TotalTokens: usageIntPtr(math.MaxInt64)}}).Found() {
		t.Fatal("没有 source 的结果不应 Found")
	}
}
