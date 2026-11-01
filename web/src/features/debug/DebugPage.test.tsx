import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import DebugPage, { type DebugTestResult } from './DebugPage';

const CHANNELS = [
  { id: 1, name: 'codex-primary', protocol: 'openai_responses', enabled: true },
  { id: 2, name: 'chat-backup', protocol: 'openai_chat', enabled: true },
];

const MODELS = ['gpt-5-codex', 'gpt-4.1-mini'];

const CHANNEL_RESULT: DebugTestResult = {
  status: 'healthy',
  http_status: 200,
  latency_ms: 128,
  channel_id: 1,
  channel_name: 'codex-primary',
  model: 'gpt-5-codex',
  upstream_model: 'gpt-5.1-codex',
  tested_at: '2026-10-30T10:00:00Z',
  request_link: '/admin/requests?channel_id=1&model=gpt-5-codex',
};

const MODEL_RESULT: DebugTestResult = {
  status: 'healthy',
  http_status: 200,
  latency_ms: 64,
  channel_id: 2,
  channel_name: 'chat-backup',
  model: 'gpt-4.1-mini',
  upstream_model: 'gpt-4-mini-upstream',
  tested_at: '2026-10-30T10:01:00Z',
  request_link: '/admin/requests?channel_id=2&model=gpt-4.1-mini',
};

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

function hasRequest(fetchMock: FetchMock, url: string, method: string): boolean {
  return fetchMock.mock.calls.some(
    ([input, init]) => requestURL(input) === url && (init?.method ?? 'GET') === method,
  );
}

function mockOptions(fetchMockOptions?: {
  channels?: unknown;
  models?: unknown;
}): (url: string, init: RequestInit) => Response | null {
  return (url, init) => {
    if (url === '/admin/api/channels' && (init.method ?? 'GET') === 'GET') {
      return jsonResponse(fetchMockOptions?.channels ?? { channels: CHANNELS });
    }
    if (url === '/admin/api/models' && (init.method ?? 'GET') === 'GET') {
      return jsonResponse(fetchMockOptions?.models ?? { models: MODELS });
    }
    return null;
  };
}

