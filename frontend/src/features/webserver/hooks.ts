import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { webserverApi } from '@/features/webserver/api';
import { websiteKeys } from '@/features/websites/hooks';
import type { WebserverMode } from '@/types/api';

export const webserverKeys = {
  all: ['webserver'] as const,
  status: () => [...webserverKeys.all, 'status'] as const,
};

/** useWebserver reads the host's arrangement and what Apache is doing. */
export function useWebserver() {
  return useQuery({
    queryKey: webserverKeys.status(),
    queryFn: ({ signal }) => webserverApi.status(signal),
    placeholderData: (previous) => previous,
  });
}

/**
 * useSetWebserverMode switches the arrangement.
 *
 * Every website's configuration is rewritten by the change, so the website
 * caches are invalidated with it: a site's page would otherwise still show the
 * arrangement it had a moment ago.
 */
export function useSetWebserverMode() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (mode: WebserverMode) => webserverApi.setMode(mode),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webserverKeys.status() });
      void queryClient.invalidateQueries({ queryKey: websiteKeys.all });
    },
  });
}

/** useInstallApache puts Apache on the host without changing the arrangement. */
export function useInstallApache() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => webserverApi.installApache(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webserverKeys.status() });
    },
  });
}
