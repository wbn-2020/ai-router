import {
  Fragment,
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';

import { ApiError, isUnauthorizedError } from '../../lib/api';
import { ChannelEditor } from './ChannelEditor';
import { channelsApi } from './api';
import { ChannelTestResultView, checkStatusLabel } from './ChannelTestResultView';
import {
  channelHost,
  describeChannelMutationError,
  describeChannelTestError,
  formatChannelTimestamp,
  keepChannelDialogFocus,
} from './channelForm';
import { protocolLabel } from './providerCatalog';
import type { Channel, ChannelTestResult } from './types';

function sortChannels(channels: Channel[]): Channel[] {
  return [...channels].sort(
    (left, right) => left.priority - right.priority || left.id - right.id,
  );
}

function HealthCell({ channel }: { channel: Channel }) {
  const status = channel.last_health_status;
  const testedAt = formatChannelTimestamp(channel.last_health_at);
  const versionKnown = !!channel.connection_revision && !!channel.last_health_revision;
  const stale = !!channel.connection_revision && typeof channel.last_health_revision === 'number' && channel.connection_revision !== channel.last_health_revision;
  if (!status) {
    return <span className="muted">未检查</span>;
  }
  return (
    <div className="health-cell">
      <span className={status === 'healthy' && versionKnown && !stale ? 'badge' : 'badge badge--muted'}>
        本次{checkStatusLabel(status)}
      </span>
      {stale ? <span className="badge badge--muted">已过期</span> : null}
      <span className="form-note">{channel.last_health_kind === 'generation' ? '有效生成' : '检查类型未知'}</span>
      <code>{channel.last_health_model || '模型未知'}</code>
      <span className="form-note">{channel.last_health_protocol || '协议未知'}</span>
      <span className="form-note">检查 {channel.last_health_revision ? `v${channel.last_health_revision}` : '版本未知'} · 当前 {channel.connection_revision ? `v${channel.connection_revision}` : '版本未知'}</span>
      <span className="form-note">头 {channel.last_health_latency_ms == null ? '未知' : `${channel.last_health_latency_ms} ms`} · 总 {channel.last_health_duration_ms == null ? '未知' : `${channel.last_health_duration_ms} ms`}</span>
      <time className="form-note" dateTime={channel.last_health_at}>{testedAt === '—' ? '检查时间未知' : testedAt}</time>
      {!channel.last_error ? null : (
        <span className="health-cell__error" title={channel.last_error}>{channel.last_error}</span>
      )}
    </div>
  );
}

function RuntimeCell({ channel }: { channel: Channel }) {
  const runtime = channel.runtime;
  const label = runtime?.state === 'ready' ? '就绪' : runtime?.state === 'cooldown' ? '冷却中' : runtime?.state === 'recovering' ? '待恢复 / 恢复中' : '运行状态未知';
  return (
    <div className="health-cell">
      <span className={runtime?.state === 'ready' ? 'badge' : 'badge badge--muted'}>{label}</span>
      {runtime === undefined || runtime.state === 'unknown' ? null : <>
        <span className="form-note">占用 {runtime.in_use} / {runtime.limit}</span>
        <span className="form-note">模型冷却 {runtime.model_cooldowns} · 待恢复 {runtime.model_recovering}</span>
        {runtime.open_until ? <time className="form-note" dateTime={runtime.open_until}>冷却至 {formatChannelTimestamp(runtime.open_until)}</time> : null}
        {runtime.last_error ? <span className="health-cell__error">{runtime.last_error}</span> : null}
      </>}
    </div>
  );
}

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

const OVERLAY_STYLE: CSSProperties = {
  position: 'fixed',
  inset: 0,
  zIndex: 50,
  display: 'flex',
  justifyContent: 'flex-end',
  background: 'var(--color-overlay)',
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

type KeyDrawerState =
  | { channel: Channel; status: 'loading' }
  | { channel: Channel; status: 'ready'; key: string; mask: string }
  | { channel: Channel; status: 'error'; message: string };

type BusyKind = 'toggle' | 'test' | 'delete';

type EditorState = {
  channel: Channel | null;
  section: 'connection' | 'models';
};

export function ChannelsPage(): ReactNode {
  const [channels, setChannels] = useState<Channel[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);

  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<{ id: number; kind: BusyKind } | null>(null);
  const [confirmingDeleteID, setConfirmingDeleteID] = useState<number | null>(null);
  const [testOutcomes, setTestOutcomes] = useState<Record<number, ChannelTestResult>>({});
  const [testErrors, setTestErrors] = useState<Record<number, string>>({});
  const busyRef = useRef(false);
  const createRef = useRef<HTMLButtonElement>(null);

  const [keyDrawer, setKeyDrawer] = useState<KeyDrawerState | null>(null);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);
  const revealSequence = useRef(0);
  const keyCloseRef = useRef<HTMLButtonElement>(null);
  const keyDialogRef = useRef<HTMLElement>(null);
  const keyOpenerRef = useRef<HTMLElement | null>(null);
  const keyOpenRef = useRef(false);
  const copyPendingRef = useRef(false);
  const [copyPending, setCopyPending] = useState(false);

  const [editor, setEditor] = useState<EditorState | null>(null);

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
    revealSequence.current += 1;
    keyOpenRef.current = false;
    copyPendingRef.current = false;
    setKeyDrawer(null);
    setCopied(false);
    setCopyError(null);
    setCopyPending(false);
    if (keyOpenerRef.current?.isConnected) {
      keyOpenerRef.current.focus();
    }
    keyOpenerRef.current = null;
  }, []);

  const openKeyDrawer = useCallback(async (channel: Channel) => {
    if (!keyOpenRef.current) {
      keyOpenerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      keyOpenRef.current = true;
    }
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
    if (copyPendingRef.current) {
      return;
    }
    const sequence = revealSequence.current;
    copyPendingRef.current = true;
    setCopyPending(true);
    try {
      await copyToClipboard(key);
      if (sequence === revealSequence.current) {
        setCopied(true);
        setCopyError(null);
      }
    } catch {
      if (sequence === revealSequence.current) {
        setCopied(false);
        setCopyError('复制失败，请手动选择输入框中的 Key 后复制。');
      }
    } finally {
      if (sequence === revealSequence.current) {
        copyPendingRef.current = false;
        setCopyPending(false);
      }
    }
  }, []);

  const openCreate = useCallback(() => {
    closeKeyDrawer();
    setEditor({ channel: null, section: 'connection' });
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
  }, [closeKeyDrawer]);

  const openEdit = useCallback(
    (channel: Channel, section: 'connection' | 'models' = 'connection') => {
      closeKeyDrawer();
      setEditor({ channel, section });
      setActionError(null);
      setNotice(null);
      setConfirmingDeleteID(null);
    },
    [closeKeyDrawer],
  );

  const keyDrawerOpen = keyDrawer !== null;
  useEffect(() => {
    if (!keyDrawerOpen) {
      return;
    }
    keyCloseRef.current?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeKeyDrawer();
      }
      keepChannelDialogFocus(event, keyDialogRef.current);
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [closeKeyDrawer, keyDrawerOpen]);

  const closeEditor = useCallback(() => {
    setEditor(null);
    void refreshList();
  }, [refreshList]);

  const handleUnauthorized = useCallback(() => {
    setEditor(null);
    setKeyDrawer(null);
    setUnauthorized(true);
  }, []);

  const handleEditorSaved = useCallback((saved: Channel, message: string) => {
    setChannels((current) => {
      const existing = current ?? [];
      return sortChannels(existing.some((item) => item.id === saved.id)
        ? existing.map((item) => item.id === saved.id ? saved : item)
        : [...existing, saved]);
    });
    setTestOutcomes((current) => {
      const next = { ...current };
      delete next[saved.id];
      return next;
    });
    setTestErrors((current) => {
      const next = { ...current };
      delete next[saved.id];
      return next;
    });
    setNotice(message);
    if (editor?.channel === null) {
      createRef.current?.focus();
      setEditor(null);
    }
  }, [editor]);

  const handleToggle = async (channel: Channel) => {
    if (busyRef.current) {
      return;
    }
    busyRef.current = true;
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
        describeChannelMutationError(error, channel.enabled ? '停用渠道失败' : '启用渠道失败'),
      );
    } finally {
      busyRef.current = false;
      setBusy(null);
    }
  };

  const handleTest = async (channel: Channel) => {
    if (busyRef.current) {
      return;
    }
    busyRef.current = true;
    setBusy({ id: channel.id, kind: 'test' });
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
    setTestOutcomes((current) => {
      const next = { ...current };
      delete next[channel.id];
      return next;
    });
    setTestErrors((current) => {
      const next = { ...current };
      delete next[channel.id];
      return next;
    });
    try {
      const result = await channelsApi.testChannel(channel.id);
      setTestOutcomes((current) => ({ ...current, [channel.id]: result }));
      await refreshList();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setTestErrors((current) => ({ ...current, [channel.id]: describeChannelTestError(error) }));
    } finally {
      busyRef.current = false;
      setBusy(null);
    }
  };

  const handleDelete = async (channel: Channel) => {
    if (busyRef.current) {
      return;
    }
    busyRef.current = true;
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
      createRef.current?.focus();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError(describeChannelMutationError(error, '删除渠道失败'));
    } finally {
      busyRef.current = false;
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
            添加渠道时选择供应商、填入 Key 并勾选模型，保存后自动生成模型路由。优先级与并发收在高级设置里。
          </p>
        </div>
        <div className="toolbar">
          <button
            ref={createRef}
            type="button"
            className="button"
            onClick={openCreate}
            disabled={loading || busy !== null}
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
            <p>添加渠道时可以直接从上游拉取模型，保存后自动生成路由。</p>
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
          <p className="panel-body form-note" id="channels-test-note">
            最近检查仅代表单次模型、协议和连接版本的有效生成，不代表全部模型或全部能力，也不等同当前运行状态；测试会消耗少量上游 token。
          </p>
          <div className="table-wrap">
            <table>
              <caption className="sr-only">
                上游渠道列表；Key 与删除操作在更多中，完整 Key 需明确点击显示。
              </caption>
              <thead>
                <tr>
                  <th scope="col">渠道</th>
                  <th scope="col">模型</th>
                  <th scope="col">最近检查</th>
                  <th scope="col">运行状态</th>
                  <th scope="col">启停</th>
                  <th scope="col">操作</th>
                </tr>
              </thead>
              <tbody>
                {channelList.map((channel) => {
                  const busyKind =
                    busy !== null && busy.id === channel.id ? busy.kind : null;
                  const rowBusy = busy !== null;
                  const keyOpen = keyDrawer !== null && keyDrawer.channel.id === channel.id;
                  const testOutcome = testOutcomes[channel.id];
                  const testError = testErrors[channel.id];
                  return (
                    <Fragment key={channel.id}>
                    <tr>
                      <td>
                        <div className="channel-cell">
                          <strong>{channel.name}</strong>
                          <span className="channel-cell__meta">
                            <code>{channelHost(channel.base_url)}</code>
                            <span className="badge badge--muted">{protocolLabel(channel.protocol)}</span>
                          </span>
                        </div>
                      </td>
                      <td>
                        <button
                          type="button"
                          className="button button--ghost"
                          aria-label={`管理渠道 ${channel.name} 的模型`}
                          disabled={rowBusy}
                          onClick={() => {
                            openEdit(channel, 'models');
                          }}
                        >
                          {channel.model_count ?? 0} 个模型
                        </button>
                      </td>
                      <td>
                        <HealthCell channel={channel} />
                      </td>
                      <td><RuntimeCell channel={channel} /></td>
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
                        <div className="channel-actions" role="group" aria-label={`渠道 ${channel.name} 的操作`}>
                          <button type="button" className="button button--ghost" aria-label={`编辑渠道 ${channel.name}`} disabled={rowBusy} onClick={() => openEdit(channel)}>编辑</button>
                          <button type="button" className="button button--ghost" aria-label={`测试渠道 ${channel.name}`} aria-describedby="channels-test-note" disabled={rowBusy} onClick={() => void handleTest(channel)}>
                            {busyKind === 'test' ? '测试中…' : '测试'}
                          </button>
                          <details className="channel-more" onToggle={(event) => {
                            if (!event.currentTarget.open) {
                              setConfirmingDeleteID((current) => current === channel.id ? null : current);
                            }
                          }}>
                            <summary aria-label={`渠道 ${channel.name} 的更多操作`}>更多</summary>
                            <div className="channel-more__body">
                              <span className="channel-cell__key"><span>Key </span><code>{channel.key_mask || '—'}</code></span>
                              <button
                                type="button" className="button button--ghost button--small"
                                aria-label={keyOpen ? `隐藏渠道 ${channel.name} 的完整 Key` : `显示渠道 ${channel.name} 的完整 Key`}
                                onClick={() => handleKeyButton(channel)} disabled={rowBusy}
                              >
                                {keyOpen ? '关闭 Key' : '显示 Key'}
                              </button>
                              {confirmingDeleteID === channel.id ? (
                                <div role="group" aria-label={`确认删除渠道 ${channel.name}`}>
                                  <p className="form-note">删除渠道「{channel.name}」及其模型映射？此操作不可撤销。</p>
                                  <div className="toolbar">
                                    <button type="button" className="button" aria-label={`确认删除渠道 ${channel.name}`} disabled={rowBusy} onClick={() => void handleDelete(channel)}>{busyKind === 'delete' ? '正在删除…' : '确认删除'}</button>
                                    <button type="button" className="button button--ghost" aria-label={`取消删除渠道 ${channel.name}`} disabled={rowBusy} onClick={() => setConfirmingDeleteID(null)}>取消</button>
                                  </div>
                                </div>
                              ) : (
                                <button type="button" className="button button--ghost" aria-label={`删除渠道 ${channel.name}`} disabled={rowBusy} onClick={() => { setConfirmingDeleteID(channel.id); setActionError(null); setNotice(null); }}>删除</button>
                              )}
                            </div>
                          </details>
                        </div>
                      </td>
                    </tr>
                    {testOutcome === undefined && testError === undefined ? null : (
                      <tr className="channel-test-row">
                        <td colSpan={6}>
                          {testError === undefined ? null : <p className="form-error model-check__error" role="alert">{testError}</p>}
                          {testOutcome === undefined ? null : <ChannelTestResultView result={testOutcome} label={`渠道 ${channel.name} 的测试结果`} />}
                        </td>
                      </tr>
                    )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}

      {editor === null ? null : (
        <ChannelEditor
          channel={editor.channel}
          initialSection={editor.section}
          onClose={closeEditor}
          onSaved={handleEditorSaved}
          onUnauthorized={handleUnauthorized}
        />
      )}

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
            ref={keyDialogRef}
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
                      disabled={copyPending}
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
