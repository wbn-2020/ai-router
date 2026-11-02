import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';

import { ApiError, isUnauthorizedError } from '../../lib/api';
import {
  CONTEXT_WINDOW_MAX,
  CONTEXT_WINDOW_MIN,
  MODALITY_OPTIONS,
  REASONING_LEVELS,
  normalizeRouteCapability,
  orderReasoningLevels,
  parseContextWindowInput,
  sameReasoningLevels,
} from '../mappings/capability';
import {
  mappingsApi,
  type InputModalities,
  type ModelRoute,
  type ModelRouteInput,
  type ModelRoutePatch,
  type ReasoningLevel,
} from '../mappings/api';
import { channelsApi } from './api';
import { ChannelModelPicker } from './ChannelModelPicker';
import { ChannelTestResultView, checkStatusLabel } from './ChannelTestResultView';
import { PRIORITY_MAX, PRIORITY_MIN, describeChannelTestError } from './channelForm';
import type { Channel, ChannelTestResult } from './types';

interface ChannelModelsTabProps {
  channel: Channel;
  connectionDirty?: boolean;
  disabled?: boolean;
  generation?: number;
  onPendingChange?: (pending: boolean) => void;
  onUnauthorized: () => void;
  onCountChange?: (count: number) => void;
}

function describeRouteError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (error.code === 'model_route_conflict' || error.status === 409) {
      return '该客户端模型在此渠道已存在。';
    }
    if (error.status === 400) {
      return error.message || '模型配置无效。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `${fallback}（HTTP ${error.status}）。`;
  }
  return `${fallback}，请检查网络连接后重试。`;
}

const CONTEXT_WINDOW_ERROR = `上下文窗口必须是 ${CONTEXT_WINDOW_MIN} 到 ${CONTEXT_WINDOW_MAX} 的整数，或留空表示未声明。`;

type CapabilityInput = Pick<
  ModelRouteInput,
  'input_modalities' | 'context_window' | 'reasoning_levels'
>;

/** declaredCapabilityFields 只把已声明的能力放进创建请求；留空即不传。 */
function declaredCapabilityFields(
  modalities: InputModalities,
  contextWindow: number | null,
  levels: ReasoningLevel[],
): CapabilityInput {
  const fields: CapabilityInput = {};
  if (modalities !== 'unknown') {
    fields.input_modalities = modalities;
  }
  if (contextWindow !== null) {
    fields.context_window = contextWindow;
  }
  if (levels.length > 0) {
    fields.reasoning_levels = levels;
  }
  return fields;
}

function toggleReasoningLevel(
  levels: ReasoningLevel[],
  level: ReasoningLevel,
  checked: boolean,
): ReasoningLevel[] {
  return orderReasoningLevels(
    checked ? [...levels, level] : levels.filter((item) => item !== level),
  );
}

interface CapabilityFieldsProps {
  idPrefix: string;
  modalities: InputModalities;
  contextWindow: string;
  levels: ReasoningLevel[];
  disabled: boolean;
  windowError: string | null;
  onModalitiesChange: (value: InputModalities) => void;
  onContextWindowChange: (value: string) => void;
  onLevelsChange: (levels: ReasoningLevel[]) => void;
}

/**
 * CapabilityFields 是创建/编辑共用的能力声明输入。
 *
 * 三项都允许“未声明”：留空或全部不勾选即表示未声明，不推断默认值。
 */
