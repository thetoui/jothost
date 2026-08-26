import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import {
  jobsApi,
  websitesApi,
  type AddDomainInput,
  type CreateWebsiteInput,
} from '@/features/websites/api';
import type { Job, Website } from '@/types/api';

/** Query keys are centralised so cache invalidation stays predictable. */
export const websiteKeys = {
  all: ['websites'] as const,
  list: () => [...websiteKeys.all, 'list'] as const,
  detail: (id: string) => [...websiteKeys.all, 'detail', id] as const,
  domains: (id: string) => [...websiteKeys.all, 'domains', id] as const,
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

/** useDeleteWebsite queues removal of a site. */
export function useDeleteWebsite() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => websitesApi.remove(id),
    onSuccess: (_result, id) => {
      void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(id) });
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
