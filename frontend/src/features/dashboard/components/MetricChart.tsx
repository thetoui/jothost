import { useMemo } from 'react';

import { formatClockTime } from '@/features/dashboard/format';
import type { MetricPoint } from '@/types/api';

/** One plotted line. */
export interface ChartSeries {
  label: string;
  /** A CSS colour. */
  color: string;
  /** Extracts this series' value from a point; null means "no reading". */
  value: (point: MetricPoint) => number | null;
}

interface MetricChartProps {
  points: MetricPoint[];
  series: ChartSeries[];
  /** Fixes the y-axis maximum. Percentages pass 100 so the scale is stable. */
  max?: number;
  /** Formats a y-axis label. */
  formatValue?: (value: number) => string;
  height?: number;
}

// The viewBox is fixed and the SVG scales to its container, so the chart is
// resolution-independent without measuring the DOM.
const VIEW_WIDTH = 600;
const PADDING = { top: 8, right: 8, bottom: 20, left: 44 };

/**
 * MetricChart plots metric history as an inline SVG.
 *
 * A charting library would be several hundred kilobytes for what is a
 * polyline and two axes; this keeps the bundle small and the rendering
 * predictable. It is deliberately minimal — if the panel later needs
 * brushing, zooming, or stacked areas, that is the point to reconsider.
 *
 * Gaps matter: a null reading means the sampler could not reach the host, and
 * the line breaks rather than interpolating across it, so an outage is visible
 * instead of smoothed away.
 */
export function MetricChart({
  points,
  series,
  max,
  formatValue = (value) => value.toFixed(0),
  height = 180,
}: MetricChartProps) {
  const chart = useMemo(
    () => buildChart(points, series, max, height),
    [points, series, max, height],
  );

  if (points.length === 0) {
    return (
      <p className="py-8 text-center text-sm text-slate-400">
        No data yet for this range.
      </p>
    );
  }

  return (
    <div>
      <svg
        viewBox={`0 0 ${VIEW_WIDTH} ${height}`}
        // The height is set in pixels rather than left to the aspect ratio.
        // With `h-auto` the rendered height was the card's width times
        // 180/600, so the chart grew with the window: on a wide screen the two
        // stacked charts were nearly the height of the viewport between them,
        // and the dashboard's own figures were pushed off the bottom.
        //
        // preserveAspectRatio="none" is what makes this safe — the drawing
        // already stretches to whatever box it is given, which is what a time
        // series wants horizontally and what keeps the axis readable.
        style={{ height }}
        className="w-full"
        role="img"
        aria-label={`${series.map((s) => s.label).join(' and ')} over time`}
        preserveAspectRatio="none"
      >
        {chart.gridLines.map((line, index) => {
          const label = formatValue(line.value);
          // At a very small scale several gridlines round to the same label.
          // Repeating "0 B" four times up the axis reads as a rendering bug,
          // so only the first occurrence is labelled.
          const duplicate =
            index > 0 && formatValue(chart.gridLines[index - 1]!.value) === label;

          return (
            <g key={line.value}>
              <line
                x1={PADDING.left}
                y1={line.y}
                x2={VIEW_WIDTH - PADDING.right}
                y2={line.y}
                className="stroke-slate-200"
                strokeWidth={1}
              />
              {!duplicate && (
                <text
                  x={PADDING.left - 6}
                  y={line.y + 3}
                  textAnchor="end"
                  className="fill-slate-400 text-[9px]"
                >
                  {label}
                </text>
              )}
            </g>
          );
        })}

        {chart.lines.map((line) => (
          <g key={line.label}>
            {line.segments.map((segment, index) => (
              <polyline
                key={`${line.label}-${index}`}
                points={segment}
                fill="none"
                stroke={line.color}
                strokeWidth={1.75}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ))}
          </g>
        ))}

        {chart.timeLabels.map((label) => (
          <text
            key={label.x}
            x={label.x}
            y={height - 6}
            textAnchor="middle"
            className="fill-slate-400 text-[9px]"
          >
            {label.text}
          </text>
        ))}
      </svg>

      <ul className="mt-2 flex flex-wrap gap-4">
        {series.map((entry) => (
          <li key={entry.label} className="flex items-center gap-1.5 text-xs text-slate-600">
            <span
              aria-hidden="true"
              className="h-2 w-2 rounded-full"
              style={{ backgroundColor: entry.color }}
            />
            {entry.label}
          </li>
        ))}
      </ul>
    </div>
  );
}

interface BuiltLine {
  label: string;
  color: string;
  /** Each segment is an unbroken run of readings. */
  segments: string[];
}

interface BuiltChart {
  lines: BuiltLine[];
  gridLines: { value: number; y: number }[];
  timeLabels: { x: number; text: string }[];
}

function buildChart(
  points: MetricPoint[],
  series: ChartSeries[],
  fixedMax: number | undefined,
  height: number,
): BuiltChart {
  const plotWidth = VIEW_WIDTH - PADDING.left - PADDING.right;
  const plotHeight = height - PADDING.top - PADDING.bottom;

  const observedMax = series.reduce((currentMax, entry) => {
    return points.reduce((innerMax, point) => {
      const value = entry.value(point);
      return value !== null && value > innerMax ? value : innerMax;
    }, currentMax);
  }, 0);

  // A flat-zero series would otherwise divide by zero; 1 keeps the axis sane.
  const max = fixedMax ?? Math.max(observedMax * 1.1, 1);

  const xFor = (index: number) => {
    if (points.length === 1) {
      return PADDING.left + plotWidth / 2;
    }
    return PADDING.left + (index / (points.length - 1)) * plotWidth;
  };
  const yFor = (value: number) =>
    PADDING.top + plotHeight - Math.min(value / max, 1) * plotHeight;

  const lines = series.map((entry) => {
    const segments: string[] = [];
    let current: string[] = [];

    points.forEach((point, index) => {
      const value = entry.value(point);
      if (value === null) {
        // A gap in collection breaks the line rather than being interpolated
        // across, so an outage stays visible.
        if (current.length > 1) {
          segments.push(current.join(' '));
        }
        current = [];
        return;
      }
      current.push(`${xFor(index).toFixed(1)},${yFor(value).toFixed(1)}`);
    });

    if (current.length > 1) {
      segments.push(current.join(' '));
    } else if (current.length === 1) {
      // A lone reading has no line to draw; repeating the point renders a dot.
      segments.push(`${current[0]} ${current[0]}`);
    }

    return { label: entry.label, color: entry.color, segments };
  });

  const gridLines = [0, 0.25, 0.5, 0.75, 1].map((fraction) => ({
    value: max * fraction,
    y: PADDING.top + plotHeight - fraction * plotHeight,
  }));

  // Four labels is enough to orient without crowding the axis.
  const labelCount = Math.min(4, points.length);
  const timeLabels = Array.from({ length: labelCount }, (_, i) => {
    const index = Math.round((i / Math.max(labelCount - 1, 1)) * (points.length - 1));
    const point = points[index];
    return {
      x: xFor(index),
      text: point ? formatClockTime(point.timestamp) : '',
    };
  });

  return { lines, gridLines, timeLabels };
}
