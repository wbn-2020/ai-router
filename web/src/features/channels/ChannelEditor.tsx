import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react';

import { isUnauthorizedError } from '../../lib/api';
import { channelsApi } from './api';
import { ChannelModelPicker } from './ChannelModelPicker';
import { ChannelModelsTab } from './ChannelModelsTab';
import {
  CONCURRENCY_MAX,
  CONCURRENCY_MIN,
  EMPTY_CHANNEL_FORM,
  PRIORITY_MAX,
  PRIORITY_MIN,
  PROTOCOL_OPTIONS,
  channelHost,
  describeChannelMutationError,
  keepChannelDialogFocus,
  validateBaseURL,
  validateChannelForm,
  type ChannelFormErrors,
  type ChannelFormState,
} from './channelForm';
import {
  PROVIDER_PRESETS,
  deriveChannelName,
  findProviderPreset,
  protocolLabel,
} from './providerCatalog';
import type { Channel } from './types';

interface ChannelEditorProps {
  /** null 表示新建。 */
  channel: Channel | null;
  initialSection?: 'connection' | 'models';
  onClose: () => void;
  onSaved: (channel: Channel, notice: string) => void;
  onUnauthorized: () => void;
}

function initialForm(channel: Channel | null): ChannelFormState {
  if (channel === null) {
    return {
      ...EMPTY_CHANNEL_FORM,
      baseURL: findProviderPreset(EMPTY_CHANNEL_FORM.providerID).defaultBaseURL,
    };
  }
  return {
    providerID: channel.protocol === 'openai_chat' ? 'openai-chat' : 'openai-responses',
    name: channel.name,
    baseURL: channel.base_url,
    protocol: channel.protocol,
    key: '',
    priority: String(channel.priority),
    maxConcurrency: String(channel.max_concurrency),
    enabled: channel.enabled,
    models: [],
  };
}

/** requestPreview 展示 Base URL 实际拼接出的请求路径。 */
function requestPreview(baseURL: string, protocol: string): string {
  const trimmed = baseURL.trim();
  if (trimmed === '' || validateBaseURL(trimmed) !== null) {
    return '';
  }
  const path =
    protocol === 'openai_chat'
      ? '/chat/completions'
      : protocol === 'anthropic_messages'
        ? '/messages'
        : '/responses';
  const parsed = new URL(trimmed);
  parsed.pathname = `${parsed.pathname.replace(/\/$/, '')}${path}`;
  return parsed.toString();
}

