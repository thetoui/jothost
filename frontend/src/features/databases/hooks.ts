import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import {
  databasesApi,
  type AddUserInput,
  type CreateDatabaseInput,
  type SetGrantInput,
  type SetPasswordInput,
} from '@/features/databases/api';
import { websiteKeys } from '@/features/websites/hooks';

/** Query keys are centralised so cache invalidation stays predictable. */
export const databaseKeys = {
  all: ['databases'] as const,
  list: () => [...databaseKeys.all, 'list'] as const,
  engines: () => [...databaseKeys.all, 'engines'] as const,
  detail: (id: string) => [...databaseKeys.all, 'detail', id] as const,
  users: () => [...databaseKeys.all, 'users'] as const,
};

/** useDatabases lists every managed database, newest first. */
export function useDatabases() {
  return useQuery({
    queryKey: databaseKeys.list(),
    queryFn: ({ signal }) => databasesApi.list(signal),
    placeholderData: (previous) => previous,
  });
}

/**
 * useDatabaseEngines reports what the host can run.
 *
 * Cached for the session: which database servers are installed does not change
 * between page views, and asking each time would put a socket round trip in
 * front of the create form.
 */
export function useDatabaseEngines() {
  return useQuery({
    queryKey: databaseKeys.engines(),
    queryFn: ({ signal }) => databasesApi.engines(signal),
    staleTime: 5 * 60_000,
  });
}

/** useDatabase reports one database and the accounts that may reach it. */
export function useDatabase(id: string | undefined) {
  return useQuery({
    queryKey: databaseKeys.detail(id ?? ''),
    queryFn: ({ signal }) => databasesApi.get(id as string, signal),
    enabled: Boolean(id),
  });
}

/** useDatabaseUsers lists every account with its grants. */
export function useDatabaseUsers() {
  return useQuery({
    queryKey: databaseKeys.users(),
    queryFn: ({ signal }) => databasesApi.users(signal),
  });
}

/** invalidate refreshes everything a database change can affect. */
function useInvalidate() {
  const queryClient = useQueryClient();

  return (databaseId?: string) => {
    void queryClient.invalidateQueries({ queryKey: databaseKeys.list() });
    void queryClient.invalidateQueries({ queryKey: databaseKeys.users() });
    if (databaseId) {
      void queryClient.invalidateQueries({ queryKey: databaseKeys.detail(databaseId) });
    }
    // A database can belong to a website, and that site's page counts them.
    void queryClient.invalidateQueries({ queryKey: websiteKeys.list() });
  };
}

/** useCreateDatabase provisions a database and, by default, its account. */
export function useCreateDatabase() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: CreateDatabaseInput) => databasesApi.create(input),
    onSuccess: () => invalidate(),
  });
}

/** useDeleteDatabase drops a database and forgets it. */
export function useDeleteDatabase() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => databasesApi.remove(id),
    onSuccess: () => invalidate(),
  });
}

/** useRefreshDatabaseSize re-measures one database on the host. */
export function useRefreshDatabaseSize() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (id: string) => databasesApi.refreshSize(id),
    onSuccess: (_result, id) => invalidate(id),
  });
}

/** useAddDatabaseUser creates an account and grants it access. */
export function useAddDatabaseUser() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: AddUserInput) => databasesApi.addUser(input),
    onSuccess: (_result, input) => invalidate(input.databaseId),
  });
}

/** useDeleteDatabaseUser removes an account from the server. */
export function useDeleteDatabaseUser() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (userId: string) => databasesApi.removeUser(userId),
    onSuccess: () => invalidate(),
  });
}

/** useSetDatabaseGrant changes or revokes an account's access. */
export function useSetDatabaseGrant() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: SetGrantInput) => databasesApi.setGrant(input),
    onSuccess: (_result, input) => invalidate(input.databaseId),
  });
}

/** useSetDatabasePassword rotates an account's password. */
export function useSetDatabasePassword() {
  const invalidate = useInvalidate();

  return useMutation({
    mutationFn: (input: SetPasswordInput) => databasesApi.setPassword(input),
    onSuccess: () => invalidate(),
  });
}

/**
 * useRevealDatabasePassword reads back a stored password.
 *
 * A mutation rather than a query, deliberately. A query would be cached, could
 * refetch on window focus, and would run the moment a component mounted — none
 * of which should ever be true of a request that returns a live credential and
 * writes an audit record. This way it happens exactly when somebody asks.
 */
export function useRevealDatabasePassword() {
  return useMutation({
    mutationFn: (userId: string) => databasesApi.revealPassword(userId),
  });
}
