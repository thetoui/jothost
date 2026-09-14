import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { LoginPage } from '@/pages/auth/LoginPage';
import { clearTokens, getAccessToken, getRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';

const tokenPair = {
  access_token: 'access-1',
  refresh_token: 'refresh-1',
  token_type: 'Bearer',
  expires_in: 900,
};

describe('LoginPage', () => {
  beforeEach(() => {
    clearTokens();
    useAuthStore.setState({ status: 'anonymous', mfaToken: null });
    vi.spyOn(globalThis, 'fetch');
  });

  it('renders the password form', () => {
    renderWithProviders(<LoginPage />);

    expect(screen.getByLabelText('Username')).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });

  it('signs in and stores the session', async () => {
    const user = userEvent.setup();
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse(tokenPair));

    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password'), 'correct-horse-battery-staple');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await waitFor(() => {
      expect(useAuthStore.getState().status).toBe('authenticated');
    });
    expect(getAccessToken()).toBe('access-1');
    expect(getRefreshToken()).toBe('refresh-1');
  });

  it('shows the server error message on bad credentials', async () => {
    const user = userEvent.setup();
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('UNAUTHORIZED', 'Invalid username or password', 401),
    );

    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Invalid username or password');
    expect(useAuthStore.getState().status).toBe('anonymous');
    // A failed sign-in must leave no credentials behind.
    expect(getRefreshToken()).toBeNull();
  });

  it('surfaces rate limiting', async () => {
    const user = userEvent.setup();
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('RATE_LIMITED', 'Too many attempts. Try again later.', 429),
    );

    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Too many attempts');
  });

  it('masks the password and never renders it as visible text', async () => {
    const user = userEvent.setup();
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('UNAUTHORIZED', 'Invalid username or password', 401),
    );

    const { container } = renderWithProviders(<LoginPage />);

    const passwordField = screen.getByLabelText('Password');
    expect(passwordField).toHaveAttribute('type', 'password');
    expect(passwordField).toHaveAttribute('autocomplete', 'current-password');

    const password = 'super-secret-password';
    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(passwordField, password);
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await screen.findByRole('alert');

    // A failed attempt must not echo the credential back as page text, and
    // must not persist it anywhere.
    expect(container.textContent).not.toContain(password);
    expect(JSON.stringify(globalThis.localStorage)).not.toContain(password);
  });

  it('does not store credentials after a failed sign-in', async () => {
    const user = userEvent.setup();
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('UNAUTHORIZED', 'Invalid username or password', 401),
    );

    renderWithProviders(<LoginPage />);

    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await screen.findByRole('alert');
    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
  });

  describe('two-factor step', () => {
    it('switches to the code form when the API demands a second factor', async () => {
      const user = userEvent.setup();
      vi.mocked(globalThis.fetch).mockResolvedValue(
        envelopeResponse({ mfa_required: true, mfa_token: 'mfa-1' }),
      );

      renderWithProviders(<LoginPage />);

      await user.type(screen.getByLabelText('Username'), 'admin');
      await user.type(screen.getByLabelText('Password'), 'correct-horse-battery-staple');
      await user.click(screen.getByRole('button', { name: 'Sign in' }));

      expect(await screen.findByLabelText('Verification code')).toBeInTheDocument();
      // No session may exist yet: the password alone is not enough.
      expect(useAuthStore.getState().status).toBe('anonymous');
      expect(getRefreshToken()).toBeNull();
    });

    it('completes the login with a valid code', async () => {
      const user = userEvent.setup();
      useAuthStore.setState({ status: 'anonymous', mfaToken: 'mfa-1' });
      vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse(tokenPair));

      renderWithProviders(<LoginPage />);

      await user.type(screen.getByLabelText('Verification code'), '123456');
      await user.click(screen.getByRole('button', { name: 'Verify' }));

      await waitFor(() => {
        expect(useAuthStore.getState().status).toBe('authenticated');
      });
    });

    it('reports an invalid code without losing the challenge', async () => {
      const user = userEvent.setup();
      useAuthStore.setState({ status: 'anonymous', mfaToken: 'mfa-1' });
      vi.mocked(globalThis.fetch).mockResolvedValue(
        errorResponse('UNAUTHORIZED', 'Invalid verification code', 401),
      );

      renderWithProviders(<LoginPage />);

      await user.type(screen.getByLabelText('Verification code'), '000000');
      await user.click(screen.getByRole('button', { name: 'Verify' }));

      expect(await screen.findByRole('alert')).toHaveTextContent('Invalid verification code');
      // The user must be able to retype the code rather than start over.
      expect(useAuthStore.getState().mfaToken).toBe('mfa-1');
    });

    it('rejects non-numeric input and requires six digits', async () => {
      const user = userEvent.setup();
      useAuthStore.setState({ status: 'anonymous', mfaToken: 'mfa-1' });

      renderWithProviders(<LoginPage />);

      const input = screen.getByLabelText('Verification code');
      await user.type(input, 'ab12cd34');

      expect(input).toHaveValue('1234');
      expect(screen.getByRole('button', { name: 'Verify' })).toBeDisabled();
    });

    it('signs in with a recovery code instead of the authenticator', async () => {
      const user = userEvent.setup();
      useAuthStore.setState({ status: 'anonymous', mfaToken: 'mfa-1' });
      const bodies: unknown[] = [];
      vi.mocked(globalThis.fetch).mockImplementation(async (_input, init) => {
        bodies.push(JSON.parse(String(init?.body)));
        return envelopeResponse(tokenPair);
      });

      renderWithProviders(<LoginPage />);

      await user.click(screen.getByRole('button', { name: 'Use a recovery code' }));
      const input = screen.getByLabelText('Recovery code');
      const verify = screen.getByRole('button', { name: 'Verify' });

      // Not until it is a whole code, however it is spaced.
      await user.type(input, 'ABCD EFGH JKMN');
      expect(verify).toBeDisabled();
      await user.type(input, ' PQRS');
      expect(verify).toBeEnabled();

      await user.click(verify);

      await waitFor(() => {
        expect(useAuthStore.getState().status).toBe('authenticated');
      });
      // Sent as a recovery code, and only as one.
      expect(bodies).toEqual([{ mfa_token: 'mfa-1', recovery_code: 'ABCD EFGH JKMN PQRS' }]);
    });

    it('reports a used recovery code and can go back to the authenticator', async () => {
      const user = userEvent.setup();
      useAuthStore.setState({ status: 'anonymous', mfaToken: 'mfa-1' });
      vi.mocked(globalThis.fetch).mockResolvedValue(
        errorResponse('UNAUTHORIZED', 'Invalid or already used recovery code', 401),
      );

      renderWithProviders(<LoginPage />);

      await user.click(screen.getByRole('button', { name: 'Use a recovery code' }));
      await user.type(screen.getByLabelText('Recovery code'), 'abcd-efgh-jkmn-pqrs');
      await user.click(screen.getByRole('button', { name: 'Verify' }));

      expect(await screen.findByRole('alert')).toHaveTextContent('already used recovery code');
      expect(useAuthStore.getState().mfaToken).toBe('mfa-1');

      await user.click(screen.getByRole('button', { name: 'Use your authenticator app' }));
      expect(screen.getByLabelText('Verification code')).toBeInTheDocument();
      // The recovery code's error does not follow the user to the other form.
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    });

    it('can return to the password step', async () => {
      const user = userEvent.setup();
      useAuthStore.setState({ status: 'anonymous', mfaToken: 'mfa-1' });

      renderWithProviders(<LoginPage />);

      await user.click(screen.getByRole('button', { name: 'Back to sign in' }));

      expect(screen.getByLabelText('Username')).toBeInTheDocument();
    });
  });
});
