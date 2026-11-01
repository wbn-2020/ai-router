/** 供应商预设只影响 UI 默认值，不改变后端协议枚举。 */
export interface ProviderPreset {
  id: string;
  name: string;
  description: string;
  protocol: string;
  badge: string;
  defaultBaseURL: string;
  supportsModelDiscovery: boolean;
  disabled?: boolean;
}

export const PROVIDER_PRESETS: ProviderPreset[] = [
  {
    id: 'openai-responses',
    name: 'OpenAI Responses',
    description: 'Codex、Responses API 原生直通',
    protocol: 'openai_responses',
    badge: 'Responses',
    defaultBaseURL: 'https://api.openai.com/v1',
    supportsModelDiscovery: true,
  },
  {
    id: 'openai-chat',
    name: 'OpenAI Chat',
    description: 'Chat Completions 原生直通',
    protocol: 'openai_chat',
    badge: 'Chat',
    defaultBaseURL: 'https://api.openai.com/v1',
    supportsModelDiscovery: true,
  },
  {
    id: 'custom-responses',
    name: '自定义 Responses',
    description: '中转站或自建 Responses 兼容服务',
    protocol: 'openai_responses',
    badge: '自定义',
    defaultBaseURL: '',
    supportsModelDiscovery: true,
  },
  {
    id: 'custom-chat',
    name: '自定义 Chat',
    description: '中转站或自建 Chat 兼容服务',
    protocol: 'openai_chat',
    badge: '自定义',
    defaultBaseURL: '',
    supportsModelDiscovery: true,
  },
  {
    id: 'anthropic',
    name: 'Anthropic Messages',
    description: '协议预留，当前不参与调度',
    protocol: 'anthropic_messages',
    badge: '预留',
    defaultBaseURL: 'https://api.anthropic.com/v1',
    supportsModelDiscovery: false,
    disabled: true,
  },
];

export function findProviderPreset(id: string): ProviderPreset {
  return PROVIDER_PRESETS.find((preset) => preset.id === id) ?? PROVIDER_PRESETS[0]!;
}

export function protocolLabel(protocol: string): string {
  switch (protocol) {
    case 'openai_responses':
      return 'Responses';
    case 'openai_chat':
      return 'Chat';
    case 'anthropic_messages':
      return 'Anthropic（预留）';
    default:
      return protocol === '' ? '未知' : protocol;
  }
}

/** deriveChannelName 从 Base URL 主机名生成默认渠道名。 */
export function deriveChannelName(baseURL: string, protocol: string): string {
  try {
    const parsed = new URL(baseURL.trim());
    const host = parsed.hostname.replace(/^www\./i, '');
    if (host === '') {
      return '';
    }
    const suffix = protocol === 'openai_chat' ? 'chat' : 'responses';
    return `${host}-${suffix}`.slice(0, 120);
  } catch {
    return '';
  }
}
