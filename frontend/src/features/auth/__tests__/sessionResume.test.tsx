import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';

import { RequireAuth } from '@/features/auth/components/RequireAuth';
import { clearTokens, setRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { createTestQueryClient, envelopeResponse, errorResponse } from '@/test/utils';

const profile = {
  id: 'u1',
  username: 'admin',
  email: null,
  status: 'active',
  roles: ['admin'],
  permissions: ['server.view'],
  two_factor_enabled: false,
  last_login_at: null,
  created_at: '2026-01-01T00:00:00Z',
};

/**
 * Renders the guard inside a real route table.
 *
 * RequireAuth redirects with <Navigate>, so it must sit in a route tree that
 * actually has a /login destination — rendered bare, the redirect would have
 * nowhere to land and would re-fire on every render.
 */
function renderGuardedApp() {
  const queryClient = createTestQueryClient();

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter
        initialEntries={['/']}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <Routes>
          <Route path="/login" element={<p>login page</p>} />
          <Route element={<RequireAuth />}>
            <Route path="/" element={<p>protected content</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/**
 * Session resumption after a page reload.
 *
 * The access token lives only in memory, so a reload always starts with just
 * the stored refresh token and an `unknown` status. Getting this wrong is
 * either a permanent loading spinner or signing out a valid user, so both
 * outcomes are pinned here.
 */
describe('session resume on reload', () => {
  beforeEach(() => {
    clearTokens();
    vi.spyOn(globalThis, 'fetch');
  });

  it('resolves to authenticated when the stored refresh token still works', async () => {
    setRefreshToken('refresh-1');
    useAuthStore.setState({ status: 'unknown', mfaToken: null });

    // The first /auth/me 401s because no access token survived the reload;
    // the client refreshes and retries.
    vi.mocked(globalThis.fetch)
      .mockResolvedValueOnce(errorResponse('UNAUTHORIZED', 'Authentication required', 401))
      .mockResolvedValueOnce(
        envelopeResponse({
          access_token: 'access-2',
          refresh_token: 'refresh-2',
          token_type: 'Bearer',
          expires_in: 900,
        }),
      )
      .mockResolvedValueOnce(envelopeResponse(profile));

    renderGuardedApp();

    // The guard must hand over to the app, not sit on the interstitial.
    expect(await screen.findByText('protected content')).toBeInTheDocument();
    expect(useAuthStore.getState().status).toBe('authenticated');
  });

  it('redirects to login when the stored refresh token is dead', async () => {
    setRefreshToken('revoked');
    useAuthStore.setState({ status: 'unknown', mfaToken: null });

    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('UNAUTHORIZED', 'Invalid or expired token', 401),
    );

    renderGuardedApp();

    expect(await screen.findByText('login page')).toBeInTheDocument();
    await waitFor(() => {
      expect(useAuthStore.getState().status).toBe('anonymous');
    });
  });

  it('shows the interstitial while the session is still unknown', () => {
    setRefreshToken('refresh-1');
    useAuthStore.setState({ status: 'unknown', mfaToken: null });

    // A pending request keeps the status unresolved.
    vi.mocked(globalThis.fetch).mockImplementation(() => new Promise(() => {}));

    renderGuardedApp();

    expect(screen.getByRole('status')).toBeInTheDocument();
    // Redirecting now would sign out a user whose session is still valid.
    expect(screen.queryByText('login page')).not.toBeInTheDocument();
  });

  it('does not call the API when there is no stored session', async () => {
    useAuthStore.setState({ status: 'anonymous', mfaToken: null });

    renderGuardedApp();

    expect(await screen.findByText('login page')).toBeInTheDocument();
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });
});
