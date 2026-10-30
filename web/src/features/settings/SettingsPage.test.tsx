import '@testing-library/jest-dom/vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { setLoginRedirect, type AdminSessionView } from '../../lib/api';
import SettingsPage from './SettingsPage';

const CURRENT_SESSION: AdminSessionView = {
  id: 'session-current',
  created_at: '2026-10-30T08:00:00Z',
  last_seen_at: '2026-10-30T09:30:00Z',
  expires_at: '2026-10-30T20:00:00Z',
  current: true,
};

const OTHER_SESSION: AdminSessionView = {
  id: 'session-other',
  created_at: '2026-10-29T10:00:00.123456789Z',
  last_seen_at: '2026-10-29T11:00:00Z',
  expires_at: '2026-10-29T22:00:00Z',
  current: false,
};

const THIRD_SESSION: AdminSessionView = {
  id: 'session-third',
  created_at: '2026-10-28T06:00:00Z',
  last_seen_at: '2026-10-28T07:00:00Z',
  expires_at: '2026-10-28T18:00:00Z',
  current: false,
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

function sessions(count: number): Response {
  const list =
    count === 1
      ? [CURRENT_SESSION]
      : count === 2
        ? [CURRENT_SESSION, OTHER_SESSION]
        : [CURRENT_SESSION, OTHER_SESSION, THIRD_SESSION];
  return jsonResponse({ sessions: list });
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

describe('设置页：改密与会话撤销', () => {
  it('加载活跃会话，只显示创建/最后使用/过期时间而不暴露 session id', async () => {
    const fetchMock = installFetch((url) => {
      if (url === '/admin/api/auth/sessions') {
        return sessions(2);
      }
      throw new Error(`未预期的请求：${url}`);
    });

    const { container } = render(<SettingsPage />);

    expect(await screen.findByRole('table')).toBeInTheDocument();
    expect(screen.getByText('活跃管理员会话')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '会话' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '创建时间' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '最后使用' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '过期时间' })).toBeInTheDocument();

    expect(container.querySelectorAll('time')).toHaveLength(6);
    expect(
      container.querySelector('time[datetime="2026-10-30T08:00:00Z"]'),
    ).not.toBeNull();
    expect(
      container.querySelector('time[datetime="2026-10-29T10:00:00.123456789Z"]'),
    ).not.toBeNull();
    expect(
      container.querySelector('time[datetime="2026-10-29T22:00:00Z"]'),
    ).not.toBeNull();

    expect(screen.getByText('当前会话')).toBeInTheDocument();
    expect(screen.getByText('其他会话')).toBeInTheDocument();
    expect(container.textContent).not.toContain(CURRENT_SESSION.id);
    expect(container.textContent).not.toContain(OTHER_SESSION.id);

    expect(fetchMock).toHaveBeenCalledWith(
      '/admin/api/auth/sessions',
      expect.objectContaining({ credentials: 'include', cache: 'no-store' }),
    );
    expect(hasRequest(fetchMock, '/admin/api/auth/sessions', 'GET')).toBe(true);
  });

  it('改密成功后清空表单并刷新列表，其他会话被撤销', async () => {
    let listCalls = 0;
    const fetchMock = installFetch((url, init) => {
      if (url === '/admin/api/auth/sessions' && init.method !== 'DELETE') {
        listCalls += 1;
        return sessions(listCalls === 1 ? 2 : 1);
      }
      if (url === '/admin/api/auth/password') {
        expect(init.method).toBe('POST');
        expect(JSON.parse(String(init.body))).toEqual({
          current_password: 'old-password-123',
          new_password: 'new-password-456',
        });
        return jsonResponse({ ok: true });
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    const { container } = render(<SettingsPage />);

    await screen.findByRole('table');
    expect(
      container.querySelector('time[datetime="2026-10-29T10:00:00.123456789Z"]'),
    ).not.toBeNull();

    await user.type(screen.getByLabelText('当前密码'), 'old-password-123');
    await user.type(screen.getByLabelText('新密码'), 'new-password-456');
    await user.type(screen.getByLabelText('确认新密码'), 'new-password-456');
    await user.click(screen.getByRole('button', { name: '修改密码' }));

    expect(await screen.findByText('密码已更新，其他会话已被撤销。')).toBeInTheDocument();
    await waitFor(() => {
      expect(
        container.querySelector('time[datetime="2026-10-29T10:00:00.123456789Z"]'),
      ).toBeNull();
    });
    expect(screen.getByLabelText('当前密码')).toHaveValue('');
    expect(screen.getByLabelText('新密码')).toHaveValue('');
    expect(screen.getByLabelText('确认新密码')).toHaveValue('');
    expect(listCalls).toBe(2);
    expect(hasRequest(fetchMock, '/admin/api/auth/password', 'POST')).toBe(true);
  });

  it('前端校验 12 字符与确认密码，不发送改密请求', async () => {
    const fetchMock = installFetch((url) => {
      if (url === '/admin/api/auth/sessions') {
        return sessions(1);
      }
      throw new Error(`未预期的请求：${url}`);
    });
    const user = userEvent.setup();

    render(<SettingsPage />);
    await screen.findByRole('table');

    await user.type(screen.getByLabelText('当前密码'), 'old-password-123');
    await user.type(screen.getByLabelText('新密码'), 'too-short');
    await user.type(screen.getByLabelText('确认新密码'), 'too-short');
    await user.click(screen.getByRole('button', { name: '修改密码' }));

    expect(await screen.findByText('新密码至少 12 个字符')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/auth/password', 'POST')).toBe(false);

    await user.clear(screen.getByLabelText('新密码'));
    await user.type(screen.getByLabelText('新密码'), 'long-enough-password');
    await user.clear(screen.getByLabelText('确认新密码'));
    await user.type(screen.getByLabelText('确认新密码'), 'different-password');
    await user.click(screen.getByRole('button', { name: '修改密码' }));

    expect(await screen.findByText('两次输入的新密码不一致')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/auth/password', 'POST')).toBe(false);
  });

  it('改密失败时显示服务端安全文案并保留输入', async () => {
    const fetchMock = installFetch((url, init) => {
      if (url === '/admin/api/auth/sessions') {
        return sessions(1);
      }
      if (url === '/admin/api/auth/password' && init.method === 'POST') {
        return jsonResponse(
          { error: 'invalid_current_password', message: '当前密码不正确' },
          400,
        );
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<SettingsPage />);
    await screen.findByRole('table');

    await user.type(screen.getByLabelText('当前密码'), 'wrong-current-password');
    await user.type(screen.getByLabelText('新密码'), 'new-password-456');
    await user.type(screen.getByLabelText('确认新密码'), 'new-password-456');
    await user.click(screen.getByRole('button', { name: '修改密码' }));

    expect(await screen.findByText('当前密码不正确')).toBeInTheDocument();
    expect(screen.getByLabelText('当前密码')).toHaveValue('wrong-current-password');
    expect(screen.getByLabelText('新密码')).toHaveValue('new-password-456');
    expect(hasRequest(fetchMock, '/admin/api/auth/password', 'POST')).toBe(true);
  });

  it('撤销单个会话后调用 DELETE 并让该行消失', async () => {
    let listCalls = 0;
    const fetchMock = installFetch((url, init) => {
      if (url === '/admin/api/auth/sessions' && init.method !== 'DELETE') {
        listCalls += 1;
        return sessions(listCalls === 1 ? 2 : 1);
      }
      if (url === `/admin/api/auth/sessions/${OTHER_SESSION.id}`) {
        expect(init.method).toBe('DELETE');
        return jsonResponse({ revoked: 1 });
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    const { container } = render(<SettingsPage />);
    await screen.findByRole('table');

    await user.click(screen.getByRole('button', { name: /撤销创建于/ }));

    expect(await screen.findByText('会话已撤销。')).toBeInTheDocument();
    await waitFor(() => {
      expect(
        container.querySelector('time[datetime="2026-10-29T10:00:00.123456789Z"]'),
      ).toBeNull();
    });
    expect(
      hasRequest(fetchMock, `/admin/api/auth/sessions/${OTHER_SESSION.id}`, 'DELETE'),
    ).toBe(true);
    expect(listCalls).toBe(2);
  });

  it('撤销其他全部会话需要二次确认，确认后仅保留当前会话', async () => {
    let listCalls = 0;
    const fetchMock = installFetch((url, init) => {
      if (url === '/admin/api/auth/sessions' && init.method !== 'DELETE') {
        listCalls += 1;
        return sessions(listCalls === 1 ? 3 : 1);
      }
      if (url === '/admin/api/auth/sessions' && init.method === 'DELETE') {
        return jsonResponse({ revoked: 2 });
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    const { container } = render(<SettingsPage />);
    await screen.findByRole('table');

    await user.click(screen.getByRole('button', { name: '撤销其他全部会话' }));

    expect(screen.getByText('将撤销 2 个其他会话。')).toBeInTheDocument();
    expect(hasRequest(fetchMock, '/admin/api/auth/sessions', 'DELETE')).toBe(false);

    await user.click(screen.getByRole('button', { name: '确认撤销' }));

    expect(await screen.findByText('已撤销 2 个其他会话。')).toBeInTheDocument();
    await waitFor(() => {
      expect(
        container.querySelector('time[datetime="2026-10-29T10:00:00.123456789Z"]'),
      ).toBeNull();
      expect(
        container.querySelector('time[datetime="2026-10-28T06:00:00Z"]'),
      ).toBeNull();
    });
    expect(hasRequest(fetchMock, '/admin/api/auth/sessions', 'DELETE')).toBe(true);
    expect(listCalls).toBe(2);
  });

  it('登出调用 POST /logout 并跳转登录', async () => {
    const fetchMock = installFetch((url, init) => {
      if (url === '/admin/api/auth/sessions') {
        return sessions(1);
      }
      if (url === '/admin/api/auth/logout' && init.method === 'POST') {
        return jsonResponse({ ok: true });
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<SettingsPage />);
    await screen.findByRole('table');

    await user.click(screen.getByRole('button', { name: '登出当前会话' }));

    await waitFor(() => {
      expect(redirectSpy).toHaveBeenCalledTimes(1);
    });
    expect(hasRequest(fetchMock, '/admin/api/auth/logout', 'POST')).toBe(true);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: '登出当前会话' })).toBeEnabled();
    });
  });

  it('会话接口 401 时统一跳登录且不渲染受保护内容', async () => {
    installFetch(() =>
      jsonResponse({ error: 'unauthorized', message: '需要管理员登录' }, 401),
    );

    render(<SettingsPage />);

    expect(await screen.findByText('登录状态已失效')).toBeInTheDocument();
    expect(redirectSpy).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('当前密码')).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: '登出当前会话' }),
    ).not.toBeInTheDocument();
  });

  it('操作中途 401 时清空受保护内容并跳转登录', async () => {
    let listCalls = 0;
    installFetch((url, init) => {
      if (url === '/admin/api/auth/sessions' && init.method !== 'DELETE') {
        listCalls += 1;
        if (listCalls === 1) {
          return sessions(2);
        }
      }
      if (url === `/admin/api/auth/sessions/${OTHER_SESSION.id}`) {
        return jsonResponse({ error: 'unauthorized', message: 'session 无效或已过期' }, 401);
      }
      throw new Error(`未预期的请求：${init.method ?? 'GET'} ${url}`);
    });
    const user = userEvent.setup();

    render(<SettingsPage />);
    await screen.findByRole('table');

    await user.click(screen.getByRole('button', { name: /撤销创建于/ }));

    expect(await screen.findByText('登录状态已失效')).toBeInTheDocument();
    expect(redirectSpy).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('当前密码')).not.toBeInTheDocument();
  });
});
