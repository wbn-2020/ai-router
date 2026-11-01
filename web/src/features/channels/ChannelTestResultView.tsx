import type { ReactNode } from 'react';

import { formatChannelTimestamp } from './channelForm';
import type { ChannelTestResult } from './types';

export function checkStatusLabel(status: string | undefined): string {
  return status === 'healthy' ? '通过' : status === 'unhealthy' ? '失败' : status === 'inconclusive' ? '不确定' : '未知';
}

export const INCONCLUSIVE_NOTE = '输出预算耗尽或响应提前结束，未能确认有效生成；不代表通过或失败。';

interface ChannelTestResultViewProps {
  result: ChannelTestResult;
  label: string;
  clientModel?: string;
}

/** 只展示本次探针，不代表渠道全部模型的可用性。 */
export function ChannelTestResultView({
  result,
  label,
  clientModel,
}: ChannelTestResultViewProps): ReactNode {
  const healthy = result.status === 'healthy';
  return (
    <section
      className={`model-check ${healthy ? 'model-check--healthy' : result.status === 'unhealthy' ? 'model-check--unhealthy' : ''}`}
      aria-label={label}
      aria-live="polite"
    >
      <div className="model-check__summary">
        <strong>单次有效生成检查</strong>
        <span className={healthy ? 'badge' : 'badge badge--muted'}>
          {checkStatusLabel(result.status)}
        </span>
      </div>
      <dl className="model-check__meta">
        <div>
          <dt>检查时间</dt>
          <dd><time dateTime={result.tested_at}>{formatChannelTimestamp(result.tested_at)}</time></dd>
        </div>
        <div>
          <dt>客户端模型</dt>
          <dd><code>{result.model || clientModel || '—'}</code></dd>
        </div>
        <div>
          <dt>上游模型</dt>
          <dd><code>{result.upstream_model}</code></dd>
        </div>
        <div>
          <dt>上游 HTTP</dt>
          <dd>{result.http_status === 0 ? '—' : result.http_status}</dd>
        </div>
        <div>
          <dt>响应头耗时</dt>
          <dd>{result.latency_ms} ms</dd>
        </div>
        <div><dt>总耗时</dt><dd>{result.duration_ms === undefined ? '未知' : `${result.duration_ms} ms`}</dd></div>
        <div><dt>协议</dt><dd><code>{result.protocol || '未知'}</code></dd></div>
        <div><dt>连接版本</dt><dd>{result.connection_revision ? `v${result.connection_revision}` : '未知'}</dd></div>
        <div><dt>检查类型</dt><dd>{result.kind === 'generation' ? '有效生成' : '未知'}</dd></div>
        <div><dt>错误码</dt><dd><code>{result.error_code || '—'}</code></dd></div>
      </dl>
      {result.status === 'inconclusive' ? <p className="form-note">{INCONCLUSIVE_NOTE}</p> : null}
      {result.error === undefined || result.error === '' ? null : (
        <p className={result.status === 'unhealthy' ? 'form-error model-check__error' : 'form-note'} role="alert">{result.status === 'unhealthy' ? '测试失败：' : '检查说明：'}{result.error}</p>
      )}
      <p className="form-note">仅代表本次模型、协议与连接版本的有效生成检查，不代表全部模型或全部能力；错误由服务端脱敏，不读取或展示完整 Key。</p>
      <a className="model-check__link" href={result.request_link}>查看该渠道最近的请求</a>
    </section>
  );
}
