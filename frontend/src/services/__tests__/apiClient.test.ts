import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError, API_BASE_URL, request } from '@/services/apiClient';
import { clearTokens, getAccessToken, getRefreshToken, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { envelopeResponse, errorResponse } from '@/test/utils';

describe('apiClient', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
  });

  afterEach(() => {
    clearTokens();
  });

  it('unwraps the data field of a success envelope', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse({ status: 'ok' }));

    await expect(request<{ status: string }>('/health')).resolves.toEqual({ status: 'ok' });
    expect(globalThis.fetch).toHaveBeenCalledWith(`${API_BASE_URL}/health`, expect.any(Object));
  });

  it('throws ApiError carrying the structured code on failure', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('RESOURCE_NOT_FOUND', 'Website not found', 404),
    );

    await expect(request('/websites/1')).rejects.toMatchObject({
      name: 'ApiError',
      code: 'RESOURCE_NOT_FOUND',
      status: 404,
      requestId: 'req_test',
    });
  });

  it('treats success:false as an error even on HTTP 200', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      new Response(
        JSON.stringify({ success: false, error: { code: 'X', message: 'y' }, request_id: 'r' }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );

    await expect(request('/health')).rejects.toBeInstanceOf(ApiError);
  });

  it('reports a non-JSON gateway response as an unknown error', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      new Response('<html>502 Bad Gateway</html>', { status: 502 }),
    );

    await expect(request('/health')).rejects.toMatchObject({ code: 'UNKNOWN_ERROR', status: 502 });
  });

  it('sends a JSON body and content type only for requests that carry one', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse({}));

    await request('/things', { method: 'POST', body: { name: 'a' } });
    const [, init] = vi.mocked(globalThis.fetch).mock.calls[0]!;
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ name: 'a' }));
    expect((init?.headers as Record<string, string>)['Content-Type']).toBe('application/json');

    vi.mocked(globalThis.fetch).mockClear();
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse({}));
    await request('/things');
    const [, getInit] = vi.mocked(globalThis.fetch).mock.calls[0]!;
    expect(getInit?.body).toBeUndefined();
    expect((getInit?.headers as Record<string, string>)['Content-Type']).toBeUndefined();
  });

  it('attaches the access token as a bearer header', async () => {
    setAccessToken('access-123');
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse({}));

    await request('/auth/me');

    const [url, init] = vi.mocked(globalThis.fetch).mock.calls[0]!;
    expect((init?.headers as Record<string, string>).Authorization).toBe('Bearer access-123');
    // The token must never appear in the URL, where logs would capture it.
    expect(String(url)).not.toContain('access-123');
  });

  it('omits the bearer header on anonymous calls', async () => {
    setAccessToken('access-123');
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse({}));

    await request('/auth/login', { method: 'POST', body: {}, anonymous: true });

    const [, init] = vi.mocked(globalThis.fetch).mock.calls[0]!;
    expect((init?.headers as Record<string, string>).Authorization).toBeUndefined();
  });

  it('refreshes once on 401 and retries the original request', async () => {
    setAccessToken('expired');
    setRefreshToken('refresh-1');

    vi.mocked(globalThis.fetch)
      .mockResolvedValueOnce(errorResponse('UNAUTHORIZED', 'Invalid or expired token', 401))
      .mockResolvedValueOnce(
        envelopeResponse({
          access_token: 'access-2',
          refresh_token: 'refresh-2',
          token_type: 'Bearer',
          expires_in: 900,
        }),
      )
      .mockResolvedValueOnce(envelopeResponse({ username: 'admin' }));

    await expect(request<{ username: string }>('/auth/me')).resolves.toEqual({ username: 'admin' });

    // The rotated pair must replace the old one.
    expect(getAccessToken()).toBe('access-2');
    expect(getRefreshToken()).toBe('refresh-2');
    expect(globalThis.fetch).toHaveBeenCalledTimes(3);
  });

  it('de-duplicates concurrent refreshes', async () => {
    setAccessToken('expired');
    setRefreshToken('refresh-1');

    let refreshCalls = 0;
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith('/auth/refresh')) {
        refreshCalls += 1;
        return envelopeResponse({
          access_token: 'access-2',
          refresh_token: 'refresh-2',
          token_type: 'Bearer',
          expires_in: 900,
        });
      }
      const headers = (init?.headers ?? {}) as Record<string, string>;
      if (headers.Authorization === 'Bearer expired') {
        return errorResponse('UNAUTHORIZED', 'Invalid or expired token', 401);
      }
      return envelopeResponse({ ok: true });
    });

    // Refresh tokens are single-use: two parallel refreshes would look like
    // token reuse to the API and destroy the session.
    await Promise.all([request('/a'), request('/b'), request('/c')]);

    expect(refreshCalls).toBe(1);
  });

  it('clears the session when the refresh fails', async () => {
    setAccessToken('expired');
    setRefreshToken('refresh-1');

    vi.mocked(globalThis.fetch)
      .mockResolvedValueOnce(errorResponse('UNAUTHORIZED', 'Invalid or expired token', 401))
      .mockResolvedValueOnce(errorResponse('UNAUTHORIZED', 'Invalid or expired token', 401));

    await expect(request('/auth/me')).rejects.toBeInstanceOf(ApiError);

    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
  });

  it('does not retry when there is no refresh token', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('UNAUTHORIZED', 'Authentication required', 401),
    );

    await expect(request('/auth/me')).rejects.toBeInstanceOf(ApiError);
    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  });

  it('does not retry non-401 failures', async () => {
    setRefreshToken('refresh-1');
    vi.mocked(globalThis.fetch).mockResolvedValue(
      errorResponse('FORBIDDEN', 'Permission denied', 403),
    );

    await expect(request('/websites')).rejects.toMatchObject({ status: 403 });
    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  });

  it('never persists the access token', async () => {
    setAccessToken('access-123');
    setRefreshToken('refresh-1');

    // Only the refresh token may reach localStorage (see tokenStorage.ts).
    const stored = JSON.stringify(globalThis.localStorage);
    expect(stored).not.toContain('access-123');
    expect(globalThis.localStorage.getItem('jothost.refresh_token')).toBe('refresh-1');
  });
});
