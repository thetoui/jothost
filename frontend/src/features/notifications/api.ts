import { request } from '@/services/apiClient';
import type {
  NotificationChannel,
  NotificationChannelInput,
  NotificationDelivery,
  NotificationOverview,
} from '@/types/api';

/** Notification calls. */
export const notificationsApi = {
  overview: (signal?: AbortSignal) =>
    request<NotificationOverview>('/notifications', signal ? { signal } : {}),

  /**
   * The delivery record.
   *
   * The most important read here, because a notification system cannot report
   * its own failure through itself: when delivery is broken, this is the only
   * place that says so.
   */
  deliveries: (status?: string, signal?: AbortSignal) =>
    request<{ deliveries: NotificationDelivery[]; count: number }>(
      `/notifications/deliveries${status ? `?status=${status}` : ''}`,
      signal ? { signal } : {},
    ),

  channels: (signal?: AbortSignal) =>
    request<{ channels: NotificationChannel[]; count: number }>(
      '/notification-channels',
      signal ? { signal } : {},
    ),

  createChannel: (body: NotificationChannelInput) =>
    request<NotificationChannel>('/notification-channels', { method: 'POST', body }),

  updateChannel: (id: string, body: Partial<NotificationChannelInput>) =>
    request<NotificationChannel>(`/notification-channels/${id}`, {
      method: 'PATCH',
      body,
    }),

  removeChannel: (id: string) =>
    request<{ deleted: boolean }>(`/notification-channels/${id}`, { method: 'DELETE' }),

  /**
   * Sends a message through one channel, now.
   *
   * A channel nobody has ever delivered through looks like protection and is
   * not — the same failure an unreached backup destination is. This is the only
   * way to find out while somebody is watching.
   */
  testChannel: (id: string) =>
    request<NotificationChannel>(`/notification-channels/${id}/test`, { method: 'POST' }),
};