async function waitForLoadedOptions(): Promise<void> {
  await screen.findByRole('option', { name: /codex-primary/ });
  await screen.findAllByRole('option', { name: /gpt-5-codex/ });
  await waitFor(() => {
    expect(screen.getByLabelText('渠道')).toBeEnabled();
    expect(screen.getByLabelText('客户端模型')).toBeEnabled();
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

describe('调试页：渠道与模型测试', () => {
  it('加载渠道/模型选项并提示测试会消耗上游 tokens', async () => {
    const fetchMock = installFetch((url, init) => {
      const response = mockOptions()(url, init);
      if (response !== null) {
        return response;
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });

    render(<DebugPage />);

    await waitForLoadedOptions();
    expect(screen.getByRole('option', { name: /chat-backup/ })).toBeInTheDocument();
    expect(screen.getByText(/每次测试都会向上游发送最小请求/)).toBeInTheDocument();
    expect(screen.getByText(/消耗少量上游 tokens/)).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByRole('button', { name: '测试渠道' })).toBeEnabled();
      expect(screen.getByRole('button', { name: '测试模型' })).toBeEnabled();
    });
    expect(
      fetchMock.mock.calls.some(([input]) => requestURL(input) === '/admin/api/channels'),
    ).toBe(true);
    expect(
      fetchMock.mock.calls.some(([input]) => requestURL(input) === '/admin/api/models'),
    ).toBe(true);
    expect(hasRequest(fetchMock, '/admin/api/debug/channels/1/test', 'POST')).toBe(false);
  });

  it('渠道测试成功显示 200、延迟与最近请求链接', async () => {
    const fetchMock = installFetch((url, init) => {
      const response = mockOptions()(url, init);
      if (response !== null) {
        return response;
      }
      if (url === '/admin/api/debug/channels/1/test') {
        expect(init.method).toBe('POST');
        expect(JSON.parse(String(init.body))).toEqual({});
        return jsonResponse(CHANNEL_RESULT);
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<DebugPage />);

    await waitForLoadedOptions();
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.click(screen.getByRole('button', { name: '测试渠道' }));

    expect(await screen.findByText('128 ms')).toBeInTheDocument();
    expect(screen.getByText('200', { selector: 'code' })).toBeInTheDocument();
    expect(screen.getByText(/渠道测试 · 通过/)).toBeInTheDocument();
    expect(screen.getByText('gpt-5.1-codex')).toBeInTheDocument();

    const link = screen.getByRole('link', { name: '查看该渠道最近的请求' });
    expect(link).toHaveAttribute('href', CHANNEL_RESULT.request_link);
    expect(hasRequest(fetchMock, '/admin/api/debug/channels/1/test', 'POST')).toBe(true);
  });

  it('渠道测试失败显示脱敏错误，不展示上游 Key', async () => {
    const failed: DebugTestResult = {
      ...CHANNEL_RESULT,
      status: 'unhealthy',
      http_status: 401,
      latency_ms: 42,
      error: '上游返回 HTTP 401 Unauthorized',
    };
    installFetch((url, init) => {
      const response = mockOptions()(url, init);
      if (response !== null) {
        return response;
      }
      if (url === '/admin/api/debug/channels/1/test') {
        return jsonResponse(failed);
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    const { container } = render(<DebugPage />);

    await waitForLoadedOptions();
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.click(screen.getByRole('button', { name: '测试渠道' }));

    expect(
      await screen.findByText('上游返回 HTTP 401 Unauthorized'),
    ).toBeInTheDocument();
    expect(screen.getByText(/失败/)).toBeInTheDocument();
    expect(screen.getByText('42 ms')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '查看该渠道最近的请求' })).toHaveAttribute(
      'href',
      CHANNEL_RESULT.request_link,
    );
    expect(container.textContent).not.toContain('sk-live');
  });

  it('可选模型用于渠道测试时提交 model 字段', async () => {
    const fetchMock = installFetch((url, init) => {
      const response = mockOptions()(url, init);
      if (response !== null) {
        return response;
      }
      if (url === '/admin/api/debug/channels/1/test') {
        expect(JSON.parse(String(init.body))).toEqual({ model: 'gpt-4.1-mini' });
        return jsonResponse({
          ...CHANNEL_RESULT,
          model: 'gpt-4.1-mini',
          upstream_model: 'gpt-4-mini-upstream',
        });
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<DebugPage />);

    await waitForLoadedOptions();
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.selectOptions(screen.getByLabelText('模型（可选）'), 'gpt-4.1-mini');
    await user.click(screen.getByRole('button', { name: '测试渠道' }));

    expect(await screen.findByText('gpt-4-mini-upstream')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/debug/channels/1/test', 'POST')).toBe(true);
  });

  it('模型测试只提交所选模型并展示候选渠道', async () => {
    const fetchMock = installFetch((url, init) => {
      const response = mockOptions()(url, init);
      if (response !== null) {
        return response;
      }
      if (url === '/admin/api/debug/models/gpt-4.1-mini/test') {
        expect(init.method).toBe('POST');
        expect(init.body).toBeUndefined();
        return jsonResponse(MODEL_RESULT);
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<DebugPage />);

    await waitForLoadedOptions();
    await user.selectOptions(screen.getByLabelText('客户端模型'), 'gpt-4.1-mini');
    await user.click(screen.getByRole('button', { name: '测试模型' }));

    expect(await screen.findByText('64 ms')).toBeInTheDocument();
    expect(screen.getByText('gpt-4-mini-upstream')).toBeInTheDocument();
    expect(screen.getByText(/模型测试 · 通过/)).toBeInTheDocument();

    const link = screen.getByRole('link', { name: '查看该渠道最近的请求' });
    expect(link).toHaveAttribute('href', MODEL_RESULT.request_link);
    expect(hasRequest(fetchMock, '/admin/api/debug/models/gpt-4.1-mini/test', 'POST')).toBe(
      true,
    );
  });

  it('预算截断显示不确定而非通过或失败，并展示生成上下文', async () => {
    installFetch((url, init) => mockOptions()(url, init) ?? jsonResponse({
      ...CHANNEL_RESULT,
      status: 'inconclusive',
      error_code: 'output_limit',
      error: '输出预算耗尽',
      duration_ms: 720,
      protocol: 'openai_responses',
      connection_revision: 4,
      kind: 'generation',
    }));
    const user = userEvent.setup();
    render(<DebugPage />);
    await waitForLoadedOptions();
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.click(screen.getByRole('button', { name: '测试渠道' }));
    expect(await screen.findByText('inconclusive')).toBeInTheDocument();
    expect(screen.getByText(/渠道测试 · 不确定/)).toBeInTheDocument();
    expect(screen.queryByText('unhealthy')).not.toBeInTheDocument();
    expect(screen.getByText('720 ms')).toBeInTheDocument();
    expect(screen.getByText('128 ms')).toBeInTheDocument();
    expect(screen.getByText('output_limit')).toBeInTheDocument();
    expect(screen.getByText('openai_responses', { selector: 'code' })).toBeInTheDocument();
    expect(screen.getByText('v4')).toBeInTheDocument();
    expect(screen.getByText(/不代表通过或失败/)).toBeInTheDocument();
    expect(screen.getByText(/不代表全部模型或全部能力/)).toBeInTheDocument();
  });

  it('无映射模型返回明确错误', async () => {
    installFetch((url, init) => {
      const response = mockOptions()(url, init);
      if (response !== null) {
        return response;
      }
      if (url === '/admin/api/debug/models/gpt-4.1-mini/test') {
        return jsonResponse(
          {
            error: 'model_not_available',
            message: '该模型没有启用的映射渠道，无法测试',
          },
          400,
        );
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<DebugPage />);

    await waitForLoadedOptions();
    await user.selectOptions(screen.getByLabelText('客户端模型'), 'gpt-4.1-mini');
    await user.click(screen.getByRole('button', { name: '测试模型' }));

    expect(
      await screen.findByText('该模型没有启用的映射渠道，无法测试'),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('link', { name: '查看该渠道最近的请求' }),
    ).not.toBeInTheDocument();
  });

  it('会话失效时跳转登录且不渲染调试内容', async () => {
    installFetch(() =>
      jsonResponse({ error: 'unauthorized', message: '需要管理员登录' }, 401),
    );

    render(<DebugPage />);

    expect(await screen.findByText('登录状态已失效')).toBeInTheDocument();
    expect(redirectSpy).toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: '测试渠道' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: '查看该渠道最近的请求' })).not.toBeInTheDocument();
  });

  it('选项加载失败显示重试入口', async () => {
    installFetch(() =>
      jsonResponse({ error: 'internal_error', message: '服务器内部错误' }, 500),
    );

    render(<DebugPage />);

    expect(
      await screen.findByText(/渠道或模型列表加载失败/),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '测试渠道' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '测试模型' })).toBeDisabled();
  });
});
