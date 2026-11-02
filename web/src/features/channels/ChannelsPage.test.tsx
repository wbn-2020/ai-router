import { useState } from 'react';
import '@testing-library/jest-dom/vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import type { ModelRoute, ModelRouteInput, ModelRoutePatch } from '../mappings/api';
import ChannelsPage from './ChannelsPage';
import { ChannelModelPicker } from './ChannelModelPicker';
import { ChannelTestResultView } from './ChannelTestResultView';
import { formatChannelTimestamp } from './channelForm';
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
  model_count: 2,
  priority: 10,
  max_concurrency: 4,
  enabled: true,
  last_health_status: 'healthy',
  last_health_latency_ms: 128,
  last_health_at: '2026-10-30T09:00:00Z',
  created_at: CREATED_AT,
  updated_at: UPDATED_AT,
};

it('运行状态、启用和旧版本检查相互独立，缺失运行状态为未知', async () => {
  const backend = createBackend([{
    ...PRIMARY,
    connection_revision: 3,
    last_health_revision: 2,
    last_health_model: 'historical-model',
    last_health_protocol: 'openai_responses',
    last_health_kind: 'generation',
    last_health_duration_ms: 600,
    runtime: { state: 'recovering', in_use: 1, limit: 4, model_cooldowns: 2, model_recovering: 1 },
  }, BACKUP], {});
  installFetch(backend.handler);
  render(<ChannelsPage />);
  await screen.findByText('codex-primary');
  expect(screen.getByRole('columnheader', { name: '运行状态' })).toBeInTheDocument();
  expect(screen.getByText('待恢复 / 恢复中')).toBeInTheDocument();
  expect(screen.getByText('运行状态未知')).toBeInTheDocument();
  expect(screen.getByText('已过期')).toBeInTheDocument();
  expect(screen.getByText(/检查 v2 · 当前 v3/)).toBeInTheDocument();
  expect(screen.getByText('historical-model')).toBeInTheDocument();
  expect(screen.getByText('头 128 ms · 总 600 ms')).toBeInTheDocument();
  expect(screen.getByRole('switch', { name: '渠道 codex-primary 启停' })).toBeChecked();
});

it('旧检查没有连接版本时不作为当前版本通过', async () => {
  installFetch(createBackend([{ ...PRIMARY, connection_revision: 3, last_health_revision: 0 }], {}).handler);
  render(<ChannelsPage />);
  await screen.findByText('codex-primary');
  expect(screen.getByText('已过期')).toBeInTheDocument();
  expect(screen.getByText('本次通过')).toHaveClass('badge--muted');
  expect(screen.getByText('检查 版本未知 · 当前 v3')).toBeInTheDocument();
  expect(screen.getByText('运行状态未知')).toBeInTheDocument();
});

it('单次生成预算截断保持不确定，不给出成功或失败样式', () => {
  render(<ChannelTestResultView label="预算检查" result={{
    status: 'inconclusive', http_status: 200, latency_ms: 12, duration_ms: 90,
    error_code: 'output_limit', error: '输出预算耗尽', protocol: 'openai_chat',
    connection_revision: 7, kind: 'generation', channel_id: 1, channel_name: 'test',
    upstream_model: 'm', tested_at: CREATED_AT, request_link: '/admin/requests',
  }} />);
  const result = screen.getByRole('region', { name: '预算检查' });
  expect(within(result).getByText('不确定')).toBeInTheDocument();
  expect(within(result).queryByText('失败')).not.toBeInTheDocument();
  expect(within(result).queryByText('通过')).not.toBeInTheDocument();
  expect(result).not.toHaveClass('model-check--healthy', 'model-check--unhealthy');
  expect(within(result).getByText(/不代表通过或失败/)).toBeInTheDocument();
  expect(within(result).getByText('v7')).toBeInTheDocument();
});

