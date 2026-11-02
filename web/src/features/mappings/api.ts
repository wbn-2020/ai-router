/**
 * 模型映射管理端 API。
 *
 * 约束（R-004 / T09）：
 * - 渠道创建时会自动生成同名映射；本 API 负责别名、多渠道分流与手工补充；
 * - 同一 client_model + channel_id 由后端唯一约束兜底，冲突返回 409；
 * - 映射页只需要渠道的名称/协议/启停状态，不读取或展示任何上游 Key；
 * - 所有请求经 web/src/lib/api.ts，统一携带 session cookie、禁用缓存并处理 401。
 */

import { apiRequest } from '../../lib/api';

/**
 * InputModalities 是逐路由的输入模态声明。
 *
 * unknown 表示未声明，不容错为“不支持图片”或“支持图片”，由服务端与调用方
 * 决定降级策略；与 internal/store/capabilities.go 的枚举一致。
 */
export type InputModalities = 'unknown' | 'text' | 'text_image';

/** ReasoningLevel 与 internal/store/capabilities.go 的档位顺序一致。 */
export type ReasoningLevel = 'none' | 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'ultra';

/**
 * ModelRoute 是 /admin/api/model-routes 返回的映射视图。
 *
 * 能力字段保持可选：旧响应（或测试 fixture）没有这些键时按“未声明”展示。
 */
export interface ModelRoute {
  id: number;
  client_model: string;
  channel_id: number;
  upstream_model: string;
  priority: number;
  enabled: boolean;
  input_modalities?: InputModalities;
  context_window?: number | null;
  reasoning_levels?: ReasoningLevel[] | null;
  created_at: string;
  updated_at: string;
}

export interface ModelRouteListResponse {
  model_routes: ModelRoute[];
}

/** ModelsResponse 来自 /admin/api/models：至少有一条启用映射的客户端模型。 */
export interface ModelsResponse {
  models: string[];
}

/**
 * ChannelSummary 是映射页用到的渠道字段子集。
 *
 * 渠道列表还包含 key_mask 等字段，但映射页不需要，也不会渲染它们。
 */
export interface ChannelSummary {
  id: number;
  name: string;
  protocol: string;
  priority: number;
  enabled: boolean;
}

export interface ChannelListResponse {
  channels: ChannelSummary[];
}

/**
 * ModelRouteInput 是创建映射与服务端全量更新共用的请求体。
 *
 * 能力字段可选：创建时省略等于未声明（服务端默认 unknown/空窗口/空档位）。
 */
export interface ModelRouteInput {
  client_model: string;
  channel_id: number;
  upstream_model: string;
  priority: number;
  enabled: boolean;
  input_modalities?: InputModalities;
  context_window?: number | null;
  reasoning_levels?: ReasoningLevel[];
}

/**
 * ModelRoutePatch 是 PATCH /admin/api/model-routes/{id} 的部分更新。
 *
 * 省略能力字段等于保持原值；显式清空用 context_window: 0 与 reasoning_levels: []，
 * 模态清空用 input_modalities: 'unknown'。
 */
export type ModelRoutePatch = Partial<ModelRouteInput>;

export const mappingsApi = {
  listRoutes(): Promise<ModelRouteListResponse> {
    return apiRequest<ModelRouteListResponse>('/admin/api/model-routes');
  },

  listModels(): Promise<ModelsResponse> {
    return apiRequest<ModelsResponse>('/admin/api/models');
  },

  listChannels(): Promise<ChannelListResponse> {
    return apiRequest<ChannelListResponse>('/admin/api/channels');
  },

  createRoute(input: ModelRouteInput): Promise<ModelRoute> {
    return apiRequest<ModelRoute>('/admin/api/model-routes', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  },

  updateRoute(id: number, patch: ModelRoutePatch): Promise<ModelRoute> {
    return apiRequest<ModelRoute>(`/admin/api/model-routes/${encodeURIComponent(String(id))}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    });
  },

  deleteRoute(id: number): Promise<void> {
    return apiRequest<void>(`/admin/api/model-routes/${encodeURIComponent(String(id))}`, {
      method: 'DELETE',
    });
  },
};
