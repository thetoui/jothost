import { useQuery } from '@tanstack/react-query';

import { systemService } from '@/services/systemService';

/** Query keys are centralised so cache invalidation stays predictable. */
export const systemKeys = {
  all: ['system'] as const,
  health: () => [...systemKeys.all, 'health'] as const,
};

/**
 * useApiHealth polls the API liveness endpoint. Server state lives in TanStack
 * Query, never in Zustand (CLAUDE.md section 10).
 */
export function useApiHealth() {
  return useQuery({
    queryKey: systemKeys.health(),
    queryFn: ({ signal }) => systemService.health(signal),
    refetchInterval: 30_000,
    staleTime: 10_000,
  });
}
