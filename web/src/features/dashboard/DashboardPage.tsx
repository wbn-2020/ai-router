import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';

import { ApiError, apiRequest, isUnauthorizedError } from '../../lib/api';
import { HourlyTrendChart, type HourlyTrendPoint } from './charts';

/**
 * 总览 Dashboard（T15）。
 *
 * 数据全部来自 T14 的 GET /admin/api/stats：
 * - 时间范围只接受 24h / 7d / 30d，切换范围就带着新的 range 参数重新请求；
 * - 只展示元数据聚合结果；usage 缺失时显示 “—” 而不是 0；
 * - 统计失败显示可重试错误，不渲染空白页；空数据展示空态而不是 NaN。
 */

type RangeKey = '24h' | '7d' | '30d';

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

/** TOP_LIMIT 是渠道/模型 Top 表的展示行数。 */
const TOP_LIMIT = 5;

const NUMBER_FORMATTER = new Intl.NumberFormat('zh-CN');

/** StatsPoint 镜像 internal/adminapi 的 statsPoint；维度字段按分组类型出现。 */
interface StatsPoint {
  bucket?: string;
  model?: string;
  channel_id?: number | null;
  channel_name?: string;

  request_count: number;
  success_count: number;
  error_count: number;
  error_rate: number | null;
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  usage_count: number;
  avg_latency_ms: number | null;
  p95_latency_ms: number | null;
}

interface StatsResponse {
  from: string;
  to: string;
  totals: StatsPoint;
  hourly: StatsPoint[];
  by_model: StatsPoint[];
  by_channel: StatsPoint[];
}

const EMPTY_TOTALS: StatsPoint = {
  request_count: 0,
  success_count: 0,
  error_count: 0,
  error_rate: null,
  input_tokens: null,
  output_tokens: null,
  total_tokens: null,
  usage_count: 0,
  avg_latency_ms: null,
  p95_latency_ms: null,
};

function normalizeStats(payload: StatsResponse): StatsResponse {
  return {
    from: payload.from,
    to: payload.to,
    totals: payload.totals ?? EMPTY_TOTALS,
    hourly: Array.isArray(payload.hourly) ? payload.hourly : [],
    by_model: Array.isArray(payload.by_model) ? payload.by_model : [],
    by_channel: Array.isArray(payload.by_channel) ? payload.by_channel : [],
  };
}

function describeStatsError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 429) {
      return '统计请求过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `加载统计数据失败（HTTP ${error.status}）`;
  }
  return '加载统计数据失败，请检查网络连接后重试。';
}

/* -------------------------------------------------------------------------- */
/* 格式化                                                                      */
/* -------------------------------------------------------------------------- */

function formatCount(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return '—';
  }
  return NUMBER_FORMATTER.format(value);
}

function formatMilliseconds(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return '—';
  }
  return `${NUMBER_FORMATTER.format(Math.round(value))} ms`;
}

function formatErrorRate(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return '—';
  }
  return `${(value * 100).toFixed(2)}%`;
}

/**
 * knownTotalTokens 只汇总已知值：total 缺失时，只有输入与输出都已知才求和，
 * 不把单个未知字段当成 0 伪装成完整总量。
 */
function knownTotalTokens(point: StatsPoint): number | null {
  if (typeof point.total_tokens === 'number' && Number.isFinite(point.total_tokens)) {
    return point.total_tokens;
  }
  if (
    typeof point.input_tokens === 'number' &&
    Number.isFinite(point.input_tokens) &&
    typeof point.output_tokens === 'number' &&
    Number.isFinite(point.output_tokens)
  ) {
    return point.input_tokens + point.output_tokens;
  }
  return null;
}

function tokenHint(point: StatsPoint): string {
  if (
    point.input_tokens === null &&
    point.output_tokens === null &&
    point.total_tokens === null
  ) {
    return '无 usage 数据（不估算）';
  }
  return `输入 ${formatCount(point.input_tokens)} · 输出 ${formatCount(point.output_tokens)}`;
}

function latencyHint(point: StatsPoint): string {
  if (point.avg_latency_ms === null && point.p95_latency_ms === null) {
    return '暂无延迟数据';
  }
  return `P95 ${formatMilliseconds(point.p95_latency_ms)}`;
}

function topByRequestCount(points: StatsPoint[]): StatsPoint[] {
  const count = (point: StatsPoint): number =>
    typeof point.request_count === 'number' && Number.isFinite(point.request_count)
      ? point.request_count
      : 0;
  return [...points].sort((left, right) => count(right) - count(left)).slice(0, TOP_LIMIT);
}

/* -------------------------------------------------------------------------- */
/* 指标卡                                                                      */
/* -------------------------------------------------------------------------- */

