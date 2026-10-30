/**
 * 管理端 /admin/api 的统一 fetch 封装。
 *
 * 约束（R-002 / R-014）：
 * - 始终带 cookie（credentials: include），session 只存在 HttpOnly Cookie 中；
 * - 始终禁用浏览器缓存（cache: no-store），管理响应不落盘；
 * - 任何 401 都先触发统一的登录跳转，再由调用方按 ApiError 处理；
 * - 只解析 JSON，错误体只保留后端稳定错误码与安全文案。
 */

/** ApiError 保留 HTTP 状态与后端稳定错误码，界面只展示安全文案。 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string | undefined;

  constructor(status: number, code: string | undefined, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

/** isUnauthorizedError 判断错误是否由统一 401 处理触发。 */
export function isUnauthorizedError(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 401;
}

/** LOGIN_PATH 是管理端登录页路径；401 与主动登出都跳转到这里。 */
export const LOGIN_PATH = '/admin/login';

type LoginRedirect = () => void;

function navigateToLoginPage(): void {
  if (typeof window === 'undefined') {
    return;
  }
  try {
    window.location.assign(LOGIN_PATH);
  } catch {
    // 导航被浏览器或测试环境阻止时仍让调用方收到 ApiError。
  }
}

let loginRedirect: LoginRedirect = navigateToLoginPage;

/**
 * setLoginRedirect 替换 401 / 登出后的跳转实现。
 *
 * 生产环境使用默认的整页跳转；测试注入 spy，避免依赖 jsdom 的导航实现。
 */
export function setLoginRedirect(handler: LoginRedirect | null): void {
  loginRedirect = handler ?? navigateToLoginPage;
}

/** redirectToLogin 执行统一的登录跳转。 */
export function redirectToLogin(): void {
  loginRedirect();
}

interface ApiErrorBody {
  error?: string;
  message?: string;
}

function asErrorBody(value: unknown): ApiErrorBody {
  if (typeof value !== 'object' || value === null) {
    return {};
  }
  const record = value as Record<string, unknown>;
  const body: ApiErrorBody = {};
  if (typeof record.error === 'string') {
    body.error = record.error;
  }
  if (typeof record.message === 'string') {
    body.message = record.message;
  }
  return body;
}

async function readJSONBody(response: Response): Promise<unknown> {
  const text = await response.text();
  if (text.trim() === '') {
    return undefined;
  }
  return JSON.parse(text) as unknown;
}

async function readErrorBody(response: Response): Promise<ApiErrorBody> {
  try {
    return asErrorBody(await readJSONBody(response));
  } catch {
    // 非 JSON 错误体（例如反向代理 HTML）只保留状态码。
    return {};
  }
}

/**
 * apiRequest 发送管理端 JSON 请求。
 *
 * 401 属于全局会话失效：先跳转登录再抛出 ApiError，调用方不得继续渲染
 * 受保护内容。其余错误保留后端 error/message，供页面展示安全文案。
 */
export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  if (init.body !== undefined && init.body !== null && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'include',
    cache: 'no-store',
  });

  if (response.status === 401) {
    const body = await readErrorBody(response);
    redirectToLogin();
    throw new ApiError(401, body.error ?? 'unauthorized', body.message ?? '登录已失效，请重新登录');
  }

  if (!response.ok) {
    const body = await readErrorBody(response);
    throw new ApiError(
      response.status,
      body.error,
      body.message ?? `请求失败（HTTP ${response.status}）`,
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }

  try {
    return (await readJSONBody(response)) as T;
  } catch {
    throw new ApiError(response.status, 'invalid_response', '服务器返回了无法解析的响应');
  }
}

/* -------------------------------------------------------------------------- */
/* 认证与 session 端点                                                         */
/* -------------------------------------------------------------------------- */

/** AdminSessionView 是后端 sessionView 的前端镜像；不包含 token 或哈希。 */
export interface AdminSessionView {
  id: string;
  created_at?: string;
  last_seen_at?: string;
  expires_at: string;
  current: boolean;
}

export interface SessionsResponse {
  sessions: AdminSessionView[];
}

export interface RevokeSessionsResponse {
  revoked: number;
  current_session_revoked?: boolean;
}

export interface OkResponse {
  ok: boolean;
}

/**
 * authApi 封装设置页使用的认证端点。
 *
 * 与会话相关的写操作同样经过 Origin 校验（浏览器自动带 Origin），
 * 前端不需要也不应该自行拼 CSRF token。
 */
export const authApi = {
  listSessions(): Promise<SessionsResponse> {
    return apiRequest<SessionsResponse>('/admin/api/auth/sessions');
  },

  changePassword(currentPassword: string, newPassword: string): Promise<OkResponse> {
    return apiRequest<OkResponse>('/admin/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({
        current_password: currentPassword,
        new_password: newPassword,
      }),
    });
  },

  revokeSession(sessionID: string): Promise<RevokeSessionsResponse> {
    return apiRequest<RevokeSessionsResponse>(
      `/admin/api/auth/sessions/${encodeURIComponent(sessionID)}`,
      { method: 'DELETE' },
    );
  },

  revokeOtherSessions(): Promise<RevokeSessionsResponse> {
    return apiRequest<RevokeSessionsResponse>('/admin/api/auth/sessions', {
      method: 'DELETE',
    });
  },

  logout(): Promise<OkResponse> {
    return apiRequest<OkResponse>('/admin/api/auth/logout', { method: 'POST' });
  },
};
