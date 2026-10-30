import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from 'react';

import { ApiError, isUnauthorizedError } from '../../lib/api';
import { channelsApi } from './api';
import type { Channel, ChannelTestResult } from './types';

/**
 * 渠道页（T12）。
 *
 * 安全边界（R-003）：
 * - 列表只渲染 /admin/api/channels 返回的 key_mask，初始 DOM 不含完整 Key；
 * - 完整 Key 只在点击“显示 Key”后请求一次，放在只读输入框中；
 * - 关闭揭示抽屉、切换渠道或离开页面即清除内存中的完整 Key；
 * - 错误统一显示安全文案，不回显上游 Key、密文或 base URL 查询串；
 * - 测试按钮调用 T22 调试 API，失败原因由服务端脱敏后展示。
 */

const PRIORITY_MIN = 0;
const PRIORITY_MAX = 1000;
const CONCURRENCY_MIN = 1;
const CONCURRENCY_MAX = 64;

const PROTOCOL_OPTIONS = [
  { value: 'openai_responses', label: 'OpenAI Responses' },
  { value: 'openai_chat', label: 'OpenAI Chat' },
  { value: 'anthropic_messages', label: 'Anthropic Messages（预留，暂不可调度）' },
] as const;

/* -------------------------------------------------------------------------- */
/* 展示辅助                                                                    */
/* -------------------------------------------------------------------------- */

const TIMESTAMP_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

/** Go 的 RFC3339Nano 最多 9 位小数；部分浏览器只接受 3 位，截断后再解析。 */
function formatTimestamp(value: string | undefined): string {
  if (typeof value !== 'string' || value === '') {
    return '—';
  }
  const parsed = new Date(value.replace(/(\.\d{3})\d+/, '$1'));
  if (Number.isNaN(parsed.getTime())) {
    return '—';
  }
  return TIMESTAMP_FORMATTER.format(parsed);
}

/** sortChannels 与后端 List 的排序一致：priority ASC → id ASC。 */
function sortChannels(channels: Channel[]): Channel[] {
  return [...channels].sort(
    (left, right) => left.priority - right.priority || left.id - right.id,
  );
}

function protocolLabel(protocol: string): string {
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

function HealthCell({ channel }: { channel: Channel }) {
  const status = channel.last_health_status ?? '';
  const testedAt = formatTimestamp(channel.last_health_at);

  if (status === 'healthy') {
    const latency = channel.last_health_latency_ms;
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="badge" title={testedAt === '—' ? undefined : `最近测试：${testedAt}`}>
          {typeof latency === 'number' ? `正常 · ${latency} ms` : '正常'}
        </span>
      </div>
    );
  }

  if (status === 'unhealthy') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span
          className="badge badge--muted"
          style={{ color: 'var(--color-danger)' }}
          title={testedAt === '—' ? undefined : `最近测试：${testedAt}`}
        >
          异常
        </span>
        {typeof channel.last_error === 'string' && channel.last_error !== '' ? (
          <span
            className="muted"
            style={{
              maxWidth: 180,
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
              fontSize: 12,
            }}
            title={channel.last_error}
          >
            {channel.last_error}
          </span>
        ) : null}
      </div>
    );
  }

  return <span className="muted">未测试</span>;
}

/* -------------------------------------------------------------------------- */
/* 复制                                                                        */
/* -------------------------------------------------------------------------- */

/**
 * copyToClipboard 优先使用异步 Clipboard API；在非安全上下文（例如通过
 * 局域网 IP 的 HTTP 访问）退回到临时 textarea + execCommand。
 */
async function copyToClipboard(text: string): Promise<void> {
  const clipboard: Clipboard | undefined = navigator.clipboard;
  if (clipboard !== undefined && typeof clipboard.writeText === 'function') {
    await clipboard.writeText(text);
    return;
  }

  if (typeof document.execCommand !== 'function') {
    throw new Error('clipboard unavailable');
  }

  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.top = '-1000px';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  try {
    textarea.select();
    if (!document.execCommand('copy')) {
      throw new Error('copy command failed');
    }
  } finally {
    document.body.removeChild(textarea);
  }
}

/* -------------------------------------------------------------------------- */
/* 表单状态与校验                                                              */
/* -------------------------------------------------------------------------- */

interface FormState {
  name: string;
  baseURL: string;
  protocol: string;
  key: string;
  priority: string;
  maxConcurrency: string;
  enabled: boolean;
}

type FormField = 'name' | 'baseURL' | 'protocol' | 'key' | 'priority' | 'maxConcurrency';

type FormErrors = Partial<Record<FormField, string>>;

