import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { securityApi } from '@/features/security/api';

export const securityKeys = {
  all: ['security'] as const,
  overview: () => [...securityKeys.all, 'overview'] as const,
  history: () => [...securityKeys.all, 'history'] as const,
};

/** useSecurityOverview reads the score and the findings behind it. */
export function useSecurityOverview() {
  return useQuery({
    queryKey: securityKeys.overview(),
    queryFn: ({ signal }) => securityApi.overview(signal),
    placeholderData: (previous) => previous,
  });
}

/**
 * useRunScan runs every scanner.
 *
 * A mutation rather than a query, deliberately: it walks a filesystem and
 * reaches the host seven times, so it happens when somebody asks rather than
 * when a component mounts.
 */
export function useRunScan() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => securityApi.scan(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: securityKeys.all });
    },
  });
}

/** useAcceptFinding records that a risk is known and deliberate. */
export function useAcceptFinding() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      securityApi.accept(id, reason),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: securityKeys.all });
    },
  });
}

/** useReopenFinding withdraws an acceptance. */
export function useReopenFinding() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => securityApi.reopen(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: securityKeys.all });
    },
  });
}
