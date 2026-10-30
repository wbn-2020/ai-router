import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  Link,
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  useNavigate,
  useRouterState,
} from '@tanstack/react-router';
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type FormEvent,
  type ReactNode,
} from 'react';

/* -------------------------------------------------------------------------- */
/* 管理 API 客户端                                                             */
/* -------------------------------------------------------------------------- */

/** ApiError 保留 HTTP 状态与后端稳定错误码，界面只展示安全文案。 */
class ApiError extends Error {
  readonly status: number;
  readonly code: string | undefined;

  constructor(status: number, code: string | undefined, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

interface ErrorBody {
  error?: string;
  message?: string;
}

async function requestJSON<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {};
  if (init.body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin',
  });

  if (!response.ok) {
    let body: ErrorBody = {};
    try {
      body = (await response.json()) as ErrorBody;
    } catch {
      // 非 JSON 错误体（例如反向代理 HTML）只保留状态码。
    }
    throw new ApiError(
      response.status,
      body.error,
      body.message ?? `请求失败（HTTP ${response.status}）`,
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

/* -------------------------------------------------------------------------- */
/* 认证状态                                                                    */
/* -------------------------------------------------------------------------- */

export interface AdminSession {
  authenticated: boolean;
  admin: { id: number; role: string };
  session: {
    id: string;
    created_at?: string;
    last_seen_at?: string;
    expires_at: string;
    current: boolean;
  };
}

export type AuthSnapshot =
  | { status: 'loading' }
  | { status: 'anonymous'; reason: 'unauthorized' | 'unavailable'; message?: string }
  | { status: 'authenticated'; session: AdminSession };

const SESSION_UNAVAILABLE_MESSAGE = '无法验证登录状态，请确认网关服务正在运行。';

/**
 * AuthController 是单管理员 session 的客户端状态机。
 *
 * session 只存在 HttpOnly Cookie 中，前端不接触 token；这里仅缓存
 * “是否已登录”和展示所需的 session 元数据。
 */
export class AuthController {
  private snapshot: AuthSnapshot = { status: 'loading' };
  private readonly listeners = new Set<() => void>();
  private pending: Promise<AuthSnapshot> | null = null;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = (): AuthSnapshot => this.snapshot;

  ensureSession(): Promise<AuthSnapshot> {
    if (this.snapshot.status !== 'loading') {
      return Promise.resolve(this.snapshot);
    }
    if (this.pending === null) {
      this.pending = requestJSON<AdminSession>('/admin/api/auth/session')
        .then((session) => {
          const next: AuthSnapshot = { status: 'authenticated', session };
          this.setSnapshot(next);
          return next;
        })
        .catch((error: unknown) => {
          const next: AuthSnapshot =
            error instanceof ApiError && error.status === 401
              ? { status: 'anonymous', reason: 'unauthorized' }
              : {
                  status: 'anonymous',
                  reason: 'unavailable',
                  message: SESSION_UNAVAILABLE_MESSAGE,
                };
          this.setSnapshot(next);
          return next;
        })
        .finally(() => {
          this.pending = null;
        });
    }
    return this.pending;
  }

  async retrySession(): Promise<AuthSnapshot> {
    this.pending = null;
    this.setSnapshot({ status: 'loading' });
    return this.ensureSession();
  }

  async login(password: string): Promise<void> {
    const session = await requestJSON<AdminSession>('/admin/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    });
    this.pending = null;
    this.setSnapshot({ status: 'authenticated', session });
  }

  async logout(): Promise<void> {
    try {
      await requestJSON<{ ok: boolean }>('/admin/api/auth/logout', { method: 'POST' });
    } finally {
      this.pending = null;
      this.setSnapshot({ status: 'anonymous', reason: 'unauthorized' });
    }
  }

  private setSnapshot(next: AuthSnapshot): void {
    this.snapshot = next;
    for (const listener of this.listeners) {
      listener();
    }
  }
}

const AuthContext = createContext<AuthController | null>(null);

function useAuth(): AuthController {
  const auth = useContext(AuthContext);
  if (auth === null) {
    throw new Error('useAuth 必须在 AuthContext.Provider 内使用');
  }
  return auth;
}

function useAuthSnapshot(): AuthSnapshot {
  const auth = useAuth();
  return useSyncExternalStore(auth.subscribe, auth.getSnapshot, auth.getSnapshot);
}

function describeLoginError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) {
      return '密码不正确';
    }
    if (error.status === 429) {
      return '尝试过于频繁，请稍后再试';
    }
    if (error.status === 400) {
      return error.message;
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后再试';
    }
  }
  return '登录失败，请检查网络连接后重试';
}

/* -------------------------------------------------------------------------- */
/* Toast                                                                       */
/* -------------------------------------------------------------------------- */

type ToastTone = 'success' | 'error' | 'info';

interface ToastItem {
  id: number;
  message: string;
  tone: ToastTone;
}

interface ToastApi {
  push: (message: string, tone?: ToastTone) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const nextID = useRef(1);
  const timers = useRef<number[]>([]);

