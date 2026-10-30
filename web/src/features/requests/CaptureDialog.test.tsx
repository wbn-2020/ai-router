import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import CaptureDialog, {
  type CaptureReadResponse,
  type CaptureStatusResponse,
} from './CaptureDialog';
import { RequestDetail, type RequestDetailResponse } from './RequestDetail';

/* -------------------------------------------------------------------------- */
/* 测试数据                                                                    */
/* -------------------------------------------------------------------------- */

const REQUEST_ID = 'req-1';
const STATUS_PATH = `/admin/api/requests/${REQUEST_ID}/capture/status`;
const CAPTURE_PATH = `/admin/api/requests/${REQUEST_ID}/capture`;
const DETAIL_PATH = `/admin/api/requests/${REQUEST_ID}`;

const STATUS_AVAILABLE: CaptureStatusResponse = {
  request_id: REQUEST_ID,
  available: true,
  can_save: true,
  saved: false,
  request_truncated: false,
  response_truncated: false,
  request_bytes: 64,
  response_bytes: 512,
  captured_at: '2026-10-30T10:00:00Z',
  expires_at: '2026-10-30T10:05:00Z',
};

const STATUS_EXPIRED: CaptureStatusResponse = {
  request_id: REQUEST_ID,
  available: false,
  can_save: false,
  saved: false,
  request_truncated: false,
  response_truncated: false,
  request_bytes: null,
  response_bytes: null,
};

const STATUS_SAVED: CaptureStatusResponse = {
  request_id: REQUEST_ID,
  available: false,
  can_save: false,
  saved: true,
  request_truncated: false,
  response_truncated: true,
  request_bytes: 64,
  response_bytes: 8 << 20,
  saved_expires_at: '2026-10-31T10:00:00Z',
};

const CONTENT: CaptureReadResponse = {
  request_id: REQUEST_ID,
  request_body: '{"model":"gpt-5-codex","input":"hello"}',
  response_body: 'data: {"type":"response.completed","response":{"id":"resp_1"}}\n\n',
  request_truncated: false,
  response_truncated: true,
  request_bytes: 64,
  response_bytes: 8 << 20,
  created_at: '2026-10-30T10:00:02Z',
  expires_at: '2026-10-31T10:00:02Z',
};

const CONTENT_HTML: CaptureReadResponse = {
  request_id: REQUEST_ID,
  request_body:
    '<script>window.__captureXss = true</script><img src="x" onerror="window.__captureXss = true"><b>not bold</b>',
  response_body: '<iframe src="https://evil.example"></iframe><div>plain response</div>',
  request_truncated: false,
  response_truncated: false,
  request_bytes: 120,
  response_bytes: 60,
  created_at: '2026-10-30T10:00:02Z',
  expires_at: '2026-10-31T10:00:02Z',
};

const DETAIL: RequestDetailResponse = {
  request: {
    request_id: REQUEST_ID,
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
    ttft_ms: 120,
    latency_ms: 860,
    input_tokens: 1200,
    output_tokens: 340,
    total_tokens: 1540,
    usage_source: 'responses.completed',
    semantic_committed: true,
    response_bytes: 4096,
    started_at: '2026-10-30T10:00:00Z',
    finished_at: '2026-10-30T10:00:00.86Z',
  },
  attempts: [],
};

/* -------------------------------------------------------------------------- */
/* fetch 测试替身                                                              */
/* -------------------------------------------------------------------------- */

interface RecordedCall {
  path: string;
  method: string;
  body: string | null;
}

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response;
}

function emptyResponse(status: number): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers(),
    text: async () => '',
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

function installFetch(handler: (call: RecordedCall) => Response) {
  const calls: RecordedCall[] = [];
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const path = new URL(requestURL(input), 'http://localhost').pathname;
    const method = (init?.method ?? 'GET').toUpperCase();
    const body = typeof init?.body === 'string' ? init.body : null;
    const call: RecordedCall = { path, method, body };
    calls.push(call);
    return handler(call);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { fetchMock, calls };
}

function callsFor(calls: RecordedCall[], path: string, method: string): RecordedCall[] {
  return calls.filter((call) => call.path === path && call.method === method);
}

interface CaptureBackendOptions {
  status: CaptureStatusResponse;
  content: CaptureReadResponse;
}

function installCaptureBackend(options: CaptureBackendOptions) {
  let deleted = false;
  return installFetch(({ path, method }) => {
    if (path === STATUS_PATH && method === 'GET') {
      if (deleted) {
        return jsonResponse({ ...options.status, saved: false, available: false, can_save: false });
      }
      return jsonResponse(options.status);
    }
    if (path === CAPTURE_PATH && method === 'POST') {
      return jsonResponse(
        {
          request_id: REQUEST_ID,
          saved: true,
          request_truncated: options.content.request_truncated,
          response_truncated: options.content.response_truncated,
          request_bytes: options.content.request_bytes,
          response_bytes: options.content.response_bytes,
          created_at: options.content.created_at,
          expires_at: options.content.expires_at,
        },
        201,
      );
    }
    if (path === CAPTURE_PATH && method === 'GET') {
      if (deleted) {
        return jsonResponse(
          { error: 'capture_not_found', message: '捕获不存在或已过期' },
          404,
        );
      }
      return jsonResponse(options.content);
    }
    if (path === CAPTURE_PATH && method === 'DELETE') {
      deleted = true;
      return emptyResponse(204);
    }
    throw new Error(`未预期的请求：${method} ${path}`);
  });
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
  delete (URL as unknown as Record<string, unknown>).createObjectURL;
  delete (URL as unknown as Record<string, unknown>).revokeObjectURL;
});

