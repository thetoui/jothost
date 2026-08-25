import {
  clearTokens,
  getAccessToken,
  getRefreshToken,
  setAccessToken,
  setRefreshToken,
} from '@/features/auth/tokenStorage';
import type { ApiEnvelope, ApiErrorDetail, TokenPair } from '@/types/api';

/**
 * ApiError carries the structured error returned by the API so feature hooks
 * can branch on a stable code instead of parsing messages.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly requestId: string;

  constructor(detail: ApiErrorDetail, status: number, requestId: string) {
    super(detail.message);
    this.name = 'ApiError';
    this.code = detail.code;
    this.status = status;
    this.requestId = requestId;
  }
}

const UNKNOWN_ERROR: ApiErrorDetail = {
  code: 'UNKNOWN_ERROR',
  message: 'The server returned an unexpected response.',
};

/** Base path for every API call. Vite proxies /api to the Go API in dev. */
export const API_BASE_URL = '/api/v1';

export interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** Skips the Authorization header and the refresh retry. */
  anonymous?: boolean;
}

/** Listeners notified when the session ends and the user must log in again. */
type SessionEndedListener = () => void;
const sessionEndedListeners = new Set<SessionEndedListener>();

/**
 * onSessionEnded registers a callback for an unrecoverable auth failure.
 * The auth store uses it to reset itself without importing the client, which
 * would be a cycle.
 */
export function onSessionEnded(listener: SessionEndedListener): () => void {
  sessionEndedListeners.add(listener);
  return () => sessionEndedListeners.delete(listener);
}

function endSession(): void {
  clearTokens();
  for (const listener of sessionEndedListeners) {
    listener();
  }
}

/**
 * refreshInFlight de-duplicates concurrent refreshes.
 *
 * Refresh tokens are single-use and rotate, so two parallel 401s must not both
 * try to redeem the same token: the second would be treated as reuse and would
 * destroy the session. All callers await the same promise.
 */
let refreshInFlight: Promise<boolean> | null = null;

async function refreshSession(): Promise<boolean> {
  refreshInFlight ??= (async () => {
    try {
      const token = getRefreshToken();
      if (!token) {
        return false;
      }

      const pair = await rawRequest<TokenPair>('/auth/refresh', {
        method: 'POST',
        body: { refresh_token: token },
        anonymous: true,
      });

      setAccessToken(pair.access_token);
      setRefreshToken(pair.refresh_token);
      return true;
    } catch {
      // Any refresh failure is terminal: the stored token is gone or revoked.
      endSession();
      return false;
    } finally {
      refreshInFlight = null;
    }
  })();

  return refreshInFlight;
}

/** rawRequest performs one call with no refresh handling. */
async function rawRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal, anonymous = false } = options;

  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
  };

  if (!anonymous) {
    const token = getAccessToken();
    if (token) {
      // Tokens travel in the Authorization header only — never in the URL,
      // where they would be captured by access logs and browser history.
      headers.Authorization = `Bearer ${token}`;
    }
  }

  const init: RequestInit = { method, headers, credentials: 'same-origin' };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  if (signal) {
    init.signal = signal;
  }

  const response = await globalThis.fetch(`${API_BASE_URL}${path}`, init);

  let envelope: ApiEnvelope<T> | null = null;
  try {
    envelope = (await response.json()) as ApiEnvelope<T>;
  } catch {
    // A non-JSON body means the request never reached the API (a proxy or
    // gateway error), which is reported as an unknown error below.
    envelope = null;
  }

  if (!response.ok || !envelope?.success) {
    throw new ApiError(
      envelope?.error ?? UNKNOWN_ERROR,
      response.status,
      envelope?.request_id ?? '',
    );
  }

  return envelope.data as T;
}

/**
 * request is the single entry point to the API. Components never call fetch
 * directly (CLAUDE.md section 10): page -> feature hook -> API service.
 *
 * A 401 triggers one refresh attempt and one retry. If the refresh fails the
 * session is ended rather than retried again, so a revoked token cannot put
 * the client into a loop.
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  try {
    return await rawRequest<T>(path, options);
  } catch (error) {
    const isAuthFailure = error instanceof ApiError && error.status === 401;
    if (!isAuthFailure || options.anonymous) {
      throw error;
    }

    const refreshed = await refreshSession();
    if (!refreshed) {
      throw error;
    }
    return rawRequest<T>(path, options);
  }
}
