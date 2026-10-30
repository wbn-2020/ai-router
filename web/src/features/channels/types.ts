/**
 * 渠道管理端类型（T12）。
 *
 * 与 internal/adminapi/channels.go 的 channelView 和
 * internal/adminapi/channel_secrets.go 的 channelKeyResponse 对齐：
 * - 列表视图只有 key_mask，永远不包含明文或密文；
 * - 完整 Key 只出现在揭示端点，前端只在用户点击后请求一次。
 */

/** Channel 是 /admin/api/channels 返回的脱敏渠道视图。 */
export interface Channel {
  id: number;
  name: string;
  base_url: string;
  protocol: string;
  key_mask: string;
  priority: number;
  max_concurrency: number;
  enabled: boolean;
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

/** ChannelTestResult 与 POST /admin/api/debug/channels/{id}/test 的响应对应。 */
export interface ChannelTestResult {
  status: string;
  http_status: number;
  latency_ms: number;
  error?: string;
  channel_id: number;
  channel_name: string;
  model?: string;
  upstream_model: string;
  tested_at: string;
  request_link: string;
}
