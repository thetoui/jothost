import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { PHPPage } from '@/pages/PHPPage';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import type { PHPVersion } from '@/types/api';

function version(overrides: Partial<PHPVersion> = {}): PHPVersion {
  return {
    id: '11111111-2222-3333-4444-555555555555',
    version: '8.3',
    binary_path: '/usr/sbin/php-fpm83',
    fpm_service: 'php-fpm83',
    status: 'available',
    installed: true,
    in_use: 0,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
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

describe('PHPPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('lists the versions the server has', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view']);
      }
      return envelopeResponse({ versions: [version()], count: 1 });
    });

    renderWithProviders(<PHPPage />);

    expect(await screen.findByText('PHP 8.3')).toBeInTheDocument();
    expect(screen.getByText('Installed')).toBeInTheDocument();
  });

  it('says so when no PHP is installed', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view']);
      }
      return envelopeResponse({ versions: [], count: 0 });
    });

    renderWithProviders(<PHPPage />);

    expect(await screen.findByText('No PHP is installed')).toBeInTheDocument();
  });

  // Removing a version websites still run would take every one of them
  // offline, so the control says why rather than offering a refused action.
  it('does not offer to remove a version that websites use', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      return envelopeResponse({ versions: [version({ in_use: 2 })], count: 1 });
    });

    renderWithProviders(<PHPPage />);

    expect(await screen.findByText('In use by 2 websites')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument();
  });

  it('offers to remove an unused version', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      return envelopeResponse({ versions: [version({ in_use: 0 })], count: 1 });
    });

    renderWithProviders(<PHPPage />);

    expect(await screen.findByRole('button', { name: 'Remove' })).toBeInTheDocument();
  });

  // Installing changes the whole server, so a viewer must not see the control.
  it('hides the install form from a user who cannot manage the server', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view']);
      }
      return envelopeResponse({ versions: [version()], count: 1 });
    });

    renderWithProviders(<PHPPage />);

    await screen.findByText('PHP 8.3');
    expect(screen.queryByLabelText('Install a version')).not.toBeInTheDocument();
  });

  it('queues an install', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (init?.method === 'POST') {
        return envelopeResponse({ job: { id: 'job-1', type: 'php.install' } }, 202);
      }
      return envelopeResponse({ versions: [], count: 0 });
    });

    const user = userEvent.setup();
    renderWithProviders(<PHPPage />);

    await user.type(await screen.findByLabelText('Install a version'), '8.3');
    await user.click(screen.getByRole('button', { name: 'Install' }));

    const posted = vi
      .mocked(globalThis.fetch)
      .mock.calls.some(([, init]) => init?.method === 'POST');
    expect(posted).toBe(true);
  });

  // The API distinguishes an unknown version from a host that cannot install,
  // so its message is shown rather than a generic failure.
  it('surfaces the server message when an install is refused', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (init?.method === 'POST') {
        return errorResponse(
          'VALIDATION_FAILED',
          'invalid PHP version: "9.9" is not available',
          422,
        );
      }
      return envelopeResponse({ versions: [], count: 0 });
    });

    const user = userEvent.setup();
    renderWithProviders(<PHPPage />);

    await user.type(await screen.findByLabelText('Install a version'), '9.9');
    await user.click(screen.getByRole('button', { name: 'Install' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('is not available');
  });

  // A version whose row survives but is no longer on the host must not read as
  // installed: a site pointed at it would return 502.
  it('shows a removed version as not installed', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['server.view']);
      }
      return envelopeResponse({
        versions: [version({ installed: false, binary_path: null })],
        count: 1,
      });
    });

    renderWithProviders(<PHPPage />);

    expect(await screen.findByText('Not installed')).toBeInTheDocument();
    expect(screen.queryByText('Installed')).not.toBeInTheDocument();
  });
});