function CapabilityFields({
  idPrefix,
  modalities,
  contextWindow,
  levels,
  disabled,
  windowError,
  onModalitiesChange,
  onContextWindowChange,
  onLevelsChange,
}: CapabilityFieldsProps): ReactNode {
  const modalitiesID = `${idPrefix}-modalities`;
  const windowID = `${idPrefix}-window`;
  const windowErrorID = `${idPrefix}-window-error`;
  const windowNoteID = `${idPrefix}-window-note`;
  return (
    <>
      <div className="channel-models__manual">
        <div className="field">
          <label htmlFor={modalitiesID}>输入模态</label>
          <select
            id={modalitiesID}
            className="input"
            value={modalities}
            disabled={disabled}
            onChange={(event) => onModalitiesChange(event.target.value as InputModalities)}
          >
            {MODALITY_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
          <p className="form-note">未声明不会被当作支持或拒绝图片。</p>
        </div>
        <div className="field">
          <label htmlFor={windowID}>上下文窗口</label>
          <input
            id={windowID}
            className="input"
            type="number"
            min={CONTEXT_WINDOW_MIN}
            max={CONTEXT_WINDOW_MAX}
            step={1}
            inputMode="numeric"
            placeholder="留空表示未声明"
            value={contextWindow}
            disabled={disabled}
            aria-invalid={windowError !== null}
            aria-describedby={windowError === null ? windowNoteID : windowErrorID}
            onChange={(event) => onContextWindowChange(event.target.value)}
          />
          {windowError === null ? (
            <p className="form-note" id={windowNoteID}>
              范围 {CONTEXT_WINDOW_MIN} 到 {CONTEXT_WINDOW_MAX}；留空表示未声明。
            </p>
          ) : (
            <p className="form-error" id={windowErrorID}>
              {windowError}
            </p>
          )}
        </div>
      </div>
      <fieldset style={{ border: 0, margin: 0, padding: 0, minWidth: 0 }}>
        <legend
          style={{
            marginBottom: 8,
            padding: 0,
            fontSize: 13,
            fontWeight: 600,
            color: 'var(--color-text-soft)',
          }}
        >
          推理档位
        </legend>
        <div className="toolbar">
          {REASONING_LEVELS.map((level) => {
            const id = `${idPrefix}-level-${level}`;
            return (
              <label className="checkbox-row" key={level} htmlFor={id}>
                <input
                  id={id}
                  type="checkbox"
                  checked={levels.includes(level)}
                  disabled={disabled}
                  onChange={(event) =>
                    onLevelsChange(toggleReasoningLevel(levels, level, event.target.checked))
                  }
                />
                {level}
              </label>
            );
          })}
        </div>
        <p className="form-note" style={{ marginTop: 8 }}>
          不勾选任何档位表示未声明。
        </p>
      </fieldset>
    </>
  );
}

type ModelCheck = { generation: number } & (
  | { result: ChannelTestResult; error?: never }
  | { error: string; result?: never }
);

/** ChannelModelsTab 管理某个渠道的模型映射。 */
export function ChannelModelsTab({
  channel,
  connectionDirty = false,
  disabled = false,
  generation = 0,
  onPendingChange,
  onUnauthorized,
  onCountChange,
}: ChannelModelsTabProps): ReactNode {
  const [routes, setRoutes] = useState<ModelRoute[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<number | null>(null);
  const [operationPending, setOperationPending] = useState(false);
  const [fetching, setFetching] = useState(false);
  const [confirmingDeleteID, setConfirmingDeleteID] = useState<number | null>(null);
  const [syncOpen, setSyncOpen] = useState(false);
  const [syncSelection, setSyncSelection] = useState<string[]>([]);
  const [syncBusy, setSyncBusy] = useState(false);
  const [manualClient, setManualClient] = useState('');
  const [manualUpstream, setManualUpstream] = useState('');
  const [manualAdvancedOpen, setManualAdvancedOpen] = useState(false);
  const [manualModalities, setManualModalities] = useState<InputModalities>('unknown');
  const [manualWindow, setManualWindow] = useState('');
  const [manualWindowError, setManualWindowError] = useState<string | null>(null);
  const [manualLevels, setManualLevels] = useState<ReasoningLevel[]>([]);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<ModelRoute | null>(null);
  const [editClient, setEditClient] = useState('');
  const [editUpstream, setEditUpstream] = useState('');
  const [editPriority, setEditPriority] = useState('0');
  const [editAdvancedOpen, setEditAdvancedOpen] = useState(false);
  const [editModalities, setEditModalities] = useState<InputModalities>('unknown');
  const [editWindow, setEditWindow] = useState('');
  const [editLevels, setEditLevels] = useState<ReasoningLevel[]>([]);
  const [editWindowError, setEditWindowError] = useState<string | null>(null);
  const [testingID, setTestingID] = useState<number | null>(null);
  const [checks, setChecks] = useState<Record<number, ModelCheck>>({});
  const pendingRef = useRef({ operation: false, fetching: false });
  const generationRef = useRef(generation);
  generationRef.current = generation;
  const blocked = disabled || operationPending || fetching;

  useEffect(() => {
    let active = true;
    void mappingsApi.listRoutes().then((response) => {
      if (active) {
        setRoutes((response.model_routes ?? []).filter((route) => route.channel_id === channel.id));
      }
    }).catch((error: unknown) => {
      if (!active) {
        return;
      }
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setLoadError(describeRouteError(error, '加载渠道模型失败'));
    });
    return () => { active = false; };
  }, [channel.id, onUnauthorized]);

  useEffect(() => {
    if (routes !== null) {
      onCountChange?.(routes.length);
    }
  }, [onCountChange, routes]);

  useEffect(() => {
    setChecks({});
  }, [generation]);

  const existing = useMemo(() => new Set((routes ?? []).map((route) => route.client_model)), [routes]);

  const beginOperation = (): boolean => {
    if (disabled || pendingRef.current.operation || pendingRef.current.fetching) {
      return false;
    }
    pendingRef.current.operation = true;
    setOperationPending(true);
    onPendingChange?.(true);
    return true;
  };

  const finishOperation = () => {
    pendingRef.current.operation = false;
    setOperationPending(false);
    onPendingChange?.(pendingRef.current.fetching);
  };

  const handleDiscoveryPending = useCallback((pending: boolean) => {
    pendingRef.current.fetching = pending;
    setFetching(pending);
    onPendingChange?.(pending || pendingRef.current.operation);
  }, [onPendingChange]);

  const clearCheck = (id: number) => {
    setChecks((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  };

  const handleToggle = async (route: ModelRoute) => {
    if (!beginOperation()) {
      return;
    }
    setBusyID(route.id);
    setActionError(null);
    setNotice(null);
    try {
      const updated = await mappingsApi.updateRoute(route.id, { enabled: !route.enabled });
      setRoutes((current) => (current ?? []).map((item) => item.id === updated.id ? updated : item));
      clearCheck(route.id);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setActionError(describeRouteError(error, route.enabled ? '停用模型失败' : '启用模型失败'));
    } finally {
      setBusyID(null);
      finishOperation();
    }
  };

  const handleDelete = async (route: ModelRoute) => {
    if (!beginOperation()) {
      return;
    }
    setBusyID(route.id);
    setActionError(null);
    setNotice(null);
    try {
      await mappingsApi.deleteRoute(route.id);
      setRoutes((current) => (current ?? []).filter((item) => item.id !== route.id));
      clearCheck(route.id);
      setConfirmingDeleteID(null);
      if (editing?.id === route.id) {
        setEditing(null);
      }
      setNotice(`已删除模型「${route.client_model}」。`);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setActionError(describeRouteError(error, '删除模型失败'));
    } finally {
      setBusyID(null);
      finishOperation();
    }
  };

  const handleSyncAdd = async () => {
    if (connectionDirty || syncSelection.length === 0 || !beginOperation()) {
      return;
    }
    setSyncBusy(true);
    setActionError(null);
    setNotice(null);
    const pending = syncSelection.filter((model) => !existing.has(model));
    const added: string[] = [];
    try {
      for (const model of pending) {
        const created = await mappingsApi.createRoute({
          client_model: model,
          channel_id: channel.id,
          upstream_model: model,
          priority: 0,
          enabled: true,
        });
        setRoutes((current) => [...(current ?? []), created]);
        added.push(model);
      }
      setSyncSelection([]);
      setSyncOpen(false);
      setNotice(added.length > 0 ? `已添加 ${added.length} 个模型。` : '选中的模型都已存在。');
    } catch (error) {
      setSyncSelection((current) => current.filter((model) => !added.includes(model)));
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setActionError(describeRouteError(error, '添加选中模型失败'));
      if (added.length > 0) {
        setNotice(`已添加 ${added.length} 个模型，剩余模型尚未保存。`);
      }
    } finally {
      setSyncBusy(false);
      finishOperation();
    }
  };

  const handleManualAdd = async (event: FormEvent) => {
    event.preventDefault();
    const client = manualClient.trim();
    const upstream = manualUpstream.trim() === '' ? client : manualUpstream.trim();
    if (client === '' || upstream === '') {
      setActionError('请输入客户端模型名。');
      return;
    }
    const parsedWindow = parseContextWindowInput(manualWindow);
    if (!parsedWindow.ok) {
      setManualWindowError(CONTEXT_WINDOW_ERROR);
      setManualAdvancedOpen(true);
      return;
    }
    setManualWindowError(null);
    if (!beginOperation()) {
      return;
    }
    setAdding(true);
    setActionError(null);
    setNotice(null);
    try {
      const created = await mappingsApi.createRoute({
        client_model: client,
        channel_id: channel.id,
        upstream_model: upstream,
        priority: 0,
        enabled: true,
        ...declaredCapabilityFields(manualModalities, parsedWindow.value, manualLevels),
      });
      setRoutes((current) => [...(current ?? []), created]);
      setManualClient('');
      setManualUpstream('');
      setManualModalities('unknown');
      setManualWindow('');
      setManualWindowError(null);
      setManualLevels([]);
      setNotice(`已添加模型「${created.client_model}」。`);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setActionError(describeRouteError(error, '添加模型失败'));
    } finally {
      setAdding(false);
      finishOperation();
    }
  };

  const startEdit = (route: ModelRoute) => {
    const capability = normalizeRouteCapability(route);
    setEditing(route);
    setEditClient(route.client_model);
    setEditUpstream(route.upstream_model);
    setEditPriority(String(route.priority));
    setEditModalities(capability.modalities);
    setEditWindow(capability.window === null ? '' : String(capability.window));
    setEditLevels(capability.levels);
    setEditWindowError(null);
    setActionError(null);
    setNotice(null);
  };

  const handleEditSave = async (event: FormEvent) => {
    event.preventDefault();
    if (editing === null) {
      return;
    }
    const client = editClient.trim();
    const upstream = editUpstream.trim();
    const priority = Number(editPriority);
    if (client === '' || upstream === '') {
      setActionError('客户端模型和上游模型不能为空。');
      return;
    }
    if (editPriority.trim() === '' || !Number.isInteger(priority) || priority < PRIORITY_MIN || priority > PRIORITY_MAX) {
      setActionError(`优先级必须是 ${PRIORITY_MIN} 到 ${PRIORITY_MAX} 的整数。`);
      return;
    }
    const parsedWindow = parseContextWindowInput(editWindow);
    if (!parsedWindow.ok) {
      setEditWindowError(CONTEXT_WINDOW_ERROR);
      setEditAdvancedOpen(true);
      return;
    }
    setEditWindowError(null);
    if (!beginOperation()) {
      return;
    }

    // 只提交与当前值不同的能力字段：省略即保持，0 / [] 才是显式清空。
    const current = normalizeRouteCapability(editing);
    const nextLevels = orderReasoningLevels(editLevels);
    const patch: ModelRoutePatch = {
      client_model: client,
      upstream_model: upstream,
      priority,
    };
    if (editModalities !== current.modalities) {
      patch.input_modalities = editModalities;
    }
    if (parsedWindow.value !== current.window) {
      patch.context_window = parsedWindow.value === null ? 0 : parsedWindow.value;
    }
    if (!sameReasoningLevels(nextLevels, current.levels)) {
      patch.reasoning_levels = nextLevels;
    }

    setBusyID(editing.id);
    setActionError(null);
    try {
      const updated = await mappingsApi.updateRoute(editing.id, patch);
      setRoutes((currentRoutes) => (currentRoutes ?? []).map((item) => item.id === updated.id ? updated : item));
      clearCheck(editing.id);
      setEditing(null);
      setNotice('模型映射已更新。');
    } catch (error) {
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      setActionError(describeRouteError(error, '更新模型失败'));
    } finally {
      setBusyID(null);
      finishOperation();
    }
  };

  const handleTest = async (route: ModelRoute) => {
    if (!route.enabled || connectionDirty || !beginOperation()) {
      return;
    }
    const requestedGeneration = generation;
    setTestingID(route.id);
    clearCheck(route.id);
    try {
      const result = await channelsApi.testChannel(channel.id, route.client_model);
      if (generationRef.current === requestedGeneration) {
        setChecks((current) => ({ ...current, [route.id]: { generation: requestedGeneration, result } }));
      }
    } catch (error) {
      if (isUnauthorizedError(error)) {
        onUnauthorized();
        return;
      }
      if (generationRef.current === requestedGeneration) {
        setChecks((current) => ({
          ...current,
          [route.id]: { generation: requestedGeneration, error: describeChannelTestError(error) },
        }));
      }
    } finally {
      setTestingID(null);
      finishOperation();
    }
  };

  return (
    <div className="channel-models">
      <div className="channel-models__toolbar">
        <div>
          <h3>支持模型</h3>
          <p className="form-note">模型操作立即保存，与连接配置分开。保留模型别名、优先级与启停状态。</p>
          <p className="form-note" id="channel-model-test-note">测试会消耗少量上游 token，仅检查当前渠道的单个模型；未检查模型不会沿用渠道最近检查结果。</p>
        </div>
        <button
          type="button" className="button button--ghost" disabled={blocked}
          onClick={() => setSyncOpen((current) => !current)}
        >
          {syncOpen ? '收起同步' : '从上游同步'}
        </button>
      </div>

      {syncOpen ? (
        <section className="subpanel" aria-label="从上游同步模型">
          <ChannelModelPicker
            value={syncSelection}
            onChange={setSyncSelection}
            fetchModels={async () => {
              const response = await channelsApi.syncModels(channel.id);
              return response.models ?? [];
            }}
            fetchLabel="获取上游模型"
            generation={generation}
            disabled={disabled || operationPending}
            fetchDisabled={connectionDirty}
            onPendingChange={handleDiscoveryPending}
            onUnauthorized={onUnauthorized}
            emptyHint="点击“获取上游模型”后勾选需要新增的模型。"
          />
          {connectionDirty ? <p className="form-note editor-dirty-note">请先保存连接配置，再获取或添加上游候选模型。</p> : null}
          <div className="toolbar">
            <button type="button" className="button" disabled={blocked || connectionDirty || syncSelection.length === 0} onClick={() => void handleSyncAdd()}>
              {syncBusy ? '正在添加…' : '添加选中模型'}
            </button>
          </div>
        </section>
      ) : null}

      {actionError === null ? null : <p className="form-error" role="alert">{actionError}</p>}
      {notice === null ? null : <p className="form-note" role="status">{notice}</p>}

      <form aria-label="手动添加模型映射" noValidate onSubmit={(event) => void handleManualAdd(event)}>
        <div className="channel-models__manual">
          <div className="field">
            <label htmlFor="manual-client-model">客户端模型</label>
            <input id="manual-client-model" className="input" value={manualClient} disabled={blocked} placeholder="例如 gpt-5" onChange={(event) => setManualClient(event.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="manual-upstream-model">上游模型（留空同名）</label>
            <input id="manual-upstream-model" className="input" value={manualUpstream} disabled={blocked} placeholder="默认与客户端模型相同" onChange={(event) => setManualUpstream(event.target.value)} />
          </div>
          <button type="submit" className="button button--ghost" disabled={blocked}>{adding ? '正在添加…' : '手动添加'}</button>
        </div>
        <details
          className="advanced"
          style={{ marginTop: 10 }}
          open={manualAdvancedOpen}
          onToggle={(event) => setManualAdvancedOpen(event.currentTarget.open)}
        >
          <summary>能力声明（可选）</summary>
          <div className="advanced__body">
            <CapabilityFields
              idPrefix="manual"
              modalities={manualModalities}
              contextWindow={manualWindow}
              levels={manualLevels}
              disabled={blocked}
              windowError={manualWindowError}
              onModalitiesChange={setManualModalities}
              onContextWindowChange={(value) => {
                setManualWindow(value);
                setManualWindowError(null);
              }}
              onLevelsChange={setManualLevels}
            />
            <p className="form-note">创建时留空的能力不会提交，保持未声明；保存后仍可在编辑表单中补充。</p>
          </div>
        </details>
      </form>

      {editing === null ? null : (
        <form className="subpanel" aria-label="编辑模型映射" noValidate onSubmit={(event) => void handleEditSave(event)}>
          <h3>编辑模型映射</h3>
          <div className="channel-models__manual">
            <div className="field">
              <label htmlFor="edit-client-model">客户端模型</label>
              <input id="edit-client-model" className="input" value={editClient} disabled={blocked} onChange={(event) => setEditClient(event.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="edit-upstream-model">上游模型</label>
              <input id="edit-upstream-model" className="input" value={editUpstream} disabled={blocked} onChange={(event) => setEditUpstream(event.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="edit-priority">优先级</label>
              <input id="edit-priority" className="input" type="number" min={PRIORITY_MIN} max={PRIORITY_MAX} value={editPriority} disabled={blocked} onChange={(event) => setEditPriority(event.target.value)} />
            </div>
          </div>
          <details
            className="advanced"
            style={{ marginTop: 10 }}
            open={editAdvancedOpen}
            onToggle={(event) => setEditAdvancedOpen(event.currentTarget.open)}
          >
            <summary>能力声明（可选）</summary>
            <div className="advanced__body">
              <CapabilityFields
                idPrefix="edit"
                modalities={editModalities}
                contextWindow={editWindow}
                levels={editLevels}
                disabled={blocked}
                windowError={editWindowError}
                onModalitiesChange={setEditModalities}
                onContextWindowChange={(value) => {
                  setEditWindow(value);
                  setEditWindowError(null);
                }}
                onLevelsChange={setEditLevels}
              />
              <p className="form-note">只提交本次改动的能力项；未修改的项保持原值，留空或未勾选表示未声明。</p>
            </div>
          </details>
          <div className="toolbar">
            <button type="submit" className="button" disabled={blocked}>{busyID === editing.id ? '正在保存…' : '保存模型映射'}</button>
            <button type="button" className="button button--ghost" disabled={blocked} onClick={() => setEditing(null)}>取消</button>
          </div>
        </form>
      )}

      {loadError !== null ? (
        <p className="form-error" role="alert">{loadError}</p>
      ) : routes === null ? (
        <p className="form-note" role="status">正在加载渠道模型…</p>
      ) : routes.length === 0 ? (
        <p className="form-note">该渠道还没有模型。先同步上游或手动添加。</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th scope="col">客户端模型</th>
                <th scope="col">上游模型</th>
                <th scope="col">优先级</th>
                <th scope="col">状态</th>
                <th scope="col">操作 / 单次检查</th>
              </tr>
            </thead>
            <tbody>
              {routes.map((route) => {
                const savedCheck = checks[route.id];
                const check = savedCheck?.generation === generation ? savedCheck : undefined;
                return (
                  <Fragment key={route.id}>
                    <tr>
                      <td><code>{route.client_model}</code></td>
                      <td><code>{route.upstream_model}</code></td>
                      <td>{route.priority}</td>
                      <td>
                        <button
                          type="button" role="switch" aria-checked={route.enabled}
                          aria-label={`启用模型 ${route.client_model}`} className="button button--ghost"
                          disabled={blocked} onClick={() => void handleToggle(route)}
                        >
                          {route.enabled ? '已启用' : '已停用'}
                        </button>
                      </td>
                      <td>
                        <div className="toolbar">
                          <button type="button" className="button button--ghost" aria-label={`测试模型 ${route.client_model}`} aria-describedby="channel-model-test-note" disabled={blocked || connectionDirty || !route.enabled} onClick={() => void handleTest(route)}>
                            {testingID === route.id ? '测试中…' : '测试'}
                          </button>
                          <span className="muted model-check-state">
                            {check === undefined ? '未检查' : check.result === undefined ? '检查未完成' : `本次${checkStatusLabel(check.result.status)}`}
                          </span>
                          {confirmingDeleteID === route.id ? (
                            <>
                              <button type="button" className="button" aria-label={`确认删除模型 ${route.client_model}`} disabled={blocked} onClick={() => void handleDelete(route)}>确认删除</button>
                              <button type="button" className="button button--ghost" disabled={blocked} onClick={() => setConfirmingDeleteID(null)}>取消</button>
                            </>
                          ) : (
                            <>
                              <button type="button" className="button button--ghost" aria-label={`编辑模型 ${route.client_model}`} disabled={blocked} onClick={() => startEdit(route)}>编辑</button>
                              <button type="button" className="button button--ghost" aria-label={`删除模型 ${route.client_model}`} disabled={blocked} onClick={() => setConfirmingDeleteID(route.id)}>删除</button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                    {check === undefined ? null : (
                      <tr className="model-check-row">
                        <td colSpan={5}>
                          {check.result === undefined ? (
                            <p className="form-error model-check__error" role="alert">{check.error}</p>
                          ) : (
                            <ChannelTestResultView result={check.result} clientModel={route.client_model} label={`模型 ${route.client_model} 的测试结果`} />
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
