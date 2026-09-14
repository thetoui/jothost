import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { SecurityCenterPage } from '@/pages/SecurityCenterPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type {
  ScannerOutcome,
  SecurityFinding,
  SecurityOverview,
  SecurityScan,
} from '@/types/api';

function finding(overrides: Partial<SecurityFinding> = {}): SecurityFinding {
  return {
    id: 'finding-1',
    server_id: 'server-1',
    scanner: 'ports',
    severity: 'critical',
    category: 'exposed-service',
    title: 'Port 3306 is reachable from the network (MySQL or MariaDB)',
    description: 'mariadbd is listening on 0.0.0.0.',
    remediation: 'Bind the service to 127.0.0.1, or block the port on the Firewall page.',
    fingerprint: 'ports:public:3306',
    status: 'open',
    first_seen_at: '2026-08-01T09:00:00Z',
    last_seen_at: '2026-09-04T09:00:00Z',
    resolved_at: null,
    accepted_at: null,
    accepted_by: null,
    accepted_reason: null,
    accepted_severity: null,
    created_at: '2026-08-01T09:00:00Z',
    ...overrides,
  };
}

function outcome(overrides: Partial<ScannerOutcome> = {}): ScannerOutcome {
  return { scanner: 'ssh', ran: true, findings: 0, duration_ms: 12, ...overrides };
}

function scan(overrides: Partial<SecurityScan> = {}): SecurityScan {
  return {
    id: 'scan-1',
    server_id: 'server-1',
    score: 100,
    checks_run: 7,
    checks_total: 7,
    critical: 0,
    high: 0,
    medium: 0,
    low: 0,
    info: 0,
    accepted: 0,
    resolved: 0,
    scanners: [outcome()],
    duration_ms: 900,
    triggered_by: null,
    created_at: '2026-09-04T09:00:00Z',
    ...overrides,
  };
}

