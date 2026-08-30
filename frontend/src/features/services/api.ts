import { request } from '@/services/apiClient';
import type { ServiceAction, ServiceActionResult, ServiceList } from '@/types/api';

/** Host service calls. */
export const servicesApi = {
  list: (signal?: AbortSignal) => request<ServiceList>('/services', signal ? { signal } : {}),

  act: (key: string, action: ServiceAction) =>
    request<ServiceActionResult>(`/services/${encodeURIComponent(key)}/${action}`, {
      method: 'POST',
    }),
};
