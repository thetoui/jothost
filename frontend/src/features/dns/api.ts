import { request } from '@/services/apiClient';
import type {
  DNSImportResult,
  DNSOverview,
  DNSProvider,
  DNSRecord,
  DNSRecordInput,
  DNSSettings,
  DNSSyncResult,
  DNSTemplate,
  DNSTemplateInput,
  DNSTemplateList,
  DNSZone,
  DNSZoneChange,
  DNSZoneDetail,
} from '@/types/api';

/** A zone to create. */
export interface DNSZoneInput {
  name?: string;
  kind?: 'master' | 'slave';
  /** Given instead of a name, this creates a reverse zone named after it. */
  reverse_network?: string;
  website_id?: string;
  primary_ns?: string;
  hostmaster?: string;
  nameservers?: string[];
  dnssec?: boolean;
  allow_transfer?: string[];
  also_notify?: string[];
  masters?: string[];
  seed_records?: boolean;
}

/** DNS calls. */
export const dnsApi = {
  overview: (signal?: AbortSignal) => request<DNSOverview>('/dns', signal ? { signal } : {}),

  install: () => request<DNSOverview>('/dns/install', { method: 'POST' }),

  /**
   * Rewrites the host's DNS configuration from the panel's record.
   *
   * The same reconcile a zone change runs. It answers the warnings the
   * overview raises — a named.conf not including the panel's zones, zones the
   * server has not loaded — which until now had no control attached at all.
   */
  repair: () => request<DNSOverview>('/dns/repair', { method: 'POST' }),

  saveSettings: (settings: Partial<DNSSettings>) =>
    request<DNSSettings>('/dns/settings', { method: 'PUT', body: settings }),

  zone: (id: string, signal?: AbortSignal) =>
    request<DNSZoneDetail>(`/dns/zones/${encodeURIComponent(id)}`, signal ? { signal } : {}),

  createZone: (body: DNSZoneInput) => request<DNSZone>('/dns/zones', { method: 'POST', body }),

  updateZone: (id: string, change: DNSZoneChange) =>
    request<DNSZone>(`/dns/zones/${encodeURIComponent(id)}`, { method: 'PATCH', body: change }),

  removeZone: (id: string) =>
    request<{ deleted: boolean }>(`/dns/zones/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  createRecord: (zoneId: string, body: DNSRecordInput) =>
    request<DNSRecord>(`/dns/zones/${encodeURIComponent(zoneId)}/records`, {
      method: 'POST',
      body,
    }),

  updateRecord: (id: string, body: DNSRecordInput) =>
    request<DNSRecord>(`/dns/records/${encodeURIComponent(id)}`, { method: 'PATCH', body }),

  removeRecord: (id: string) =>
    request<{ deleted: boolean }>(`/dns/records/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  forWebsite: (websiteId: string, signal?: AbortSignal) =>
    request<{ zones: DNSZone[]; count: number }>(
      `/websites/${encodeURIComponent(websiteId)}/dns`,
      signal ? { signal } : {},
    ),

  addProvider: (body: { kind: string; label?: string; token: string; account_id?: string }) =>
    request<DNSProvider>('/dns/providers', { method: 'POST', body }),

  removeProvider: (id: string) =>
    request<{ deleted: boolean }>(`/dns/providers/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  importZone: (zoneId: string, body: { provider_id: string; replace?: boolean }) =>
    request<DNSImportResult>(`/dns/zones/${encodeURIComponent(zoneId)}/import`, {
      method: 'POST',
      body,
    }),

  sync: (zoneId: string, body: { provider_id: string; prune?: boolean }) =>
    request<DNSSyncResult>(`/dns/zones/${encodeURIComponent(zoneId)}/sync`, {
      method: 'POST',
      body,
    }),

  templates: (signal?: AbortSignal) =>
    request<DNSTemplateList>('/dns/templates', signal ? { signal } : {}),

  createTemplate: (body: DNSTemplateInput) =>
    request<DNSTemplate>('/dns/templates', { method: 'POST', body }),

  /** Replaces a template whole, records included. */
  updateTemplate: (id: string, body: DNSTemplateInput) =>
    request<DNSTemplate>(`/dns/templates/${encodeURIComponent(id)}`, { method: 'PUT', body }),

  removeTemplate: (id: string) =>
    request<{ deleted: boolean }>(`/dns/templates/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
};
