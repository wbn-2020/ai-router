import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect } from '../../lib/api';
import MappingsPage from './MappingsPage';
import type { ChannelSummary, ModelRoute, ModelRouteInput } from './api';

const CODEX_CHANNEL: ChannelSummary = {
  id: 1,
  name: 'codex-primary',
  protocol: 'openai_responses',
  priority: 10,
  enabled: true,
};

const CHAT_CHANNEL: ChannelSummary = {
  id: 2,
  name: 'chat-backup',
  protocol: 'openai_chat',
  priority: 20,
  enabled: true,
};

const CREATED_AT = '2026-10-30T08:00:00Z';
const UPDATED_AT = '2026-10-30T09:15:00.123456789Z';

const CODEX_ROUTE: ModelRoute = {
  id: 1,
  client_model: 'gpt-5-codex',
  channel_id: 1,
  upstream_model: 'gpt-5.1-codex',
  priority: 0,
  enabled: true,
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

async function expandModel(user: ReturnType<typeof userEvent.setup>, clientModel: string) {
  const group = await screen.findByRole('region', { name: `${clientModel} 模型路由` });
  await user.click(within(group).getByText('展开映射明细'));
  await within(group).findByRole('table', { name: `${clientModel} 的映射明细` });
  return group;
}

function rowOf(button: HTMLElement): HTMLElement {
  const row = button.closest('tr');
  if (!(row instanceof HTMLElement)) {
    throw new Error('未找到映射表格行');
  }
  return row;
}

/**
 * createBackend 模拟 T09 后端：列表、模型去重、创建、PATCH、删除与唯一约束。
 *
 * 模型列表由启用映射及启用渠道实时推导，因此启用/禁用后刷新 /admin/api/models 时，
 * 测试能观察到“模型不再出现”的行为。
 */
function createBackend(initialRoutes: ModelRoute[], channels: ChannelSummary[]) {
  const routes = [...initialRoutes];
  let nextID = 100;

  const deriveModels = () =>
    Array.from(new Set(routes.filter(
      (route) => route.enabled && channels.some(
        (channel) => channel.id === route.channel_id && channel.enabled,
      ),
    ).map((route) => route.client_model)))
      .sort((left, right) => left.localeCompare(right));

  const conflict = () =>
    jsonResponse(
      { error: 'model_route_conflict', message: '同一客户端模型与渠道的映射已存在' },
      409,
    );

  const handler = (url: string, init: RequestInit): Response => {
    const method = init.method ?? 'GET';

    if (url === '/admin/api/model-routes' && method === 'GET') {
      return jsonResponse({ model_routes: routes });
    }
    if (url === '/admin/api/models' && method === 'GET') {
      return jsonResponse({ models: deriveModels() });
    }
    if (url === '/admin/api/channels' && method === 'GET') {
      return jsonResponse({ channels });
    }
    if (url === '/admin/api/model-routes' && method === 'POST') {
      const body = JSON.parse(String(init.body)) as ModelRouteInput;
      if (
        routes.some(
          (route) =>
            route.client_model === body.client_model && route.channel_id === body.channel_id,
        )
      ) {
        return conflict();
      }
      const created: ModelRoute = {
        id: nextID,
        ...body,
        created_at: CREATED_AT,
        updated_at: UPDATED_AT,
      };
      nextID += 1;
      routes.push(created);
      return jsonResponse(created, 201);
    }

    const match = /^\/admin\/api\/model-routes\/(\d+)$/.exec(url);
    if (match !== null) {
      const id = Number(match[1]);
      const index = routes.findIndex((route) => route.id === id);
      const existing = routes[index];
      if (existing === undefined) {
        return jsonResponse(
          { error: 'model_route_not_found', message: '模型映射不存在' },
          404,
        );
      }

      if (method === 'PATCH') {
        const patch = JSON.parse(String(init.body)) as Partial<ModelRouteInput>;
        const nextClientModel = patch.client_model ?? existing.client_model;
        const nextChannelID = patch.channel_id ?? existing.channel_id;
        if (
          routes.some(
            (route) =>
              route.id !== existing.id &&
              route.client_model === nextClientModel &&
              route.channel_id === nextChannelID,
          )
        ) {
          return conflict();
        }
        const next: ModelRoute = {
          ...existing,
          ...patch,
          updated_at: UPDATED_AT,
        };
        routes[index] = next;
        return jsonResponse(next);
      }

      if (method === 'DELETE') {
        routes.splice(index, 1);
        return emptyResponse(204);
      }
    }

    throw new Error(`未预期的请求：${method} ${url}`);
  };

  return { handler, routes, deriveModels };
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

describe('模型映射页', () => {
  it('无映射时显示空态，创建后表格出现与输入一致的新行', async () => {
    const backend = createBackend([], [CODEX_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    expect(await screen.findByText('尚无模型映射')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '创建第一条映射' })).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '创建第一条映射' }));

    expect(screen.getByRole('heading', { name: '新建手工映射' })).toBeInTheDocument();
    expect(screen.getByLabelText('客户端模型')).toHaveAttribute(
      'list',
      'mapping-client-model-options',
    );

    await user.type(screen.getByLabelText('客户端模型'), 'gpt-5-codex');
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.type(screen.getByLabelText('上游模型'), 'gpt-5.1-codex');
    await user.clear(screen.getByLabelText('优先级'));
    await user.type(screen.getByLabelText('优先级'), '7');
    await user.click(screen.getByRole('button', { name: '创建映射' }));

    expect(await screen.findByText('映射已创建。')).toBeInTheDocument();

    const postCalls = callsFor(fetchMock, '/admin/api/model-routes', 'POST');
    expect(postCalls).toHaveLength(1);
    expect(JSON.parse(String(postCalls[0]?.[1]?.body))).toEqual({
      client_model: 'gpt-5-codex',
      channel_id: 1,
      upstream_model: 'gpt-5.1-codex',
      priority: 7,
      enabled: true,
    });

    const group = await expandModel(user, 'gpt-5-codex');
    const editButton = await screen.findByRole('button', {
      name: '编辑映射 gpt-5-codex → codex-primary',
    });
    const row = rowOf(editButton);
    expect(within(row).getByText('gpt-5-codex')).toBeInTheDocument();
    expect(within(row).getByText('codex-primary')).toBeInTheDocument();
    expect(within(row).getByText('gpt-5.1-codex')).toBeInTheDocument();
    expect(within(row).getByText('7')).toBeInTheDocument();
    expect(within(row).getByText('启用')).toBeInTheDocument();
    expect(row.querySelector(`time[datetime="${UPDATED_AT}"]`)).not.toBeNull();
    expect(within(group).getByText('已配置 1 条')).toBeInTheDocument();
    expect(within(group).getByText('可用 1 条')).toBeInTheDocument();

    // 创建后刷新 /admin/api/models：启用的模型应立即可用。
    expect(hasRequest(fetchMock, '/admin/api/models', 'GET')).toBe(true);
    expect(
      await screen.findByText('gpt-5-codex', { selector: '.badge' }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/暂无已发布的客户端模型/)).not.toBeInTheDocument();
  });

  it('重复客户端模型+渠道返回冲突提示，不新增行且保留表单', async () => {
    const backend = createBackend([CODEX_ROUTE], [CODEX_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    await expandModel(user, 'gpt-5-codex');
    await user.click(screen.getByRole('button', { name: '高级手工映射' }));

    await user.type(screen.getByLabelText('客户端模型'), 'gpt-5-codex');
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.type(screen.getByLabelText('上游模型'), 'another-upstream');
    await user.click(screen.getByRole('button', { name: '创建映射' }));

    expect(
      await screen.findByText(/同一客户端模型与渠道的映射已存在/),
    ).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/model-routes', 'POST')).toBe(true);
    expect(screen.getByRole('heading', { name: '新建手工映射' })).toBeInTheDocument();
    expect(screen.queryByText('映射已创建。')).not.toBeInTheDocument();
    expect(screen.queryByText('another-upstream')).not.toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /^编辑映射 / })).toHaveLength(1);
    expect(backend.routes).toHaveLength(1);
  });

  it('禁用映射后行状态变为禁用，并刷新模型列表移除无启用路由的模型', async () => {
    const backend = createBackend([CODEX_ROUTE], [CODEX_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    await expandModel(user, 'gpt-5-codex');
    const row = rowOf(
      await screen.findByRole('button', { name: '编辑映射 gpt-5-codex → codex-primary' }),
    );
    expect(within(row).getByText('启用')).toBeInTheDocument();
    expect(screen.queryByText(/暂无已发布的客户端模型/)).not.toBeInTheDocument();

    const modelListCallsBefore = callsFor(fetchMock, '/admin/api/models', 'GET').length;
    await user.click(
      screen.getByRole('button', { name: '禁用映射 gpt-5-codex → codex-primary' }),
    );

    expect(await screen.findByText('映射已禁用。')).toBeInTheDocument();

    const patchCalls = callsFor(fetchMock, '/admin/api/model-routes/1', 'PATCH');
    expect(patchCalls).toHaveLength(1);
    expect(JSON.parse(String(patchCalls[0]?.[1]?.body))).toEqual({ enabled: false });

    const disabledRow = rowOf(
      screen.getByRole('button', { name: '编辑映射 gpt-5-codex → codex-primary' }),
    );
    expect(within(disabledRow).getByText('禁用')).toBeInTheDocument();
    expect(screen.getByText('全部停用')).toBeInTheDocument();
    expect(screen.getByText('已配置 1 条')).toBeInTheDocument();
    expect(screen.getByText('可用 0 条')).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: '启用映射 gpt-5-codex → codex-primary' }),
    ).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByText(/暂无已发布的客户端模型/)).toBeInTheDocument();
    });
    expect(callsFor(fetchMock, '/admin/api/models', 'GET').length).toBeGreaterThan(
      modelListCallsBefore,
    );
    expect(backend.deriveModels()).toEqual([]);
  });

  it('默认聚合且不挂载明细，展开后按优先级与 id 排序，筛选只统计匹配路由', async () => {
    const routes: ModelRoute[] = [
      {
        ...CODEX_ROUTE,
        id: 2,
        channel_id: 2,
        upstream_model: 'gpt-5.1-codex-chat',
        priority: 5,
      },
      CODEX_ROUTE,
      {
        id: 3,
        client_model: 'gpt-4o-mini',
        channel_id: 2,
        upstream_model: 'gpt-4o-mini-2026',
        priority: 0,
        enabled: true,
        created_at: CREATED_AT,
        updated_at: UPDATED_AT,
      },
      {
        ...CODEX_ROUTE,
        id: 4,
        channel_id: 3,
        upstream_model: 'gpt-5.1-codex-tie',
        priority: 0,
      },
    ];
    const thirdChannel = { ...CHAT_CHANNEL, id: 3, name: 'chat-third' };
    const backend = createBackend(routes, [CODEX_CHANNEL, CHAT_CHANNEL, thirdChannel]);
    installFetch(backend.handler);
    const user = userEvent.setup();

    const { container } = render(<MappingsPage />);

    const codexGroup = await screen.findByRole('region', { name: 'gpt-5-codex 模型路由' });
    const miniGroup = screen.getByRole('region', { name: 'gpt-4o-mini 模型路由' });
    expect(screen.getAllByRole('heading', { level: 3 }).map((heading) => heading.textContent))
      .toEqual(['gpt-4o-mini', 'gpt-5-codex']);
    expect(within(codexGroup).getByText('已配置 3 条')).toBeInTheDocument();
    expect(within(codexGroup).getByText('可用 3 条')).toBeInTheDocument();
    expect(within(miniGroup).getByText('已配置 1 条')).toBeInTheDocument();
    expect(within(codexGroup).getByText('codex-primary · openai_responses')).toBeInTheDocument();
    expect(within(codexGroup).getByText('chat-backup · openai_chat')).toBeInTheDocument();
    expect(container.querySelectorAll('details[open]')).toHaveLength(0);
    expect(container.querySelectorAll('table')).toHaveLength(0);
    expect(screen.queryByRole('button', { name: /^编辑映射 / })).not.toBeInTheDocument();
    expect(screen.queryByText('gpt-5.1-codex')).not.toBeInTheDocument();

    await expandModel(user, 'gpt-5-codex');
    expect(screen.getAllByRole('button', { name: /^编辑映射 / }).map((button) => button.getAttribute('aria-label')))
      .toEqual([
        '编辑映射 gpt-5-codex → codex-primary',
        '编辑映射 gpt-5-codex → chat-third',
        '编辑映射 gpt-5-codex → chat-backup',
      ]);
    expect(within(miniGroup).queryByRole('table')).not.toBeInTheDocument();
    await user.click(within(codexGroup).getByText('收起映射明细'));
    await waitFor(() => expect(container.querySelectorAll('table')).toHaveLength(0));
    await expandModel(user, 'gpt-5-codex');
    await expandModel(user, 'gpt-4o-mini');

    await user.selectOptions(screen.getByLabelText('按客户端模型筛选'), 'gpt-5-codex');
    expect(screen.getAllByRole('button', { name: /^编辑映射 / })).toHaveLength(3);
    expect(screen.queryByRole('region', { name: 'gpt-4o-mini 模型路由' })).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('按渠道筛选'), '2');
    expect(screen.getAllByRole('button', { name: /^编辑映射 / })).toHaveLength(1);
    expect(within(codexGroup).getByText('已配置 1 条')).toBeInTheDocument();
    expect(within(codexGroup).getByText('可用 1 条')).toBeInTheDocument();
    expect(within(codexGroup).getByText('可用协议：openai_chat')).toBeInTheDocument();
    expect(within(codexGroup).queryByText('codex-primary · openai_responses')).not.toBeInTheDocument();
    expect(screen.getByText('1 / 4 条映射')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '编辑映射 gpt-5-codex → chat-backup' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '编辑映射 gpt-5-codex → codex-primary' })).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('按客户端模型筛选'), 'all');
    expect(await screen.findByRole('button', { name: '编辑映射 gpt-4o-mini → chat-backup' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /^编辑映射 / })).toHaveLength(2);
    expect(screen.getByText('2 / 4 条映射')).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('按客户端模型筛选'), 'gpt-4o-mini');
    await user.selectOptions(screen.getByLabelText('按渠道筛选'), '1');
    expect(screen.getByText('没有符合筛选条件的映射')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '清除筛选' }));
    expect(screen.getByLabelText('按客户端模型筛选')).toHaveValue('all');
    expect(screen.getByLabelText('按渠道筛选')).toHaveValue('all');
    expect(await screen.findByRole('region', { name: 'gpt-5-codex 模型路由' })).toBeInTheDocument();

    // 客户端模型建议来自 /admin/api/models：datalist 必须包含已发布模型。
    await user.click(screen.getByRole('button', { name: '高级手工映射' }));
    const options = Array.from(
      container.querySelectorAll('#mapping-client-model-options option'),
    ).map((option) => option.getAttribute('value'));
    expect(options).toEqual(['gpt-4o-mini', 'gpt-5-codex']);
  });

  it('聚合保留停用映射与已删除渠道，可用数同时依赖映射和渠道启用', async () => {
    const routes: ModelRoute[] = [
      CODEX_ROUTE,
      { ...CODEX_ROUTE, id: 2, channel_id: 2, enabled: false },
      { ...CODEX_ROUTE, id: 3, channel_id: 99 },
      { ...CODEX_ROUTE, id: 4, client_model: 'retired-model', channel_id: 2, enabled: false },
    ];
    const backend = createBackend(routes, [{ ...CODEX_CHANNEL, enabled: false }, CHAT_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    const group = await screen.findByRole('region', { name: 'gpt-5-codex 模型路由' });
    expect(within(group).getByText('已配置 3 条')).toBeInTheDocument();
    expect(within(group).getByText('可用 0 条')).toBeInTheDocument();
    expect(within(group).getByText('无可用路由')).toBeInTheDocument();
    expect(within(group).queryByText('全部停用')).not.toBeInTheDocument();
    expect(within(group).getByText('codex-primary · openai_responses · 渠道已停用')).toBeInTheDocument();
    expect(within(group).getByText('chat-backup · openai_chat · 映射已停用')).toBeInTheDocument();
    expect(within(group).getByText('已删除或未知渠道 #99')).toBeInTheDocument();
    expect(within(group).queryByText(/^可用协议：/)).not.toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'retired-model 模型路由' })).getByText('全部停用'))
      .toBeInTheDocument();
    expect(screen.getByText(/暂无已发布的客户端模型/)).toBeInTheDocument();
    expect(backend.deriveModels()).toEqual([]);
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    await expandModel(user, 'gpt-5-codex');
    const disabledChannelRow = rowOf(screen.getByRole('button', {
      name: '编辑映射 gpt-5-codex → codex-primary',
    }));
    expect(within(disabledChannelRow).getByText('启用')).toBeInTheDocument();
    expect(within(disabledChannelRow).getByText('渠道已停用')).toBeInTheDocument();
    const deletedChannelRow = rowOf(screen.getByRole('button', {
      name: '编辑映射 gpt-5-codex → 渠道 #99',
    }));
    expect(within(deletedChannelRow).getByText('已删除或未知渠道')).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('按渠道筛选'), '1');
    expect(within(group).getByText('已配置 1 条')).toBeInTheDocument();
    expect(within(group).getByText('可用 0 条')).toBeInTheDocument();
    expect(within(group).queryByText('已删除或未知渠道 #99')).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'retired-model 模型路由' })).not.toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /^编辑映射 / })).toHaveLength(1);
    expect(callsFor(fetchMock, '/admin/api/model-routes', 'GET')).toHaveLength(1);
    expect(callsFor(fetchMock, '/admin/api/models', 'GET')).toHaveLength(1);
  });

  it('展开后启用映射，并通过取消及确认删除保留既有 CRUD 与模型刷新', async () => {
    const backend = createBackend([{ ...CODEX_ROUTE, enabled: false }], [CODEX_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    const group = await expandModel(user, 'gpt-5-codex');
    expect(within(group).getByText('全部停用')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '启用映射 gpt-5-codex → codex-primary' }));
    expect(await screen.findByText('映射已启用。')).toBeInTheDocument();
    expect(within(group).getByText('可用 1 条')).toBeInTheDocument();
    expect(within(group).queryByText('全部停用')).not.toBeInTheDocument();
    const patchCalls = callsFor(fetchMock, '/admin/api/model-routes/1', 'PATCH');
    expect(patchCalls).toHaveLength(1);
    expect(JSON.parse(String(patchCalls[0]?.[1]?.body))).toEqual({ enabled: true });

    await user.click(screen.getByRole('button', { name: '删除映射 gpt-5-codex → codex-primary' }));
    expect(hasRequest(fetchMock, '/admin/api/model-routes/1', 'DELETE')).toBe(false);
    await user.click(screen.getByRole('button', { name: '取消删除映射 gpt-5-codex → codex-primary' }));
    expect(screen.queryByRole('button', { name: '确认删除映射 gpt-5-codex → codex-primary' })).not.toBeInTheDocument();
    expect(backend.routes).toHaveLength(1);

    await user.click(screen.getByRole('button', { name: '删除映射 gpt-5-codex → codex-primary' }));
    await user.click(screen.getByRole('button', { name: '确认删除映射 gpt-5-codex → codex-primary' }));
    expect(await screen.findByText('映射已删除。')).toBeInTheDocument();
    expect(callsFor(fetchMock, '/admin/api/model-routes/1', 'DELETE')).toHaveLength(1);
    expect(screen.getByText('尚无模型映射')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'gpt-5-codex 模型路由' })).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/暂无已发布的客户端模型/)).toBeInTheDocument());
    expect(callsFor(fetchMock, '/admin/api/models', 'GET')).toHaveLength(3);
    expect(backend.routes).toEqual([]);
  });

  it('编辑映射时表单预填，保存后行内上游模型与优先级更新', async () => {
    const backend = createBackend([CODEX_ROUTE], [CODEX_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    await expandModel(user, 'gpt-5-codex');
    const editButton = await screen.findByRole('button', {
      name: '编辑映射 gpt-5-codex → codex-primary',
    });
    await user.click(editButton);

    expect(screen.getByRole('heading', { name: '编辑映射：gpt-5-codex' })).toBeInTheDocument();
    expect(screen.getByLabelText('客户端模型')).toHaveValue('gpt-5-codex');
    expect(screen.getByLabelText('渠道')).toHaveValue('1');
    expect(screen.getByLabelText('上游模型')).toHaveValue('gpt-5.1-codex');
    expect(screen.getByLabelText('优先级')).toHaveValue(0);

    await user.clear(screen.getByLabelText('上游模型'));
    await user.type(screen.getByLabelText('上游模型'), 'gpt-5.2-codex');
    await user.clear(screen.getByLabelText('优先级'));
    await user.type(screen.getByLabelText('优先级'), '25');
    await user.click(screen.getByRole('button', { name: '保存修改' }));

    expect(await screen.findByText('映射已更新。')).toBeInTheDocument();

    const patchCalls = callsFor(fetchMock, '/admin/api/model-routes/1', 'PATCH');
    expect(patchCalls).toHaveLength(1);
    expect(JSON.parse(String(patchCalls[0]?.[1]?.body))).toEqual({
      client_model: 'gpt-5-codex',
      channel_id: 1,
      upstream_model: 'gpt-5.2-codex',
      priority: 25,
      enabled: true,
    });

    const row = rowOf(
      screen.getByRole('button', { name: '编辑映射 gpt-5-codex → codex-primary' }),
    );
    expect(within(row).getByText('gpt-5.2-codex')).toBeInTheDocument();
    expect(within(row).getByText('25')).toBeInTheDocument();
  });

  it('表单校验空字段与优先级范围，不发送创建请求', async () => {
    const backend = createBackend([], [CODEX_CHANNEL]);
    const fetchMock = installFetch(backend.handler);
    const user = userEvent.setup();

    render(<MappingsPage />);

    await screen.findByText('尚无模型映射');
    await user.click(screen.getByRole('button', { name: '创建第一条映射' }));
    await user.click(screen.getByRole('button', { name: '创建映射' }));

    expect(await screen.findByText('请输入客户端模型名')).toBeInTheDocument();
    expect(screen.getByText('请选择渠道', { selector: '.form-error' })).toBeInTheDocument();
    expect(screen.getByText('请输入上游模型名')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/model-routes', 'POST')).toBe(false);

    await user.type(screen.getByLabelText('客户端模型'), 'gpt-5-codex');
    await user.selectOptions(screen.getByLabelText('渠道'), '1');
    await user.type(screen.getByLabelText('上游模型'), 'gpt-5.1-codex');
    await user.clear(screen.getByLabelText('优先级'));
    await user.type(screen.getByLabelText('优先级'), '1200');
    await user.click(screen.getByRole('button', { name: '创建映射' }));

    expect(await screen.findByText('优先级必须是 0 到 1000 的整数')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/model-routes', 'POST')).toBe(false);
  });

  it('初始化 401 时跳转登录且不渲染映射内容', async () => {
    installFetch(() => jsonResponse({ error: 'unauthorized', message: '需要管理员登录' }, 401));

    render(<MappingsPage />);

    expect(await screen.findByText('登录状态已失效')).toBeInTheDocument();
    expect(redirectSpy).toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: '高级手工映射' })).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});
