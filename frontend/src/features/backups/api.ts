import { request } from '@/services/apiClient';
import type {
  Backup,
  BackupDestination,
  BackupDestinationInput,
  BackupOverview,
  BackupSchedule,
  BackupScheduleInput,
  BackupVerifyResult,
} from '@/types/api';

/** Backup calls. */
export const backupsApi = {
  overview: (signal?: AbortSignal) =>
    request<BackupOverview>('/backups', signal ? { signal } : {}),

  create: (body: {
    type: string;
    website_id?: string;
    database_id?: string;
    destination_id: string;
    include_databases?: boolean;
  }) => request<Backup>('/backups', { method: 'POST', body }),

  get: (id: string, signal?: AbortSignal) =>
    request<Backup>(`/backups/${id}`, signal ? { signal } : {}),

  remove: (id: string) =>
    request<{ deleted: boolean }>(`/backups/${id}`, { method: 'DELETE' }),

  /**
   * The confirmation is the backup's own id, not a boolean.
   *
   * A `confirm: true` is something a caller sets once and forgets. Sending the
   * id of the thing about to overwrite a live site is a confirmation of that
   * restore rather than of restoring in general.
   */
  restore: (id: string, body: { confirm: string; keep_previous?: boolean }) =>
    request<{ job: { id: string } }>(`/backups/${id}/restore`, { method: 'POST', body }),

  verify: (id: string) =>
    request<BackupVerifyResult>(`/backups/${id}/verify`, { method: 'POST' }),

  destinations: (signal?: AbortSignal) =>
    request<{ destinations: BackupDestination[]; count: number }>(
      '/backup-destinations',
      signal ? { signal } : {},
    ),

  createDestination: (body: BackupDestinationInput) =>
    request<BackupDestination>('/backup-destinations', { method: 'POST', body }),

  updateDestination: (id: string, body: BackupDestinationInput) =>
    request<BackupDestination>(`/backup-destinations/${id}`, { method: 'PATCH', body }),

  removeDestination: (id: string) =>
    request<{ deleted: boolean }>(`/backup-destinations/${id}`, { method: 'DELETE' }),

  /** Writes a small object and reads it back, so a destination that cannot work says so. */
  checkDestination: (id: string) =>
    request<BackupDestination>(`/backup-destinations/${id}/check`, { method: 'POST' }),

  schedules: (signal?: AbortSignal) =>
    request<{ schedules: BackupSchedule[]; count: number }>(
      '/backup-schedules',
      signal ? { signal } : {},
    ),

  createSchedule: (body: BackupScheduleInput) =>
    request<BackupSchedule>('/backup-schedules', { method: 'POST', body }),

  updateSchedule: (id: string, body: Partial<BackupScheduleInput>) =>
    request<BackupSchedule>(`/backup-schedules/${id}`, { method: 'PATCH', body }),

  removeSchedule: (id: string) =>
    request<{ deleted: boolean }>(`/backup-schedules/${id}`, { method: 'DELETE' }),

  runSchedule: (id: string) =>
    request<Backup>(`/backup-schedules/${id}/run`, { method: 'POST' }),
};
