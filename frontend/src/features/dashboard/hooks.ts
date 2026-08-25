import { useQuery } from '@tanstack/react-query';

import { dashboardApi } from '@/features/dashboard/api';
import type { MetricRange } from '@/types/api';

/** Query keys are centralised so cache invalidation stays predictable. */
export const dashboardKeys = {
  all: ['dashboard'] as const,
  snapshot: () => [...dashboardKeys.all, 'snapshot'] as const,
  metrics: (serverId: string, range: MetricRange) =>
    [...dashboardKeys.all, 'metrics', serverId, range] as const,
};

/**
 * How often the live view refreshes.
 *
 * Matched to the server's default sampling interval: polling faster would
 * re-render the same numbers, and slower would make a dashboard someone is
 * watching during an incident feel stale.
 */
const LIVE_REFRESH_MS = 30_000;

/** useDashboard polls the live server view. */
export function useDashboard() {
  return useQuery({
    queryKey: dashboardKeys.snapshot(),
    queryFn: ({ signal }) => dashboardApi.snapshot(signal),
    refetchInterval: LIVE_REFRESH_MS,
    // The previous snapshot stays on screen while the next one loads, so the
    // page does not blank out every refresh.
    placeholderData: (previous) => previous,
    staleTime: 10_000,
  });
}

/**
 * useMetricHistory loads the graph series.
 *
 * Longer ranges refresh less often: a 30-day graph does not visibly change
 * minute to minute, and re-querying it would be work for no new information.
 */
export function useMetricHistory(serverId: string | undefined, range: MetricRange) {
  return useQuery({
    queryKey: dashboardKeys.metrics(serverId ?? '', range),
    queryFn: ({ signal }) => dashboardApi.metrics(serverId as string, range, signal),
    enabled: Boolean(serverId),
    refetchInterval: range === '1h' ? LIVE_REFRESH_MS : 5 * 60_000,
    placeholderData: (previous) => previous,
  });
}
