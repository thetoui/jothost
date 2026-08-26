import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { DashboardPage } from '@/pages/DashboardPage';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import type { DashboardSnapshot, MetricSeries } from '@/types/api';

const server: DashboardSnapshot['server'] = {
  id: '11111111-2222-3333-4444-555555555555',
  hostname: 'web01',
  os_name: 'Debian GNU/Linux',
  os_version: '12',
  kernel: '6.1.0',
  architecture: 'amd64',
  ipv4: '192.0.2.10',
  ipv6: null,
  status: 'online',
  agent_version: '0.1.0-dev',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

/** snapshot builds a healthy dashboard payload, overridable per test. */
function snapshot(overrides: Partial<DashboardSnapshot> = {}): DashboardSnapshot {
  return {
    server,
    system: {
      available: true,
      data: {
        hostname: 'web01',
        os_name: 'Debian GNU/Linux',
        os_version: '12',
        kernel_version: '6.1.0',
        architecture: 'amd64',
        uptime_seconds: 90_000,
        boot_time: '2026-01-01T00:00:00Z',
        cores: 4,
      },
    },
    cpu: {
      available: true,
      data: {
        usage_percent: 12.5,
        user_percent: 8,
        system_percent: 4,
        iowait_percent: 0.5,
        idle_percent: 87.5,
        cores: 4,
        sample_window: '30s',
      },
    },
    memory: {
      available: true,
      data: {
        total_bytes: 16 * 1024 ** 3,
        available_bytes: 8 * 1024 ** 3,
        used_bytes: 8 * 1024 ** 3,
        free_bytes: 4 * 1024 ** 3,
        buffers_bytes: 0,
        cached_bytes: 2 * 1024 ** 3,
        swap_total_bytes: 0,
        swap_used_bytes: 0,
        swap_free_bytes: 0,
        used_percent: 50,
        swap_used_percent: 0,
      },
    },
    disk: {
      available: true,
      data: {
        filesystems: [
          {
            device: '/dev/sda1',
            mount_point: '/',
            type: 'ext4',
            total_bytes: 100 * 1024 ** 3,
            used_bytes: 45 * 1024 ** 3,
            free_bytes: 55 * 1024 ** 3,
            available_bytes: 55 * 1024 ** 3,
            used_percent: 45,
            inodes_used_percent: 10,
            read_only: false,
          },
        ],
        total_bytes: 100 * 1024 ** 3,
        used_bytes: 45 * 1024 ** 3,
      },
    },
    network: {
      available: true,
      data: {
        interfaces: [
          {
            name: 'eth0',
            rx_bytes: 1000,
            tx_bytes: 500,
            rx_errors: 0,
            tx_errors: 0,
            rx_bytes_per_second: 2048,
            tx_bytes_per_second: 1024,
          },
        ],
        total_rx_bytes: 1000,
        total_tx_bytes: 500,
        sample_window: '30s',
      },
    },
    load: {
      available: true,
      data: {
        load_1: 0.5,
        load_5: 0.4,
        load_15: 0.3,
        running_processes: 1,
        total_processes: 200,
        cores: 4,
        load_per_core: 0.125,
      },
    },
    services: {
      available: true,
      data: [
        { name: 'postgres', kind: 'dependency', running: true, status: 'running' },
        { name: 'nginx', kind: 'systemd', running: true, status: 'running' },
      ],
    },
    alerts: [],
    generated_at: '2026-08-25T10:00:00Z',
    ...overrides,
  };
}

const emptySeries: MetricSeries = {
  range: '1h',
  bucket: '1m0s',
  from: '2026-08-25T09:00:00Z',
  to: '2026-08-25T10:00:00Z',
  points: [],
};

/** mockDashboard answers both the snapshot and the history request. */
function mockDashboard(payload: DashboardSnapshot, series: MetricSeries = emptySeries) {
  vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
    const url = String(input);
    if (url.includes('/metrics')) {
      return envelopeResponse(series);
    }
    return envelopeResponse(payload);
  });
}

