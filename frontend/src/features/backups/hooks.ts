import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { backupsApi } from '@/features/backups/api';
import type { BackupDestinationInput, BackupScheduleInput } from '@/types/api';

export const backupKeys = {
  all: ['backups'] as const,
  overview: () => [...backupKeys.all, 'overview'] as const,
};

/**
 * useBackupOverview reads everything the page shows.
 *
 * It polls while something is running, and only then. A backup finishes on its
 * own, and a page that needed reloading to notice is a page people reload.
 */
export function useBackupOverview() {
  return useQuery({
    queryKey: backupKeys.overview(),
    queryFn: ({ signal }) => backupsApi.overview(signal),
    placeholderData: (previous) => previous,
    refetchInterval: (query) => ((query.state.data?.stats.running ?? 0) > 0 ? 3000 : false),
  });
}

/** useCreateBackup queues a backup. */
export function useCreateBackup() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: {
      type: string;
      website_id?: string;
      database_id?: string;
      destination_id: string;
      include_databases?: boolean;
    }) => backupsApi.create(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
    },
  });
}

/** useDeleteBackup removes an archive from its destination and the panel's record. */
export function useDeleteBackup() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => backupsApi.remove(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useRestoreBackup queues putting a backup back. */
export function useRestoreBackup() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, keepPrevious }: { id: string; keepPrevious: boolean }) =>
      backupsApi.restore(id, { confirm: id, keep_previous: keepPrevious }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
      // A restore replaces a site's files and reloads its databases, so every
      // page describing either is now showing something older than the host.
      void queryClient.invalidateQueries({ queryKey: ['websites'] });
      void queryClient.invalidateQueries({ queryKey: ['databases'] });
    },
  });
}

/** useVerifyBackup reads a stored backup back and records what it found. */
export function useVerifyBackup() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => backupsApi.verify(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useCreateDestination adds somewhere backups can go. */
export function useCreateDestination() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: BackupDestinationInput) => backupsApi.createDestination(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useUpdateDestination changes a destination. */
export function useUpdateDestination() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: BackupDestinationInput }) =>
      backupsApi.updateDestination(id, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useDeleteDestination removes a destination. */
export function useDeleteDestination() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => backupsApi.removeDestination(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useCheckDestination proves a destination can be written to and read back from. */
export function useCheckDestination() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => backupsApi.checkDestination(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useCreateSchedule adds a standing instruction to take a backup. */
export function useCreateSchedule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: BackupScheduleInput) => backupsApi.createSchedule(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useUpdateSchedule changes a schedule. */
export function useUpdateSchedule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: Partial<BackupScheduleInput> }) =>
      backupsApi.updateSchedule(id, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useDeleteSchedule removes a schedule, leaving the backups it took. */
export function useDeleteSchedule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => backupsApi.removeSchedule(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
    },
  });
}

/** useRunSchedule takes a schedule's backup now. */
export function useRunSchedule() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => backupsApi.runSchedule(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: backupKeys.all });
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
    },
  });
}
