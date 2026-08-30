import { request } from '@/services/apiClient';
import type { ApacheStatus, WebserverMode, WebserverModeChanged, WebserverStatus } from '@/types/api';

/** Web server arrangement calls. */
export const webserverApi = {
  status: (signal?: AbortSignal) =>
    request<WebserverStatus>('/webserver', signal ? { signal } : {}),

  setMode: (mode: WebserverMode) =>
    request<WebserverModeChanged>('/webserver', {
      method: 'PUT',
      body: { mode },
    }),

  installApache: () =>
    request<ApacheStatus>('/webserver/apache/install', { method: 'POST' }),
};
