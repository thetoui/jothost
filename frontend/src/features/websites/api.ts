import { request } from '@/services/apiClient';
import type {
  DocumentRootMode,
  DomainCreated,
  DomainList,
  DomainType,
  Job,
  JobAccepted,
  JobList,
  PHPPoolMode,
  SubdomainList,
  SystemUserMode,
  Website,
  WebsiteCreated,
  WebsiteList,
} from '@/types/api';

export interface CreateWebsiteInput {
  domain: string;
  name?: string;
}

export interface CreateSubdomainInput {
  parentId: string;
  /**
   * The label beneath the parent, not a full hostname: "shop", "dev.shop", or
   * "*". The API derives the full name from the parent's own domain.
   */
  name: string;
  documentRootMode: DocumentRootMode;
  phpPoolMode: PHPPoolMode;
  systemUserMode: SystemUserMode;
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

  // removeFiles deletes the site's content along with it. Off by default,
  // matching the API: a vhost can be recreated and content cannot.
  remove: (id: string, removeFiles = false) =>
    request<JobAccepted>(
      `/websites/${encodeURIComponent(id)}${removeFiles ? '?remove_files=true' : ''}`,
      { method: 'DELETE' },
    ),

  subdomains: (websiteId: string, signal?: AbortSignal) =>
    request<SubdomainList>(
      `/websites/${encodeURIComponent(websiteId)}/subdomains`,
      signal ? { signal } : {},
    ),

  createSubdomain: (input: CreateSubdomainInput) =>
    request<WebsiteCreated>(`/websites/${encodeURIComponent(input.parentId)}/subdomains`, {
      method: 'POST',
      body: {
        name: input.name,
        document_root_mode: input.documentRootMode,
        php_pool_mode: input.phpPoolMode,
        system_user_mode: input.systemUserMode,
      },
    }),

  removeSubdomain: (id: string, removeFiles = false) =>
    request<JobAccepted>(
      `/subdomains/${encodeURIComponent(id)}${removeFiles ? '?remove_files=true' : ''}`,
      { method: 'DELETE' },
    ),

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

  /** Turns .htaccess on or off for a site, which rewrites its Apache vhost. */
  setAllowOverride: (websiteId: string, allow: boolean) =>
    request<Website>(`/websites/${encodeURIComponent(websiteId)}`, {
      method: 'PATCH',
      body: { allow_override: allow },
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
