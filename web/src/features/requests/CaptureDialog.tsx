import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';

import { ApiError, apiRequest } from '../../lib/api';

/**
 * 按需 body 捕获面板（T25）。
 *
 * 安全边界（R-012 / R-014）：
 * - 只有管理员在请求详情中显式打开面板并确认风险后，才调用保存接口；
 * - 默认详情不读取 body；打开面板先查询 capture/status，诊断环不可用时
 *   只说明已过期，不提供抓取按钮；
 * - body 只通过捕获读取接口获取，以 <pre> 纯文本渲染，绝不使用
 *   dangerouslySetInnerHTML，也不执行其中的 HTML/脚本；
 * - 内容摘要明确提示 24 小时自动删除、8 MiB 截断与不含 header；
 * - 删除后关闭内容视图并回到不可用状态（后端同时清除诊断环副本）。
 */

/** CaptureStatusResponse 镜像 internal/adminapi.captureStatusResponse。 */
export interface CaptureStatusResponse {
  request_id: string;
  available: boolean;
  can_save: boolean;
  saved: boolean;

  request_truncated: boolean;
  response_truncated: boolean;
  request_bytes: number | null;
  response_bytes: number | null;

  captured_at?: string;
  expires_at?: string;
  saved_expires_at?: string;
}

/** CaptureReadResponse 镜像 internal/adminapi.captureReadResponse。 */
export interface CaptureReadResponse {
  request_id: string;

  request_body: string;
  response_body: string;

  request_truncated: boolean;
  response_truncated: boolean;
  request_bytes: number | null;
  response_bytes: number | null;

  created_at: string;
  expires_at: string;
}

interface CaptureDialogProps {
  requestID: string;
  onClose: () => void;
}

type CapturePhase =
  | 'loading'
  | 'available'
  | 'saving'
  | 'content'
  | 'expired'
  | 'deleted'
  | 'error';

const NUMBER_FORMATTER = new Intl.NumberFormat('zh-CN');

const TIMESTAMP_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

function captureEndpoint(requestID: string): string {
  return `/admin/api/requests/${encodeURIComponent(requestID)}/capture`;
}

function formatBytes(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return '—';
  }
  if (value < 1024) {
    return `${NUMBER_FORMATTER.format(value)} B`;
  }
  if (value < 1024 * 1024) {
    return `${(value / 1024).toFixed(1)} KiB`;
  }
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}

function formatTimestamp(value: string | null | undefined): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '—';
  }
  const parsed = new Date(value.replace(/(\.\d{3})\d+/, '$1'));
  if (Number.isNaN(parsed.getTime())) {
    return '—';
  }
  return TIMESTAMP_FORMATTER.format(parsed);
}

function phaseLabel(phase: CapturePhase): string {
  switch (phase) {
    case 'loading':
      return '检查中';
    case 'available':
      return '可抓取';
    case 'saving':
      return '保存中';
    case 'content':
      return '已保存';
    case 'expired':
      return '已过期';
    case 'deleted':
      return '已删除';
    case 'error':
      return '不可用';
    default:
      return '未知';
  }
}

function describeCaptureError(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'capture_not_found':
        return '捕获不存在或已过期。';
      case 'capture_unavailable':
        return '请求捕获暂不可用：数据库或主密钥未就绪。';
      case 'capture_confirmation_required':
        return '保存前必须确认隐私风险。';
      case 'invalid_capture':
        return '捕获内容无效，可能已损坏。';
      default:
        break;
    }
    if (error.status === 401) {
      return '登录状态已失效，请重新登录。';
    }
    if (error.status === 404) {
      return '捕获不存在或已过期。';
    }
    if (error.status === 429) {
      return '操作过于频繁，请稍后再试。';
    }
    if (error.status === 503) {
      return '请求捕获暂不可用：数据库或主密钥未就绪。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || fallback;
  }
  return fallback;
}

/* -------------------------------------------------------------------------- */
/* 复制与下载                                                                  */
/* -------------------------------------------------------------------------- */

