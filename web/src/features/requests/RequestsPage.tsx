import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from 'react';

import { ApiError, apiRequest, isUnauthorizedError } from '../../lib/api';
import RequestDetail, {
  formatCount,
  formatMilliseconds,
  formatTimestamp,
  parseTimestamp,
  RequestStatusBadge,
  summarizeError,
  type RequestLogView,
} from './RequestDetail';

/**
 * 请求观测列表（T16）。
 *
 * 数据来自 T14：
 * - GET /admin/api/requests 支持 range/from/to、model、channel_id、status、
 *   error_code 与 limit/offset；服务端按 started_at 倒序并分页；
 * - 详情来自 GET /admin/api/requests/{id}，只含元数据与 attempts；
 * - 页面不请求也不展示任何 header/body/prompt；body 捕获入口由 T25 接入。
 */

type RangeKey = '24h' | '7d' | '30d';
type StatusValue = '' | 'success' | 'error' | 'cancelled';

const RANGE_OPTIONS = [
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
] as const satisfies ReadonlyArray<{ value: RangeKey; label: string }>;

const RANGE_LABEL: Record<RangeKey, string> = {
  '24h': '最近 24 小时',
  '7d': '最近 7 天',
  '30d': '最近 30 天',
};

const STATUS_OPTIONS = [
  { value: '', label: '全部状态' },
  { value: 'success', label: '成功' },
  { value: 'error', label: '失败' },
  { value: 'cancelled', label: '已取消' },
] as const satisfies ReadonlyArray<{ value: StatusValue; label: string }>;

/** PAGE_SIZE 与服务端默认上限（200）无关，页面固定 25 条一页。 */
const PAGE_SIZE = 25;

/** MAX_FILTER_LENGTH 与后端 maxFilterValueLength 对齐，避免提交必然 400 的值。 */
const MAX_FILTER_LENGTH = 256;

interface FilterState {
  range: RangeKey;
  model: string;
  channelID: string;
  status: StatusValue;
  errorCode: string;
}

const DEFAULT_FILTERS: FilterState = {
  range: '24h',
  model: '',
  channelID: '',
  status: '',
  errorCode: '',
};

/** ChannelOption 只取筛选下拉需要的字段，不涉及 key 或 base_url。 */
interface ChannelOption {
  id: number;
  name: string;
  protocol?: string;
  enabled?: boolean;
}

interface ChannelListResponse {
  channels?: ChannelOption[];
}

interface ModelsResponse {
  models?: string[];
}

interface RequestListResponse {
  requests?: RequestLogView[];
  total?: number;
  limit?: number;
  offset?: number;
}

interface RequestPage {
  requests: RequestLogView[];
  total: number;
  limit: number;
  offset: number;
}

function isRangeKey(value: string): value is RangeKey {
  return RANGE_OPTIONS.some((option) => option.value === value);
}

function isStatusValue(value: string): value is StatusValue {
  return STATUS_OPTIONS.some((option) => option.value === value);
}

function clipFilter(value: string): string {
  return value.trim().slice(0, MAX_FILTER_LENGTH);
}

/**
 * readInitialFilters 从当前 URL 恢复筛选。
 *
 * 总览的渠道/模型 Top 表用普通链接跳转到
 * /admin/requests?channel_id=…&model=…，所以初始筛选必须来自 query。
 */
function readInitialFilters(): FilterState {
  if (typeof window === 'undefined') {
    return DEFAULT_FILTERS;
  }
  const params = new URLSearchParams(window.location.search);

  const rawRange = params.get('range') ?? '';
  const range = isRangeKey(rawRange) ? rawRange : DEFAULT_FILTERS.range;

  const rawChannel = (params.get('channel_id') ?? '').trim();
  const channelID = /^[1-9]\d*$/.test(rawChannel) ? rawChannel : '';

  const rawStatus = params.get('status') ?? '';
  const status = isStatusValue(rawStatus) ? rawStatus : DEFAULT_FILTERS.status;

  return {
    range,
    model: clipFilter(params.get('model') ?? ''),
    channelID,
    status,
    errorCode: clipFilter(params.get('error_code') ?? ''),
  };
}

function buildListPath(filters: FilterState, offset: number): string {
  const params = new URLSearchParams();
  params.set('range', filters.range);
  if (filters.model !== '') {
    params.set('model', filters.model);
  }
  if (filters.channelID !== '') {
    params.set('channel_id', filters.channelID);
  }
  if (filters.status !== '') {
    params.set('status', filters.status);
  }
  if (filters.errorCode !== '') {
    params.set('error_code', filters.errorCode);
  }
  params.set('limit', String(PAGE_SIZE));
  params.set('offset', String(offset));
  return `/admin/api/requests?${params.toString()}`;
}

