import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { WebserverPage } from '@/pages/WebserverPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { WebserverStatus } from '@/types/api';

function status(overrides: Partial<WebserverStatus> = {}): WebserverStatus {
  return {
    mode: 'nginx',
    sites: 3,
    apache: {
      available: true,
      running: false,
      version: '2.4.68',
      sites: 0,
      can_install: true,
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
    recovery_codes_remaining: 0,
    roles: ['admin'],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('WebserverPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(current: WebserverStatus, onPut?: (body: unknown) => void) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage', 'website.update']);
      }
      if (url.includes('/webserver/apache/install')) {
        return envelopeResponse({ ...current.apache, available: true });
      }
      if (url.endsWith('/webserver') && init?.method === 'PUT') {
        onPut?.(JSON.parse(String(init.body)));
        return envelopeResponse({ mode: 'hybrid', jobs: [] }, 202);
      }
      return envelopeResponse(current);
    });
  }

  it('shows which arrangement the host runs', async () => {
    mockApi(status());
    renderWithProviders(<WebserverPage />);

    expect(await screen.findByText('nginx only')).toBeInTheDocument();
    expect(screen.getByText('nginx + Apache')).toBeInTheDocument();
    expect(screen.getByText('In use')).toBeInTheDocument();
    expect(screen.getByText('2.4.68')).toBeInTheDocument();
  });

  // Switching rewrites every site on the machine. That is not something to
  // discover after clicking.
  it('says how many sites a switch rewrites before making it', async () => {
    const put: unknown[] = [];
    mockApi(status(), (body) => put.push(body));
    renderWithProviders(<WebserverPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Use this' }));

    expect(await screen.findByText(/3 of them/)).toBeInTheDocument();
    expect(put).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: /Switch to nginx \+ Apache/ }));
    await waitFor(() => expect(put).toEqual([{ mode: 'hybrid' }]));
  });

  // Offering an arrangement the host cannot run would produce a mode change
  // that fails once per site.
  it('cannot choose the hybrid arrangement without Apache', async () => {
    mockApi(status({ apache: { ...status().apache, available: false } }));
    renderWithProviders(<WebserverPage />);

    expect(await screen.findByText('Apache is not installed on this host.')).toBeInTheDocument();
    // findBy rather than getBy: the buttons appear with the profile that says
    // which of them this user may see.
    expect(await screen.findByRole('button', { name: 'Use this' })).toBeDisabled();
    // Installing is offered instead, and it is a separate decision from
    // switching: one downloads packages, the other reconfigures every site.
    expect(screen.getByRole('button', { name: /Install Apache/ })).toBeInTheDocument();
  });

  it('offers no install on a host with no package manager', async () => {
    mockApi(
      status({ apache: { ...status().apache, available: false, can_install: false } }),
    );
    renderWithProviders(<WebserverPage />);

    expect(
      await screen.findByText('This host has no package manager the panel can use.'),
    ).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /Install Apache/ })).toBeDisabled();
  });
});
