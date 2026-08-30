import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { NodePage } from '@/pages/NodePage';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { NodeApp } from '@/types/api';

function app(overrides: Partial<NodeApp> = {}): NodeApp {
  return {
    id: '11111111-2222-3333-4444-555555555555',
    server_id: 'server-1',
    website_id: 'site-1',
    name: 'shop-app',
    node_version: '22',
    application_root: '/var/www/shop.example/public',
    startup_file: 'server.js',
    port: 3000,
    status: 'running',
    systemd_service: 'jothost-node-shop-app.service',
    autostart: true,
    last_error: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    website_domain: 'shop.example',
    system_user: 'web_shop',
    runtime: {
      state: 'running',
      pid: 4242,
      port: 3000,
      uptime_seconds: 120,
      managed_by: 'agent',
      listening: true,
      detail: '',
    },
    environment: ['DATABASE_URL'],
    ...overrides,
  };
}

const versions = {
  versions: [
    { version: '22', full_version: '22.11.0', binary_path: '/usr/bin/node', npm_version: '10.9.1' },
  ],
  count: 1,
  available: true,
  offers: [{ version: '', package: 'nodejs', label: 'Long-term support' }],
  can_install: true,
  package_manager: 'apk',
  managed_by: 'agent',
};

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

function mockApi(
  overrides: { apps?: NodeApp[]; versionsData?: unknown; permissions?: string[] } = {},
) {
  const apps = overrides.apps ?? [app()];

  return vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
    const url = String(input);
    if (url.includes('/auth/me')) {
      return mockProfile(overrides.permissions ?? ['website.view', 'website.update', 'server.manage']);
    }
    if (url.includes('/node/versions')) {
      return envelopeResponse(overrides.versionsData ?? versions);
    }
    if (url.includes('/logs')) {
      return envelopeResponse({
        source: 'files',
        lines: ['listening on 3000'],
        error_lines: [],
        out_path: '/var/log/jothost/node/shop-app/out.log',
        error_path: '/var/log/jothost/node/shop-app/error.log',
        detail: '',
      });
    }
    if (/\/node\/apps\/[^/]+$/.test(url)) {
      return envelopeResponse({ application: apps[0] });
    }
    if (url.includes('/websites')) {
      return envelopeResponse({ websites: [], count: 0 });
    }
    return envelopeResponse({ applications: apps, count: apps.length });
  });
}

describe('NodePage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('lists the applications the server runs', async () => {
    mockApi();
    renderWithProviders(<NodePage />);

    expect(await screen.findByText('shop-app')).toBeInTheDocument();
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getByText('3000')).toBeInTheDocument();
  });

  it('names the runtime and how applications are run', async () => {
    mockApi();
    renderWithProviders(<NodePage />);

    // Which mechanism runs them decides where the logs are, so the reader is
    // told rather than left to find out from an empty page.
    expect(await screen.findByText(/Node 22\.11\.0/)).toBeInTheDocument();
    expect(screen.getByText(/Run by the panel’s agent/)).toBeInTheDocument();
  });

  it('says so when no runtime is installed', async () => {
    mockApi({
      apps: [],
      versionsData: { ...versions, versions: [], count: 0, available: false },
    });
    renderWithProviders(<NodePage />);

    expect(await screen.findByText('No Node.js runtime is installed')).toBeInTheDocument();
    // Offering a button that can only fail is worse than disabling it.
    expect(screen.getAllByRole('button', { name: /Add Application/ })[0]).toBeDisabled();
  });

  it('offers stop and restart for a running application, and start for a stopped one', async () => {
    mockApi();
    renderWithProviders(<NodePage />);

    await screen.findByText('shop-app');
    expect(screen.getByRole('button', { name: /Stop/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Restart/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Start$/ })).not.toBeInTheDocument();
  });

  it('shows Start when the application is stopped', async () => {
    // The field is absent, not undefined: the API omits it for an application
    // that is not running, and that is the case being tested.
    const stopped = app({ status: 'stopped' });
    delete stopped.runtime;
    mockApi({ apps: [stopped] });
    renderWithProviders(<NodePage />);

    await screen.findByText('shop-app');
    expect(screen.getByRole('button', { name: /Start/ })).toBeInTheDocument();
  });

  // "Running" and "answering" are not the same thing, and a process that is up
  // but not listening is the most confusing state an application can be in.
  it('says when a process is running but nothing is listening', async () => {
    mockApi({
      apps: [
        app({
          runtime: {
            state: 'running',
            pid: 4242,
            port: 3000,
            uptime_seconds: 5,
            managed_by: 'agent',
            listening: false,
            detail: '',
          },
        }),
      ],
    });

    renderWithProviders(<NodePage />);
    await screen.findByText('shop-app');
    await userEvent.click(screen.getByRole('button', { name: 'Expand shop-app' }));

    expect(await screen.findByText('This application is not answering')).toBeInTheDocument();
  });

  it('lists the environment names without their values', async () => {
    mockApi();
    renderWithProviders(<NodePage />);

    await screen.findByText('shop-app');
    await userEvent.click(screen.getByRole('button', { name: 'Expand shop-app' }));

    // The values are the application's credentials: what is set is useful, what
    // it is set to is a separate, audited read.
    expect(await screen.findByText('DATABASE_URL')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Show values/ })).toBeInTheDocument();

    const calls = vi.mocked(globalThis.fetch).mock.calls.map((call) => String(call[0]));
    expect(calls.some((url) => /\/environment$/.test(url) && !url.includes('PUT'))).toBe(false);
  });

  it('warns that the code is left alone when removing an application', async () => {
    mockApi();
    renderWithProviders(<NodePage />);

    await screen.findByText('shop-app');
    await userEvent.click(screen.getByRole('button', { name: 'Remove shop-app' }));

    expect(await screen.findByText(/left exactly where they are/)).toBeInTheDocument();
  });

  it('hides every control from a reader who cannot change websites', async () => {
    mockApi({ permissions: ['website.view'] });
    renderWithProviders(<NodePage />);

    expect(await screen.findByText('shop-app')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Add Application/ })).not.toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /Stop/ })).not.toBeInTheDocument();
  });
});
