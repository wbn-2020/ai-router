import { ApiError } from '../../lib/api';

export const PRIORITY_MIN = 0;
export const PRIORITY_MAX = 1000;
export const CONCURRENCY_MIN = 1;
export const CONCURRENCY_MAX = 64;

export const PROTOCOL_OPTIONS = [
  { value: 'openai_responses', label: 'OpenAI Responses' },
  { value: 'openai_chat', label: 'OpenAI Chat' },
  { value: 'anthropic_messages', label: 'Anthropic Messages（预留，暂不可调度）' },
] as const;

export interface ChannelFormState {
  providerID: string;
  name: string;
  baseURL: string;
  protocol: string;
  key: string;
  priority: string;
  maxConcurrency: string;
  enabled: boolean;
  models: string[];
}

export type ChannelFormField =
  | 'name'
  | 'baseURL'
  | 'protocol'
  | 'key'
  | 'priority'
  | 'maxConcurrency';

export type ChannelFormErrors = Partial<Record<ChannelFormField, string>>;

export const EMPTY_CHANNEL_FORM: ChannelFormState = {
  providerID: 'openai-responses',
  name: '',
  baseURL: '',
  protocol: 'openai_responses',
  key: '',
  priority: '0',
  maxConcurrency: '1',
  enabled: true,
  models: [],
};

export function parseIntegerField(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === '') {
    return null;
  }
  const value = Number(trimmed);
  return Number.isInteger(value) ? value : null;
}

/** validateBaseURL 与后端 normalizeChannelBaseURL 的规则保持同构。 */
export function validateBaseURL(raw: string): string | null {
  const value = raw.trim();
  if (value === '') {
    return '请输入 Base URL';
  }

  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return 'Base URL 不是合法的 URL';
  }

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return 'Base URL 只允许 http/https';
  }
  if (parsed.hostname === '') {
    return 'Base URL 缺少主机名';
  }
  if (parsed.username !== '' || parsed.password !== '') {
    return 'Base URL 不能包含用户信息';
  }
  if (parsed.hash !== '' || value.includes('#')) {
    return 'Base URL 不能包含 fragment';
  }
  return null;
}

export function validateChannelForm(
  form: ChannelFormState,
  editing: boolean,
): ChannelFormErrors {
  const errors: ChannelFormErrors = {};

  if (form.name.trim() === '') {
    errors.name = '请输入渠道名称';
  }

  const baseURLError = validateBaseURL(form.baseURL);
  if (baseURLError !== null) {
    errors.baseURL = baseURLError;
  }

  if (!PROTOCOL_OPTIONS.some((option) => option.value === form.protocol)) {
    errors.protocol = '请选择渠道协议';
  }

  if (!editing) {
    if (form.key.trim() === '') {
      errors.key = '请输入上游 Key';
    }
  } else if (form.key !== '' && form.key.trim() === '') {
    errors.key = '上游 Key 不能只包含空白字符';
  }

  const priority = parseIntegerField(form.priority);
  if (priority === null || priority < PRIORITY_MIN || priority > PRIORITY_MAX) {
    errors.priority = `优先级必须是 ${PRIORITY_MIN} 到 ${PRIORITY_MAX} 的整数`;
  }

  const concurrency = parseIntegerField(form.maxConcurrency);
  if (
    concurrency === null ||
    concurrency < CONCURRENCY_MIN ||
    concurrency > CONCURRENCY_MAX
  ) {
    errors.maxConcurrency = `并发上限必须是 ${CONCURRENCY_MIN} 到 ${CONCURRENCY_MAX} 的整数`;
  }

  return errors;
}

