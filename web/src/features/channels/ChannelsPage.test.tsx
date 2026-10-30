import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import ChannelsPage from './ChannelsPage';
import type { Channel, ChannelInput, ChannelPatch } from './types';

const SECRET_PRIMARY = 'sk-live-PRIMARY-0123456789abcdef';
const SECRET_BACKUP = 'sk-live-BACKUP-fedcba9876543210';
const SECRET_ROTATED = 'sk-live-ROTATED-abcdefghijkl';

const CREATED_AT = '2026-10-30T08:00:00Z';
const UPDATED_AT = '2026-10-30T09:15:00.123456789Z';

/** maskKey 与 internal/secret.MaskKey 的 3 前缀 + 4 后缀规则一致。 */
function maskKey(key: string): string {
  if (key.length < 11) {
    return '*'.repeat(key.length);
  }
  return `${key.slice(0, 3)}${'*'.repeat(key.length - 7)}${key.slice(-4)}`;
}

const PRIMARY: Channel = {
  id: 1,
  name: 'codex-primary',
  base_url: 'https://codex.example.com/v1',
  protocol: 'openai_responses',
  key_mask: maskKey(SECRET_PRIMARY),
  priority: 10,
  max_concurrency: 4,
  enabled: true,
  last_health_status: 'healthy',
  last_health_latency_ms: 128,
  last_health_at: '2026-10-30T09:00:00Z',
  created_at: CREATED_AT,
  updated_at: UPDATED_AT,
};

