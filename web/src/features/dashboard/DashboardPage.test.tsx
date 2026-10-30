import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import DashboardPage from './DashboardPage';

/* -------------------------------------------------------------------------- */
/* 测试数据                                                                    */
/* -------------------------------------------------------------------------- */

interface StatsPointFixture {
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

function point(
  overrides: Partial<StatsPointFixture> & Pick<StatsPointFixture, 'request_count'>,
): StatsPointFixture {
  return {
    success_count: overrides.request_count,
    error_count: 0,
    error_rate: 0,
    input_tokens: null,
    output_tokens: null,
    total_tokens: null,
    usage_count: 0,
    avg_latency_ms: null,
    p95_latency_ms: null,
    ...overrides,
  };
}

const STATS_24H = {
  from: '2026-10-29T09:00:00Z',
  to: '2026-10-30T09:00:00Z',
  totals: point({
    request_count: 42,
    success_count: 40,
    error_count: 2,
    error_rate: 2 / 42,
    input_tokens: 12000,
    output_tokens: 3400,
    total_tokens: 15400,
    usage_count: 40,
    avg_latency_ms: 820.4,
    p95_latency_ms: 2100,
  }),
  hourly: [
    point({
      bucket: '2026-10-30T07:00:00Z',
      request_count: 10,
      input_tokens: 3000,
      output_tokens: 600,
      total_tokens: 3600,
      usage_count: 10,
      avg_latency_ms: 500,
      p95_latency_ms: 700,
    }),
    point({
      bucket: '2026-10-30T08:00:00Z',
      request_count: 20,
      success_count: 18,
      error_count: 2,
      error_rate: 0.1,
      input_tokens: 6000,
      output_tokens: 1700,
      total_tokens: 7700,
      usage_count: 20,
      avg_latency_ms: 900,
      p95_latency_ms: 2100,
    }),
    point({
      bucket: '2026-10-30T09:00:00Z',
      request_count: 12,
      input_tokens: 3000,
      output_tokens: 1100,
      total_tokens: 4100,
      usage_count: 10,
      avg_latency_ms: 700,
      p95_latency_ms: 1200,
    }),
  ],
  by_model: [
    point({
      model: 'gpt-5-codex',
      request_count: 30,
      success_count: 29,
      error_count: 1,
      error_rate: 1 / 30,
      total_tokens: 12000,
      usage_count: 30,
      p95_latency_ms: 2000,
    }),
    point({
      model: 'gpt-5-chat',
      request_count: 12,
      success_count: 11,
      error_count: 1,
      error_rate: 1 / 12,
      total_tokens: 3400,
      usage_count: 10,
      p95_latency_ms: 900,
    }),
  ],
  by_channel: [
    point({
      channel_id: 7,
      channel_name: 'codex-primary',
      request_count: 30,
      success_count: 29,
      error_count: 1,
      error_rate: 1 / 30,
      total_tokens: 12000,
      usage_count: 30,
      p95_latency_ms: 2000,
    }),
    point({
      channel_name: 'retired-channel',
      request_count: 12,
      success_count: 11,
      error_count: 1,
      error_rate: 1 / 12,
      total_tokens: 3400,
      usage_count: 10,
      p95_latency_ms: 900,
    }),
  ],
};

const EMPTY_STATS = {
  from: '2026-10-29T09:00:00Z',
  to: '2026-10-30T09:00:00Z',
  totals: point({ request_count: 0 }),
  hourly: [],
  by_model: [],
  by_channel: [],
};

const NO_USAGE_STATS = {
  from: '2026-10-29T09:00:00Z',
  to: '2026-10-30T09:00:00Z',
  totals: point({
    request_count: 5,
    success_count: 3,
    error_count: 2,
    error_rate: 0.4,
    avg_latency_ms: 120,
  }),
  hourly: [
    point({
      bucket: '2026-10-30T08:00:00Z',
      request_count: 5,
      success_count: 3,
      error_count: 2,
      error_rate: 0.4,
      avg_latency_ms: 120,
    }),
  ],
  by_model: [],
  by_channel: [],
};

/* -------------------------------------------------------------------------- */
/* fetch 测试替身                                                              */
/* -------------------------------------------------------------------------- */

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response;
}

