import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';

import { MetricChart, type ChartSeries } from '@/features/dashboard/components/MetricChart';
import type { MetricPoint } from '@/types/api';

function point(timestamp: string, cpu: number | null): MetricPoint {
  return {
    timestamp,
    cpu_percent: cpu,
    memory_percent: null,
    disk_percent: null,
    load_1: null,
    network_rx_per_second: null,
    network_tx_per_second: null,
  };
}

const cpuSeries: ChartSeries[] = [
  { label: 'CPU', color: '#3b6ef6', value: (p) => p.cpu_percent },
];

describe('MetricChart', () => {
  it('says so when there is nothing to plot', () => {
    render(<MetricChart points={[]} series={cpuSeries} />);

    // An empty chart area with no explanation looks like a broken widget.
    expect(screen.getByText('No data yet for this range.')).toBeInTheDocument();
  });

  it('draws one line for a continuous series', () => {
    const { container } = render(
      <MetricChart
        points={[
          point('2026-08-25T10:00:00Z', 10),
          point('2026-08-25T10:01:00Z', 20),
          point('2026-08-25T10:02:00Z', 30),
        ]}
        series={cpuSeries}
        max={100}
      />,
    );

    expect(container.querySelectorAll('polyline')).toHaveLength(1);
  });

  it('breaks the line across a gap in collection', () => {
    const { container } = render(
      <MetricChart
        points={[
          point('2026-08-25T10:00:00Z', 10),
          point('2026-08-25T10:01:00Z', 20),
          // The sampler could not reach the host here.
          point('2026-08-25T10:02:00Z', null),
          point('2026-08-25T10:03:00Z', 40),
          point('2026-08-25T10:04:00Z', 50),
        ]}
        series={cpuSeries}
        max={100}
      />,
    );

    // Two segments, not one: interpolating across the gap would smooth an
    // outage into a plausible-looking line.
    expect(container.querySelectorAll('polyline')).toHaveLength(2);
  });

  it('plots nothing for a series that is entirely missing', () => {
    const { container } = render(
      <MetricChart
        points={[point('2026-08-25T10:00:00Z', null), point('2026-08-25T10:01:00Z', null)]}
        series={cpuSeries}
        max={100}
      />,
    );

    expect(container.querySelectorAll('polyline')).toHaveLength(0);
  });

  it('labels the chart and its series for assistive technology', () => {
    render(
      <MetricChart
        points={[point('2026-08-25T10:00:00Z', 10)]}
        series={cpuSeries}
        max={100}
      />,
    );

    expect(screen.getByRole('img', { name: /CPU over time/ })).toBeInTheDocument();
    // The legend is text, so the colour is not the only way to tell lines apart.
    expect(screen.getByText('CPU')).toBeInTheDocument();
  });

  it('scales to an observed maximum when none is fixed', () => {
    const { container } = render(
      <MetricChart
        points={[point('2026-08-25T10:00:00Z', 5), point('2026-08-25T10:01:00Z', 10)]}
        series={cpuSeries}
      />,
    );

    // Without a fixed max a flat, low series would otherwise be invisible at
    // the bottom of a 0-100 axis.
    const polyline = container.querySelector('polyline');
    expect(polyline).not.toBeNull();
    expect(polyline?.getAttribute('points')).toBeTruthy();
  });
});