  useEffect(() => {
    return () => {
      for (const timer of timers.current) {
        window.clearTimeout(timer);
      }
    };
  }, []);

  const dismiss = useCallback((id: number) => {
    setToasts((current) => current.filter((item) => item.id !== id));
  }, []);

  const push = useCallback((message: string, tone: ToastTone = 'info') => {
    const id = nextID.current;
    nextID.current += 1;
    setToasts((current) => [...current, { id, message, tone }]);

    const timer = window.setTimeout(() => {
      setToasts((current) => current.filter((item) => item.id !== id));
      timers.current = timers.current.filter((item) => item !== timer);
    }, 5000);
    timers.current.push(timer);
  }, []);

  return (
    <ToastContext.Provider value={{ push }}>
      {children}
      <div className="toast-viewport" aria-live="polite" aria-atomic="false">
        {toasts.map((item) => (
          <div
            key={item.id}
            className={`toast toast--${item.tone}`}
            role={item.tone === 'error' ? 'alert' : 'status'}
          >
            <span className="toast-message">{item.message}</span>
            <button
              type="button"
              className="toast-close"
              onClick={() => {
                dismiss(item.id);
              }}
              aria-label={`关闭提示：${item.message}`}
            >
              ×
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

function useToast(): ToastApi {
  const toast = useContext(ToastContext);
  if (toast === null) {
    throw new Error('useToast 必须在 ToastContext.Provider 内使用');
  }
  return toast;
}

/* -------------------------------------------------------------------------- */
/* 导航图标                                                                    */
/* -------------------------------------------------------------------------- */

type IconName = 'dashboard' | 'channels' | 'mappings' | 'requests' | 'debug' | 'settings';

const NAV_ICON_PATHS: Record<IconName, ReactNode> = {
  dashboard: (
    <>
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    </>
  ),
  channels: (
    <>
      <path d="M4 7h9a3 3 0 0 1 3 3v7" />
      <path d="M7 4 4 7l3 3" />
      <circle cx="16" cy="17" r="3" />
    </>
  ),
  mappings: (
    <>
      <path d="M4 7h6a4 4 0 0 1 4 4v7" />
      <path d="M7 4 4 7l3 3" />
      <path d="M17 21l3-3-3-3" />
      <path d="M14 18h6" />
    </>
  ),
  requests: (
    <>
      <rect x="5" y="4" width="14" height="16" rx="2" />
      <path d="M8 9h8M8 13h8M8 17h5" />
    </>
  ),
  debug: (
    <>
      <path d="M9 4h6M12 4v3" />
      <rect x="6" y="7" width="12" height="12" rx="4" />
      <path d="M3 11h3M18 11h3M3 16h3M18 16h3M9 19l-2 2M15 19l2 2" />
    </>
  ),
  settings: (
    <>
      <path d="M4 7h10M18 7h2M4 17h2M10 17h10" />
      <circle cx="16" cy="7" r="2" />
      <circle cx="8" cy="17" r="2" />
    </>
  ),
};

function NavIcon({ name }: { name: IconName }) {
  return (
    <svg
      className="nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {NAV_ICON_PATHS[name]}
    </svg>
  );
}

function MenuIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="20"
      height="20"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M4 6h16M4 12h16M4 18h16" />
    </svg>
  );
}

/* -------------------------------------------------------------------------- */
/* 通用状态组件                                                                */
/* -------------------------------------------------------------------------- */

function FullPageLoading() {
  return (
    <div className="full-page-loading" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      <span>正在验证登录状态…</span>
    </div>
  );
}

function EmptyState({ title, description }: { title: string; description: string }) {
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

function ErrorState({
  title,
  description,
  onRetry,
  retryLabel = '重试',
}: {
  title: string;
  description: string;
  onRetry?: () => void;
  retryLabel?: string;
}) {
  return (
    <div className="state state--error" role="alert">
      <span className="state-icon" aria-hidden="true">
        !
      </span>
      <h2>{title}</h2>
      <p>{description}</p>
      {onRetry === undefined ? null : (
        <button type="button" className="button button--ghost" onClick={onRetry}>
          {retryLabel}
        </button>
      )}
    </div>
  );
}

function PageHeader({ title, description }: { title: string; description: string }) {
  return (
    <header className="page-header">
      <div>
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
    </header>
  );
}

/* -------------------------------------------------------------------------- */
/* 登录页                                                                      */
/* -------------------------------------------------------------------------- */

const LOGIN_FEATURES = [
  {
    title: '单管理员',
    description: '密码登录与可撤销 session，写请求校验同源 Origin。',
  },
  {
    title: '原生协议',
    description: 'Responses 与 Chat 直通上游，不做隐式跨协议转换。',
  },
  {
    title: '可观测',
    description: '默认记录请求元数据与延迟，body 只在显式确认后捕获。',
  },
] as const;

function LoginPage() {
  const auth = useAuth();
  const snapshot = useAuthSnapshot();
  const navigate = useNavigate();
  const toast = useToast();
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (snapshot.status === 'authenticated') {
      void navigate({ to: '/' });
    }
  }, [navigate, snapshot.status]);

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (password.length === 0) {
      setError('请输入管理员密码');
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await auth.login(password);
      toast.push('登录成功', 'success');
    } catch (loginError) {
      setError(describeLoginError(loginError));
    } finally {
      setSubmitting(false);
    }
  };

  const sessionUnavailable =
    snapshot.status === 'anonymous' && snapshot.reason === 'unavailable';

  return (
    <main className="login-page">
      <section className="login-panel" aria-labelledby="login-title">
        <div className="brand brand--login">
          <span className="brand-mark" aria-hidden="true">
            AI
          </span>
          <span className="brand-text">
            <strong>AI Router</strong>
            <span>个人 API 网关</span>
          </span>
        </div>

        <div className="login-copy">
          <p className="eyebrow">单管理员控制台</p>
          <h1 id="login-title">管理员登录</h1>
          <p>使用部署时设置的管理员密码进入管理端。会话保存在 HttpOnly Cookie 中。</p>
        </div>

        {sessionUnavailable ? (
          <ErrorState
            title="无法验证登录状态"
            description={snapshot.message ?? SESSION_UNAVAILABLE_MESSAGE}
            onRetry={() => {
              void auth.retrySession();
            }}
            retryLabel="重新检查"
          />
        ) : null}

        {snapshot.status === 'loading' ? (
          <p className="form-note" role="status">
            正在重新检查登录状态…
          </p>
        ) : null}

        <form className="login-form" onSubmit={handleSubmit} noValidate>
          <div className="field">
            <label htmlFor="admin-password">管理员密码</label>
            <input
              id="admin-password"
              className="input"
              type="password"
              name="password"
              autoComplete="current-password"
              autoFocus
              value={password}
              onChange={(event) => {
                setPassword(event.target.value);
              }}
              aria-invalid={error !== null}
              aria-describedby={error === null ? undefined : 'login-error'}
            />
          </div>

          {error === null ? null : (
            <p className="form-error" id="login-error" role="alert">
              {error}
            </p>
          )}

          <button type="submit" className="button button--block" disabled={submitting}>
            {submitting ? '正在登录…' : '登录'}
          </button>
        </form>

        <p className="login-footnote">管理员凭据由部署者初始化；本管理端没有自助入口。</p>
      </section>

      <aside className="login-aside" aria-label="网关特性">
        <p className="eyebrow eyebrow--light">AI Router</p>
        <h2>个人 AI API 网关</h2>
        <p className="aside-lead">
          为 Codex 与 OpenAI 兼容客户端提供单入口转发，配置简单，状态集中。
        </p>
        <ul className="feature-list">
          {LOGIN_FEATURES.map((feature) => (
            <li key={feature.title}>
              <strong>{feature.title}</strong>
              <span>{feature.description}</span>
            </li>
          ))}
        </ul>
        <p className="aside-note">本机优先，可迁移 Linux / Docker；数据落在单个 SQLite 文件。</p>
      </aside>
    </main>
  );
}

/* -------------------------------------------------------------------------- */
/* 受保护布局                                                                  */
/* -------------------------------------------------------------------------- */

const NAV_ITEMS = [
  { to: '/', label: '总览', icon: 'dashboard' },
  { to: '/channels', label: '渠道', icon: 'channels' },
  { to: '/mappings', label: '模型映射', icon: 'mappings' },
  { to: '/requests', label: '请求', icon: 'requests' },
  { to: '/debug', label: '调试', icon: 'debug' },
  { to: '/settings', label: '设置', icon: 'settings' },
] as const;

const SECTION_TITLES = [
  ['/channels', '渠道'],
  ['/mappings', '模型映射'],
  ['/requests', '请求观测'],
  ['/debug', '调试'],
  ['/settings', '设置'],
] as const;

function sectionTitle(pathname: string): string {
  const normalized = pathname.replace(/^\/admin(?=\/|$)/, '') || '/';
  if (normalized === '/') {
    return '总览';
  }
  const match = SECTION_TITLES.find(
    ([prefix]) => normalized === prefix || normalized.startsWith(`${prefix}/`),
  );
  return match?.[1] ?? '管理端';
}

function AdminLayout() {
  const auth = useAuth();
  const toast = useToast();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);