function MetricCards({ totals }: { totals: StatsPoint }) {
  return (
    <section className="metric-grid" aria-label="核心指标">
      <article className="metric-card">
        <p className="metric-label">请求数</p>
        <p className="metric-value">{formatCount(totals.request_count)}</p>
        {/* 后端的 error_count 包含 error 与 cancelled，这里统称为失败。 */}
        <p className="metric-hint">
          成功 {formatCount(totals.success_count)} · 失败 {formatCount(totals.error_count)}
        </p>
      </article>

      <article className="metric-card">
        <p className="metric-label">Token 用量</p>
        <p className="metric-value">{formatCount(knownTotalTokens(totals))}</p>
        <p className="metric-hint">{tokenHint(totals)}</p>
      </article>

      <article className="metric-card">
        <p className="metric-label">平均 / P95 延迟</p>
        <p className="metric-value">{formatMilliseconds(totals.avg_latency_ms)}</p>
        <p className="metric-hint">{latencyHint(totals)}</p>
      </article>

      <article className="metric-card">
        <p className="metric-label">错误率</p>
        <p className="metric-value">
          {formatErrorRate(totals.request_count > 0 ? totals.error_rate : null)}
        </p>
        <p className="metric-hint">
          失败 {formatCount(totals.error_count)} 次 · 共 {formatCount(totals.request_count)} 次
        </p>
      </article>
    </section>
  );
}

/* -------------------------------------------------------------------------- */
/* 趋势与 Top 表                                                               */
/* -------------------------------------------------------------------------- */

function EmptyPanelState({ title, description }: { title: string; description: string }) {
  return (
    <div className="state state--empty">
      <span className="state-icon" aria-hidden="true">
        ◇
      </span>
      <h3>{title}</h3>
      <p>{description}</p>
    </div>
  );
}

function TrendPanel({ hourly, range }: { hourly: StatsPoint[]; range: RangeKey }) {
  const points: HourlyTrendPoint[] = hourly.map((point) => ({
    bucket: typeof point.bucket === 'string' ? point.bucket : '',
    request_count: point.request_count,
    success_count: point.success_count,
    error_count: point.error_count,
  }));

  const totalRequests = points.reduce(
    (sum, point) => sum + (Number.isFinite(point.request_count) ? point.request_count : 0),
    0,
  );
  const totalErrors = points.reduce(
    (sum, point) => sum + (Number.isFinite(point.error_count) ? point.error_count : 0),
    0,
  );

  return (
    <section className="panel" aria-labelledby="dashboard-trend-title">
      <div className="panel-header">
        <h2 id="dashboard-trend-title">请求趋势</h2>
        <div className="toolbar">
          <span className="badge">{RANGE_LABEL[range]}</span>
          {points.length > 0 ? (
            <span className="badge badge--muted">
              共 {formatCount(totalRequests)} 次请求 · {formatCount(totalErrors)} 次错误
            </span>
          ) : null}
        </div>
      </div>
      {points.length === 0 ? (
        <EmptyPanelState
          title="当前时间范围暂无统计数据"
          description="收到第一条请求后，这里会展示按小时聚合的请求量与错误数。"
        />
      ) : (
        <div className="panel-body">
          <HourlyTrendChart points={points} />
        </div>
      )}
    </section>
  );
}

function ChannelNameCell({ point }: { point: StatsPoint }) {
  const name =
    typeof point.channel_name === 'string' && point.channel_name.trim() !== ''
      ? point.channel_name
      : '未知渠道';

  if (typeof point.channel_id === 'number' && Number.isFinite(point.channel_id)) {
    // 用普通链接而不是 Router Link：总览不依赖路由上下文即可渲染与测试，
    // 整页跳转后请求页仍能从 URL 的 channel_id 恢复筛选。
    return (
      <a
        href={`/admin/requests?channel_id=${encodeURIComponent(String(point.channel_id))}`}
        title={`在请求观测中查看 ${name} 的请求`}
      >
        {name}
      </a>
    );
  }

  // 渠道删除后 channel_id 为 null，历史统计仍保留 channel_name 快照。
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
      <span>{name}</span>
      <span className="badge badge--muted">已删除</span>
    </span>
  );
}

function ModelNameCell({ point }: { point: StatsPoint }) {
  const model = typeof point.model === 'string' ? point.model.trim() : '';
  if (model === '') {
    return <span className="muted">未知模型</span>;
  }
  return (
    <a
      href={`/admin/requests?model=${encodeURIComponent(model)}`}
      title={`在请求观测中查看 ${model} 的请求`}
    >
      {model}
    </a>
  );
}

