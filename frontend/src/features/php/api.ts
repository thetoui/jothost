import { request } from '@/services/apiClient';
import type {
  JobAccepted,
  PHPConfig,
  PHPVersionList,
  WebsitePHP,
} from '@/types/api';

export interface PHPSettingsInput {
  memory_limit?: string;
  upload_max_filesize?: string;
  max_execution_time?: number;
  opcache?: boolean;
  max_children?: number;
}

export interface SetPHPInput extends PHPSettingsInput {
  websiteId: string;
  /** null turns PHP off and makes the site static again. */
  version: string | null;
}

/** PHP version and per-site pool API calls. */
export const phpApi = {
  versions: (signal?: AbortSignal) =>
    request<PHPVersionList>('/php/versions', signal ? { signal } : {}),

  install: (version: string) =>
    request<JobAccepted>('/php/versions/install', {
      method: 'POST',
      body: { version },
    }),

  uninstall: (version: string) =>
    request<JobAccepted>(`/php/versions/${encodeURIComponent(version)}`, {
      method: 'DELETE',
    }),

  websitePHP: (websiteId: string, signal?: AbortSignal) =>
    request<WebsitePHP>(
      `/websites/${encodeURIComponent(websiteId)}/php`,
      signal ? { signal } : {},
    ),

  setWebsitePHP: (input: SetPHPInput) => {
    const { websiteId, ...body } = input;
    return request<JobAccepted>(`/websites/${encodeURIComponent(websiteId)}/php`, {
      method: 'PATCH',
      body,
    });
  },

  config: (websiteId: string, signal?: AbortSignal) =>
    request<PHPConfig>(
      `/websites/${encodeURIComponent(websiteId)}/php/config`,
      signal ? { signal } : {},
    ),

  setConfig: (websiteId: string, settings: PHPSettingsInput) =>
    request<JobAccepted>(`/websites/${encodeURIComponent(websiteId)}/php/config`, {
      method: 'PATCH',
      body: settings,
    }),
};
