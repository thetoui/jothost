import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { MonitoringPage } from '@/pages/MonitoringPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Alert, AlertRule, MonitoringOverview } from '@/types/api';

function alert(overrides: Partial<Alert> = {}): Alert {
  return {
    id: 'alert-1',
    server_id: 'server-1',
    metric: 'disk',
    target: '/var',
    severity: 'critical',
    threshold: 90,
    status: 'open',
    message: 'Disk nearly full (/var): 96% is above 90% for 2 hours',
    value: 96,
    worst: 97,
    opened_at: '2026-09-03T07:00:00Z',
    last_seen_at: '2026-09-03T09:00:00Z',
    ...overrides,
  };
}

function rule(overrides: Partial<AlertRule> = {}): AlertRule {
  return {
    id: 'rule-1',
    server_id: 'server-1',
    name: 'Disk nearly full',
    metric: 'disk',
    target: '',
    comparison: 'above',
    threshold: 90,
    for_seconds: 300,
    severity: 'critical',
    enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function overview(overrides: Partial<MonitoringOverview> = {}): MonitoringOverview {
  return {
    open: [],
    recent: [],
    rules: [rule()],
    services: [
      { service: 'nginx', running: true, status: 'running', since: '2026-09-01T00:00:00Z', for_seconds: 7200 },
    ],
    counts: { critical: 0, warning: 0, unacknowledged: 0 },
    ...overrides,
  };
}

function mockProfile(permissions: string[]) {
  return envelopeResponse({
    id: 'user-1',
    username: 'admin',
    email: null,
    status: 'active',
    two_factor_enabled: false,
    roles: ['admin'],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('MonitoringPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: MonitoringOverview;
    rules?: AlertRule[];
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'monitor.manage']);
      }
      if (init?.method && init.method !== 'GET') {
        options.onWrite?.(url, init);
        if (options.refuse) {
          return new Response(
            JSON.stringify({
              success: false,
              error: { code: 'VALIDATION_FAILED', message: options.refuse },
              request_id: 'req_test',
            }),
            { status: 422, headers: { 'Content-Type': 'application/json' } },
          );
        }
        return envelopeResponse(alert({ acknowledged_at: '2026-09-03T09:05:00Z' }));
      }
      if (url.includes('/monitoring/rules')) {
        const rules = options.rules ?? options.overview?.rules ?? [rule()];
        return envelopeResponse({
          rules,
          count: rules.length,
          metrics: ['cpu', 'memory', 'disk', 'swap', 'load', 'service'],
        });
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('says nothing is wrong only after having looked', async () => {
    mockApi({});
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByText('Nothing is wrong')).toBeInTheDocument();
    expect(screen.getByText(/not the same as not having looked/)).toBeInTheDocument();
  });

  it('shows an open alert with what opened it and how long it has been open', async () => {
    mockApi({
      overview: overview({
        open: [alert()],
        counts: { critical: 1, warning: 0, unacknowledged: 1 },
      }),
    });
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByText(/Disk nearly full \(\/var\)/)).toBeInTheDocument();
    expect(screen.getByText('1 open alert')).toBeInTheDocument();
    expect(screen.getByText(/worst 97/)).toBeInTheDocument();
  });

  it('offers no way to resolve an alert by hand, and says so', async () => {
    // A panel where a person can mark a full disk as fine is a panel that will
    // one day say a full disk is fine.
    mockApi({
      overview: overview({
        open: [alert()],
        counts: { critical: 1, warning: 0, unacknowledged: 1 },
      }),
    });
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByRole('button', { name: /Acknowledge/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Resolve/ })).not.toBeInTheDocument();
    expect(screen.getByText(/no way to mark one as fine by hand/)).toBeInTheDocument();
  });

  it('acknowledges an alert without resolving it', async () => {
    const writes: string[] = [];
    mockApi({
      overview: overview({
        open: [alert()],
        counts: { critical: 1, warning: 0, unacknowledged: 1 },
      }),
      onWrite: (url) => writes.push(url),
    });
    renderWithProviders(<MonitoringPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Acknowledge/ }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toContain('/acknowledge');
  });

  it('hides the acknowledge button on an alert somebody has already seen', async () => {
    mockApi({
      overview: overview({
        open: [alert({ acknowledged_at: '2026-09-03T08:00:00Z' })],
        counts: { critical: 1, warning: 0, unacknowledged: 0 },
      }),
    });
    renderWithProviders(<MonitoringPage />);

    // The word appears in the card's summary too, so the alert's own line is
    // the one matched.
    expect(await screen.findByText(/· acknowledged/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Acknowledge/ })).not.toBeInTheDocument();
  });

  it('describes a rule as a sentence, including how long a breach must last', async () => {
    // The duration is the field that decides whether the panel is usable, so it
    // is on the face of the rule rather than hidden in an editor.
    mockApi({ rules: [rule({ target: '/var', for_seconds: 600 })] });
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByText(/disk on \/var above 90% for 10 min/)).toBeInTheDocument();
  });

  it('says when a rule fires on the first reading', async () => {
    mockApi({
      rules: [
        rule({
          name: 'nginx down',
          metric: 'service',
          target: 'nginx',
          comparison: 'below',
          threshold: 1,
          for_seconds: 0,
        }),
      ],
    });
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByText(/on the first reading/)).toBeInTheDocument();
  });

  it('shows how long each service has been in its state', async () => {
    mockApi({});
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByText('nginx')).toBeInTheDocument();
    expect(screen.getByText(/running for 2 hours/)).toBeInTheDocument();
  });

  it('creates a rule and sends the duration that was set', async () => {
    const writes: Array<{ url: string; body: Record<string, unknown> }> = [];
    mockApi({ onWrite: (url, init) => writes.push({ url, body: JSON.parse(String(init?.body)) }) });
    renderWithProviders(<MonitoringPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add rule/ }));
    await userEvent.type(await screen.findByLabelText('Name'), 'Memory pressure');
    await userEvent.selectOptions(screen.getByLabelText('Watch'), 'memory');
    await userEvent.clear(screen.getByLabelText(/For at least/));
    await userEvent.type(screen.getByLabelText(/For at least/), '900');
    await userEvent.click(screen.getByRole('button', { name: 'Save rule' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.url).toContain('/monitoring/rules');
    expect(writes[0]?.body).toMatchObject({
      name: 'Memory pressure',
      metric: 'memory',
      for_seconds: 900,
    });
  });

  it('will not let an existing rule be pointed at something else', async () => {
    // A rule that watched something else would be a different rule, and the
    // alerts it had already opened would describe a condition it never saw.
    mockApi({});
    renderWithProviders(<MonitoringPage />);

    await userEvent.click(await screen.findByText('Disk nearly full'));
    expect(await screen.findByLabelText('Watch')).toBeDisabled();
    expect(screen.getByLabelText('Which one')).toBeDisabled();
    expect(screen.getByText(/cannot be pointed at something else/)).toBeInTheDocument();
  });

  it('does not send the metric when editing a rule', async () => {
    const writes: Array<{ body: Record<string, unknown> }> = [];
    mockApi({ onWrite: (_url, init) => writes.push({ body: JSON.parse(String(init?.body)) }) });
    renderWithProviders(<MonitoringPage />);

    await userEvent.click(await screen.findByText('Disk nearly full'));
    await userEvent.click(await screen.findByRole('button', { name: 'Save rule' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.body).not.toHaveProperty('metric');
    expect(writes[0]?.body).not.toHaveProperty('target');
  });

  it('shows the reason a rule was refused', async () => {
    mockApi({ refuse: 'memory is a percentage, so a threshold above 100 could never be reached' });
    renderWithProviders(<MonitoringPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add rule/ }));
    await userEvent.type(await screen.findByLabelText('Name'), 'Impossible');
    await userEvent.click(screen.getByRole('button', { name: 'Save rule' }));

    expect(await screen.findByText(/could never be reached/)).toBeInTheDocument();
  });

  it('confirms before a rule stops watching, and says the history is kept', async () => {
    const writes: string[] = [];
    mockApi({ onWrite: (url) => writes.push(url) });
    renderWithProviders(<MonitoringPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Delete Disk nearly full' }));
    expect(await screen.findByText(/Stop watching with "Disk nearly full"\?/)).toBeInTheDocument();
    expect(screen.getByText(/does not erase what it caught/)).toBeInTheDocument();
    expect(writes).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Delete rule' }));
    await waitFor(() => expect(writes).toHaveLength(1));
  });

  it('shows resolved alerts with how long they lasted', async () => {
    mockApi({
      overview: overview({
        recent: [
          alert({
            id: 'alert-2',
            status: 'resolved',
            opened_at: '2026-09-03T07:00:00Z',
            resolved_at: '2026-09-03T08:00:00Z',
          }),
        ],
      }),
    });
    renderWithProviders(<MonitoringPage />);

    expect(await screen.findByText(/lasted 1 hour/)).toBeInTheDocument();
  });
});