export function ChannelEditor({
  channel,
  initialSection = 'connection',
  onClose,
  onSaved,
  onUnauthorized,
}: ChannelEditorProps): ReactNode {
  const editing = channel !== null;
  const [savedChannel, setSavedChannel] = useState(channel);
  const [form, setForm] = useState<ChannelFormState>(() => initialForm(channel));
  const [errors, setErrors] = useState<ChannelFormErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saveNotice, setSaveNotice] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [previewPending, setPreviewPending] = useState(false);
  const [modelsPending, setModelsPending] = useState(false);
  const [generation, setGeneration] = useState(0);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [modelCount, setModelCount] = useState(channel?.model_count ?? 0);
  const nameTouched = useRef(editing);
  const closeRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLElement>(null);
  const modelsRef = useRef<HTMLElement>(null);
  const pendingRef = useRef({ submitting: false, preview: false, models: false });
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  const pending = submitting || previewPending || modelsPending;
  const connectionDirty = savedChannel !== null && (
    form.name.trim() !== savedChannel.name ||
    form.baseURL.trim() !== savedChannel.base_url ||
    form.protocol !== savedChannel.protocol ||
    form.key !== '' ||
    form.priority !== String(savedChannel.priority) ||
    form.maxConcurrency !== String(savedChannel.max_concurrency) ||
    form.enabled !== savedChannel.enabled
  );

  const close = () => {
    if (Object.values(pendingRef.current).some(Boolean)) {
      return;
    }
    onCloseRef.current();
  };

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (initialSection === 'models' && modelsRef.current !== null) {
      modelsRef.current.scrollIntoView?.({ block: 'start' });
      modelsRef.current.focus();
    } else {
      closeRef.current?.focus();
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        if (!Object.values(pendingRef.current).some(Boolean)) {
          onCloseRef.current();
        }
      }
      keepChannelDialogFocus(event, dialogRef.current);
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (opener?.isConnected) {
        opener.focus();
      }
    };
  }, [initialSection]);

  // 新建时根据 Base URL 自动生成名称；用户手动改过名称后不再覆盖。
  useEffect(() => {
    if (editing || nameTouched.current) {
      return;
    }
    const derived = deriveChannelName(form.baseURL, form.protocol);
    setForm((current) => current.name === derived ? current : { ...current, name: derived });
  }, [editing, form.baseURL, form.protocol]);

  const updateForm = <Key extends keyof ChannelFormState>(key: Key, value: ChannelFormState[Key]) => {
    if (form[key] === value) {
      return;
    }
    if (key === 'baseURL' || key === 'key' || key === 'protocol') {
      setGeneration((current) => current + 1);
    }
    setSaveNotice(null);
    setForm((current) => ({ ...current, [key]: value }));
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (Object.values(pendingRef.current).some(Boolean) || (editing && !connectionDirty)) {
      return;
    }
    const nextErrors = validateChannelForm(form, editing);
    setErrors(nextErrors);
    if (nextErrors.priority !== undefined || nextErrors.maxConcurrency !== undefined || nextErrors.protocol !== undefined) {
      setAdvancedOpen(true);
    }
    if (Object.keys(nextErrors).length > 0) {
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
    pendingRef.current.submitting = true;
    setSubmitting(true);
    setFormError(null);
    setSaveNotice(null);
    try {
      if (savedChannel === null) {
        const created = await channelsApi.create({ ...config, key: form.key, models: form.models });
        onSaved(
          created,
          form.models.length > 0
            ? `渠道「${created.name}」已创建，并添加 ${form.models.length} 个模型。`
            : `渠道「${created.name}」已创建。`,
        );
        return;
      }
      const updated = await channelsApi.update(savedChannel.id, {
        ...config,
        ...(form.key === '' ? {} : { key: form.key }),
      });
      setSavedChannel(updated);
      setForm(initialForm(updated));
      setGeneration((current) => current + 1);
      setSaveNotice('连接配置已保存，现在可获取或测试模型。');
      onSaved(updated, `渠道「${updated.name}」已更新。`);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setFormError(describeChannelMutationError(error, editing ? '更新渠道失败' : '创建渠道失败'));
    } finally {
      pendingRef.current.submitting = false;
      setSubmitting(false);
    }
  };

  const handlePreviewPending = useCallback((value: boolean) => {
    pendingRef.current.preview = value;
    setPreviewPending(value);
  }, []);
  const handleModelsPending = useCallback((value: boolean) => {
    pendingRef.current.models = value;
    setModelsPending(value);
  }, []);
  const provider = findProviderPreset(form.providerID);
  const canDiscover = provider.supportsModelDiscovery &&
    form.key.trim() !== '' && validateBaseURL(form.baseURL) === null &&
    form.protocol !== 'anthropic_messages';

  return (
    <div
      className="editor-overlay"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          close();
        }
      }}
    >
      <section
        ref={dialogRef}
        className="editor-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="channel-editor-title"
        aria-busy={pending}
      >
        <header className="editor-header">
          <div>
            <p className="eyebrow">{editing ? '编辑渠道' : '新建渠道'}</p>
            <h2 id="channel-editor-title">{editing ? `编辑渠道：${savedChannel?.name}` : '新建渠道'}</h2>
            <p className="form-note">
              {savedChannel === null
                ? '连接与模型一次填写，创建时一起保存。'
                : `${channelHost(savedChannel.base_url)} · ${modelCount} 个模型`}
            </p>
          </div>
          <button ref={closeRef} type="button" className="button button--ghost" aria-label="关闭渠道表单" onClick={close} disabled={pending}>
            关闭
          </button>
        </header>

        <div className="editor-body">
          <form id="channel-connection-form" className="editor-form" onSubmit={(event) => void handleSubmit(event)} noValidate>
            {!editing ? (
              <div className="field editor-preset">
                <label htmlFor="channel-provider">供应商预设</label>
                <select
                  id="channel-provider"
                  className="input"
                  value={form.providerID}
                  disabled={submitting}
                  onChange={(event) => {
                    const preset = findProviderPreset(event.target.value);
                    if (preset.disabled) {
                      return;
                    }
                    setForm((current) => ({ ...current, providerID: preset.id, protocol: preset.protocol, baseURL: preset.defaultBaseURL }));
                    setGeneration((current) => current + 1);
                    setErrors({});
                    setFormError(null);
                  }}
                >
                  {PROVIDER_PRESETS.map((preset) => (
                    <option key={preset.id} value={preset.id} disabled={preset.disabled}>{preset.name}{preset.disabled ? '（预留）' : ''}</option>
                  ))}
                </select>
                <p className="form-note">{provider.description}</p>
              </div>
            ) : null}

            <section className="editor-section" aria-labelledby="channel-connection-title">
              <div className="editor-section__header">
                <h3 id="channel-connection-title">连接配置</h3>
                <span className="badge badge--muted">{protocolLabel(form.protocol)}</span>
              </div>
              <div className="editor-connection-grid">
                <div className="field">
                  <label htmlFor="channel-name">渠道名称</label>
                  <input
                    id="channel-name" className="input" type="text" name="name" autoComplete="off"
                    placeholder="根据 Base URL 自动生成" value={form.name} disabled={submitting}
                    onChange={(event) => { nameTouched.current = true; updateForm('name', event.target.value); }}
                    aria-invalid={errors.name !== undefined}
                    aria-describedby={errors.name === undefined ? undefined : 'channel-name-error'}
                  />
                  {errors.name === undefined ? null : <p className="form-error" id="channel-name-error">{errors.name}</p>}
                </div>
                <div className="field">
                  <label htmlFor="channel-base-url">Base URL</label>
                  <input
                    id="channel-base-url" className="input" type="text" name="base-url" autoComplete="off"
                    placeholder="https://upstream.example.com/v1" value={form.baseURL} disabled={submitting}
                    onChange={(event) => updateForm('baseURL', event.target.value)}
                    aria-invalid={errors.baseURL !== undefined}
                    aria-describedby={errors.baseURL === undefined ? undefined : 'channel-base-url-error'}
                  />
                  {errors.baseURL === undefined ? null : <p className="form-error" id="channel-base-url-error">{errors.baseURL}</p>}
                  <p className="form-note">{requestPreview(form.baseURL, form.protocol) === '' ? '只允许 http/https，不能包含用户信息或 fragment。' : `实际请求：${requestPreview(form.baseURL, form.protocol)}`}</p>
                </div>
                <div className="field">
                  <label htmlFor="channel-key">{editing ? '上游 Key（留空保持不变）' : '上游 Key'}</label>
                  <input
                    id="channel-key" className="input" type="password" name="key" autoComplete="new-password"
                    placeholder={editing ? '留空则保留现有 Key' : 'sk-...'} value={form.key} disabled={submitting}
                    onChange={(event) => updateForm('key', event.target.value)}
                    aria-invalid={errors.key !== undefined}
                    aria-describedby={errors.key === undefined ? undefined : 'channel-key-error'}
                  />
                  {errors.key === undefined ? null : <p className="form-error" id="channel-key-error">{errors.key}</p>}
                  {savedChannel === null ? null : <p className="form-note">当前脱敏值：<code>{savedChannel.key_mask || '—'}</code>；留空不会修改 Key。</p>}
                </div>
              </div>
            </section>

            <details className="advanced" open={advancedOpen} onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}>
              <summary>高级设置</summary>
              <div className="advanced__body">
                <div className="field">
                  <label htmlFor="channel-protocol">协议</label>
                  <select id="channel-protocol" className="input" value={form.protocol} disabled={submitting} onChange={(event) => updateForm('protocol', event.target.value)} aria-invalid={errors.protocol !== undefined}>
                    {PROTOCOL_OPTIONS.map((option) => <option key={option.value} value={option.value} disabled={option.value === 'anthropic_messages' && form.protocol !== 'anthropic_messages'}>{option.label}</option>)}
                  </select>
                  <p className="form-note">协议必须与入站请求一致，不做隐式转换。</p>
                  {errors.protocol === undefined ? null : <p className="form-error">{errors.protocol}</p>}
                </div>
                <div className="field">
                  <label htmlFor="channel-priority">优先级</label>
                  <input id="channel-priority" className="input" type="number" min={PRIORITY_MIN} max={PRIORITY_MAX} value={form.priority} disabled={submitting} onChange={(event) => updateForm('priority', event.target.value)} aria-invalid={errors.priority !== undefined} />
                  <p className="form-note">数值越小越优先（{PRIORITY_MIN}–{PRIORITY_MAX}）。</p>
                  {errors.priority === undefined ? null : <p className="form-error">{errors.priority}</p>}
                </div>
                <div className="field">
                  <label htmlFor="channel-max-concurrency">并发上限</label>
                  <input id="channel-max-concurrency" className="input" type="number" min={CONCURRENCY_MIN} max={CONCURRENCY_MAX} value={form.maxConcurrency} disabled={submitting} onChange={(event) => updateForm('maxConcurrency', event.target.value)} aria-invalid={errors.maxConcurrency !== undefined} />
                  <p className="form-note">每渠道并发槽位（{CONCURRENCY_MIN}–{CONCURRENCY_MAX}）。</p>
                  {errors.maxConcurrency === undefined ? null : <p className="form-error">{errors.maxConcurrency}</p>}
                </div>
                <label className="checkbox-row">
                  <input type="checkbox" checked={form.enabled} disabled={submitting} onChange={(event) => updateForm('enabled', event.target.checked)} />
                  启用此渠道
                </label>
              </div>
            </details>

            {!editing ? (
              <section className="editor-section" aria-labelledby="channel-create-models-title">
                <div className="editor-section__header"><h3 id="channel-create-models-title">支持模型</h3></div>
                <ChannelModelPicker
                  value={form.models}
                  onChange={(models) => updateForm('models', models)}
                  fetchModels={async () => {
                    const response = await channelsApi.previewModels({ base_url: form.baseURL.trim(), protocol: form.protocol, key: form.key });
                    return response.models ?? [];
                  }}
                  fetchLabel="从上游获取模型"
                  fetchDisabled={!canDiscover}
                  disabled={submitting}
                  generation={generation}
                  onPendingChange={handlePreviewPending}
                  onUnauthorized={onUnauthorized}
                  emptyHint="填写有效 Base URL 和 Key 后可获取模型，也可手动添加；允许暂不添加。"
                />
              </section>
            ) : null}

            {connectionDirty ? <p className="form-note editor-dirty-note" role="status">连接配置尚未保存，请先保存连接配置后再获取或测试模型。</p> : null}
            {saveNotice === null ? null : <p className="form-note" role="status">{saveNotice}</p>}
            {formError === null ? null : <p className="form-error" role="alert">{formError}</p>}
          </form>

          {savedChannel === null ? null : (
            <section ref={modelsRef} className="editor-models" tabIndex={-1} aria-label="支持模型配置">
              <ChannelModelsTab
                channel={savedChannel}
                connectionDirty={connectionDirty}
                disabled={submitting}
                generation={generation}
                onPendingChange={handleModelsPending}
                onUnauthorized={onUnauthorized}
                onCountChange={setModelCount}
              />
            </section>
          )}
        </div>
        <footer className="toolbar editor-actions">
          <button type="submit" form="channel-connection-form" className="button" disabled={pending || (editing && !connectionDirty)}>
            {submitting ? (editing ? '正在保存…' : '正在创建…') : editing ? '保存连接配置' : '创建渠道'}
          </button>
          <button type="button" className="button button--ghost" onClick={close} disabled={pending}>{editing ? '完成' : '取消'}</button>
        </footer>
      </section>
    </div>
  );
}
