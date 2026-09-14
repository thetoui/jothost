import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { useAuthStore } from '@/stores/authStore';
import { DatabasesPage } from '@/pages/DatabasesPage';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Database, DatabaseUser } from '@/types/api';

function database(overrides: Partial<Database> = {}): Database {
  return {
    id: '11111111-2222-3333-4444-555555555555',
    server_id: 'server-1',
    website_id: null,
    name: 'shop',
    engine: 'mariadb',
    status: 'active',
    charset: 'utf8mb4',
    collation: 'utf8mb4_unicode_ci',
    size_bytes: 65536,
    size_checked_at: '2026-01-01T00:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    website_domain: null,
    user_count: 1,
    ...overrides,
  };
}

function user(overrides: Partial<DatabaseUser> = {}): DatabaseUser {
  return {
    id: '99999999-8888-7777-6666-555555555555',
    server_id: 'server-1',
    engine: 'mariadb',
    username: 'shop',
    host: 'localhost',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    password_updated_at: '2026-01-01T00:00:00Z',
    grants: [{ database_id: database().id, database_name: 'shop', privilege: 'full' }],
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

const engines = {
  engines: [
    {
      engine: 'mariadb',
      available: true,
      version: '11.4.2-MariaDB',
      supports_host_patterns: true,
    },
    {
      engine: 'postgres',
      available: false,
      detail: 'PostgreSQL is not installed on this host',
      supports_host_patterns: false,
    },
  ],
  available: true,
  privileges: ['readonly', 'readwrite', 'full'],
};

/** route builds the default fetch mock: profile, engines, list, and detail. */
function mockApi(overrides: {
  databases?: Database[];
  users?: DatabaseUser[];
  enginesData?: unknown;
  console?: unknown;
  permissions?: string[];
} = {}) {
  const databases = overrides.databases ?? [database()];
  const users = overrides.users ?? [user()];

  return vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
    const url = String(input);
    if (url.includes('/auth/me')) {
      return mockProfile(overrides.permissions ?? ['database.manage']);
    }
    if (url.includes('/databases/engines')) {
      return envelopeResponse(overrides.enginesData ?? engines);
    }
    if (url.includes('/databases/console')) {
      return envelopeResponse(overrides.console ?? { installed: false, served: false, can_install: true });
    }
    if (/\/databases\/[^/]+$/.test(url)) {
      return envelopeResponse({ database: databases[0], users });
    }
    if (url.includes('/database-users')) {
      return envelopeResponse({ users, count: users.length });
    }
    return envelopeResponse({
      databases,
      count: databases.length,
      total_size_bytes: databases.reduce((sum, item) => sum + (item.size_bytes ?? 0), 0),
    });
  });
}

