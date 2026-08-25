import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError, API_BASE_URL, request } from '@/services/apiClient';
import { envelopeResponse, errorResponse } from '@/test/utils';

describe('apiClient', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
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
      new Response(JSON.stringify({ success: false, error: { code: 'X', message: 'y' }, request_id: 'r' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
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

  it('does not read or write authentication tokens in localStorage', async () => {
    // Phase 0 has no auth; this guards CLAUDE.md section 10 against a
    // convenience token cache being added without an explicit decision.
    const getItem = vi.spyOn(Storage.prototype, 'getItem');
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    vi.mocked(globalThis.fetch).mockResolvedValue(envelopeResponse({}));

    await request('/health');

    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
  });
});
