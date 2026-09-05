import { request } from '@/services/apiClient';
import type {
  ImpersonationRecord,
  ServicePlan,
  ServicePlanInput,
  Subscription,
  TenantAccount,
  TenantAccountInput,
  TenancyOverview,
} from '@/types/api';

/** Tenancy calls: accounts, plans, subscriptions, and impersonation. */
export const tenancyApi = {
  overview: (signal?: AbortSignal) =>
    request<TenancyOverview>('/tenancy/overview', signal ? { signal } : {}),

  createAccount: (body: TenantAccountInput) =>
    request<TenantAccount>('/tenancy/accounts', { method: 'POST', body }),

  updateAccount: (id: string, body: { status?: string; full_name?: string }) =>
    request<TenantAccount>(`/tenancy/accounts/${id}`, { method: 'PATCH', body }),

  deleteAccount: (id: string) =>
    request<{ deleted: boolean }>(`/tenancy/accounts/${id}`, { method: 'DELETE' }),

  createPlan: (body: ServicePlanInput) =>
    request<ServicePlan>('/tenancy/plans', { method: 'POST', body }),

  updatePlan: (id: string, body: ServicePlanInput) =>
    request<ServicePlan>(`/tenancy/plans/${id}`, { method: 'PUT', body }),

  deletePlan: (id: string) =>
    request<{ deleted: boolean }>(`/tenancy/plans/${id}`, { method: 'DELETE' }),

  createSubscription: (body: { owner_user_id: string; plan_id: string; name: string }) =>
    request<Subscription>('/tenancy/subscriptions', { method: 'POST', body }),

  subscription: (id: string, signal?: AbortSignal) =>
    request<Subscription>(`/tenancy/subscriptions/${id}`, signal ? { signal } : {}),

  updateSubscription: (id: string, body: { name?: string; plan_id?: string }) =>
    request<Subscription>(`/tenancy/subscriptions/${id}`, { method: 'PATCH', body }),

  deleteSubscription: (id: string) =>
    request<{ deleted: boolean }>(`/tenancy/subscriptions/${id}`, { method: 'DELETE' }),

  setStatus: (id: string, status: 'active' | 'suspended', reason: string) =>
    request<Subscription>(`/tenancy/subscriptions/${id}/status`, {
      method: 'POST',
      body: { status, reason },
    }),

  addAddon: (id: string, planId: string, quantity: number) =>
    request<Subscription>(`/tenancy/subscriptions/${id}/addons`, {
      method: 'POST',
      body: { plan_id: planId, quantity },
    }),

  removeAddon: (id: string, planId: string) =>
    request<Subscription>(`/tenancy/subscriptions/${id}/addons/${planId}`, {
      method: 'DELETE',
    }),

  /** Measures disk and bandwidth now, rather than waiting for the sampler. */
  measure: (id: string) =>
    request<Subscription>(`/tenancy/subscriptions/${id}/measure`, { method: 'POST' }),

  applyIsolation: (id: string) =>
    request<Subscription>(`/tenancy/subscriptions/${id}/isolation`, { method: 'POST' }),

  /**
   * Signs in as another account.
   *
   * The reply carries a token pair for that account. Swapping the stored
   * session for it is what makes the rest of the panel act as them, and
   * `endImpersonation` is what puts it back.
   */
  impersonate: (userId: string, reason: string) =>
    request<{
      subject: string;
      tokens: { access_token: string; refresh_token: string; expires_in: number };
    }>('/tenancy/impersonation', { method: 'POST', body: { user_id: userId, reason } }),

  currentImpersonation: (signal?: AbortSignal) =>
    request<{ impersonation: ImpersonationRecord | null }>(
      '/tenancy/impersonation',
      signal ? { signal } : {},
    ),

  endImpersonation: () =>
    request<{ ended: boolean }>('/tenancy/impersonation', { method: 'DELETE' }),

  impersonationHistory: (signal?: AbortSignal) =>
    request<{ impersonations: ImpersonationRecord[] }>(
      '/tenancy/impersonation/history',
      signal ? { signal } : {},
    ),
};
