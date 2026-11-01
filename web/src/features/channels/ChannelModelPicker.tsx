import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';

import { ApiError, isUnauthorizedError } from '../../lib/api';
import { describeModelDiscoveryError } from './channelForm';

interface ChannelModelPickerProps {
  value: string[];
  onChange: (models: string[]) => void;
  /** 返回上游模型列表；不传时只允许手动添加。 */
  fetchModels?: () => Promise<string[]>;
  fetchLabel?: string;
  disabled?: boolean;
  fetchDisabled?: boolean;
  /** 连接变化时递增，不包含 Base URL 或 Key。 */
  generation?: number;
  onPendingChange?: (pending: boolean) => void;
  onUnauthorized?: () => void;
  emptyHint?: string;
}

interface DiscoveryState {
  generation: number;
  models: string[];
  error: string | null;
  notice: string | null;
}

/** ChannelModelPicker 是渠道创建/编辑共用的模型多选器。 */
export function ChannelModelPicker({
  value,
  onChange,
  fetchModels,
  fetchLabel = '从上游获取',
  disabled = false,
  fetchDisabled = false,
  generation = 0,
  onPendingChange,
  onUnauthorized,
  emptyHint = '还没有选择模型；可以手动添加，或从上游获取后勾选。',
}: ChannelModelPickerProps): ReactNode {
  const [discovery, setDiscovery] = useState<DiscoveryState>({
    generation,
    models: [],
    error: null,
    notice: null,
  });
  const [query, setQuery] = useState('');
  const [manual, setManual] = useState('');
  const [fetchingGeneration, setFetchingGeneration] = useState<number | null>(null);
  const requestSequence = useRef(0);
  const fetchingRef = useRef(false);

  useEffect(() => {
    requestSequence.current += 1;
    fetchingRef.current = false;
    setFetchingGeneration(null);
    setQuery('');
    onPendingChange?.(false);
    return () => {
      requestSequence.current += 1;
      fetchingRef.current = false;
      onPendingChange?.(false);
    };
  }, [generation, onPendingChange]);

  const currentDiscovery = discovery.generation === generation ? discovery : null;
  const candidates = currentDiscovery?.models ?? [];
  const fetching = fetchingGeneration === generation;
  const selected = useMemo(() => new Set(value), [value]);
  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    return keyword === ''
      ? candidates
      : candidates.filter((model) => model.toLowerCase().includes(keyword));
  }, [candidates, query]);

  const replace = (next: string[]) => {
    const normalized = Array.from(new Set(next.map((model) => model.trim()).filter(Boolean)));
    onChange(normalized);
  };

  const toggle = (model: string) => {
    replace(selected.has(model) ? value.filter((item) => item !== model) : [...value, model]);
  };

  const addManual = () => {
    if (disabled || manual.trim() === '') {
      return;
    }
    replace([...value, manual]);
    setManual('');
  };

  const handleFetch = async () => {
    if (fetchModels === undefined || disabled || fetchDisabled || fetchingRef.current) {
      return;
    }
    const sequence = ++requestSequence.current;
    fetchingRef.current = true;
    setFetchingGeneration(generation);
    onPendingChange?.(true);
    setDiscovery({ generation, models: candidates, error: null, notice: null });
    try {
      const models = await fetchModels();
      if (sequence !== requestSequence.current) {
        return;
      }
      const normalized = Array.from(new Set(models.map((model) => model.trim()).filter(Boolean)));
      setDiscovery({
        generation,
        models: normalized,
        error: null,
        notice:
          normalized.length === 0
            ? '上游返回了空模型列表，请手动添加。'
            : `已从上游获取 ${normalized.length} 个模型。`,
      });
    } catch (error) {
      if (sequence !== requestSequence.current) {
        return;
      }
      if (isUnauthorizedError(error)) {
        onUnauthorized?.();
        return;
      }
      setDiscovery({
        generation,
        models:
          error instanceof ApiError && error.code === 'unsupported_protocol' ? [] : candidates,
        error: describeModelDiscoveryError(error),
        notice: null,
      });
    } finally {
      if (sequence === requestSequence.current) {
        fetchingRef.current = false;
        setFetchingGeneration(null);
        onPendingChange?.(false);
      }
    }
  };

  return (
    <div className="model-picker">
      <div className="model-picker__toolbar">
        {fetchModels === undefined ? null : (
          <button
            type="button"
            className="button button--ghost"
            onClick={() => void handleFetch()}
            disabled={disabled || fetchDisabled || fetching}
          >
            {fetching ? '正在获取…' : fetchLabel}
          </button>
        )}
        {candidates.length === 0 ? null : (
          <>
            <button
              type="button"
              className="button button--ghost"
              disabled={disabled || filtered.length === 0}
              onClick={() => replace([...value, ...filtered])}
            >
              全选结果
            </button>
            <button
              type="button"
              className="button button--ghost"
              disabled={disabled || !filtered.some((model) => selected.has(model))}
              onClick={() => {
                const visible = new Set(filtered);
                replace(value.filter((model) => !visible.has(model)));
              }}
            >
              取消结果
            </button>
          </>
        )}
        <span className="badge badge--muted">已选 {value.length}</span>
      </div>

      {currentDiscovery?.error == null ? null : (
        <p className="form-error" role="alert">
          {currentDiscovery.error}
        </p>
      )}
      {currentDiscovery?.notice == null ? null : (
        <p className="form-note" role="status">
          {currentDiscovery.notice}
        </p>
      )}

      {candidates.length === 0 ? (
        <p className="form-note">{emptyHint}</p>
      ) : (
        <>
          <input
            className="input"
            type="search"
            aria-label="搜索模型"
            placeholder="搜索模型名"
            value={query}
            disabled={disabled}
            onChange={(event) => setQuery(event.target.value)}
          />
          <div className="model-picker__list" role="group" aria-label="上游模型列表">
            {filtered.length === 0 ? (
              <p className="form-note">没有匹配的模型。</p>
            ) : (
              filtered.map((model) => (
                <label key={model} className="model-picker__item">
                  <input
                    type="checkbox"
                    aria-label={`选择模型 ${model}`}
                    checked={selected.has(model)}
                    disabled={disabled}
                    onChange={() => toggle(model)}
                  />
                  <code>{model}</code>
                </label>
              ))
            )}
          </div>
        </>
      )}

      <div className="model-picker__manual">
        <input
          className="input"
          type="text"
          aria-label="手动添加模型"
          placeholder="输入模型名后回车"
          value={manual}
          disabled={disabled}
          onChange={(event) => setManual(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              addManual();
            }
          }}
        />
        <button
          type="button"
          className="button button--ghost"
          disabled={disabled || manual.trim() === ''}
          onClick={addManual}
        >
          添加
        </button>
      </div>

      {value.length === 0 ? null : (
        <div className="model-picker__selected" aria-label="已选模型">
          {value.map((model) => (
            <span key={model} className="chip">
              <code>{model}</code>
              <button
                type="button"
                className="chip__remove"
                aria-label={`移除模型 ${model}`}
                disabled={disabled}
                onClick={() => replace(value.filter((item) => item !== model))}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
