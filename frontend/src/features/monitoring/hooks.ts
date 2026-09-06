import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { grafanaApi, monitoringApi } from '@/features/monitoring/api';
import type { AlertRuleInput } from '@/types/api';

export const monitoringKeys = {
  all: ['monitoring'] as const,
  overview: () => [...monitoringKeys.all, 'overview'] as const,
  rules: () => [...monitoringKeys.all, 'rules'] as const,
  service: (name: string) => [...monitoringKeys.all, 'service', name] as const,
};

/**
 * useMonitoringOverview reads what is open and what is being watched.
 *
 * Polled, because an alert page that only refreshed on navigation would be
 * showing a host as it was several minutes ago — which for the one screen whose
 * job is to say "something is wrong now" is the wrong answer.
 */
export function useMonitoringOverview() {
  return useQuery({
    queryKey: monitoringKeys.overview(),
    queryFn: ({ signal }) => monitoringApi.overview(signal),
    refetchInterval: 30_000,
    placeholderData: (previous) => previous,
  });
}

/** useAlertRules reads the rules and the metrics that can be watched. */
export function useAlertRules() {
  return useQuery({
    queryKey: monitoringKeys.rules(),
    queryFn: ({ signal }) => monitoringApi.rules(signal),
  });
}

/** useAcknowledgeAlert records that somebody has seen an alert. */
export function useAcknowledgeAlert() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => monitoringApi.acknowledge(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: monitoringKeys.all });
    },
  });
}

/** useCreateAlertRule adds a rule. */
export function useCreateAlertRule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: AlertRuleInput) => monitoringApi.createRule(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: monitoringKeys.all });
      // The dashboard's thresholds come from these rules, so its alerts change
      // with them.
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

/** useUpdateAlertRule changes a rule. */
export function useUpdateAlertRule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: AlertRuleInput }) =>
      monitoringApi.updateRule(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: monitoringKeys.all });
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

/** useDeleteAlertRule removes a rule. */
export function useDeleteAlertRule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => monitoringApi.removeRule(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: monitoringKeys.all });
      void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
    },
  });
}

/** useServiceHistory reads one service's state changes. */
export function useServiceHistory(service: string, enabled = true) {
  return useQuery({
    queryKey: monitoringKeys.service(service),
    queryFn: ({ signal }) => monitoringApi.serviceHistory(service, signal),
    enabled: enabled && Boolean(service),
  });
}

/** useGrafana reports whether the chart provider is usable. */
export function useGrafana() {
  return useQuery({
    queryKey: [...monitoringKeys.all, 'grafana'],
    queryFn: ({ signal }) => grafanaApi.status(signal),
    staleTime: 30_000,
  });
}

/**
 * useInstallGrafana puts Grafana on the host.
 *
 * The mutation returns a job id and the install carries on without it: this is
 * a package download measured in hundreds of megabytes, and a page that waited
 * for it would look hung.
 */
export function useInstallGrafana() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => grafanaApi.install(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: [...monitoringKeys.all, 'grafana'] });
      // The job list is what the page watches for the result.
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
    },
  });
}
