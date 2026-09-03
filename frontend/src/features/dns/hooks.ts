import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { dnsApi, type DNSZoneInput } from '@/features/dns/api';
import type { DNSRecordInput, DNSSettings, DNSZoneChange } from '@/types/api';

export const dnsKeys = {
  all: ['dns'] as const,
  overview: () => [...dnsKeys.all, 'overview'] as const,
  zone: (id: string) => [...dnsKeys.all, 'zone', id] as const,
  forWebsite: (websiteId: string) => [...dnsKeys.all, 'website', websiteId] as const,
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
