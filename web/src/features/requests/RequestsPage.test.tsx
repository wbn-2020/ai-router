import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import RequestsPage from './RequestsPage';
import type { RequestAttemptView, RequestDetailResponse, RequestLogView } from './RequestDetail';

/* -------------------------------------------------------------------------- */
/* 测试数据                                                                    */
/* -------------------------------------------------------------------------- */

interface ChannelFixture {
  id: number;
  name: string;
  protocol: string;
  priority: number;
  enabled: boolean;
}

const CHANNELS: ChannelFixture[] = [
  { id: 7, name: 'codex-primary', protocol: 'openai_responses', priority: 0, enabled: true },
  { id: 8, name: 'codex-backup', protocol: 'openai_responses', priority: 10, enabled: true },
];

const MODELS = ['gpt-5-codex', 'gpt-5-chat'];

/** extraFields 模拟后端若意外附带敏感字段时前端仍不得渲染。 */
const EXTRA_SENSITIVE_FIELDS = {
  request_body: 'SECRET_PROMPT_TEXT',
  request_headers: { authorization: 'Bearer secret-token' },
  prompt: 'SECRET_PROMPT_TEXT',
};

function log(
  overrides: Partial<RequestLogView> & Pick<RequestLogView, 'request_id'>,
): RequestLogView {
  return {
    protocol: 'openai_responses',
    endpoint: '/v1/responses',
    model: 'gpt-5-codex',
    channel_id: 7,
    channel_name: 'codex-primary',
    status: 'success',
    http_status: 200,
    error_code: null,
    error_message: null,
    stream: true,
    attempt_count: 1,
    retry_count: 0,
    ttft_ms: 420,
    latency_ms: 1200,
    input_tokens: 1200,
    output_tokens: 340,
    total_tokens: 1540,
    usage_source: 'responses.completed',
    semantic_committed: true,
    response_bytes: 8192,
    started_at: '2026-10-30T10:00:00Z',
    finished_at: '2026-10-30T10:00:01.2Z',
    ...overrides,
  };
}

const LOG_OK = {
  ...log({ request_id: 'req_ok', started_at: '2026-10-30T10:00:00Z' }),
  ...EXTRA_SENSITIVE_FIELDS,
};

const LOG_ERROR = {
  ...log({
    request_id: 'req_error',
    status: 'error',
    http_status: 502,
    error_code: 'upstream_bad_gateway',
    error_message: '上游响应异常（已脱敏）',
    ttft_ms: null,
    latency_ms: 2400,
    input_tokens: null,
    output_tokens: null,
    total_tokens: null,
    usage_source: null,
    semantic_committed: false,
    attempt_count: 2,
    retry_count: 1,
    started_at: '2026-10-30T09:00:00Z',
    finished_at: '2026-10-30T09:00:02.4Z',
  }),
  ...EXTRA_SENSITIVE_FIELDS,
};

const LOG_CANCELLED = {
  ...log({
    request_id: 'req_cancelled',
    status: 'cancelled',
    http_status: 499,
    ttft_ms: null,
    latency_ms: 180,
    input_tokens: null,
    output_tokens: null,
    total_tokens: null,
    usage_source: null,
    semantic_committed: false,
    channel_id: null,
    channel_name: 'retired-channel',
    started_at: '2026-10-30T08:00:00Z',
    finished_at: '2026-10-30T08:00:00.18Z',
  }),
  ...EXTRA_SENSITIVE_FIELDS,
};

const LOG_PAGE_2 = {
  ...log({
    request_id: 'req_page2',
    input_tokens: 80,
    output_tokens: 20,
    total_tokens: 100,
    latency_ms: 300,
    started_at: '2026-10-30T07:00:00Z',
    finished_at: '2026-10-30T07:00:00.3Z',
  }),
  ...EXTRA_SENSITIVE_FIELDS,
};

