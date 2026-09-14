import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { FirewallPage } from '@/pages/FirewallPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import type { FirewallRule, FirewallStatus } from '@/types/api';

function rule(overrides: Partial<FirewallRule> = {}): FirewallRule {
  return {
    action: 'allow',
    direction: 'in',
    protocol: 'tcp',
    port: '22',
    source: 'any',
    ...overrides,
  };
}

function status(overrides: Partial<FirewallStatus> = {}): FirewallStatus {
  return {
    available: true,
    enabled: true,
    default_incoming: 'deny',
    default_outgoing: 'allow',
    rules: [rule()],
    guarded_ports: [22, 80, 443],
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

describe('FirewallPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('shows the rules and which ports can never be closed', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      return envelopeResponse(status());
    });

    renderWithProviders(<FirewallPage />);

    expect(await screen.findByText('The firewall is on')).toBeInTheDocument();
    expect(screen.getByText('22/tcp')).toBeInTheDocument();
    expect(screen.getByText(/Ports 22, 80, 443 can never be closed/)).toBeInTheDocument();
  });

  // The client's half of CLAUDE.md section 19: apply, prove the panel is still
  // reachable by making a request that has to cross the network the change just
  // altered, and only then confirm.
  it('confirms a change only after re-reading the firewall', async () => {
    const calls: string[] = [];
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      calls.push(`${init?.method ?? 'GET'} ${url.replace(/^.*\/api\/v1/, '')}`);
      if (url.includes('/firewall/rules')) {
        return envelopeResponse(
          { id: 'fwc_1', kind: 'rule.add', rule: rule({ port: '8080' }), deadline: '2026-01-01T00:01:00Z' },
          202,
        );
      }
      if (url.includes('/confirm')) {
        return envelopeResponse({ id: 'fwc_1', kind: 'rule.add', rule: rule(), deadline: '' });
      }
      return envelopeResponse(status());
    });

    renderWithProviders(<FirewallPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'New rule' }));
    await userEvent.type(screen.getByLabelText('Port'), '8080');
    await userEvent.click(screen.getByRole('button', { name: 'Add rule' }));

    await waitFor(() => expect(calls.some((call) => call.includes('/confirm'))).toBe(true));

    const applied = calls.findIndex((call) => call.startsWith('POST /firewall/rules'));
    const verified = calls.findIndex((call, index) => index > applied && call === 'GET /firewall');
    const confirmed = calls.findIndex((call) => call.includes('/confirm'));

    expect(applied).toBeGreaterThanOrEqual(0);
    // The order is the safety property: the firewall is re-read between
    // applying and confirming, and that read is what proves the panel survived.
    expect(verified).toBeGreaterThan(applied);
    expect(confirmed).toBeGreaterThan(verified);
  });

  // If the change cuts the panel off, the verification never comes back — and
  // the page says what is about to happen rather than showing a bare failure.
  it('says the change is being undone when the panel cannot be reached', async () => {
    let applied = false;
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (url.includes('/firewall/rules') && init?.method === 'POST') {
        applied = true;
        return envelopeResponse(
          { id: 'fwc_1', kind: 'rule.add', rule: rule(), deadline: '2026-01-01T00:01:00Z' },
          202,
        );
      }
      if (applied) {
        // The network is gone: this is what a browser sees when the rule it
        // just applied closed the port the panel is served on.
        throw new TypeError('Failed to fetch');
      }
      return envelopeResponse(status());
    });

    renderWithProviders(<FirewallPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'New rule' }));
    await userEvent.type(screen.getByLabelText('Port'), '8080');
    await userEvent.click(screen.getByRole('button', { name: 'Add rule' }));

    expect(
      await screen.findByText('The panel could not be reached after the change'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/the rules this host had before will be back/i),
    ).toBeInTheDocument();
  });

  it('shows a change that is still waiting, with a way to undo it', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      return envelopeResponse(
        status({
          pending: {
            id: 'fwc_9',
            kind: 'rule.add',
            rule: rule({ port: '8080' }),
            deadline: '2026-01-01T00:01:00Z',
          },
        }),
      );
    });

    renderWithProviders(<FirewallPage />);

    expect(await screen.findByText('A change is waiting to be confirmed')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Undo it now' })).toBeInTheDocument();
    // Nothing else may be changed while one is in flight.
    expect(screen.getByRole('button', { name: 'New rule' })).toBeDisabled();
  });

  // The refusal the guard produces is the most useful message on this page: it
  // names the port and says what to do about it.
  it('shows why a change that would lock the host out was refused', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (url.includes('/firewall/rules') && init?.method === 'POST') {
        return errorResponse(
          'CONFLICT',
          'that change would lock this host out: this rule would deny port 22, ' +
            'which is how this host is administered',
          409,
        );
      }
      return envelopeResponse(status());
    });

    renderWithProviders(<FirewallPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'New rule' }));
    await userEvent.type(screen.getByLabelText('Port'), '22');
    await userEvent.click(screen.getByRole('button', { name: 'Add rule' }));

    expect(await screen.findByText(/would deny port 22/)).toBeInTheDocument();
  });

  it('says plainly when the host has no firewall', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view']);
      }
      return envelopeResponse({
        available: false,
        enabled: false,
        default_incoming: '',
        default_outgoing: '',
        rules: [],
        guarded_ports: [],
        reason: 'ufw is not installed',
      });
    });

    renderWithProviders(<FirewallPage />);

    expect(
      await screen.findByText('This host has no firewall the panel can manage'),
    ).toBeInTheDocument();
    expect(screen.getByText('ufw is not installed')).toBeInTheDocument();
  });
});
