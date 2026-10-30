/**
 * 总览页的轻量 SVG 图表。
 *
 * 约束（T15）：
 * - 不引入图表库，只用 React 自绘 SVG，避免为单一图形增加打包体积；
 * - 只消费后端 /admin/api/stats 已聚合的小时桶，不在前端二次聚合；
 * - 坐标计算对空数组、非法数值做保护，任何输入都不会把 NaN 写进 SVG。
 */

import type { ReactNode } from 'react';

/** HourlyTrendPoint 是趋势图一个小时的聚合数据。 */
export interface HourlyTrendPoint {
  /** bucket 是后端小时桶的起点（RFC3339，UTC）。 */
  bucket: string;
  request_count: number;
  success_count: number;
  error_count: number;
}

const WIDTH = 720;
const HEIGHT = 220;
const PADDING_TOP = 16;
const PADDING_RIGHT = 16;
const PADDING_BOTTOM = 30;
const PADDING_LEFT = 44;

const PLOT_WIDTH = WIDTH - PADDING_LEFT - PADDING_RIGHT;
const PLOT_HEIGHT = HEIGHT - PADDING_TOP - PADDING_BOTTOM;
const PLOT_BOTTOM = PADDING_TOP + PLOT_HEIGHT;

/** BUCKET_FORMATTER 按管理员本地时区显示桶起点，精确到分钟。 */
const BUCKET_FORMATTER = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
});

interface BarSegment {
  key: string;
  x: number;
  y: number;
  width: number;
  height: number;
  fill: string;
}

interface XAxisLabel {
  key: string;
  x: number;
  anchor: 'start' | 'middle' | 'end';
  text: string;
}

interface ChartGeometry {
  /** segments 与 points 一一对应；极端数据下可能为空数组。 */
  segments: BarSegment[][];
  labels: XAxisLabel[];
  maxCount: number;
  ticks: Array<{ y: number; label: string }>;
}