function overview(overrides: Partial<SecurityOverview> = {}): SecurityOverview {
  return {
    score: {
      value: 100,
      grade: 'good',
      checks_run: 7,
      checks_total: 7,
      complete: true,
      summary: 'Every check ran and found nothing.',
    },
    counts: {
      critical: 0,
      high: 0,
      medium: 0,
      low: 0,
      info: 0,
      open: 0,
      accepted: 0,
    },
    findings: [],
    accepted: [],
    last_scan: scan(),
    scanners: [outcome()],
    severities: ['critical', 'high', 'medium', 'low', 'info'],
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
    recovery_codes_remaining: 0,
    roles: ['admin'],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('SecurityCenterPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: SecurityOverview;
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'security.view']);
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
        return envelopeResponse(finding({ status: 'accepted' }));
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('says a host has not been scanned rather than showing it a score', async () => {
    // A host nobody has looked at is not a host with no problems, and a number
    // here would make one of those up.
    mockApi({
      overview: overview({
        last_scan: null,
        scanners: [],
        score: {
          value: 0,
          grade: 'unknown',
          checks_run: 0,
          checks_total: 0,
          complete: false,
          summary: 'This host has not been scanned yet.',
        },
      }),
    });
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText('Nothing has been checked')).toBeInTheDocument();
    expect(
      screen.getByText(/not a host with no problems/),
    ).toBeInTheDocument();
  });

  it('never shows the score without the checks it is built from', async () => {
    // A 100 from two checks is not a 100.
    mockApi({});
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText('100')).toBeInTheDocument();
    expect(screen.getByText('from 7 of 7 checks')).toBeInTheDocument();
  });

  it('says outright when a score is incomplete rather than reassuring', async () => {
    // The failure this phase is most able to commit: a score computed from
    // blanks looks exactly like a good one.
    mockApi({
      overview: overview({
        score: {
          value: 100,
          grade: 'good',
          checks_run: 5,
          checks_total: 7,
          complete: false,
          summary:
            'Built from 5 of 7 checks — firewall and ssl could not be run, so this ' +
            'score is incomplete rather than reassuring.',
        },
      }),
    });
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText('This score is incomplete')).toBeInTheDocument();
    expect(screen.getByText(/incomplete rather than reassuring/)).toBeInTheDocument();
    expect(screen.getByText('from 5 of 7 checks')).toBeInTheDocument();
  });

  it('names the checks that could not run, and why', async () => {
    mockApi({
      overview: overview({
        scanners: [
          outcome({ scanner: 'ssh', ran: true, findings: 2 }),
          outcome({
            scanner: 'firewall',
            ran: false,
            reason: 'the firewall could not be read: ufw is not installed',
          }),
        ],
      }),
    });
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText('firewall')).toBeInTheDocument();
    expect(screen.getByText(/could not be run — .*ufw is not installed/)).toBeInTheDocument();
    expect(screen.getByText('2 finding(s)')).toBeInTheDocument();
  });

  it('shows a finding with what to do about it', async () => {
    // A finding without a next step is a nag, and a page full of nags is one
    // nobody opens twice.
    mockApi({
      overview: overview({
        findings: [finding()],
        counts: {
          critical: 1,
          high: 0,
          medium: 0,
          low: 0,
          info: 0,
          open: 1,
          accepted: 0,
        },
      }),
    });
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText(/Port 3306 is reachable/)).toBeInTheDocument();
    expect(screen.getByText(/Bind the service to 127.0.0.1/)).toBeInTheDocument();
    expect(screen.getByText(/Found by the ports check/)).toBeInTheDocument();
  });

  it('offers no way to mark a finding fixed, and says why', async () => {
    // A panel where a person can mark an open port as closed is one that will
    // one day say an open port is closed.
    mockApi({ overview: overview({ findings: [finding()] }) });
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByRole('button', { name: /Accept/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Resolve/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Mark as fixed/ })).not.toBeInTheDocument();
    expect(
      screen.getByText(/There is no way to mark a finding as fixed/),
    ).toBeInTheDocument();
  });

  it('asks why before accepting a risk', async () => {
    // A mute button on a security page is how a real problem becomes permanent.
    mockApi({ overview: overview({ findings: [finding()] }) });
    renderWithProviders(<SecurityCenterPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Accept/ }));

    expect(await screen.findByLabelText(/Why is this acceptable/)).toBeInTheDocument();
    expect(screen.getByText(/read this in a year/)).toBeInTheDocument();
  });

  it('promises an accepted risk comes back if it gets worse', async () => {
    // The property that makes accepting safe to offer at all, said where
    // somebody is deciding whether to use it.
    mockApi({ overview: overview({ findings: [finding()] }) });
    renderWithProviders(<SecurityCenterPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Accept/ }));

    expect(
      await screen.findByText(/comes back on its own if the scan ever finds it worse/),
    ).toBeInTheDocument();
  });

  it('sends the reason with the acceptance', async () => {
    const bodies: Array<Record<string, unknown>> = [];
    mockApi({
      overview: overview({ findings: [finding()] }),
      onWrite: (_url, init) => bodies.push(JSON.parse(String(init?.body))),
    });
    renderWithProviders(<SecurityCenterPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Accept/ }));
    await userEvent.type(
      await screen.findByLabelText(/Why is this acceptable/),
      'behind a hardware firewall',
    );
    await userEvent.click(screen.getByRole('button', { name: 'Accept the risk' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      status: 'accepted',
      reason: 'behind a hardware firewall',
    });
  });

  it('shows the reason the panel refused an acceptance', async () => {
    mockApi({
      overview: overview({ findings: [finding()] }),
      refuse: 'say why this risk is accepted, in at least 8 characters',
    });
    renderWithProviders(<SecurityCenterPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Accept/ }));
    await userEvent.click(screen.getByRole('button', { name: 'Accept the risk' }));

    expect(await screen.findByText(/at least 8 characters/)).toBeInTheDocument();
  });

  it('keeps accepted risks visible with the reason they were accepted', async () => {
    // An accepted risk that scrolled out of sight would be one nobody reviews
    // again.
    mockApi({
      overview: overview({
        accepted: [
          finding({
            id: 'accepted-1',
            status: 'accepted',
            accepted_at: '2026-09-01T09:00:00Z',
            accepted_reason: 'this host is behind a hardware firewall',
            accepted_severity: 'critical',
          }),
        ],
        counts: {
          critical: 0,
          high: 0,
          medium: 0,
          low: 0,
          info: 0,
          open: 0,
          accepted: 1,
        },
      }),
    });
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText('Accepted risks')).toBeInTheDocument();
    expect(screen.getByText(/behind a hardware firewall/)).toBeInTheDocument();
    expect(
      screen.getByText(/A score carried by accepted risk is a different thing/),
    ).toBeInTheDocument();
  });

  it('can withdraw an acceptance', async () => {
    const bodies: Array<Record<string, unknown>> = [];
    mockApi({
      overview: overview({
        accepted: [
          finding({ status: 'accepted', accepted_reason: 'temporary while we migrate' }),
        ],
      }),
      onWrite: (_url, init) => bodies.push(JSON.parse(String(init?.body))),
    });
    renderWithProviders(<SecurityCenterPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Reopen/ }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ status: 'open' });
  });

  it('runs a scan when asked', async () => {
    const writes: string[] = [];
    mockApi({ onWrite: (url) => writes.push(url) });
    renderWithProviders(<SecurityCenterPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Run a scan/ }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toContain('/security/scan');
  });

  it('says nothing is outstanding only about the checks that ran', async () => {
    mockApi({});
    renderWithProviders(<SecurityCenterPage />);

    expect(await screen.findByText('Nothing outstanding')).toBeInTheDocument();
    expect(
      screen.getByText(/Every check that ran found nothing to report/),
    ).toBeInTheDocument();
  });

  it('hides the accepted section when there is nothing accepted', async () => {
    mockApi({});
    renderWithProviders(<SecurityCenterPage />);

    await screen.findByText('Nothing outstanding');
    expect(screen.queryByText('Accepted risks')).not.toBeInTheDocument();
  });
});