const BACKUP: Channel = {
  id: 2,
  name: 'chat-backup',
  base_url: 'https://chat.example.com/v1?api-version=2026-01-01',
  protocol: 'openai_chat',
  key_mask: maskKey(SECRET_BACKUP),
  model_count: 1,
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

function installFetch(handler: (url: string, init: RequestInit) => Response | Promise<Response>) {
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

function deferred<Value>() {
  let resolve!: (value: Value) => void;
  const promise = new Promise<Value>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

function modelRoute(patch: Partial<ModelRoute> = {}): ModelRoute {
  return {
    id: 11,
    client_model: 'codex-alias',
    channel_id: 1,
    upstream_model: 'gpt-5.1-codex',
    priority: 7,
    enabled: true,
    created_at: CREATED_AT,
    updated_at: UPDATED_AT,
    ...patch,
  };
}

async function openMore(user: ReturnType<typeof userEvent.setup>, name: string) {
  const summary = screen.getByLabelText(`渠道 ${name} 的更多操作`);
  const details = summary.closest('details');
  if (!details?.open) {
    await user.click(summary);
  }
}

/** openCapabilityFields 展开表单里的“能力声明（可选）”，已展开时保持原状。 */
async function openCapabilityFields(
  user: ReturnType<typeof userEvent.setup>,
  form: HTMLElement,
): Promise<void> {
  const summary = within(form).getByText('能力声明（可选）');
  const details = summary.closest('details');
  if (details !== null && !details.open) {
    await user.click(summary);
  }
}

/** openModelsDialog 打开渠道编辑抽屉并等待模型列表加载完成。 */
async function openModelsDialog(
  user: ReturnType<typeof userEvent.setup>,
  channelName = PRIMARY.name,
): Promise<HTMLElement> {
  await screen.findByText(channelName);
  await user.click(screen.getByRole('button', { name: `编辑渠道 ${channelName}` }));
  const dialog = await screen.findByRole('dialog');
  await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' });
  return dialog;
}

/** fake 后端只在内存中保留 Key；所有端点由 fetch stub 截获。 */
function createBackend(initialChannels: Channel[], initialSecrets: Record<number, string>, initialRoutes: ModelRoute[] = []) {
  const channels = initialChannels.map((channel) => ({ ...channel }));
  const secrets = new Map<number, string>();
  for (const [rawID, key] of Object.entries(initialSecrets)) {
    secrets.set(Number(rawID), key);
  }
  let nextID = 100;
  let nextRouteID = 1000;
  const routes = initialRoutes.map((route) => ({ ...route }));
  let debugHandler: ((channel: Channel, body: { model?: string }) => Response) | null = null;
  let discoveryModels = ['gpt-5', 'o3'];

  const find = (id: number): Channel | undefined => channels.find((channel) => channel.id === id);

  const nameConflict = () =>
    jsonResponse({ error: 'channel_name_conflict', message: '渠道名称已存在' }, 409);

  const handler = (url: string, init: RequestInit): Response => {
    const method = init.method ?? 'GET';

    if (url === '/admin/api/channels' && method === 'GET') {
      return jsonResponse({ channels });
    }

    if (url === '/admin/api/model-routes' && method === 'GET') {
      return jsonResponse({ model_routes: routes });
    }
    if (url === '/admin/api/model-routes' && method === 'POST') {
      const body = JSON.parse(String(init.body)) as ModelRouteInput;
      if (routes.some((route) => route.channel_id === body.channel_id && route.client_model === body.client_model)) {
        return jsonResponse({ error: 'model_route_conflict', message: '模型已存在' }, 409);
      }
      const created = modelRoute({ ...body, id: nextRouteID++ });
      routes.push(created);
      const channel = find(body.channel_id);
      if (channel !== undefined) {
        channel.model_count += 1;
      }
      return jsonResponse(created, 201);
    }
    const routeMatch = /^\/admin\/api\/model-routes\/(\d+)$/.exec(url);
    if (routeMatch !== null) {
      const index = routes.findIndex((route) => route.id === Number(routeMatch[1]));
      const route = routes[index];
      if (route === undefined) {
        return jsonResponse({ error: 'model_route_not_found', message: '模型不存在' }, 404);
      }
      if (method === 'PATCH') {
        const patch = JSON.parse(String(init.body)) as ModelRoutePatch;
        const updated = { ...route, ...patch, updated_at: UPDATED_AT };
        routes[index] = updated;
        return jsonResponse(updated);
      }
      if (method === 'DELETE') {
        routes.splice(index, 1);
        const channel = find(route.channel_id);
        if (channel !== undefined) {
          channel.model_count -= 1;
        }
        return emptyResponse(204);
      }
    }

    if (url === '/admin/api/channels/preview-models' && method === 'POST') {
      return jsonResponse({ models: discoveryModels });
    }

    if (/^\/admin\/api\/channels\/\d+\/models\/sync$/.test(url) && method === 'POST') {
      return jsonResponse({ models: discoveryModels });
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
        model_count: body.models?.length ?? 0,
        priority: body.priority,
        max_concurrency: body.max_concurrency,
        enabled: body.enabled,
        created_at: CREATED_AT,
        updated_at: UPDATED_AT,
      };
      nextID += 1;
      secrets.set(created.id, body.key);
      channels.push(created);
      for (const model of body.models ?? []) {
        routes.push(modelRoute({ id: nextRouteID++, client_model: model, upstream_model: model, channel_id: created.id, priority: 0 }));
      }
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
      return debugHandler(channel, JSON.parse(String(init.body)) as { model?: string });
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
    routes,
    setDebugHandler(next: ((channel: Channel, body: { model?: string }) => Response) | null) {
      debugHandler = next;
    },
    setDiscoveryModels(next: string[]) {
      discoveryModels = next;
    },
  };
}

describe('模型选择器：搜索与连接代次', () => {
  it('全选和清空只作用于搜索结果，保留手填和隐藏选中', async () => {
    const fetchModels = vi.fn(async () => ['gpt-5', 'o3', 'gpt-5']);
    function Picker() {
      const [models, setModels] = useState(['manual-model', 'o3']);
      return <ChannelModelPicker value={models} onChange={setModels} fetchModels={fetchModels} />;
    }
    const user = userEvent.setup();
    render(<Picker />);
    await user.click(screen.getByRole('button', { name: '从上游获取' }));
    await screen.findByText('已从上游获取 2 个模型。');
    await user.type(screen.getByRole('searchbox', { name: '搜索模型' }), 'gpt');
    await user.click(screen.getByRole('button', { name: '全选结果' }));
    expect(screen.getByText('已选 3')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '移除模型 manual-model' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '移除模型 o3' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '取消结果' }));
    expect(screen.getByText('已选 2')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '移除模型 gpt-5' })).not.toBeInTheDocument();
    await user.clear(screen.getByRole('searchbox', { name: '搜索模型' }));
    expect(screen.getByRole('checkbox', { name: '选择模型 o3' })).toBeChecked();
  });

  it('连接代次变化清候选但保留选中，旧异步结果不覆盖新结果', async () => {
    const oldRequest = deferred<string[]>();
    const onChange = vi.fn();
    const props = { value: ['manual-model'], onChange };
    const { rerender } = render(<ChannelModelPicker {...props} generation={0} fetchModels={() => oldRequest.promise} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: '从上游获取' }));
    rerender(<ChannelModelPicker {...props} generation={1} fetchModels={async () => ['new-model']} />);
    await user.click(screen.getByRole('button', { name: '从上游获取' }));
    await screen.findByRole('checkbox', { name: '选择模型 new-model' });
    await act(async () => oldRequest.resolve(['stale-model']));
    expect(screen.queryByRole('checkbox', { name: '选择模型 stale-model' })).not.toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: '选择模型 new-model' })).toBeInTheDocument();
    rerender(<ChannelModelPicker {...props} generation={2} fetchModels={async () => []} />);
    expect(screen.queryByRole('checkbox', { name: '选择模型 new-model' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '移除模型 manual-model' })).toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe('渠道编辑：连接、模型与单次检查', () => {
  it('新建同页显示预设连接模型，自动名称保留人工修改，三项连接输入都会失效候选', async () => {
    const backend = createBackend([], {});
    backend.setDiscoveryModels(['gpt-5', 'o3']);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();
    render(<ChannelsPage />);
    await screen.findByText('尚未配置渠道');
    await user.click(screen.getByRole('button', { name: '新建渠道' }));
    const dialog = await screen.findByRole('dialog', { name: '新建渠道' });
    expect(within(dialog).getByLabelText('供应商预设').tagName).toBe('SELECT');
    expect(within(dialog).queryByLabelText('新建渠道步骤')).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('Base URL')).toHaveValue('https://api.openai.com/v1');
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('api.openai.com-responses');
    expect(within(dialog).getByRole('button', { name: '从上游获取模型' })).toBeDisabled();
    await user.type(within(dialog).getByLabelText('上游 Key'), 'sk-fake-candidates');
    await user.click(within(dialog).getByRole('button', { name: '从上游获取模型' }));
    await within(dialog).findByRole('checkbox', { name: '选择模型 gpt-5' });
    await user.click(within(dialog).getByRole('checkbox', { name: '选择模型 gpt-5' }));
    fireEvent.change(within(dialog).getByLabelText('Base URL'), { target: { value: 'https://relay.example.com/v1' } });
    expect(within(dialog).queryByRole('checkbox', { name: '选择模型 gpt-5' })).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '移除模型 gpt-5' })).toBeInTheDocument();
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('relay.example.com-responses');
    await user.click(within(dialog).getByRole('button', { name: '从上游获取模型' }));
    await within(dialog).findByRole('checkbox', { name: '选择模型 o3' });
    fireEvent.change(within(dialog).getByLabelText('上游 Key'), { target: { value: 'sk-fake-rotated' } });
    expect(within(dialog).queryByRole('checkbox', { name: '选择模型 o3' })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: '从上游获取模型' }));
    await within(dialog).findByRole('checkbox', { name: '选择模型 o3' });
    await user.click(within(dialog).getByText('高级设置'));
    await user.selectOptions(within(dialog).getByLabelText('协议'), 'openai_chat');
    expect(within(dialog).queryByRole('checkbox', { name: '选择模型 o3' })).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('relay.example.com-chat');
    await user.clear(within(dialog).getByLabelText('渠道名称'));
    await user.type(within(dialog).getByLabelText('渠道名称'), 'my-relay');
    await user.selectOptions(within(dialog).getByLabelText('供应商预设'), 'openai-responses');
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('my-relay');
    expect(within(dialog).getByRole('button', { name: '移除模型 gpt-5' })).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: '创建渠道' }));
    await screen.findByText('渠道「my-relay」已创建，并添加 1 个模型。');
    const posts = callsFor(fetchMock, '/admin/api/channels', 'POST');
    expect(posts).toHaveLength(1);
    expect(JSON.parse(String(posts[0]?.[1]?.body))).toMatchObject({ name: 'my-relay', models: ['gpt-5'] });
    expect(callsFor(fetchMock, '/admin/api/model-routes', 'POST')).toHaveLength(0);
  });

  it('模型表单在连接 form 外，立即 CRUD 保留 route id、alias、priority 和 enabled', async () => {
    const route = modelRoute();
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY }, [route]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();
    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.name);
    await user.click(screen.getByRole('button', { name: '编辑渠道 codex-primary' }));
    const dialog = await screen.findByRole('dialog');
    const manual = within(dialog).getByRole('form', { name: '手动添加模型映射' });
    const saveButton = within(dialog).getByRole('button', { name: '保存连接配置' }) as HTMLButtonElement;
    const connectionForm = saveButton.form;
    expect(connectionForm).toHaveAttribute('id', 'channel-connection-form');
    expect(saveButton.closest('form')).toBeNull();
    expect(connectionForm?.contains(manual)).toBe(false);
    await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' });
    await user.click(within(dialog).getByRole('switch', { name: '启用模型 codex-alias' }));
    await waitFor(() => expect(within(dialog).getByRole('switch', { name: '启用模型 codex-alias' })).toHaveAttribute('aria-checked', 'false'));
    expect(backend.routes[0]).toMatchObject({ id: route.id, client_model: route.client_model, upstream_model: route.upstream_model, priority: 7, enabled: false });
    await user.click(within(dialog).getByRole('button', { name: '编辑模型 codex-alias' }));
    const edit = within(dialog).getByRole('form', { name: '编辑模型映射' });
    expect(connectionForm?.contains(edit)).toBe(false);
    await user.clear(within(edit).getByLabelText('客户端模型'));
    await user.type(within(edit).getByLabelText('客户端模型'), 'renamed-alias');
    await user.clear(within(edit).getByLabelText('上游模型'));
    await user.type(within(edit).getByLabelText('上游模型'), 'upstream-v2');
    await user.clear(within(edit).getByLabelText('优先级'));
    await user.type(within(edit).getByLabelText('优先级'), '9');
    await user.click(within(edit).getByRole('button', { name: '保存模型映射' }));
    await within(dialog).findByText('模型映射已更新。');
    expect(backend.routes[0]).toMatchObject({ id: route.id, client_model: 'renamed-alias', upstream_model: 'upstream-v2', priority: 9, enabled: false });
    const patches = callsFor(fetchMock, '/admin/api/model-routes/11', 'PATCH');
    expect(patches).toHaveLength(2);
    expect(JSON.parse(String(patches[0]?.[1]?.body))).toEqual({ enabled: false });
    expect(JSON.parse(String(patches[1]?.[1]?.body))).toEqual({ client_model: 'renamed-alias', upstream_model: 'upstream-v2', priority: 9 });
    await user.type(within(manual).getByLabelText('客户端模型'), 'manual-alias');
    await user.type(within(manual).getByLabelText('上游模型（留空同名）'), 'manual-upstream');
    await user.click(within(manual).getByRole('button', { name: '手动添加' }));
    await within(dialog).findByText('已添加模型「manual-alias」。');
    await user.click(within(dialog).getByRole('button', { name: '删除模型 renamed-alias' }));
    expect(callsFor(fetchMock, '/admin/api/model-routes/11', 'DELETE')).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: '确认删除模型 renamed-alias' }));
    await within(dialog).findByText('已删除模型「renamed-alias」。');
    expect(callsFor(fetchMock, '/admin/api/channels/1', 'PATCH')).toHaveLength(0);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(await screen.findByRole('button', { name: '管理渠道 codex-primary 的模型' })).toHaveTextContent('2 个模型');
  });

  it('dirty 禁用保存候选和模型测试，连接保存后 snapshot/key 更新并可继续测试固定 alias', async () => {
    const route = modelRoute();
    const disabledRoute = modelRoute({ id: 12, client_model: 'disabled-alias', enabled: false });
    const backend = createBackend([PRIMARY, BACKUP], { 1: SECRET_PRIMARY, 2: SECRET_BACKUP }, [route, disabledRoute]);
    backend.setDebugHandler((channel, body) => jsonResponse({
      status: 'unhealthy', http_status: 200, latency_ms: 31, error: '上游响应缺少有效输出',
      channel_id: channel.id, channel_name: channel.name, model: body.model,
      upstream_model: route.upstream_model, tested_at: '2026-10-30T10:00:00Z',
      request_link: '/admin/requests?channel_id=1&model=codex-alias',
    }));
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();
    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.name);
    await user.click(screen.getByRole('button', { name: '编辑渠道 codex-primary' }));
    const dialog = await screen.findByRole('dialog');
    const test = await within(dialog).findByRole('button', { name: '测试模型 codex-alias' });
    expect(test).toBeEnabled();
    expect(within(dialog).getByRole('button', { name: '测试模型 disabled-alias' })).toBeDisabled();
    expect(within(dialog).getAllByText('未检查')).toHaveLength(2);
    await user.click(within(dialog).getByRole('button', { name: '从上游同步' }));
    await user.click(within(dialog).getByRole('button', { name: '获取上游模型' }));
    await within(dialog).findByRole('checkbox', { name: '选择模型 gpt-5' });
    await user.click(within(dialog).getByRole('checkbox', { name: '选择模型 gpt-5' }));
    fireEvent.change(within(dialog).getByLabelText('Base URL'), { target: { value: 'https://saved-relay.example.com/v1' } });
    expect(within(dialog).queryByRole('checkbox', { name: '选择模型 gpt-5' })).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '移除模型 gpt-5' })).toBeInTheDocument();
    expect(within(dialog).getByText('连接配置尚未保存，请先保存连接配置后再获取或测试模型。')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '获取上游模型' })).toBeDisabled();
    expect(within(dialog).getByRole('button', { name: '添加选中模型' })).toBeDisabled();
    expect(test).toBeDisabled();
    await user.click(test);
    expect(callsFor(fetchMock, '/admin/api/debug/channels/1/test', 'POST')).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: '保存连接配置' }));
    await within(dialog).findByText('连接配置已保存，现在可获取或测试模型。');
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByLabelText(/上游 Key/)).toHaveValue('');
    expect(test).toBeEnabled();
    expect(within(dialog).getByRole('button', { name: '获取上游模型' })).toBeEnabled();
    expect(within(dialog).getByRole('button', { name: '添加选中模型' })).toBeEnabled();
    const patch = callsFor(fetchMock, '/admin/api/channels/1', 'PATCH')[0];
    expect(JSON.parse(String(patch?.[1]?.body))).not.toHaveProperty('key');
    await user.click(test);
    const result = await within(dialog).findByRole('region', { name: '模型 codex-alias 的测试结果' });
    expect(within(result).getByText('失败')).toBeInTheDocument();
    expect(within(result).getByText('200')).toBeInTheDocument();
    expect(within(result).getByText(route.client_model)).toBeInTheDocument();
    expect(within(result).getByText(route.upstream_model)).toBeInTheDocument();
    expect(within(result).getByText(formatChannelTimestamp('2026-10-30T10:00:00Z'))).toBeInTheDocument();
    expect(within(result).getByRole('link')).toHaveAttribute('href', '/admin/requests?channel_id=1&model=codex-alias');
    expect(within(dialog).getByText('本次失败')).toBeInTheDocument();
    expect(within(dialog).getByText('未检查')).toBeInTheDocument();
    const testCalls = callsFor(fetchMock, '/admin/api/debug/channels/1/test', 'POST');
    expect(testCalls).toHaveLength(1);
    expect(JSON.parse(String(testCalls[0]?.[1]?.body))).toEqual({ model: route.client_model });
    expect(callsFor(fetchMock, '/admin/api/debug/channels/2/test', 'POST')).toHaveLength(0);
    expect(callsFor(fetchMock, '/admin/api/channels/1/key', 'GET')).toHaveLength(0);
    backend.setDebugHandler((channel, body) => jsonResponse({
      status: 'inconclusive', http_status: 200, latency_ms: 12, duration_ms: 52,
      error_code: 'output_limit', protocol: channel.protocol, connection_revision: 2, kind: 'generation',
      channel_id: channel.id, channel_name: channel.name, model: body.model,
      upstream_model: route.upstream_model, tested_at: CREATED_AT, request_link: '/admin/requests',
    }));
    await user.click(test);
    expect(await within(dialog).findByText('本次不确定')).toBeInTheDocument();
    expect(within(dialog).queryByText('本次失败')).not.toBeInTheDocument();
    expect(within(dialog).getByText('未检查')).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText(/上游 Key/), SECRET_ROTATED);
    expect(within(dialog).queryByRole('region', { name: '模型 codex-alias 的测试结果' })).not.toBeInTheDocument();
    expect(test).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: '保存连接配置' }));
    await within(dialog).findByText(maskKey(SECRET_ROTATED));
    expect(within(dialog).getByLabelText(/上游 Key/)).toHaveValue('');
    expect(test).toBeEnabled();
  });

  it('pending 创建、连接 PATCH 和模型 CRUD 统一关闭/Escape/重复提交保护并恢复焦点', async () => {
    const createdRequest = deferred<Response>();
    const updatedRequest = deferred<Response>();
    const routeRequest = deferred<Response>();
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY });
    let pendingCreate = true;
    let pendingPatch = true;
    let pendingRoute = true;
    const fetchMock = installFetch((url, init) => {
      if (url === '/admin/api/channels' && init.method === 'POST' && pendingCreate) {
        return createdRequest.promise;
      }
      if (url === '/admin/api/channels/1' && init.method === 'PATCH' && pendingPatch) {
        return updatedRequest.promise;
      }
      if (url === '/admin/api/model-routes' && init.method === 'POST' && pendingRoute) {
        return routeRequest.promise;
      }
      return backend.handler(url, init);
    });
    const user = userEvent.setup();
    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.name);
    const createButton = screen.getByRole('button', { name: '新建渠道' });
    await user.click(createButton);
    let dialog = await screen.findByRole('dialog', { name: '新建渠道' });
    await user.type(within(dialog).getByLabelText('上游 Key'), 'sk-fake-pending');
    const submit = within(dialog).getByRole('button', { name: '创建渠道' });
    await user.click(submit);
    expect(within(dialog).getByRole('button', { name: '关闭渠道表单' })).toBeDisabled();
    await user.keyboard('{Escape}');
    fireEvent.mouseDown(dialog.parentElement!);
    fireEvent.submit((submit as HTMLButtonElement).form!);
    expect(dialog).toBeInTheDocument();
    expect(callsFor(fetchMock, '/admin/api/channels', 'POST')).toHaveLength(1);
    pendingCreate = false;
    const createCall = callsFor(fetchMock, '/admin/api/channels', 'POST')[0]!;
    await act(async () => createdRequest.resolve(backend.handler('/admin/api/channels', createCall[1]!)));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(createButton).toHaveFocus();

    const editButton = screen.getByRole('button', { name: '编辑渠道 codex-primary' });
    await user.click(editButton);
    dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Base URL'), { target: { value: 'https://new.example.com/v1' } });
    const save = within(dialog).getByRole('button', { name: '保存连接配置' });
    await user.click(save);
    await user.keyboard('{Escape}');
    fireEvent.mouseDown(dialog.parentElement!);
    fireEvent.submit((save as HTMLButtonElement).form!);
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '完成' })).toBeDisabled();
    expect(callsFor(fetchMock, '/admin/api/channels/1', 'PATCH')).toHaveLength(1);
    pendingPatch = false;
    const patchCall = callsFor(fetchMock, '/admin/api/channels/1', 'PATCH')[0]!;
    await act(async () => updatedRequest.resolve(backend.handler('/admin/api/channels/1', patchCall[1]!)));
    await within(dialog).findByText('连接配置已保存，现在可获取或测试模型。');

    const manual = within(dialog).getByRole('form', { name: '手动添加模型映射' });
    await user.type(within(manual).getByLabelText('客户端模型'), 'pending-model');
    await user.click(within(manual).getByRole('button', { name: '手动添加' }));
    expect(within(dialog).getByRole('button', { name: '关闭渠道表单' })).toBeDisabled();
    await user.keyboard('{Escape}');
    fireEvent.submit(manual);
    expect(dialog).toBeInTheDocument();
    expect(callsFor(fetchMock, '/admin/api/model-routes', 'POST')).toHaveLength(1);
    pendingRoute = false;
    const routeCall = callsFor(fetchMock, '/admin/api/model-routes', 'POST')[0]!;
    await act(async () => routeRequest.resolve(backend.handler('/admin/api/model-routes', routeCall[1]!)));
    await within(dialog).findByText('已添加模型「pending-model」。');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(editButton).toHaveFocus();
    expect(callsFor(fetchMock, '/admin/api/channels', 'GET')).toHaveLength(2);
  });
});

