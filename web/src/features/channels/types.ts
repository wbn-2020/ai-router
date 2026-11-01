/**
 * 渠道管理端类型（T12）。
 *
 * 与 internal/adminapi/channels.go 的 channelView 和
 * internal/adminapi/channel_secrets.go 的 channelKeyResponse 对齐：
 * - 列表视图只有 key_mask，永远不包含明文或密文；
 * - 完整 Key 只出现在揭示端点，前端只在用户点击后请求一次。
 */

export interface ChannelRuntime {
  state: string;
  in_use: number;
  limit: number;
  open_until?: string | null;
  last_error?: string;
  model_cooldowns: number;
  model_recovering: number;
}

/** Channel 是 /admin/api/channels 返回的脱敏渠道视图。 */
export interface Channel {
  id: number;
  name: string;
  base_url: string;
  protocol: string;
  key_mask: string;
  model_count: number;
  priority: number;
  max_concurrency: number;
  enabled: boolean;
  connection_revision?: number;
  runtime?: ChannelRuntime;
  last_health_revision?: number;
  last_health_model?: string;
  last_health_protocol?: string;
  last_health_kind?: string;
  last_health_duration_ms?: number | null;
  last_used_at?: string;
  last_health_status?: string;
  last_health_latency_ms?: number | null;
  last_health_at?: string;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface ChannelListResponse {
  channels: Channel[];
}

/** ChannelInput 是创建渠道的请求体；key 只在这里以明文出现一次。 */
export interface ChannelInput {
  name: string;
  base_url: string;
  protocol: string;
  key: string;
  models?: string[];
  priority: number;
  max_concurrency: number;
  enabled: boolean;
}

/** ChannelPatch 是 PATCH /admin/api/channels/{id} 的部分更新；省略 key 保留原 Key。 */
export interface ChannelPatch {
  name?: string;
  base_url?: string;
  protocol?: string;
  key?: string;
  priority?: number;
  max_concurrency?: number;
  enabled?: boolean;
}

/** RevealedChannelKey 是 GET /admin/api/channels/{id}/key 的响应（no-store）。 */
export interface RevealedChannelKey {
  key: string;
  mask: string;
}

/** ChannelModelsResponse 是模型发现端点的响应；models 始终为数组。 */
export interface ChannelModelsResponse {
  models: string[];
}

/** PreviewChannelModelsInput 用于尚未保存渠道的草稿模型发现。 */
export interface PreviewChannelModelsInput {
  base_url: string;
  protocol: string;
  key: string;
}

/** ChannelTestResult 与 POST /admin/api/debug/channels/{id}/test 的响应对应。 */
export interface ChannelTestResult {
  status: string;
  http_status: number;
  latency_ms: number;
  duration_ms?: number;
  error_code?: string;
  protocol?: string;
  connection_revision?: number;
  kind?: string;
  error?: string;
  channel_id: number;
  channel_name: string;
  model?: string;
  upstream_model: string;
  tested_at: string;
  request_link: string;
}
