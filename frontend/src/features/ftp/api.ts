import { request } from '@/services/apiClient';
import type {
  FTPCreateResult,
  FTPOverview,
  FTPSession,
  FTPSettings,
  FTPSettingsChange,
  FTPUser,
  FTPUserChange,
} from '@/types/api';

/** FTP calls. */
export const ftpApi = {
  overview: (signal?: AbortSignal) => request<FTPOverview>('/ftp', signal ? { signal } : {}),

  install: () => request<FTPOverview>('/ftp/install', { method: 'POST' }),

  sessions: (signal?: AbortSignal) =>
    request<{ sessions: FTPSession[]; count: number }>('/ftp/sessions', signal ? { signal } : {}),

  disconnect: (pid: number) =>
    request<{ pid: number; disconnected: boolean }>(`/ftp/sessions/${pid}`, { method: 'DELETE' }),

  forWebsite: (websiteId: string, signal?: AbortSignal) =>
    request<{ users: FTPUser[]; count: number }>(
      `/websites/${encodeURIComponent(websiteId)}/ftp`,
      signal ? { signal } : {},
    ),

  create: (body: {
    website_id: string;
    username: string;
    password?: string;
    home_subpath?: string;
    access_level?: string;
    quota_mb?: number;
  }) => request<FTPCreateResult>('/ftp/users', { method: 'POST', body }),

  update: (id: string, change: FTPUserChange) =>
    request<FTPUser>(`/ftp/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: change }),

  remove: (id: string) =>
    request<{ deleted: boolean }>(`/ftp/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  saveSettings: (change: FTPSettingsChange) =>
    request<FTPSettings>('/ftp/settings', { method: 'PUT', body: change }),
};
