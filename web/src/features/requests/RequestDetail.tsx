import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';

import { ApiError, apiRequest, isUnauthorizedError } from '../../lib/api';

/**
 * 请求详情抽屉（T16）。
 *
 * 约束（R-011 / R-014）：
 * - 只展示 /admin/api/requests/{id} 返回的请求元数据与 attempt 列表；
 * - 不展示 header、body 或 prompt：这些字段根本不在视图模型里；
 * - usage 缺失时显示 “—”，不把未知值伪装成 0；
 * - 每次 attempt 的渠道、状态、延迟与错误都可见；
 * - body 捕获入口属于 T25；这里只说明默认未捕获与诊断环提示。
 */

/**
 * RequestLogView 镜像 internal/adminapi.requestLogView。
 *
 * 刻意不包含 header/body/Authorization/prompt 字段；即使后端意外返回额外
 * 字段，本组件也不会渲染它们。
 */
export interface RequestLogView {
  request_id: string;
  protocol: string;
  endpoint: string;
  model: string;

  channel_id: number | null;
  channel_name: string;

  status: string;
  http_status: number | null;

  error_code: string | null;
  error_message: string | null;

  stream: boolean;
  attempt_count: number;
  retry_count: number;
  ttft_ms: number | null;
  latency_ms: number;
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  usage_source: string | null;
  semantic_committed: boolean;
  response_bytes: number | null;

  started_at: string;
  finished_at: string | null;
}

/** RequestAttemptView 镜像 internal/adminapi.requestAttemptView。 */
export interface RequestAttemptView {
  attempt_index: number;

  channel_id: number | null;
  channel_name: string;
  upstream_model: string;

  status: string;
  http_status: number | null;

  error_code: string | null;
  error_message: string | null;

  latency_ms: number;
  semantic_committed: boolean;

  started_at: string;
  finished_at: string | null;
}

/** RequestDetailResponse 是 GET /admin/api/requests/{id} 的响应体。 */
export interface RequestDetailResponse {
  request: RequestLogView;
  attempts: RequestAttemptView[] | null;
}

const NUMBER_FORMATTER = new Intl.NumberFormat('zh-CN');

const TIMESTAMP_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

/* -------------------------------------------------------------------------- */
/* 共享格式化（RequestsPage 也使用）                                           */
/* -------------------------------------------------------------------------- */

/**
 * parseTimestamp 解析 Go 的 RFC3339Nano 时间。
 *
 * 部分浏览器只接受 3 位小数，先把纳秒截断到毫秒再解析。
 */
export function parseTimestamp(value: string | null | undefined): Date | null {
  if (typeof value !== 'string' || value.trim() === '') {
    return null;
  }
  const normalized = value.replace(/(\.\d{3})\d+/, '$1');
  const parsed = new Date(normalized);
  if (Number.isNaN(parsed.getTime())) {
    return null;
  }
  return parsed;
}

/** formatTimestamp 输出本地化时间；缺失或非法时显示 “—”。 */
export function formatTimestamp(value: string | null | undefined): string {
  const parsed = parseTimestamp(value);
  if (parsed === null) {
    return '—';
  }
  return TIMESTAMP_FORMATTER.format(parsed);
}

export function formatCount(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return '—';
  }
  return NUMBER_FORMATTER.format(value);
}

export function formatMilliseconds(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return '—';
  }
  return `${NUMBER_FORMATTER.format(Math.round(value))} ms`;
}

