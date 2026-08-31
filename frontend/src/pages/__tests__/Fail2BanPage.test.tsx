import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { Fail2BanPage } from '@/pages/Fail2BanPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Fail2BanBanned, Fail2BanJail, Fail2BanStatus } from '@/types/api';

function jail(overrides: Partial<Fail2BanJail> = {}): Fail2BanJail {
  return {
    name: 'sshd',
    label: 'SSH',
    summary: 'Bans hosts that fail to authenticate over SSH.',
    managed: true,
    enabled: true,
    max_retry: 5,
    find_time: 600,
    ban_time: 3600,
    currently_banned: 1,
    total_banned: 4,
    currently_failed: 2,
    total_failed: 19,
    log_paths: ['/var/log/auth.log'],
    banned: ['198.51.100.7'],
    available: true,
    reason: '',
    ...overrides,
  };
}

function status(overrides: Partial<Fail2BanStatus> = {}): Fail2BanStatus {
  return {
    available: true,
    running: true,
    can_install: false,
    version: '1.1.0',
    reason: '',
    jails: [jail()],
    ignored: ['127.0.0.1/8', '::1'],
    drop_in_path: '/etc/fail2ban/jail.d/99-jothost.local',
    banned: 1,
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

describe('Fail2BanPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    status?: Fail2BanStatus;
    banned?: Fail2BanBanned[];
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'firewall.manage']);
      }
      if (init?.method && init.method !== 'GET') {
        options.onWrite?.(url, init);
        if (options.refuse) {
          return new Response(
            JSON.stringify({
              success: false,
              error: { code: 'CONFLICT', message: options.refuse },
              request_id: 'req_test',
            }),
            { status: 409, headers: { 'Content-Type': 'application/json' } },
          );
        }
        return envelopeResponse({ jail: 'sshd', enabled: true, policy: {}, backup: '', reloaded: true });
      }
      if (url.includes('/fail2ban/banned')) {
        const list = options.banned ?? [{ address: '198.51.100.7', jail: 'sshd' }];
        return envelopeResponse({ banned: list, count: list.length });
      }
      return envelopeResponse(options.status ?? status());
    });
  }

  it('shows each jail with what it watches and how strict it is', async () => {
    mockApi({});
    renderWithProviders(<Fail2BanPage />);

    expect(await screen.findByText('SSH')).toBeInTheDocument();
    expect(screen.getByText(/\/var\/log\/auth\.log/)).toBeInTheDocument();
    expect(screen.getByText('Watching')).toBeInTheDocument();
    expect(await screen.findByLabelText('Failures allowed')).toHaveValue('5');
  });

  it('lists what is banned now, with the jail that banned it', async () => {
    mockApi({});
    renderWithProviders(<Fail2BanPage />);

    expect(await screen.findByText('198.51.100.7')).toBeInTheDocument();
    expect(screen.getByText(/banned by sshd/)).toBeInTheDocument();
  });

  it('asks before unbanning', async () => {
    const calls: string[] = [];
    mockApi({ onWrite: (url) => calls.push(url) });
    renderWithProviders(<Fail2BanPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Unban/ }));
    expect(await screen.findByText(/reach this host again/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    // Within the dialog: the row's button says "Unban" too, and clicking that
    // one again would prove nothing.
    const dialog = await screen.findByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Unban' }));
    await waitFor(() => expect(calls.some((url) => url.endsWith('/unban'))).toBe(true));
  });

  it('sends only the setting that changed', async () => {
    const bodies: string[] = [];
    mockApi({
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<Fail2BanPage />);

    await userEvent.selectOptions(await screen.findByLabelText('Failures allowed'), '10');

    await waitFor(() => expect(bodies).toHaveLength(1));
    // Not the whole policy: a body carrying the others would rewrite settings
    // the operator did not touch.
    expect(JSON.parse(bodies[0] as string)).toEqual({ max_retry: 10 });
  });

  // A jail somebody wrote by hand is banning people whether the panel knows how
  // it was configured or not. It is shown, and not touched.
  it('shows a jail configured outside the panel without offering to edit it', async () => {
    mockApi({
      status: status({
        jails: [jail({ name: 'postfix', label: '', summary: '', managed: false })],
      }),
    });
    renderWithProviders(<Fail2BanPage />);

    expect(await screen.findByText(/Configured outside the panel/)).toBeInTheDocument();
    expect(await screen.findByLabelText('On')).toBeDisabled();
    expect(screen.queryByLabelText('Failures allowed')).not.toBeInTheDocument();
  });

  // "There is no jail for that" and "this host cannot run it" are different
  // answers, and the second one has a reason worth reading.
  it('says why a jail cannot run here', async () => {
    mockApi({
      status: status({
        jails: [
          jail({
            enabled: false,
            available: false,
            reason: 'none of the logs this jail watches exist on this host yet',
          }),
        ],
      }),
    });
    renderWithProviders(<Fail2BanPage />);

    expect(await screen.findByText(/none of the logs this jail watches/)).toBeInTheDocument();
    expect(await screen.findByLabelText('On')).toBeDisabled();
  });

  it('offers to install fail2ban where it is missing', async () => {
    const calls: string[] = [];
    mockApi({
      status: status({ available: false, running: false, can_install: true, jails: [] }),
      onWrite: (url) => calls.push(url),
    });
    renderWithProviders(<Fail2BanPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Install fail2ban/ }));
    await waitFor(() => expect(calls.some((url) => url.endsWith('/install'))).toBe(true));
  });

  it('says nothing is being banned while the daemon is stopped', async () => {
    mockApi({ status: status({ running: false }) });
    renderWithProviders(<Fail2BanPage />);

    expect(await screen.findByText('fail2ban is not running')).toBeInTheDocument();
    expect(screen.getByText(/Nothing is banned while fail2ban is stopped/)).toBeInTheDocument();
  });

  // An address that is never banned is a hole somebody opened deliberately, and
  // the field says why it exists.
  it('shows the ignore list and saves changes to it', async () => {
    const bodies: string[] = [];
    mockApi({
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<Fail2BanPage />);

    const field = await screen.findByLabelText('Addresses');
    expect(field).toHaveValue('127.0.0.1/8, ::1');
    expect(screen.getByText(/Your own address belongs here/)).toBeInTheDocument();

    await userEvent.clear(field);
    await userEvent.type(field, '203.0.113.4');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(JSON.parse(bodies[0] as string)).toEqual({ ignored: ['203.0.113.4'] });
  });

  it('shows the reason a change was refused', async () => {
    mockApi({ refuse: 'it is running 10 failures in 600s, not 5 — something else configures this jail' });
    renderWithProviders(<Fail2BanPage />);

    await userEvent.selectOptions(await screen.findByLabelText('Failures allowed'), '10');
    expect(await screen.findByText(/something else configures this jail/)).toBeInTheDocument();
  });
});
