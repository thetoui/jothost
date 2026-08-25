import { request } from '@/services/apiClient';
import type {
  LoginResponse,
  TokenPair,
  TwoFactorSetupResponse,
  UserProfile,
} from '@/types/api';

/**
 * Auth API calls. Login, refresh, and 2FA verification are anonymous: they are
 * how a caller obtains credentials in the first place.
 */
export const authApi = {
  login: (username: string, password: string) =>
    request<LoginResponse>('/auth/login', {
      method: 'POST',
      body: { username, password },
      anonymous: true,
    }),

  verifyTwoFactor: (mfaToken: string, code: string) =>
    request<TokenPair>('/auth/2fa/verify', {
      method: 'POST',
      body: { mfa_token: mfaToken, code },
      anonymous: true,
    }),

  logout: () => request<{ logged_out: boolean }>('/auth/logout', { method: 'POST' }),

  me: (signal?: AbortSignal) =>
    request<UserProfile>('/auth/me', signal ? { signal } : {}),

  setupTwoFactor: () =>
    request<TwoFactorSetupResponse>('/auth/2fa/setup', { method: 'POST' }),

  enableTwoFactor: (code: string) =>
    request<{ two_factor_enabled: boolean }>('/auth/2fa/enable', {
      method: 'POST',
      body: { code },
    }),

  disableTwoFactor: (password: string) =>
    request<{ two_factor_enabled: boolean }>('/auth/2fa/disable', {
      method: 'POST',
      body: { password },
    }),
};
