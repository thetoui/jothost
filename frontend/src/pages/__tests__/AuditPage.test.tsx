import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { AuditPage } from '@/pages/AuditPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { AuditEntry } from '@/types/api';

function entry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 'entry-1',
    action: 'website.delete',
    user_id: 'user-1',
    username: 'admin',
    resource_type: 'website',
    resource_id: '11111111-2222-3333-4444-555555555555',
    ip_address: '203.0.113.7',
    user_agent: 'Mozilla/5.0',
    status: 'SUCCESS',
    metadata: { domain: 'example.test' },
    created_at: '2026-09-04T09:00:00Z',
    ...overrides,
  };
}

describe('AuditPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    entries?: AuditEntry[];
    total?: number;
    actions?: string[];
    onList?: (url: string) => void;
  }) {
    const entries = options.entries ?? [entry()];
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/audit/actions')) {
        const actions = options.actions ?? ['website.create', 'website.delete'];
        return envelopeResponse({ actions, count: actions.length });
      }
      if (url.includes('/audit')) {
        options.onList?.(url);
        return envelopeResponse({
          entries,
          count: entries.length,
          total: options.total ?? entries.length,
          limit: 50,
          offset: 0,
        });
      }
      return envelopeResponse({});
    });
  }

  it('shows who did what, and from where', async () => {
    mockApi({});
    renderWithProviders(<AuditPage />);

    // Scoped to the table on purpose: the action name is also one of the
    // filter's options, so an unscoped query matches twice and fails on the
    // ambiguity rather than on anything being wrong.
    const table = within(await screen.findByRole('table'));
    expect(table.getByText('website.delete')).toBeInTheDocument();
    expect(table.getByText('admin')).toBeInTheDocument();
    expect(table.getByText('203.0.113.7')).toBeInTheDocument();
    expect(table.getByText('Succeeded')).toBeInTheDocument();
  });

  it('says how many entries matched, not how many are on the page', async () => {
    // A page of 50 out of 9,318 must not read as a history of 50: the count is
    // the answer to the question somebody came here with.
    mockApi({ entries: [entry(), entry({ id: 'entry-2' })], total: 9318 });
    renderWithProviders(<AuditPage />);

    expect(await screen.findByText(/2 of 9,318 entries/)).toBeInTheDocument();
  });

  it('tells an action with no actor apart from one whose account was deleted', async () => {
    // Two different silences. A failed login against a username that never
    // existed has nobody to name; an action by an account removed afterwards
    // had somebody, and the trail kept the row but could not keep the name.
    mockApi({
      entries: [
        entry({ id: 'anon', action: 'auth.login_failed', user_id: null, username: null }),
        entry({ id: 'gone', action: 'website.create', user_id: 'user-9', username: null }),
      ],
    });
    renderWithProviders(<AuditPage />);

    expect(await screen.findByText('not signed in')).toBeInTheDocument();
    expect(screen.getByText('account deleted')).toBeInTheDocument();
  });

  it('marks a refused action as failed', async () => {
    mockApi({ entries: [entry({ status: 'FAILURE', action: 'auth.login_failed' })] });
    renderWithProviders(<AuditPage />);

    expect(await screen.findByText('Failed')).toBeInTheDocument();
  });

  it('shows an entry’s recorded detail on request', async () => {
    mockApi({});
    renderWithProviders(<AuditPage />);

    const expand = await screen.findByRole('button', {
      name: /Show the detail of website.delete/,
    });
    await userEvent.click(expand);

    // "website.delete" cannot say which website without its metadata.
    expect(await screen.findByText('example.test')).toBeInTheDocument();
  });

  it('asks the server for the filtered set rather than filtering in the browser', async () => {
    // The trail is unbounded and paged, so a filter applied client-side would
    // search only the page in hand and quietly miss everything else.
    const urls: string[] = [];
    mockApi({ onList: (url) => urls.push(url) });
    renderWithProviders(<AuditPage />);

    await screen.findByRole('table');
    await userEvent.selectOptions(screen.getByLabelText('Outcome'), 'FAILURE');

    await waitFor(() => {
      expect(urls.some((url) => url.includes('status=FAILURE'))).toBe(true);
    });
  });

  it('offers only the actions actually recorded', async () => {
    // Built from the trail, not from a list in the code: every feature appends
    // its own action names, so a hard-coded filter would offer names nothing
    // ever recorded and hide the ones that matter.
    mockApi({ actions: ['backup.restore', 'firewall.change'] });
    renderWithProviders(<AuditPage />);

    // findBy, not getBy: the options arrive with the actions query, which is a
    // second request and lands after the first paint.
    expect(await screen.findByRole('option', { name: 'backup.restore' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'firewall.change' })).toBeInTheDocument();
  });

  it('says the trail is empty rather than showing a blank table', async () => {
    mockApi({ entries: [], total: 0 });
    renderWithProviders(<AuditPage />);

    expect(await screen.findByText('The trail is empty')).toBeInTheDocument();
  });
});
