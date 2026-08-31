import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { FtpPage } from '@/pages/FtpPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { FTPOverview, FTPSession, FTPUser } from '@/types/api';

function user(overrides: Partial<FTPUser> = {}): FTPUser {
  return {
    id: 'user-1',
    server_id: 'server-1',
    website_id: 'site-1',
    username: 'designer',
    home_subpath: '',
    access_level: 'full',
    quota_mb: 100,
    suspended: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    website_domain: 'example.com',
    system_user: 'web_example_com',
    document_root: '/var/www/example.com/public',
    home: '/var/www/example.com/public',
    used_mb: 12.5,
    locked: false,
    missing_on_host: false,
    ...overrides,
  };
}

function session(overrides: Partial<FTPSession> = {}): FTPSession {
  return {
    pid: 4242,
    user: 'designer',
    elapsed: '0m12s',
    activity: 'STOR photo.jpg',
    client: 'desk.example.com [203.0.113.9]',
    protocol: 'ftps',
    location: '/uploads',
    ...overrides,
  };
}

function overview(overrides: Partial<FTPOverview> = {}): FTPOverview {
  return {
    available: true,
    running: true,
    can_install: false,
    version: '1.3.8d',
    reason: '',
    supports_tls: true,
    supports_quota: true,
    accounts: [],
    sessions: [],
    config_path: '/etc/proftpd/conf.d/10-jothost.conf',
    conflicts: null,
    firewall_open: true,
    firewall_reason: '',
    users: [user()],
    settings: {
      server_id: 'server-1',
      passive_from: 30000,
      passive_to: 30100,
      tls_website_id: '',
      require_tls: false,
      masquerade_address: '',
      max_clients: 0,
      tls_domain: '',
    },
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

describe('FtpPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: FTPOverview;
    sessions?: FTPSession[];
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'ftp.manage']);
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
        return envelopeResponse({ ok: true });
      }
      if (url.includes('/ftp/sessions')) {
        const list = options.sessions ?? [session()];
        return envelopeResponse({ sessions: list, count: list.length });
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('shows each account with the website and the folder it reaches', async () => {
    mockApi({});
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText('designer')).toBeInTheDocument();
    // The account's website, and the exact directory it is confined to.
    expect(screen.getByText(/example\.com ·/)).toBeInTheDocument();
    expect(screen.getByText('/var/www/example.com/public')).toBeInTheDocument();
  });

  // "What can this credential actually touch" is the question, and the system
  // account is the answer.
  it('says which system account uploads land as', async () => {
    mockApi({});
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText(/uploads as web_example_com/)).toBeInTheDocument();
  });

  it('shows what a limited account has used', async () => {
    mockApi({});
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText(/12\.5 MB of 100 MB used/)).toBeInTheDocument();
  });

  it('marks a read-only account as one', async () => {
    mockApi({ overview: overview({ users: [user({ access_level: 'readonly' })] }) });
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText('Read-only')).toBeInTheDocument();
  });

  it('asks before deleting an account', async () => {
    const calls: string[] = [];
    mockApi({ onWrite: (url) => calls.push(url) });
    renderWithProviders(<FtpPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Delete/ }));
    expect(await screen.findByText(/stop being able to connect/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    const dialog = await screen.findByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(calls.some((url) => url.includes('/ftp/users/'))).toBe(true));
  });

  // The one thing on the session list an operator cannot see anywhere else: an
  // unencrypted session is one whose password crossed the network in the clear.
  it('says whether a connected session is encrypted', async () => {
    mockApi({ sessions: [session(), session({ pid: 4243, user: 'other', protocol: 'ftp' })] });
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText('Encrypted')).toBeInTheDocument();
    expect(await screen.findByText('Not encrypted')).toBeInTheDocument();
  });

  it('asks before disconnecting somebody mid-transfer', async () => {
    const calls: string[] = [];
    mockApi({ onWrite: (url) => calls.push(url) });
    renderWithProviders(<FtpPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Disconnect' }));
    expect(await screen.findByText(/stop part-way/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    const dialog = await screen.findByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Disconnect' }));
    await waitFor(() =>
      expect(calls.some((url) => url.includes('/ftp/sessions/4242'))).toBe(true),
    );
  });

  it('says nobody can connect while the server is stopped', async () => {
    mockApi({ overview: overview({ running: false }) });
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText('The FTP server is not running')).toBeInTheDocument();
    expect(
      screen.getByText(/Nobody can be connected while the FTP server is stopped/),
    ).toBeInTheDocument();
  });

  it('offers to install an FTP server where there is none', async () => {
    const calls: string[] = [];
    mockApi({
      overview: overview({ available: false, running: false, can_install: true, users: [] }),
      onWrite: (url) => calls.push(url),
    });
    renderWithProviders(<FtpPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Install an FTP server/ }));
    await waitFor(() => expect(calls.some((url) => url.endsWith('/ftp/install'))).toBe(true));
  });

  // A build with no TLS module cannot offer encryption, and the control says so
  // rather than accepting a setting the server would skip in silence.
  it('will not let encryption be required where the server cannot do it', async () => {
    mockApi({ overview: overview({ supports_tls: false }) });
    renderWithProviders(<FtpPage />);

    expect(await screen.findByLabelText(/Require encryption/)).toBeDisabled();
    expect(screen.getByText(/no TLS module/)).toBeInTheDocument();
  });

  // The failure this warns about is silent: the panel writes a correct file and
  // another file's value is the one in force.
  it('warns when another configuration file overrides the panel', async () => {
    mockApi({
      overview: overview({
        conflicts: [{ directive: 'PassivePorts', file: '05-operator.conf', panel_wins: false }],
      }),
    });
    renderWithProviders(<FtpPage />);

    expect(
      await screen.findByText(/Another configuration file overrides the panel/),
    ).toBeInTheDocument();
    expect(screen.getByText(/05-operator\.conf/)).toBeInTheDocument();
  });

  it('does not warn about a file the panel takes precedence over', async () => {
    mockApi({
      overview: overview({
        conflicts: [{ directive: 'PassivePorts', file: '99-other.conf', panel_wins: true }],
      }),
    });
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText('designer')).toBeInTheDocument();
    expect(
      screen.queryByText(/Another configuration file overrides the panel/),
    ).not.toBeInTheDocument();
  });

  // The login succeeds and the first listing hangs, which looks like anything
  // except a closed port — so the page has to say it.
  it('warns when the firewall is blocking the passive range', async () => {
    mockApi({
      overview: overview({
        firewall_open: false,
        firewall_reason:
          'the firewall is on and does not allow ports 30000-30100, so logins will succeed and every transfer will hang',
      }),
    });
    renderWithProviders(<FtpPage />);

    expect(await screen.findByText('The firewall is blocking FTP')).toBeInTheDocument();
    expect(screen.getByText(/every transfer will hang/)).toBeInTheDocument();
  });

  it('shows the reason a change was refused', async () => {
    mockApi({ refuse: 'at least 16 ports — each transfer in progress uses one' });
    renderWithProviders(<FtpPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/each transfer in progress uses one/)).toBeInTheDocument();
  });
});
