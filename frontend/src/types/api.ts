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

/** Stable error codes the UI branches on. */
export const ApiErrorCode = {
  Unauthorized: 'UNAUTHORIZED',
  Forbidden: 'FORBIDDEN',
  NotFound: 'RESOURCE_NOT_FOUND',
  ValidationFailed: 'VALIDATION_FAILED',
  RateLimited: 'RATE_LIMITED',
  Conflict: 'CONFLICT',
  Internal: 'INTERNAL_ERROR',
} as const;

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

/* ------------------------------------------------------------------ auth */

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
}

/**
 * A login either returns tokens or demands a second factor. The two shapes
 * arrive on the same endpoint, so `mfa_required` is the discriminator.
 */
export interface LoginResponse extends Partial<TokenPair> {
  mfa_required?: boolean;
  mfa_token?: string;
}

export interface UserProfile {
  id: string;
  username: string;
  email: string | null;
  status: string;
  roles: string[];
  permissions: string[];
  two_factor_enabled: boolean;
  last_login_at: string | null;
  created_at: string;
}

export interface TwoFactorSetupResponse {
  /** Shown once at enrolment. Never persist or log this. */
  secret: string;
  otpauth_uri: string;
}
