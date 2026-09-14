import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { LogsPage } from '@/pages/LogsPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { LogLine, LogSource, LogTail } from '@/types/api';

function source(overrides: Partial<LogSource> = {}): LogSource {
  return {
    key: 'nginx.error',
    label: 'nginx error',
    summary: 'What the web server could not do, and why.',
    group: 'web',
    format: 'nginx-error',
    path: '/var/log/nginx/error.log',
    present: true,
    size: 2048,
    modified: '2026-08-31T00:00:00Z',
    ...overrides,
  };
}

function line(text: string, level = '', offset = 0): LogLine {
  return { offset, text, level, truncated: false };
}

function tail(overrides: Partial<LogTail> = {}): LogTail {
  return {
    key: 'nginx.error',
    path: '/var/log/nginx/error.log',
    lines: [line('2026/08/31 00:34:01 [error] open() failed', 'error', 0)],
    offset: 42,
    size: 2048,
    rotated: false,
    scanned: 2048,
    partial: false,
    filtered: 0,
    format: 'nginx-error',
    ...overrides,
  };
}

function mockProfile(permissions: string[]) {
  return envelopeResponse({
    id: 'user-1',
    username: 'admin',
    email: null,
    status: 'active',
    two_factor_enabled: false,
    recovery_codes_remaining: 0,
    roles: ['admin'],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('LogsPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  /** mockApi answers the two calls the page makes, recording the tail URLs. */
  function mockApi(
    sources: LogSource[],
    tails: LogTail | LogTail[],
    onTail?: (url: string) => void,
  ) {
    const queue = Array.isArray(tails) ? [...tails] : [tails];
    let last = queue[0] as LogTail;

    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (/\/logs\/[^?]+/.test(url)) {
        onTail?.(url);
        last = queue.shift() ?? last;
        return envelopeResponse(last);
      }
      return envelopeResponse({ sources, count: sources.length, levels: ['error', 'warn'] });
    });
  }

  it('lists the logs this host has and opens the first of them', async () => {
    const urls: string[] = [];
    mockApi([source(), source({ key: 'agent', label: 'Agent audit', group: 'panel' })], tail(),
      (url) => urls.push(url));
    renderWithProviders(<LogsPage />);

    expect(await screen.findByText('nginx error')).toBeInTheDocument();
    expect(screen.getByText('Agent audit')).toBeInTheDocument();
    // Something is on screen without the operator having to pick first.
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));
    expect(urls[0]).toContain('/logs/nginx.error');
    expect(await screen.findByText(/open\(\) failed/)).toBeInTheDocument();
  });

  // "nginx has recorded no errors" and "this panel does not offer that log" are
  // different answers, and the page has to distinguish them.
  it('shows a log this host does not have, and says so', async () => {
    mockApi(
      [source({ key: 'system', label: 'System', group: 'system', present: false, path: '', size: 0 })],
      tail(),
    );
    renderWithProviders(<LogsPage />);

    // By role, because the group heading is also called "System".
    expect(await screen.findByRole('button', { name: /System/ })).toBeInTheDocument();
    expect(screen.getByText('not on this host')).toBeInTheDocument();
  });

  it('sends the search and level as filters rather than filtering on screen', async () => {
    const urls: string[] = [];
    mockApi([source()], tail(), (url) => urls.push(url));
    renderWithProviders(<LogsPage />);

    await screen.findByText(/open\(\) failed/);
    await userEvent.type(screen.getByLabelText('Search'), 'upstream');

    // Debounced: the request carries the settled value, not one per keystroke.
    await waitFor(() => expect(urls.some((url) => url.includes('search=upstream'))).toBe(true));
    expect(urls.filter((url) => url.includes('search=')).length).toBeLessThan(4);

    await userEvent.selectOptions(screen.getByLabelText('Level'), 'error');
    await waitFor(() => expect(urls.some((url) => url.includes('level=error'))).toBe(true));
  });

  it('says how many lines the filters hid', async () => {
    mockApi([source()], tail({ filtered: 37 }));
    renderWithProviders(<LogsPage />);

    expect(await screen.findByText(/37 hidden by the filters/)).toBeInTheDocument();
  });

  // A rotated file's offsets refer to a file that no longer exists. The page
  // says so rather than presenting two files spliced together as one.
  it('reports a log that was rotated underneath it', async () => {
    mockApi([source()], tail({ rotated: true }));
    renderWithProviders(<LogsPage />);

    expect(
      await screen.findByText(/replaced while you were reading it/),
    ).toBeInTheDocument();
  });

  it('follows a log by asking only for what is new', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const urls: string[] = [];
    mockApi(
      [source()],
      [tail(), tail({ lines: [line('a new line', 'error', 42)], offset: 84 })],
      (url) => urls.push(url),
    );

    try {
      renderWithProviders(<LogsPage />);
      await screen.findByText(/open\(\) failed/);

      await userEvent.click(screen.getByLabelText('Follow'));
      await vi.advanceTimersByTimeAsync(3_500);

      // The follow poll carries the offset from the previous read, so a quiet
      // log transfers nothing at all.
      await waitFor(() => expect(urls.some((url) => url.includes('after=42'))).toBe(true));
      // And what arrives is added to what is already shown, not replacing it.
      expect(await screen.findByText('a new line')).toBeInTheDocument();
      expect(screen.getByText(/open\(\) failed/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not offer a download for a log with nothing in it', async () => {
    mockApi([source({ size: 0 })], tail({ lines: [], size: 0 }));
    renderWithProviders(<LogsPage />);

    expect(await screen.findByRole('button', { name: /Download/ })).toBeDisabled();
  });
});
