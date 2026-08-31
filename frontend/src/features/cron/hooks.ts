import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { cronApi, type CronJobInput } from '@/features/cron/api';

export const cronKeys = {
  all: ['cron'] as const,
  list: (websiteID?: string) => [...cronKeys.all, 'list', websiteID ?? 'all'] as const,
};

/**
 * useCronJobs lists the scheduled jobs.
 *
 * Polled while the page is open, because the interesting column changes without
 * anybody pressing anything: a job runs on its schedule, and "last run" and
 * "next run" are the two things an operator came to look at.
 */
export function useCronJobs(websiteID?: string) {
  return useQuery({
    queryKey: cronKeys.list(websiteID),
    queryFn: ({ signal }) => cronApi.list(websiteID, signal),
    refetchInterval: 30_000,
    placeholderData: (previous) => previous,
  });
}

/** useCreateCronJob schedules a new job. */
export function useCreateCronJob() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: CronJobInput) => cronApi.create(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: cronKeys.all });
    },
  });
}

/** useUpdateCronJob changes a job, including enabling and disabling it. */
export function useUpdateCronJob() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: CronJobInput }) =>
      cronApi.update(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: cronKeys.all });
    },
  });
}

/** useDeleteCronJob removes a job from the panel and the host. */
export function useDeleteCronJob() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => cronApi.remove(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: cronKeys.all });
    },
  });
}

/**
 * useRunCronJob runs a job now.
 *
 * The request is held open until the job finishes, which is the point: pressing
 * "run now" is asking what happens, and a reply of "started" would answer a
 * different question. The list is refreshed afterwards because the run updates
 * the job's last outcome.
 */
export function useRunCronJob() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => cronApi.run(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: cronKeys.all });
    },
  });
}
