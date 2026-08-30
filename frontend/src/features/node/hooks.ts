import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { nodeApi, type CreateAppInput, type SetEnvInput } from '@/features/node/api';
import { websiteKeys } from '@/features/websites/hooks';
import type { NodeApp } from '@/types/api';

/** Query keys are centralised so cache invalidation stays predictable. */
export const nodeKeys = {
  all: ['node'] as const,
  versions: () => [...nodeKeys.all, 'versions'] as const,
  list: () => [...nodeKeys.all, 'list'] as const,
  detail: (id: string) => [...nodeKeys.all, 'detail', id] as const,
  logs: (id: string) => [...nodeKeys.all, 'logs', id] as const,
};

/**
 * How often a running application is re-read.
 *
 * A process can exit at any moment, so "running" is a claim with a shelf life.
 * Ten seconds is often enough to notice without making the page chatty.
 */
const RUNNING_POLL_MS = 10_000;

/**
 * useNodeVersions reports what the host runs and could run.
 *
 * Cached for the session: which runtime is installed does not change between
 * page views, and asking each time would put a socket round trip in front of
 * every visit.
 */
export function useNodeVersions() {
  return useQuery({
    queryKey: nodeKeys.versions(),
    queryFn: ({ signal }) => nodeApi.versions(signal),
    staleTime: 5 * 60_000,
  });
}

/** useNodeApps lists every application. */
export function useNodeApps() {
  return useQuery({
    queryKey: nodeKeys.list(),
    queryFn: ({ signal }) => nodeApi.list(signal),
    placeholderData: (previous) => previous,
    refetchInterval: (query) => {
      const apps = query.state.data?.applications ?? [];
      return apps.some(isSettling) ? RUNNING_POLL_MS : false;
    },
  });
}

function isSettling(app: NodeApp): boolean {
  return app.status === 'running' || app.status === 'starting';
}

/** useNodeApp reports one application with what the host says about it. */
export function useNodeApp(id: string | undefined) {
  return useQuery({
    queryKey: nodeKeys.detail(id ?? ''),
    queryFn: ({ signal }) => nodeApi.get(id as string, signal),
    enabled: Boolean(id),
  });
}

/** useNodeLogs reads the tail of an application's output. */
export function useNodeLogs(id: string | undefined, lines = 200) {
  return useQuery({
    queryKey: [...nodeKeys.logs(id ?? ''), lines],
    queryFn: ({ signal }) => nodeApi.logs(id as string, lines, signal),
    enabled: Boolean(id),
  });
}

/** invalidate refreshes everything an application change can affect. */
function useInvalidate() {
  const queryClient = useQueryClient();

  return (id?: string) => {
    void queryClient.invalidateQueries({ queryKey: nodeKeys.list() });
    if (id) {
      void queryClient.invalidateQueries({ queryKey: nodeKeys.detail(id) });
      void queryClient.invalidateQueries({ queryKey: nodeKeys.logs(id) });
    }
    // Starting an application rewrites the site's vhost, so the website's own
    // row is no longer what the panel last showed.
    void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
  };
}

/** useCreateNodeApp prepares an application on the host. */
export function useCreateNodeApp() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: CreateAppInput) => nodeApi.create(input),
    onSuccess: () => invalidate(),
  });
}

/** useDeleteNodeApp removes an application from the host and the panel. */
export function useDeleteNodeApp() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => nodeApi.remove(id),
    onSuccess: () => invalidate(),
  });
}

/** useStartNodeApp runs an application and points its website at it. */
export function useStartNodeApp() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => nodeApi.start(id),
    onSuccess: (_result, id) => invalidate(id),
  });
}

/** useStopNodeApp ends an application and returns its website to files. */
export function useStopNodeApp() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => nodeApi.stop(id),
    onSuccess: (_result, id) => invalidate(id),
  });
}

/** useRestartNodeApp restarts an application. */
export function useRestartNodeApp() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => nodeApi.restart(id),
    onSuccess: (_result, id) => invalidate(id),
  });
}

/** useInstallDependencies runs npm install for an application. */
export function useInstallDependencies() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => nodeApi.installDependencies(id),
    onSuccess: (_result, id) => invalidate(id),
  });
}

/** useSetNodeEnv adds or changes one environment variable. */
export function useSetNodeEnv() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: SetEnvInput) => nodeApi.setEnv(input),
    onSuccess: (_result, input) => invalidate(input.id),
  });
}

/** useRemoveNodeEnv deletes one environment variable. */
export function useRemoveNodeEnv() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: ({ id, key }: { id: string; key: string }) => nodeApi.removeEnv(id, key),
    onSuccess: (_result, input) => invalidate(input.id),
  });
}

/**
 * useRevealNodeEnv reads back the environment's values.
 *
 * A mutation rather than a query, deliberately: a query would be cached, could
 * refetch on window focus, and would run the moment a component mounted — none
 * of which should be true of a request that returns credentials and writes an
 * audit record.
 */
export function useRevealNodeEnv() {
  return useMutation({
    mutationFn: (id: string) => nodeApi.revealEnv(id),
  });
}
