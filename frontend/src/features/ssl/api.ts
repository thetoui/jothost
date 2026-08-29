import { request } from '@/services/apiClient';
import type {
  JobAccepted,
  SSLCertificateList,
  SSLProvider,
  SSLProviders,
  WebsiteSSL,
} from '@/types/api';

export interface IssueInput {
  websiteId: string;
  provider: SSLProvider;
  email?: string;
  /** Uses Let's Encrypt staging, which does not consume production rate limits. */
  staging?: boolean;
  https_redirect?: boolean;
  auto_renew?: boolean;
}

export interface ConfigureInput {
  websiteId: string;
  auto_renew?: boolean;
  https_redirect?: boolean;
}

/** Certificate API calls. */
export const sslApi = {
  list: (signal?: AbortSignal) =>
    request<SSLCertificateList>('/ssl', signal ? { signal } : {}),

  providers: (signal?: AbortSignal) =>
    request<SSLProviders>('/ssl/providers', signal ? { signal } : {}),

  forWebsite: (websiteId: string, signal?: AbortSignal) =>
    request<WebsiteSSL>(
      `/websites/${encodeURIComponent(websiteId)}/ssl`,
      signal ? { signal } : {},
    ),

  issue: ({ websiteId, ...body }: IssueInput) =>
    request<JobAccepted>(`/websites/${encodeURIComponent(websiteId)}/ssl/issue`, {
      method: 'POST',
      body,
    }),

  renew: (websiteId: string) =>
    request<JobAccepted>(`/websites/${encodeURIComponent(websiteId)}/ssl/renew`, {
      method: 'POST',
    }),

  revoke: (websiteId: string) =>
    request<JobAccepted>(`/websites/${encodeURIComponent(websiteId)}/ssl/revoke`, {
      method: 'POST',
    }),

  configure: ({ websiteId, ...body }: ConfigureInput) =>
    request<{ job?: unknown; updated?: boolean }>(
      `/websites/${encodeURIComponent(websiteId)}/ssl`,
      { method: 'PATCH', body },
    ),
};
