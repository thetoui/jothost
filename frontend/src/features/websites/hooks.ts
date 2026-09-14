import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import {
  jobsApi,
  websitesApi,
  type AddDomainInput,
  type CreateSubdomainInput,
  type CreateWebsiteInput,
} from '@/features/websites/api';
import type { Job, Website } from '@/types/api';

/** Query keys are centralised so cache invalidation stays predictable. */
export const websiteKeys = {
  all: ['websites'] as const,
  list: () => [...websiteKeys.all, 'list'] as const,
  detail: (id: string) => [...websiteKeys.all, 'detail', id] as const,
  domains: (id: string) => [...websiteKeys.all, 'domains', id] as const,
  subdomains: (id: string) => [...websiteKeys.all, 'subdomains', id] as const,
  jobs: (id: string) => [...websiteKeys.all, 'jobs', id] as const,
};

export const jobKeys = {
  all: ['jobs'] as const,
  detail: (id: string) => [...jobKeys.all, 'detail', id] as const,
};

/**
 * How often a site with work in flight is re-read.
 *
 * Provisioning takes a few seconds, so this is fast enough that the list stops
 * saying "creating" promptly, and slow enough not to hammer the API. Sites
 * with nothing pending are not polled at all.
 */
const WORK_IN_FLIGHT_MS = 2_000;

/** True while a site is mid-change and the server view will move on its own. */
function isSettling(status: Website['status']): boolean {
  return status === 'creating' || status === 'deleting';
}

/** useWebsites lists hosted sites, polling only while something is changing. */
export function useWebsites() {
  return useQuery({
    queryKey: websiteKeys.list(),
    queryFn: ({ signal }) => websitesApi.list(signal),
    refetchInterval: (query) => {
      const websites = query.state.data?.websites ?? [];
      return websites.some((site) => isSettling(site.status)) ? WORK_IN_FLIGHT_MS : false;
    },
    placeholderData: (previous) => previous,
  });
}

/** useWebsite loads one site, polling while it is being provisioned. */
export function useWebsite(id: string | undefined) {
  return useQuery({
    queryKey: websiteKeys.detail(id ?? ''),
    queryFn: ({ signal }) => websitesApi.get(id as string, signal),
    enabled: Boolean(id),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status && isSettling(status) ? WORK_IN_FLIGHT_MS : false;
    },
    placeholderData: (previous) => previous,
  });
}

/** useWebsiteJobs lists the work done to one site, newest first. */
export function useWebsiteJobs(id: string | undefined) {
  return useQuery({
    queryKey: websiteKeys.jobs(id ?? ''),
    queryFn: ({ signal }) => jobsApi.forWebsite(id as string, signal),
    enabled: Boolean(id),
    refetchInterval: (query) => {
      const jobs = query.state.data?.jobs ?? [];
      return jobs.some(isJobRunning) ? WORK_IN_FLIGHT_MS : false;
    },
  });
}

/** isJobRunning reports whether a job is still going to change. */
export function isJobRunning(job: Job): boolean {
  return job.status === 'PENDING' || job.status === 'RUNNING';
}

/**
 * useCreateWebsite queues a new site.
 *
 * The list is invalidated on success so the new site appears immediately in
 * "creating"; its own polling then follows it to active or failed.
 */
export function useCreateWebsite() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: CreateWebsiteInput) => websitesApi.create(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
    },
  });
}

/**
 * useDeleteWebsite queues removal of a site.
 *
 * `removeFiles` is a separate decision from deleting the record, because the
 * two are separate on the host: the panel keeps a deleted site's content by
 * default. Keeping it has a consequence worth surfacing rather than burying —
 * the directory stays, and a later site cannot be created on the same document
 * root until it is cleared.
 */
export function useDeleteWebsite() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, removeFiles }: { id: string; removeFiles: boolean }) =>
      websitesApi.remove(id, removeFiles),
    onSuccess: (_result, { id }) => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(id) });
    },
  });
}

/**
 * useSetNginxDirectives replaces a site's additional nginx configuration.
 *
 * It queues a vhost rewrite on the host, so the site's jobs are invalidated
 * along with the record: the change is not applied until that job succeeds,
 * and the page should be watching it rather than reporting the setting saved.
 */
export function useSetNginxDirectives(websiteId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (directives: string) =>
      websitesApi.setNginxDirectives(websiteId, directives),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.jobs(websiteId) });
    },
  });
}

/** useAddDomain attaches a hostname to a site. */
export function useAddDomain() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: AddDomainInput) => websitesApi.addDomain(input),
    onSuccess: (_result, input) => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(input.websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.domains(input.websiteId) });
    },
  });
}

/** useRemoveDomain detaches a hostname from a site. */
export function useRemoveDomain(websiteId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (domainId: string) => websitesApi.removeDomain(domainId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.domains(websiteId) });
    },
  });
}

/**
 * useSetDomainRoot points one of a site's names at a directory of its own.
 *
 * The site's detail is invalidated as well as its domains: the vhost is being
 * rewritten, so the site goes back through a job and its status changes.
 */
export function useSetDomainRoot(websiteId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ domainId, documentRoot }: { domainId: string; documentRoot: string }) =>
      websitesApi.setDomainRoot(domainId, documentRoot),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.domains(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
    },
  });
}

/** useSubdomains lists the sites beneath a website. */
export function useSubdomains(websiteId: string | undefined) {
  return useQuery({
    queryKey: websiteKeys.subdomains(websiteId ?? ''),
    queryFn: ({ signal }) => websitesApi.subdomains(websiteId as string, signal),
    enabled: Boolean(websiteId),
    refetchInterval: (query) => {
      const subdomains = query.state.data?.subdomains ?? [];
      return subdomains.some((site) => isSettling(site.status)) ? WORK_IN_FLIGHT_MS : false;
    },
    placeholderData: (previous) => previous,
  });
}

/** useCreateSubdomain adds a site beneath a website. */
export function useCreateSubdomain() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: CreateSubdomainInput) => websitesApi.createSubdomain(input),
    onSuccess: (_result, input) => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.subdomains(input.parentId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(input.parentId) });
      // A subdomain is a website, so the sites listing can show it too.
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
    },
  });
}

/** useDeleteSubdomain removes a site beneath a website. */
export function useDeleteSubdomain(parentId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => websitesApi.removeSubdomain(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.subdomains(parentId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(parentId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
    },
  });
}

/**
 * useSetAllowOverride turns .htaccess on or off for one site.
 *
 * It rewrites the site's Apache configuration, so the site's own caches are
 * invalidated: the detail page would otherwise show the setting it had a
 * moment ago while the host is being reconfigured.
 */
export function useSetAllowOverride(websiteId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (allow: boolean) => websitesApi.setAllowOverride(websiteId, allow),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
    },
  });
}

/**
 * useSetDocumentRoot moves where a site is served from.
 *
 * Both the list and the detail are invalidated, and for a reason worth naming:
 * moving the document root rewrites the vhost on the host, so what the panel
 * shows until the refresh lands is the directory the site was served from a
 * moment ago.
 */
export function useSetDocumentRoot(websiteId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (relative: string) => websitesApi.setDocumentRoot(websiteId, relative),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
    },
  });
}
