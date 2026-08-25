import { request } from '@/services/apiClient';
import type { DashboardSnapshot, MetricRange, MetricSeries } from '@/types/api';

/** Dashboard and metric API calls. */
export const dashboardApi = {
  snapshot: (signal?: AbortSignal) =>
    request<DashboardSnapshot>('/dashboard', signal ? { signal } : {}),

  metrics: (serverId: string, range: MetricRange, signal?: AbortSignal) =>
    request<MetricSeries>(
      `/servers/${encodeURIComponent(serverId)}/metrics?range=${range}`,
      signal ? { signal } : {},
    ),
};
