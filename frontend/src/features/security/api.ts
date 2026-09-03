import { request } from '@/services/apiClient';
import type { SecurityFinding, SecurityOverview, SecurityScan } from '@/types/api';

/** Security Center calls. */
export const securityApi = {
  overview: (signal?: AbortSignal) =>
    request<SecurityOverview>('/security/score', signal ? { signal } : {}),

  findings: (params: { status?: string; scanner?: string; severity?: string } = {}) => {
    const query = new URLSearchParams();
    if (params.status) query.set('status', params.status);
    if (params.scanner) query.set('scanner', params.scanner);
    if (params.severity) query.set('severity', params.severity);
    const suffix = query.toString() ? `?${query.toString()}` : '';
    return request<{ findings: SecurityFinding[]; count: number; scanners: string[] }>(
      `/security/findings${suffix}`,
    );
  },

  /**
   * A POST, because a scan is not free: it walks a filesystem and reaches the
   * host agent seven times. A GET that did that would be re-run by every retry
   * and every refresh of the page.
   */
  scan: () => request<SecurityScan>('/security/scan', { method: 'POST' }),

  /**
   * Accepting records that a risk is known and deliberate, with a reason.
   *
   * There is deliberately no way to resolve a finding here. Whether a weakness
   * still exists is the scanner's to decide, and a panel where a person can
   * mark an open port as closed is one that will one day say an open port is
   * closed.
   */
  accept: (id: string, reason: string) =>
    request<SecurityFinding>(`/security/findings/${id}`, {
      method: 'PATCH',
      body: { status: 'accepted', reason },
    }),

  reopen: (id: string) =>
    request<SecurityFinding>(`/security/findings/${id}`, {
      method: 'PATCH',
      body: { status: 'open', reason: '' },
    }),

  history: (limit = 30, signal?: AbortSignal) =>
    request<{ scans: SecurityScan[]; count: number }>(
      `/security/history?limit=${limit}`,
      signal ? { signal } : {},
    ),
};