function TopChannelsPanel({ points }: { points: StatsPoint[] }) {
  const rows = topByRequestCount(points);
  return (
    <section className="panel" aria-labelledby="dashboard-channels-title">
      <div className="panel-header">
        <h2 id="dashboard-channels-title">渠道 Top</h2>
        <span className="badge badge--muted">按请求数排序</span>
      </div>
      {rows.length === 0 ? (
        <EmptyPanelState
          title="暂无渠道统计"
          description="当前时间范围内没有可聚合的渠道请求。"
        />
      ) : (
        <div className="table-wrap">
          <table>
            <caption className="sr-only">按渠道聚合的请求统计，按请求数降序</caption>
            <thead>
              <tr>
                <th scope="col">渠道</th>
                <th scope="col">请求数</th>
                <th scope="col">错误率</th>
                <th scope="col">P95 延迟</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => (
                <tr key={`channel-${row.channel_id ?? 'deleted'}-${index}`}>
                  <td>
                    <ChannelNameCell point={row} />
                  </td>
                  <td>{formatCount(row.request_count)}</td>
                  <td>{formatErrorRate(row.error_rate)}</td>
                  <td>{formatMilliseconds(row.p95_latency_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function TopModelsPanel({ points }: { points: StatsPoint[] }) {
  const rows = topByRequestCount(points);
  return (
    <section className="panel" aria-labelledby="dashboard-models-title">
      <div className="panel-header">
        <h2 id="dashboard-models-title">模型 Top</h2>
        <span className="badge badge--muted">按请求数排序</span>
      </div>
      {rows.length === 0 ? (
        <EmptyPanelState title="暂无模型统计" description="当前时间范围内没有可聚合的模型请求。" />
      ) : (
        <div className="table-wrap">
          <table>
            <caption className="sr-only">按客户端模型聚合的请求统计，按请求数降序</caption>
            <thead>
              <tr>
                <th scope="col">模型</th>
                <th scope="col">请求数</th>
                <th scope="col">错误率</th>
                <th scope="col">P95 延迟</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => (
                <tr key={`model-${row.model ?? 'unknown'}-${index}`}>
                  <td>
                    <ModelNameCell point={row} />
                  </td>
                  <td>{formatCount(row.request_count)}</td>
                  <td>{formatErrorRate(row.error_rate)}</td>
                  <td>{formatMilliseconds(row.p95_latency_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

/* -------------------------------------------------------------------------- */
/* 页面                                                                        */
/* -------------------------------------------------------------------------- */

export function DashboardPage(): ReactNode {
  const [range, setRange] = useState<RangeKey>('24h');
  const [stats, setStats] = useState<StatsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);
  const sequence = useRef(0);

  const load = useCallback(async (nextRange: RangeKey) => {
    sequence.current += 1;
    const current = sequence.current;

    setLoading(true);
    setError(null);
    // 切换范围时清空旧数据，避免把 24h 数字挂在“最近 7 天”的标签下。
    setStats(null);

    try {
      const response = await apiRequest<StatsResponse>(`/admin/api/stats?range=${nextRange}`);
      if (sequence.current !== current) {
        return;
      }
      setStats(normalizeStats(response));
    } catch (loadError) {
      if (sequence.current !== current) {
        return;
      }
      if (isUnauthorizedError(loadError)) {
        setUnauthorized(true);
        return;
      }
      setError(describeStatsError(loadError));
    } finally {
      if (sequence.current === current) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    void load(range);
  }, [load, range]);

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

  return (
    <>
      <header className="page-header">
        <div>
          <h2>总览</h2>
          <p>
            {RANGE_LABEL[range]}的请求数、Token、延迟与错误率；统计只来自请求元数据，
            usage 缺失时不做估算。
          </p>
        </div>
        <div className="toolbar" role="group" aria-label="时间范围">
          {RANGE_OPTIONS.map((option) => (
            <button
              key={option.value}
              type="button"
              className={option.value === range ? 'button' : 'button button--ghost'}
              aria-pressed={option.value === range}
              onClick={() => {
                setRange(option.value);
              }}
            >
              {option.label}
            </button>
          ))}
        </div>
      </header>

      {error !== null ? (
        <section className="panel">
          <div className="state state--error" role="alert">
            <span className="state-icon" aria-hidden="true">
              !
            </span>
            <h2>统计数据加载失败</h2>
            <p>{error}</p>
            <button
              type="button"
              className="button button--ghost"
              onClick={() => {
                void load(range);
              }}
            >
              重试
            </button>
          </div>
        </section>
      ) : stats === null ? (
        <section className="panel" aria-busy={loading}>
          <p className="panel-body form-note" role="status">
            正在加载统计数据…
          </p>
        </section>
      ) : (
        <>
          <MetricCards totals={stats.totals} />
          <TrendPanel hourly={stats.hourly} range={range} />
          <TopChannelsPanel points={stats.by_channel} />
          <TopModelsPanel points={stats.by_model} />
        </>
      )}
    </>
  );
}

export default DashboardPage;
