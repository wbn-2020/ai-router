import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react';

import { ApiError, isUnauthorizedError } from '../../lib/api';
import {
  mappingsApi,
  type ChannelSummary,
  type ModelRoute,
  type ModelRouteInput,
} from './api';

/** PRIORITY_MIN / PRIORITY_MAX 与后端 model_routes.priority 校验范围一致。 */
export const PRIORITY_MIN = 0;
export const PRIORITY_MAX = 1000;

const TIMESTAMP_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

/** Go 的 RFC3339Nano 最多 9 位小数；部分浏览器只接受 3 位，截断后再解析。 */
function parseTimestamp(value: string): Date | null {
  const normalized = value.replace(/(\.\d{3})\d+/, '$1');
  const parsed = new Date(normalized);
  if (Number.isNaN(parsed.getTime())) {
    return null;
  }
  return parsed;
}

function formatTimestamp(value: string | undefined): string {
  if (typeof value !== 'string' || value === '') {
    return '—';
  }
  const parsed = parseTimestamp(value);
  if (parsed === null) {
    return '—';
  }
  return TIMESTAMP_FORMATTER.format(parsed);
}

function TimeCell({ value }: { value?: string }) {
  if (typeof value !== 'string' || value === '' || parseTimestamp(value) === null) {
    return <span className="muted">—</span>;
  }
  return <time dateTime={value}>{formatTimestamp(value)}</time>;
}

/* -------------------------------------------------------------------------- */
/* 表单状态与校验                                                              */
/* -------------------------------------------------------------------------- */

interface FormState {
  clientModel: string;
  channelID: string;
  upstreamModel: string;
  priority: string;
  enabled: boolean;
}

type FormField = 'clientModel' | 'channelID' | 'upstreamModel' | 'priority';

type FormErrors = Partial<Record<FormField, string>>;

const EMPTY_FORM: FormState = {
  clientModel: '',
  channelID: '',
  upstreamModel: '',
  priority: String(PRIORITY_MIN),
  enabled: true,
};

/**
 * validateMappingForm 在发请求前拦截明显的字段错误。
 *
 * 后端仍会做同样的校验与 client_model + channel_id 唯一约束；这里只是让
 * 管理员尽快看到问题，不把唯一约束当作前端责任。
 */
function validateMappingForm(form: FormState): FormErrors {
  const errors: FormErrors = {};

  if (form.clientModel.trim() === '') {
    errors.clientModel = '请输入客户端模型名';
  }

  const channelID = Number(form.channelID);
  if (form.channelID === '' || !Number.isInteger(channelID) || channelID <= 0) {
    errors.channelID = '请选择渠道';
  }

  if (form.upstreamModel.trim() === '') {
    errors.upstreamModel = '请输入上游模型名';
  }

  const priority = Number(form.priority);
  if (
    form.priority.trim() === '' ||
    !Number.isInteger(priority) ||
    priority < PRIORITY_MIN ||
    priority > PRIORITY_MAX
  ) {
    errors.priority = `优先级必须是 ${PRIORITY_MIN} 到 ${PRIORITY_MAX} 的整数`;
  }

  return errors;
}

function describeLoadError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 429) {
      return '加载过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `加载模型映射失败（HTTP ${error.status}）`;
  }
  return '加载模型映射失败，请检查网络连接后重试。';
}

function describeMutationError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (error.code === 'model_route_conflict' || error.status === 409) {
      return '同一客户端模型与渠道的映射已存在，请调整客户端模型或渠道。';
    }
    if (error.code === 'model_route_not_found' || error.status === 404) {
      return '模型映射不存在，可能已被删除，请刷新页面后重试。';
    }
    if (error.status === 400) {
      return error.message || '模型映射配置无效，请检查输入。';
    }
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `${fallback}（HTTP ${error.status}）`;
  }
  return `${fallback}，请检查网络连接后重试。`;
}

function sortRoutes(routes: ModelRoute[]): ModelRoute[] {
  return [...routes].sort(
    (left, right) =>
      left.client_model.localeCompare(right.client_model) ||
      left.priority - right.priority ||
      left.id - right.id,
  );
}

