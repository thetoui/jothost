import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { authKeys } from '@/features/auth/hooks';
import {
  getAccessToken,
  getRefreshToken,
  setAccessToken,
  setRefreshToken,
} from '@/features/auth/tokenStorage';
import { tenancyApi } from '@/features/tenancy/api';
import type { ServicePlanInput, TenantAccountInput } from '@/types/api';

export const tenancyKeys = {
  all: ['tenancy'] as const,
  overview: () => [...tenancyKeys.all, 'overview'] as const,
  subscription: (id: string) => [...tenancyKeys.all, 'subscription', id] as const,
  impersonation: () => [...tenancyKeys.all, 'impersonation'] as const,
  history: () => [...tenancyKeys.all, 'impersonation', 'history'] as const,
};

/** useTenancy reads accounts, plans, subscriptions and what the host enforces. */
export function useTenancy() {
  return useQuery({
    queryKey: tenancyKeys.overview(),
    queryFn: ({ signal }) => tenancyApi.overview(signal),
    placeholderData: (previous) => previous,
  });
}

/** useSubscription reads one subscription with its websites and usage. */
export function useSubscription(id: string | undefined) {
  return useQuery({
    queryKey: tenancyKeys.subscription(id ?? ''),
    queryFn: ({ signal }) => tenancyApi.subscription(id ?? '', signal),
    enabled: Boolean(id),
  });
}

/**
 * useImpersonation reads whether this session is somebody signing in as
 * somebody else.
 *
 * Read from the server rather than from a local flag: the token is what
 * decides, and a flag in the browser would survive a session the server has
 * already ended.
 */
export function useImpersonation() {
  return useQuery({
    queryKey: tenancyKeys.impersonation(),
    queryFn: ({ signal }) => tenancyApi.currentImpersonation(signal),
    staleTime: 30_000,
  });
}

/** useImpersonationHistory lists recent impersonations. */
export function useImpersonationHistory() {
  return useQuery({
    queryKey: tenancyKeys.history(),
    queryFn: ({ signal }) => tenancyApi.impersonationHistory(signal),
  });
}

/** Mutations that change the tenancy and refresh what the page shows. */
function useTenancyMutation<TArgs, TResult>(fn: (args: TArgs) => Promise<TResult>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: tenancyKeys.all });
    },
  });
}

export function useCreateAccount() {
  return useTenancyMutation((body: TenantAccountInput) => tenancyApi.createAccount(body));
}

export function useDeleteAccount() {
  return useTenancyMutation((id: string) => tenancyApi.deleteAccount(id));
}

export function useCreatePlan() {
  return useTenancyMutation((body: ServicePlanInput) => tenancyApi.createPlan(body));
}

export function useUpdatePlan() {
  return useTenancyMutation((args: { id: string; body: ServicePlanInput }) =>
    tenancyApi.updatePlan(args.id, args.body),
  );
}

export function useDeletePlan() {
  return useTenancyMutation((id: string) => tenancyApi.deletePlan(id));
}

export function useCreateSubscription() {
  return useTenancyMutation((body: { owner_user_id: string; plan_id: string; name: string }) =>
    tenancyApi.createSubscription(body),
  );
}

export function useDeleteSubscription() {
  return useTenancyMutation((id: string) => tenancyApi.deleteSubscription(id));
}

export function useSetSubscriptionStatus() {
  return useTenancyMutation((args: { id: string; status: 'active' | 'suspended'; reason: string }) =>
    tenancyApi.setStatus(args.id, args.status, args.reason),
  );
}

export function useAddAddon() {
  return useTenancyMutation((args: { id: string; planId: string; quantity: number }) =>
    tenancyApi.addAddon(args.id, args.planId, args.quantity),
  );
}

export function useRemoveAddon() {
  return useTenancyMutation((args: { id: string; planId: string }) =>
    tenancyApi.removeAddon(args.id, args.planId),
  );
}

export function useMeasureSubscription() {
  return useTenancyMutation((id: string) => tenancyApi.measure(id));
}

export function useApplyIsolation() {
  return useTenancyMutation((id: string) => tenancyApi.applyIsolation(id));
}

// --------------------------------------------------------------- swapping in

/**
 * Where the operator's own session is kept while they are impersonating.
 *
 * sessionStorage rather than localStorage, deliberately: it dies with the tab,
 * which is the right lifetime for "put me back afterwards". A stash that
 * outlived the browser would be an operator's session sitting on disk long
 * after they had stopped using it.
 */
const STASH_KEY = 'jothost.impersonation.return';

interface StashedSession {
  access: string;
  refresh: string;
}

function stashSession(): void {
  const access = getAccessToken();
  const refresh = getRefreshToken();
  if (!access || !refresh) {
    return;
  }
  try {
    globalThis.sessionStorage?.setItem(
      STASH_KEY,
      JSON.stringify({ access, refresh } satisfies StashedSession),
    );
  } catch {
    // A browser with storage disabled still gets a working impersonation; what
    // it loses is the one-click way back, and signing in again is the fallback.
  }
}

function restoreSession(): boolean {
  let raw: string | null = null;
  try {
    raw = globalThis.sessionStorage?.getItem(STASH_KEY) ?? null;
    globalThis.sessionStorage?.removeItem(STASH_KEY);
  } catch {
    return false;
  }
  if (!raw) {
    return false;
  }
  try {
    const stashed = JSON.parse(raw) as StashedSession;
    setAccessToken(stashed.access);
    setRefreshToken(stashed.refresh);
    return true;
  } catch {
    return false;
  }
}

/**
 * useStartImpersonation swaps this browser's session for the customer's.
 *
 * The operator's own tokens are stashed first, so stopping puts them back
 * without another sign-in. The whole query cache is cleared afterwards rather
 * than invalidated: every cached answer belongs to a different account, and
 * showing one of them to the wrong person for even a moment is the mistake
 * this feature must not make.
 */
export function useStartImpersonation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (args: { userId: string; reason: string }) => {
      stashSession();
      const result = await tenancyApi.impersonate(args.userId, args.reason);
      setAccessToken(result.tokens.access_token);
      setRefreshToken(result.tokens.refresh_token);
      return result;
    },
    onSuccess: () => {
      queryClient.clear();
      void queryClient.invalidateQueries({ queryKey: authKeys.all });
    },
  });
}

/**
 * useEndImpersonation ends the session and puts the operator's back.
 *
 * The server is asked first. If it refuses, the operator's session is not
 * restored — otherwise the panel would look like it had stopped impersonating
 * while the customer's session was still live somewhere.
 */
export function useEndImpersonation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const result = await tenancyApi.endImpersonation();
      restoreSession();
      return result;
    },
    onSuccess: () => {
      queryClient.clear();
    },
  });
}