export function formatBytes(value: number | null | undefined): string {
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

export function statusLabel(status: string): string {
  switch (status) {
    case 'success':
      return '成功';
    case 'error':
      return '失败';
    case 'cancelled':
      return '已取消';
    default:
      return status.trim() === '' ? '未知' : status;
  }
}

type ErrorLike = {
  status: string;
  http_status: number | null;
  error_code: string | null;
  error_message: string | null;
};

/**
 * summarizeError 生成可读错误摘要。
 *
 * 后端的 error_message 在落库前已做敏感值脱敏；这里只拼接，不再二次解释。
 */
export function summarizeError(item: ErrorLike): string | null {
  const code = typeof item.error_code === 'string' ? item.error_code.trim() : '';
  const message = typeof item.error_message === 'string' ? item.error_message.trim() : '';
  if (code !== '' && message !== '') {
    return `${code}：${message}`;
  }
  if (code !== '') {
    return code;
  }
  if (message !== '') {
    return message;
  }
  if (item.status !== 'success' && typeof item.http_status === 'number') {
    return `HTTP ${item.http_status}`;
  }
  return null;
}

/** RequestStatusBadge 统一列表与详情的状态色彩与文案。 */
export function RequestStatusBadge({
  status,
  httpStatus,
}: {
  status: string;
  httpStatus?: number | null;
}): ReactNode {
  const label = statusLabel(status);
  const suffix = typeof httpStatus === 'number' ? ` · HTTP ${httpStatus}` : '';
  const style: CSSProperties | undefined =
    status === 'error'
      ? { background: 'var(--color-danger-soft)', color: 'var(--color-danger)' }
      : undefined;

  return (
    <span
      className={status === 'success' ? 'badge' : 'badge badge--muted'}
      style={style}
      title={`状态：${label}${suffix}`}
    >
      {label}
      {suffix}
    </span>
  );
}

/* -------------------------------------------------------------------------- */
/* 详情组件                                                                    */
/* -------------------------------------------------------------------------- */

interface RequestDetailProps {
  requestID: string;
  onClose: () => void;
}

type DetailState =
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'ready'; detail: RequestDetailResponse };

function describeDetailError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'request_not_found' || error.status === 404) {
      return '请求日志不存在，可能已超过 90 天保留期。';
    }
    if (error.status === 429) {
      return '请求过于频繁，请稍后再试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `加载请求详情失败（HTTP ${error.status}）`;
  }
  return '加载请求详情失败，请检查网络连接后重试。';
}

function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <li>
      <span>{label}</span>
      <span style={{ color: 'var(--color-text-soft)', textAlign: 'right' }}>{children}</span>
    </li>
  );
}

function ChannelLabel({
  channelID,
  channelName,
}: {
  channelID: number | null;
  channelName: string;
}): ReactNode {
  const name = channelName.trim() === '' ? '未知渠道' : channelName;
  if (channelID === null) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
        <span>{name}</span>
        <span className="badge badge--muted">已删除</span>
      </span>
    );
  }
  return <>{name}</>;
}

function MetadataPanel({ request }: { request: RequestLogView }) {
  const errorSummary = summarizeError(request);

  return (
    <>
      <section className="panel" aria-labelledby="request-detail-metadata">
        <div className="panel-header">
          <h2 id="request-detail-metadata">请求元数据</h2>
          <RequestStatusBadge status={request.status} httpStatus={request.http_status} />
        </div>
        <div className="panel-body">
          <ul className="detail-list">
            <DetailRow label="请求 ID">
              <code>{request.request_id}</code>
            </DetailRow>
            <DetailRow label="协议 / 端点">
              <code>{request.protocol || '—'}</code>
              {' · '}
              <code>{request.endpoint || '—'}</code>
            </DetailRow>
            <DetailRow label="客户端模型">
              <code>{request.model || '—'}</code>
            </DetailRow>
            <DetailRow label="最终渠道">
              <ChannelLabel
                channelID={request.channel_id}
                channelName={request.channel_name}
              />
            </DetailRow>
            <DetailRow label="请求方式">{request.stream ? '流式 (SSE)' : '非流式'}</DetailRow>
            <DetailRow label="尝试次数">
              {formatCount(request.attempt_count)} 次
              {request.retry_count > 0 ? `（重试 ${formatCount(request.retry_count)} 次）` : ''}
            </DetailRow>
            <DetailRow label="开始时间">
              {formatTimestamp(request.started_at)}
            </DetailRow>
            <DetailRow label="结束时间">
              {formatTimestamp(request.finished_at)}
            </DetailRow>
            <DetailRow label="首内容延迟">
              {formatMilliseconds(request.ttft_ms)}
            </DetailRow>
            <DetailRow label="总延迟">{formatMilliseconds(request.latency_ms)}</DetailRow>
            <DetailRow label="响应字节">{formatBytes(request.response_bytes)}</DetailRow>
            <DetailRow label="首内容已提交">
              {request.semantic_committed ? '是' : '否'}
            </DetailRow>
          </ul>
        </div>
      </section>

      {request.status !== 'success' ? (
        <section className="panel" aria-labelledby="request-detail-error">
          <div className="panel-header">
            <h2 id="request-detail-error">错误摘要</h2>
            {request.http_status === null ? null : (
              <span className="badge badge--muted">HTTP {request.http_status}</span>
            )}
          </div>
          <div className="panel-body">
            <p className="form-error" style={{ whiteSpace: 'pre-wrap' }}>
              {errorSummary ??
                `请求以${statusLabel(request.status)}状态结束，未记录错误详情。`}
            </p>
          </div>
        </section>
      ) : null}
    </>
  );
}

