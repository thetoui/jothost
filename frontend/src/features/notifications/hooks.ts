import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { notificationsApi } from '@/features/notifications/api';
import type { NotificationChannelInput } from '@/types/api';

export const notificationKeys = {
  all: ['notifications'] as const,
  overview: () => [...notificationKeys.all, 'overview'] as const,
};

/**
 * useNotificationOverview reads the channels and what has been getting through.
 *
 * It polls while anything is queued, and only then. A delivery is retried on a
 * backoff over the following quarter of an hour, and a page that needed
 * reloading to notice would be a page people reload.
 */
export function useNotificationOverview() {
  return useQuery({
    queryKey: notificationKeys.overview(),
    queryFn: ({ signal }) => notificationsApi.overview(signal),
    placeholderData: (previous) => previous,
    refetchInterval: (query) =>
      (query.state.data?.stats.pending ?? 0) > 0 ? 5000 : false,
  });
}

/** useCreateChannel adds somewhere notifications can go. */
export function useCreateChannel() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: NotificationChannelInput) => notificationsApi.createChannel(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}

/** useUpdateChannel changes a channel. */
export function useUpdateChannel() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: Partial<NotificationChannelInput> }) =>
      notificationsApi.updateChannel(id, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}

/** useDeleteChannel removes a channel and its delivery history. */
export function useDeleteChannel() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => notificationsApi.removeChannel(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}

/** useTestChannel proves a channel can actually deliver. */
export function useTestChannel() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => notificationsApi.testChannel(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}
