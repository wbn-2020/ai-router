/**
 * 渠道管理端 API（T12）。
 *
 * 约束（R-003 / R-014）：
 * - 列表只返回脱敏 Key；完整 Key 只经揭示端点获取，响应为 no-store；
 * - 创建/更新是唯一接收明文 Key 的请求，后端立即 AES-256-GCM 加密；
 * - 调试测试调用 T22 的 /admin/api/debug/channels/{id}/test，错误由服务端脱敏；
 * - 所有请求经 web/src/lib/api.ts，统一携带 session cookie、禁用缓存并处理 401。
 */

import { apiRequest } from '../../lib/api';
import type {
  Channel,
  ChannelInput,
  ChannelListResponse,
  ChannelPatch,
  ChannelTestResult,
  RevealedChannelKey,
} from './types';

export const channelsApi = {
  list(): Promise<ChannelListResponse> {
    return apiRequest<ChannelListResponse>('/admin/api/channels');
  },

  create(input: ChannelInput): Promise<Channel> {
    return apiRequest<Channel>('/admin/api/channels', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  },

  update(id: number, patch: ChannelPatch): Promise<Channel> {
    return apiRequest<Channel>(`/admin/api/channels/${encodeURIComponent(String(id))}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    });
  },

  remove(id: number): Promise<void> {
    return apiRequest<void>(`/admin/api/channels/${encodeURIComponent(String(id))}`, {
      method: 'DELETE',
    });
  },

  /** revealKey 只在管理员点击“显示 Key”后调用；调用方不得缓存结果。 */
  revealKey(id: number): Promise<RevealedChannelKey> {
    return apiRequest<RevealedChannelKey>(
      `/admin/api/channels/${encodeURIComponent(String(id))}/key`,
    );
  },

  /** testChannel 使用渠道上已启用的映射发送最小探针；model 省略时用默认映射。 */
  testChannel(id: number, model?: string): Promise<ChannelTestResult> {
    const trimmed = model?.trim() ?? '';
    return apiRequest<ChannelTestResult>(
      `/admin/api/debug/channels/${encodeURIComponent(String(id))}/test`,
      {
        method: 'POST',
        body: JSON.stringify(trimmed === '' ? {} : { model: trimmed }),
      },
    );
  },
};
