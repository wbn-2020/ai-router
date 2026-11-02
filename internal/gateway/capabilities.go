package gateway

import (
	"encoding/json"
	"strings"

	"ai-router/internal/store"
)

// ModelCapabilities 是同一客户端别名在某个入站协议下的共同能力。
//
// 未声明（unknown）不在这里变成 false：调用方按产品决定选择保守或宽松
// 默认，不把缺失证据包装成能力结论。
type ModelCapabilities struct {
	Slug                string
	InputModalities     string
	ContextWindow       *int64
	HasUndeclaredWindow bool
	ReasoningLevels     []string
}

// capabilitiesByAlias 按首次出现顺序聚合每个别名的共同能力。
func capabilitiesByAlias(routes []store.ModelRoute) []ModelCapabilities {
	order := make([]string, 0)
	grouped := make(map[string][]store.ModelRoute)
	for _, route := range routes {
		slug := strings.TrimSpace(route.ClientModel)
		if slug == "" {
			continue
		}
		if _, exists := grouped[slug]; !exists {
			order = append(order, slug)
		}
		grouped[slug] = append(grouped[slug], route)
	}

	capabilities := make([]ModelCapabilities, 0, len(order))
	for _, slug := range order {
		effective := store.AggregateRouteCapabilities(grouped[slug])
		capabilities = append(capabilities, ModelCapabilities{
			Slug:                slug,
			InputModalities:     effective.InputModalities,
			ContextWindow:       effective.ContextWindow,
			HasUndeclaredWindow: effective.HasUndeclaredWindow,
			ReasoningLevels:     effective.ReasoningLevels,
		})
	}
	return capabilities
}

// effectiveInputModalities 返回同一别名全部候选的共同模态。
func effectiveInputModalities(routes []store.ModelRoute) string {
	return store.AggregateRouteCapabilities(routes).InputModalities
}

// requestHasImage 判断请求是否携带图片输入。
//
// 只识别协议里明确的图片结构（Responses 的 input_image、Chat 的
// image_url），不做字符串匹配：工具 schema 里的同名字段不应误伤请求。
func requestHasImage(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return jsonHasImagePart(payload)
}

func jsonHasImagePart(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if kind, ok := typed["type"].(string); ok {
			switch kind {
			case "input_image", "image_url":
				return true
			}
		}
		for _, nested := range typed {
			if jsonHasImagePart(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if jsonHasImagePart(nested) {
				return true
			}
		}
	}
	return false
}

// statefulRequest 描述 Responses 请求是否依赖上游会话状态。
//
// 这里只判断“是否需要同一上游”，不推断上游一定保存了什么：只要请求
// 引用上一次 response、conversation、加密推理内容或 item_reference，
// 换渠道就可能失效。
type statefulRequest struct {
	Background         bool            `json:"background"`
	PreviousResponseID string          `json:"previous_response_id"`
	Conversation       json.RawMessage `json:"conversation"`
	Include            []string        `json:"include"`
	Input              json.RawMessage `json:"input"`
}

func parseStatefulRequest(body []byte) statefulRequest {
	var payload statefulRequest
	_ = json.Unmarshal(body, &payload)
	return payload
}

func (s statefulRequest) wantsBackground() bool {
	return s.Background
}

func (s statefulRequest) isStateful() bool {
	if strings.TrimSpace(s.PreviousResponseID) != "" {
		return true
	}
	if len(s.Conversation) > 0 && !strings.EqualFold(strings.TrimSpace(string(s.Conversation)), "null") {
		return true
	}
	for _, item := range s.Include {
		if strings.TrimSpace(item) == "reasoning.encrypted_content" {
			return true
		}
	}
	if len(s.Input) > 0 {
		var value any
		if json.Unmarshal(s.Input, &value) == nil && jsonHasType(value, "item_reference") {
			return true
		}
	}
	return false
}

func jsonHasType(value any, kind string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if value, ok := typed["type"].(string); ok && value == kind {
			return true
		}
		for _, nested := range typed {
			if jsonHasType(nested, kind) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if jsonHasType(nested, kind) {
				return true
			}
		}
	}
	return false
}
