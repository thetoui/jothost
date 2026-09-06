import { request } from '@/services/apiClient';
import type {
  Alert,
  GrafanaState,
  AlertMetric,
  AlertRule,
  AlertRuleInput,
  MonitoringOverview,
  ServiceStateRecord,
} from '@/types/api';

/** Monitoring calls. */
export const monitoringApi = {
  overview: (signal?: AbortSignal) =>
    request<MonitoringOverview>('/monitoring', signal ? { signal } : {}),

  alerts: (status?: string, signal?: AbortSignal) =>
    request<{ alerts: Alert[]; count: number }>(
      status ? `/monitoring/alerts?status=${encodeURIComponent(status)}` : '/monitoring/alerts',
      signal ? { signal } : {},
    ),

  /**
   * Acknowledging says somebody has seen it. There is deliberately no call that
   * resolves an alert: whether a condition has cleared is a fact about the
   * machine, and the monitor decides it.
   */
  acknowledge: (id: string) =>
    request<Alert>(`/monitoring/alerts/${encodeURIComponent(id)}/acknowledge`, {
      method: 'POST',
    }),

  rules: (signal?: AbortSignal) =>
    request<{ rules: AlertRule[]; count: number; metrics: AlertMetric[] }>(
      '/monitoring/rules',
      signal ? { signal } : {},
    ),

  createRule: (body: AlertRuleInput) =>
    request<AlertRule>('/monitoring/rules', { method: 'POST', body }),

  updateRule: (id: string, body: AlertRuleInput) =>
    request<AlertRule>(`/monitoring/rules/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body,
    }),

  removeRule: (id: string) =>
    request<{ deleted: boolean }>(`/monitoring/rules/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  serviceHistory: (service: string, signal?: AbortSignal) =>
    request<{ states: ServiceStateRecord[]; count: number }>(
      `/monitoring/services/${encodeURIComponent(service)}`,
      signal ? { signal } : {},
    ),
};

/** The chart provider's state on the host. */
export const grafanaApi = {
  status: (signal?: AbortSignal) =>
    request<GrafanaState>('/monitoring/grafana', signal ? { signal } : {}),

  install: () => request<{ job_id: string }>('/monitoring/grafana', { method: 'POST' }),
};