describe('DatabasesPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('lists the databases on the server', async () => {
    mockApi();
    renderWithProviders(<DatabasesPage />);

    expect(await screen.findByText('shop')).toBeInTheDocument();
    // MariaDB appears twice: once in the engine summary, once in the row.
    expect(screen.getAllByText('MariaDB').length).toBeGreaterThan(0);
    expect(screen.getByText('64 KB')).toBeInTheDocument();
  });

  it('names both engines, including the one that is not installed', async () => {
    mockApi();
    renderWithProviders(<DatabasesPage />);

    // An absent engine is named rather than hidden: hiding it makes the panel
    // look like it only knows about one, and leaves someone hunting.
    expect(await screen.findByText(/MariaDB 11\.4\.2/)).toBeInTheDocument();
    expect(screen.getByText(/PostgreSQL unavailable/)).toBeInTheDocument();
  });

  it('says so when the host runs no database server at all', async () => {
    mockApi({
      databases: [],
      enginesData: {
        engines: [
          {
            engine: 'mariadb',
            available: false,
            detail: 'no MySQL or MariaDB client is installed on this host',
            supports_host_patterns: true,
          },
        ],
        available: false,
        privileges: ['readonly', 'readwrite', 'full'],
      },
    });

    renderWithProviders(<DatabasesPage />);

    expect(await screen.findByText('This server runs no database engine')).toBeInTheDocument();
    expect(
      screen.getByText('no MySQL or MariaDB client is installed on this host'),
    ).toBeInTheDocument();
    // Offering a button that can only fail is worse than disabling it.
    expect(screen.getAllByRole('button', { name: /Add Database/ })[0]).toBeDisabled();
  });

  it('shows a database size of "Not measured" rather than zero', async () => {
    mockApi({ databases: [database({ size_bytes: null, size_checked_at: null })] });
    renderWithProviders(<DatabasesPage />);

    // 0 B and "never measured" are different facts, and only one is worth
    // investigating.
    expect(await screen.findByText('Not measured')).toBeInTheDocument();
  });

  it('opens a database in place and shows its tools and facts', async () => {
    mockApi();
    renderWithProviders(<DatabasesPage />);

    await screen.findByText('shop');
    await userEvent.click(screen.getByRole('button', { name: 'Expand shop' }));

    expect(await screen.findByText('Connection info')).toBeInTheDocument();
    // Export and import are buttons, not links to the Backups page. That is
    // the point of them: a backup is an archive of the host, restored whole,
    // and these are one database as a .sql file. Pointing them at /backups was
    // a different feature wearing the same word.
    expect(screen.getByRole('button', { name: /Export dump/ })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Export dump/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Import dump/ })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Import dump/ })).not.toBeInTheDocument();
    // What genuinely is not built says so as a fact about the panel, without
    // a phase number.
    expect(screen.getByText('This panel does not copy databases yet')).toBeInTheDocument();
    // The facts strip answers "who can reach this" without opening anything.
    expect(await screen.findByText('shop', { selector: 'dd' })).toBeInTheDocument();
  });

  it('shows the account that may reach a database on the users tab', async () => {
    mockApi();
    renderWithProviders(<DatabasesPage />);

    await screen.findByText('shop');
    await userEvent.click(screen.getByRole('tab', { name: 'User Management' }));

    expect(await screen.findByText('Full access')).toBeInTheDocument();
    // The account is named with the host it may connect from.
    expect(screen.getByText('@localhost')).toBeInTheDocument();
  });

  it('keeps a password hidden until it is asked for', async () => {
    mockApi();
    renderWithProviders(<DatabasesPage />);

    await screen.findByText('shop');
    await userEvent.click(screen.getByRole('tab', { name: 'User Management' }));

    // Reading a credential writes an audit record, so it must never happen as
    // a side effect of a page loading.
    const show = await screen.findByRole('button', { name: 'Show' });
    expect(show).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Copy password/ })).not.toBeInTheDocument();

    const calls = vi.mocked(globalThis.fetch).mock.calls.map((call) => String(call[0]));
    expect(calls.some((url) => /\/database-users\/[^/]+\/password$/.test(url))).toBe(false);
  });

  it('fetches the password only when Show is pressed', async () => {
    mockApi();
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['database.manage']);
      }
      if (url.includes('/databases/engines')) {
        return envelopeResponse(engines);
      }
      if (/\/database-users\/[^/]+\/password$/.test(url)) {
        return envelopeResponse({ password: 'Revealed-Password_1' });
      }
      if (url.includes('/databases/console')) {
        return envelopeResponse({ installed: false, served: false, can_install: true });
      }
      if (url.includes('/database-users')) {
        return envelopeResponse({ users: [user()], count: 1 });
      }
      if (/\/databases\/[^/]+$/.test(url)) {
        return envelopeResponse({ database: database(), users: [user()] });
      }
      return envelopeResponse({ databases: [database()], count: 1, total_size_bytes: 65536 });
    });

    renderWithProviders(<DatabasesPage />);
    await screen.findByText('shop');
    await userEvent.click(screen.getByRole('tab', { name: 'User Management' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Show' }));

    expect(await screen.findByText('Revealed-Password_1')).toBeInTheDocument();
  });

  it('hides every control from a reader without database.manage', async () => {
    mockApi({ permissions: ['website.view'] });
    renderWithProviders(<DatabasesPage />);

    // The list is still readable; nothing that changes the server is offered.
    expect(await screen.findByText('shop')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /Add Database/ })).not.toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /^Delete shop$/ })).not.toBeInTheDocument();
  });

  it('warns that other accounts lose access before deleting a database', async () => {
    mockApi({ databases: [database({ user_count: 2 })] });
    renderWithProviders(<DatabasesPage />);

    await screen.findByText('shop');
    await userEvent.click(screen.getByRole('button', { name: 'Delete shop' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/2 accounts have/)).toBeInTheDocument();
    expect(within(dialog).getByText(/cannot be undone/)).toBeInTheDocument();
  });

  it('links a database to the website it belongs to', async () => {
    mockApi({
      databases: [
        database({ website_id: 'site-1', website_domain: 'shop.example' }),
      ],
    });
    renderWithProviders(<DatabasesPage />);

    const link = await screen.findByRole('link', { name: /shop\.example/ });
    expect(link).toHaveAttribute('href', '/websites/site-1');
  });
});
