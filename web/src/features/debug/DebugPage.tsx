import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';

import { ApiError, apiRequest, isUnauthorizedError } from '../../lib/api';
import { checkStatusLabel, INCONCLUSIVE_NOTE } from '../channels/ChannelTestResultView';
import type { ChannelTestResult } from '../channels/types';

/**
 * 调试页（T22）。
 *
 * 数据来源：
 * - GET /admin/api/channels 与 /admin/api/models 提供渠道/模型选择项；
 * - POST /admin/api/debug/channels/{id}/test 使用渠道上已启用的映射发最小请求；
 * - POST /admin/api/debug/models/{model}/test 只使用该模型已启用的映射渠道，
 *   按 route.priority → channel.priority → last_used_at 选择第一个候选。
 *
 * 安全边界：
 * - 页面不接触上游 Key，渠道列表只使用名称/协议/启停字段；
 * - 服务端返回的错误已经脱敏，页面只展示服务端文案；
 * - 测试会消耗上游 tokens，页面在操作前明确提示。
 */

interface ChannelOption {
  id: number;
  name: string;
  protocol?: string;
  enabled?: boolean;
}

interface ChannelListResponse {
  channels?: ChannelOption[];
}

interface ModelsResponse {
  models?: string[];
}

/** DebugTestResult 与后端 debugTestResponse 对应；error 已脱敏。 */
export type DebugTestResult = ChannelTestResult;

type TestKind = 'channel' | 'model';

interface TestOutcome {
  kind: TestKind;
  result: DebugTestResult;
}

const FORM_GRID_STYLE: CSSProperties = {
  display: 'grid',
  gap: 12,
  gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
  alignItems: 'end',
};

const RESULT_ACTION_STYLE: CSSProperties = {
  marginTop: 12,
};

function describeDebugError(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'model_not_available':
        return error.message || '该模型没有启用的映射渠道。';
      case 'model_route_not_found':
        return error.message || '该模型在此渠道没有启用的映射。';
      case 'channel_has_no_mapping':
        return error.message || '该渠道没有启用的模型映射。';
      case 'invalid_model':
        return error.message || '模型名无效。';
      case 'channel_not_found':
        return '渠道不存在，可能已被删除。';
      case 'channels_unavailable':
        return '调试暂不可用：数据库或主密钥未就绪。';
      default:
        break;
    }
    if (error.status === 400) {
      return error.message || '调试请求无效。';
    }
    if (error.status === 503) {
      return '调试暂不可用，请稍后重试。';
    }
    if (error.status >= 500) {
      return '服务器暂时不可用，请稍后重试。';
    }
    return error.message || `测试失败（HTTP ${error.status}）`;
  }
  return '测试失败，请检查网络连接后重试。';
}

function channelLabel(channel: ChannelOption): string {
  const protocol = channel.protocol ?? '未知协议';
  const suffix = channel.enabled === false ? '（已禁用）' : '';
  return `${channel.name} · ${protocol}${suffix}`;
}

function isHealthy(result: DebugTestResult): boolean {
  return result.status === 'healthy';
}

