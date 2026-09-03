import { request } from '@/services/apiClient';
import type {
  UpdateCheck,
  UpdateOverview,
  UpdateRun,
  UpdateSettings,
  UpdateSettingsChange,
} from '@/types/api';

/** System update calls. */
export const updatesApi = {
  overview: (signal?: AbortSignal) =>
    request<UpdateOverview>('/updates', signal ? { signal } : {}),

  /**
   * A POST, because checking is not free: it refreshes the host's package index
   * and reaches the network. A GET that did that would be re-run by every
   * retry and every refresh of the page.
   */
  check: () => request<UpdateCheck>('/updates/check', { method: 'POST' }),

  apply: (body: { packages?: string[]; security_only?: boolean }) =>
    request<UpdateRun>('/updates/apply', { method: 'POST', body }),

  revert: (body: { package: string; version: string }) =>
    request<UpdateRun>('/updates/revert', { method: 'POST', body }),

  saveSettings: (change: UpdateSettingsChange) =>
    request<UpdateSettings>('/updates/settings', { method: 'PUT', body: change }),

  history: (limit = 50, signal?: AbortSignal) =>
    request<{ runs: UpdateRun[]; count: number }>(
      `/updates/history?limit=${limit}`,
      signal ? { signal } : {},
    ),
};