const EMPTY_FORM: FormState = {
  name: '',
  baseURL: '',
  protocol: 'openai_responses',
  key: '',
  priority: '0',
  maxConcurrency: '1',
  enabled: true,
};

function parseIntegerField(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === '') {
    return null;
  }
  const value = Number(trimmed);
  return Number.isInteger(value) ? value : null;
}

/** validateBaseURL 与后端 normalizeChannelBaseURL 的规则保持同构。 */
function validateBaseURL(raw: string): string | null {
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

/**
 * validateChannelForm 在发请求前拦截明显的字段错误。
 *
 * 后端仍会做同样的校验与 name 唯一约束；这里只是让管理员尽快看到问题。
 * 编辑时 Key 可留空，表示保留现有密文。
 */
function validateChannelForm(form: FormState, editing: boolean): FormErrors {
  const errors: FormErrors = {};

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

/* -------------------------------------------------------------------------- */
/* 错误文案                                                                    */
/* -------------------------------------------------------------------------- */

function describeLoadError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'channels_unavailable' || error.status === 503) {
      return '渠道管理暂不可用：数据库或主密钥未就绪。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `加载渠道失败（HTTP ${error.status}）。`;
  }
  return '加载渠道失败，请检查网络连接后重试。';
}

function describeMutationError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'channel_name_conflict':
        return '渠道名称已存在，请更换名称。';
      case 'invalid_channel_config':
        return error.message || '渠道配置无效，请检查输入。';
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

/** describeRevealError 只返回固定安全文案，绝不拼接服务端原始错误。 */
function describeRevealError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'channel_not_found') {
      return '渠道不存在，可能已被删除。';
    }
    if (error.code === 'channels_unavailable' || error.status === 503) {
      return '无法读取完整 Key：数据库或主密钥未就绪。';
    }
    return '无法读取完整 Key：服务器内部错误。';
  }
  return '无法读取完整 Key，请检查网络连接后重试。';
}

function describeTestError(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'channel_has_no_mapping':
        return error.message || '该渠道没有启用的模型映射，请先在模型映射页配置。';
      case 'model_route_not_found':
        return error.message || '该模型在此渠道没有启用的映射。';
      case 'channel_not_found':
        return '渠道不存在，可能已被删除。';
      case 'channels_unavailable':
        return '调试暂不可用：数据库或主密钥未就绪。';
      default:
        break;
    }
    if (error.status === 400) {
      return error.message || '调试请求无效。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `测试失败（HTTP ${error.status}）。`;
  }
  return '测试失败，请检查网络连接后重试。';
}

/* -------------------------------------------------------------------------- */
/* 抽屉样式                                                                    */
/* -------------------------------------------------------------------------- */

const OVERLAY_STYLE: CSSProperties = {
  position: 'fixed',
  inset: 0,
  zIndex: 50,
  display: 'flex',
  justifyContent: 'flex-end',
  background: 'rgba(15, 23, 42, 0.42)',
};

const DRAWER_STYLE: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  width: 'min(560px, 100vw)',
  height: '100%',
  background: 'var(--color-surface)',
  boxShadow: 'var(--shadow-lg)',
};

const DRAWER_HEADER_STYLE: CSSProperties = {
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'space-between',
  gap: 16,
  padding: '18px 22px',
  borderBottom: '1px solid var(--color-border)',
};

const DRAWER_BODY_STYLE: CSSProperties = {
  flex: 1,
  overflowY: 'auto',
  padding: '20px 22px 28px',
};

const FORM_GRID_STYLE: CSSProperties = {
  display: 'grid',
  gap: 14,
  gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
  marginTop: 16,
};

const CHECKBOX_STYLE: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 8,
  fontSize: 13,
  fontWeight: 600,
  color: 'var(--color-text-soft)',
};

/* -------------------------------------------------------------------------- */
/* 揭示抽屉状态                                                                */
/* -------------------------------------------------------------------------- */

type KeyDrawerState =
  | { channel: Channel; status: 'loading' }
  | { channel: Channel; status: 'ready'; key: string; mask: string }
  | { channel: Channel; status: 'error'; message: string };

type BusyKind = 'toggle' | 'test' | 'delete';

/* -------------------------------------------------------------------------- */
/* 页面                                                                        */
/* -------------------------------------------------------------------------- */

