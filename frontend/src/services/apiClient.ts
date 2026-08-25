import type { ApiEnvelope, ApiErrorDetail } from '@/types/api';

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
}

/**
 * request is the single entry point to the API. Components never call fetch
 * directly (CLAUDE.md section 10): page -> feature hook -> API service.
 *
 * Tokens are deliberately not read from localStorage here; Phase 1 introduces
 * authentication and will decide on the storage mechanism explicitly.
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal } = options;

  const init: RequestInit = {
    method,
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    // Session cookies, when Phase 1 introduces them, must accompany requests.
    credentials: 'same-origin',
  };
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