function normalizePage(payload: RequestListResponse | null | undefined): RequestPage {
  const requests = Array.isArray(payload?.requests) ? payload.requests : [];
  const total =
    typeof payload?.total === 'number' && Number.isFinite(payload.total) && payload.total >= 0
      ? payload.total
      : requests.length;
  return {
    requests,
    total,
    limit:
      typeof payload?.limit === 'number' && payload.limit > 0 ? payload.limit : PAGE_SIZE,
    offset:
      typeof payload?.offset === 'number' && payload.offset >= 0 ? payload.offset : 0,
  };
}

/** sortByStartedAtDesc 对当前页再做一次稳定倒序，保证展示顺序不依赖上游顺序。 */
function sortByStartedAtDesc(requests: RequestLogView[]): RequestLogView[] {
  return [...requests].sort((left, right) => {
    const leftTime = parseTimestamp(left.started_at)?.getTime() ?? 0;
    const rightTime = parseTimestamp(right.started_at)?.getTime() ?? 0;
    return rightTime - leftTime;
  });
}

function describeListError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 400) {
      return error.message || '筛选条件无效，请调整后重试。';
    }
    if (error.status === 429) {
      return '请求过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `加载请求日志失败（HTTP ${error.status}）`;
  }
  return '加载请求日志失败，请检查网络连接后重试。';
}

function hasActiveFilters(filters: FilterState): boolean {
  return (
    filters.range !== DEFAULT_FILTERS.range ||
    filters.model !== '' ||
    filters.channelID !== '' ||
    filters.status !== '' ||
    filters.errorCode !== ''
  );
}

function TimeCell({ value }: { value: string }): ReactNode {
  const formatted = formatTimestamp(value);
  if (formatted === '—') {
    return <span className="muted">—</span>;
  }
  return <time dateTime={value}>{formatted}</time>;
}

function ChannelCell({ request }: { request: RequestLogView }): ReactNode {
  const name =
    typeof request.channel_name === 'string' && request.channel_name.trim() !== ''
      ? request.channel_name
      : request.channel_id === null
        ? '未知渠道'
        : `渠道 #${request.channel_id}`;

  if (request.channel_id === null) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
        <span>{name}</span>
        <span className="badge badge--muted">已删除</span>
      </span>
    );
  }
  return <>{name}</>;
}

function TokenCell({ request }: { request: RequestLogView }): ReactNode {
  const inputMissing = request.input_tokens === null;
  const outputMissing = request.output_tokens === null;
  if (inputMissing && outputMissing) {
    return <span title="无 usage（不估算）">—</span>;
  }
  return (
    <span
      title={`输入 ${formatCount(request.input_tokens)} · 输出 ${formatCount(request.output_tokens)}`}
    >
      {formatCount(request.input_tokens)} / {formatCount(request.output_tokens)}
    </span>
  );
}

function ErrorCell({ request }: { request: RequestLogView }): ReactNode {
  const summary = summarizeError(request);
  if (summary === null) {
    return <span className="muted">—</span>;
  }
  return (
    <span
      className="form-error"
      title={summary}
      style={{
        display: 'inline-block',
        maxWidth: 240,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
        verticalAlign: 'bottom',
      }}
    >
      {summary}
    </span>
  );
}

const FILTER_GRID_STYLE: CSSProperties = {
  display: 'grid',
  gap: 12,
  gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))',
  alignItems: 'end',
};

const FILTER_ACTIONS_STYLE: CSSProperties = {
  gridColumn: '1 / -1',
  marginTop: 2,
};

/* -------------------------------------------------------------------------- */
/* 页面                                                                        */
/* -------------------------------------------------------------------------- */

