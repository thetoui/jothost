import { afterEach, describe, expect, it } from 'vitest';

import {
  clearTokens,
  getAccessToken,
  getRefreshToken,
  setAccessToken,
  setRefreshToken,
} from '@/features/auth/tokenStorage';

describe('tokenStorage', () => {
  afterEach(() => {
    clearTokens();
  });

  it('keeps the access token in memory only', () => {
    setAccessToken('access-1');

    expect(getAccessToken()).toBe('access-1');
    // The short-lived credential must never be persisted (CLAUDE.md §10).
    expect(globalThis.localStorage.getItem('jothost.access_token')).toBeNull();
    expect(JSON.stringify(globalThis.localStorage)).not.toContain('access-1');
  });

  it('persists the refresh token so a reload keeps the session', () => {
    setRefreshToken('refresh-1');

    expect(getRefreshToken()).toBe('refresh-1');
    expect(globalThis.localStorage.getItem('jothost.refresh_token')).toBe('refresh-1');
  });

  it('clears both tokens', () => {
    setAccessToken('access-1');
    setRefreshToken('refresh-1');

    clearTokens();

    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
    expect(globalThis.localStorage.getItem('jothost.refresh_token')).toBeNull();
  });

  it('removes the stored token when set to null', () => {
    setRefreshToken('refresh-1');
    setRefreshToken(null);

    expect(getRefreshToken()).toBeNull();
  });
});
