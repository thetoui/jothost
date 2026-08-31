import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { ftpApi } from '@/features/ftp/api';
import type { FTPSettingsChange, FTPUserChange } from '@/types/api';

export const ftpKeys = {
  all: ['ftp'] as const,
  overview: () => [...ftpKeys.all, 'overview'] as const,
  sessions: () => [...ftpKeys.all, 'sessions'] as const,
  forWebsite: (websiteId: string) => [...ftpKeys.all, 'website', websiteId] as const,
};

/** useFTPOverview reads the server, its accounts and its settings. */
export function useFTPOverview() {
  return useQuery({
    queryKey: ftpKeys.overview(),
    queryFn: ({ signal }) => ftpApi.overview(signal),
    placeholderData: (previous) => previous,
  });
}

/**
 * useFTPSessions lists what is connected right now.
 *
 * Polled, and quickly: a session monitor is watched while somebody is
 * transferring, and a table that only refreshed on navigation would be showing
 * a server as it was several minutes ago. Only while the daemon is running —
 * polling a stopped server would be asking a question whose answer cannot
 * change.
 */
export function useFTPSessions(enabled: boolean) {
  return useQuery({
    queryKey: ftpKeys.sessions(),
    queryFn: ({ signal }) => ftpApi.sessions(signal),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
    enabled,
  });
}

/** useWebsiteFTPUsers reads one website's accounts, for its own tab. */
export function useWebsiteFTPUsers(websiteId: string, enabled = true) {
  return useQuery({
    queryKey: ftpKeys.forWebsite(websiteId),
    queryFn: ({ signal }) => ftpApi.forWebsite(websiteId, signal),
    enabled: enabled && Boolean(websiteId),
  });
}

/** useInstallFTP puts an FTP server on the host. */
export function useInstallFTP() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => ftpApi.install(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ftpKeys.all });
      // The Services page lists the daemon too, and it has just appeared there.
      void queryClient.invalidateQueries({ queryKey: ['services'] });
    },
  });
}

/** useCreateFTPUser adds an account. */
export function useCreateFTPUser() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: {
      website_id: string;
      username: string;
      password?: string;
      home_subpath?: string;
      access_level?: string;
      quota_mb?: number;
    }) => ftpApi.create(body),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ftpKeys.all }),
  });
}

/** useUpdateFTPUser changes one account. */
export function useUpdateFTPUser() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, change }: { id: string; change: FTPUserChange }) =>
      ftpApi.update(id, change),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ftpKeys.all }),
  });
}

/** useDeleteFTPUser removes one account. */
export function useDeleteFTPUser() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => ftpApi.remove(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ftpKeys.all }),
  });
}

/** useDisconnectFTPSession ends one session. */
export function useDisconnectFTPSession() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (pid: number) => ftpApi.disconnect(pid),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ftpKeys.sessions() }),
  });
}

/** useSaveFTPSettings changes the server's own options. */
export function useSaveFTPSettings() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (change: FTPSettingsChange) => ftpApi.saveSettings(change),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ftpKeys.all }),
  });
}
