import { request } from '@/services/apiClient';
import type { ProcessList } from '@/types/api';

/** How a process listing may be ordered. The host implements these two. */
export type ProcessSort = 'memory' | 'cpu';

export const serverApi = {
  processes: (limit: number, sort: ProcessSort, signal?: AbortSignal) =>
    request<ProcessList>(
      `/server/processes?limit=${limit}&sort=${sort}`,
      signal ? { signal } : {},
    ),
};
