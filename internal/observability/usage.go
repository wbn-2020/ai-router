// usage.go 提取上游协议中的 token usage。
//
// 提取结果只用于请求日志统计，不参与路由或计费。所有字段都是协议的
// 原始值：上游没有提供 usage 时保持 nil，绝不按文本长度或其它启发式
// 估算 token；usage 中的 0 是已知值，与缺失（nil）区分。
//
// 协议范围与数据面一致：OpenAI Responses 与 OpenAI Chat Completions。
// 本文件不依赖 gateway/upstream/store，避免观测层反向耦合数据面。
package observability

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

const (
	// UsageSourceResponsesResponse 表示非流式 Responses 响应顶层的 usage。
	UsageSourceResponsesResponse = "responses.response"
	// UsageSourceResponsesCompleted 表示 Responses 流式 response.completed。
	UsageSourceResponsesCompleted = "responses.completed"
	// UsageSourceResponsesFailed 表示 Responses 流式 response.failed。
	UsageSourceResponsesFailed = "responses.failed"
	// UsageSourceResponsesIncomplete 表示 Responses 流式 response.incomplete。
	UsageSourceResponsesIncomplete = "responses.incomplete"
	// UsageSourceResponsesCancelled 表示 Responses 流式 response.cancelled。
	UsageSourceResponsesCancelled = "responses.cancelled"
	// UsageSourceChatCompletion 表示非流式 Chat Completions 响应顶层 usage。
	UsageSourceChatCompletion = "chat.completion"
	// UsageSourceChatStream 表示 Chat Completions 流式 final chunk 的 usage。
	UsageSourceChatStream = "chat.stream"

	// maxUsageEventBytes 是 SSE usage 观察器为单个事件保留的最大字节数。
	//
	// usage 事件本身很小；上限只用于防止恶意/异常上游用不完整事件占满
	// 内存。超过上限时丢弃该事件直到下一个空行，绝不截断转发给客户端。
	maxUsageEventBytes = 1 << 20
)

// Usage 是一次上游响应报告的 token 数量。
//
// 字段为 nil 表示上游没有提供该值。未提供 total_tokens 时保持 nil，
// 不把 input+output 当作 total 回填。
type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
}

// Empty 表示三个 token 字段全部缺失。
func (u Usage) Empty() bool {
	return u.InputTokens == nil && u.OutputTokens == nil && u.TotalTokens == nil
}

// UsageResult 是 usage 提取结果及其协议来源。
//
// Source 为空表示上游没有给出可用 usage；调用方应把 token 列写成 NULL，
// 而不是 0。
type UsageResult struct {
	Usage  Usage
	Source string
}

// Found 表示提取到了至少一个 token 字段。
func (r UsageResult) Found() bool {
	return r.Source != "" && !r.Usage.Empty()
}

// ExtractResponsesUsage 解析非流式 Responses 响应顶层的 usage。
//
// 标准响应把 usage 放在响应对象顶层；少数兼容实现会返回事件形状
// （{"response":{"usage":...}}），这里作为兜底支持。
func ExtractResponsesUsage(body []byte) UsageResult {
	var payload struct {
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return UsageResult{}
	}
	usage, ok := parseUsageObject(payload.Usage)
	if !ok {
		usage, ok = parseUsageObject(payload.Response.Usage)
	}
	if !ok {
		return UsageResult{}
	}
	return UsageResult{Usage: usage, Source: UsageSourceResponsesResponse}
}

// ExtractChatUsage 解析非流式 Chat Completions 响应顶层的 usage。
func ExtractChatUsage(body []byte) UsageResult {
	var payload struct {
		Usage json.RawMessage `json:"usage"`
	}
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return UsageResult{}
	}
	usage, ok := parseUsageObject(payload.Usage)
	if !ok {
		return UsageResult{}
	}
	return UsageResult{Usage: usage, Source: UsageSourceChatCompletion}
}