function safeCount(value: number): number {
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function formatBucket(bucket: string): string {
  const parsed = new Date(bucket);
  if (Number.isNaN(parsed.getTime())) {
    return bucket;
  }
  return BUCKET_FORMATTER.format(parsed);
}

function formatAxisValue(value: number): string {
  return Number.isFinite(value) ? String(Math.round(value)) : '0';
}

/**
 * buildChartGeometry 把小时桶换算成 SVG 坐标。
 *
 * 每个桶画成堆叠柱：底部成功、其上错误；若后端数据异常出现
 * request_count > success + error，余量用中性色补齐，保证柱高对应请求数。
 */
function buildChartGeometry(points: HourlyTrendPoint[]): ChartGeometry {
  let maxCount = 1;
  for (const point of points) {
    const stacked = safeCount(point.success_count) + safeCount(point.error_count);
    maxCount = Math.max(maxCount, safeCount(point.request_count), stacked);
  }

  const slot = PLOT_WIDTH / points.length;
  const barWidth = Math.max(1, Math.min(28, slot * 0.62));

  const segments = points.map((point, index) => {
    const success = safeCount(point.success_count);
    const error = safeCount(point.error_count);
    const other = Math.max(0, safeCount(point.request_count) - success - error);

    const successHeight = (success / maxCount) * PLOT_HEIGHT;
    const errorHeight = (error / maxCount) * PLOT_HEIGHT;
    const otherHeight = (other / maxCount) * PLOT_HEIGHT;

    const x = PADDING_LEFT + slot * index + (slot - barWidth) / 2;
    const successY = PLOT_BOTTOM - successHeight;
    const errorY = successY - errorHeight;
    const otherY = errorY - otherHeight;

    const result: BarSegment[] = [];
    if (successHeight > 0) {
      result.push({
        key: `success-${index}`,
        x,
        y: successY,
        width: barWidth,
        height: successHeight,
        fill: 'var(--color-primary)',
      });
    }
    if (errorHeight > 0) {
      result.push({
        key: `error-${index}`,
        x,
        y: errorY,
        width: barWidth,
        height: errorHeight,
        fill: 'var(--color-danger)',
      });
    }
    if (otherHeight > 0) {
      result.push({
        key: `other-${index}`,
        x,
        y: otherY,
        width: barWidth,
        height: otherHeight,
        fill: 'var(--color-border-strong)',
      });
    }
    return result;
  });

  const labelIndices = Array.from(
    new Set([0, Math.floor((points.length - 1) / 2), points.length - 1]),
  ).sort((left, right) => left - right);

  const labels: XAxisLabel[] = [];
  for (const index of labelIndices) {
    const point = points[index];
    if (point === undefined) {
      continue;
    }
    let anchor: XAxisLabel['anchor'] = 'middle';
    let x = PADDING_LEFT + slot * index + slot / 2;
    if (index === 0) {
      anchor = points.length === 1 ? 'middle' : 'start';
      x = PADDING_LEFT;
    } else if (index === points.length - 1) {
      anchor = 'end';
      x = PADDING_LEFT + PLOT_WIDTH;
    }
    labels.push({
      key: `label-${index}`,
      x,
      anchor,
      text: formatBucket(point.bucket),
    });
  }

  const ticks = [0, 0.5, 1].map((ratio) => ({
    y: PLOT_BOTTOM - PLOT_HEIGHT * ratio,
    label: formatAxisValue(maxCount * ratio),
  }));

  return { segments, labels, maxCount, ticks };
}

const LEGEND_ITEM_STYLE = {
  display: 'inline-flex',
  alignItems: 'center',
  gap: 6,
} as const;

const LEGEND_SWATCH_STYLE = {
  width: 10,
  height: 10,
  borderRadius: 2,
} as const;

/**
 * HourlyTrendChart 渲染按小时的请求趋势。
 *
 * 图例说明颜色；每个柱子附带原生 <title>，悬停可见该小时的请求/成功/错误数。
 */
export function HourlyTrendChart({ points }: { points: HourlyTrendPoint[] }): ReactNode {
  if (points.length === 0) {
    return null;
  }

  const geometry = buildChartGeometry(points);
  const totalRequests = points.reduce((sum, point) => sum + safeCount(point.request_count), 0);
  const totalErrors = points.reduce((sum, point) => sum + safeCount(point.error_count), 0);

  return (
    <div>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 16,
          marginBottom: 10,
          color: 'var(--color-muted)',
          fontSize: 12,
        }}
      >
        <span style={LEGEND_ITEM_STYLE}>
          <span
            aria-hidden="true"
            style={{ ...LEGEND_SWATCH_STYLE, background: 'var(--color-primary)' }}
          />
          成功
        </span>
        <span style={LEGEND_ITEM_STYLE}>
          <span
            aria-hidden="true"
            style={{ ...LEGEND_SWATCH_STYLE, background: 'var(--color-danger)' }}
          />
          错误
        </span>
        <span style={{ marginLeft: 'auto' }}>
          每小时峰值 {formatAxisValue(geometry.maxCount)} 次
        </span>
      </div>

      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label={`按小时请求趋势：共 ${totalRequests} 次请求，${totalErrors} 次错误，${points.length} 个小时桶`}
        style={{ display: 'block', width: '100%', height: 'auto' }}
      >
        {geometry.ticks.map((tick, index) => (
          <g key={`tick-${index}`}>
            <line
              x1={PADDING_LEFT}
              x2={PADDING_LEFT + PLOT_WIDTH}
              y1={tick.y}
              y2={tick.y}
              stroke="var(--color-border)"
              strokeWidth={1}
            />
            <text
              x={PADDING_LEFT - 8}
              y={tick.y}
              textAnchor="end"
              dominantBaseline="middle"
              fill="var(--color-muted)"
              fontSize={10}
            >
              {tick.label}
            </text>
          </g>
        ))}

        {points.map((point, index) => (
          <g key={`bar-${index}`}>
            <title>
              {`${formatBucket(point.bucket)}：请求 ${safeCount(point.request_count)}，成功 ${safeCount(point.success_count)}，错误 ${safeCount(point.error_count)}`}
            </title>
            {(geometry.segments[index] ?? []).map((segment) => (
              <rect
                key={segment.key}
                x={segment.x}
                y={segment.y}
                width={segment.width}
                height={segment.height}
                fill={segment.fill}
              />
            ))}
          </g>
        ))}

        {geometry.labels.map((label) => (
          <text
            key={label.key}
            x={label.x}
            y={HEIGHT - 8}
            textAnchor={label.anchor}
            fill="var(--color-muted)"
            fontSize={10}
          >
            {label.text}
          </text>
        ))}
      </svg>
    </div>
  );
}
