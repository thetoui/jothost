import { useQuery } from '@tanstack/react-query';

import { serverApi, type ProcessSort } from '@/features/server/api';

export const serverKeys = {
  all: ['server'] as const,
  processes: (limit: number, sort: ProcessSort) =>
    [...serverKeys.all, 'processes', limit, sort] as const,
};

/**
 * useHostProcesses lists what is running.
 *
 * Polled, because a process list is only useful if it is current — but slowly:
 * each read walks /proc on the host, and a table refreshing every second is a
 * cost paid on the managed machine to animate a page nobody is watching that
 * closely.
 */
export function useHostProcesses(limit: number, sort: ProcessSort) {
  return useQuery({
    queryKey: serverKeys.processes(limit, sort),
    queryFn: ({ signal }) => serverApi.processes(limit, sort, signal),
    refetchInterval: 10_000,
    staleTime: 5_000,
  });
}
