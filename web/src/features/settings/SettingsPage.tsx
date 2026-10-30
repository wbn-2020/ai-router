import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react';

import {
  ApiError,
  authApi,
  isUnauthorizedError,
  redirectToLogin,
  type AdminSessionView,
} from '../../lib/api';

/** ADMIN_PASSWORD_MIN_LENGTH 与后端 adminauth.MinPasswordLength 保持一致。 */
export const ADMIN_PASSWORD_MIN_LENGTH = 12;

const TIMESTAMP_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

function parseTimestamp(value: string): Date | null {
  // Go 的 RFC3339Nano 最多带 9 位小数；部分浏览器只接受 3 位，这里截断后再解析。
  const normalized = value.replace(/(\.\d{3})\d+/, '$1');
  const parsed = new Date(normalized);
  if (Number.isNaN(parsed.getTime())) {
    return null;
  }
  return parsed;
}

function formatTimestamp(value: string | undefined): string {
  if (typeof value !== 'string' || value === '') {
    return '—';
  }
  const parsed = parseTimestamp(value);
  if (parsed === null) {
    return '—';
  }
  return TIMESTAMP_FORMATTER.format(parsed);
}

/** characterCount 按 Unicode 码点计数，与 Go 的 utf8.RuneCountInString 对齐。 */
function characterCount(value: string): number {
  return Array.from(value).length;
}

function validatePasswordFields(
  currentPassword: string,
  newPassword: string,
  confirmPassword: string,
): string | null {
  if (currentPassword === '') {
    return '请输入当前密码';
  }
  if (newPassword === '') {
    return '请输入新密码';
  }
  if (characterCount(newPassword) < ADMIN_PASSWORD_MIN_LENGTH) {
    return `新密码至少 ${ADMIN_PASSWORD_MIN_LENGTH} 个字符`;
  }
  if (newPassword !== confirmPassword) {
    return '两次输入的新密码不一致';
  }
  return null;
}

function describePasswordError(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'invalid_current_password':
        return '当前密码不正确';
      case 'password_too_short':
        return error.message || `新密码至少 ${ADMIN_PASSWORD_MIN_LENGTH} 个字符`;
      case 'invalid_request':
        return error.message || '请检查当前密码与新密码';
      default:
        break;
    }
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后再试';
    }
    return error.message || `修改密码失败（HTTP ${error.status}）`;
  }
  return '修改密码失败，请检查网络连接后重试';
}

function describeSessionActionError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后再试';
    }
    return error.message || `${fallback}（HTTP ${error.status}）`;
  }
  return `${fallback}，请检查网络连接后重试`;
}

function TimeCell({ value }: { value?: string }) {
  if (typeof value !== 'string' || value === '' || parseTimestamp(value) === null) {
    return <span className="muted">—</span>;
  }
  return <time dateTime={value}>{formatTimestamp(value)}</time>;
}

function SessionsLoadError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="state state--error" role="alert">
      <span className="state-icon" aria-hidden="true">
        !
      </span>
      <h2>会话加载失败</h2>
      <p>{message}</p>
      <button type="button" className="button button--ghost" onClick={onRetry}>
        重试
      </button>
    </div>
  );
}

/**
 * SettingsPage 集中处理管理员安全操作：
 * - 修改密码（当前密码 / 新密码 / 确认，最少 12 字符）；
 * - 查看活跃 session 元数据（不含 token），撤销单个或其他全部；
 * - 登出当前 session。
 *
 * 所有请求经 web/src/lib/api.ts；遇到 401 立即隐藏受保护内容并跳转登录。
 */
