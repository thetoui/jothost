import { request } from '@/services/apiClient';
import type {
  FirewallPending,
  FirewallRuleInput,
  FirewallStatus,
} from '@/types/api';

/** Firewall calls. Every change comes back provisional and must be confirmed. */
export const firewallApi = {
  status: (signal?: AbortSignal) =>
    request<FirewallStatus>('/firewall', signal ? { signal } : {}),

  addRule: (rule: FirewallRuleInput) =>
    request<FirewallPending>('/firewall/rules', { method: 'POST', body: rule }),

  deleteRule: (rule: FirewallRuleInput) =>
    request<FirewallPending>('/firewall/rules', { method: 'DELETE', body: rule }),

  enable: () => request<FirewallPending>('/firewall/enable', { method: 'POST' }),

  disable: () => request<FirewallPending>('/firewall/disable', { method: 'POST' }),

  confirm: (id: string) =>
    request<FirewallPending>(`/firewall/changes/${encodeURIComponent(id)}/confirm`, {
      method: 'POST',
    }),

  rollback: (id: string) =>
    request<FirewallPending>(`/firewall/changes/${encodeURIComponent(id)}/rollback`, {
      method: 'POST',
    }),
};