describe('渠道模型能力声明', () => {
  it('编辑表单预填能力，只提交被修改的能力字段', async () => {
    const route = modelRoute({
      input_modalities: 'text',
      context_window: 32000,
      reasoning_levels: ['low', 'high'],
    });
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY }, [route]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    const dialog = await openModelsDialog(user);
    await user.click(await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' }));
    const edit = within(dialog).getByRole('form', { name: '编辑模型映射' });
    await openCapabilityFields(user, edit);

    expect(within(edit).getByLabelText('输入模态')).toHaveValue('text');
    expect(within(edit).getByLabelText('上下文窗口')).toHaveValue(32000);
    expect(within(edit).getByRole('checkbox', { name: 'low' })).toBeChecked();
    expect(within(edit).getByRole('checkbox', { name: 'high' })).toBeChecked();
    expect(within(edit).getByRole('checkbox', { name: 'medium' })).not.toBeChecked();

    await user.selectOptions(within(edit).getByLabelText('输入模态'), 'text_image');
    await user.click(within(edit).getByRole('button', { name: '保存模型映射' }));
    await within(dialog).findByText('模型映射已更新。');

    const patches = callsFor(fetchMock, '/admin/api/model-routes/11', 'PATCH');
    expect(patches).toHaveLength(1);
    expect(JSON.parse(String(patches[0]?.[1]?.body))).toEqual({
      client_model: 'codex-alias',
      upstream_model: 'gpt-5.1-codex',
      priority: 7,
      input_modalities: 'text_image',
    });
    expect(backend.routes[0]).toMatchObject({
      id: route.id,
      input_modalities: 'text_image',
      context_window: 32000,
      reasoning_levels: ['low', 'high'],
    });
  });

  it('清空窗口与档位时发送 0 / []，重新编辑回到未声明', async () => {
    const route = modelRoute({
      input_modalities: 'text_image',
      context_window: 128000,
      reasoning_levels: ['low', 'medium'],
    });
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY }, [route]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    const dialog = await openModelsDialog(user);
    await user.click(await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' }));
    const edit = within(dialog).getByRole('form', { name: '编辑模型映射' });
    await openCapabilityFields(user, edit);

    await user.selectOptions(within(edit).getByLabelText('输入模态'), 'unknown');
    await user.clear(within(edit).getByLabelText('上下文窗口'));
    await user.click(within(edit).getByRole('checkbox', { name: 'low' }));
    await user.click(within(edit).getByRole('checkbox', { name: 'medium' }));
    await user.click(within(edit).getByRole('button', { name: '保存模型映射' }));
    await within(dialog).findByText('模型映射已更新。');

    const patches = callsFor(fetchMock, '/admin/api/model-routes/11', 'PATCH');
    expect(patches).toHaveLength(1);
    expect(JSON.parse(String(patches[0]?.[1]?.body))).toEqual({
      client_model: 'codex-alias',
      upstream_model: 'gpt-5.1-codex',
      priority: 7,
      input_modalities: 'unknown',
      context_window: 0,
      reasoning_levels: [],
    });

    await user.click(await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' }));
    const reopened = within(dialog).getByRole('form', { name: '编辑模型映射' });
    await openCapabilityFields(user, reopened);
    expect(within(reopened).getByLabelText('输入模态')).toHaveValue('unknown');
    expect(within(reopened).getByLabelText('上下文窗口')).toHaveValue(null);
    for (const level of ['low', 'medium']) {
      expect(within(reopened).getByRole('checkbox', { name: level })).not.toBeChecked();
    }
  });

  it('窗口越界只显示行内错误且不发 PATCH，合法值可保存', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY }, [modelRoute()]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    const dialog = await openModelsDialog(user);
    await user.click(await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' }));
    const edit = within(dialog).getByRole('form', { name: '编辑模型映射' });
    await openCapabilityFields(user, edit);
    const windowInput = within(edit).getByLabelText('上下文窗口');

    for (const invalid of ['10000001', '0', '-1']) {
      fireEvent.change(windowInput, { target: { value: invalid } });
      await user.click(within(edit).getByRole('button', { name: '保存模型映射' }));
      expect(within(edit).getByText('上下文窗口必须是 1 到 10000000 的整数，或留空表示未声明。'))
        .toBeInTheDocument();
      expect(windowInput).toHaveAttribute('aria-invalid', 'true');
      expect(callsFor(fetchMock, '/admin/api/model-routes/11', 'PATCH')).toHaveLength(0);
    }

    fireEvent.change(windowInput, { target: { value: '32000' } });
    await user.click(within(edit).getByRole('button', { name: '保存模型映射' }));
    await within(dialog).findByText('模型映射已更新。');
    const patches = callsFor(fetchMock, '/admin/api/model-routes/11', 'PATCH');
    expect(patches).toHaveLength(1);
    expect(JSON.parse(String(patches[0]?.[1]?.body))).toEqual({
      client_model: 'codex-alias',
      upstream_model: 'gpt-5.1-codex',
      priority: 7,
      context_window: 32000,
    });
  });

  it('手动添加可声明能力，留空时创建请求仍只含五个基础字段', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY });
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.name);
    await user.click(screen.getByRole('button', { name: '编辑渠道 codex-primary' }));
    const dialog = await screen.findByRole('dialog');
    const manual = within(dialog).getByRole('form', { name: '手动添加模型映射' });
    await openCapabilityFields(user, manual);

    await user.type(within(manual).getByLabelText('客户端模型'), 'vision-alias');
    await user.type(within(manual).getByLabelText('上游模型（留空同名）'), 'vision-upstream');
    await user.selectOptions(within(manual).getByLabelText('输入模态'), 'text_image');
    await user.type(within(manual).getByLabelText('上下文窗口'), '64000');
    await user.click(within(manual).getByRole('checkbox', { name: 'high' }));
    await user.click(within(manual).getByRole('button', { name: '手动添加' }));
    await within(dialog).findByText('已添加模型「vision-alias」。');
    expect(within(manual).getByLabelText('输入模态')).toHaveValue('unknown');
    expect(within(manual).getByLabelText('上下文窗口')).toHaveValue(null);

    await user.type(within(manual).getByLabelText('客户端模型'), 'plain-alias');
    await user.type(within(manual).getByLabelText('上游模型（留空同名）'), 'plain-upstream');
    await user.click(within(manual).getByRole('button', { name: '手动添加' }));
    await within(dialog).findByText('已添加模型「plain-alias」。');

    const posts = callsFor(fetchMock, '/admin/api/model-routes', 'POST');
    expect(posts).toHaveLength(2);
    expect(JSON.parse(String(posts[0]?.[1]?.body))).toEqual({
      client_model: 'vision-alias',
      channel_id: 1,
      upstream_model: 'vision-upstream',
      priority: 0,
      enabled: true,
      input_modalities: 'text_image',
      context_window: 64000,
      reasoning_levels: ['high'],
    });
    expect(JSON.parse(String(posts[1]?.[1]?.body))).toEqual({
      client_model: 'plain-alias',
      channel_id: 1,
      upstream_model: 'plain-upstream',
      priority: 0,
      enabled: true,
    });
  });

  it('保存失败保留能力输入并显示后端安全文案，不显示上游 Key', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY }, [modelRoute()]);
    installFetch((url, init) => {
      if (url === '/admin/api/model-routes/11' && init.method === 'PATCH') {
        return jsonResponse(
          { error: 'invalid_model_route', message: 'context_window 必须为 1-10000000 或留空' },
          400,
        );
      }
      return backend.handler(url, init);
    });
    const user = userEvent.setup();

    render(<ChannelsPage />);
    const dialog = await openModelsDialog(user);
    await user.click(await within(dialog).findByRole('button', { name: '编辑模型 codex-alias' }));
    const edit = within(dialog).getByRole('form', { name: '编辑模型映射' });
    await openCapabilityFields(user, edit);

    await user.selectOptions(within(edit).getByLabelText('输入模态'), 'text');
    await user.type(within(edit).getByLabelText('上下文窗口'), '32000');
    await user.click(within(edit).getByRole('checkbox', { name: 'high' }));
    await user.click(within(edit).getByRole('button', { name: '保存模型映射' }));

    expect(await within(dialog).findByText('context_window 必须为 1-10000000 或留空'))
      .toBeInTheDocument();
    expect(within(edit).getByLabelText('输入模态')).toHaveValue('text');
    expect(within(edit).getByLabelText('上下文窗口')).toHaveValue(32000);
    expect(within(edit).getByRole('checkbox', { name: 'high' })).toBeChecked();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_BACKUP)).not.toBeInTheDocument();
  });

  it('启停只发送 enabled，切换后能力声明保持不变', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY }, [modelRoute({
      input_modalities: 'text',
      context_window: 32000,
      reasoning_levels: ['low', 'high'],
    })]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    const dialog = await openModelsDialog(user);
    await user.click(within(dialog).getByRole('switch', { name: '启用模型 codex-alias' }));
    await waitFor(() => {
      expect(within(dialog).getByRole('switch', { name: '启用模型 codex-alias' }))
        .toHaveAttribute('aria-checked', 'false');
    });

    const patches = callsFor(fetchMock, '/admin/api/model-routes/11', 'PATCH');
    expect(patches).toHaveLength(1);
    expect(JSON.parse(String(patches[0]?.[1]?.body))).toEqual({ enabled: false });
    expect(backend.routes[0]).toMatchObject({
      enabled: false,
      input_modalities: 'text',
      context_window: 32000,
      reasoning_levels: ['low', 'high'],
    });

    await user.click(within(dialog).getByRole('button', { name: '编辑模型 codex-alias' }));
    const edit = within(dialog).getByRole('form', { name: '编辑模型映射' });
    await openCapabilityFields(user, edit);
    expect(within(edit).getByLabelText('输入模态')).toHaveValue('text');
    expect(within(edit).getByLabelText('上下文窗口')).toHaveValue(32000);
    expect(within(edit).getByRole('checkbox', { name: 'high' })).toBeChecked();
  });
});

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

    expect(await screen.findByText(PRIMARY.name)).toBeInTheDocument();
    await openMore(user, PRIMARY.name);
    await openMore(user, BACKUP.name);
    expect(screen.getByText(PRIMARY.key_mask)).toBeInTheDocument();
    expect(screen.getByText(BACKUP.key_mask)).toBeInTheDocument();
    expect(screen.getByText('本次通过')).toBeInTheDocument();
    expect(screen.getByText('本次失败')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '最近检查' })).toBeInTheDocument();
    expect(screen.getByText(formatChannelTimestamp(PRIMARY.last_health_at))).toBeVisible();
    expect(screen.getByText('检查时间未知')).toBeVisible();

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
    await screen.findByText(PRIMARY.name);
    await openMore(user, PRIMARY.name);
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
    await user.selectOptions(await screen.findByLabelText('供应商预设'), 'custom-responses');

    await user.type(screen.getByLabelText('渠道名称'), 'bad-channel');
    await user.type(screen.getByLabelText('Base URL'), 'not-a-url');
    await user.type(screen.getByLabelText('上游 Key'), 'sk-test-123456');
    await user.click(screen.getByText('高级设置'));
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
    expect(screen.getByRole('button', { name: '新建渠道' })).toHaveFocus();

    const postCalls = callsFor(fetchMock, '/admin/api/channels', 'POST');
    expect(postCalls).toHaveLength(1);
    expect(JSON.parse(String(postCalls[0]?.[1]?.body))).toEqual({
      name: 'bad-channel',
      base_url: 'https://upstream.example.com/v1',
      protocol: 'openai_responses',
      key: 'sk-test-123456',
      models: [],
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
    await user.click(within(dialog).getByText('高级设置'));

    await user.clear(within(dialog).getByLabelText('优先级'));
    await user.type(within(dialog).getByLabelText('优先级'), '3');
    await user.click(within(dialog).getByRole('button', { name: '保存连接配置' }));

    expect(await screen.findByText('渠道「codex-primary」已更新。')).toBeInTheDocument();
    expect(await within(dialog).findByText('连接配置已保存，现在可获取或测试模型。')).toBeInTheDocument();
    expect(callsFor(fetchMock, '/admin/api/channels', 'GET')).toHaveLength(1);
    expect(dialog).toBeInTheDocument();

    const firstPatch = callsFor(fetchMock, '/admin/api/channels/1', 'PATCH')[0];
    expect(firstPatch).toBeDefined();
    const firstBody = JSON.parse(String(firstPatch?.[1]?.body)) as Record<string, unknown>;
    expect(firstBody).not.toHaveProperty('key');
    expect(firstBody.priority).toBe(3);

    // 保存后继续在同一抽屉轮换 Key。
    await user.type(within(dialog).getByLabelText(/上游 Key/), SECRET_ROTATED);
    await user.click(within(dialog).getByRole('button', { name: '保存连接配置' }));

    expect(await within(dialog).findByText(maskKey(SECRET_ROTATED))).toBeInTheDocument();

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

    await openMore(user, PRIMARY.name);
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
    expect(screen.getByText('本次通过')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '测试渠道 codex-primary' }));

    expect(
      await screen.findByText('测试失败：上游返回 HTTP 429 Too Many Requests'),
    ).toBeInTheDocument();
    const testResult = screen.getByRole('region', { name: '渠道 codex-primary 的测试结果' });
    expect(testResult.closest('tr')).toHaveClass('channel-test-row');
    expect(within(testResult).getByText('失败')).toBeInTheDocument();
    expect(screen.queryByText('最近一次测试')).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(SECRET_PRIMARY)).not.toBeInTheDocument();

    const testCalls = callsFor(fetchMock, '/admin/api/debug/channels/1/test', 'POST');
    expect(testCalls).toHaveLength(1);
    expect(JSON.parse(String(testCalls[0]?.[1]?.body))).toEqual({});

    // 测试结果刷新列表：健康状态由正常变为异常。
    expect(await screen.findByText('本次失败')).toBeInTheDocument();
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

    await user.click(screen.getByRole('button', { name: '编辑渠道 codex-primary' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Base URL'), {
      target: { value: 'https://updated.example.com/v1' },
    });
    await user.click(within(dialog).getByRole('button', { name: '保存连接配置' }));
    await within(dialog).findByText('连接配置已保存，现在可获取或测试模型。');
    expect(screen.queryByText('服务器暂时不可用，请稍后重试。')).not.toBeInTheDocument();
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


describe('渠道页：模型发现与内嵌模型', () => {
  it('新建渠道可从上游拉取模型并在保存时一起提交', async () => {
    const backend = createBackend([], {});
    backend.setDiscoveryModels(['gpt-5', 'o3']);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText('尚未配置渠道');
    await user.click(screen.getByRole('button', { name: '添加第一个渠道' }));
    await user.selectOptions(await screen.findByLabelText('供应商预设'), 'custom-responses');

    await user.type(screen.getByLabelText('渠道名称'), 'relay');
    await user.type(screen.getByLabelText('Base URL'), 'https://relay.example.com/v1');
    await user.type(screen.getByLabelText('上游 Key'), 'sk-relay-model');
    expect(screen.getByText('高级设置')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '从上游获取模型' }));
    expect(await screen.findByText('已从上游获取 2 个模型。')).toBeInTheDocument();
    await user.click(screen.getByRole('checkbox', { name: '选择模型 gpt-5' }));
    await user.click(screen.getByRole('button', { name: '创建渠道' }));

    expect(
      await screen.findByText('渠道「relay」已创建，并添加 1 个模型。'),
    ).toBeInTheDocument();
    const post = callsFor(fetchMock, '/admin/api/channels', 'POST')[0];
    expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({
      name: 'relay',
      base_url: 'https://relay.example.com/v1',
      protocol: 'openai_responses',
      key: 'sk-relay-model',
      models: ['gpt-5'],
    });
    const preview = callsFor(fetchMock, '/admin/api/channels/preview-models', 'POST')[0];
    expect(JSON.parse(String(preview?.[1]?.body))).toEqual({
      base_url: 'https://relay.example.com/v1',
      protocol: 'openai_responses',
      key: 'sk-relay-model',
    });
  });

  it('从渠道列表直接定位同页模型，连接表单仍可见', async () => {
    const backend = createBackend([PRIMARY], { 1: SECRET_PRIMARY });
    installFetch(backend.handler);
    const user = userEvent.setup();

    render(<ChannelsPage />);
    await screen.findByText(PRIMARY.key_mask);
    await user.click(screen.getByRole('button', { name: '管理渠道 codex-primary 的模型' }));

    const dialog = await screen.findByRole('dialog', { name: '编辑渠道：codex-primary' });
    expect(within(dialog).queryByRole('tab')).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('Base URL')).toHaveValue(PRIMARY.base_url);
    expect(within(dialog).getByRole('region', { name: '支持模型配置' })).toHaveFocus();
    expect(await within(dialog).findByText('该渠道还没有模型。先同步上游或手动添加。')).toBeInTheDocument();
  });
});
