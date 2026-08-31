import { request } from '@/services/apiClient';
import type { CronJob, CronJobList, CronJobType, CronRunResult } from '@/types/api';

/** What a job is created or changed with. */
export interface CronJobInput {
  website_id?: string;
  name?: string;
  job_type?: CronJobType;
  schedule?: string;
  target?: string;
  enabled?: boolean;
}

/** Scheduled job calls. */
export const cronApi = {
  list: (websiteID?: string, signal?: AbortSignal) =>
    request<CronJobList>(
      websiteID ? `/cron?website_id=${encodeURIComponent(websiteID)}` : '/cron',
      signal ? { signal } : {},
    ),

  get: (id: string) => request<CronJob>(`/cron/${encodeURIComponent(id)}`),

  create: (input: CronJobInput) =>
    request<CronJob>('/cron', { method: 'POST', body: input }),

  update: (id: string, input: CronJobInput) =>
    request<CronJob>(`/cron/${encodeURIComponent(id)}`, { method: 'PATCH', body: input }),

  remove: (id: string) =>
    request<{ deleted: boolean }>(`/cron/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  run: (id: string) =>
    request<CronRunResult>(`/cron/${encodeURIComponent(id)}/run`, { method: 'POST' }),
};
