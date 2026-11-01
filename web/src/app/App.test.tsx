import '@testing-library/jest-dom/vitest';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import packageJSON from '../../package.json';
import { App, type AdminSession } from './App';
import { THEME_STORAGE_KEY } from '../lib/theme';

const ADMIN_SESSION: AdminSession = {
  authenticated: true,
  admin: { id: 1, role: 'admin' },
  session: {
    id: 'sess_test',
    created_at: '2026-10-30T08:00:00Z',
    last_seen_at: '2026-10-30T09:00:00Z',
    expires_at: '2026-10-30T20:00:00Z',
    current: true,
  },
};

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
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

function installFetch(handler: (url: string, init: RequestInit | undefined) => Response) {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) =>
    handler(requestURL(input), init),
  );
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function unauthorized(): Response {
  return jsonResponse({ error: 'unauthorized', message: '需要管理员登录' }, 401);
}

beforeEach(() => {
  window.history.replaceState({}, '', '/admin/');
  vi.stubGlobal('scrollTo', vi.fn());
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
  delete document.documentElement.dataset.themePreference;
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
  delete document.documentElement.dataset.themePreference;
});

describe('外观主题', () => {
  function mockSystemTheme(initialDark: boolean) {
    let dark = initialDark;
    const listeners = new Set<() => void>();
    const media = {
      get matches() { return dark; },
      addEventListener: vi.fn((_event: string, listener: () => void) => listeners.add(listener)),
      removeEventListener: vi.fn((_event: string, listener: () => void) => listeners.delete(listener)),
    };
    vi.stubGlobal('matchMedia', vi.fn(() => media));
    return {
      media,
      change(value: boolean) {
        dark = value;
        act(() => listeners.forEach((listener) => listener()));
      },
      listenerCount: () => listeners.size,
    };
  }

  it('默认跟随系统，在系统变化时更新并卸载监听', async () => {
    const system = mockSystemTheme(true);
    installFetch(() => unauthorized());
    const view = render(<App />);
    const control = await screen.findByRole('combobox', { name: '外观主题' });

    expect(control).toHaveValue('system');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
    system.change(false);
    expect(document.documentElement).toHaveAttribute('data-theme', 'light');
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBeNull();

    view.unmount();
    expect(system.listenerCount()).toBe(0);
  });

  it('手动偏好优先于系统，并在重挂载后保留', async () => {
    const system = mockSystemTheme(true);
    window.localStorage.setItem(THEME_STORAGE_KEY, 'light');
    installFetch(() => unauthorized());
    const user = userEvent.setup();
    const view = render(<App />);

    const control = await screen.findByRole('combobox', { name: '外观主题' });
    expect(control).toHaveValue('light');
    expect(document.documentElement).toHaveAttribute('data-theme', 'light');
    await user.selectOptions(control, 'dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe('dark');
    system.change(false);
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');

    view.unmount();
    render(<App />);
    expect(await screen.findByRole('combobox', { name: '外观主题' })).toHaveValue('dark');
  });

  it('存储不可用或内容非法时仍可切换', async () => {
    mockSystemTheme(false);
    window.localStorage.setItem(THEME_STORAGE_KEY, 'invalid');
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    installFetch(() => unauthorized());
    const user = userEvent.setup();
    render(<App />);

    const control = await screen.findByRole('combobox', { name: '外观主题' });
    expect(control).toHaveValue('system');
    await user.selectOptions(control, 'dark');
    expect(control).toHaveValue('dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
  });

  it('其他页签修改偏好时同步主题', async () => {
    mockSystemTheme(false);
    installFetch(() => unauthorized());
    render(<App />);
    await screen.findByRole('combobox', { name: '外观主题' });

    window.localStorage.setItem(THEME_STORAGE_KEY, 'dark');
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: THEME_STORAGE_KEY, newValue: 'dark' }));
    });
    expect(screen.getByRole('combobox', { name: '外观主题' })).toHaveValue('dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
  });

  it('切换主题后登录和登出不丢失偏好', async () => {
    mockSystemTheme(false);
    installFetch((url) => {
      if (url.endsWith('/admin/api/auth/login')) return jsonResponse(ADMIN_SESSION);
      if (url.endsWith('/admin/api/auth/session')) return unauthorized();
      if (url.endsWith('/admin/api/auth/logout')) return jsonResponse({ ok: true });
      return jsonResponse({ error: 'not_found' }, 404);
    });
    const user = userEvent.setup();
    render(<App />);

    await user.selectOptions(await screen.findByRole('combobox', { name: '外观主题' }), 'dark');
    await user.type(screen.getByLabelText('管理员密码'), 'correct-horse');
    await user.click(screen.getByRole('button', { name: '登录' }));
    await screen.findByRole('navigation', { name: '管理导航' });
    expect(screen.getByRole('combobox', { name: '外观主题' })).toHaveValue('dark');
    await user.click(screen.getByRole('button', { name: '登出' }));
    await screen.findByRole('heading', { name: '管理员登录' });
    expect(screen.getByRole('combobox', { name: '外观主题' })).toHaveValue('dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
  });
});

