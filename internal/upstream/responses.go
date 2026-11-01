package upstream

import (
	"encoding/json"

	"ai-router/internal/store"
)

// responsesAdapter 是 OpenAI Responses 的原生直通适配器。
//
// 它只声明协议、上游路径与探针请求体；模型替换、请求头过滤、超时与响应头
// 过滤统一由 Client 处理。适配器不做 Responses→Chat 或任何跨协议转换。
type responsesAdapter struct{}

var _ adapter = responsesAdapter{}

func (responsesAdapter) protocol() string {
	return store.ChannelProtocolOpenAIResponses
}

func (responsesAdapter) path() string {
	return "/responses"
}

// probeBody 构造最小 Responses 请求：单条 ping 文本输入、很小的输出上限、
// 非流式。探针验证该模型的生成响应，不携带用户 prompt。
func (responsesAdapter) probeBody(upstreamModel string) ([]byte, error) {
	payload := struct {
		Model           string `json:"model"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		Stream          bool   `json:"stream"`
	}{
		Model:           upstreamModel,
		Input:           "ping",
		MaxOutputTokens: probeMaxOutputTokens,
	}
	return json.Marshal(payload)
}