function attempt(
  overrides: Partial<RequestAttemptView> & Pick<RequestAttemptView, 'attempt_index'>,
): RequestAttemptView {
  return {
    channel_id: 7,
    channel_name: 'codex-primary',
    upstream_model: 'gpt-5.1-codex',
    status: 'success',
    http_status: 200,
    error_code: null,
    error_message: null,
    latency_ms: 900,
    semantic_committed: true,
    started_at: '2026-10-30T09:00:00Z',
    finished_at: '2026-10-30T09:00:00.9Z',
    ...overrides,
  };
}

const ERROR_DETAIL: RequestDetailResponse = {
  request: LOG_ERROR,
  attempts: [
    attempt({
      attempt_index: 1,
      channel_name: 'codex-primary',
      status: 'error',
      http_status: 429,
      error_code: 'upstream_rate_limited',
      error_message: '上游限流，请稍后重试',
      latency_ms: 210,
      semantic_committed: false,
    }),
    attempt({
      attempt_index: 2,
      channel_id: 8,
      channel_name: 'codex-backup',
      status: 'error',
      http_status: 503,
      error_code: 'upstream_unavailable',
      error_message: '上游暂不可用',
      latency_ms: 900,
      semantic_committed: false,
    }),
  ],
};

const OK_DETAIL: RequestDetailResponse = {
  request: LOG_OK,
  attempts: [
    attempt({
      attempt_index: 1,
      latency_ms: 900,
      semantic_committed: true,
    }),
  ],
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

interface ApiOptions {
  list?: (params: URLSearchParams) => Response;
  detail?: (requestID: string) => Response;
  channels?: ChannelFixture[];
  models?: string[];
}

function installApi(options: ApiOptions = {}) {
  const details: Record<string, RequestDetailResponse> = {
    req_ok: OK_DETAIL,
    req_error: ERROR_DETAIL,
  };

  return installFetch((url) => {
    const parsed = new URL(url, 'http://localhost');

    if (parsed.pathname === '/admin/api/channels') {
      return jsonResponse({ channels: options.channels ?? CHANNELS });
    }
    if (parsed.pathname === '/admin/api/models') {
      return jsonResponse({ models: options.models ?? MODELS });
    }
    if (parsed.pathname === '/admin/api/requests') {
      if (options.list !== undefined) {
        return options.list(parsed.searchParams);
      }
      return jsonResponse({
        requests: [LOG_CANCELLED, LOG_OK, LOG_ERROR],
        total: 60,
        limit: 25,
        offset: Number(parsed.searchParams.get('offset') ?? '0'),
      });
    }
    if (parsed.pathname.startsWith('/admin/api/requests/')) {
      const requestID = decodeURIComponent(
        parsed.pathname.slice('/admin/api/requests/'.length),
      );
      if (options.detail !== undefined) {
        return options.detail(requestID);
      }
      const detail = details[requestID];
      if (detail === undefined) {
        return jsonResponse({ error: 'request_not_found', message: '请求日志不存在' }, 404);
      }
      return jsonResponse(detail);
    }
    throw new Error(`未预期的请求：${url}`);
  });
}

function listCalls(fetchMock: FetchMock): URLSearchParams[] {
  return fetchMock.mock.calls
    .map(([input]) => new URL(requestURL(input), 'http://localhost'))
    .filter((parsed) => parsed.pathname === '/admin/api/requests')
    .map((parsed) => parsed.searchParams);
}

function detailCalls(fetchMock: FetchMock): string[] {
  return fetchMock.mock.calls
    .map(([input]) => new URL(requestURL(input), 'http://localhost'))
    .filter((parsed) => parsed.pathname.startsWith('/admin/api/requests/'))
    .map((parsed) => decodeURIComponent(parsed.pathname));
}

let redirectSpy = vi.fn();

beforeEach(() => {
  window.history.replaceState({}, '', '/admin/');
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

describe('请求观测列表', () => {
  it('默认加载最近 24 小时并按时间倒序展示元数据，不渲染 body/header', async () => {
    const fetchMock = installApi();

    render(<RequestsPage />);

    const table = await screen.findByRole('table');
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(3);

    const rowTexts = rows.map((row) => row.textContent ?? '');
    expect(rowTexts[0]).toContain('req_ok');
    expect(rowTexts[1]).toContain('req_error');
    expect(rowTexts[2]).toContain('req_cancelled');

    expect(within(rows[0]!).getByText(/成功/)).toBeInTheDocument();
    expect(within(rows[1]!).getByText(/失败/)).toBeInTheDocument();
    expect(within(rows[1]!).getByText(/upstream_bad_gateway/)).toBeInTheDocument();
    expect(within(rows[2]!).getByText(/已取消/)).toBeInTheDocument();

    // usage 缺失显示 —，而不是 0。
    expect(within(rows[2]!).getByTitle('无 usage（不估算）')).toHaveTextContent('—');
    expect(within(rows[0]!).getByText('1,200 / 340')).toBeInTheDocument();

    // 敏感字段即使被后端误带也不会进入 DOM。
    expect(document.body.textContent).not.toContain('SECRET_PROMPT_TEXT');
    expect(document.body.textContent).not.toContain('Bearer secret-token');

    const calls = listCalls(fetchMock);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.get('range')).toBe('24h');
    expect(calls[0]!.get('limit')).toBe('25');
    expect(calls[0]!.get('offset')).toBe('0');
    expect(calls[0]!.has('model')).toBe(false);
    expect(calls[0]!.has('channel_id')).toBe(false);
    expect(calls[0]!.has('status')).toBe(false);
    expect(calls[0]!.has('error_code')).toBe(false);

    // 列表只请求元数据端点，不请求详情或捕获端点。
    expect(detailCalls(fetchMock)).toHaveLength(0);
  });

  it('从 URL 恢复总览跳转带来的模型与渠道筛选', async () => {
    window.history.replaceState(
      {},
      '',
      '/admin/requests?channel_id=7&model=gpt-5-codex&status=error&error_code=upstream_timeout',
    );
    const fetchMock = installApi();

    render(<RequestsPage />);

    await screen.findByRole('table');

    const calls = listCalls(fetchMock);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.get('range')).toBe('24h');
    expect(calls[0]!.get('model')).toBe('gpt-5-codex');
    expect(calls[0]!.get('channel_id')).toBe('7');
    expect(calls[0]!.get('status')).toBe('error');
    expect(calls[0]!.get('error_code')).toBe('upstream_timeout');

    await waitFor(() => {
      expect(screen.getByLabelText('渠道')).toHaveValue('7');
    });
    expect(screen.getByLabelText('模型')).toHaveValue('gpt-5-codex');
    expect(screen.getByLabelText('状态')).toHaveValue('error');
    expect(screen.getByLabelText('错误码')).toHaveValue('upstream_timeout');
  });

  it('提交筛选后请求带对应 query 参数并重置分页', async () => {
    const fetchMock = installApi();
    const user = userEvent.setup();

    render(<RequestsPage />);
    await screen.findByRole('table');

    await screen.findByRole('option', { name: 'codex-primary' });

    await user.selectOptions(screen.getByLabelText('时间范围'), '7d');
    await user.type(screen.getByLabelText('模型'), 'gpt-5-codex');
    await user.selectOptions(screen.getByLabelText('渠道'), '7');
    await user.selectOptions(screen.getByLabelText('状态'), 'error');
    await user.type(screen.getByLabelText('错误码'), 'upstream_timeout');
    await user.click(screen.getByRole('button', { name: '应用筛选' }));

    await waitFor(() => {
      expect(listCalls(fetchMock)).toHaveLength(2);
    });

    const params = listCalls(fetchMock)[1]!;
    expect(params.get('range')).toBe('7d');
    expect(params.get('model')).toBe('gpt-5-codex');
    expect(params.get('channel_id')).toBe('7');
    expect(params.get('status')).toBe('error');
    expect(params.get('error_code')).toBe('upstream_timeout');
    expect(params.get('limit')).toBe('25');
    expect(params.get('offset')).toBe('0');
  });

  it('翻页使用服务端 limit/offset，并展示当前页范围', async () => {
    const fetchMock = installApi({
      list: (params) => {
        const offset = Number(params.get('offset') ?? '0');
        return jsonResponse({
          requests: offset === 0 ? [LOG_CANCELLED, LOG_OK, LOG_ERROR] : [LOG_PAGE_2],
          total: 60,
          limit: 25,
          offset,
        });
      },
    });
    const user = userEvent.setup();

    render(<RequestsPage />);
    await screen.findByRole('table');

    expect(screen.getByText(/第 1 \/ 3 页/)).toBeInTheDocument();
    expect(screen.getByText(/第 1–3 条，共 60 条/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: '下一页' }));

    expect(await screen.findByText('req_page2')).toBeInTheDocument();
    await waitFor(() => {
      expect(listCalls(fetchMock)).toHaveLength(2);
    });
    expect(listCalls(fetchMock)[1]!.get('offset')).toBe('25');
    expect(screen.getByText(/第 2 \/ 3 页/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '上一页' }));

    expect(await screen.findByText('req_ok')).toBeInTheDocument();
    await waitFor(() => {
      expect(listCalls(fetchMock)).toHaveLength(3);
    });
    expect(listCalls(fetchMock)[2]!.get('offset')).toBe('0');
  });

  it('无数据展示空态，而不是空白表格', async () => {
    installApi({
      list: () =>
        jsonResponse({ requests: [], total: 0, limit: 25, offset: 0 }),
    });

    render(<RequestsPage />);

    expect(await screen.findByText('暂无请求记录')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.queryByText('正在加载请求日志…')).not.toBeInTheDocument();
  });

  it('筛选后无结果展示空态并支持清除筛选', async () => {
    window.history.replaceState({}, '', '/admin/requests?channel_id=7');
    const fetchMock = installApi({
      list: (params) => {
        if (params.get('channel_id') === '7') {
          return jsonResponse({ requests: [], total: 0, limit: 25, offset: 0 });
        }
        return jsonResponse({
          requests: [LOG_OK],
          total: 1,
          limit: 25,
          offset: 0,
        });
      },
    });
    const user = userEvent.setup();

    render(<RequestsPage />);

    expect(await screen.findByText('没有符合筛选条件的请求')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '清除筛选' }));

    expect(await screen.findByText('req_ok')).toBeInTheDocument();
    const params = listCalls(fetchMock)[1]!;
    expect(params.has('channel_id')).toBe(false);
    expect(params.get('range')).toBe('24h');
  });

  it('加载失败显示可重试错误，重试成功后恢复表格', async () => {
    let calls = 0;
    installApi({
      list: () => {
        calls += 1;
        if (calls === 1) {
          return jsonResponse({ error: 'internal_error', message: '服务器内部错误' }, 500);
        }
        return jsonResponse({ requests: [LOG_OK], total: 1, limit: 25, offset: 0 });
      },
    });
    const user = userEvent.setup();

    render(<RequestsPage />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('请求日志加载失败');
    expect(
      within(alert).getByText('服务器暂时不可用，请稍后重试。'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    await user.click(within(alert).getByRole('button', { name: '重试' }));

    expect(await screen.findByText('req_ok')).toBeInTheDocument();
    expect(calls).toBe(2);
  });
});

describe('请求详情抽屉', () => {
  async function openDetail(requestID: string) {
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole('button', {
        name: `查看请求 ${requestID} 的详情`,
      }),
    );
    return screen.findByRole('dialog', { name: '请求详情' });
  }

  it('展示每次 attempt 的渠道、状态、延迟与错误，且不显示 body/header', async () => {
    const fetchMock = installApi();
    render(<RequestsPage />);
    await screen.findByRole('table');

    const dialog = await openDetail('req_error');

    expect(await within(dialog).findByText('请求元数据')).toBeInTheDocument();
    expect(within(dialog).getAllByText('req_error').length).toBeGreaterThanOrEqual(1);
    expect(
      within(dialog).getByText(/upstream_bad_gateway：上游响应异常（已脱敏）/),
    ).toBeInTheDocument();

    // usage 缺失时全部显示 —，并明确说明不估算。
    expect(
      within(dialog).getByText('上游未返回 usage，显示 —，不估算。'),
    ).toBeInTheDocument();

    expect(within(dialog).getByText('渠道尝试')).toBeInTheDocument();
    expect(within(dialog).getAllByText('codex-primary').length).toBeGreaterThanOrEqual(1);
    expect(within(dialog).getAllByText('codex-backup').length).toBeGreaterThanOrEqual(1);
    expect(within(dialog).getByText(/upstream_rate_limited/)).toBeInTheDocument();
    expect(within(dialog).getByText(/upstream_unavailable/)).toBeInTheDocument();
    expect(within(dialog).getByText('210 ms')).toBeInTheDocument();
    expect(within(dialog).getByText('900 ms')).toBeInTheDocument();
    expect(
      within(dialog).getAllByText('gpt-5.1-codex').length,
    ).toBeGreaterThanOrEqual(1);

    // 首内容已提交：请求级与 attempt 级都展示。
    expect(within(dialog).getAllByText('否').length).toBeGreaterThanOrEqual(2);

    // 只显示元数据；body/header 绝不渲染。
    expect(within(dialog).getByText(/内存诊断环/)).toBeInTheDocument();
    expect(dialog.textContent).not.toContain('SECRET_PROMPT_TEXT');
    expect(dialog.textContent).not.toContain('Bearer secret-token');
    expect(dialog.textContent).not.toContain('request_body');
    expect(dialog.textContent).not.toContain('request_headers');

    expect(detailCalls(fetchMock)).toEqual(['/admin/api/requests/req_error']);

    await userEvent.setup().click(
      within(dialog).getByRole('button', { name: '关闭' }),
    );
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
  });

  it('成功请求显示 token 与 usage 来源，且没有错误摘要', async () => {
    installApi();
    render(<RequestsPage />);
    await screen.findByRole('table');

    const dialog = await openDetail('req_ok');

    expect(await within(dialog).findByText('1,540')).toBeInTheDocument();
    expect(within(dialog).getByText('1,200')).toBeInTheDocument();
    expect(within(dialog).getByText('340')).toBeInTheDocument();
    expect(within(dialog).getByText('来源 responses.completed')).toBeInTheDocument();
    expect(
      within(dialog).queryByRole('heading', { name: '错误摘要' }),
    ).not.toBeInTheDocument();
    expect(within(dialog).getAllByText('是').length).toBeGreaterThanOrEqual(2);
  });

  it('详情加载失败时可重试', async () => {
    let detailAttempts = 0;
    installApi({
      detail: (requestID) => {
        detailAttempts += 1;
        if (detailAttempts === 1) {
          return jsonResponse({ error: 'internal_error', message: '服务器内部错误' }, 500);
        }
        return jsonResponse(
          requestID === 'req_ok'
            ? OK_DETAIL
            : { error: 'request_not_found', message: '请求日志不存在' },
          requestID === 'req_ok' ? 200 : 404,
        );
      },
    });
    render(<RequestsPage />);
    await screen.findByRole('table');

    const dialog = await openDetail('req_ok');

    const alert = await within(dialog).findByRole('alert');
    expect(alert).toHaveTextContent('请求详情加载失败');
    expect(
      within(alert).getByText('服务器暂时不可用，请稍后重试。'),
    ).toBeInTheDocument();

    await userEvent.setup().click(within(alert).getByRole('button', { name: '重试' }));

    expect(await within(dialog).findByText('请求元数据')).toBeInTheDocument();
    expect(within(dialog).getByText('1,540')).toBeInTheDocument();
    expect(detailAttempts).toBe(2);
  });
});