export function ChannelsPage(): ReactNode {
  const [channels, setChannels] = useState<Channel[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);

  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<{ id: number; kind: BusyKind } | null>(null);
  const [confirmingDeleteID, setConfirmingDeleteID] = useState<number | null>(null);
  const [testOutcome, setTestOutcome] = useState<ChannelTestResult | null>(null);

  const [keyDrawer, setKeyDrawer] = useState<KeyDrawerState | null>(null);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);
  const revealSequence = useRef(0);
  const keyCloseRef = useRef<HTMLButtonElement>(null);

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Channel | null>(null);
  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [formErrors, setFormErrors] = useState<FormErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const formCloseRef = useRef<HTMLButtonElement>(null);

  const loadInitial = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const response = await channelsApi.list();
      setChannels(sortChannels(response.channels ?? []));
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setLoadError(describeLoadError(error));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadInitial();
  }, [loadInitial]);

  /** refreshList 在写操作后静默刷新，保留当前页面而不是回到加载态。 */
  const refreshList = useCallback(async () => {
    try {
      const response = await channelsApi.list();
      setChannels(sortChannels(response.channels ?? []));
      setLoadError(null);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError('渠道列表刷新失败，当前显示的可能不是最新状态。');
    }
  }, []);

  const closeKeyDrawer = useCallback(() => {
    // 递增序号使仍在途的揭示请求无法在关闭后回填完整 Key。
    revealSequence.current += 1;
    setKeyDrawer(null);
    setCopied(false);
    setCopyError(null);
  }, []);

  const openKeyDrawer = useCallback(async (channel: Channel) => {
    revealSequence.current += 1;
    const sequence = revealSequence.current;
    setCopied(false);
    setCopyError(null);
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
    setKeyDrawer({ channel, status: 'loading' });

    try {
      const response = await channelsApi.revealKey(channel.id);
      if (revealSequence.current !== sequence) {
        return;
      }
      setKeyDrawer({ channel, status: 'ready', key: response.key, mask: response.mask });
    } catch (error) {
      if (revealSequence.current !== sequence) {
        return;
      }
      if (isUnauthorizedError(error)) {
        setKeyDrawer(null);
        setUnauthorized(true);
        return;
      }
      setKeyDrawer({ channel, status: 'error', message: describeRevealError(error) });
    }
  }, []);

  const handleKeyButton = useCallback(
    (channel: Channel) => {
      if (keyDrawer !== null && keyDrawer.channel.id === channel.id) {
        closeKeyDrawer();
        return;
      }
      void openKeyDrawer(channel);
    },
    [closeKeyDrawer, keyDrawer, openKeyDrawer],
  );

  const handleCopyKey = useCallback(async (key: string) => {
    try {
      await copyToClipboard(key);
      setCopied(true);
      setCopyError(null);
    } catch {
      setCopied(false);
      setCopyError('复制失败，请手动选择输入框中的 Key 后复制。');
    }
  }, []);

  const resetForm = useCallback(() => {
    setForm(EMPTY_FORM);
    setFormErrors({});
    setFormError(null);
    setEditing(null);
  }, []);

  const openCreate = useCallback(() => {
    closeKeyDrawer();
    resetForm();
    setFormOpen(true);
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
  }, [closeKeyDrawer, resetForm]);

  const openEdit = useCallback(
    (channel: Channel) => {
      closeKeyDrawer();
      setEditing(channel);
      setForm({
        name: channel.name,
        baseURL: channel.base_url,
        protocol: channel.protocol,
        key: '',
        priority: String(channel.priority),
        maxConcurrency: String(channel.max_concurrency),
        enabled: channel.enabled,
      });
      setFormErrors({});
      setFormError(null);
      setFormOpen(true);
      setActionError(null);
      setNotice(null);
      setConfirmingDeleteID(null);
    },
    [closeKeyDrawer],
  );

  const closeForm = useCallback(() => {
    if (submitting) {
      return;
    }
    setFormOpen(false);
    resetForm();
  }, [resetForm, submitting]);

  useEffect(() => {
    if (!formOpen && keyDrawer === null) {
      return;
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') {
        return;
      }
      if (formOpen) {
        closeForm();
      } else {
        closeKeyDrawer();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [closeForm, closeKeyDrawer, formOpen, keyDrawer]);

  useEffect(() => {
    if (formOpen) {
      formCloseRef.current?.focus();
    }
  }, [formOpen]);

  const keyDrawerOpen = keyDrawer !== null;

  useEffect(() => {
    if (keyDrawerOpen) {
      keyCloseRef.current?.focus();
    }
  }, [keyDrawerOpen]);

  const updateForm = <Key extends keyof FormState>(key: Key, value: FormState[Key]) => {
    setForm((current) => ({ ...current, [key]: value }));
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const errors = validateChannelForm(form, editing !== null);
    setFormErrors(errors);
    if (Object.keys(errors).length > 0) {
      setFormError(null);
      return;
    }

    const config = {
      name: form.name.trim(),
      base_url: form.baseURL.trim(),
      protocol: form.protocol,
      priority: Number(form.priority),
      max_concurrency: Number(form.maxConcurrency),
      enabled: form.enabled,
    };

    setSubmitting(true);
    setFormError(null);
    setActionError(null);
    setNotice(null);
    try {
      if (editing === null) {
        const created = await channelsApi.create({ ...config, key: form.key });
        setChannels((current) => sortChannels([...(current ?? []), created]));
        setNotice(`渠道「${created.name}」已创建。`);
      } else {
        const updated = await channelsApi.update(editing.id, {
          ...config,
          ...(form.key === '' ? {} : { key: form.key }),
        });
        setChannels((current) =>
          (current ?? []).map((item) => (item.id === updated.id ? updated : item)),
        );
        setNotice(`渠道「${updated.name}」已更新。`);
      }
      setFormOpen(false);
      resetForm();
      // 提交成功后静默刷新，保证并发/健康等字段与数据库一致。
      await refreshList();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setFormError(
        describeMutationError(error, editing === null ? '创建渠道失败' : '更新渠道失败'),
      );
    } finally {
      setSubmitting(false);
    }
  };

  const handleToggle = async (channel: Channel) => {
    setBusy({ id: channel.id, kind: 'toggle' });
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
    try {
      const updated = await channelsApi.update(channel.id, { enabled: !channel.enabled });
      setChannels((current) =>
        (current ?? []).map((item) => (item.id === updated.id ? updated : item)),
      );
      setNotice(updated.enabled ? `渠道「${updated.name}」已启用。` : `渠道「${updated.name}」已停用。`);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError(
        describeMutationError(error, channel.enabled ? '停用渠道失败' : '启用渠道失败'),
      );
    } finally {
      setBusy(null);
    }
  };

  const handleTest = async (channel: Channel) => {
    setBusy({ id: channel.id, kind: 'test' });
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
    setTestOutcome(null);
    try {
      const result = await channelsApi.testChannel(channel.id);
      setTestOutcome(result);
      // 测试会写回健康状态；刷新列表让健康列与服务端一致。
      await refreshList();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError(describeTestError(error));
    } finally {
      setBusy(null);
    }
  };

  const handleDelete = async (channel: Channel) => {
    setBusy({ id: channel.id, kind: 'delete' });
    setActionError(null);
    setNotice(null);
    try {
      await channelsApi.remove(channel.id);
      setChannels((current) => (current ?? []).filter((item) => item.id !== channel.id));
      setConfirmingDeleteID(null);
      if (keyDrawer !== null && keyDrawer.channel.id === channel.id) {
        closeKeyDrawer();
      }
      setNotice(`渠道「${channel.name}」已删除。`);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError(describeMutationError(error, '删除渠道失败'));
    } finally {
      setBusy(null);
    }
  };

  if (unauthorized) {
    return (
      <div className="state state--error" role="alert">
        <span className="state-icon" aria-hidden="true">
          !
        </span>
        <h2>登录状态已失效</h2>
        <p>正在跳转登录页，请重新登录后继续操作。</p>
      </div>
    );
  }

  const channelList = channels ?? [];
  const enabledCount = channelList.filter((channel) => channel.enabled).length;

  return (
    <>
      <header className="page-header">
        <div>
          <h2>渠道</h2>
          <p>
            维护上游 base URL、原生协议、优先级与并发上限。列表只显示脱敏 Key；完整值只在点击
            “显示 Key”后从服务端读取，关闭抽屉立即清除。
          </p>
        </div>
        <div className="toolbar">
          <button
            type="button"
            className="button"
            onClick={openCreate}
            disabled={loading}
          >
            新建渠道
          </button>
        </div>
      </header>

      {actionError === null ? null : (
        <p className="form-error" role="alert" style={{ marginBottom: 14 }}>
          {actionError}
        </p>
      )}
      {notice === null ? null : (
        <p className="form-note" role="status" style={{ marginBottom: 14 }}>
          {notice}
        </p>
      )}

      {testOutcome === null ? null : (
        <section className="panel" aria-labelledby="channels-test-result-title" aria-live="polite">
          <div className="panel-header">
            <h2 id="channels-test-result-title">最近一次测试</h2>
            <span className={testOutcome.status === 'healthy' ? 'badge' : 'badge badge--muted'}>
              {testOutcome.status === 'healthy' ? '正常' : '失败'}
            </span>
          </div>
          <div className="panel-body">
            <ul className="detail-list">
              <li>
                <span>渠道</span>
                <code>
                  {testOutcome.channel_name}（#{testOutcome.channel_id}）
                </code>
              </li>
              <li>
                <span>上游模型</span>
                <code>{testOutcome.upstream_model}</code>
              </li>
              <li>
                <span>上游 HTTP</span>
                <code>{testOutcome.http_status === 0 ? '—' : testOutcome.http_status}</code>
              </li>
              <li>
                <span>延迟</span>
                <code>{testOutcome.latency_ms} ms</code>
              </li>
            </ul>
            {testOutcome.error === undefined || testOutcome.error === '' ? null : (
              <p className="form-error" role="alert">
                测试失败：{testOutcome.error}
              </p>
            )}
            <p className="form-note" style={{ marginTop: 10 }}>
              调试结果由服务端脱敏；测试过程不会读取或展示渠道完整 Key。
            </p>
            <p style={{ marginTop: 10 }}>
              <a href={testOutcome.request_link}>查看该渠道最近的请求</a>
            </p>
          </div>
        </section>
      )}

      {loadError !== null ? (
        <section className="panel">
          <div className="state state--error" role="alert">
            <span className="state-icon" aria-hidden="true">
              !
            </span>
            <h2>渠道加载失败</h2>
            <p>{loadError}</p>
            <button
              type="button"
              className="button button--ghost"
              onClick={() => {
                void loadInitial();
              }}
            >
              重试
            </button>
          </div>
        </section>
      ) : channels === null || loading ? (
        <section className="panel">
          <p className="panel-body form-note" role="status">
            正在加载渠道…
          </p>
        </section>
      ) : channelList.length === 0 ? (
        <section className="panel">
          <div className="state state--empty">
            <span className="state-icon" aria-hidden="true">
              ◇
            </span>
            <h3>尚未配置渠道</h3>
            <p>
              添加渠道后这里会显示脱敏 Key；只有点击“显示 Key”才会向服务端请求完整值。
            </p>
            <button type="button" className="button" onClick={openCreate}>
              添加第一个渠道
            </button>
          </div>
        </section>
      ) : (
        <section className="panel" aria-labelledby="channels-list-title">
          <div className="panel-header">
            <h2 id="channels-list-title">渠道列表</h2>
            <div className="toolbar">
              <span className="badge badge--muted">{channelList.length} 个渠道</span>
              <span className="badge badge--muted">{enabledCount} 个启用</span>
            </div>
          </div>
          <div className="table-wrap">
            <table>
              <caption className="sr-only">
                上游渠道列表，Key 列只显示脱敏值；完整 Key 需点击显示
              </caption>
              <thead>
                <tr>
                  <th scope="col">名称</th>
                  <th scope="col">Base URL</th>
                  <th scope="col">协议</th>
                  <th scope="col">优先级</th>
                  <th scope="col">并发</th>
                  <th scope="col">启停</th>
                  <th scope="col">健康状态</th>
                  <th scope="col">Key</th>
                  <th scope="col">操作</th>
                </tr>
              </thead>
              <tbody>
                {channelList.map((channel) => {
                  const busyKind =
                    busy !== null && busy.id === channel.id ? busy.kind : null;
                  const rowBusy = busyKind !== null;
                  const keyOpen = keyDrawer !== null && keyDrawer.channel.id === channel.id;
                  return (
                    <tr key={channel.id}>
                      <td>
                        <strong>{channel.name}</strong>
                      </td>
                      <td>
                        <code style={{ wordBreak: 'break-all' }}>{channel.base_url}</code>
                      </td>
                      <td>
                        <span className="badge badge--muted">
                          {protocolLabel(channel.protocol)}
                        </span>
                      </td>
                      <td>{channel.priority}</td>
                      <td>{channel.max_concurrency}</td>
                      <td>
                        <button
                          type="button"
                          role="switch"
                          aria-checked={channel.enabled}
                          aria-label={`渠道 ${channel.name} 启停`}
                          className="button button--ghost"
                          style={{ minWidth: 74 }}
                          onClick={() => {
                            void handleToggle(channel);
                          }}
                          disabled={rowBusy}
                        >
                          {busyKind === 'toggle' ? '处理中…' : channel.enabled ? '已启用' : '已停用'}
                        </button>
                      </td>
                      <td>
                        <HealthCell channel={channel} />
                      </td>
                      <td>
                        <div
                          style={{
                            display: 'flex',
                            flexDirection: 'column',
                            gap: 6,
                            alignItems: 'flex-start',
                          }}
                        >
                          <code>{channel.key_mask || '—'}</code>
                          <button
                            type="button"
                            className="button button--ghost"
                            aria-label={
                              keyOpen
                                ? `隐藏渠道 ${channel.name} 的完整 Key`
                                : `显示渠道 ${channel.name} 的完整 Key`
                            }
                            onClick={() => {
                              handleKeyButton(channel);
                            }}
                            disabled={rowBusy}
                          >
                            {keyOpen ? '关闭 Key' : '显示 Key'}
                          </button>
                        </div>
                      </td>
                      <td>
                        {confirmingDeleteID === channel.id ? (
                          <div
                            className="toolbar"
                            role="group"
                            aria-label={`确认删除渠道 ${channel.name}`}
                          >
                            <button
                              type="button"
                              className="button"
                              aria-label={`确认删除渠道 ${channel.name}`}
                              onClick={() => {
                                void handleDelete(channel);
                              }}
                              disabled={rowBusy}
                            >
                              {busyKind === 'delete' ? '正在删除…' : '确认删除'}
                            </button>
                            <button
                              type="button"
                              className="button button--ghost"
                              aria-label={`取消删除渠道 ${channel.name}`}
                              onClick={() => {
                                setConfirmingDeleteID(null);
                              }}
                              disabled={rowBusy}
                            >
                              取消
                            </button>
                          </div>
                        ) : (
                          <div
                            className="toolbar"
                            role="group"
                            aria-label={`渠道 ${channel.name} 的操作`}
                          >
                            <button
                              type="button"
                              className="button button--ghost"
                              aria-label={`编辑渠道 ${channel.name}`}
                              onClick={() => {
                                openEdit(channel);
                              }}
                              disabled={rowBusy}
                            >
                              编辑
                            </button>
                            <button
                              type="button"
                              className="button button--ghost"
                              aria-label={`测试渠道 ${channel.name}`}
                              onClick={() => {
                                void handleTest(channel);
                              }}
                              disabled={rowBusy}
                            >
                              {busyKind === 'test' ? '测试中…' : '测试'}
                            </button>
                            <button
                              type="button"
                              className="button button--ghost"
                              aria-label={`删除渠道 ${channel.name}`}
                              onClick={() => {
                                setConfirmingDeleteID(channel.id);
                                setActionError(null);
                                setNotice(null);
                              }}
                              disabled={rowBusy}
                            >
                              删除
                            </button>
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}

      {formOpen ? (
        <div
          style={OVERLAY_STYLE}
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              closeForm();
            }
          }}
        >
          <section
            role="dialog"
            aria-modal="true"
            aria-labelledby="channel-form-title"
            style={DRAWER_STYLE}
            onMouseDown={(event) => {
              event.stopPropagation();
            }}
          >
            <header style={DRAWER_HEADER_STYLE}>
              <div>
                <p className="eyebrow">{editing === null ? '新建渠道' : '编辑渠道'}</p>
                <h2 id="channel-form-title" style={{ margin: '4px 0 0', fontSize: 20 }}>
                  {editing === null ? '新建渠道' : `编辑渠道：${editing.name}`}
                </h2>
              </div>
              <button
                ref={formCloseRef}
                type="button"
                className="button button--ghost"
                aria-label="关闭渠道表单"
                onClick={closeForm}
                disabled={submitting}
              >
                关闭
              </button>
            </header>

            <div style={DRAWER_BODY_STYLE}>
              <p className="form-note">
                上游 Key 只在提交时接收明文，随后立即用 AES-256-GCM 加密；列表始终只显示脱敏值。
              </p>

              <form
                onSubmit={(event) => {
                  void handleSubmit(event);
                }}
                noValidate
                style={FORM_GRID_STYLE}
              >
                <div className="field" style={{ gridColumn: '1 / -1' }}>
                  <label htmlFor="channel-name">渠道名称</label>
                  <input
                    id="channel-name"
                    className="input"
                    type="text"
                    name="name"
                    autoComplete="off"
                    placeholder="例如 codex-primary"
                    value={form.name}
                    onChange={(event) => {
                      updateForm('name', event.target.value);
                    }}
                    aria-invalid={formErrors.name !== undefined}
                    aria-describedby={formErrors.name === undefined ? undefined : 'channel-name-error'}
                  />
                  {formErrors.name === undefined ? null : (
                    <p className="form-error" id="channel-name-error">
                      {formErrors.name}
                    </p>
                  )}
                </div>

                <div className="field" style={{ gridColumn: '1 / -1' }}>
                  <label htmlFor="channel-base-url">Base URL</label>
                  <input
                    id="channel-base-url"
                    className="input"
                    type="text"
                    name="base-url"
                    autoComplete="off"
                    placeholder="https://upstream.example.com/v1"
                    value={form.baseURL}
                    onChange={(event) => {
                      updateForm('baseURL', event.target.value);
                    }}
                    aria-invalid={formErrors.baseURL !== undefined}
                    aria-describedby={
                      formErrors.baseURL === undefined ? undefined : 'channel-base-url-error'
                    }
                  />
                  {formErrors.baseURL === undefined ? null : (
                    <p className="form-error" id="channel-base-url-error">
                      {formErrors.baseURL}
                    </p>
                  )}
                  <p className="form-note">只允许 http/https，不能包含用户信息或 fragment。</p>
                </div>

                <div className="field">
                  <label htmlFor="channel-protocol">协议</label>
                  <select
                    id="channel-protocol"
                    className="input"
                    name="protocol"
                    value={form.protocol}
                    onChange={(event) => {
                      updateForm('protocol', event.target.value);
                    }}
                    aria-invalid={formErrors.protocol !== undefined}
                    aria-describedby={
                      formErrors.protocol === undefined ? undefined : 'channel-protocol-error'
                    }
                  >
                    {PROTOCOL_OPTIONS.map((option) => (
                      <option
                        key={option.value}
                        value={option.value}
                        disabled={
                          option.value === 'anthropic_messages' &&
                          form.protocol !== 'anthropic_messages'
                        }
                      >
                        {option.label}
                      </option>
                    ))}
                    {PROTOCOL_OPTIONS.some((option) => option.value === form.protocol) ? null : (
                      <option value={form.protocol}>{form.protocol}（未知协议）</option>
                    )}
                  </select>
                  {formErrors.protocol === undefined ? null : (
                    <p className="form-error" id="channel-protocol-error">
                      {formErrors.protocol}
                    </p>
                  )}
                  <p className="form-note">渠道协议必须与入站请求协议一致，不做隐式转换。</p>
                </div>

                <div className="field" style={{ gridColumn: '1 / -1' }}>
                  <label htmlFor="channel-key">
                    {editing === null ? '上游 Key' : '上游 Key（留空保持不变）'}
                  </label>
                  <input
                    id="channel-key"
                    className="input"
                    type="password"
                    name="key"
                    autoComplete="new-password"
                    placeholder={editing === null ? 'sk-...' : '留空则保留现有 Key'}
                    value={form.key}
                    onChange={(event) => {
                      updateForm('key', event.target.value);
                    }}
                    aria-invalid={formErrors.key !== undefined}
                    aria-describedby={formErrors.key === undefined ? undefined : 'channel-key-error'}
                  />
                  {formErrors.key === undefined ? null : (
                    <p className="form-error" id="channel-key-error">
                      {formErrors.key}
                    </p>
                  )}
                  {editing === null ? null : (
                    <p className="form-note">
                      当前脱敏值：<code>{editing.key_mask || '—'}</code>；留空不会修改 Key。
                    </p>
                  )}
                </div>

                <div className="field">
                  <label htmlFor="channel-priority">优先级</label>
                  <input
                    id="channel-priority"
                    className="input"
                    type="number"
                    name="priority"
                    min={PRIORITY_MIN}
                    max={PRIORITY_MAX}
                    step={1}
                    value={form.priority}
                    onChange={(event) => {
                      updateForm('priority', event.target.value);
                    }}
                    aria-invalid={formErrors.priority !== undefined}
                    aria-describedby={
                      formErrors.priority === undefined ? undefined : 'channel-priority-error'
                    }
                  />
                  {formErrors.priority === undefined ? null : (
                    <p className="form-error" id="channel-priority-error">
                      {formErrors.priority}
                    </p>
                  )}
                  <p className="form-note">数值越小越优先（{PRIORITY_MIN}–{PRIORITY_MAX}）。</p>
                </div>

                <div className="field">
                  <label htmlFor="channel-max-concurrency">并发上限</label>
                  <input
                    id="channel-max-concurrency"
                    className="input"
                    type="number"
                    name="max-concurrency"
                    min={CONCURRENCY_MIN}
                    max={CONCURRENCY_MAX}
                    step={1}
                    value={form.maxConcurrency}
                    onChange={(event) => {
                      updateForm('maxConcurrency', event.target.value);
                    }}
                    aria-invalid={formErrors.maxConcurrency !== undefined}
                    aria-describedby={
                      formErrors.maxConcurrency === undefined
                        ? undefined
                        : 'channel-max-concurrency-error'
                    }
                  />
                  {formErrors.maxConcurrency === undefined ? null : (
                    <p className="form-error" id="channel-max-concurrency-error">
                      {formErrors.maxConcurrency}
                    </p>
                  )}
                  <p className="form-note">
                    进程内每渠道并发槽位（{CONCURRENCY_MIN}–{CONCURRENCY_MAX}）。
                  </p>
                </div>

                <label htmlFor="channel-enabled" style={{ ...CHECKBOX_STYLE, gridColumn: '1 / -1' }}>
                  <input
                    id="channel-enabled"
                    type="checkbox"
                    name="enabled"
                    checked={form.enabled}
                    onChange={(event) => {
                      updateForm('enabled', event.target.checked);
                    }}
                  />
                  启用此渠道
                </label>

                {formError === null ? null : (
                  <p className="form-error" role="alert" style={{ gridColumn: '1 / -1' }}>
                    {formError}
                  </p>
                )}

                <div className="toolbar" style={{ gridColumn: '1 / -1' }}>
                  <button type="submit" className="button" disabled={submitting}>
                    {editing === null
                      ? submitting
                        ? '正在创建…'
                        : '创建渠道'
                      : submitting
                        ? '正在保存…'
                        : '保存修改'}
                  </button>
                  <button
                    type="button"
                    className="button button--ghost"
                    onClick={closeForm}
                    disabled={submitting}
                  >
                    取消
                  </button>
                </div>
              </form>
            </div>
          </section>
        </div>
      ) : null}

      {keyDrawer === null ? null : (
        <div
          style={OVERLAY_STYLE}
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              closeKeyDrawer();
            }
          }}
        >
          <section
            role="dialog"
            aria-modal="true"
            aria-labelledby="channel-key-title"
            style={DRAWER_STYLE}
            onMouseDown={(event) => {
              event.stopPropagation();
            }}
          >
            <header style={DRAWER_HEADER_STYLE}>
              <div>
                <p className="eyebrow">上游凭据</p>
                <h2 id="channel-key-title" style={{ margin: '4px 0 0', fontSize: 20 }}>
                  渠道 Key
                </h2>
                <p className="form-note" style={{ margin: '6px 0 0' }}>
                  {keyDrawer.channel.name} ·{' '}
                  <code>
                    {keyDrawer.status === 'ready'
                      ? keyDrawer.mask
                      : keyDrawer.channel.key_mask || '—'}
                  </code>
                </p>
              </div>
              <button
                ref={keyCloseRef}
                type="button"
                className="button button--ghost"
                aria-label="关闭 Key 面板"
                onClick={closeKeyDrawer}
              >
                关闭
              </button>
            </header>

            <div style={DRAWER_BODY_STYLE}>
              {keyDrawer.status === 'loading' ? (
                <p className="form-note" role="status">
                  正在读取完整 Key…
                </p>
              ) : null}

              {keyDrawer.status === 'error' ? (
                <div className="state state--error" role="alert">
                  <span className="state-icon" aria-hidden="true">
                    !
                  </span>
                  <h3>无法显示完整 Key</h3>
                  <p>{keyDrawer.message}</p>
                  <button
                    type="button"
                    className="button button--ghost"
                    onClick={() => {
                      void openKeyDrawer(keyDrawer.channel);
                    }}
                  >
                    重试
                  </button>
                </div>
              ) : null}

              {keyDrawer.status === 'ready' ? (
                <>
                  <div className="field">
                    <label htmlFor="channel-revealed-key">完整 Key（只读）</label>
                    <input
                      id="channel-revealed-key"
                      className="input"
                      type="text"
                      readOnly
                      autoComplete="off"
                      spellCheck={false}
                      value={keyDrawer.key}
                      aria-label="完整 Key"
                      style={{ fontFamily: 'var(--font-mono, ui-monospace, SFMono-Regular, monospace)' }}
                      onFocus={(event) => {
                        event.currentTarget.select();
                      }}
                    />
                  </div>

                  <div className="toolbar" style={{ marginTop: 12 }}>
                    <button
                      type="button"
                      className="button"
                      aria-label={`复制渠道 ${keyDrawer.channel.name} 的 Key`}
                      onClick={() => {
                        void handleCopyKey(keyDrawer.key);
                      }}
                    >
                      {copied ? '已复制' : '复制 Key'}
                    </button>
                  </div>

                  {copyError === null ? null : (
                    <p className="form-error" role="alert" style={{ marginTop: 10 }}>
                      {copyError}
                    </p>
                  )}
                  {copied ? (
                    <p className="form-note" role="status" style={{ marginTop: 10 }}>
                      已复制完整 Key。
                    </p>
                  ) : null}

                  <p className="form-note" style={{ marginTop: 16 }}>
                    完整 Key 只保存在当前页面内存中，关闭后立即清除；响应带 no-store，不写入请求日志。
                    请勿在共享屏幕上停留。
                  </p>
                </>
              ) : null}
            </div>
          </section>
        </div>
      )}
    </>
  );
}

export default ChannelsPage;
