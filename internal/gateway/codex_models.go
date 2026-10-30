package gateway

import (
	"os"
	"strconv"
	"strings"
)

// Codex 0.164.0 的模型选择器读取完整 ModelInfo descriptor，而不是普通
// OpenAI /v1/models 的最简条目。除 slug/display_name 外，多个可空字段必须
// 以 null 或空数组显式出现；缺失字段会让 Codex 的 Rust serde 解析失败或
// 模型选择器为空。
//
// 本文件只依据已记录的协议字段构造 descriptor，不访问上游模型目录，也不
// 暴露上游模型名、渠道或 Key。
const (
	// defaultCodexContextWindow 是未配置 GATEWAY_CODEX_CONTEXT_WINDOW 时的
	// 默认上下文窗口；与 internal/config 的默认值保持一致。
	defaultCodexContextWindow = 128_000
	// codexContextWindowEnv 与 internal/config.EnvCodexContextWindow 同名。
	// 数据面可通过 Options.CodexContextWindow 显式传入；未传入时在 New 时
	// 读取该环境变量，使直接构造 Handler 的入口也遵守同一配置契约。
	codexContextWindowEnv = "GATEWAY_CODEX_CONTEXT_WINDOW"

	codexModelPriority       = 50
	codexToolOutputMaxTokens = 10_000
	codexWindowPercent       = 95

	// codexInstructionsTemplate 是 manifest 下发给 Codex 的通用系统提示。
	// 保持非空，避免空模板被 Codex 当作缺失配置。
	codexInstructionsTemplate = "You are a coding agent. Inspect the workspace, " +
		"use the available tools when needed, and complete the requested task."
)

// codexManifest 是 GET /backend-api/codex/models 与
// GET /v1/models?client_version=... 的顶层响应。
type codexManifest struct {
	Models []codexModelInfo `json:"models"`
}

// codexReasoningLevel 对应 Codex ModelInfo 的 supported_reasoning_levels 条目。
type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// codexServiceTier 对应 Codex ModelInfo 的 service_tiers 条目。
type codexServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// codexTruncationPolicy 对应 Codex ModelInfo 的 truncation_policy。
type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

// codexModelMessages 是 Codex ModelInfo 的 model_messages 对象。
//
// 可空子字段使用 any 且不加 omitempty，保证序列化为 null 而不是被省略；
// 协议要求这些键存在。
type codexModelMessages struct {
	InstructionsTemplate  string `json:"instructions_template"`
	InstructionsVariables any    `json:"instructions_variables"`
	Approvals             any    `json:"approvals"`
	CollaborationModes    any    `json:"collaboration_modes"`
	AutoReview            any    `json:"auto_review"`
	Permissions           any    `json:"permissions"`
	MultiAgent            any    `json:"multi_agent"`
	TokenBudget           any    `json:"token_budget"`
	GuardianV2            any    `json:"guardian_v2"`
}

// codexModelInfo 是 Codex 0.164.0 可接受的完整 ModelInfo descriptor。
//
// 字段集合与协议证据一致；所有字段都必须序列化（包括 null），因此没有
// omitempty。不要为了“精简响应”删除字段或把可空字段改成省略。
type codexModelInfo struct {
	Slug                              string                `json:"slug"`
	DisplayName                       string                `json:"display_name"`
	Description                       string                `json:"description"`
	DefaultReasoningLevel             *string               `json:"default_reasoning_level"`
	SupportedReasoningLevels          []codexReasoningLevel `json:"supported_reasoning_levels"`
	MultiAgentReasoningEffort         *string               `json:"multi_agent_reasoning_effort"`
	ShellType                         string                `json:"shell_type"`
	Visibility                        string                `json:"visibility"`
	SupportedInAPI                    bool                  `json:"supported_in_api"`
	Priority                          int                   `json:"priority"`
	AdditionalSpeedTiers              []string              `json:"additional_speed_tiers"`
	ServiceTiers                      []codexServiceTier    `json:"service_tiers"`
	DefaultServiceTier                any                   `json:"default_service_tier"`
	AvailabilityNUX                   any                   `json:"availability_nux"`
	Upgrade                           any                   `json:"upgrade"`
	ModelMessages                     codexModelMessages    `json:"model_messages"`
	IncludeSkillsUsageInstructions    bool                  `json:"include_skills_usage_instructions"`
	IncludePluginUsageInstructions    bool                  `json:"include_plugin_usage_instructions"`
	IncludeAppsUsageInstructions      bool                  `json:"include_apps_usage_instructions"`
	SupportsReasoningSummaryParameter bool                  `json:"supports_reasoning_summary_parameter"`
	DefaultReasoningSummary           string                `json:"default_reasoning_summary"`
	SupportVerbosity                  bool                  `json:"support_verbosity"`
	DefaultVerbosity                  any                   `json:"default_verbosity"`
	ApplyPatchToolType                any                   `json:"apply_patch_tool_type"`
	WebSearchToolType                 string                `json:"web_search_tool_type"`
	TruncationPolicy                  codexTruncationPolicy `json:"truncation_policy"`
	SupportsImageDetailOriginal       bool                  `json:"supports_image_detail_original"`
	SupportsParallelToolCalls         bool                  `json:"supports_parallel_tool_calls"`
	ContextWindow                     int64                 `json:"context_window"`
	MaxContextWindow                  int64                 `json:"max_context_window"`
	AutoCompactTokenLimit             any                   `json:"auto_compact_token_limit"`
	CompHash                          any                   `json:"comp_hash"`
	EffectiveContextWindowPercent     int                   `json:"effective_context_window_percent"`
	ExperimentalSupportedTools        []string              `json:"experimental_supported_tools"`
	InputModalities                   []string              `json:"input_modalities"`
	SupportsSearchTool                bool                  `json:"supports_search_tool"`
	UseResponsesLite                  bool                  `json:"use_responses_lite"`
	NodeREPLAutoReviewRequired        bool                  `json:"node_repl_auto_review_required"`
	NodeREPLDisabled                  bool                  `json:"node_repl_disabled"`
	AutoReviewModelOverride           any                   `json:"auto_review_model_override"`
	ModelSpecialty                    any                   `json:"model_specialty"`
	ToolMode                          any                   `json:"tool_mode"`
	MultiAgentVersion                 any                   `json:"multi_agent_version"`
}