describe('管理端骨架', () => {
  it('未登录访问管理端根路径时显示登录页，不渲染管理导航', async () => {
    installFetch(() => unauthorized());

    render(<App />);

    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: '管理导航' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: '渠道' })).not.toBeInTheDocument();
    await waitFor(() => {
      expect(window.location.pathname).toBe('/admin/login');
    });
  });

  it('登录成功后显示 6 项侧边栏导航并进入总览', async () => {
    const fetchMock = installFetch((url, init) => {
      if (url.endsWith('/admin/api/auth/session')) {
        return unauthorized();
      }
      if (url.endsWith('/admin/api/auth/login')) {
        expect(init?.method).toBe('POST');
        expect(JSON.parse(String(init?.body))).toEqual({ password: 'correct-horse' });
        return jsonResponse(ADMIN_SESSION);
      }
      return jsonResponse({ error: 'not_found' }, 404);
    });
    const user = userEvent.setup();

    render(<App />);

    const password = await screen.findByLabelText('管理员密码');
    await user.type(password, 'correct-horse');
    await user.click(screen.getByRole('button', { name: '登录' }));

    const nav = await screen.findByRole('navigation', { name: '管理导航' });
    const labels = within(nav)
      .getAllByRole('link')
      .map((link) => (link.textContent ?? '').replace(/\s+/g, ''));
    expect(labels).toEqual(['总览', '渠道', '模型路由', '请求', '调试', '设置']);
    expect(within(nav).queryByRole('link', { name: '登录' })).not.toBeInTheDocument();
    expect(
      await screen.findByRole('heading', { level: 1, name: '总览' }),
    ).toBeInTheDocument();

    const calledURLs = fetchMock.mock.calls.map(([input]) => requestURL(input));
    expect(calledURLs).toContain('/admin/api/auth/session');
    expect(calledURLs).toContain('/admin/api/auth/login');
  });

  it('密码错误时停留在登录页并显示安全错误文案', async () => {
    installFetch((url) => {
      if (url.endsWith('/admin/api/auth/session')) {
        return unauthorized();
      }
      if (url.endsWith('/admin/api/auth/login')) {
        return jsonResponse({ error: 'invalid_credentials', message: '凭据无效' }, 401);
      }
      return jsonResponse({ error: 'not_found' }, 404);
    });
    const user = userEvent.setup();

    render(<App />);

    await user.type(await screen.findByLabelText('管理员密码'), 'wrong-password');
    await user.click(screen.getByRole('button', { name: '登录' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('密码不正确');
    expect(screen.getByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: '管理导航' })).not.toBeInTheDocument();
  });

  it('登出后回到登录页，后续 session 查询返回 401 时保持未登录', async () => {
    let sessionStatus = 200;
    const fetchMock = installFetch((url) => {
      if (url.endsWith('/admin/api/auth/session')) {
        return sessionStatus === 200 ? jsonResponse(ADMIN_SESSION) : unauthorized();
      }
      if (url.endsWith('/admin/api/auth/logout')) {
        return jsonResponse({ ok: true });
      }
      return jsonResponse({ error: 'not_found' }, 404);
    });
    const user = userEvent.setup();

    render(<App />);

    await screen.findByRole('navigation', { name: '管理导航' });
    await user.click(screen.getByRole('button', { name: '登出' }));

    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: '管理导航' })).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input]) => requestURL(input).endsWith('/admin/api/auth/logout')),
    ).toBe(true);

    cleanup();
    sessionStatus = 401;
    window.history.replaceState({}, '', '/admin/');
    render(<App />);

    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: '管理导航' })).not.toBeInTheDocument();
  });

  it.each(['/admin/register', '/admin/payment', '/admin/topup', '/admin/rebate'])(
    '直接访问 %s 显示 404 且不渲染管理页',
    async (path) => {
      installFetch(() => jsonResponse(ADMIN_SESSION));
      window.history.replaceState({}, '', path);

      render(<App />);

      expect(await screen.findByRole('heading', { name: '页面不存在' })).toBeInTheDocument();
      expect(screen.queryByRole('navigation', { name: '管理导航' })).not.toBeInTheDocument();
      expect(screen.queryByRole('link', { name: '渠道' })).not.toBeInTheDocument();
    },
  );

  it('package.json 声明 React/TypeScript/Vite/Vitest 与 build 脚本', () => {
    expect(packageJSON.scripts.build).toBe('vite build');
    expect(packageJSON.scripts['test:app']).toContain('src/app/App.test.tsx');

    expect(packageJSON.dependencies.react).toMatch(/^19\./);
    expect(packageJSON.dependencies['react-dom']).toMatch(/^19\./);
    expect(packageJSON.devDependencies.typescript).toMatch(/^5\.6\./);
    expect(packageJSON.devDependencies.vite).toMatch(/^5\./);
    expect(packageJSON.devDependencies.vitest).toBeDefined();
    expect(packageJSON.devDependencies['@testing-library/react']).toBeDefined();
    expect(packageJSON.devDependencies['@types/react']).toBeDefined();
  });
});
