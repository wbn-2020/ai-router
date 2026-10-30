package upstream

import (
	"encoding/json"

	"ai-router/internal/store"
)

// chatAdapter 是 OpenAI Chat Completions 的原生直通适配器。
//
// 它只声明协议、上游路径与探针请求体；模型替换、请求头过滤、超时与响应头
// 过滤统一由 Client 处理。适配器不做 Chat→Responses 或任何跨协议转换。
type chatAdapter struct{}

var _ adapter = chatAdapter{}

func (chatAdapter) protocol() string {
	return store.ChannelProtocolOpenAIChat
}

func (chatAdapter) path() string {
	return "/chat/completions"
}

type chatProbeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// probeBody 构造最小 Chat 请求：单条 ping 消息、很小的输出上限、非流式。
func (chatAdapter) probeBody(upstreamModel string) ([]byte, error) {
	payload := struct {
		Model     string             `json:"model"`
		Messages  []chatProbeMessage `json:"messages"`
		MaxTokens int                `json:"max_tokens"`
		Stream    bool               `json:"stream"`
	}{
		Model:     upstreamModel,
		Messages:  []chatProbeMessage{{Role: "user", Content: "ping"}},
		MaxTokens: probeMaxOutputTokens,
	}
	return json.Marshal(payload)
}