async function copyToClipboard(text: string): Promise<void> {
  const clipboard: Clipboard | undefined = navigator.clipboard;
  if (clipboard !== undefined && typeof clipboard.writeText === 'function') {
    await clipboard.writeText(text);
    return;
  }

  if (typeof document.execCommand !== 'function') {
    throw new Error('clipboard unavailable');
  }

  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.top = '-1000px';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  try {
    textarea.select();
    if (!document.execCommand('copy')) {
      throw new Error('copy command failed');
    }
  } finally {
    document.body.removeChild(textarea);
  }
}

/** downloadText 以纯文本 Blob 下载；不经过任何 HTML 解析。 */
function downloadText(filename: string, text: string): void {
  if (typeof URL.createObjectURL !== 'function') {
    throw new Error('download unavailable');
  }
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  anchor.style.display = 'none';
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    document.body.removeChild(anchor);
    if (typeof URL.revokeObjectURL === 'function') {
      URL.revokeObjectURL(url);
    }
  }
}

/* -------------------------------------------------------------------------- */
/* 样式                                                                        */
/* -------------------------------------------------------------------------- */

const OVERLAY_STYLE: CSSProperties = {
  position: 'fixed',
  inset: 0,
  zIndex: 60,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 20,
  background: 'rgba(15, 23, 42, 0.52)',
};

const DIALOG_STYLE: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  width: 'min(760px, 100%)',
  maxHeight: 'calc(100vh - 40px)',
  background: 'var(--color-surface)',
  borderRadius: 12,
  boxShadow: 'var(--shadow-lg)',
  overflow: 'hidden',
};

const HEADER_STYLE: CSSProperties = {
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'space-between',
  gap: 16,
  padding: '18px 22px',
  borderBottom: '1px solid var(--color-border)',
};

const BODY_STYLE: CSSProperties = {
  flex: 1,
  overflowY: 'auto',
  padding: '20px 22px 24px',
};

const RISK_STYLE: CSSProperties = {
  padding: '16px 18px',
  borderRadius: 10,
  border: '1px solid var(--color-danger)',
  background: 'var(--color-danger-soft)',
};

const BODY_TEXT_STYLE: CSSProperties = {
  margin: '10px 0 12px',
  padding: 12,
  maxHeight: 300,
  overflow: 'auto',
  background: 'var(--color-surface-muted)',
  border: '1px solid var(--color-border)',
  borderRadius: 8,
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
  fontSize: 12,
  lineHeight: 1.5,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
};

interface CapturedBodyPanelProps {
  title: string;
  testID: string;
  text: string;
  truncated: boolean;
  bytes: number | null;
  onCopy: () => void;
  onDownload: () => void;
}

function CapturedBodyPanel({
  title,
  testID,
  text,
  truncated,
  bytes,
  onCopy,
  onDownload,
}: CapturedBodyPanelProps): ReactNode {
  return (
    <section className="panel" aria-labelledby={`${testID}-title`} style={{ marginTop: 16 }}>
      <div className="panel-header">
        <h3 id={`${testID}-title`} style={{ margin: 0, fontSize: 16 }}>
          {title}
        </h3>
        <span className="badge badge--muted">{formatBytes(bytes)}</span>
      </div>
      <div className="panel-body">
        {truncated ? (
          <p className="form-error" role="status">
            {title} 超过单侧 8 MiB 上限，仅保存了前 8 MiB（已截断）。
          </p>
        ) : null}
        <pre data-testid={testID} style={BODY_TEXT_STYLE}>
          {text === '' ? <span className="muted">（空）</span> : text}
        </pre>
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button
            type="button"
            className="button button--ghost"
            aria-label={`复制${title}`}
            onClick={onCopy}
          >
            复制
          </button>
          <button
            type="button"
            className="button button--ghost"
            aria-label={`下载${title}`}
            onClick={onDownload}
          >
            下载
          </button>
        </div>
      </div>
    </section>
  );
}

/* -------------------------------------------------------------------------- */
/* 主组件                                                                      */
/* -------------------------------------------------------------------------- */