function UsagePanel({ request }: { request: RequestLogView }) {
  const allMissing =
    request.input_tokens === null &&
    request.output_tokens === null &&
    request.total_tokens === null;

  return (
    <section className="panel" aria-labelledby="request-detail-usage">
      <div className="panel-header">
        <h2 id="request-detail-usage">Token 用量</h2>
        {typeof request.usage_source === 'string' && request.usage_source !== '' ? (
          <span className="badge badge--muted">来源 {request.usage_source}</span>
        ) : (
          <span className="badge badge--muted">usage 来源未知</span>
        )}
      </div>
      <div className="panel-body">
        <ul className="detail-list">
          <DetailRow label="输入 Token">{formatCount(request.input_tokens)}</DetailRow>
          <DetailRow label="输出 Token">{formatCount(request.output_tokens)}</DetailRow>
          <DetailRow label="合计 Token">{formatCount(request.total_tokens)}</DetailRow>
        </ul>
        {allMissing ? (
          <p className="muted">上游未返回 usage，显示 —，不估算。</p>
        ) : (
          <p className="muted">Token 来自上游 usage；缺失的字段显示 —，不做估算。</p>
        )}
      </div>
    </section>
  );
}

function AttemptsPanel({ attempts }: { attempts: RequestAttemptView[] }) {
  return (
    <section className="panel" aria-labelledby="request-detail-attempts">
      <div className="panel-header">
        <h2 id="request-detail-attempts">渠道尝试</h2>
        <span className="badge badge--muted">{attempts.length} 次</span>
      </div>
      {attempts.length === 0 ? (
        <div className="state state--empty">
          <span className="state-icon" aria-hidden="true">
            ◇
          </span>
          <h3>没有渠道尝试记录</h3>
          <p>该请求没有落库的 attempt 明细，可能来自旧版本或写入中断。</p>
        </div>
      ) : (
        <div className="table-wrap">
          <table>
            <caption className="sr-only">渠道尝试列表，按尝试序号升序</caption>
            <thead>
              <tr>
                <th scope="col">#</th>
                <th scope="col">渠道</th>
                <th scope="col">上游模型</th>
                <th scope="col">状态</th>
                <th scope="col">延迟</th>
                <th scope="col">首内容已提交</th>
                <th scope="col">错误摘要</th>
              </tr>
            </thead>
            <tbody>
              {attempts.map((attempt) => {
                const summary = summarizeError(attempt);
                return (
                  <tr key={`${attempt.attempt_index}-${attempt.channel_id ?? 'deleted'}`}>
                    <td>{attempt.attempt_index}</td>
                    <td>
                      <ChannelLabel
                        channelID={attempt.channel_id}
                        channelName={attempt.channel_name}
                      />
                    </td>
                    <td>
                      <code>{attempt.upstream_model || '—'}</code>
                    </td>
                    <td>
                      <RequestStatusBadge
                        status={attempt.status}
                        httpStatus={attempt.http_status}
                      />
                    </td>
                    <td>{formatMilliseconds(attempt.latency_ms)}</td>
                    <td>{attempt.semantic_committed ? '是' : '否'}</td>
                    <td>
                      {summary === null ? (
                        <span className="muted">—</span>
                      ) : (
                        <span
                          className="form-error"
                          title={summary}
                          style={{ whiteSpace: 'pre-wrap' }}
                        >
                          {summary}
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function CaptureHintPanel() {
  return (
    <section className="panel" aria-labelledby="request-detail-capture">
      <div className="panel-header">
        <h2 id="request-detail-capture">Body 捕获</h2>
        <span className="badge badge--muted">默认关闭</span>
      </div>
      <div className="panel-body">
        <p>默认只保存请求元数据，不保存 header、body 或 prompt。</p>
        <p className="muted">
          只有内存诊断环仍可用的请求才能按需抓取 body；保存需要管理员显式确认风险，内容使用
          AES-GCM 加密并在 24 小时后自动删除。本页当前只展示元数据。
        </p>
      </div>
    </section>
  );
}

const OVERLAY_STYLE: CSSProperties = {
  position: 'fixed',
  inset: 0,
  zIndex: 50,
  display: 'flex',
  justifyContent: 'flex-end',
  background: 'rgba(15, 23, 42, 0.42)',
};

const DRAWER_STYLE: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  width: 'min(780px, 100vw)',
  height: '100%',
  background: 'var(--color-surface)',
  boxShadow: 'var(--shadow-lg)',
};

const DRAWER_HEADER_STYLE: CSSProperties = {
  display: 'flex',
  alignItems: 'flex-start',
  justifyContent: 'space-between',
  gap: 16,
  padding: '18px 22px',
  borderBottom: '1px solid var(--color-border)',
};

const DRAWER_BODY_STYLE: CSSProperties = {
  flex: 1,
  overflowY: 'auto',
  padding: '20px 22px 28px',
};

export function RequestDetail({ requestID, onClose }: RequestDetailProps): ReactNode {
  const [state, setState] = useState<DetailState>({ status: 'loading' });
  const [reloadKey, setReloadKey] = useState(0);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const sequence = useRef(0);

  const load = useCallback(async (id: string) => {
    sequence.current += 1;
    const current = sequence.current;
    setState({ status: 'loading' });

    try {
      const detail = await apiRequest<RequestDetailResponse>(
        `/admin/api/requests/${encodeURIComponent(id)}`,
      );
      if (sequence.current !== current) {
        return;
      }
      setState({
        status: 'ready',
        detail: {
          request: detail.request,
          attempts: Array.isArray(detail.attempts) ? detail.attempts : [],
        },
      });
    } catch (error) {
      if (sequence.current !== current) {
        return;
      }
      if (isUnauthorizedError(error)) {
        setState({ status: 'error', message: '登录状态已失效，请重新登录后查看。' });
        return;
      }
      setState({ status: 'error', message: describeDetailError(error) });
    }
  }, []);

  useEffect(() => {
    void load(requestID);
  }, [load, requestID, reloadKey]);

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
        onClose();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [onClose]);

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
        aria-labelledby="request-detail-title"
        style={DRAWER_STYLE}
        onMouseDown={(event) => {
          event.stopPropagation();
        }}
      >
        <header style={DRAWER_HEADER_STYLE}>
          <div>
            <p className="eyebrow">请求观测</p>
            <h2 id="request-detail-title" style={{ margin: '4px 0 6px', fontSize: 20 }}>
              请求详情
            </h2>
            <p className="form-note" style={{ wordBreak: 'break-all' }}>
              <code>{requestID}</code>
            </p>
          </div>
          <button
            ref={closeButtonRef}
            type="button"
            className="button button--ghost"
            onClick={onClose}
          >
            关闭
          </button>
        </header>

        <div style={DRAWER_BODY_STYLE}>
          {state.status === 'loading' ? (
            <p className="form-note" role="status">
              正在加载请求详情…
            </p>
          ) : state.status === 'error' ? (
            <div className="state state--error" role="alert">
              <span className="state-icon" aria-hidden="true">
                !
              </span>
              <h3>请求详情加载失败</h3>
              <p>{state.message}</p>
              <button
                type="button"
                className="button button--ghost"
                onClick={() => {
                  setReloadKey((current) => current + 1);
                }}
              >
                重试
              </button>
            </div>
          ) : (
            <>
              <MetadataPanel request={state.detail.request} />
              <UsagePanel request={state.detail.request} />
              <AttemptsPanel attempts={state.detail.attempts ?? []} />
              <CaptureHintPanel />
            </>
          )}
        </div>
      </section>
    </div>
  );
}

export default RequestDetail;
