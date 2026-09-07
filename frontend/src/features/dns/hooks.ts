import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { dnsApi, type DNSZoneInput } from '@/features/dns/api';
import type { DNSRecordInput, DNSSettings, DNSTemplateInput, DNSZoneChange } from '@/types/api';

export const dnsKeys = {
  all: ['dns'] as const,
  overview: () => [...dnsKeys.all, 'overview'] as const,
  zone: (id: string) => [...dnsKeys.all, 'zone', id] as const,
  forWebsite: (websiteId: string) => [...dnsKeys.all, 'website', websiteId] as const,
  templates: () => [...dnsKeys.all, 'templates'] as const,
};

/** useDNSOverview reads the name server, its zones and its settings. */
export function useDNSOverview() {
  return useQuery({
    queryKey: dnsKeys.overview(),
    queryFn: ({ signal }) => dnsApi.overview(signal),
    placeholderData: (previous) => previous,
  });
}

/** useDNSZone reads one zone with its records and what the host says about it. */
export function useDNSZone(id: string, enabled = true) {
  return useQuery({
    queryKey: dnsKeys.zone(id),
    queryFn: ({ signal }) => dnsApi.zone(id, signal),
    enabled: enabled && Boolean(id),
  });
}

/** useWebsiteDNSZones reads one website's zones, for its own tab. */
export function useWebsiteDNSZones(websiteId: string, enabled = true) {
  return useQuery({
    queryKey: dnsKeys.forWebsite(websiteId),
    queryFn: ({ signal }) => dnsApi.forWebsite(websiteId, signal),
    enabled: enabled && Boolean(websiteId),
  });
}

/** useInstallDNS puts a name server on the host. */
export function useInstallDNS() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => dnsApi.install(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
      // The Services page lists the daemon too, and it has just appeared there.
      void queryClient.invalidateQueries({ queryKey: ['services'] });
    },
  });
}

/** useSaveDNSSettings writes the name server's settings. */
export function useSaveDNSSettings() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (settings: Partial<DNSSettings>) => dnsApi.saveSettings(settings),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useCreateDNSZone adds a zone. */
export function useCreateDNSZone() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: DNSZoneInput) => dnsApi.createZone(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useUpdateDNSZone changes a zone's settings. */
export function useUpdateDNSZone(zoneId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (change: DNSZoneChange) => dnsApi.updateZone(zoneId, change),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useDeleteDNSZone stops serving a zone. */
export function useDeleteDNSZone() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => dnsApi.removeZone(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useCreateDNSRecord adds a record to a zone. */
export function useCreateDNSRecord(zoneId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: DNSRecordInput) => dnsApi.createRecord(zoneId, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useUpdateDNSRecord replaces a record's contents. */
export function useUpdateDNSRecord() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: DNSRecordInput }) =>
      dnsApi.updateRecord(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useDeleteDNSRecord removes a record. */
export function useDeleteDNSRecord() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => dnsApi.removeRecord(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useAddDNSProvider records a remote provider's credentials. */
export function useAddDNSProvider() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: { kind: string; label?: string; token: string; account_id?: string }) =>
      dnsApi.addProvider(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.overview() });
    },
  });
}

/** useRemoveDNSProvider forgets a remote provider. */
export function useRemoveDNSProvider() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => dnsApi.removeProvider(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.overview() });
    },
  });
}

/**
 * useSyncDNSZone publishes a zone to a remote provider.
 *
 * Not invalidated on success beyond the overview's sync timestamps: the push
 * changes the provider, not this panel's record of the zone.
 */
export function useSyncDNSZone(zoneId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: { provider_id: string; prune?: boolean }) => dnsApi.sync(zoneId, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.overview() });
    },
  });
}

/**
 * useImportDNSZone reads a provider's copy of a zone into the panel.
 *
 * The opposite direction from useSyncDNSZone, and a separate hook rather than
 * an option on it: a push and an import are opposite operations on the same
 * records, and one call that did either depending on an argument is how the
 * wrong argument eventually overwrites the side somebody meant to keep.
 */
export function useImportDNSZone(zoneId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: { provider_id: string; replace?: boolean }) =>
      dnsApi.importZone(zoneId, body),
    onSuccess: () => {
      // The zone's records have changed, so everything showing them is stale.
      void queryClient.invalidateQueries({ queryKey: dnsKeys.all });
    },
  });
}

/** useDNSTemplates reads the templates a new zone can be seeded from. */
export function useDNSTemplates() {
  return useQuery({
    queryKey: dnsKeys.templates(),
    queryFn: ({ signal }) => dnsApi.templates(signal),
  });
}

/**
 * useSaveDNSTemplate creates a template, or replaces one whole.
 *
 * One hook for both because the request is the same one: the records are the
 * entire list either way, so an edit that reorders or removes lines needs no
 * vocabulary of its own.
 */
export function useSaveDNSTemplate() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, input }: { id?: string; input: DNSTemplateInput }) =>
      id ? dnsApi.updateTemplate(id, input) : dnsApi.createTemplate(input),
    onSuccess: () => {
      // Not just the templates: making one the default clears the flag on
      // whichever one held it, so the whole list is stale.
      void queryClient.invalidateQueries({ queryKey: dnsKeys.templates() });
    },
  });
}

/** useDeleteDNSTemplate removes one. The built-in is refused by the server. */
export function useDeleteDNSTemplate() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => dnsApi.removeTemplate(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: dnsKeys.templates() });
    },
  });
}
