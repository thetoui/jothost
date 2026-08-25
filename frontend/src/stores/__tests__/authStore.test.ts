import { beforeEach, describe, expect, it } from 'vitest';

import { useAuthStore } from '@/stores/authStore';
import { clearTokens, getAccessToken, getRefreshToken } from '@/features/auth/tokenStorage';

const tokens = {
  access_token: 'access-1',
  refresh_token: 'refresh-1',
  token_type: 'Bearer',
  expires_in: 900,
};

describe('authStore', () => {
  beforeEach(() => {
    clearTokens();
    useAuthStore.setState({ status: 'anonymous', mfaToken: null });
  });

  it('stores tokens and marks the session authenticated', () => {
    useAuthStore.getState().setSession(tokens);

    expect(useAuthStore.getState().status).toBe('authenticated');
    expect(getAccessToken()).toBe('access-1');
    expect(getRefreshToken()).toBe('refresh-1');
  });

  it('clears the MFA challenge once the session starts', () => {
    useAuthStore.getState().setMfaToken('mfa-1');
    useAuthStore.getState().setSession(tokens);

    expect(useAuthStore.getState().mfaToken).toBeNull();
  });

  it('reset clears every credential', () => {
    useAuthStore.getState().setSession(tokens);
    useAuthStore.getState().reset();

    expect(useAuthStore.getState().status).toBe('anonymous');
    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
  });

  it('resolveAnonymous settles the unknown state', () => {
    useAuthStore.setState({ status: 'unknown' });
    useAuthStore.getState().resolveAnonymous();

    expect(useAuthStore.getState().status).toBe('anonymous');
  });
});
