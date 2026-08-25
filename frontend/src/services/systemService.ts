import { request } from '@/services/apiClient';
import type { HealthResponse, ReadinessResponse } from '@/types/api';

/** System-level API calls backing the Phase 0 shell. */
export const systemService = {
  health: (signal?: AbortSignal) =>
    request<HealthResponse>('/health', signal ? { signal } : {}),
};

/**
 * Readiness lives outside /api/v1 because orchestrators probe it directly.
 * It is exposed here so the UI can surface dependency state later.
 */
export type { ReadinessResponse };