/* -------------------------------------------------------------------------- */
/* 页面                                                                        */
/* -------------------------------------------------------------------------- */

/**
 * MappingsPage 维护“客户端模型 → 渠道 + 上游模型”的手工映射。
 *
 * - 列表按客户端模型分组，可再按模型/渠道筛选；
 * - 创建/编辑共用表单；客户端模型来自 /admin/api/models 的建议列表，也允许
 *   输入新模型名（第一个映射创建前模型尚不存在于该列表）；
 * - 启停直接 PATCH；每次写操作后刷新 /admin/api/models，让“没有启用路由的
 *   模型不再出现在模型列表”在界面上可见；
 * - 冲突（409 model_route_conflict）只显示错误，不修改本地列表。
 */
export function MappingsPage(): ReactNode {
  const [routes, setRoutes] = useState<ModelRoute[] | null>(null);
  const [channels, setChannels] = useState<ChannelSummary[] | null>(null);
  const [models, setModels] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [modelsError, setModelsError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);

  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<number | null>(null);
  const [confirmingDeleteID, setConfirmingDeleteID] = useState<number | null>(null);

  const [formOpen, setFormOpen] = useState(false);
  const [editingID, setEditingID] = useState<number | null>(null);
  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [formErrors, setFormErrors] = useState<FormErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const [modelFilter, setModelFilter] = useState('all');
  const [channelFilter, setChannelFilter] = useState('all');
  const [expandedModels, setExpandedModels] = useState<Set<string>>(() => new Set());

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    setModelsError(null);
    try {
      const [routeResponse, modelResponse, channelResponse] = await Promise.all([
        mappingsApi.listRoutes(),
        mappingsApi.listModels(),
        mappingsApi.listChannels(),
      ]);
      setRoutes(routeResponse.model_routes ?? []);
      setModels(modelResponse.models ?? []);
      setChannels(channelResponse.channels ?? []);
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
    void load();
  }, [load]);

  const refreshModels = useCallback(async () => {
    try {
      const response = await mappingsApi.listModels();
      setModels(response.models ?? []);
      setModelsError(null);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setModelsError('已发布客户端模型刷新失败，请重试。');
    }
  }, []);

  const channelByID = useMemo(() => {
    const map = new Map<number, ChannelSummary>();
    for (const channel of channels ?? []) {
      map.set(channel.id, channel);
    }
    return map;
  }, [channels]);

  const channelName = useCallback(
    (route: ModelRoute): string => channelByID.get(route.channel_id)?.name ?? `渠道 #${route.channel_id}`,
    [channelByID],
  );

  const modelOptions = useMemo(() => {
    const unique = new Set((routes ?? []).map((route) => route.client_model));
    return Array.from(unique).sort((left, right) => left.localeCompare(right));
  }, [routes]);

  // 编辑 client_model 或删除记录后，旧筛选值可能不再存在；回退到“全部”。
  useEffect(() => {
    if (modelFilter !== 'all' && routes !== null && !modelOptions.includes(modelFilter)) {
      setModelFilter('all');
    }
  }, [modelFilter, modelOptions, routes]);

  const filteredRoutes = useMemo(() => {
    return (routes ?? []).filter((route) => {
      const matchesModel = modelFilter === 'all' || route.client_model === modelFilter;
      const matchesChannel =
        channelFilter === 'all' || String(route.channel_id) === channelFilter;
      return matchesModel && matchesChannel;
    });
  }, [routes, modelFilter, channelFilter]);

  const groups = useMemo(() => {
    const grouped = new Map<string, ModelRoute[]>();
    for (const route of filteredRoutes) {
      const list = grouped.get(route.client_model);
      if (list === undefined) {
        grouped.set(route.client_model, [route]);
      } else {
        list.push(route);
      }
    }
    return Array.from(grouped.entries())
      .map(([clientModel, items]) => ({
        clientModel,
        routes: [...items].sort(
          (left, right) => left.priority - right.priority || left.id - right.id,
        ),
      }))
      .sort((left, right) => left.clientModel.localeCompare(right.clientModel));
  }, [filteredRoutes]);

  const channelOptions = useMemo(() => {
    const list = [...(channels ?? [])];
    return list.sort((left, right) => left.name.localeCompare(right.name));
  }, [channels]);

  const resetForm = () => {
    setForm(EMPTY_FORM);
    setFormErrors({});
    setFormError(null);
    setEditingID(null);
  };

  const openCreate = () => {
    resetForm();
    setFormOpen(true);
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
  };

  const openEdit = (route: ModelRoute) => {
    setEditingID(route.id);
    setForm({
      clientModel: route.client_model,
      channelID: String(route.channel_id),
      upstreamModel: route.upstream_model,
      priority: String(route.priority),
      enabled: route.enabled,
    });
    setFormErrors({});
    setFormError(null);
    setFormOpen(true);
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
  };

  const closeForm = () => {
    if (submitting) {
      return;
    }
    setFormOpen(false);
    resetForm();
  };

  const updateForm = <Key extends keyof FormState>(key: Key, value: FormState[Key]) => {
    setForm((current) => ({ ...current, [key]: value }));
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const errors = validateMappingForm(form);
    setFormErrors(errors);
    if (Object.keys(errors).length > 0) {
      setFormError(null);
      return;
    }

    const payload: ModelRouteInput = {
      client_model: form.clientModel.trim(),
      channel_id: Number(form.channelID),
      upstream_model: form.upstreamModel.trim(),
      priority: Number(form.priority),
      enabled: form.enabled,
    };

    setSubmitting(true);
    setFormError(null);
    setActionError(null);
    setNotice(null);
    try {
      if (editingID === null) {
        const created = await mappingsApi.createRoute(payload);
        setRoutes((current) => sortRoutes([...(current ?? []), created]));
        setNotice('映射已创建。');
      } else {
        const updated = await mappingsApi.updateRoute(editingID, payload);
        setRoutes((current) =>
          sortRoutes(
            (current ?? []).map((route) => (route.id === updated.id ? updated : route)),
          ),
        );
        setNotice('映射已更新。');
      }
      setFormOpen(false);
      resetForm();
      await refreshModels();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setFormError(
        describeMutationError(error, editingID === null ? '创建映射失败' : '更新映射失败'),
      );
    } finally {
      setSubmitting(false);
    }
  };

  const handleToggle = async (route: ModelRoute) => {
    setBusyID(route.id);
    setActionError(null);
    setNotice(null);
    setConfirmingDeleteID(null);
    try {
      const updated = await mappingsApi.updateRoute(route.id, { enabled: !route.enabled });
      setRoutes((current) =>
        (current ?? []).map((item) => (item.id === updated.id ? updated : item)),
      );
      setNotice(updated.enabled ? '映射已启用。' : '映射已禁用。');
      await refreshModels();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError(describeMutationError(error, route.enabled ? '禁用映射失败' : '启用映射失败'));
    } finally {
      setBusyID(null);
    }
  };

  const handleDelete = async (route: ModelRoute) => {
    setBusyID(route.id);
    setActionError(null);
    setNotice(null);
    try {
      await mappingsApi.deleteRoute(route.id);
      setRoutes((current) => (current ?? []).filter((item) => item.id !== route.id));
      setConfirmingDeleteID(null);
      setNotice('映射已删除。');
      await refreshModels();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setActionError(describeMutationError(error, '删除映射失败'));
    } finally {
      setBusyID(null);
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

  const editingRoute = editingID === null ? null : (routes ?? []).find((route) => route.id === editingID) ?? null;
  const hasFilters = modelFilter !== 'all' || channelFilter !== 'all';
  const availableModels = models ?? [];

  return (
    <>
      <header className="page-header">
        <div>
          <h2>模型路由</h2>
          <p>
            正常添加渠道时会自动生成模型路由。这里只处理别名、多渠道分流和优先级；禁用映射或渠道不会进入候选。
          </p>
        </div>
        <div className="toolbar">
          <button
            type="button"
            className="button"
            onClick={openCreate}
            disabled={routes === null || loading}
          >
            高级手工映射
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

      {formOpen ? (
        <section className="panel" aria-labelledby="mappings-form-title">
          <div className="panel-header">
            <h2 id="mappings-form-title">
              {editingRoute === null ? '新建手工映射' : `编辑映射：${editingRoute.client_model}`}
            </h2>
            <span className="badge badge--muted">高级 · 手工映射</span>
          </div>
          <div className="panel-body">
            <p className="muted">
              客户端模型可输入新名称，也可从已发布模型中选择；同一客户端模型与渠道只能有一条映射。
            </p>

            <form
              onSubmit={(event) => {
                void handleSubmit(event);
              }}
              noValidate
              style={{
                display: 'grid',
                gap: 14,
                gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
                marginTop: 16,
              }}
            >
              <div className="field">
                <label htmlFor="mapping-client-model">客户端模型</label>
                <input
                  id="mapping-client-model"
                  className="input"
                  type="text"
                  name="client-model"
                  list="mapping-client-model-options"
                  autoComplete="off"
                  placeholder="例如 gpt-5-codex"
                  value={form.clientModel}
                  onChange={(event) => {
                    updateForm('clientModel', event.target.value);
                  }}
                  aria-invalid={formErrors.clientModel !== undefined}
                  aria-describedby={
                    formErrors.clientModel === undefined
                      ? undefined
                      : 'mapping-client-model-error'
                  }
                />
                <datalist id="mapping-client-model-options">
                  {availableModels.map((model) => (
                    <option key={model} value={model} />
                  ))}
                </datalist>
                {formErrors.clientModel === undefined ? null : (
                  <p className="form-error" id="mapping-client-model-error">
                    {formErrors.clientModel}
                  </p>
                )}
              </div>

              <div className="field">
                <label htmlFor="mapping-channel">渠道</label>
                <select
                  id="mapping-channel"
                  className="input"
                  name="channel"
                  value={form.channelID}
                  onChange={(event) => {
                    updateForm('channelID', event.target.value);
                  }}
                  aria-invalid={formErrors.channelID !== undefined}
                  aria-describedby={
                    formErrors.channelID === undefined ? undefined : 'mapping-channel-error'
                  }
                >
                  <option value="">请选择渠道</option>
                  {channelOptions.map((channel) => (
                    <option key={channel.id} value={channel.id}>
                      {channel.name} · {channel.protocol}
                      {channel.enabled ? '' : ' · 已禁用'}
                    </option>
                  ))}
                </select>
                {formErrors.channelID === undefined ? null : (
                  <p className="form-error" id="mapping-channel-error">
                    {formErrors.channelID}
                  </p>
                )}
                {channelOptions.length === 0 ? (
                  <p className="form-note">尚未配置渠道；请先在渠道页创建渠道，再添加映射。</p>
                ) : null}
              </div>

              <div className="field">
                <label htmlFor="mapping-upstream-model">上游模型</label>
                <input
                  id="mapping-upstream-model"
                  className="input"
                  type="text"
                  name="upstream-model"
                  autoComplete="off"
                  placeholder="例如 gpt-5.1-codex"
                  value={form.upstreamModel}
                  onChange={(event) => {
                    updateForm('upstreamModel', event.target.value);
                  }}
                  aria-invalid={formErrors.upstreamModel !== undefined}
                  aria-describedby={
                    formErrors.upstreamModel === undefined
                      ? undefined
                      : 'mapping-upstream-model-error'
                  }
                />
                {formErrors.upstreamModel === undefined ? null : (
                  <p className="form-error" id="mapping-upstream-model-error">
                    {formErrors.upstreamModel}
                  </p>
                )}
              </div>

              <div className="field">
                <label htmlFor="mapping-priority">优先级</label>
                <input
                  id="mapping-priority"
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
                    formErrors.priority === undefined ? undefined : 'mapping-priority-error'
                  }
                />
                {formErrors.priority === undefined ? null : (
                  <p className="form-error" id="mapping-priority-error">
                    {formErrors.priority}
                  </p>
                )}
                <p className="form-note">数值越小越优先（{PRIORITY_MIN}–{PRIORITY_MAX}）。</p>
              </div>

              <label
                htmlFor="mapping-enabled"
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  fontSize: 13,
                  fontWeight: 600,
                  color: 'var(--color-text-soft)',
                }}
              >
                <input
                  id="mapping-enabled"
                  type="checkbox"
                  name="enabled"
                  checked={form.enabled}
                  onChange={(event) => {
                    updateForm('enabled', event.target.checked);
                  }}
                />
                启用此映射
              </label>

              {formError === null ? null : (
                <p
                  className="form-error"
                  role="alert"
                  style={{ marginTop: 0, gridColumn: '1 / -1' }}
                >
                  {formError}
                </p>
              )}

              <div className="toolbar" style={{ marginTop: 2, gridColumn: '1 / -1' }}>
                <button
                  type="submit"
                  className="button"
                  disabled={submitting || channelOptions.length === 0}
                >
                  {submitting
                    ? editingID === null
                      ? '正在创建…'
                      : '正在保存…'
                    : editingID === null
                      ? '创建映射'
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
      ) : null}

      {loadError !== null ? (
        <section className="panel">
          <div className="state state--error" role="alert">
            <span className="state-icon" aria-hidden="true">
              !
            </span>
            <h2>模型映射加载失败</h2>
            <p>{loadError}</p>
            <button
              type="button"
              className="button button--ghost"
              onClick={() => {
                void load();
              }}
            >
              重试
            </button>
          </div>
        </section>
      ) : routes === null || loading ? (
        <section className="panel">
          <p className="panel-body form-note" role="status">
            正在加载模型映射…
          </p>
        </section>
      ) : (
        <>
          <section className="panel" aria-labelledby="mappings-models-title">
            <div className="panel-header">
              <h2 id="mappings-models-title">已发布客户端模型</h2>
              <span className="badge badge--muted">{availableModels.length} 个</span>
            </div>
            <div className="panel-body">
              {modelsError === null ? null : (
                <p className="form-error" role="alert" style={{ marginBottom: 10 }}>
                  {modelsError}{' '}
                  <button
                    type="button"
                    className="button button--ghost"
                    onClick={() => {
                      void refreshModels();
                    }}
                  >
                    重试
                  </button>
                </p>
              )}
              {availableModels.length === 0 ? (
                <p className="muted">
                  暂无已发布的客户端模型。映射与承载渠道均启用后才会发布到 /v1/models；
                  Codex manifest 仅包含 Responses 协议候选。已配置路由仍保留在下方。
                </p>
              ) : (
                <>
                  <p className="muted">
                    来自模型接口的发布列表；下方仍展示所有已配置路由，包括停用映射与已删除渠道的映射。
                  </p>
                  <div className="toolbar" aria-label="已发布客户端模型">
                    {availableModels.map((model) => (
                      <span className="badge" key={model}>
                        {model}
                      </span>
                    ))}
                  </div>
                </>
              )}
            </div>
          </section>

          <section className="panel" aria-labelledby="mappings-list-title">
            <div className="panel-header">
              <h2 id="mappings-list-title">映射列表</h2>
              <div className="toolbar" role="group" aria-label="映射筛选">
                <select
                  className="input"
                  style={{ width: 'auto', minWidth: 160 }}
                  aria-label="按客户端模型筛选"
                  value={modelFilter}
                  onChange={(event) => {
                    setModelFilter(event.target.value);
                  }}
                >
                  <option value="all">全部客户端模型</option>
                  {modelOptions.map((model) => (
                    <option key={model} value={model}>
                      {model}
                    </option>
                  ))}
                </select>
                <select
                  className="input"
                  style={{ width: 'auto', minWidth: 150 }}
                  aria-label="按渠道筛选"
                  value={channelFilter}
                  onChange={(event) => {
                    setChannelFilter(event.target.value);
                  }}
                >
                  <option value="all">全部渠道</option>
                  {channelOptions.map((channel) => (
                    <option key={channel.id} value={String(channel.id)}>
                      {channel.name}
                    </option>
                  ))}
                </select>
                <span className="badge badge--muted">
                  {hasFilters ? `${filteredRoutes.length} / ${routes.length}` : routes.length} 条映射
                </span>
              </div>
            </div>

            {routes.length === 0 ? (
              <div className="state state--empty">
                <span className="state-icon" aria-hidden="true">
                  ◇
                </span>
                <h3>尚无模型映射</h3>
                <p>
                  正常添加渠道时会自动生成模型路由。需要别名或多渠道分流时，再在这里创建映射。
                </p>
                <button type="button" className="button" onClick={openCreate}>
                  创建第一条映射
                </button>
              </div>
            ) : filteredRoutes.length === 0 ? (
              <div className="state state--empty">
                <span className="state-icon" aria-hidden="true">
                  ◇
                </span>
                <h3>没有符合筛选条件的映射</h3>
                <p>调整客户端模型或渠道筛选后再试。</p>
                {hasFilters ? (
                  <button
                    type="button"
                    className="button button--ghost"
                    onClick={() => {
                      setModelFilter('all');
                      setChannelFilter('all');
                    }}
                  >
                    清除筛选
                  </button>
                ) : null}
              </div>
            ) : (
              <div className="model-group-list">
                {groups.map((group) => {
                  const availableRoutes = group.routes.filter(
                    (route) => route.enabled && channelByID.get(route.channel_id)?.enabled === true,
                  );
                  const protocols = Array.from(new Set(
                    availableRoutes.map((route) => channelByID.get(route.channel_id)?.protocol ?? '未知协议'),
                  ));
                  const carrierRoutes = Array.from(new Map(
                    group.routes.map((route) => [route.channel_id, route]),
                  ).values());
                  const allDisabled = group.routes.every((route) => !route.enabled);
                  const expanded = expandedModels.has(group.clientModel);

                  return (
                    <section key={group.clientModel} aria-label={`${group.clientModel} 模型路由`}>
                      <details
                        className="model-group"
                        open={expanded}
                        onToggle={(event) => {
                          const open = event.currentTarget.open;
                          setExpandedModels((current) => {
                            if (current.has(group.clientModel) === open) {
                              return current;
                            }
                            const next = new Set(current);
                            if (open) {
                              next.add(group.clientModel);
                            } else {
                              next.delete(group.clientModel);
                            }
                            return next;
                          });
                        }}
                      >
                        <summary className="panel-header model-group-header">
                          <h3>{group.clientModel}</h3>
                          <span className="badge badge--muted">已配置 {group.routes.length} 条</span>
                          <span className={availableRoutes.length > 0 ? 'badge' : 'badge badge--muted'}>
                            可用 {availableRoutes.length} 条
                          </span>
                          {protocols.length === 0 ? null : (
                            <span className="badge badge--muted">可用协议：{protocols.join(' / ')}</span>
                          )}
                          {allDisabled ? (
                            <span className="badge badge--muted">全部停用</span>
                          ) : availableRoutes.length === 0 ? (
                            <span className="badge badge--muted">无可用路由</span>
                          ) : null}
                          <span className="muted model-group-toggle">
                            {expanded ? '收起映射明细' : '展开映射明细'}
                          </span>
                          <span className="toolbar model-group-channels" aria-label="承载渠道">
                            {carrierRoutes.map((route) => {
                              const channel = channelByID.get(route.channel_id);
                              const available = route.enabled && channel?.enabled === true;
                              return (
                                <span
                                  className={available ? 'badge' : 'badge badge--muted'}
                                  key={route.channel_id}
                                >
                                  {channel === undefined
                                    ? `已删除或未知渠道 #${route.channel_id}`
                                    : `${channel.name} · ${channel.protocol}${channel.enabled ? '' : ' · 渠道已停用'}`}
                                  {route.enabled ? '' : ' · 映射已停用'}
                                </span>
                              );
                            })}
                          </span>
                        </summary>
                        {expanded ? (
                          <div className="table-wrap">
                            <table>
                              <caption className="sr-only">{group.clientModel} 的映射明细</caption>
                              <thead>
                                <tr>
                                  <th scope="col">客户端模型</th>
                                  <th scope="col">渠道</th>
                                  <th scope="col">上游模型</th>
                                  <th scope="col">优先级</th>
                                  <th scope="col">状态</th>
                                  <th scope="col">更新时间</th>
                                  <th scope="col">操作</th>
                                </tr>
                              </thead>
                              <tbody>
                                {group.routes.map((route) => {
                                  const name = channelName(route);
                                  const channel = channelByID.get(route.channel_id);
                                  const busy = busyID === route.id;
                                  return (
                                    <tr key={route.id}>
                                      <td>
                                        <code>{route.client_model}</code>
                                      </td>
                                      <td>
                                        {name}
                                        {channel === undefined ? (
                                          <span className="badge badge--muted">已删除或未知渠道</span>
                                        ) : (
                                          <>
                                            <span className="badge badge--muted">{channel.protocol}</span>
                                            {channel.enabled ? null : (
                                              <span className="badge badge--muted">渠道已停用</span>
                                            )}
                                          </>
                                        )}
                                      </td>
                                      <td>
                                        <code>{route.upstream_model}</code>
                                      </td>
                                      <td>{route.priority}</td>
                                      <td>
                                        <span className={route.enabled ? 'badge' : 'badge badge--muted'}>
                                          {route.enabled ? '启用' : '禁用'}
                                        </span>
                                      </td>
                                      <td>
                                        <TimeCell value={route.updated_at} />
                                      </td>
                                      <td>
                                        <div
                                          className="toolbar"
                                          role="group"
                                          aria-label={`映射 ${route.client_model} 到 ${name} 的操作`}
                                        >
                                          <button
                                            type="button"
                                            className="button button--ghost"
                                            aria-label={`编辑映射 ${route.client_model} → ${name}`}
                                            onClick={() => {
                                              openEdit(route);
                                            }}
                                            disabled={busy || submitting}
                                          >
                                            编辑
                                          </button>
                                          {confirmingDeleteID === route.id ? (
                                            <>
                                              <button
                                                type="button"
                                                className="button"
                                                aria-label={`确认删除映射 ${route.client_model} → ${name}`}
                                                onClick={() => {
                                                  void handleDelete(route);
                                                }}
                                                disabled={busy}
                                              >
                                                {busy ? '正在删除…' : '确认删除'}
                                              </button>
                                              <button
                                                type="button"
                                                className="button button--ghost"
                                                aria-label={`取消删除映射 ${route.client_model} → ${name}`}
                                                onClick={() => {
                                                  setConfirmingDeleteID(null);
                                                }}
                                                disabled={busy}
                                              >
                                                取消
                                              </button>
                                            </>
                                          ) : (
                                            <>
                                              <button
                                                type="button"
                                                className="button button--ghost"
                                                aria-label={`${route.enabled ? '禁用' : '启用'}映射 ${route.client_model} → ${name}`}
                                                onClick={() => {
                                                  void handleToggle(route);
                                                }}
                                                disabled={busy}
                                              >
                                                {busy ? '处理中…' : route.enabled ? '禁用' : '启用'}
                                              </button>
                                              <button
                                                type="button"
                                                className="button button--ghost"
                                                aria-label={`删除映射 ${route.client_model} → ${name}`}
                                                onClick={() => {
                                                  setConfirmingDeleteID(route.id);
                                                  setActionError(null);
                                                }}
                                                disabled={busy}
                                              >
                                                删除
                                              </button>
                                            </>
                                          )}
                                        </div>
                                      </td>
                                    </tr>
                                  );
                                })}
                              </tbody>
                            </table>
                          </div>
                        ) : null}
                      </details>
                    </section>
                  );
                })}
              </div>
            )}
          </section>
        </>
      )}
    </>
  );
}

export default MappingsPage;