/** describeChannelMutationError 把管理端错误码映射为安全文案。 */
export function describeChannelMutationError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'channel_name_conflict':
        return '渠道名称已存在，请更换名称。';
      case 'invalid_channel_config':
        return error.message || '渠道配置无效，请检查输入。';
      case 'invalid_models':
        return error.message || '模型列表无效，请检查模型名。';
      case 'invalid_key':
        return '上游 Key 不能为空。';
      case 'channel_not_found':
        return '渠道不存在，可能已被删除，请刷新页面后重试。';
      case 'channels_unavailable':
        return '渠道管理暂不可用：数据库或主密钥未就绪。';
      default:
        break;
    }
    if (error.status === 409) {
      return '渠道名称已存在，请更换名称。';
    }
    if (error.status === 400) {
      return error.message || '渠道配置无效，请检查输入。';
    }
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `${fallback}（HTTP ${error.status}）。`;
  }
  return `${fallback}，请检查网络连接后重试。`;
}

/** describeModelDiscoveryError 只返回模型发现相关的安全文案。 */
export function describeModelDiscoveryError(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'upstream_auth_failed':
        return '上游拒绝了模型列表请求，请检查 Key 与协议。';
      case 'model_discovery_failed':
        return error.message || '上游没有可用的 /models 端点，可手动添加模型。';
      case 'upstream_rate_limited':
        return '上游限流，暂时无法获取模型列表。';
      case 'upstream_timeout':
        return '获取模型列表超时，请稍后重试。';
      case 'unsupported_protocol':
        return '该协议暂不支持自动获取模型，请手动添加。';
      case 'invalid_key':
        return '请先填写上游 Key。';
      case 'invalid_channel_config':
        return error.message || 'Base URL 或协议无效。';
      case 'channel_key_unavailable':
        return '渠道 Key 暂不可用，请重新保存 Key 后再试。';
      default:
        break;
    }
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || '无法从上游获取模型列表。';
  }
  return '无法从上游获取模型列表，请检查网络连接后重试。';
}

const TIMESTAMP_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

export function formatChannelTimestamp(value: string | undefined): string {
  if (typeof value !== 'string' || value === '') {
    return '—';
  }
  const parsed = new Date(value.replace(/(\.\d{3})\d+/, '$1'));
  return Number.isNaN(parsed.getTime()) ? '—' : TIMESTAMP_FORMATTER.format(parsed);
}

export function describeChannelTestError(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'channel_has_no_mapping':
        return '该渠道没有启用的模型映射，请先添加并启用模型。';
      case 'model_route_not_found':
        return '该模型在此渠道没有启用的映射。';
      case 'channel_not_found':
        return '渠道不存在，可能已被删除。';
      case 'channels_unavailable':
        return '测试暂不可用：数据库或主密钥未就绪。';
      default:
        break;
    }
    if (error.status === 400) {
      return error.message || '测试请求无效。';
    }
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `测试失败（HTTP ${error.status}）。`;
  }
  return '测试失败，请检查网络连接后重试。';
}

/** 抽屉保持键盘焦点；折叠的高级设置不参与 Tab 顺序。 */
export function keepChannelDialogFocus(event: KeyboardEvent, dialog: HTMLElement | null): void {
  if (event.key !== 'Tab' || dialog === null) {
    return;
  }
  const elements = Array.from(
    dialog.querySelectorAll<HTMLElement>(
      'button:not(:disabled), input:not(:disabled), select:not(:disabled), a[href], summary, [tabindex="0"]',
    ),
  ).filter((element) => {
    const collapsed = element.closest('details:not([open])');
    return collapsed === null || (element.tagName === 'SUMMARY' && element.parentElement === collapsed);
  });
  const first = elements[0];
  const last = elements[elements.length - 1];
  if (first === undefined || last === undefined) {
    event.preventDefault();
    return;
  }
  if (event.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (document.activeElement === last || !dialog.contains(document.activeElement))) {
    event.preventDefault();
    first.focus();
  }
}

export function channelHost(baseURL: string): string {
  try {
    const parsed = new URL(baseURL);
    return parsed.host;
  } catch {
    return baseURL;
  }
}