const BACKUP: Channel = {
  id: 2,
  name: 'chat-backup',
  base_url: 'https://chat.example.com/v1?api-version=2026-01-01',
  protocol: 'openai_chat',
  key_mask: maskKey(SECRET_BACKUP),
  priority: 20,
  max_concurrency: 2,
  enabled: false,
  last_health_status: 'unhealthy',
  last_health_latency_ms: 42,
  last_error: '上游返回 HTTP 429 Too Many Requests',
  created_at: CREATED_AT,
  updated_at: UPDATED_AT,
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

function installFetch(handler: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => handler(requestURL(input), init ?? {}));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

type FetchMock = ReturnType<typeof installFetch>;

function callsFor(fetchMock: FetchMock, url: string, method: string) {
  return fetchMock.mock.calls.filter(
    ([input, init]) => requestURL(input) === url && (init?.method ?? 'GET') === method,
  );
}

function hasRequest(fetchMock: FetchMock, url: string, method: string): boolean {
  return callsFor(fetchMock, url, method).length > 0;
}

/**
 * createBackend 模拟 T07/T08/T22 后端：
 * - 列表只返回 key_mask，完整 Key 只经 /key 端点返回；
 * - 创建/更新保存密文对应的测试明文，用于断言 mask 更新；
 * - 删除返回 204；调试端点可由用例替换为成功/失败响应。
 */
function createBackend(initialChannels: Channel[], initialSecrets: Record<number, string>) {
  const channels = initialChannels.map((channel) => ({ ...channel }));
  const secrets = new Map<number, string>();
  for (const [rawID, key] of Object.entries(initialSecrets)) {
    secrets.set(Number(rawID), key);
  }
  let nextID = 100;
  let debugHandler: ((channel: Channel) => Response) | null = null;

  const find = (id: number): Channel | undefined => channels.find((channel) => channel.id === id);

  const nameConflict = () =>
    jsonResponse({ error: 'channel_name_conflict', message: '渠道名称已存在' }, 409);

  const handler = (url: string, init: RequestInit): Response => {
    const method = init.method ?? 'GET';

    if (url === '/admin/api/channels' && method === 'GET') {
      return jsonResponse({ channels });
    }

    if (url === '/admin/api/channels' && method === 'POST') {
      const body = JSON.parse(String(init.body)) as ChannelInput;
      if (channels.some((channel) => channel.name === body.name)) {
        return nameConflict();
      }
      const created: Channel = {
        id: nextID,
        name: body.name,
        base_url: body.base_url,
        protocol: body.protocol,
        key_mask: maskKey(body.key),
        priority: body.priority,
        max_concurrency: body.max_concurrency,
        enabled: body.enabled,
        created_at: CREATED_AT,
        updated_at: UPDATED_AT,
      };
      nextID += 1;
      secrets.set(created.id, body.key);
      channels.push(created);
      return jsonResponse(created, 201);
    }

    const keyMatch = /^\/admin\/api\/channels\/(\d+)\/key$/.exec(url);
    if (keyMatch !== null && method === 'GET') {
      const channel = find(Number(keyMatch[1]));
      if (channel === undefined) {
        return jsonResponse({ error: 'channel_not_found', message: '渠道不存在' }, 404);
      }
      const secret = secrets.get(channel.id);
      if (secret === undefined) {
        return jsonResponse({ error: 'internal_error', message: '服务器内部错误' }, 500);
      }
      return jsonResponse({ key: secret, mask: channel.key_mask });
    }

    const debugMatch = /^\/admin\/api\/debug\/channels\/(\d+)\/test$/.exec(url);
    if (debugMatch !== null && method === 'POST') {
      const channel = find(Number(debugMatch[1]));
      if (channel === undefined) {
        return jsonResponse({ error: 'channel_not_found', message: '渠道不存在' }, 404);
      }
      if (debugHandler === null) {
        return jsonResponse(
          { error: 'channel_has_no_mapping', message: '该渠道没有启用的模型映射' },
          400,
        );
      }
      return debugHandler(channel);
    }

    const match = /^\/admin\/api\/channels\/(\d+)$/.exec(url);
    if (match !== null) {
      const id = Number(match[1]);
      const index = channels.findIndex((channel) => channel.id === id);
      const channel = channels[index];
      if (channel === undefined) {
        return jsonResponse({ error: 'channel_not_found', message: '渠道不存在' }, 404);
      }

      if (method === 'PATCH') {
        const patch = JSON.parse(String(init.body)) as ChannelPatch;
        if (
          patch.name !== undefined &&
          channels.some((item) => item.id !== id && item.name === patch.name)
        ) {
          return nameConflict();
        }
        const { key, ...config } = patch;
        const next: Channel = { ...channel, ...config, updated_at: UPDATED_AT };
        if (key !== undefined && key !== '') {
          secrets.set(id, key);
          next.key_mask = maskKey(key);
        }
        channels[index] = next;
        return jsonResponse(next);
      }

      if (method === 'DELETE') {
        channels.splice(index, 1);
        secrets.delete(id);
        return emptyResponse(204);
      }
    }

    throw new Error(`未预期的请求：${method} ${url}`);
  };

  return {
    handler,
    channels,
    setDebugHandler(next: ((channel: Channel) => Response) | null) {
      debugHandler = next;
    },
  };
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

describe('渠道页：脱敏 Key、揭示与复制', () => {
  it('列表初始只显示 mask；点击揭示后可复制，关闭后清除完整 Key', async () => {
    const backend = createBackend([PRIMARY, BACKUP], {
      1: SECRET_PRIMARY,
      2: SECRET_BACKUP,
    });
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);

    expect(await screen.findByText(PRIMARY.key_mask)).toBeInTheDocument();
    expect(screen.getByText(BACKUP.key_mask)).toBeInTheDocument();
    expect(screen.getByText('正常 · 128 ms')).toBeInTheDocument();
    expect(screen.getByText('异常')).toBeInTheDocument();

    // 初始状态不请求揭示端点，DOM 中也没有完整 Key。
    expect(callsFor(fetchMock, '/admin/api/channels/1/key', 'GET')).toHaveLength(0);
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_BACKUP)).not.toBeInTheDocument();

    await user.click(
      screen.getByRole('button', { name: '显示渠道 codex-primary 的完整 Key' }),
    );

    const dialog = await screen.findByRole('dialog', { name: '渠道 Key' });
    expect(callsFor(fetchMock, '/admin/api/channels/1/key', 'GET')).toHaveLength(1);

    const keyInput = await within(dialog).findByRole('textbox', { name: '完整 Key' });
    expect(keyInput).toHaveValue(SECRET_PRIMARY);
    expect(keyInput).toHaveAttribute('readonly');

    await user.click(
      within(dialog).getByRole('button', { name: '复制渠道 codex-primary 的 Key' }),
    );
    expect(await within(dialog).findByText('已复制完整 Key。')).toBeInTheDocument();
    expect(await navigator.clipboard.readText()).toBe(SECRET_PRIMARY);

    await user.click(within(dialog).getByRole('button', { name: '关闭 Key 面板' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();

    // 揭示另一个渠道时只保留当前渠道的完整 Key。
    await user.click(screen.getByRole('button', { name: '显示渠道 chat-backup 的完整 Key' }));
    const secondDialog = await screen.findByRole('dialog', { name: '渠道 Key' });
    expect(await within(secondDialog).findByDisplayValue(SECRET_BACKUP)).toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();
  });

  it('揭示失败时只显示安全错误，不渲染完整 Key', async () => {
    // 没有保存密钥的渠道模拟解密/存储故障：返回 500。
    const backend = createBackend([PRIMARY], {});
    installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.key_mask);
    await user.click(
      screen.getByRole('button', { name: '显示渠道 codex-primary 的完整 Key' }),
    );

    const dialog = await screen.findByRole('dialog', { name: '渠道 Key' });
    expect(await within(dialog).findByText('无法显示完整 Key')).toBeInTheDocument();
    expect(
      within(dialog).getByText('无法读取完整 Key：服务器内部错误。'),
    ).toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();
  });
});

describe('渠道页：创建与编辑', () => {
  it('非法 URL 与并发 0 显示字段错误且不发请求，修正后创建成功', async () => {
    const backend = createBackend([], {});
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);

    expect(await screen.findByText('尚未配置渠道')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '添加第一个渠道' }));

    await user.type(screen.getByLabelText('渠道名称'), 'bad-channel');
    await user.type(screen.getByLabelText('Base URL'), 'not-a-url');
    await user.type(screen.getByLabelText('上游 Key'), 'sk-test-123456');
    await user.clear(screen.getByLabelText('并发上限'));
    await user.type(screen.getByLabelText('并发上限'), '0');
    await user.click(screen.getByRole('button', { name: '创建渠道' }));

    expect(await screen.findByText('Base URL 不是合法的 URL')).toBeInTheDocument();
    expect(screen.getByText('并发上限必须是 1 到 64 的整数')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/channels', 'POST')).toBe(false);

    // 合法主机名但非 http/https 同样被前端拦截。
    await user.clear(screen.getByLabelText('Base URL'));
    await user.type(screen.getByLabelText('Base URL'), 'ftp://upstream.example.com/v1');
    await user.click(screen.getByRole('button', { name: '创建渠道' }));
    expect(await screen.findByText('Base URL 只允许 http/https')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/channels', 'POST')).toBe(false);

    await user.clear(screen.getByLabelText('Base URL'));
    await user.type(screen.getByLabelText('Base URL'), 'https://upstream.example.com/v1');
    await user.clear(screen.getByLabelText('并发上限'));
    await user.type(screen.getByLabelText('并发上限'), '3');
    await user.click(screen.getByRole('button', { name: '创建渠道' }));

    expect(await screen.findByText('渠道「bad-channel」已创建。')).toBeInTheDocument();

    const postCalls = callsFor(fetchMock, '/admin/api/channels', 'POST');
    expect(postCalls).toHaveLength(1);
    expect(JSON.parse(String(postCalls[0]?.[1]?.body))).toEqual({
      name: 'bad-channel',
      base_url: 'https://upstream.example.com/v1',
      protocol: 'openai_responses',
      key: 'sk-test-123456',
      priority: 0,
      max_concurrency: 3,
      enabled: true,
    });

    // 提交后刷新：列表出现新渠道，且只显示 mask。
    expect(await screen.findByText(maskKey('sk-test-123456'))).toBeInTheDocument();
    expect(screen.queryByDisplayValue('sk-test-123456')).not.toBeInTheDocument();
  });

  it('编辑时 Key 留空不发 key 字段，填写新 Key 后 mask 更新', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY });
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.key_mask);

    await user.click(screen.getByRole('button', { name: '编辑渠道 codex-primary' }));
    let dialog = await screen.findByRole('dialog', { name: '编辑渠道：codex-primary' });
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('codex-primary');
    expect(within(dialog).getByLabelText('Base URL')).toHaveValue(PRIMARY.base_url);
    expect(within(dialog).getByLabelText(/上游 Key/)).toHaveValue('');

    await user.clear(within(dialog).getByLabelText('优先级'));
    await user.type(within(dialog).getByLabelText('优先级'), '3');
    await user.click(within(dialog).getByRole('button', { name: '保存修改' }));

    expect(await screen.findByText('渠道「codex-primary」已更新。')).toBeInTheDocument();
    await waitFor(() => {
      expect(callsFor(fetchMock, '/admin/api/channels', 'GET').length).toBeGreaterThan(1);
    });

    const firstPatch = callsFor(fetchMock, '/admin/api/channels/1', 'PATCH')[0];
    expect(firstPatch).toBeDefined();
    const firstBody = JSON.parse(String(firstPatch?.[1]?.body)) as Record<string, unknown>;
    expect(firstBody).not.toHaveProperty('key');
    expect(firstBody.priority).toBe(3);

    // 再次编辑并填写新 Key：PATCH 带 key，返回的 mask 刷新到列表。
    await user.click(screen.getByRole('button', { name: '编辑渠道 codex-primary' }));
    dialog = await screen.findByRole('dialog', { name: '编辑渠道：codex-primary' });
    await user.type(within(dialog).getByLabelText(/上游 Key/), SECRET_ROTATED);
    await user.click(within(dialog).getByRole('button', { name: '保存修改' }));

    expect(await screen.findByText(maskKey(SECRET_ROTATED))).toBeInTheDocument();

    const patchCalls = callsFor(fetchMock, '/admin/api/channels/1', 'PATCH');
    expect(patchCalls).toHaveLength(2);
    const rotatedBody = JSON.parse(String(patchCalls[1]?.[1]?.body)) as Record<string, unknown>;
    expect(rotatedBody.key).toBe(SECRET_ROTATED);
    expect(screen.queryByDisplayValue(SECRET_ROTATED)).not.toBeInTheDocument();
  });
});