function requestURL(input: RequestInfo | URL): string {
  if (typeof input === 'string') {
    return input;
  }
  if (input instanceof URL) {
    return input.toString();
  }
  return input.url;
}

function installFetch(handler: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) =>
    handler(requestURL(input), init ?? {}),
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

type FetchMock = ReturnType<typeof installFetch>;

function statsCalls(fetchMock: FetchMock): string[] {
  return fetchMock.mock.calls
    .map(([input]) => requestURL(input))
    .filter((url) => url.startsWith('/admin/api/stats'));
}

function hasStatsRequest(fetchMock: FetchMock, range: string): boolean {
  return statsCalls(fetchMock).includes(`/admin/api/stats?range=${range}`);
}

function installStatsFetch(body: unknown, status = 200) {
  return installFetch((url) => {
    if (url.startsWith('/admin/api/stats?')) {
      return jsonResponse(body, status);
    }
    throw new Error(`未预期的请求：${url}`);
  });
}

let redirectSpy = vi.fn();

beforeEach(() => {
  redirectSpy = vi.fn();
  setLoginRedirect(redirectSpy);
});

afterEach(() => {
  cleanup();
  setLoginRedirect(null);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/* -------------------------------------------------------------------------- */
/* 测试                                                                        */
/* -------------------------------------------------------------------------- */

describe('总览 Dashboard', () => {
  it('默认按 24h 加载，渲染四张指标卡、趋势图与渠道/模型 Top 表', async () => {
    const fetchMock = installStatsFetch(STATS_24H);

    render(<DashboardPage />);

    const metrics = await screen.findByLabelText('核心指标');
    expect(within(metrics).getByText('请求数')).toBeInTheDocument();
    expect(within(metrics).getByText('42')).toBeInTheDocument();
    expect(within(metrics).getByText('成功 40 · 失败 2')).toBeInTheDocument();

    expect(within(metrics).getByText('Token 用量')).toBeInTheDocument();
    expect(within(metrics).getByText('15,400')).toBeInTheDocument();
    expect(within(metrics).getByText('输入 12,000 · 输出 3,400')).toBeInTheDocument();

    expect(within(metrics).getByText('平均 / P95 延迟')).toBeInTheDocument();
    expect(within(metrics).getByText('820 ms')).toBeInTheDocument();
    expect(within(metrics).getByText('P95 2,100 ms')).toBeInTheDocument();

    expect(within(metrics).getByText('错误率')).toBeInTheDocument();
    expect(within(metrics).getByText('4.76%')).toBeInTheDocument();

    const chart = screen.getByRole('img', { name: /按小时请求趋势/ });
    expect(chart.getAttribute('aria-label')).toContain('共 42 次请求，2 次错误，3 个小时桶');
    expect(chart.querySelectorAll('rect').length).toBeGreaterThanOrEqual(3);

    expect(screen.getByRole('link', { name: 'codex-primary' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'gpt-5-codex' })).toBeInTheDocument();
    expect(screen.getByText('retired-channel')).toBeInTheDocument();
    expect(screen.getByText('已删除')).toBeInTheDocument();

    expect(screen.getByRole('button', { name: '24 小时' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    expect(hasStatsRequest(fetchMock, '24h')).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith(
      '/admin/api/stats?range=24h',
      expect.objectContaining({ credentials: 'include', cache: 'no-store' }),
    );
  });

  it('切换 7 天/30 天时带新的 range 参数重新请求', async () => {
    const fetchMock = installStatsFetch(STATS_24H);
    const user = userEvent.setup();

    render(<DashboardPage />);

    await screen.findByLabelText('核心指标');
    expect(statsCalls(fetchMock)).toEqual(['/admin/api/stats?range=24h']);
    await user.click(screen.getByRole('button', { name: '7 天' }));

    await waitFor(() => {
      expect(hasStatsRequest(fetchMock, '7d')).toBe(true);
    });
    expect(await screen.findByText('最近 7 天')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '7 天' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );

    await user.click(screen.getByRole('button', { name: '30 天' }));

    await waitFor(() => {
      expect(hasStatsRequest(fetchMock, '30d')).toBe(true);
    });
    expect(await screen.findByText('最近 30 天')).toBeInTheDocument();
    expect(statsCalls(fetchMock)).toEqual([
      '/admin/api/stats?range=24h',
      '/admin/api/stats?range=7d',
      '/admin/api/stats?range=30d',
    ]);
  });

  it('空数据展示空态与占位符，不出现 NaN', async () => {
    installStatsFetch(EMPTY_STATS);

    const { container } = render(<DashboardPage />);

    expect(await screen.findByText('当前时间范围暂无统计数据')).toBeInTheDocument();
    expect(screen.getByText('暂无渠道统计')).toBeInTheDocument();
    expect(screen.getByText('暂无模型统计')).toBeInTheDocument();

    const metrics = screen.getByLabelText('核心指标');
    expect(within(metrics).getByText('0')).toBeInTheDocument();
    expect(within(metrics).getAllByText('—')).toHaveLength(3);
    expect(within(metrics).getByText('无 usage 数据（不估算）')).toBeInTheDocument();
    expect(within(metrics).getByText('暂无延迟数据')).toBeInTheDocument();

    expect(screen.queryByRole('img')).not.toBeInTheDocument();
    expect(container.textContent).not.toContain('NaN');
  });

  it('统计加载失败显示可重试错误，重试成功后恢复指标卡', async () => {
    let calls = 0;
    const fetchMock = installFetch((url) => {
      if (!url.startsWith('/admin/api/stats?')) {
        throw new Error(`未预期的请求：${url}`);
      }
      calls += 1;
      if (calls === 1) {
        return jsonResponse({ error: 'internal_error', message: '服务器内部错误' }, 500);
      }
      return jsonResponse(STATS_24H);
    });
    const user = userEvent.setup();

    render(<DashboardPage />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('统计数据加载失败');
    expect(within(alert).getByText('服务器暂时不可用，请稍后重试。')).toBeInTheDocument();
    expect(screen.queryByLabelText('核心指标')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '重试' }));

    const metrics = await screen.findByRole('region', { name: '核心指标' });
    expect(within(metrics).getByText('42')).toBeInTheDocument();
    expect(calls).toBe(2);
    expect(hasStatsRequest(fetchMock, '24h')).toBe(true);
  });

  it('usage 缺失时 Token 与 P95 显示占位符，不估算为 0', async () => {
    installStatsFetch(NO_USAGE_STATS);

    const { container } = render(<DashboardPage />);

    const metrics = await screen.findByRole('region', { name: '核心指标' });
    expect(within(metrics).getByText('无 usage 数据（不估算）')).toBeInTheDocument();
    expect(within(metrics).getByText('120 ms')).toBeInTheDocument();
    expect(within(metrics).getByText('P95 —')).toBeInTheDocument();
    expect(within(metrics).getByText('40.00%')).toBeInTheDocument();
    expect(container.textContent).not.toContain('NaN');
  });

  it('渠道/模型 Top 表链接到请求页并带上对应筛选参数', async () => {
    installStatsFetch(STATS_24H);

    render(<DashboardPage />);

    const channelLink = await screen.findByRole('link', { name: 'codex-primary' });
    expect(channelLink).toHaveAttribute('href', '/admin/requests?channel_id=7');

    const modelLink = screen.getByRole('link', { name: 'gpt-5-codex' });
    expect(modelLink).toHaveAttribute('href', '/admin/requests?model=gpt-5-codex');

    // 已删除渠道只保留名称快照，没有可用的 channel_id，不生成跳转链接。
    expect(screen.queryByRole('link', { name: /retired-channel/ })).not.toBeInTheDocument();
  });
});
