import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { SubdomainPanel } from '@/features/websites/components/SubdomainPanel';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Website } from '@/types/api';

function website(overrides: Partial<Website> = {}): Website {
  return {
    id: 'parent-id',
    server_id: 'server-1',
    name: null,
    primary_domain: 'example.test',
    document_root: '/var/www/example.test/public',
    system_user: 'web_example_test_a1b2c3',
    php_version: '8.3',
    status: 'active',
    ssl_enabled: false,
    https_redirect: false,
    parent_website_id: null,
    apache_port: null,
    allow_override: true,
    nginx_directives: '',
    document_root_mode: null,
    php_pool_mode: null,
    system_user_mode: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function subdomain(overrides: Partial<Website> = {}): Website {
  return website({
    id: 'sub-id',
    primary_domain: 'shop.example.test',
    document_root: '/var/www/example.test/shop.example.test/public',
    parent_website_id: 'parent-id',
    document_root_mode: 'nested',
    php_pool_mode: 'inherit',
    system_user_mode: 'inherit',
    ...overrides,
  });
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

describe('SubdomainPanel', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(subdomains: Website[], onPost?: (body: unknown) => void) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['website.view', 'website.create', 'website.delete']);
      }
      if (url.includes('/subdomains') && init?.method === 'POST') {
        onPost?.(JSON.parse(String(init.body)));
        return envelopeResponse({ website: subdomain(), job: null }, 201);
      }
      return envelopeResponse({ subdomains, count: subdomains.length });
    });
  }

  it('lists the sites beneath a website with where their files live', async () => {
    mockApi([subdomain()]);
    renderWithProviders(<SubdomainPanel site={website()} />);

    const row = await screen.findByText('shop.example.test');
    // The facts are behind the row, so a site with twenty subdomains is a list
    // rather than a wall of paths.
    expect(
      screen.queryByText('/var/www/example.test/shop.example.test/public'),
    ).not.toBeInTheDocument();

    await userEvent.click(row);
    expect(
      await screen.findByText('/var/www/example.test/shop.example.test/public'),
    ).toBeInTheDocument();
    expect(screen.getByText("The parent's pool")).toBeInTheDocument();
  });

  // The name is a label, not a hostname: the API derives the full name from
  // the parent, which is what stops a site being created under someone else's
  // domain. The form shows what will be created so that is not a surprise.
  it('previews the full hostname while the label is typed', async () => {
    mockApi([]);
    renderWithProviders(<SubdomainPanel site={website()} />);

    await userEvent.click(await screen.findByRole('button', { name: /Add subdomain/ }));
    await userEvent.type(screen.getByLabelText('Name'), 'shop');

    expect(await screen.findByText('shop.example.test')).toBeInTheDocument();
  });

  it('sends the label and the three modes', async () => {
    const posted: unknown[] = [];
    mockApi([], (body) => posted.push(body));
    renderWithProviders(<SubdomainPanel site={website()} />);

    await userEvent.click(await screen.findByRole('button', { name: /Add subdomain/ }));
    await userEvent.type(screen.getByLabelText('Name'), 'shop');
    await userEvent.selectOptions(screen.getByLabelText('Files'), 'isolated');
    await userEvent.click(screen.getByRole('button', { name: 'Create subdomain' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({
      name: 'shop',
      document_root_mode: 'isolated',
      php_pool_mode: 'inherit',
      system_user_mode: 'inherit',
    });
  });

  // A dedicated account with the parent's pool runs PHP as one user over files
  // owned by another. The API refuses it; the form moves the pool with the
  // account rather than letting someone find out by being refused.
  it('gives a subdomain with its own account its own pool', async () => {
    mockApi([]);
    renderWithProviders(<SubdomainPanel site={website()} />);

    await userEvent.click(await screen.findByRole('button', { name: /Add subdomain/ }));
    await userEvent.selectOptions(screen.getByLabelText('System account'), 'dedicated');

    const pool = screen.getByLabelText('PHP pool') as HTMLSelectElement;
    expect(pool.value).toBe('dedicated');
    expect(pool).toBeDisabled();
  });

  // A wildcard answers for every name beneath the parent that nothing else
  // claims, so there is no single address to open.
  it('marks a wildcard as a catch-all and offers no link to open it', async () => {
    mockApi([subdomain({ id: 'wild', primary_domain: '*.example.test' })]);
    renderWithProviders(<SubdomainPanel site={website()} />);

    expect(await screen.findByText('Catch-all')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Open/ })).not.toBeInTheDocument();
  });
});