export function DebugPage(): ReactNode {
  const [channels, setChannels] = useState<ChannelOption[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [optionsLoading, setOptionsLoading] = useState(true);
  const [optionsError, setOptionsError] = useState<string | null>(null);
  const [optionsReload, setOptionsReload] = useState(0);

  const [channelID, setChannelID] = useState('');
  const [channelModel, setChannelModel] = useState('');
  const [model, setModel] = useState('');

  const [running, setRunning] = useState<TestKind | null>(null);
  const [outcome, setOutcome] = useState<TestOutcome | null>(null);
  const [resultError, setResultError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);

  const optionsSequence = useRef(0);

  useEffect(() => {
    optionsSequence.current += 1;
    const current = optionsSequence.current;

    setOptionsLoading(true);
    setOptionsError(null);

    void Promise.allSettled([
      apiRequest<ChannelListResponse>('/admin/api/channels'),
      apiRequest<ModelsResponse>('/admin/api/models'),
    ])
      .then(([channelResult, modelResult]) => {
        if (optionsSequence.current !== current) {
          return;
        }

        const unauthorizedError = [channelResult, modelResult].find(
          (item): item is PromiseRejectedResult =>
            item.status === 'rejected' && isUnauthorizedError(item.reason),
        );
        if (unauthorizedError !== undefined) {
          setUnauthorized(true);
          return;
        }

        if (channelResult.status === 'fulfilled') {
          const list = channelResult.value.channels ?? [];
          setChannels(
            list
              .filter(
                (channel) =>
                  typeof channel?.id === 'number' &&
                  channel.id > 0 &&
                  typeof channel?.name === 'string' &&
                  channel.name !== '',
              )
              .sort((left, right) => left.name.localeCompare(right.name)),
          );
        }
        if (modelResult.status === 'fulfilled') {
          const list = modelResult.value.models ?? [];
          setModels(
            list
              .filter((item): item is string => typeof item === 'string' && item !== '')
              .sort((left, right) => left.localeCompare(right)),
          );
        }
        if (channelResult.status === 'rejected' || modelResult.status === 'rejected') {
          setOptionsError('渠道或模型列表加载失败，请检查服务后重试。');
        }
      })
      .finally(() => {
        if (optionsSequence.current === current) {
          setOptionsLoading(false);
        }
      });
  }, [optionsReload]);

  // 首次加载后默认选中第一个渠道与模型，减少无效点击。
  useEffect(() => {
    if (channelID !== '' || channels.length === 0) {
      return;
    }
    const first = channels[0];
    if (first !== undefined) {
      setChannelID(String(first.id));
    }
  }, [channels, channelID]);

  useEffect(() => {
    if (model !== '' || models.length === 0) {
      return;
    }
    const first = models[0];
    if (first !== undefined) {
      setModel(first);
    }
  }, [models, model]);

  const runChannelTest = useCallback(async () => {
    if (channelID === '') {
      setResultError('请选择渠道。');
      return;
    }

    setRunning('channel');
    setOutcome(null);
    setResultError(null);
    try {
      const payload = await apiRequest<DebugTestResult>(
        `/admin/api/debug/channels/${encodeURIComponent(channelID)}/test`,
        {
          method: 'POST',
          body: JSON.stringify(
            channelModel.trim() === '' ? {} : { model: channelModel.trim() },
          ),
        },
      );
      setOutcome({ kind: 'channel', result: payload });
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setResultError(describeDebugError(error));
    } finally {
      setRunning(null);
    }
  }, [channelID, channelModel]);

  const runModelTest = useCallback(async () => {
    const name = model.trim();
    if (name === '') {
      setResultError('请选择模型。');
      return;
    }

    setRunning('model');
    setOutcome(null);
    setResultError(null);
    try {
      const payload = await apiRequest<DebugTestResult>(
        `/admin/api/debug/models/${encodeURIComponent(name)}/test`,
        { method: 'POST' },
      );
      setOutcome({ kind: 'model', result: payload });
    } catch (error) {
      if (isUnauthorizedError(error)) {
        setUnauthorized(true);
        return;
      }
      setResultError(describeDebugError(error));
    } finally {
      setRunning(null);
    }
  }, [model]);

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

  const healthy = outcome !== null && isHealthy(outcome.result);
  const result = outcome?.result ?? null;

  return (
    <>
      <header className="page-header">
        <div>
          <h2>调试</h2>
          <p>
            检查指定模型的有效生成、模型映射与脱敏错误。测试只发送最小 ping 请求，不携带任何用户
            prompt。
          </p>
        </div>
        <span className="badge badge--muted">会消耗上游 tokens</span>
      </header>

      <p className="form-note" role="note">
        每次测试都会向上游发送最小请求（输出上限 16 tokens），消耗少量上游 tokens，并将结果写入
        最近检查。仅代表本次模型、协议与连接版本，不代表全部模型或全部能力，也不等同当前运行状态。
      </p>

      {optionsError === null ? null : (
        <p className="form-note" role="alert">
          {optionsError}{' '}
          <button
            type="button"
            className="button button--ghost"
            onClick={() => {
              setOptionsReload((current) => current + 1);
            }}
          >
            重试
          </button>
        </p>
      )}

      <section className="panel" aria-labelledby="debug-channel-title">
        <div className="panel-header">
          <h2 id="debug-channel-title">渠道有效生成检查</h2>
          <span className="badge badge--muted">
            {optionsLoading ? '正在加载…' : `${channels.length} 个渠道`}
          </span>
        </div>
        <div className="panel-body">
          <div style={FORM_GRID_STYLE}>
            <div className="field">
              <label htmlFor="debug-channel">渠道</label>
              <select
                id="debug-channel"
                className="input"
                name="channel"
                value={channelID}
                disabled={optionsLoading || channels.length === 0}
                onChange={(event) => {
                  setChannelID(event.target.value);
                }}
              >
                <option value="">选择渠道</option>
                {channels.map((channel) => (
                  <option key={channel.id} value={String(channel.id)}>
                    {channelLabel(channel)}
                  </option>
                ))}
              </select>
            </div>

            <div className="field">
              <label htmlFor="debug-channel-model">模型（可选）</label>
              <select
                id="debug-channel-model"
                className="input"
                name="channel-model"
                value={channelModel}
                disabled={optionsLoading || models.length === 0}
                onChange={(event) => {
                  setChannelModel(event.target.value);
                }}
              >
                <option value="">使用渠道默认映射</option>
                {models.map((item) => (
                  <option key={item} value={item}>
                    {item}
                  </option>
                ))}
              </select>
            </div>

            <div className="field">
              <button
                type="button"
                className="button"
                onClick={() => {
                  void runChannelTest();
                }}
                disabled={running !== null || optionsLoading || channelID === ''}
              >
                {running === 'channel' ? '正在测试…' : '测试渠道'}
              </button>
            </div>
          </div>

          {channels.length === 0 && !optionsLoading ? (
            <p className="form-note" style={RESULT_ACTION_STYLE}>
              暂无渠道，请先在“渠道”页创建。
            </p>
          ) : null}
        </div>
      </section>

      <section className="panel" aria-labelledby="debug-model-title">
        <div className="panel-header">
          <h2 id="debug-model-title">模型路由测试</h2>
          <span className="badge badge--muted">仅使用已启用映射</span>
        </div>
        <div className="panel-body">
          <div style={FORM_GRID_STYLE}>
            <div className="field">
              <label htmlFor="debug-model">客户端模型</label>
              <select
                id="debug-model"
                className="input"
                name="model"
                value={model}
                disabled={optionsLoading || models.length === 0}
                onChange={(event) => {
                  setModel(event.target.value);
                }}
              >
                <option value="">选择模型</option>
                {models.map((item) => (
                  <option key={item} value={item}>
                    {item}
                  </option>
                ))}
              </select>
            </div>

            <div className="field">
              <button
                type="button"
                className="button"
                onClick={() => {
                  void runModelTest();
                }}
                disabled={running !== null || optionsLoading || model === ''}
              >
                {running === 'model' ? '正在测试…' : '测试模型'}
              </button>
            </div>
          </div>

          <p className="muted" style={RESULT_ACTION_STYLE}>
            按 route.priority → channel.priority → last_used_at 选择第一个候选渠道，不会自动尝试
            第二个渠道。
          </p>
        </div>
      </section>

      <section className="panel" aria-labelledby="debug-result-title" aria-live="polite">
        <div className="panel-header">
          <h2 id="debug-result-title">最近一次测试</h2>
          {outcome === null ? null : (
            <span className={healthy ? 'badge' : 'badge badge--muted'}>
              {outcome.kind === 'channel' ? '渠道测试' : '模型测试'} ·{' '}
              {checkStatusLabel(outcome.result.status)}
            </span>
          )}
        </div>

        {resultError !== null ? (
          <div className="panel-body">
            <p className="form-error" role="alert">
              {resultError}
            </p>
          </div>
        ) : result === null ? (
          <div className="state state--empty">
            <span className="state-icon" aria-hidden="true">
              ◇
            </span>
            <h3>暂无调试结果</h3>
            <p>选择渠道或模型运行测试，查看状态、延迟与脱敏错误。</p>
          </div>
        ) : (
          <div className="panel-body">
            <ul className="detail-list">
              <li>
                <span>状态</span>
                <code
                  style={{
                    color: healthy ? 'var(--color-success)' : result.status === 'unhealthy' ? 'var(--color-danger)' : undefined,
                  }}
                >
                  {['healthy', 'unhealthy', 'inconclusive'].includes(result.status) ? result.status : 'unknown'}
                </code>
              </li>
              <li>
                <span>上游 HTTP</span>
                <code>{result.http_status === 0 ? '—' : result.http_status}</code>
              </li>
              <li>
                <span>响应头耗时</span>
                <code>{result.latency_ms} ms</code>
              </li>
              <li><span>总耗时</span><code>{result.duration_ms === undefined ? '未知' : `${result.duration_ms} ms`}</code></li>
              <li><span>协议</span><code>{result.protocol || '未知'}</code></li>
              <li><span>连接版本</span><code>{result.connection_revision ? `v${result.connection_revision}` : '未知'}</code></li>
              <li><span>检查类型</span><code>{result.kind === 'generation' ? '有效生成' : '未知'}</code></li>
              <li><span>错误码</span><code>{result.error_code || '—'}</code></li>
              <li>
                <span>渠道</span>
                <code>
                  {result.channel_name}（#{result.channel_id}）
                </code>
              </li>
              <li>
                <span>上游模型</span>
                <code>{result.upstream_model}</code>
              </li>
              {result.model === undefined || result.model === '' ? null : (
                <li>
                  <span>客户端模型</span>
                  <code>{result.model}</code>
                </li>
              )}
            </ul>

            {result.status === 'inconclusive' ? <p className="form-note">{INCONCLUSIVE_NOTE}</p> : null}
            {result.error === undefined || result.error === '' ? null : (
              <p className={result.status === 'unhealthy' ? 'form-error' : 'form-note'} role="alert" style={RESULT_ACTION_STYLE}>
                {result.error}
              </p>
            )}

            <p style={RESULT_ACTION_STYLE}>
              <a href={result.request_link}>查看该渠道最近的请求</a>
            </p>
          </div>
        )}
      </section>
    </>
  );
}

export default DebugPage;