describe('渠道页：启停、删除与测试', () => {
  it('启停后状态更新，删除需要二次确认', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY });
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.key_mask);

    const toggle = screen.getByRole('switch', { name: '渠道 codex-primary 启停' });
    expect(toggle).toHaveAttribute('aria-checked', 'true');
    await user.click(toggle);

    expect(await screen.findByText('渠道「codex-primary」已停用。')).toBeInTheDocument();
    const patchCalls = callsFor(fetchMock, '/admin/api/channels/1', 'PATCH');
    expect(patchCalls).toHaveLength(1);
    expect(JSON.parse(String(patchCalls[0]?.[1]?.body))).toEqual({ enabled: false });
    expect(
      screen.getByRole('switch', { name: '渠道 codex-primary 启停' }),
    ).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByText('已停用')).toBeInTheDocument();

    await user.click(screen.getByRole('switch', { name: '渠道 codex-primary 启停' }));
    expect(await screen.findByText('渠道「codex-primary」已启用。')).toBeInTheDocument();
    expect(
      screen.getByRole('switch', { name: '渠道 codex-primary 启停' }),
    ).toHaveAttribute('aria-checked', 'true');

    await user.click(screen.getByRole('button', { name: '删除渠道 codex-primary' }));
    // 第一次点击只进入确认态，不发 DELETE。
    expect(hasRequest(fetchMock, '/admin/api/channels/1', 'DELETE')).toBe(false);
    expect(screen.getByText('确认删除')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '确认删除渠道 codex-primary' }));
    expect(await screen.findByText('渠道「codex-primary」已删除。')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/channels/1', 'DELETE')).toBe(true);
    expect(await screen.findByText('尚未配置渠道')).toBeInTheDocument();
    expect(screen.queryByText(PRIMARY.key_mask)).not.toBeInTheDocument();
  });

  it('测试失败时显示脱敏错误，不显示完整 Key', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY });
    backend.setDebugHandler((channel) => {
      channel.last_health_status = 'unhealthy';
      channel.last_health_latency_ms = 42;
      channel.last_error = '上游返回 HTTP 429 Too Many Requests';
      return jsonResponse({
        status: 'unhealthy',
        http_status: 429,
        latency_ms: 42,
        error: '上游返回 HTTP 429 Too Many Requests',
        channel_id: channel.id,
        channel_name: channel.name,
        upstream_model: 'gpt-5.1-codex',
        tested_at: '2026-10-30T10:00:00Z',
        request_link: '/admin/requests?channel_id=1',
      });
    });
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.key_mask);
    expect(screen.getByText('正常 · 128 ms')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '测试渠道 codex-primary' }));

    expect(
      await screen.findByText('测试失败：上游返回 HTTP 429 Too Many Requests'),
    ).toBeInTheDocument();
    expect(screen.getByText('失败')).toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();

    const testCalls = callsFor(fetchMock, '/admin/api/debug/channels/1/test', 'POST');
    expect(testCalls).toHaveLength(1);
    expect(JSON.parse(String(testCalls[0]?.[1]?.body))).toEqual({});

    // 测试结果刷新列表：健康状态由正常变为异常。
    expect(await screen.findByText('异常')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: '测试渠道 codex-primary' })).toBeEnabled();
    });

    // HTTP 层失败只显示安全文案，不拼接服务端原始错误。
    backend.setDebugHandler(() =>
      jsonResponse({ error: 'internal_error', message: '服务器内部错误' }, 500),
    );
    await user.click(screen.getByRole('button', { name: '测试渠道 codex-primary' }));
    expect(await screen.findByText('服务器暂时不可用，请稍后重试。')).toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();
    expect(screen.getByText(PRIMARY.key_mask)).toBeInTheDocument();
  });

  it('初始化 401 时跳转登录且不渲染渠道内容', async () => {
    installFetch(() => jsonResponse({ error: 'unauthorized', message: '需要管理员登录' }, 401));

    render(<ChannelsPage />);

    expect(await screen.findByText('登录状态已失效')).toBeInTheDocument();
    expect(redirectSpy).toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: '新建渠道' })).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});
