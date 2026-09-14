import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { SecurityPage } from '@/pages/auth/SecurityPage';
import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';

const codes = Array.from({ length: 10 }, (_, i) => `abc${i}-defg-hjkm-npqr`);
const replacement = Array.from({ length: 10 }, (_, i) => `zyx${i}-wvut-srqp-nmkj`);

function profile(enabled: boolean, remaining: number) {
  return envelopeResponse({
    id: 'user-1',
    username: 'admin',
    email: null,
    status: 'active',
    two_factor_enabled: enabled,
    recovery_codes_remaining: remaining,
    roles: ['admin'],
    permissions: [],
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('SecurityPage recovery codes', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('shows the codes after enabling, even once the profile says two-factor is on', async () => {
    const user = userEvent.setup();
    let enabled = false;
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) return profile(enabled, enabled ? 10 : 0);
      if (url.includes('/auth/2fa/setup')) {
        return envelopeResponse({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/x' });
      }
      if (url.includes('/auth/2fa/enable')) {
        enabled = true;
        return envelopeResponse({ two_factor_enabled: true, recovery_codes: codes });
      }
      throw new Error(`unexpected request ${url}`);
    });

    renderWithProviders(<SecurityPage />);

    await user.click(
      await screen.findByRole('button', { name: 'Set up two-factor authentication' }),
    );
    await user.type(await screen.findByLabelText('Verification code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Enable' }));

    const list = await screen.findByRole('list', { name: 'Recovery codes' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(10);
    expect(list).toHaveTextContent('abc0-defg-hjkm-npqr');

    // The profile refetch flips the section to "enabled". The codes must
    // survive that: they are never shown again.
    await screen.findByText('Enabled');
    expect(screen.getByRole('list', { name: 'Recovery codes' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'I have saved these codes' }));
    expect(screen.queryByRole('list', { name: 'Recovery codes' })).not.toBeInTheDocument();
    expect(await screen.findByText(/10 unused codes left/)).toBeInTheDocument();
  });

  it('replaces the codes with the password and shows the new set', async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) return profile(true, 2);
      if (url.includes('/auth/2fa/recovery-codes')) {
        bodies.push(JSON.parse(String(init?.body)));
        return envelopeResponse({ recovery_codes: replacement });
      }
      throw new Error(`unexpected request ${url}`);
    });

    renderWithProviders(<SecurityPage />);

    // Two left is low enough to say so.
    expect(await screen.findByText(/2 unused codes left/)).toHaveTextContent(
      'Replace them before you run out',
    );

    const replace = screen.getByRole('button', { name: 'Replace recovery codes' });
    expect(replace).toBeDisabled();
    await user.type(screen.getByLabelText('Password to replace them'), 'correct-horse');
    await user.click(replace);

    const list = await screen.findByRole('list', { name: 'Recovery codes' });
    expect(list).toHaveTextContent('zyx9-wvut-srqp-nmkj');
    expect(bodies).toEqual([{ password: 'correct-horse' }]);
  });

  it('reports a wrong password without showing any codes', async () => {
    const user = userEvent.setup();
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) return profile(true, 8);
      if (url.includes('/auth/2fa/recovery-codes')) {
        return errorResponse('UNAUTHORIZED', 'Invalid username or password', 401);
      }
      throw new Error(`unexpected request ${url}`);
    });

    renderWithProviders(<SecurityPage />);

    await user.type(await screen.findByLabelText('Password to replace them'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Replace recovery codes' }));

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('Invalid username or password');
    });
    expect(screen.queryByRole('list', { name: 'Recovery codes' })).not.toBeInTheDocument();
  });
});
