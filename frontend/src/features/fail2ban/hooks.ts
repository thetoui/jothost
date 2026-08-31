import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { fail2banApi } from '@/features/fail2ban/api';
import type { Fail2BanChange } from '@/types/api';

export const fail2banKeys = {
  all: ['fail2ban'] as const,
  status: () => [...fail2banKeys.all, 'status'] as const,
  banned: () => [...fail2banKeys.all, 'banned'] as const,
};

/**
 * useFail2BanStatus reads the jails and the policy.
 *
 * Polled, because the counters are the point: an operator watching this page is
 * watching failures accumulate, and a table that only refreshed on navigation
 * would show a host that was quiet ten minutes ago.
 */
export function useFail2BanStatus() {
  return useQuery({
    queryKey: fail2banKeys.status(),
    queryFn: ({ signal }) => fail2banApi.status(signal),
    refetchInterval: 20_000,
    placeholderData: (previous) => previous,
  });
}

/** useBannedAddresses lists what the host is currently blocking. */
export function useBannedAddresses(enabled: boolean) {
  return useQuery({
    queryKey: fail2banKeys.banned(),
    queryFn: ({ signal }) => fail2banApi.banned(signal),
    refetchInterval: 20_000,
    placeholderData: (previous) => previous,
    enabled,
  });
}

/** useInstallFail2Ban puts it on the host. */
export function useInstallFail2Ban() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => fail2banApi.install(),
    onSuccess: () => {
      // The Services page lists it too, and it has just appeared there.
      void queryClient.invalidateQueries({ queryKey: fail2banKeys.all });
      void queryClient.invalidateQueries({ queryKey: ['services'] });
    },
  });
}

/** useConfigureJail changes one jail. */
export function useConfigureJail() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ jail, change }: { jail: string; change: Fail2BanChange }) =>
      fail2banApi.configure(jail, change),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: fail2banKeys.all });
    },
  });
}

/** useSetIgnored changes the addresses no jail may ban. */
export function useSetIgnored() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (ignored: string[]) => fail2banApi.setIgnored(ignored),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: fail2banKeys.all });
    },
  });
}

/** useUnban releases an address. */
export function useUnban() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ jail, address }: { jail: string; address: string }) =>
      fail2banApi.unban(jail, address),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: fail2banKeys.all });
    },
  });
}

/** useBan blocks an address by hand. */
export function useBan() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ jail, address }: { jail: string; address: string }) =>
      fail2banApi.ban(jail, address),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: fail2banKeys.all });
    },
  });
}
