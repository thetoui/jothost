import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import {
  phpApi,
  type PHPSettingsInput,
  type SetPHPInput,
} from '@/features/php/api';
import { websiteKeys } from '@/features/websites/hooks';
import type { PHPVersion } from '@/types/api';

/** Query keys are centralised so cache invalidation stays predictable. */
export const phpKeys = {
  all: ['php'] as const,
  versions: () => [...phpKeys.all, 'versions'] as const,
  website: (id: string) => [...phpKeys.all, 'website', id] as const,
  config: (id: string) => [...phpKeys.all, 'config', id] as const,
};

/**
 * How often a version list with work in flight is re-read.
 *
 * Installing a PHP package takes minutes, not seconds, so this is slower than
 * the website poll: a faster one would be a request per second for the length
 * of a download without telling anyone anything new.
 */
const INSTALL_POLL_MS = 5_000;

/** True while a version is mid-install or mid-removal. */
function isSettling(version: PHPVersion): boolean {
  return version.status === 'installing' || version.status === 'removing';
}

/** usePHPVersions lists the versions this server has. */
export function usePHPVersions() {
  return useQuery({
    queryKey: phpKeys.versions(),
    queryFn: ({ signal }) => phpApi.versions(signal),
    refetchInterval: (query) => {
      const versions = query.state.data?.versions ?? [];
      return versions.some(isSettling) ? INSTALL_POLL_MS : false;
    },
    placeholderData: (previous) => previous,
  });
}

/** useWebsitePHP reports whether a site runs PHP, and on what. */
export function useWebsitePHP(websiteId: string | undefined) {
  return useQuery({
    queryKey: phpKeys.website(websiteId ?? ''),
    queryFn: ({ signal }) => phpApi.websitePHP(websiteId as string, signal),
    enabled: Boolean(websiteId),
  });
}

/** useInstallPHP queues installation of a version. */
export function useInstallPHP() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (version: string) => phpApi.install(version),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: phpKeys.versions() });
    },
  });
}

/** useUninstallPHP queues removal of a version. */
export function useUninstallPHP() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (version: string) => phpApi.uninstall(version),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: phpKeys.versions() });
    },
  });
}

/**
 * useSetWebsitePHP changes which PHP a site runs.
 *
 * The website itself is invalidated too: its php_version changes with the
 * pool, and a stale detail page would disagree with the PHP panel beside it.
 */
export function useSetWebsitePHP() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: SetPHPInput) => phpApi.setWebsitePHP(input),
    onSuccess: (_result, input) => {
      void queryClient.invalidateQueries({ queryKey: phpKeys.website(input.websiteId) });
      void queryClient.invalidateQueries({ queryKey: phpKeys.config(input.websiteId) });
      void queryClient.invalidateQueries({ queryKey: phpKeys.versions() });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.detail(input.websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.jobs(input.websiteId) });
    },
  });
}

/** useSetPHPConfig changes a site's php.ini values without changing version. */
export function useSetPHPConfig(websiteId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (settings: PHPSettingsInput) => phpApi.setConfig(websiteId, settings),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: phpKeys.website(websiteId) });
      void queryClient.invalidateQueries({ queryKey: phpKeys.config(websiteId) });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.jobs(websiteId) });
    },
  });
}
