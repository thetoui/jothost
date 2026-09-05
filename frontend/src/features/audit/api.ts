import { request } from '@/services/apiClient';
import type { AuditActionList, AuditEntryList } from '@/types/api';

/** What a reading of the trail asks for. */
export interface AuditQuery {
  action?: string;
  user_id?: string;
  resource_type?: string;
  resource_id?: string;
  status?: string;
  since?: string;
  until?: string;
  limit?: number;
  offset?: number;
}

function query(params: AuditQuery): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    // Empty strings are filters nobody set. Sending them would ask the API to
    // match the empty action, which nothing has.
    if (value === undefined || value === '' || value === null) continue;
    search.set(key, String(value));
  }
  const rendered = search.toString();
  return rendered === '' ? '' : `?${rendered}`;
}

export const auditApi = {
  list: (params: AuditQuery, signal?: AbortSignal) =>
    request<AuditEntryList>(`/audit${query(params)}`, signal ? { signal } : {}),

  /** The action names actually present in the trail, for the filter. */
  actions: (signal?: AbortSignal) =>
    request<AuditActionList>('/audit/actions', signal ? { signal } : {}),
};