  useEffect(() => {
    setSidebarOpen(false);
  }, [pathname]);

  const handleLogout = async () => {
    setLoggingOut(true);
    try {
      await auth.logout();
      toast.push('已退出登录', 'success');
    } catch {
      toast.push('服务端登出失败，请刷新页面确认会话状态', 'error');
    } finally {
      setLoggingOut(false);
      await navigate({ to: '/login', replace: true });
    }
  };

  return (
    <div className="admin-shell">
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>

      <aside
        id="admin-sidebar"
        className={sidebarOpen ? 'sidebar sidebar--open' : 'sidebar'}
        aria-label="管理端侧边栏"
      >
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            AI
          </span>
          <span className="brand-text">
            <strong>AI Router</strong>
            <span>个人 API 网关</span>
          </span>
        </div>

        <nav className="nav" aria-label="管理导航">
          {NAV_ITEMS.map((item) => (
            <Link
              key={item.to}
              to={item.to}
              className="nav-link"
              activeProps={{ className: 'nav-link nav-link--active' }}
              activeOptions={{ exact: item.to === '/' }}
              onClick={() => {
                setSidebarOpen(false);
              }}
            >
              <NavIcon name={item.icon} />
              <span>{item.label}</span>
            </Link>
          ))}
        </nav>

        <div className="sidebar-footer">
          <p>单管理员 · 单客户端 Key</p>
          <p>SQLite · 单进程</p>
        </div>
      </aside>

      {sidebarOpen ? (
        <button
          type="button"
          className="sidebar-scrim"
          aria-label="关闭导航"
          onClick={() => {
            setSidebarOpen(false);
          }}
        />
      ) : null}

      <div className="admin-main">
        <header className="topbar">
          <button
            type="button"
            className="icon-button menu-button"
            aria-label="打开导航"
            aria-expanded={sidebarOpen}
            aria-controls="admin-sidebar"
            onClick={() => {
              setSidebarOpen((open) => !open);
            }}
          >
            <MenuIcon />
          </button>

          <div className="topbar-heading">
            <span className="eyebrow">管理端</span>
            <h1>{sectionTitle(pathname)}</h1>
          </div>

          <div className="topbar-actions">
            <span className="admin-chip">
              <span className="status-dot" aria-hidden="true" />
              管理员
            </span>
            <button
              type="button"
              className="button button--ghost"
              onClick={() => {
                void handleLogout();
              }}
              disabled={loggingOut}
            >
              {loggingOut ? '正在退出…' : '登出'}
            </button>
          </div>
        </header>

        <main id="main-content" className="content" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* 六个受保护页的骨架                                                          */
/* -------------------------------------------------------------------------- */

const DASHBOARD_METRICS = [
  { label: '请求数（24h）', hint: '等待统计数据' },
  { label: 'Token 用量', hint: '输入与输出合计' },
  { label: '平均 / P95 延迟', hint: '首个内容与总耗时' },
  { label: '错误率', hint: '按最终状态统计' },
] as const;

function DashboardSkeleton() {
  return (
    <>
      <PageHeader title="总览" description="最近 24 小时的请求、Token 与延迟概览。" />

      <section className="metric-grid" aria-label="核心指标">
        {DASHBOARD_METRICS.map((metric) => (
          <article className="metric-card" key={metric.label}>
            <p className="metric-label">{metric.label}</p>
            <p className="metric-value">—</p>
            <p className="metric-hint">{metric.hint}</p>
          </article>
        ))}
      </section>

      <section className="panel">
        <div className="panel-header">
          <h2>请求趋势</h2>
          <span className="badge">最近 24 小时</span>
        </div>
        <EmptyState
          title="暂无统计数据"
          description="收到第一条请求后，这里会展示按小时聚合的请求量与错误率。"
        />
      </section>
    </>
  );
}

const CHANNEL_COLUMNS = ['名称', '协议', '优先级', '并发', '健康状态', 'Key'] as const;

function ChannelsSkeleton() {
  return (
    <>
      <PageHeader title="渠道" description="维护上游 base URL、协议、并发与优先级。" />

      <section className="panel">
        <div className="panel-header">
          <h2>渠道列表</h2>
          <span className="badge badge--muted">0 个渠道</span>
        </div>
        <div className="table-wrap">
          <table>
            <caption className="sr-only">上游渠道列表</caption>
            <thead>
              <tr>
                {CHANNEL_COLUMNS.map((column) => (
                  <th key={column} scope="col">
                    {column}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colSpan={CHANNEL_COLUMNS.length}>
                  <EmptyState
                    title="尚未配置渠道"
                    description="添加渠道后，这里会显示脱敏 Key；只有点击后才会请求完整 Key。"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

const MAPPING_COLUMNS = ['客户端模型', '渠道', '上游模型', '优先级', '状态'] as const;

function MappingsSkeleton() {
  return (
    <>
      <PageHeader
        title="模型映射"
        description="客户端模型到渠道与上游模型的显式映射，是唯一的路由来源。"
      />

      <section className="panel">
        <div className="panel-header">
          <h2>映射列表</h2>
          <span className="badge badge--muted">0 条映射</span>
        </div>
        <div className="table-wrap">
          <table>
            <caption className="sr-only">客户端模型映射列表</caption>
            <thead>
              <tr>
                {MAPPING_COLUMNS.map((column) => (
                  <th key={column} scope="col">
                    {column}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colSpan={MAPPING_COLUMNS.length}>
                  <EmptyState
                    title="尚无模型映射"
                    description="创建映射后，启用的客户端模型才会出现在模型列表与 Codex manifest 中。"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

function RequestsSkeleton() {
  return (
    <>
      <PageHeader title="请求观测" description="请求元数据、渠道尝试与错误摘要。" />

      <section className="panel">
        <div className="panel-header">
          <h2>请求日志</h2>
          <div className="toolbar" role="group" aria-label="请求筛选">
            <span className="badge">最近 24 小时</span>
            <span className="badge badge--muted">全部状态</span>
          </div>
        </div>
        <EmptyState
          title="暂无请求记录"
          description="默认只记录元数据、Token 用量、延迟与错误；不记录 header 或 body。"
        />
      </section>
    </>
  );
}

function DebugSkeleton() {
  return (
    <>
      <PageHeader title="调试" description="验证渠道连通性、延迟与脱敏错误。" />

      <section className="panel">
        <div className="panel-header">
          <h2>连通性测试</h2>
          <button
            type="button"
            className="button"
            disabled
            title="调试 API 就绪后开放"
          >
            运行测试
          </button>
        </div>
        <div className="panel-body">
          <p className="muted">
            向选中的渠道发送最小请求，用于验证 Key、base URL 与协议兼容性；每次测试会消耗上游
            Token。
          </p>
        </div>
        <EmptyState
          title="暂无调试结果"
          description="调试 API 就绪后，可选择渠道或模型运行测试并查看状态、延迟与脱敏错误。"
        />
      </section>
    </>
  );
}

function SettingsSkeleton() {
  return (
    <>
      <PageHeader title="设置" description="管理员密码、session 策略与安全边界。" />

      <div className="settings-grid">
        <section className="panel">
          <div className="panel-header">
            <h2>管理员密码</h2>
          </div>
          <div className="panel-body">
            <p>
              密码只以 PBKDF2-HMAC-SHA256 哈希存储，数据库与日志不保留明文。修改密码会撤销其他
              session。
            </p>
            <p className="muted">密码修改入口与活跃 session 管理将在设置功能中提供。</p>
          </div>
        </section>

        <section className="panel">
          <div className="panel-header">
            <h2>会话安全</h2>
          </div>
          <div className="panel-body">
            <ul className="detail-list">
              <li>
                <span>Session Cookie</span>
                <code>HttpOnly · SameSite=Lax</code>
              </li>
              <li>
                <span>管理写请求</span>
                <code>同源 Origin 校验</code>
              </li>
              <li>
                <span>登出</span>
                <code>撤销服务端 token</code>
              </li>
            </ul>
            <p className="muted">使用右上角“登出”结束当前会话。</p>
          </div>
        </section>
      </div>
    </>
  );
}

/* -------------------------------------------------------------------------- */
/* 路由                                                                        */
/* -------------------------------------------------------------------------- */

/**
 * 后续任务把真实页面落在 web/src/features/<feature>/<Name>Page.tsx。
 * T10 先用骨架占位；Vite 构建期自动发现同名页面，避免后续任务改动本文件。
 */
const PAGE_MODULES = import.meta.glob<Record<string, unknown>>('../features/*/*Page.tsx', {
  eager: true,
});

type PageComponent = () => ReactNode;

function loadFeaturePage(file: string, exportName: string): PageComponent | null {
  const module = PAGE_MODULES[`../features/${file}`];
  if (module === undefined) {
    return null;
  }
  const candidate = module['default'] ?? module[exportName];
  return typeof candidate === 'function' ? (candidate as PageComponent) : null;
}

const DashboardPage = loadFeaturePage('dashboard/DashboardPage.tsx', 'DashboardPage');
const ChannelsPage = loadFeaturePage('channels/ChannelsPage.tsx', 'ChannelsPage');
const MappingsPage = loadFeaturePage('mappings/MappingsPage.tsx', 'MappingsPage');
const RequestsPage = loadFeaturePage('requests/RequestsPage.tsx', 'RequestsPage');
const DebugPage = loadFeaturePage('debug/DebugPage.tsx', 'DebugPage');
const SettingsPage = loadFeaturePage('settings/SettingsPage.tsx', 'SettingsPage');

function RootLayout() {
  return <Outlet />;
}

function NotFoundPage() {
  return (
    <main className="not-found">
      <p className="eyebrow">HTTP 404</p>
      <h1>页面不存在</h1>
      <p>该地址不属于管理端。</p>
      <Link to="/" className="button">
        返回总览
      </Link>
    </main>
  );
}

function createAppRouter(auth: AuthController) {
  const rootRoute = createRootRoute({
    component: RootLayout,
    pendingComponent: FullPageLoading,
    pendingMs: 0,
    pendingMinMs: 0,
    notFoundComponent: NotFoundPage,
  });

  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    beforeLoad: async () => {
      const snapshot = await auth.ensureSession();
      if (snapshot.status === 'authenticated') {
        throw redirect({ to: '/' });
      }
    },
    component: LoginPage,
  });

  const adminRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: 'admin',
    beforeLoad: async () => {
      const snapshot = await auth.ensureSession();
      if (snapshot.status !== 'authenticated') {
        throw redirect({ to: '/login' });
      }
    },
    component: AdminLayout,
  });

  const dashboardRoute = createRoute({
    getParentRoute: () => adminRoute,
    path: '/',
    component: DashboardPage ?? DashboardSkeleton,
  });
  const channelsRoute = createRoute({
    getParentRoute: () => adminRoute,
    path: '/channels',
    component: ChannelsPage ?? ChannelsSkeleton,
  });
  const mappingsRoute = createRoute({
    getParentRoute: () => adminRoute,
    path: '/mappings',
    component: MappingsPage ?? MappingsSkeleton,
  });
  const requestsRoute = createRoute({
    getParentRoute: () => adminRoute,
    path: '/requests',
    component: RequestsPage ?? RequestsSkeleton,
  });
  const debugRoute = createRoute({
    getParentRoute: () => adminRoute,
    path: '/debug',
    component: DebugPage ?? DebugSkeleton,
  });
  const settingsRoute = createRoute({
    getParentRoute: () => adminRoute,
    path: '/settings',
    component: SettingsPage ?? SettingsSkeleton,
  });

  const routeTree = rootRoute.addChildren([
    loginRoute,
    adminRoute.addChildren([
      dashboardRoute,
      channelsRoute,
      mappingsRoute,
      requestsRoute,
      debugRoute,
      settingsRoute,
    ]),
  ]);

  return createRouter({
    routeTree,
    basepath: '/admin',
    defaultPreload: false,
    defaultPendingMs: 0,
    defaultPendingMinMs: 0,
  });
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}

/* -------------------------------------------------------------------------- */
/* 应用根                                                                      */
/* -------------------------------------------------------------------------- */

export function App() {
  const [auth] = useState(() => new AuthController());
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            retry: false,
            refetchOnWindowFocus: false,
            staleTime: 30_000,
          },
        },
      }),
  );
  const [router] = useState(() => createAppRouter(auth));

  return (
    <AuthContext.Provider value={auth}>
      <QueryClientProvider client={queryClient}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>
    </AuthContext.Provider>
  );
}
