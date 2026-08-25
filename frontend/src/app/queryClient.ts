import { QueryClient } from '@tanstack/react-query';

import { ApiError } from '@/services/apiClient';

/**
 * Shared query defaults. Authentication and authorization failures are never
 * retried: retrying a 401 or 403 only produces more audit noise.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (failureCount, error) => {
          if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
            return false;
          }
          return failureCount < 2;
        },
        refetchOnWindowFocus: false,
        staleTime: 5_000,
      },
      mutations: { retry: false },
    },
  });
}