// newCodexManifestBuilder 返回 Handler 使用的 manifest 构造器。
//
// configured 来自 Options.CodexContextWindow；<=0 时回退到
// GATEWAY_CODEX_CONTEXT_WINDOW，再回退到默认值。
func newCodexManifestBuilder(configured int) codexManifestBuilder {
	window := resolveCodexContextWindow(configured)
	return func(models []string) any {
		return buildCodexManifest(models, window)
	}
}

// buildCodexManifest 从启用映射的客户端模型生成完整 Codex manifest。
//
// 空白模型名被跳过，重复模型名只保留首次出现，保证 slug 与启用的客户端
// 模型一致且不重复。空列表返回合法的 {"models":[]}。
func buildCodexManifest(models []string, contextWindow int) codexManifest {
	if contextWindow <= 0 {
		contextWindow = defaultCodexContextWindow
	}

	entries := make([]codexModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		slug := strings.TrimSpace(raw)
		if slug == "" {
			continue
		}
		if _, exists := seen[slug]; exists {
			continue
		}
		seen[slug] = struct{}{}
		entries = append(entries, newCodexModelInfo(slug, contextWindow))
	}
	return codexManifest{Models: entries}
}

// newCodexModelInfo 构造一个面向任意上游模型的完整 descriptor。
//
// 网关不做跨协议转换，也无法从手工映射推断上游能力；因此只声明最小、
// 保守且协议合法的能力：文本+图片输入、不启用 reasoning/搜索/并行工具、
// 上下文窗口由配置统一指定。
func newCodexModelInfo(slug string, contextWindow int) codexModelInfo {
	window := int64(contextWindow)
	defaultReasoningLevel := "none"

	return codexModelInfo{
		Slug:        slug,
		DisplayName: slug,
		Description: "Model routed through the AI gateway.",
		SupportedReasoningLevels: []codexReasoningLevel{
			{Effort: "none", Description: "No reasoning"},
		},
		DefaultReasoningLevel:     &defaultReasoningLevel,
		MultiAgentReasoningEffort: nil,
		ShellType:                 "unified_exec",
		Visibility:                "list",
		SupportedInAPI:            true,
		Priority:                  codexModelPriority,
		AdditionalSpeedTiers:      []string{},
		ServiceTiers:              []codexServiceTier{},
		DefaultServiceTier:        nil,
		AvailabilityNUX:           nil,
		Upgrade:                   nil,
		ModelMessages: codexModelMessages{
			InstructionsTemplate:  codexInstructionsTemplate,
			InstructionsVariables: nil,
			Approvals:             nil,
			CollaborationModes:    nil,
			AutoReview:            nil,
			Permissions:           nil,
			MultiAgent:            nil,
			TokenBudget:           nil,
			GuardianV2:            nil,
		},
		IncludeSkillsUsageInstructions:    false,
		IncludePluginUsageInstructions:    false,
		IncludeAppsUsageInstructions:      false,
		SupportsReasoningSummaryParameter: true,
		DefaultReasoningSummary:           "auto",
		SupportVerbosity:                  false,
		DefaultVerbosity:                  nil,
		ApplyPatchToolType:                nil,
		WebSearchToolType:                 "text",
		TruncationPolicy: codexTruncationPolicy{
			Mode:  "bytes",
			Limit: codexToolOutputMaxTokens,
		},
		SupportsImageDetailOriginal:   false,
		SupportsParallelToolCalls:     false,
		ContextWindow:                 window,
		MaxContextWindow:              window,
		AutoCompactTokenLimit:         nil,
		CompHash:                      nil,
		EffectiveContextWindowPercent: codexWindowPercent,
		ExperimentalSupportedTools:    []string{},
		InputModalities:               []string{"text", "image"},
		SupportsSearchTool:            false,
		UseResponsesLite:              false,
		NodeREPLAutoReviewRequired:    false,
		NodeREPLDisabled:              true,
		AutoReviewModelOverride:       nil,
		ModelSpecialty:                nil,
		ToolMode:                      nil,
		MultiAgentVersion:             nil,
	}
}

// resolveCodexContextWindow 解析 manifest 的 context_window。
//
// 优先级：显式 Options 值 > GATEWAY_CODEX_CONTEXT_WINDOW > 默认值。
// 非法环境变量按默认值处理；进程启动时 internal/config 已负责拒绝非法值。
func resolveCodexContextWindow(configured int) int {
	if configured > 0 {
		return configured
	}
	if raw := strings.TrimSpace(os.Getenv(codexContextWindowEnv)); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			return value
		}
	}
	return defaultCodexContextWindow
}
