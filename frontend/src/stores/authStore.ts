import { create } from 'zustand';

import { clearTokens, getRefreshToken, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { onSessionEnded } from '@/services/apiClient';
import type { TokenPair } from '@/types/api';

/**
 * Authentication status.
 *
 * `unknown` is the state before the app has decided whether a stored refresh
 * token is still valid. Routing must wait for it rather than assuming logged
 * out, or a reload would bounce an authenticated user to the login screen.
 */
export type AuthStatus = 'unknown' | 'authenticated' | 'anonymous';

interface AuthState {
  status: AuthStatus;
  /** Set between a correct password and a correct TOTP code. */
  mfaToken: string | null;
  setSession: (tokens: TokenPair) => void;
  setMfaToken: (token: string | null) => void;
  /** Confirms a stored refresh token was successfully redeemed on startup. */
  resolveAuthenticated: () => void;
  resolveAnonymous: () => void;
  reset: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  // A stored refresh token means a session may be resumable; the app confirms
  // it by fetching the profile before trusting it.
  status: getRefreshToken() ? 'unknown' : 'anonymous',
  mfaToken: null,

  setSession: (tokens) => {
    setAccessToken(tokens.access_token);
    setRefreshToken(tokens.refresh_token);
    set({ status: 'authenticated', mfaToken: null });
  },

  setMfaToken: (token) => set({ mfaToken: token }),

  resolveAuthenticated: () => set({ status: 'authenticated' }),

  resolveAnonymous: () => set({ status: 'anonymous' }),

  reset: () => {
    clearTokens();
    set({ status: 'anonymous', mfaToken: null });
  },
}));

// The API client ends the session when a refresh fails; the store follows so
// the UI redirects instead of rendering an authenticated shell with no tokens.
onSessionEnded(() => {
  useAuthStore.setState({ status: 'anonymous', mfaToken: null });
});
