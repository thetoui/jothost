import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { sslApi, type ConfigureInput, type IssueInput } from '@/features/ssl/api';
import { websiteKeys } from '@/features/websites/hooks';
import type { SSLCertificate } from '@/types/api';

/** Query keys are centralised so cache invalidation stays predictable. */
export const sslKeys = {
  all: ['ssl'] as const,
  list: () => [...sslKeys.all, 'list'] as const,
  providers: () => [...sslKeys.all, 'providers'] as const,
  website: (id: string) => [...sslKeys.all, 'website', id] as const,
};

/**
 * How often a certificate mid-issuance is re-read.
 *
 * Self-signed issuance is instant; an ACME exchange takes tens of seconds. Two
 * seconds is responsive for the first and not wasteful for the second, and the
 * poll stops as soon as nothing is in flight.
 */
const ISSUING_POLL_MS = 2_000;

function isSettling(certificate: SSLCertificate): boolean {
  return certificate.status === 'issuing' || certificate.status === 'pending';
}

/** useCertificates lists every certificate, soonest to expire first. */
export function useCertificates() {
  return useQuery({
    queryKey: sslKeys.list(),
    queryFn: ({ signal }) => sslApi.list(signal),
    refetchInterval: (query) => {
      const certificates = query.state.data?.certificates ?? [];
      return certificates.some(isSettling) ? ISSUING_POLL_MS : false;
    },
    placeholderData: (previous) => previous,
  });
}

/**
 * useSSLProviders reports which providers this host can use.
 *
 * Cached for the session: whether certbot is installed does not change between
 * page views, and asking on every render would put a socket round trip behind
 * every visit to a website's page.
 */
export function useSSLProviders() {
  return useQuery({
    queryKey: sslKeys.providers(),
    queryFn: ({ signal }) => sslApi.providers(signal),
    staleTime: 5 * 60_000,
  });
}

/** useWebsiteSSL reports a site's certificate, if it has one. */
export function useWebsiteSSL(websiteId: string | undefined) {
  return useQuery({
    queryKey: sslKeys.website(websiteId ?? ''),
    queryFn: ({ signal }) => sslApi.forWebsite(websiteId as string, signal),
    enabled: Boolean(websiteId),
    refetchInterval: (query) => {
      const certificate = query.state.data?.certificate;
      return certificate && isSettling(certificate) ? ISSUING_POLL_MS : false;
    },
  });
}

/** invalidate refreshes everything a certificate change can affect. */
function useInvalidate() {
  const queryClient = useQueryClient();

  return (websiteId: string) => {
    void queryClient.invalidateQueries({ queryKey: sslKeys.website(websiteId) });
    void queryClient.invalidateQueries({ queryKey: sslKeys.list() });
    // The website's own row carries ssl_enabled and https_redirect, so a stale
    // detail page would contradict the certificate panel beside it.
    void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(websiteId) });
    void queryClient.invalidateQueries({ queryKey: websiteKeys.jobs(websiteId) });
    void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
  };
}

/** useIssueCertificate queues issuance for a website. */
export function useIssueCertificate() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: IssueInput) => sslApi.issue(input),
    onSuccess: (_result, input) => invalidate(input.websiteId),
  });
}

/** useRenewCertificate queues renewal. */
export function useRenewCertificate() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (websiteId: string) => sslApi.renew(websiteId),
    onSuccess: (_result, websiteId) => invalidate(websiteId),
  });
}

/** useRevokeCertificate queues withdrawal of a certificate. */
export function useRevokeCertificate() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (websiteId: string) => sslApi.revoke(websiteId),
    onSuccess: (_result, websiteId) => invalidate(websiteId),
  });
}

/** useConfigureSSL changes auto-renewal and the HTTPS redirect. */
export function useConfigureSSL() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: ConfigureInput) => sslApi.configure(input),
    onSuccess: (_result, input) => invalidate(input.websiteId),
  });
}
