import { keepPreviousData, useQuery } from '@tanstack/react-query';

import { auditApi, type AuditQuery } from '@/features/audit/api';

export const auditKeys = {
  all: ['audit'] as const,
  list: (params: AuditQuery) => [...auditKeys.all, 'list', params] as const,
  actions: () => [...auditKeys.all, 'actions'] as const,
};

/**
 * useAuditTrail reads a page of the trail.
 *
 * `keepPreviousData` so paging and changing a filter do not blank the table
 * between requests: a list that empties and refills reads as "nothing matched"
 * for as long as the request takes.
 */
export function useAuditTrail(params: AuditQuery) {
  return useQuery({
    queryKey: auditKeys.list(params),
    queryFn: ({ signal }) => auditApi.list(params, signal),
    placeholderData: keepPreviousData,
    // The trail is append-only and nothing in this page writes to it, so there
    // is no invalidation to do — only the passage of time makes it stale.
    staleTime: 15_000,
  });
}

/** useAuditActions lists the action names present, for the filter. */
export function useAuditActions() {
  return useQuery({
    queryKey: auditKeys.actions(),
    queryFn: ({ signal }) => auditApi.actions(signal),
    staleTime: 5 * 60_000,
  });
}
