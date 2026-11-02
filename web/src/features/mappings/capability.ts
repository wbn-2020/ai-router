/**
 * 逐路由能力声明的前端工具（纯函数，无 JSX、无新依赖）。
 *
 * 能力是“管理员声明的上游事实”，不是自动探测结果：unknown / 空窗口 / 空档位
 * 都表示未声明，前端不做任何降级推断，也不复制 manifest 的交集与回退策略
 * （那属于 internal/gateway）。
 *
 * 服务端语义见 internal/store/capabilities.go 与 internal/adminapi/routes.go：
 * - input_modalities: unknown | text | text_image；
 * - context_window: 1..10000000，null 表示未声明；PATCH 传 0 清空；
 * - reasoning_levels: 固定枚举，空数组表示未声明；PATCH 传 [] 清空。
 */

import type { InputModalities, ModelRoute, ReasoningLevel } from './api';

/** CONTEXT_WINDOW_MAX 与 internal/store.ModelContextWindowMax 一致。 */
export const CONTEXT_WINDOW_MIN = 1;
export const CONTEXT_WINDOW_MAX = 10000000;

/** REASONING_LEVELS 与服务端规范化顺序一致，避免勾选顺序影响 PATCH 体。 */
export const REASONING_LEVELS: readonly ReasoningLevel[] = [
  'none',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
  'ultra',
];

/** MODALITY_OPTIONS 用于下拉框：第一个选项即“未声明”。 */
export const MODALITY_OPTIONS: ReadonlyArray<{ value: InputModalities; label: string }> = [
  { value: 'unknown', label: '未声明' },
  { value: 'text', label: 'text（仅文本）' },
  { value: 'text_image', label: 'text_image（文本+图片）' },
];

const REASONING_LEVEL_SET = new Set<string>(REASONING_LEVELS);

const MODALITY_SHORT_LABELS: Record<InputModalities, string> = {
  unknown: '未声明',
  text: 'text',
  text_image: 'text+image',
};

/** RouteCapability 是归一化后的能力声明，null / [] / unknown 统一为未声明。 */
export interface RouteCapability {
  modalities: InputModalities;
  window: number | null;
  levels: ReasoningLevel[];
}

type CapabilityFields = Pick<
  ModelRoute,
  'input_modalities' | 'context_window' | 'reasoning_levels'
>;

function isInputModalities(value: unknown): value is InputModalities {
  return value === 'unknown' || value === 'text' || value === 'text_image';
}

/** orderReasoningLevels 去重并按固定枚举顺序输出。 */
export function orderReasoningLevels(levels: Iterable<ReasoningLevel>): ReasoningLevel[] {
  const selected = new Set<string>(levels);
  return REASONING_LEVELS.filter((level) => selected.has(level));
}

/** normalizeRouteCapability 把视图字段（可能缺失）整理成可比较的三元组。 */
export function normalizeRouteCapability(
  route: CapabilityFields | null | undefined,
): RouteCapability {
  const modalities = isInputModalities(route?.input_modalities)
    ? route.input_modalities
    : 'unknown';
  const window =
    typeof route?.context_window === 'number' &&
    Number.isInteger(route.context_window) &&
    route.context_window > 0
      ? route.context_window
      : null;
  const rawLevels = Array.isArray(route?.reasoning_levels) ? route.reasoning_levels : [];
  const levels = orderReasoningLevels(
    rawLevels.filter((level): level is ReasoningLevel => REASONING_LEVEL_SET.has(level)),
  );
  return { modalities, window, levels };
}

/** sameReasoningLevels 按已排序数组比较，避免勾选顺序造成假变更。 */
export function sameReasoningLevels(
  left: readonly ReasoningLevel[],
  right: readonly ReasoningLevel[],
): boolean {
  return left.length === right.length && left.every((level, index) => level === right[index]);
}

/** formatContextWindow 输出紧凑窗口值：32000 → 32k，1000000 → 1M。 */
export function formatContextWindow(window: number | null): string {
  if (window === null) {
    return '未声明';
  }
  if (window >= 1000000 && window % 100000 === 0) {
    return `${window / 1000000}M`;
  }
  if (window % 1000 === 0) {
    return `${window / 1000}k`;
  }
  return String(window);
}

/** hasDeclaredCapability 判断路由是否声明了至少一项能力。 */
export function hasDeclaredCapability(route: CapabilityFields | null | undefined): boolean {
  const capability = normalizeRouteCapability(route);
  return (
    capability.modalities !== 'unknown' ||
    capability.window !== null ||
    capability.levels.length > 0
  );
}

/** describeRouteCapability 生成映射页的简短提示，三项均未声明时只写“未声明”。 */
export function describeRouteCapability(route: CapabilityFields | null | undefined): string {
  if (!hasDeclaredCapability(route)) {
    return '未声明';
  }
  const capability = normalizeRouteCapability(route);
  return [
    MODALITY_SHORT_LABELS[capability.modalities],
    formatContextWindow(capability.window),
    capability.levels.length === 0 ? '未声明' : `reasoning ${capability.levels.length} 档`,
  ].join(' · ');
}

/**
 * parseContextWindowInput 解析表单输入：留空 = 未声明（null），
 * 其余必须是 1..10000000 的整数，否则返回错误由调用方提示。
 */
export function parseContextWindowInput(
  raw: string,
): { ok: true; value: number | null } | { ok: false } {
  const trimmed = raw.trim();
  if (trimmed === '') {
    return { ok: true, value: null };
  }
  const value = Number(trimmed);
  if (
    !Number.isInteger(value) ||
    value < CONTEXT_WINDOW_MIN ||
    value > CONTEXT_WINDOW_MAX
  ) {
    return { ok: false };
  }
  return { ok: true, value };
}
