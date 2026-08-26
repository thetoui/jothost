import { request } from '@/services/apiClient';
import type {
  DomainCreated,
  DomainList,
  DomainType,
  Job,
  JobAccepted,
  JobList,
  Website,
  WebsiteCreated,
  WebsiteList,
} from '@/types/api';

export interface CreateWebsiteInput {
  domain: string;
  name?: string;
}

export interface AddDomainInput {
  websiteId: string;
  domain: string;
  type: DomainType;
  redirectTo?: string;
}

/** Website, domain, and job API calls. */
export const websitesApi = {
  list: (signal?: AbortSignal) => request<WebsiteList>('/websites', signal ? { signal } : {}),

  get: (id: string, signal?: AbortSignal) =>
    request<Website>(`/websites/${encodeURIComponent(id)}`, signal ? { signal } : {}),

  create: (input: CreateWebsiteInput) =>
    request<WebsiteCreated>('/websites', {
      method: 'POST',
      // The API rejects unknown fields, so only what it declares is sent.
      body: { domain: input.domain, name: input.name ?? '' },
    }),

  remove: (id: string) =>
    request<JobAccepted>(`/websites/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  domains: (websiteId: string, signal?: AbortSignal) =>
    request<DomainList>(
      `/websites/${encodeURIComponent(websiteId)}/domains`,
      signal ? { signal } : {},
    ),

  addDomain: (input: AddDomainInput) =>
    request<DomainCreated>(`/websites/${encodeURIComponent(input.websiteId)}/domains`, {
      method: 'POST',
      body: {
        domain: input.domain,
        type: input.type,
        redirect_to: input.redirectTo ?? '',
      },
    }),

  removeDomain: (domainId: string) =>
    request<JobAccepted>(`/domains/${encodeURIComponent(domainId)}`, { method: 'DELETE' }),
};

/** Job API calls. Jobs are how the UI follows work happening on the host. */
export const jobsApi = {
  get: (id: string, signal?: AbortSignal) =>
    request<Job>(`/jobs/${encodeURIComponent(id)}`, signal ? { signal } : {}),

  forWebsite: (websiteId: string, signal?: AbortSignal) =>
    request<JobList>(
      `/jobs?resource_type=website&resource_id=${encodeURIComponent(websiteId)}`,
      signal ? { signal } : {},
    ),
};
