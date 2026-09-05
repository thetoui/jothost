import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
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
    parent_website_id: null,
    apache_port: null,
    allow_override: true,
    document_root_mode: null,
    php_pool_mode: null,
    system_user_mode: null,
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

    expect(await screen.findByRole('button', { name: 'example.test' })).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    // The document root is inside the row's panel, which starts collapsed:
    // a list of twenty sites should not be a wall of paths.
    expect(screen.queryByText('/var/www/example.test/public')).not.toBeInTheDocument();
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
    expect(screen.queryByRole('button', { name: /add website/i })).not.toBeInTheDocument();
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

    await user.click(await screen.findByRole('button', { name: /add website/i }));
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

    await user.click(await screen.findByRole('button', { name: /add website/i }));
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

    await user.click(await screen.findByRole('button', { name: /add website/i }));
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

describe('WebsitesPage domain panel', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  /** listing answers the profile and website calls, and nothing else. */
  function listing(permissions: string[] = ['website.view', 'website.create']) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(permissions);
      }
      if (url.includes('/websites')) {
        return envelopeResponse({ websites: [website()], count: 1 });
      }
      // The rail reads the dashboard snapshot; an unavailable one must not
      // stop the page rendering.
      return errorResponse('UNAVAILABLE', 'no agent', 503);
    });
  }

  // The whole point of the arrangement: the tools open under the domain rather
  // than a page away.
  it('opens a domain panel in place', async () => {
    const user = userEvent.setup();
    listing();

    renderWithProviders(<WebsitesPage />);
    await user.click(await screen.findByRole('button', { name: 'example.test' }));

    expect(await screen.findByRole('tab', { name: 'Dashboard' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Hosting & DNS' })).toBeInTheDocument();
    expect(screen.getByText('Files & Databases')).toBeInTheDocument();
    expect(screen.getByText('Dev Tools')).toBeInTheDocument();
    expect(screen.getByText('Security')).toBeInTheDocument();
  });

  // This is what someone actually came for: where does this domain live.
  it('shows the document root, log directory and system user', async () => {
    const user = userEvent.setup();
    listing();

    renderWithProviders(<WebsitesPage />);
    await user.click(await screen.findByRole('button', { name: 'example.test' }));

    expect(await screen.findByText('/var/www/example.test/logs')).toBeInTheDocument();
    expect(screen.getByText('web_example_test_a1b2c3')).toBeInTheDocument();
    expect(screen.getAllByText('/var/www/example.test/public').length).toBeGreaterThan(0);
  });

  it('links the files tool at that domain rather than the root', async () => {
    const user = userEvent.setup();
    listing();

    renderWithProviders(<WebsitesPage />);
    await user.click(await screen.findByRole('button', { name: 'example.test' }));

    const link = await screen.findByRole('link', { name: /Files for example.test/i });
    expect(link).toHaveAttribute(
      'href',
      '/files?path=' + encodeURIComponent('/var/www/example.test/public'),
    );
  });

  // A tool this build does not have is shown inert with the reason. Hiding it
  // makes the panel look complete and leaves someone hunting. But the reason
  // has to be true: these tiles named release numbers for features that had
  // shipped, so the panel was telling people to wait for something already
  // sitting in the menu beside them.
  it('marks tools that are not built yet, and links the ones that are', async () => {
    const user = userEvent.setup();
    listing();

    renderWithProviders(<WebsitesPage />);
    await user.click(await screen.findByRole('button', { name: 'example.test' }));

    // Backups shipped, so it is a link — in the panel's tool grid and in the
    // server rail beside it, which is why there are two.
    const backups = await screen.findAllByRole('link', { name: /Backup & restore/ });
    expect(backups).toHaveLength(2);
    for (const link of backups) {
      expect(link).toHaveAttribute('href', '/backups');
    }
    // What is genuinely absent says so without naming a release.
    expect(
      screen.getByText('This panel does not manage these yet'),
    ).toBeInTheDocument();
    // Databases stopped being a future phase in Phase 8, so it is now a real
    // link rather than a greyed placeholder — in the panel's tool grid and in
    // the server rail beside it.
    const databaseLinks = screen.getAllByRole('link', { name: /Databases/ });
    expect(databaseLinks.length).toBeGreaterThan(0);
    for (const link of databaseLinks) {
      expect(link).toHaveAttribute('href', '/databases');
    }
  });

  it('closes the panel again', async () => {
    const user = userEvent.setup();
    listing();

    renderWithProviders(<WebsitesPage />);
    const row = await screen.findByRole('button', { name: 'example.test' });

    await user.click(row);
    expect(await screen.findByRole('tab', { name: 'Dashboard' })).toBeInTheDocument();

    await user.click(row);
    await waitFor(() => {
      expect(screen.queryByRole('tab', { name: 'Dashboard' })).not.toBeInTheDocument();
    });
  });

  it('summarises the server beside the list', async () => {
    listing();

    renderWithProviders(<WebsitesPage />);

    const rail = await screen.findByRole('complementary', { name: 'Server' });
    expect(rail).toBeInTheDocument();
    expect(within(rail).getByText('System Overview')).toBeInTheDocument();
    expect(within(rail).getByText('System Security')).toBeInTheDocument();
  });
});