/* -------------------------------------------------------------------------- */
/* 测试                                                                        */
/* -------------------------------------------------------------------------- */

describe('Body 捕获对话框', () => {
  it('诊断环可用时：未确认不保存，确认后保存并展示纯文本、截断、复制与下载', async () => {
    const user = userEvent.setup();
    const { calls } = installCaptureBackend({
      status: STATUS_AVAILABLE,
      content: CONTENT,
    });

    const createObjectURL = vi.fn(() => 'blob:capture');
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: createObjectURL,
    });
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: revokeObjectURL,
    });
    const anchorClick = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(() => {});

    render(<CaptureDialog requestID={REQUEST_ID} onClose={vi.fn()} />);

    const dialog = await screen.findByRole('dialog', { name: 'Body 捕获' });

    expect(await within(dialog).findByText('保存前请确认风险')).toBeInTheDocument();
    expect(within(dialog).getByText(/完整 prompt、对话历史与上游响应正文/)).toBeInTheDocument();
    expect(within(dialog).getByText(/24 小时后自动删除/)).toBeInTheDocument();
    expect(within(dialog).getByText(/不保存 header、Authorization 或 Cookie/)).toBeInTheDocument();

    // 只查询了状态；打开面板与展示风险提示都不会调用保存接口。
    expect(callsFor(calls, STATUS_PATH, 'GET')).toHaveLength(1);
    expect(callsFor(calls, CAPTURE_PATH, 'POST')).toHaveLength(0);
    expect(within(dialog).queryByTestId('capture-request-body')).not.toBeInTheDocument();

    await user.click(
      within(dialog).getByRole('button', { name: '确认风险并抓取 body' }),
    );

    await waitFor(() => {
      expect(callsFor(calls, CAPTURE_PATH, 'POST')).toHaveLength(1);
    });
    expect(JSON.parse(callsFor(calls, CAPTURE_PATH, 'POST')[0]!.body ?? '{}')).toEqual({
      confirm_risk: true,
    });

    const requestBody = await within(dialog).findByTestId('capture-request-body');
    const responseBody = within(dialog).getByTestId('capture-response-body');
    expect(requestBody).toHaveTextContent(CONTENT.request_body);
    expect(responseBody).toHaveTextContent('response.completed');

    // 响应超过 8 MiB：只提示截断，不伪造完整内容。
    expect(
      within(dialog).getByText(/响应 Body 超过单侧 8 MiB 上限，仅保存了前 8 MiB（已截断）。/),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/24 小时后/)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: '复制请求 Body' }));
    expect(await within(dialog).findByText('已复制请求 Body。')).toBeInTheDocument();
    expect(await navigator.clipboard.readText()).toBe(CONTENT.request_body);

    await user.click(within(dialog).getByRole('button', { name: '下载响应 Body' }));
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(anchorClick).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:capture');
    expect(within(dialog).getByText('已开始下载响应 Body。')).toBeInTheDocument();
  });

  it('诊断环已过期：说明原因且不显示抓取按钮，也不调用保存接口', async () => {
    const { calls } = installCaptureBackend({
      status: STATUS_EXPIRED,
      content: CONTENT,
    });

    render(<CaptureDialog requestID={REQUEST_ID} onClose={vi.fn()} />);

    const dialog = await screen.findByRole('dialog', { name: 'Body 捕获' });
    expect(await within(dialog).findByText('body 捕获已过期')).toBeInTheDocument();
    expect(within(dialog).getByText(/5 分钟内保留 body/)).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: /抓取 body/ })).not.toBeInTheDocument();
    expect(within(dialog).queryByTestId('capture-request-body')).not.toBeInTheDocument();

    expect(callsFor(calls, STATUS_PATH, 'GET')).toHaveLength(1);
    expect(callsFor(calls, CAPTURE_PATH, 'POST')).toHaveLength(0);
    expect(callsFor(calls, CAPTURE_PATH, 'GET')).toHaveLength(0);
  });

  it('已保存时直接读取内容；删除后回到不可用状态', async () => {
    const user = userEvent.setup();
    const { calls } = installCaptureBackend({
      status: STATUS_SAVED,
      content: CONTENT,
    });

    render(<CaptureDialog requestID={REQUEST_ID} onClose={vi.fn()} />);

    const dialog = await screen.findByRole('dialog', { name: 'Body 捕获' });
    expect(await within(dialog).findByTestId('capture-request-body')).toHaveTextContent(
      CONTENT.request_body,
    );
    expect(within(dialog).getByText('已保存')).toBeInTheDocument();
    expect(callsFor(calls, CAPTURE_PATH, 'GET')).toHaveLength(1);

    await user.click(within(dialog).getByRole('button', { name: '删除捕获' }));
    expect(within(dialog).getByText('删除后无法恢复，确认删除该捕获？')).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: '确认删除' }));

    expect(await within(dialog).findByText('捕获已删除')).toBeInTheDocument();
    expect(callsFor(calls, CAPTURE_PATH, 'DELETE')).toHaveLength(1);
    expect(within(dialog).queryByTestId('capture-request-body')).not.toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: /抓取 body/ })).not.toBeInTheDocument();
    expect(within(dialog).getByText('已删除')).toBeInTheDocument();
  });

  it('请求与响应同时截断时都显示提示', async () => {
    installCaptureBackend({
      status: STATUS_SAVED,
      content: {
        ...CONTENT,
        request_truncated: true,
        response_truncated: true,
        request_bytes: 8 << 20,
        response_bytes: 8 << 20,
      },
    });

    render(<CaptureDialog requestID={REQUEST_ID} onClose={vi.fn()} />);

    const dialog = await screen.findByRole('dialog', { name: 'Body 捕获' });
    expect(
      await within(dialog).findByText(/请求 Body 超过单侧 8 MiB 上限/),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/响应 Body 超过单侧 8 MiB 上限/),
    ).toBeInTheDocument();
    expect(within(dialog).getAllByText(/已截断/)).toHaveLength(2);
  });

  it('body 以纯文本展示，不执行其中的 HTML 或脚本', async () => {
    installCaptureBackend({
      status: STATUS_SAVED,
      content: CONTENT_HTML,
    });

    render(<CaptureDialog requestID={REQUEST_ID} onClose={vi.fn()} />);

    const dialog = await screen.findByRole('dialog', { name: 'Body 捕获' });
    const requestBody = await within(dialog).findByTestId('capture-request-body');

    expect(requestBody).toHaveTextContent('<script>');
    expect(requestBody).toHaveTextContent('<img src="x" onerror=');
    expect(requestBody).toHaveTextContent('<b>not bold</b>');
    expect(within(dialog).queryByText('not bold', { exact: true })).not.toBeInTheDocument();
    expect(dialog.querySelector('script')).toBeNull();
    expect(dialog.querySelector('img')).toBeNull();
    expect(dialog.querySelector('iframe')).toBeNull();
    expect(
      (window as unknown as Record<string, unknown>).__captureXss,
    ).toBeUndefined();
  });

  it('请求详情默认不请求捕获：打开面板、确认风险后才保存并展示内容', async () => {
    const user = userEvent.setup();
    let saved = false;
    const { calls } = installFetch(({ path, method }) => {
      if (path === DETAIL_PATH && method === 'GET') {
        return jsonResponse(DETAIL);
      }
      if (path === STATUS_PATH && method === 'GET') {
        return jsonResponse(STATUS_AVAILABLE);
      }
      if (path === CAPTURE_PATH && method === 'POST') {
        saved = true;
        return jsonResponse({ request_id: REQUEST_ID, saved: true }, 201);
      }
      if (path === CAPTURE_PATH && method === 'GET' && saved) {
        return jsonResponse(CONTENT);
      }
      throw new Error(`未预期的请求：${method} ${path}`);
    });

    render(<RequestDetail requestID={REQUEST_ID} onClose={vi.fn()} />);

    expect(await screen.findByText('请求元数据')).toBeInTheDocument();
    expect(screen.queryByText(CONTENT.request_body)).not.toBeInTheDocument();
    // 默认详情只加载元数据，不查询诊断环，也不出现抓取按钮。
    expect(callsFor(calls, STATUS_PATH, 'GET')).toHaveLength(0);
    expect(screen.queryByRole('button', { name: /抓取 body/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '打开 Body 捕获面板' }));

    const capture = await screen.findByRole('dialog', { name: 'Body 捕获' });
    expect(callsFor(calls, STATUS_PATH, 'GET')).toHaveLength(1);
    expect(callsFor(calls, CAPTURE_PATH, 'POST')).toHaveLength(0);

    await user.click(
      within(capture).getByRole('button', { name: '确认风险并抓取 body' }),
    );

    expect(await within(capture).findByTestId('capture-request-body')).toHaveTextContent(
      CONTENT.request_body,
    );
    expect(callsFor(calls, CAPTURE_PATH, 'POST')).toHaveLength(1);

    // 关闭捕获面板后请求详情仍然打开；body 回到不可见状态。
    await user.click(within(capture).getByRole('button', { name: '关闭 Body 捕获' }));
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'Body 捕获' })).not.toBeInTheDocument();
    });
    expect(screen.getByRole('dialog', { name: '请求详情' })).toBeInTheDocument();
    expect(screen.queryByTestId('capture-request-body')).not.toBeInTheDocument();
  });
});
