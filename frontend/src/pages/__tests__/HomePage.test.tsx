import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { useLocation } from 'react-router-dom';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { HomePage } from '@/pages/HomePage';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import { useAuthStore } from '@/stores/authStore';

function WhereAmI() {
  return <p data-testid="location">{useLocation().pathname}</p>;
}

function signedInWith(permissions: string[]) {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    if (String(input).includes('/auth/me')) {
      return envelopeResponse({
        id: 'user-1',
        username: 'someone',
        email: null,
        status: 'active',
        two_factor_enabled: false,
        recovery_codes_remaining: 0,
        roles: [],
        permissions,
        created_at: '2026-01-01T00:00:00Z',
        last_login_at: null,
      });
    }
    return errorResponse('FORBIDDEN', 'Permission denied', 403);
  });
  renderWithProviders(
    <>
      <HomePage />
      <WhereAmI />
    </>,
  );
}

describe('HomePage', () => {
  beforeEach(() => {
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });
  afterEach(() => {
    clearTokens();
    useAuthStore.setState({ status: 'anonymous' });
    vi.restoreAllMocks();
  });

  it('sends a hosting customer to their websites, not to a dashboard they cannot read', async () => {
    signedInWith(['website.view', 'file.read']);

    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent('/websites');
    });
  });

  it('sends somebody who can open nothing else to their own account security', async () => {
    signedInWith([]);

    await waitFor(() => {
      expect(screen.getByTestId('location')).toHaveTextContent('/security');
    });
  });

  it('keeps the dashboard for anybody who can see the server', async () => {
    signedInWith(['server.view', 'website.view']);

    expect(await screen.findByRole('heading', { name: 'Dashboard' })).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/$/);
  });
});