describe('DashboardPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
  });

  it('renders the host and its live metrics', async () => {
    mockDashboard(snapshot());
    renderWithProviders(<DashboardPage />);

    expect(await screen.findByRole('heading', { name: 'web01' })).toBeInTheDocument();
    expect(screen.getByText('Online')).toBeInTheDocument();

    // Each gauge exposes its value through progressbar semantics rather than
    // relying on the bar's width alone.
    expect(screen.getByRole('progressbar', { name: 'Usage' })).toHaveAttribute(
      'aria-valuenow',
      '13',
    );
    expect(screen.getByRole('progressbar', { name: 'Used' })).toHaveAttribute(
      'aria-valuenow',
      '50',
    );
    expect(screen.getByRole('progressbar', { name: '/' })).toHaveAttribute('aria-valuenow', '45');
  });

  it('shows server details and uptime', async () => {
    mockDashboard(snapshot());
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });

    // Scoped to the Server panel: the headline tiles also carry the OS and the
    // uptime, so an unscoped query now matches in two places by design.
    const serverPanel = within(screen.getByRole('region', { name: 'Server' }));
    expect(serverPanel.getByText('Debian GNU/Linux 12')).toBeInTheDocument();
    // 90000 seconds is one day and one hour.
    expect(serverPanel.getByText('1d 1h')).toBeInTheDocument();
  });

  // The headline row is the first thing read, so it must carry the figures
  // rather than only repeating what the panels below already say.
  it('summarises the host in the headline tiles', async () => {
    mockDashboard(snapshot());
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });

    for (const label of ['CPU', 'Memory', 'Disk', 'Uptime']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    // The fullest filesystem is the one worth showing, not the root.
    expect(screen.getAllByText('/').length).toBeGreaterThan(0);
  });

  it('reports a healthy host as having no alerts', async () => {
    mockDashboard(snapshot());
    renderWithProviders(<DashboardPage />);

    expect(await screen.findByText(/No alerts/)).toBeInTheDocument();
  });

  it('lists alerts with the critical ones first', async () => {
    mockDashboard(
      snapshot({
        alerts: [
          { severity: 'warning', category: 'memory', message: 'Memory is 86% used' },
          { severity: 'critical', category: 'disk', message: 'Disk /var is 95% full' },
        ],
      }),
    );
    renderWithProviders(<DashboardPage />);

    const alerts = await screen.findAllByRole('listitem');
    const messages = alerts.map((item) => item.textContent ?? '');

    // The worst problem must be the first thing read.
    const criticalIndex = messages.findIndex((text) => text.includes('Disk /var'));
    const warningIndex = messages.findIndex((text) => text.includes('Memory is'));
    expect(criticalIndex).toBeGreaterThanOrEqual(0);
    expect(criticalIndex).toBeLessThan(warningIndex);
  });

  it('shows why an unavailable widget is empty', async () => {
    mockDashboard(
      snapshot({
        cpu: { available: false, error: 'agent unreachable' },
      }),
    );
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });

    // A blank panel with no explanation would leave an operator guessing.
    expect(screen.getByText('agent unreachable')).toBeInTheDocument();
  });

  it('distinguishes an unsupported metric from a failure', async () => {
    mockDashboard(
      snapshot({
        disk: { available: false, unsupported: true, error: 'not available on this host' },
      }),
    );
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });

    const message = screen.getByText('not available on this host');
    expect(message).toBeInTheDocument();
    // "The host cannot do this" must not be styled as something to fix.
    expect(message).not.toHaveAttribute('role', 'status');
  });

  it('still renders the page when the agent is unreachable', async () => {
    mockDashboard(
      snapshot({
        system: { available: false, error: 'agent unreachable' },
        cpu: { available: false, error: 'agent unreachable' },
        memory: { available: false, error: 'agent unreachable' },
        disk: { available: false, error: 'agent unreachable' },
        network: { available: false, error: 'agent unreachable' },
        load: { available: false, error: 'agent unreachable' },
        services: { available: false, error: 'agent unreachable' },
        alerts: [
          {
            severity: 'critical',
            category: 'agent',
            message: 'The host agent is unreachable, so live metrics are unavailable',
          },
        ],
      }),
    );
    renderWithProviders(<DashboardPage />);

    // The dashboard is what an operator opens when things are already wrong.
    expect(await screen.findByRole('heading', { name: 'web01' })).toBeInTheDocument();
    expect(screen.getByText(/host agent is unreachable/)).toBeInTheDocument();
  });

  it('shows services with their state', async () => {
    mockDashboard(
      snapshot({
        services: {
          available: true,
          data: [
            { name: 'postgres', kind: 'dependency', running: true, status: 'running' },
            { name: 'nginx', kind: 'systemd', running: false, status: 'stopped' },
            { name: 'php-fpm', kind: 'systemd', running: false, status: 'not installed' },
          ],
        },
      }),
    );
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });

    const panel = screen.getByRole('region', { name: 'Services' });
    expect(within(panel).getByText('stopped')).toBeInTheDocument();
    // An absent optional unit is a neutral state, not a failure.
    expect(within(panel).getByText('not installed')).toBeInTheDocument();
    expect(within(panel).getByLabelText('Not installed')).toBeInTheDocument();
  });

  it('switches the history range', async () => {
    const user = userEvent.setup();
    mockDashboard(snapshot());
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });

    const button = screen.getByRole('button', { name: '24h' });
    expect(button).toHaveAttribute('aria-pressed', 'false');

    await user.click(button);

    expect(button).toHaveAttribute('aria-pressed', 'true');
    // The new range must actually be requested, not just highlighted.
    const requested = vi
      .mocked(globalThis.fetch)
      .mock.calls.some(([input]) => String(input).includes('range=24h'));
    expect(requested).toBe(true);
  });

  it('reports an empty history rather than an empty chart', async () => {
    mockDashboard(snapshot());
    renderWithProviders(<DashboardPage />);

    await screen.findByRole('heading', { name: 'web01' });
    expect(screen.getAllByText('No data yet for this range.').length).toBeGreaterThan(0);
  });

  it('surfaces a failed dashboard load', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('SERVICE_UNAVAILABLE', 'No server has been registered yet', 503),
    );
    renderWithProviders(<DashboardPage />);

    expect(await screen.findByRole('alert')).toHaveTextContent('No server has been registered yet');
  });
});
