import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { WebsitesPage } from '@/pages/WebsitesPage';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import type { Job, Website, WebsiteCreated } from '@/types/api';

function website(overrides: Partial<Website> = {}): Website {
  return {
    id: '11111111-2222-3333-4444-555555555555',
    server_id: '99999999-8888-7777-6666-555555555555',
    name: 'Example',
    primary_domain: 'example.test',
    document_root: '/var/www/example.test/public',
    system_user: 'web_example_test_a1b2c3',
    php_version: null,
    status: 'active',
    ssl_enabled: false,
    https_redirect: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function job(overrides: Partial<Job> = {}): Job {
  return {
    id: '22222222-3333-4444-5555-666666666666',
    type: 'website.create',
    status: 'PENDING',
    progress: 0,
    created_by: null,
    resource_type: 'website',
    resource_id: '11111111-2222-3333-4444-555555555555',
    created_at: '2026-01-01T00:00:00Z',
    started_at: null,
    completed_at: null,
    ...overrides,
  };
}

/** The profile call decides which controls RequirePermission renders. */
function mockProfile(permissions: string[]) {
  return envelopeResponse({
    id: 'user-1',
    username: 'admin',
    email: null,
    status: 'active',
    two_factor_enabled: false,
    roles: ['admin'],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('WebsitesPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    // RequirePermission renders from the profile, and useProfile only runs for
    // a session that exists. The store's initial status is computed when the
    // module loads, so it is set here rather than inferred from the tokens.
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('lists hosted websites with their status', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['website.view']);
      }
      return envelopeResponse({ websites: [website()], count: 1 });
    });

    renderWithProviders(<WebsitesPage />);

    expect(await screen.findByRole('link', { name: 'example.test' })).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('/var/www/example.test/public')).toBeInTheDocument();
  });

  it('says so when there are no websites yet', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['website.view']);
      }
      return envelopeResponse({ websites: [], count: 0 });
    });

    renderWithProviders(<WebsitesPage />);

    expect(await screen.findByText('No websites yet')).toBeInTheDocument();
  });

  // A site mid-provision must not be shown as working. The panel says
  // "creating" until the host confirms otherwise.
  it('shows a provisioning site as creating, not active', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['website.view']);
      }
      return envelopeResponse({ websites: [website({ status: 'creating' })], count: 1 });
    });

    renderWithProviders(<WebsitesPage />);

    expect(await screen.findByText('Creating')).toBeInTheDocument();
    expect(screen.queryByText('Active')).not.toBeInTheDocument();
  });

  it('hides the create button from a user who cannot create websites', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['website.view']);
      }
      return envelopeResponse({ websites: [], count: 0 });
    });

    renderWithProviders(<WebsitesPage />);

    await screen.findByText('No websites yet');
    expect(screen.queryByRole('button', { name: /new website/i })).not.toBeInTheDocument();
  });

  it('creates a website and reports it as queued', async () => {
    const created: WebsiteCreated = {
      website: website({ status: 'creating' }),
      job: job(),
    };

    let listCalls = 0;
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['website.view', 'website.create']);
      }
      if (init?.method === 'POST') {
        return envelopeResponse(created, 201);
      }
      listCalls += 1;
      return envelopeResponse(
        listCalls === 1
          ? { websites: [], count: 0 }
          : { websites: [created.website], count: 1 },
      );
    });

    const user = userEvent.setup();
    renderWithProviders(<WebsitesPage />);

    await user.click(await screen.findByRole('button', { name: /new website/i }));
    await user.type(screen.getByLabelText('Domain'), 'example.test');
    await user.click(screen.getByRole('button', { name: 'Create website' }));

    expect(await screen.findByText('Creating')).toBeInTheDocument();
  });

  // The API distinguishes a taken domain from an invalid one, so its message
  // is shown rather than a generic failure.
  it('surfaces the server message when a domain is already in use', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['website.view', 'website.create']);
      }
      if (init?.method === 'POST') {
        return errorResponse(
          'CONFLICT',
          'That domain is already in use by another website',
          409,
        );
      }
      return envelopeResponse({ websites: [], count: 0 });
    });

    const user = userEvent.setup();
    renderWithProviders(<WebsitesPage />);

    await user.click(await screen.findByRole('button', { name: /new website/i }));
    await user.type(screen.getByLabelText('Domain'), 'example.test');
    await user.click(screen.getByRole('button', { name: 'Create website' }));

    expect(
      await screen.findByText('That domain is already in use by another website'),
    ).toBeInTheDocument();
  });

  // An invalid domain is caught before a round trip, so the API is not called.
  it('rejects an invalid domain without contacting the API', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['website.view', 'website.create']);
      }
      return envelopeResponse({ websites: [], count: 0 });
    });

    const user = userEvent.setup();
    renderWithProviders(<WebsitesPage />);

    await user.click(await screen.findByRole('button', { name: /new website/i }));
    await user.type(screen.getByLabelText('Domain'), 'localhost');
    await user.click(screen.getByRole('button', { name: 'Create website' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/full domain/i);

    const posted = vi
      .mocked(globalThis.fetch)
      .mock.calls.some(([, init]) => init?.method === 'POST');
    expect(posted).toBe(false);
  });

  it('surfaces a failed listing', async () => {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      if (String(input).includes('/auth/me')) {
        return mockProfile(['website.view']);
      }
      return errorResponse('INTERNAL_ERROR', 'The website list is unavailable', 500);
    });

    renderWithProviders(<WebsitesPage />);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('The website list is unavailable');
    });
  });
});
