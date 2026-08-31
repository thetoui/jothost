import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { sshApi } from '@/features/ssh/api';
import type { SSHChange } from '@/types/api';

export const sshKeys = {
  all: ['ssh'] as const,
  status: () => [...sshKeys.all, 'status'] as const,
  keys: (account: string) => [...sshKeys.all, 'keys', account] as const,
};

/**
 * useSSHStatus reads the server's settings, accounts and recommendations.
 *
 * Not polled. Nothing here changes without somebody changing it, and a page
 * that re-fetched every few seconds would fight the form an operator is in the
 * middle of filling in.
 */
export function useSSHStatus() {
  return useQuery({
    queryKey: sshKeys.status(),
    queryFn: ({ signal }) => sshApi.status(signal),
  });
}

/** useSSHKeys lists one account's authorised keys. */
export function useSSHKeys(account: string) {
  return useQuery({
    queryKey: sshKeys.keys(account),
    queryFn: ({ signal }) => sshApi.keys(account, signal),
    enabled: account !== '',
  });
}

/** useConfigureSSH changes a setting. */
export function useConfigureSSH() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (change: SSHChange) => sshApi.configure(change),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: sshKeys.all });
    },
  });
}

/** useAddSSHKey authorises a key for an account. */
export function useAddSSHKey() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ account, key }: { account: string; key: string }) =>
      sshApi.addKey(account, key),
    onSuccess: () => {
      // The status is refreshed too: adding the first key changes what the
      // recommendations say, and is what makes turning passwords off possible.
      void queryClient.invalidateQueries({ queryKey: sshKeys.all });
    },
  });
}

/** useRemoveSSHKey withdraws a key. */
export function useRemoveSSHKey() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ account, fingerprint }: { account: string; fingerprint: string }) =>
      sshApi.removeKey(account, fingerprint),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: sshKeys.all });
    },
  });
}