export function RequestsPage(): ReactNode {
  const [draft, setDraft] = useState<FilterState>(readInitialFilters);
  const [applied, setApplied] = useState<FilterState>(readInitialFilters);
  const [offset, setOffset] = useState(0);

  const [page, setPage] = useState<RequestPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);

  const [channels, setChannels] = useState<ChannelOption[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [optionsError, setOptionsError] = useState<string | null>(null);
  const [optionsLoading, setOptionsLoading] = useState(true);
  const [optionsReload, setOptionsReload] = useState(0);

  const [selectedID, setSelectedID] = useState<string | null>(null);

  const sequence = useRef(0);
  const optionsSequence = useRef(0);

  const load = useCallback(async (filters: FilterState, nextOffset: number) => {
    sequence.current += 1;
    const current = sequence.current;

    setLoading(true);
    setLoadError(null);

    try {
      const payload = await apiRequest<RequestListResponse>(buildListPath(filters, nextOffset));
      if (sequence.current !== current) {
        return;
      }
      const normalized = normalizePage(payload);
      setPage(normalized);

      // 删除筛选或换页后，旧 offset 可能已经越界；回到最后一页而不是显示
      // 一个“空页”误导管理员。
      if (
        normalized.requests.length === 0 &&
        normalized.total > 0 &&
        nextOffset > 0
      ) {
        const lastOffset = Math.floor((normalized.total - 1) / PAGE_SIZE) * PAGE_SIZE;
        setOffset(lastOffset);
      }
    } catch (error) {
      if (sequence.current !== current) {
        return;
      }
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setLoadError(describeListError(error));
    } finally {
      if (sequence.current === current) {
        setLoading(false);
      }
    }
  }, []);

  // 请求列表的 effect 先声明，保证挂载后第一个 fetch 是 /admin/api/requests。
  useEffect(() => {
    void load(applied, offset);
  }, [load, applied, offset]);

  useEffect(() => {
    optionsSequence.current += 1;
    const current = optionsSequence.current;

    setOptionsLoading(true);
    setOptionsError(null);

    void Promise.allSettled([
      apiRequest<ChannelListResponse>('/admin/api/channels'),
      apiRequest<ModelsResponse>('/admin/api/models'),
    ]).then(([channelResult, modelResult]) => {
      if (optionsSequence.current !== current) {
        return;
      }

      const unauthorizedError = [channelResult, modelResult]
        .filter(
          (result): result is PromiseRejectedResult => result.status === 'rejected',
        )
        .find((result) => isUnauthorizedError(result.reason));
      if (unauthorizedError !== undefined) {
        setUnauthorized(true);
        return;
      }

      if (channelResult.status === 'fulfilled') {
        const list = channelResult.value.channels ?? [];
        setChannels(
          list
            .filter(
              (channel): channel is ChannelOption =>
                typeof channel?.id === 'number' && typeof channel?.name === 'string',
            )
            .sort((left, right) => left.name.localeCompare(right.name)),
        );
      }
      if (modelResult.status === 'fulfilled') {
        const list = modelResult.value.models ?? [];
        setModels(
          list
            .filter((model): model is string => typeof model === 'string' && model !== '')
            .sort((left, right) => left.localeCompare(right)),
        );
      }
      if (channelResult.status === 'rejected' || modelResult.status === 'rejected') {
        setOptionsError('筛选选项（渠道/模型）加载失败，可直接输入模型名后重试。');
      }
    }).finally(() => {
      if (optionsSequence.current === current) {
        setOptionsLoading(false);
      }
    });
  }, [optionsReload]);

  const handleApply = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const next: FilterState = {
      range: draft.range,
      model: clipFilter(draft.model),
      channelID: draft.channelID,
      status: draft.status,
      errorCode: clipFilter(draft.errorCode),
    };
    setApplied(next);
    setOffset(0);
  };

  const resetFilters = () => {
    setDraft(DEFAULT_FILTERS);
    setApplied(DEFAULT_FILTERS);
    setOffset(0);
  };

  const closeDetail = useCallback(() => {
    setSelectedID(null);
  }, []);

  const channelOptions = useMemo(() => {
    if (
      draft.channelID === '' ||
      channels.some((channel) => String(channel.id) === draft.channelID)
    ) {
      return channels;
    }
    // URL 指向的渠道可能已被删除：保留一个占位 option，避免 select 变空值。
    return [
      ...channels,
      { id: Number(draft.channelID), name: `渠道 #${draft.channelID}` },
    ];
  }, [channels, draft.channelID]);

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

  const rows = page === null ? [] : sortByStartedAtDesc(page.requests);
  const total = page?.total ?? 0;
  const activeFilters = hasActiveFilters(applied);
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const pageNumber = Math.floor(offset / PAGE_SIZE) + 1;
  const rangeStart = rows.length === 0 ? 0 : offset + 1;
  const rangeEnd = rows.length === 0 ? 0 : Math.min(offset + rows.length, total);
  const showInitialLoading = page === null || (loading && rows.length === 0);

  return (
    <>
      <header className="page-header">
        <div>
          <h2>请求观测</h2>
          <p>
            请求元数据、渠道尝试与错误摘要。默认只记录 token、延迟和错误；不记录 header、
            body 或 prompt。
          </p>
        </div>
        <div className="toolbar">
          <span className="badge">{RANGE_LABEL[applied.range]}</span>
          <span className="badge badge--muted">服务端分页 · 每页 {PAGE_SIZE} 条</span>
        </div>
      </header>

      <section className="panel" aria-labelledby="requests-filter-title">
        <div className="panel-header">
          <h2 id="requests-filter-title">筛选</h2>
          <span className="badge badge--muted">
            {loading ? '正在更新…' : `共 ${formatCount(total)} 条`}
          </span>
        </div>
        <div className="panel-body">
          <form
            aria-label="请求筛选"
            onSubmit={handleApply}
            noValidate
            style={FILTER_GRID_STYLE}
          >
            <div className="field">
              <label htmlFor="request-range">时间范围</label>
              <select
                id="request-range"
                className="input"
                name="range"
                value={draft.range}
                onChange={(event) => {
                  const value = event.target.value;
                  if (isRangeKey(value)) {
                    setDraft((current) => ({ ...current, range: value }));
                  }
                }}
              >
                {RANGE_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>

            <div className="field">
              <label htmlFor="request-model">模型</label>
              <input
                id="request-model"
                className="input"
                type="text"
                name="model"
                list="request-model-options"
                autoComplete="off"
                placeholder="全部模型"
                maxLength={MAX_FILTER_LENGTH}
                value={draft.model}
                onChange={(event) => {
                  const value = event.target.value;
                  setDraft((current) => ({ ...current, model: value }));
                }}
              />
              <datalist id="request-model-options">
                {models.map((model) => (
                  <option key={model} value={model} />
                ))}
              </datalist>
            </div>

            <div className="field">
              <label htmlFor="request-channel">渠道</label>
              <select
                id="request-channel"
                className="input"
                name="channel"
                value={draft.channelID}
                onChange={(event) => {
                  const value = event.target.value;
                  setDraft((current) => ({ ...current, channelID: value }));
                }}
              >
                <option value="">全部渠道</option>
                {channelOptions.map((channel) => (
                  <option key={channel.id} value={String(channel.id)}>
                    {channel.name}
                    {channel.enabled === false ? '（已禁用）' : ''}
                  </option>
                ))}
              </select>
            </div>

            <div className="field">
              <label htmlFor="request-status">状态</label>
              <select
                id="request-status"
                className="input"
                name="status"
                value={draft.status}
                onChange={(event) => {
                  const value = event.target.value;
                  if (isStatusValue(value)) {
                    setDraft((current) => ({ ...current, status: value }));
                  }
                }}
              >
                {STATUS_OPTIONS.map((option) => (
                  <option key={option.value || 'all'} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>

            <div className="field">
              <label htmlFor="request-error-code">错误码</label>
              <input
                id="request-error-code"
                className="input"
                type="text"
                name="error-code"
                autoComplete="off"
                placeholder="例如 upstream_timeout"
                maxLength={MAX_FILTER_LENGTH}
                value={draft.errorCode}
                onChange={(event) => {
                  const value = event.target.value;
                  setDraft((current) => ({ ...current, errorCode: value }));
                }}
              />
            </div>

            <div className="toolbar" style={FILTER_ACTIONS_STYLE}>
              <button type="submit" className="button">
                应用筛选
              </button>
              <button
                type="button"
                className="button button--ghost"
                onClick={resetFilters}
                disabled={!activeFilters && !hasActiveFilters(draft)}
              >
                重置
              </button>
            </div>
          </form>

          {optionsLoading ? (
            <p className="form-note" role="status" style={{ marginTop: 12 }}>
              正在加载筛选选项…
            </p>
          ) : optionsError === null ? null : (
            <p className="form-note" role="alert" style={{ marginTop: 12 }}>
              {optionsError}{' '}
              <button
                type="button"
                className="button button--ghost"
                onClick={() => {
                  setOptionsReload((current) => current + 1);
                }}
              >
                重试
              </button>
            </p>
          )}
        </div>
      </section>

      <section className="panel" aria-labelledby="requests-list-title" aria-busy={loading}>
        <div className="panel-header">
          <h2 id="requests-list-title">请求日志</h2>
          <div className="toolbar">
            <span className="badge">{RANGE_LABEL[applied.range]}</span>
            <span className="badge badge--muted">
              {page === null ? '—' : `共 ${formatCount(total)} 条`}
            </span>
          </div>
        </div>

        {loadError !== null ? (
          <div className="state state--error" role="alert">
            <span className="state-icon" aria-hidden="true">
              !
            </span>
            <h2>请求日志加载失败</h2>
            <p>{loadError}</p>
            <button
              type="button"
              className="button button--ghost"
              onClick={() => {
                void load(applied, offset);
              }}
            >
              重试
            </button>
          </div>
        ) : showInitialLoading ? (
          <p className="panel-body form-note" role="status">
            正在加载请求日志…
          </p>
        ) : rows.length === 0 ? (
          <div className="state state--empty">
            <span className="state-icon" aria-hidden="true">
              ◇
            </span>
            <h3>{activeFilters ? '没有符合筛选条件的请求' : '暂无请求记录'}</h3>
            <p>
              {activeFilters
                ? '调整时间范围、模型、渠道、状态或错误码后再试。'
                : '收到第一条请求后，这里会显示请求元数据、Token、延迟与错误摘要。'}
            </p>
            {activeFilters ? (
              <button type="button" className="button button--ghost" onClick={resetFilters}>
                清除筛选
              </button>
            ) : null}
          </div>
        ) : (
          <>
            <div className="table-wrap">
              <table>
                <caption className="sr-only">请求日志列表，按开始时间倒序</caption>
                <thead>
                  <tr>
                    <th scope="col">开始时间</th>
                    <th scope="col">请求</th>
                    <th scope="col">模型</th>
                    <th scope="col">渠道</th>
                    <th scope="col">状态</th>
                    <th scope="col">Token（输入/输出）</th>
                    <th scope="col">延迟</th>
                    <th scope="col">错误摘要</th>
                    <th scope="col">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((request) => (
                    <tr key={request.request_id}>
                      <td>
                        <TimeCell value={request.started_at} />
                      </td>
                      <td>
                        <span
                          style={{
                            display: 'inline-flex',
                            flexDirection: 'column',
                            gap: 2,
                          }}
                        >
                          <code>{request.request_id}</code>
                          <span className="muted" style={{ fontSize: 12 }}>
                            <code>{request.endpoint || '—'}</code>
                          </span>
                        </span>
                      </td>
                      <td>
                        <code>{request.model || '—'}</code>
                      </td>
                      <td>
                        <ChannelCell request={request} />
                      </td>
                      <td>
                        <RequestStatusBadge
                          status={request.status}
                          httpStatus={request.http_status}
                        />
                      </td>
                      <td>
                        <TokenCell request={request} />
                      </td>
                      <td>{formatMilliseconds(request.latency_ms)}</td>
                      <td>
                        <ErrorCell request={request} />
                      </td>
                      <td>
                        <button
                          type="button"
                          className="button button--ghost"
                          aria-label={`查看请求 ${request.request_id} 的详情`}
                          onClick={() => {
                            setSelectedID(request.request_id);
                          }}
                        >
                          详情
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div className="panel-body" style={{ borderTop: '1px solid var(--color-border)' }}>
              <div
                className="toolbar"
                style={{ justifyContent: 'space-between', alignItems: 'center' }}
              >
                <span className="form-note">
                  第 {formatCount(rangeStart)}–{formatCount(rangeEnd)} 条，共{' '}
                  {formatCount(total)} 条 · 第 {formatCount(pageNumber)} /{' '}
                  {formatCount(totalPages)} 页
                </span>
                <div className="toolbar" role="group" aria-label="请求分页">
                  <button
                    type="button"
                    className="button button--ghost"
                    disabled={offset === 0 || loading}
                    onClick={() => {
                      setOffset(Math.max(0, offset - PAGE_SIZE));
                    }}
                  >
                    上一页
                  </button>
                  <button
                    type="button"
                    className="button button--ghost"
                    disabled={offset + PAGE_SIZE >= total || loading}
                    onClick={() => {
                      setOffset(offset + PAGE_SIZE);
                    }}
                  >
                    下一页
                  </button>
                </div>
              </div>
            </div>
          </>
        )}
      </section>

      {selectedID === null ? null : (
        <RequestDetail requestID={selectedID} onClose={closeDetail} />
      )}
    </>
  );
}

export default RequestsPage;