export function SettingsPage(): ReactNode {
  const [sessions, setSessions] = useState<AdminSessionView[] | null>(null);
  const [sessionsError, setSessionsError] = useState<string | null>(null);
  const [sessionActionError, setSessionActionError] = useState<string | null>(null);
  const [sessionNotice, setSessionNotice] = useState<string | null>(null);
  const [revokingID, setRevokingID] = useState<string | null>(null);
  const [confirmingRevokeAll, setConfirmingRevokeAll] = useState(false);
  const [revokingAll, setRevokingAll] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const [logoutError, setLogoutError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);

  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [passwordError, setPasswordError] = useState<string | null>(null);
  const [passwordNotice, setPasswordNotice] = useState<string | null>(null);
  const [changingPassword, setChangingPassword] = useState(false);

  const loadSessions = useCallback(async () => {
    setSessionsError(null);
    try {
      const response = await authApi.listSessions();
      setSessions(response.sessions ?? []);
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        setSessions([]);
        return;
      }
      setSessionsError(describeSessionActionError(error, '加载活跃会话失败'));
    }
  }, []);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  const orderedSessions = useMemo(() => {
    const list = [...(sessions ?? [])];
    // 当前 session 置顶，其余保持后端返回的创建时间倒序。
    return list.sort((left, right) => Number(right.current) - Number(left.current));
  }, [sessions]);

  const otherSessions = useMemo(
    () => (sessions ?? []).filter((session) => !session.current),
    [sessions],
  );

  const handlePasswordSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const validationError = validatePasswordFields(
      currentPassword,
      newPassword,
      confirmPassword,
    );
    if (validationError !== null) {
      setPasswordError(validationError);
      setPasswordNotice(null);
      return;
    }

    setChangingPassword(true);
    setPasswordError(null);
    setPasswordNotice(null);
    try {
      await authApi.changePassword(currentPassword, newPassword);
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
      setSessions((current) => current?.filter((item) => item.current) ?? null);
      setPasswordNotice('密码已更新，其他会话已被撤销。');
      await loadSessions();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setPasswordError(describePasswordError(error));
    } finally {
      setChangingPassword(false);
    }
  };

  const handleRevokeSession = async (session: AdminSessionView) => {
    setRevokingID(session.id);
    setSessionActionError(null);
    setSessionNotice(null);
    try {
      const result = await authApi.revokeSession(session.id);
      if (result.current_session_revoked === true) {
        redirectToLogin();
        return;
      }
      setSessions((current) => current?.filter((item) => item.id !== session.id) ?? null);
      setSessionNotice('会话已撤销。');
      await loadSessions();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setSessionActionError(describeSessionActionError(error, '撤销会话失败'));
    } finally {
      setRevokingID(null);
    }
  };

  const handleRevokeAll = async () => {
    setRevokingAll(true);
    setSessionActionError(null);
    setSessionNotice(null);
    try {
      const result = await authApi.revokeOtherSessions();
      setConfirmingRevokeAll(false);
      setSessions((current) => current?.filter((item) => item.current) ?? null);
      setSessionNotice(`已撤销 ${result.revoked} 个其他会话。`);
      await loadSessions();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setSessionActionError(describeSessionActionError(error, '撤销其他会话失败'));
    } finally {
      setRevokingAll(false);
    }
  };

  const handleLogout = async () => {
    setLoggingOut(true);
    setLogoutError(null);
    try {
      await authApi.logout();
      redirectToLogin();
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setLogoutError(describeSessionActionError(error, '登出失败'));
    } finally {
      setLoggingOut(false);
    }
  };

  if (unauthorized) {
    return (
      <div className="state state--error" role="alert">
        <span className="state-icon" aria-hidden="true">
          !
        </span>
        <h2>登录状态已失效</h2>
        <p>正在跳转登录页，请重新登录后继续操作。</p>
      </div>
    );
  }

  const hasSessionMessages =
    sessions !== null &&
    (sessionActionError !== null || sessionNotice !== null || sessionsError !== null);

  return (
    <>
      <header className="page-header">
        <div>
          <h2>设置</h2>
          <p>管理员密码与会话安全。密码只保存哈希；撤销或登出会立即让旧 token 失效。</p>
        </div>
      </header>

      <div className="settings-grid">
        <section className="panel" aria-labelledby="settings-password-title">
          <div className="panel-header">
            <h2 id="settings-password-title">修改密码</h2>
            <span className="badge">PBKDF2-HMAC-SHA256</span>
          </div>
          <div className="panel-body">
            <p className="muted">
              密码长度不少于 {ADMIN_PASSWORD_MIN_LENGTH} 个字符。修改成功后，除当前会话外的其他
              session 会被撤销。
            </p>

            <form
              onSubmit={(event) => {
                void handlePasswordSubmit(event);
              }}
              noValidate
              style={{ display: 'grid', gap: 14, marginTop: 16 }}
            >
              <div className="field">
                <label htmlFor="settings-current-password">当前密码</label>
                <input
                  id="settings-current-password"
                  className="input"
                  type="password"
                  name="current-password"
                  autoComplete="current-password"
                  value={currentPassword}
                  onChange={(event) => {
                    setCurrentPassword(event.target.value);
                  }}
                  aria-invalid={passwordError !== null}
                  aria-describedby={
                    passwordError === null ? undefined : 'settings-password-error'
                  }
                />
              </div>

              <div className="field">
                <label htmlFor="settings-new-password">新密码</label>
                <input
                  id="settings-new-password"
                  className="input"
                  type="password"
                  name="new-password"
                  autoComplete="new-password"
                  value={newPassword}
                  onChange={(event) => {
                    setNewPassword(event.target.value);
                  }}
                  aria-invalid={passwordError !== null}
                  aria-describedby={
                    passwordError === null ? undefined : 'settings-password-error'
                  }
                />
              </div>

              <div className="field">
                <label htmlFor="settings-confirm-password">确认新密码</label>
                <input
                  id="settings-confirm-password"
                  className="input"
                  type="password"
                  name="confirm-password"
                  autoComplete="new-password"
                  value={confirmPassword}
                  onChange={(event) => {
                    setConfirmPassword(event.target.value);
                  }}
                  aria-invalid={passwordError !== null}
                  aria-describedby={
                    passwordError === null ? undefined : 'settings-password-error'
                  }
                />
              </div>

              {passwordError === null ? null : (
                <p className="form-error" id="settings-password-error" role="alert">
                  {passwordError}
                </p>
              )}

              {passwordNotice === null ? null : (
                <p className="form-note" role="status">
                  {passwordNotice}
                </p>
              )}

              <div>
                <button type="submit" className="button" disabled={changingPassword}>
                  {changingPassword ? '正在修改…' : '修改密码'}
                </button>
              </div>
            </form>
          </div>
        </section>

        <section className="panel" aria-labelledby="settings-security-title">
          <div className="panel-header">
            <h2 id="settings-security-title">会话安全</h2>
            <span className="badge badge--muted">HttpOnly Cookie</span>
          </div>
          <div className="panel-body">
            <ul className="detail-list">
              <li>
                <span>密码存储</span>
                <code>PBKDF2-HMAC-SHA256</code>
              </li>
              <li>
                <span>Session Cookie</span>
                <code>HttpOnly · SameSite=Lax</code>
              </li>
              <li>
                <span>管理写请求</span>
                <code>同源 Origin 校验</code>
              </li>
            </ul>
            <p className="muted">登出会撤销当前服务端 token 并清除浏览器 Cookie。</p>
            <div className="toolbar" style={{ marginTop: 14 }}>
              <button
                type="button"
                className="button button--ghost"
                onClick={() => {
                  void handleLogout();
                }}
                disabled={loggingOut}
              >
                {loggingOut ? '正在登出…' : '登出当前会话'}
              </button>
            </div>
            {logoutError === null ? null : (
              <p className="form-error" role="alert" style={{ marginTop: 10 }}>
                {logoutError}
              </p>
            )}
          </div>
        </section>
      </div>

      <section className="panel" aria-labelledby="settings-sessions-title">
        <div className="panel-header">
          <h2 id="settings-sessions-title">活跃会话</h2>
          <div className="toolbar" role="group" aria-label="会话操作">
            <span className="badge badge--muted">
              {sessions === null ? '—' : `${sessions.length} 个`}
            </span>
            {confirmingRevokeAll ? (
              <>
                <span className="form-note">将撤销 {otherSessions.length} 个其他会话。</span>
                <button
                  type="button"
                  className="button"
                  onClick={() => {
                    void handleRevokeAll();
                  }}
                  disabled={revokingAll}
                >
                  {revokingAll ? '正在撤销…' : '确认撤销'}
                </button>
                <button
                  type="button"
                  className="button button--ghost"
                  onClick={() => {
                    setConfirmingRevokeAll(false);
                  }}
                  disabled={revokingAll}
                >
                  取消
                </button>
              </>
            ) : (
              <button
                type="button"
                className="button button--ghost"
                onClick={() => {
                  setConfirmingRevokeAll(true);
                }}
                disabled={sessions === null || otherSessions.length === 0}
              >
                撤销其他全部会话
              </button>
            )}
          </div>
        </div>

        {hasSessionMessages ? (
          <div className="panel-body" style={{ paddingBottom: 0 }}>
            {sessionActionError === null ? null : (
              <p className="form-error" role="alert">
                {sessionActionError}
              </p>
            )}
            {sessionNotice === null ? null : (
              <p className="form-note" role="status">
                {sessionNotice}
              </p>
            )}
            {sessionsError === null ? null : (
              <p className="form-error" role="alert">
                {sessionsError}
              </p>
            )}
          </div>
        ) : null}

        {sessions === null ? (
          sessionsError === null ? (
            <p className="panel-body form-note" role="status">
              正在加载活跃会话…
            </p>
          ) : (
            <SessionsLoadError
              message={sessionsError}
              onRetry={() => {
                void loadSessions();
              }}
            />
          )
        ) : orderedSessions.length === 0 ? (
          <div className="state state--empty">
            <span className="state-icon" aria-hidden="true">
              ◇
            </span>
            <h3>没有活跃会话</h3>
            <p>当前没有可展示的管理员 session。</p>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <caption className="sr-only">活跃管理员会话</caption>
              <thead>
                <tr>
                  <th scope="col">会话</th>
                  <th scope="col">创建时间</th>
                  <th scope="col">最后使用</th>
                  <th scope="col">过期时间</th>
                  <th scope="col">操作</th>
                </tr>
              </thead>
              <tbody>
                {orderedSessions.map((session) => (
                  <tr key={session.id}>
                    <td>
                      {session.current ? (
                        <span className="badge">当前会话</span>
                      ) : (
                        <span className="badge badge--muted">其他会话</span>
                      )}
                    </td>
                    <td>
                      <TimeCell value={session.created_at} />
                    </td>
                    <td>
                      <TimeCell value={session.last_seen_at} />
                    </td>
                    <td>
                      <TimeCell value={session.expires_at} />
                    </td>
                    <td>
                      {session.current ? (
                        <span className="muted">—</span>
                      ) : (
                        <button
                          type="button"
                          className="button button--ghost"
                          aria-label={`撤销创建于 ${formatTimestamp(session.created_at)} 的会话`}
                          onClick={() => {
                            void handleRevokeSession(session);
                          }}
                          disabled={
                            revokingID !== null ||
                            revokingAll ||
                            changingPassword ||
                            loggingOut
                          }
                        >
                          {revokingID === session.id ? '正在撤销…' : '撤销'}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}

export default SettingsPage;