// ResponsesUsageFromEvent 解析 Responses SSE 终止事件中的 response.usage。
//
// 只接受 response.completed/response.failed/response.incomplete/
// response.cancelled；前导事件即使带有 usage 也不采用，避免把中间状态
// 当成最终用量。event 为空或不可识别时回退到 data.type。
func ResponsesUsageFromEvent(event, data []byte) UsageResult {
	if len(data) == 0 {
		return UsageResult{}
	}

	var payload struct {
		Type     string `json:"type"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		// 少数兼容实现把 usage 放在事件顶层；作为 response.usage 缺失时的
		// 兜底，但不改变来源判定。
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return UsageResult{}
	}

	kind := strings.TrimSpace(string(event))
	source := responsesUsageSource(kind)
	if source == "" {
		// event 行缺失或使用通用事件名时，以 data.type 为准；与数据面
		// classifyResponsesFrame 的判定保持一致。
		kind = strings.TrimSpace(payload.Type)
		source = responsesUsageSource(kind)
	}
	if source == "" {
		return UsageResult{}
	}

	usage, ok := parseUsageObject(payload.Response.Usage)
	if !ok {
		usage, ok = parseUsageObject(payload.Usage)
	}
	if !ok {
		return UsageResult{}
	}
	return UsageResult{Usage: usage, Source: source}
}

func responsesUsageSource(kind string) string {
	switch kind {
	case "response.completed":
		return UsageSourceResponsesCompleted
	case "response.failed":
		return UsageSourceResponsesFailed
	case "response.incomplete":
		return UsageSourceResponsesIncomplete
	case "response.cancelled":
		return UsageSourceResponsesCancelled
	default:
		return ""
	}
}

// ChatUsageFromChunk 解析 Chat Completions 流式 final chunk 的 usage。
//
// Chat 的 usage 通常出现在 choices 为空、finish_reason 已出现过之后的
// 最后一个数据块；[DONE] 不带 usage。
func ChatUsageFromChunk(data []byte) UsageResult {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return UsageResult{}
	}

	var payload struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(trimmed, &payload) != nil {
		return UsageResult{}
	}
	usage, ok := parseUsageObject(payload.Usage)
	if !ok {
		return UsageResult{}
	}
	return UsageResult{Usage: usage, Source: UsageSourceChatStream}
}

// parseUsageObject 从 usage JSON 对象中读取已知 token 字段。
//
// 支持 Responses 的 input/output/total 与 Chat 的 prompt/completion/total；
// Chat 兼容少数上游使用的 input/output 别名。任一字段非法时只忽略该字段，
// 不把整个 usage 当作 0。
func parseUsageObject(raw json.RawMessage) (Usage, bool) {
	if !hasJSONValue(raw) {
		return Usage{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return Usage{}, false
	}

	usage := Usage{
		InputTokens:  firstUsageInt(fields, "input_tokens", "prompt_tokens"),
		OutputTokens: firstUsageInt(fields, "output_tokens", "completion_tokens"),
		TotalTokens:  firstUsageInt(fields, "total_tokens"),
	}
	if usage.Empty() {
		return Usage{}, false
	}
	return usage, true
}

func firstUsageInt(fields map[string]json.RawMessage, names ...string) *int64 {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		if value := parseUsageInt(raw); value != nil {
			return value
		}
	}
	return nil
}

// parseUsageInt 只接受非负整数；JSON 中的 12.0 这类等值整数也接受，
// 但 12.5 与负数视为缺失。
func parseUsageInt(raw json.RawMessage) *int64 {
	if !hasJSONValue(raw) {
		return nil
	}

	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		if value < 0 {
			return nil
		}
		return &value
	}

	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil
	}
	if number < 0 || number != math.Trunc(number) || number > float64(1<<53) {
		return nil
	}
	value = int64(number)
	return &value
}

func hasJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// StreamUsageObserver 从原始 SSE 字节中增量提取 usage。
//
// 它只解析 framing 与 usage 字段，不缓存正文，也不改变转发字节。调用方
// 应把上游读到的每个字节块按顺序交给 Write；Result 返回最近一次有效
// usage。Write/Result 不是并发安全的，需由单一 reader goroutine 使用。
type StreamUsageObserver struct {
	chat bool

	line       []byte
	event      []byte
	data       []byte
	eventBytes int
	skipping   bool

	result UsageResult
}

// NewResponsesStreamUsageObserver 构造 Responses SSE usage 观察器。
func NewResponsesStreamUsageObserver() *StreamUsageObserver {
	return &StreamUsageObserver{}
}

// NewChatStreamUsageObserver 构造 Chat Completions SSE usage 观察器。
func NewChatStreamUsageObserver() *StreamUsageObserver {
	return &StreamUsageObserver{chat: true}
}

// Write 消费一段原始 SSE 字节。
func (o *StreamUsageObserver) Write(chunk []byte) {
	if o == nil || len(chunk) == 0 {
		return
	}
	for len(chunk) > 0 {
		index := bytes.IndexByte(chunk, '\n')
		if index < 0 {
			o.appendLine(chunk)
			return
		}
		o.appendLine(chunk[:index])
		o.endLine()
		chunk = chunk[index+1:]
	}
}

// Result 返回目前观察到的最新 usage；不会估算或合成缺失值。
func (o *StreamUsageObserver) Result() UsageResult {
	if o == nil {
		return UsageResult{}
	}
	// 上游可能在没有结尾空行时 EOF；把最后一个未完成的事件也尝试解析。
	if !o.skipping && len(o.line) > 0 {
		o.endLine()
	}
	if !o.skipping && (len(o.event) > 0 || len(o.data) > 0) {
		o.dispatch()
	}
	return o.result
}

func (o *StreamUsageObserver) appendLine(fragment []byte) {
	if o.skipping {
		return
	}
	o.eventBytes += len(fragment) + 1
	if o.eventBytes > maxUsageEventBytes {
		o.skipping = true
		o.line = nil
		o.event = nil
		o.data = nil
		return
	}
	o.line = append(o.line, fragment...)
}

func (o *StreamUsageObserver) endLine() {
	line := bytes.TrimSuffix(o.line, []byte("\r"))
	o.line = o.line[:0]

	if o.skipping {
		if len(line) == 0 {
			o.resetEvent()
		}
		return
	}
	if len(line) == 0 {
		o.dispatch()
		return
	}
	if line[0] == ':' {
		return
	}

	field := line
	var value []byte
	if index := bytes.IndexByte(line, ':'); index >= 0 {
		field = line[:index]
		value = line[index+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}

	switch string(field) {
	case "event":
		o.event = append(o.event[:0], value...)
	case "data":
		if len(o.data) > 0 {
			o.data = append(o.data, '\n')
		}
		o.data = append(o.data, value...)
	}
}

func (o *StreamUsageObserver) dispatch() {
	if len(o.event) > 0 || len(o.data) > 0 {
		var result UsageResult
		if o.chat {
			result = ChatUsageFromChunk(o.data)
		} else {
			result = ResponsesUsageFromEvent(o.event, o.data)
		}
		if result.Found() {
			o.result = result
		}
	}
	o.resetEvent()
}

func (o *StreamUsageObserver) resetEvent() {
	o.event = o.event[:0]
	o.data = o.data[:0]
	o.eventBytes = 0
	o.skipping = false
}
