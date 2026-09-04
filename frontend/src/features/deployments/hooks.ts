import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { deploymentsApi } from '@/features/deployments/api';
import type { DeployActionKind, GitRepositoryInput } from '@/types/api';

export const deploymentKeys = {
  all: ['deployments'] as const,
  overview: () => [...deploymentKeys.all, 'overview'] as const,
  detail: (id: string) => [...deploymentKeys.all, 'detail', id] as const,
  log: (id: string) => [...deploymentKeys.all, 'log', id] as const,
};

/**
 * useDeployments reads every repository and what the host says about it.
 *
 * It polls while anything is deploying, and only then. A deployment takes
 * minutes and a page that needed reloading to notice it had finished would be a
 * page people reload — which is also the page that never shows the failure.
 */
export function useDeployments() {
  return useQuery({
    queryKey: deploymentKeys.overview(),
    queryFn: ({ signal }) => deploymentsApi.overview(signal),
    placeholderData: (previous) => previous,
    refetchInterval: (query) =>
      anythingRunning(query.state.data?.repositories) ? 3000 : false,
  });
}

/** useRepository reads one repository, with the host's own view of it. */
export function useRepository(id: string | undefined) {
  return useQuery({
    queryKey: deploymentKeys.detail(id ?? ''),
    queryFn: ({ signal }) => deploymentsApi.detail(id ?? '', signal),
    enabled: Boolean(id),
    refetchInterval: (query) =>
      (query.state.data?.recent ?? []).some(
        (deployment) => deployment.status === 'running' || deployment.status === 'pending',
      )
        ? 3000
        : false,
  });
}

/** useDeploymentLog reads what a build printed. */
export function useDeploymentLog(id: string | undefined) {
  return useQuery({
    queryKey: deploymentKeys.log(id ?? ''),
    queryFn: ({ signal }) => deploymentsApi.log(id ?? '', signal),
    enabled: Boolean(id),
    // While it is running the log grows, so it is re-read; once it has
    // finished it never changes again.
    refetchInterval: (query) =>
      query.state.data?.status === 'running' || query.state.data?.status === 'pending'
        ? 2000
        : false,
  });
}

function anythingRunning(repositories: { recent?: { status: string }[] | null }[] | undefined) {
  return (repositories ?? []).some((repository) =>
    (repository.recent ?? []).some(
      (deployment) => deployment.status === 'running' || deployment.status === 'pending',
    ),
  );
}

function useInvalidate() {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({ queryKey: deploymentKeys.all });
  };
}

/** useConfigureRepository records where a website's source comes from. */
export function useConfigureRepository() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (body: GitRepositoryInput) => deploymentsApi.configure(body),
    onSuccess: invalidate,
  });
}

/** useRemoveRepository disconnects a website from its repository. */
export function useRemoveRepository() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (id: string) => deploymentsApi.remove(id),
    onSuccess: invalidate,
  });
}

/** useSetActions replaces a repository's steps. */
export function useSetActions() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ id, steps }: { id: string; steps: DeployActionKind[] }) =>
      deploymentsApi.setActions(id, steps),
    onSuccess: invalidate,
  });
}

/** useGenerateDeployKey asks the host for a key and records its public half. */
export function useGenerateDeployKey() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (id: string) => deploymentsApi.generateKey(id),
    onSuccess: invalidate,
  });
}

/** useDeploy queues a deployment. */
export function useDeploy() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ id, commit }: { id: string; commit?: string }) =>
      deploymentsApi.deploy(id, commit),
    onSuccess: invalidate,
  });
}

/** useRollback deploys the commit a website was on before. */
export function useRollback() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: ({ id, commit }: { id: string; commit: string }) =>
      deploymentsApi.rollback(id, commit),
    onSuccess: invalidate,
  });
}
