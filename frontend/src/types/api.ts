/**
 * Client-side mirror of the API response envelope defined in
 * API_SPEC.md section 1. Keep this in sync with api/internal/httpx.
 */
export interface ApiEnvelope<T> {
  success: boolean;
  data?: T;
  error?: ApiErrorDetail;
  request_id: string;
}

export interface ApiErrorDetail {
  code: string;
  message: string;
}

export interface HealthResponse {
  status: string;
  service: string;
  version: string;
}

export type DependencyStatus = 'up' | 'down';

export interface ReadinessCheck {
  status: DependencyStatus;
  error?: string;
}

export interface ReadinessResponse {
  ready: boolean;
  checks: Record<string, ReadinessCheck>;
}
