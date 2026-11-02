package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 逐路由能力声明。
//
// 能力是“管理员声明的上游事实”，不是自动探测结果：unknown 表示未声明，
// 调用方必须自行决定降级策略，不能把它当作支持或拒绝。
const (
	ModelModalitiesUnknown   = "unknown"
	ModelModalitiesText      = "text"
	ModelModalitiesTextImage = "text_image"

	// ModelContextWindowMax 是声明窗口的上限，避免异常值放大计算与展示。
	ModelContextWindowMax int64 = 10000000
)

// reasoningLevelOrder 是 Codex 0.164 模型中出现的推理档位顺序。
//
// 交集与规范化都按此顺序输出，保证 manifest 稳定；不在列表中的档位
// 一律拒绝，避免把拼写错误静默写进数据库。
var reasoningLevelOrder = []string{"none", "low", "medium", "high", "xhigh", "max", "ultra"}

// EffectiveRouteCapabilities 是同一客户端模型、同一协议下全部候选的
// 共同能力。窗口取已声明候选的最小值；任一候选未声明窗口时
// HasUndeclaredWindow 为 true，由调用方决定是否回退全局值。
type EffectiveRouteCapabilities struct {
	RouteCount          int
	InputModalities     string
	ContextWindow       *int64
	HasUndeclaredWindow bool
	ReasoningLevels     []string
}

func normalizeModalities(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", ModelModalitiesUnknown:
		return ModelModalitiesUnknown, nil
	case ModelModalitiesText:
		return ModelModalitiesText, nil
	case ModelModalitiesTextImage:
		return ModelModalitiesTextImage, nil
	default:
		return "", invalidModelRoute("input_modalities 必须是 unknown、text 或 text_image")
	}
}

func normalizeContextWindow(window int64) (*int64, error) {
	if window == 0 {
		return nil, nil
	}
	if window < 0 || window > ModelContextWindowMax {
		return nil, invalidModelRoute(fmt.Sprintf("context_window 必须为 1-%d 或留空", ModelContextWindowMax))
	}
	value := window
	return &value, nil
}

func normalizeReasoningLevels(levels []string) ([]string, error) {
	if len(levels) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(levels))
	for _, raw := range levels {
		level := strings.ToLower(strings.TrimSpace(raw))
		if level == "" {
			return nil, invalidModelRoute("reasoning_levels 不能包含空值")
		}
		valid := false
		for _, allowed := range reasoningLevelOrder {
			if level == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return nil, invalidModelRoute("reasoning_levels 含未知档位")
		}
		seen[level] = struct{}{}
	}
	normalized := make([]string, 0, len(seen))
	for _, level := range reasoningLevelOrder {
		if _, ok := seen[level]; ok {
			normalized = append(normalized, level)
		}
	}
	return normalized, nil
}

// normalizeCapabilities 校验并规范化三项能力声明。
func normalizeCapabilities(modalities string, window int64, levels []string) (string, *int64, []string, error) {
	normalizedModalities, err := normalizeModalities(modalities)
	if err != nil {
		return "", nil, nil, err
	}
	normalizedWindow, err := normalizeContextWindow(window)
	if err != nil {
		return "", nil, nil, err
	}
	normalizedLevels, err := normalizeReasoningLevels(levels)
	if err != nil {
		return "", nil, nil, err
	}
	return normalizedModalities, normalizedWindow, normalizedLevels, nil
}

// AggregateRouteCapabilities 计算同一别名、同一协议下全部候选的共同能力。
//
//   - 模态：任一候选为 text 即收窄为 text；否则任一未声明则为 unknown；
//     全部声明为 text_image 才是 text_image。
//   - 窗口：取已声明候选的最小值；任一候选未声明则标记 HasUndeclaredWindow。
//   - 档位：取全部候选的交集；任一候选未声明或交集为空则为空。
func AggregateRouteCapabilities(routes []ModelRoute) EffectiveRouteCapabilities {
	result := EffectiveRouteCapabilities{
		RouteCount:      len(routes),
		InputModalities: ModelModalitiesUnknown,
		ReasoningLevels: []string{},
	}
	if len(routes) == 0 {
		return result
	}

	hasText, hasUnknown := false, false
	levelsInitialized := false
	var levels []string
	for _, route := range routes {
		switch route.InputModalities {
		case ModelModalitiesText:
			hasText = true
		case ModelModalitiesTextImage:
		default:
			hasUnknown = true
		}

		if route.ContextWindow == nil {
			result.HasUndeclaredWindow = true
		} else if result.ContextWindow == nil || *route.ContextWindow < *result.ContextWindow {
			value := *route.ContextWindow
			result.ContextWindow = &value
		}

		if len(route.ReasoningLevels) == 0 {
			levels = nil
			levelsInitialized = true
			continue
		}
		if !levelsInitialized {
			levels = append([]string(nil), route.ReasoningLevels...)
			levelsInitialized = true
			continue
		}
		if levels == nil {
			continue
		}
		levels = intersectReasoningLevels(levels, route.ReasoningLevels)
	}

	switch {
	case hasText:
		result.InputModalities = ModelModalitiesText
	case hasUnknown:
		result.InputModalities = ModelModalitiesUnknown
	default:
		result.InputModalities = ModelModalitiesTextImage
	}
	if levels != nil {
		result.ReasoningLevels = levels
	}
	return result
}

func intersectReasoningLevels(left, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	rightSet := make(map[string]struct{}, len(right))
	for _, level := range right {
		rightSet[level] = struct{}{}
	}
	intersection := make([]string, 0, len(left))
	for _, level := range left {
		if _, ok := rightSet[level]; ok {
			intersection = append(intersection, level)
		}
	}
	if len(intersection) == 0 {
		return nil
	}
	return intersection
}

func encodeReasoningLevels(levels []string) (any, error) {
	if len(levels) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(levels)
	if err != nil {
		return nil, fmt.Errorf("store: 编码 reasoning_levels 失败：%w", err)
	}
	return string(encoded), nil
}

func decodeReasoningLevels(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var levels []string
	if err := json.Unmarshal([]byte(raw), &levels); err != nil {
		return nil, fmt.Errorf("store: 解析 reasoning_levels 失败：%w", err)
	}
	return levels, nil
}
