import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { servicesApi } from '@/features/services/api';
import type { ServiceAction } from '@/types/api';

export const serviceKeys = {
  all: ['services'] as const,
  list: () => [...serviceKeys.all, 'list'] as const,
};

/**
 * useServices lists what the host runs.
 *
 * Polled while the page is open: a service's state changes for reasons that
 * have nothing to do with this panel — a crash, a package upgrade, an operator
 * at a terminal — and a list that only refreshes on navigation is one that is
 * quietly wrong.
 */
export function useServices() {
  return useQuery({
    queryKey: serviceKeys.list(),
    queryFn: ({ signal }) => servicesApi.list(signal),
    refetchInterval: 15_000,
    placeholderData: (previous) => previous,
  });
}

/** useServiceAction starts, stops, restarts, enables or disables a service. */
export function useServiceAction() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ key, action }: { key: string; action: ServiceAction }) =>
      servicesApi.act(key, action),
    onSuccess: () => {
      // The dashboard shows the same services, so it is refreshed with them.
      void queryClient.invalidateQueries({ queryKey: serviceKeys.list() });
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}
