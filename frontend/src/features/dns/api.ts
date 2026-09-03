import { request } from '@/services/apiClient';
import type {
  DNSOverview,
  DNSProvider,
  DNSRecord,
  DNSRecordInput,
  DNSSettings,
  DNSSyncResult,
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

  sync: (zoneId: string, body: { provider_id: string; prune?: boolean }) =>
    request<DNSSyncResult>(`/dns/zones/${encodeURIComponent(zoneId)}/sync`, {
      method: 'POST',
      body,
    }),
};
