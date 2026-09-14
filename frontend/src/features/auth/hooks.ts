import { useEffect } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { authApi } from '@/features/auth/api';
import { ApiError } from '@/services/apiClient';
import { useAuthStore } from '@/stores/authStore';
import { getRefreshToken } from '@/features/auth/tokenStorage';

/** Query keys are centralised so cache invalidation stays predictable. */
export const authKeys = {
  all: ['auth'] as const,
  me: () => [...authKeys.all, 'me'] as const,
};

/**
 * useProfile loads the signed-in user.
 *
 * It runs whenever a refresh token exists, including on a cold reload: the
 * access token lives only in memory, so after a reload the first request 401s
 * and the API client silently redeems the stored refresh token. Whether that
 * succeeds is what decides the session state, which is why the `unknown`
 * status is resolved here rather than guessed at startup.
 */
export function useProfile() {
  const status = useAuthStore((state) => state.status);
  const resolveAuthenticated = useAuthStore((state) => state.resolveAuthenticated);
  const resolveAnonymous = useAuthStore((state) => state.resolveAnonymous);

  const query = useQuery({
    queryKey: authKeys.me(),
    queryFn: ({ signal }) => authApi.me(signal),
    enabled: status !== 'anonymous' && getRefreshToken() !== null,
    retry: false,
    staleTime: 60_000,
  });

  const { isSuccess, isError } = query;

  useEffect(() => {
    if (status !== 'unknown') {
      return;
    }
    if (isSuccess) {
      // The stored refresh token was redeemed successfully.
      resolveAuthenticated();
    } else if (isError) {
      // The stored session is gone: expired, revoked, or from another install.
      resolveAnonymous();
    }
  }, [status, isSuccess, isError, resolveAuthenticated, resolveAnonymous]);

  return query;
}

/** useLogin performs the password step. */
export function useLogin() {
  const setSession = useAuthStore((state) => state.setSession);
  const setMfaToken = useAuthStore((state) => state.setMfaToken);
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ username, password }: { username: string; password: string }) =>
      authApi.login(username, password),
    onSuccess: async (data) => {
      if (data.mfa_required && data.mfa_token) {
        setMfaToken(data.mfa_token);
        return;
      }
      if (!data.access_token || !data.refresh_token) {
        throw new Error('The server returned an incomplete login response.');
      }

      setSession({
        access_token: data.access_token,
        refresh_token: data.refresh_token,
        token_type: data.token_type ?? 'Bearer',
        expires_in: data.expires_in ?? 0,
      });
      await queryClient.invalidateQueries({ queryKey: authKeys.all });
    },
  });
}

/**
 * useVerifyTwoFactor completes a login that required a second factor, with
 * either an authenticator code or a recovery code.
 */
export function useVerifyTwoFactor() {
  const setSession = useAuthStore((state) => state.setSession);
  const mfaToken = useAuthStore((state) => state.mfaToken);
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (factor: { code: string } | { recoveryCode: string }) => {
      if (!mfaToken) {
        throw new Error('The verification session expired. Sign in again.');
      }
      return 'recoveryCode' in factor
        ? authApi.verifyRecoveryCode(mfaToken, factor.recoveryCode)
        : authApi.verifyTwoFactor(mfaToken, factor.code);
    },
    onSuccess: async (tokens) => {
      setSession(tokens);
      await queryClient.invalidateQueries({ queryKey: authKeys.all });
    },
  });
}

/**
 * useLogout ends the session.
 *
 * The local session is cleared even if the API call fails: the user asked to
 * sign out, and leaving them apparently signed in would be worse than a
 * server-side session that expires on its own.
 */
export function useLogout() {
  const reset = useAuthStore((state) => state.reset);
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => authApi.logout(),
    onSettled: () => {
      reset();
      queryClient.clear();
    },
  });
}

export function useSetupTwoFactor() {
  return useMutation({ mutationFn: () => authApi.setupTwoFactor() });
}

export function useEnableTwoFactor() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (code: string) => authApi.enableTwoFactor(code),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: authKeys.me() }),
  });
}

/** useRegenerateRecoveryCodes replaces the signed-in user's recovery codes. */
export function useRegenerateRecoveryCodes() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (password: string) => authApi.regenerateRecoveryCodes(password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: authKeys.me() }),
  });
}

export function useDisableTwoFactor() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (password: string) => authApi.disableTwoFactor(password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: authKeys.me() }),
  });
}

/** errorMessage renders an API error for display. */
export function errorMessage(error: unknown, fallback = 'Something went wrong.'): string {
  if (error instanceof ApiError) {
    return error.message;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return fallback;
}