export function CaptureDialog({ requestID, onClose }: CaptureDialogProps): ReactNode {
  const [phase, setPhase] = useState<CapturePhase>('loading');
  const [status, setStatus] = useState<CaptureStatusResponse | null>(null);
  const [content, setContent] = useState<CaptureReadResponse | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState(false);
  const [deleteBusy, setDeleteBusy] = useState(false);

  const generation = useRef(0);
  const closeButtonRef = useRef<HTMLButtonElement>(null);

  const loadContent = useCallback(
    async (current: number): Promise<void> => {
      try {
        const data = await apiRequest<CaptureReadResponse>(captureEndpoint(requestID));
        if (generation.current !== current) {
          return;
        }
        setContent(data);
        setPhase('content');
      } catch (error) {
        if (generation.current !== current) {
          return;
        }
        setContent(null);
        if (
          error instanceof ApiError &&
          (error.status === 404 || error.code === 'capture_not_found')
        ) {
          setStatus((previous) =>
            previous === null ? previous : { ...previous, saved: false },
          );
          setMessage('已保存的捕获不存在或已过期。');
          setPhase('expired');
          return;
        }
        setMessage(describeCaptureError(error, '读取已保存的 body 失败，请稍后重试。'));
        setPhase('error');
      }
    },
    [requestID],
  );

  const loadStatus = useCallback(async (): Promise<void> => {
    const current = generation.current + 1;
    generation.current = current;
    setPhase('loading');
    setMessage(null);
    setNotice(null);
    setContent(null);
    setDeleteConfirm(false);

    try {
      const next = await apiRequest<CaptureStatusResponse>(
        `${captureEndpoint(requestID)}/status`,
      );
      if (generation.current !== current) {
        return;
      }
      setStatus(next);
      if (next.saved) {
        await loadContent(current);
        return;
      }
      if (next.available && next.can_save) {
        setPhase('available');
        return;
      }
      if (next.available) {
        setMessage('诊断环仍可用，但保存服务当前不可用（数据库或主密钥未就绪）。');
      }
      setPhase('expired');
    } catch (error) {
      if (generation.current !== current) {
        return;
      }
      setMessage(describeCaptureError(error, '检查诊断环失败，请稍后重试。'));
      setPhase('error');
    }
  }, [loadContent, requestID]);

  useEffect(() => {
    void loadStatus();
    return () => {
      generation.current += 1;
    };
  }, [loadStatus]);

  useEffect(() => {
    const previous = document.activeElement;
    closeButtonRef.current?.focus();
    return () => {
      if (previous instanceof HTMLElement) {
        previous.focus();
      }
    };
  }, []);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [onClose]);

  const handleSave = async (): Promise<void> => {
    if (status === null) {
      return;
    }
    const current = generation.current + 1;
    generation.current = current;
    setPhase('saving');
    setMessage(null);
    setNotice(null);
    setDeleteConfirm(false);

    try {
      await apiRequest<{ saved: boolean }>(captureEndpoint(requestID), {
        method: 'POST',
        body: JSON.stringify({ confirm_risk: true }),
      });
      if (generation.current !== current) {
        return;
      }
      setStatus((previous) =>
        previous === null
          ? previous
          : { ...previous, saved: true, available: false, can_save: false },
      );
      await loadContent(current);
    } catch (error) {
      if (generation.current !== current) {
        return;
      }
      if (
        error instanceof ApiError &&
        (error.status === 404 || error.code === 'capture_not_found')
      ) {
        setStatus((previous) =>
          previous === null ? previous : { ...previous, available: false, can_save: false },
        );
        setMessage('请求 body 已过期或未被诊断环捕获，无法再保存。');
        setPhase('expired');
        return;
      }
      setMessage(describeCaptureError(error, '保存 body 失败，请稍后重试。'));
      setPhase('available');
    }
  };

  const handleDelete = async (): Promise<void> => {
    setDeleteBusy(true);
    setMessage(null);
    try {
      await apiRequest<void>(captureEndpoint(requestID), { method: 'DELETE' });
      setContent(null);
      setStatus((previous) =>
        previous === null
          ? previous
          : { ...previous, saved: false, available: false, can_save: false },
      );
      setPhase('deleted');
      setNotice('捕获已删除，内存诊断环副本也已清除。');
    } catch (error) {
      if (
        error instanceof ApiError &&
        (error.status === 404 || error.code === 'capture_not_found')
      ) {
        setContent(null);
        setStatus((previous) =>
          previous === null
            ? previous
            : { ...previous, saved: false, available: false, can_save: false },
        );
        setPhase('expired');
        setMessage('捕获不存在或已过期，可能已被删除。');
      } else {
        setMessage(describeCaptureError(error, '删除捕获失败，请稍后重试。'));
      }
    } finally {
      setDeleteBusy(false);
      setDeleteConfirm(false);
    }
  };

  const handleCopy = async (label: string, text: string): Promise<void> => {
    try {
      await copyToClipboard(text);
      setNotice(`已复制${label}。`);
    } catch {
      setNotice(`无法复制${label}，请手动选择文本。`);
    }
  };

  const handleDownload = (label: string, filename: string, text: string): void => {
    try {
      downloadText(filename, text);
      setNotice(`已开始下载${label}。`);
    } catch {
      setNotice('当前浏览器不支持直接下载，请改用复制。');
    }
  };

  return (
    <div
      style={OVERLAY_STYLE}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <section
        role="dialog"
        aria-modal="true"
        aria-labelledby="capture-dialog-title"
        style={DIALOG_STYLE}
        onMouseDown={(event) => {
          event.stopPropagation();
        }}
      >
        <header style={HEADER_STYLE}>
          <div>
            <p className="eyebrow">按需捕获</p>
            <h2 id="capture-dialog-title" style={{ margin: '4px 0 6px', fontSize: 20 }}>
              Body 捕获
            </h2>
            <p className="form-note" style={{ margin: 0, wordBreak: 'break-all' }}>
              <code>{requestID}</code>
            </p>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span className="badge badge--muted">{phaseLabel(phase)}</span>
            <button
              ref={closeButtonRef}
              type="button"
              className="button button--ghost"
              aria-label="关闭 Body 捕获"
              onClick={onClose}
            >
              关闭
            </button>
          </div>
        </header>

        <div style={BODY_STYLE}>
          {notice !== null && phase !== 'deleted' ? (
            <p className="form-note" role="status">
              {notice}
            </p>
          ) : null}

          {phase === 'loading' ? (
            <p className="form-note" role="status">
              正在检查诊断环并读取捕获…
            </p>
          ) : null}

          {phase === 'error' ? (
            <div className="state state--error" role="alert">
              <span className="state-icon" aria-hidden="true">
                !
              </span>
              <h3>无法读取捕获</h3>
              <p>{message ?? '请求捕获暂时不可用，请稍后重试。'}</p>
              <button
                type="button"
                className="button button--ghost"
                onClick={() => {
                  void loadStatus();
                }}
              >
                重试
              </button>
            </div>
          ) : null}

          {phase === 'expired' ? (
            <div className="state state--empty">
              <span className="state-icon" aria-hidden="true">
                ◇
              </span>
              <h3>body 捕获已过期</h3>
              <p>
                {message ??
                  '内存诊断环只在请求结束后的 5 分钟内保留 body（最多 50 请求 / 32 MiB），过期后无法再抓取。'}
              </p>
              <p className="muted">默认只保存请求元数据，不保存 header、prompt 或响应正文。</p>
            </div>
          ) : null}

          {phase === 'deleted' ? (
            <div className="state state--empty">
              <span className="state-icon" aria-hidden="true">
                ✓
              </span>
              <h3>捕获已删除</h3>
              <p>{notice ?? '捕获内容与诊断环副本已删除。'}</p>
              <p className="muted">删除后无法恢复，也无法再次抓取该请求的 body。</p>
            </div>
          ) : null}

          {(phase === 'available' || phase === 'saving') && status !== null ? (
            <section aria-labelledby="capture-risk-title" style={RISK_STYLE}>
              <h3 id="capture-risk-title" style={{ margin: '0 0 8px', fontSize: 16 }}>
                保存前请确认风险
              </h3>
              <ul style={{ margin: '0 0 12px', paddingLeft: 20, lineHeight: 1.7 }}>
                <li>
                  body 包含完整 prompt、对话历史与上游响应正文，可能含密码、个人数据或业务机密。
                </li>
                <li>
                  内容使用 AES-256-GCM 加密写入本机 SQLite，<strong>24 小时后自动删除</strong>
                  ，也可以随时手动删除。
                </li>
                <li>
                  不保存 header、Authorization 或 Cookie；单侧 body 超过 8 MiB 时只保存前 8
                  MiB 并标记截断。
                </li>
                <li>诊断环只在内存中保留最近 5 分钟 / 50 请求 / 32 MiB，过期后无法再抓取。</li>
              </ul>

              {status.request_truncated || status.response_truncated ? (
                <p className="form-error">
                  诊断环中的
                  {status.request_truncated ? ' 请求 body' : ''}
                  {status.response_truncated ? ' 响应 body' : ''}
                  已超过 8 MiB，保存内容会被截断。
                </p>
              ) : null}

              {message !== null ? (
                <p className="form-error" role="alert">
                  {message}
                </p>
              ) : null}

              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                <button
                  type="button"
                  className="button"
                  onClick={() => {
                    void handleSave();
                  }}
                  disabled={phase === 'saving'}
                >
                  {phase === 'saving' ? '正在保存…' : '确认风险并抓取 body'}
                </button>
                <button
                  type="button"
                  className="button button--ghost"
                  onClick={onClose}
                  disabled={phase === 'saving'}
                >
                  取消
                </button>
              </div>
            </section>
          ) : null}

          {phase === 'content' && content !== null ? (
            <>
              {message !== null ? (
                <p className="form-error" role="alert">
                  {message}
                </p>
              ) : null}

              <section className="panel" aria-labelledby="capture-meta-title">
                <div className="panel-header">
                  <h3 id="capture-meta-title" style={{ margin: 0, fontSize: 16 }}>
                    捕获元数据
                  </h3>
                  <span className="badge">已加密保存</span>
                </div>
                <div className="panel-body">
                  <ul className="detail-list">
                    <li>
                      <span>保存时间</span>
                      <span>{formatTimestamp(content.created_at)}</span>
                    </li>
                    <li>
                      <span>自动删除</span>
                      <span>{formatTimestamp(content.expires_at)}（24 小时后）</span>
                    </li>
                    <li>
                      <span>大小</span>
                      <span>
                        请求 {formatBytes(content.request_bytes)} · 响应{' '}
                        {formatBytes(content.response_bytes)}
                      </span>
                    </li>
                  </ul>
                  <p className="muted">
                    内容以纯文本展示，不执行其中的 HTML 或脚本；不包含请求 header、Authorization
                    或 Cookie。
                  </p>
                </div>
              </section>

              <CapturedBodyPanel
                title="请求 Body"
                testID="capture-request-body"
                text={content.request_body}
                truncated={content.request_truncated}
                bytes={content.request_bytes}
                onCopy={() => {
                  void handleCopy('请求 Body', content.request_body);
                }}
                onDownload={() => {
                  handleDownload(
                    '请求 Body',
                    `capture-${requestID}-request.txt`,
                    content.request_body,
                  );
                }}
              />

              <CapturedBodyPanel
                title="响应 Body"
                testID="capture-response-body"
                text={content.response_body}
                truncated={content.response_truncated}
                bytes={content.response_bytes}
                onCopy={() => {
                  void handleCopy('响应 Body', content.response_body);
                }}
                onDownload={() => {
                  handleDownload(
                    '响应 Body',
                    `capture-${requestID}-response.txt`,
                    content.response_body,
                  );
                }}
              />

              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 10,
                  flexWrap: 'wrap',
                  marginTop: 18,
                }}
              >
                {deleteConfirm ? (
                  <>
                    <span className="form-error">删除后无法恢复，确认删除该捕获？</span>
                    <button
                      type="button"
                      className="button"
                      onClick={() => {
                        void handleDelete();
                      }}
                      disabled={deleteBusy}
                    >
                      {deleteBusy ? '正在删除…' : '确认删除'}
                    </button>
                    <button
                      type="button"
                      className="button button--ghost"
                      onClick={() => {
                        setDeleteConfirm(false);
                      }}
                      disabled={deleteBusy}
                    >
                      取消删除
                    </button>
                  </>
                ) : (
                  <button
                    type="button"
                    className="button button--ghost"
                    onClick={() => {
                      setMessage(null);
                      setDeleteConfirm(true);
                    }}
                  >
                    删除捕获
                  </button>
                )}
              </div>
            </>
          ) : null}
        </div>
      </section>
    </div>
  );
}

export default CaptureDialog;
