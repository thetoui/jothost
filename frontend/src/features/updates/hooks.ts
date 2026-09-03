import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { updatesApi } from '@/features/updates/api';
import type { UpdateSettingsChange } from '@/types/api';

export const updateKeys = {
  all: ['updates'] as const,
  overview: () => [...updateKeys.all, 'overview'] as const,
  history: () => [...updateKeys.all, 'history'] as const,
};

/** useUpdateOverview reads what this host has waiting. */
export function useUpdateOverview() {
  return useQuery({
    queryKey: updateKeys.overview(),
    queryFn: ({ signal }) => updatesApi.overview(signal),
    placeholderData: (previous) => previous,
  });
}

/**
 * useCheckUpdates refreshes the package index and reads what is outstanding.
 *
 * Deliberately a mutation rather than a query: it changes the host's package
 * index and costs a round trip to a distribution's mirrors, so it happens when
 * somebody asks rather than when a component mounts.
 */
export function useCheckUpdates() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => updatesApi.check(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: updateKeys.all });
    },
  });
}

/** useApplyUpdates installs updates. */
export function useApplyUpdates() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: { packages?: string[]; security_only?: boolean }) =>
      updatesApi.apply(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: updateKeys.all });
      // An upgrade can move the PHP or Node runtime, and both pages show a
      // version this has just changed.
      void queryClient.invalidateQueries({ queryKey: ['php'] });
      void queryClient.invalidateQueries({ queryKey: ['node'] });
      void queryClient.invalidateQueries({ queryKey: ['services'] });
    },
  });
}

/** useRevertPackage puts one package back to an earlier version. */
export function useRevertPackage() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: { package: string; version: string }) => updatesApi.revert(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: updateKeys.all });
    },
  });
}

/** useSaveUpdateSettings writes the automatic-update settings. */
export function useSaveUpdateSettings() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (change: UpdateSettingsChange) => updatesApi.saveSettings(change),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: updateKeys.all });
    },
  });
}

/** useUpdateHistory reads what has been applied. */
export function useUpdateHistory(limit = 50, enabled = true) {
  return useQuery({
    queryKey: [...updateKeys.history(), limit],
    queryFn: ({ signal }) => updatesApi.history(limit, signal),
    enabled,
  });
}
