import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { WebsiteFTPPanel } from '@/features/ftp/components/WebsiteFTPPanel';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { FTPUser } from '@/types/api';

function user(overrides: Partial<FTPUser> = {}): FTPUser {
  return {
    id: 'user-1',
    server_id: 'server-1',
    website_id: 'site-1',
    username: 'designer',
    home_subpath: '',
    access_level: 'full',
    quota_mb: 0,
    suspended: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    website_domain: 'example.com',
    system_user: 'web_example_com',
    document_root: '/var/www/example.com/public',
    home: '/var/www/example.com/public',
    used_mb: 0,
    locked: false,
    missing_on_host: false,
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

describe('WebsiteFTPPanel', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    users?: FTPUser[];
    available?: boolean;
    createdPassword?: string;
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
              error: { code: 'UNPROCESSABLE', message: options.refuse },
              request_id: 'req_test',
            }),
            { status: 422, headers: { 'Content-Type': 'application/json' } },
          );
        }
        return envelopeResponse({
          user: user(),
          ...(options.createdPassword ? { password: options.createdPassword } : {}),
        });
      }
      if (url.includes('/websites/')) {
        const list = options.users ?? [user()];
        return envelopeResponse({ users: list, count: list.length });
      }
      // The overview, which the panel reads to know whether FTP exists here.
      return envelopeResponse({
        available: options.available ?? true,
        running: true,
        can_install: false,
        version: '1.3.8d',
        reason: '',
        supports_tls: true,
        supports_quota: true,
        accounts: [],
        sessions: [],
        config_path: '',
        conflicts: null,
        firewall_open: true,
        firewall_reason: '',
        users: [],
        settings: {
          server_id: 'server-1',
          passive_from: 30000,
          passive_to: 30100,
          tls_website_id: '',
          require_tls: false,
          masquerade_address: '',
          max_clients: 0,
        },
      });
    });
  }

  it("lists the website's own accounts", async () => {
    mockApi({});
    renderWithProviders(<WebsiteFTPPanel websiteId="site-1" domain="example.com" />);

    expect(await screen.findByText('designer')).toBeInTheDocument();
    expect(screen.getByText('/var/www/example.com/public')).toBeInTheDocument();
  });

  it('says when there are none yet', async () => {
    mockApi({ users: [] });
    renderWithProviders(<WebsiteFTPPanel websiteId="site-1" domain="example.com" />);

    expect(await screen.findByText(/No FTP accounts for this website yet/)).toBeInTheDocument();
  });

  it('offers nothing to add where the host has no FTP server', async () => {
    mockApi({ available: false, users: [] });
    renderWithProviders(<WebsiteFTPPanel websiteId="site-1" domain="example.com" />);

    expect(await screen.findByText(/No FTP server is installed/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Add account/ })).not.toBeInTheDocument();
  });

  // Nothing stores the password, so the dialog has to stay open over the one
  // chance to read it. Closing on success would lose it silently.
  it('shows a generated password once and warns that it is not stored', async () => {
    mockApi({ createdPassword: 'Nx7fQ2mBvR4tYkHs9WdZ' });
    renderWithProviders(<WebsiteFTPPanel websiteId="site-1" domain="example.com" />);

    await userEvent.click(await screen.findByRole('button', { name: /Add account/ }));
    await userEvent.type(await screen.findByLabelText(/Account name/), 'designer');
    await userEvent.click(screen.getByRole('button', { name: 'Create account' }));

    expect(await screen.findByText('Nx7fQ2mBvR4tYkHs9WdZ')).toBeInTheDocument();
    expect(screen.getByText(/Nothing stores it/)).toBeInTheDocument();
  });

  it('sends the folder and the access level that were chosen', async () => {
    const bodies: string[] = [];
    mockApi({
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<WebsiteFTPPanel websiteId="site-1" domain="example.com" />);

    await userEvent.click(await screen.findByRole('button', { name: /Add account/ }));
    await userEvent.type(await screen.findByLabelText(/Account name/), 'designer');
    await userEvent.type(screen.getByLabelText(/Folder/), 'uploads');
    await userEvent.selectOptions(screen.getByLabelText(/Access/), 'readonly');
    await userEvent.click(screen.getByRole('button', { name: 'Create account' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(JSON.parse(bodies[0] as string)).toEqual({
      website_id: 'site-1',
      username: 'designer',
      home_subpath: 'uploads',
      access_level: 'readonly',
      quota_mb: 0,
    });
  });

  it('shows the reason a name was refused', async () => {
    mockApi({ refuse: 'this server already has an FTP account with that name' });
    renderWithProviders(<WebsiteFTPPanel websiteId="site-1" domain="example.com" />);

    await userEvent.click(await screen.findByRole('button', { name: /Add account/ }));
    await userEvent.type(await screen.findByLabelText(/Account name/), 'designer');
    await userEvent.click(screen.getByRole('button', { name: 'Create account' }));

    expect(await screen.findByText(/already has an FTP account with that name/)).toBeInTheDocument();
  });
});
