import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { WebmailCard } from '@/features/mail/components/WebmailCard';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Job, MailOverview, Website } from '@/types/api';

function site(overrides: Partial<Website> = {}): Website {
  return {
    id: 'site-1',
    server_id: 'server-1',
    name: null,
    primary_domain: 'webmail.example.com',
    document_root: '/var/www/webmail.example.com/public',
    system_user: 'web_webmail',
    php_version: '8.3',
    status: 'active',
    ssl_enabled: true,
    https_redirect: true,
    parent_website_id: null,
    apache_port: null,
    allow_override: false,
    nginx_directives: '',
    document_root_mode: null,
    php_pool_mode: null,
    system_user_mode: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function overview(settings: Partial<MailOverview['settings']> = {}, webmailDetail?: string): MailOverview {
  return {
    settings: {
      server_id: 'server-1',
      enabled: true,
      hostname: 'mail.example.com',
      require_tls: true,
      spam_enabled: true,
      spam_reject_score: 15,
      virus_enabled: false,
      max_message_mb: 25,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      ...settings,
    },
    status: {
      available: true,
      can_install: true,
      postfix: { installed: true, running: true },
      dovecot: { installed: true, running: true },
      rspamd: { installed: true, running: true },
      antivirus: { installed: false, running: false },
      antivirus_present: false,
      hostname: 'mail.example.com',
      tls: { configured: true },
      ports: [],
      queue_length: 0,
      queue_oldest_seconds: 0,
      open_relay: { checked: true, open: false, detail: '' },
      signing: [],
      map_type: 'lmdb',
      webmail: webmailDetail ? { installed: true, detail: webmailDetail } : undefined,
    },
    domains: [],
    mailboxes: 0,
    aliases: 0,
  } as MailOverview;
}

function job(status: Job['status'], extra: Partial<Job> = {}): Job {
  return {
    id: 'job-1',
    type: 'webmail.install',
    status,
    payload: { domain: 'webmail.example.com' },
    progress: status === 'RUNNING' ? 40 : 100,
    created_by: 'user-1',
    resource_type: 'webmail',
    resource_id: 'site-1',
    created_at: '2026-01-01T00:00:00Z',
    started_at: null,
    completed_at: null,
    ...extra,
  };
}

function profile(permissions: string[]) {
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

describe('WebmailCard', () => {
  let posted: unknown[];

  function mockApi({
    websites = [site()],
    permissions = ['mail.view', 'mail.manage', 'website.view', 'server.view'],
    jobResult = job('SUCCESS'),
  }: { websites?: Website[]; permissions?: string[]; jobResult?: Job } = {}) {
    posted = [];
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) return profile(permissions);
      if (url.endsWith('/mail/webmail') && init?.method === 'POST') {
        posted.push(JSON.parse(String(init.body)));
        return envelopeResponse({ job: job('PENDING') }, 202);
      }
      if (url.includes('/jobs/')) return envelopeResponse(jobResult);
      if (url.includes('/websites')) return envelopeResponse({ websites, count: websites.length });
      return envelopeResponse({});
    });
  }

  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('installs into the chosen site only after saying its content will be replaced', async () => {
    mockApi({ websites: [site({ id: 'site-9', primary_domain: 'blog.example.com' }), site()] });
    renderWithProviders(<WebmailCard overview={overview()} />);

    const user = userEvent.setup();
    await user.selectOptions(await screen.findByLabelText('Website'), 'site-1');
    await user.click(screen.getByRole('button', { name: 'Install webmail' }));

    // The warning comes before anything is sent: installing deletes what the
    // site served until now.
    expect(await screen.findByText(/document root is replaced by webmail/)).toBeInTheDocument();
    expect(screen.getByText('/var/www/webmail.example.com/public')).toBeInTheDocument();
    expect(posted).toHaveLength(0);

    const dialog = screen.getByRole('dialog');
    await user.click(
      Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent === 'Install webmail')!,
    );

    await waitFor(() => expect(posted).toEqual([{ website_id: 'site-1' }]));
    expect(await screen.findByText('Webmail is installed')).toBeInTheDocument();
  });

  it('says why an install failed, and that nothing was recorded', async () => {
    mockApi({
      jobResult: job('FAILED', {
        error: 'the webmail download did not match its expected checksum',
      }),
    });
    renderWithProviders(<WebmailCard overview={overview()} />);

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Install webmail' }));
    const dialog = await screen.findByRole('dialog');
    await user.click(
      Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent === 'Install webmail')!,
    );

    expect(await screen.findByText('Webmail was not installed')).toBeInTheDocument();
    expect(screen.getByText(/did not match its expected checksum/)).toBeInTheDocument();
  });

  it('warns before webmail is served without PHP or without a certificate', async () => {
    // Either way "installed" means a page nobody can use — or one that sends
    // every mailbox password over the network in the clear.
    mockApi({ websites: [site({ php_version: null, ssl_enabled: false })] });
    renderWithProviders(<WebmailCard overview={overview()} />);

    expect(await screen.findByText('PHP is off for this site')).toBeInTheDocument();
    expect(screen.getByText('This site has no certificate')).toBeInTheDocument();
  });

  it('shows where installed webmail is served, and offers removal', async () => {
    mockApi();
    renderWithProviders(
      <WebmailCard overview={overview({ webmail_website_id: 'site-1', webmail_version: '1.6.9' })} />,
    );

    const link = await screen.findByRole('link', { name: 'https://webmail.example.com/' });
    expect(link).toHaveAttribute('href', 'https://webmail.example.com/');
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reinstall webmail' })).toBeInTheDocument();
  });

  it('offers a person who may only read mail nothing to change', async () => {
    mockApi({ permissions: ['mail.view', 'website.view'] });
    renderWithProviders(
      <WebmailCard overview={overview({ webmail_website_id: 'site-1', webmail_version: '1.6.9' })} />,
    );

    expect(await screen.findByRole('link', { name: 'https://webmail.example.com/' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /install webmail/i })).not.toBeInTheDocument();
  });
});
