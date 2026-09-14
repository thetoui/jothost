import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { MailPage } from '@/pages/MailPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { MailDomain, MailOverview, MailStatus } from '@/types/api';

function status(overrides: Partial<MailStatus> = {}): MailStatus {
  return {
    available: true,
    can_install: true,
    postfix: { installed: true, running: true, version: '3.9.14' },
    dovecot: { installed: true, running: true, version: '2.3.21.1' },
    rspamd: { installed: true, running: true, version: '3.10.2' },
    antivirus: { installed: false, running: false, detail: 'virus scanning is turned off' },
    // The package is not on this host. Distinct from the line above, which is
    // false whenever scanning is switched off however complete the install.
    antivirus_present: false,
    hostname: 'mail.example.com',
    tls: { configured: true, certificate_path: '/etc/jothost/ssl/example.com/fullchain.pem' },
    ports: [
      { name: 'SMTP', port: 25, configured: true, listening: true, requires_tls: false },
      {
        name: 'Submission (STARTTLS)',
        port: 587,
        configured: true,
        listening: true,
        requires_tls: true,
      },
    ],
    queue_length: 0,
    queue_oldest_seconds: 0,
    open_relay: { checked: true, open: false, detail: 'refused as it should be: 554 5.7.1' },
    signing: ['example.com'],
    map_type: 'lmdb',
    warnings: [],
    ...overrides,
  };
}

function domain(overrides: Partial<MailDomain> = {}): MailDomain {
  return {
    id: 'domain-1',
    server_id: 'server-1',
    domain: 'example.com',
    active: true,
    catch_all: '',
    dkim_selector: 'jh2026090410',
    dkim_public_key: 'MIIBIjANBg',
    spf_policy: 'soft',
    dmarc_policy: 'none',
    dmarc_rua: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    mailboxes: 2,
    aliases: 1,
    signing: true,
    dns_managed: true,
    mx_published: true,
    spf_published: true,
    dkim_published: true,
    dmarc_published: true,
    ...overrides,
  };
}

function overview(overrides: Partial<MailOverview> = {}): MailOverview {
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
    },
    status: status(),
    domains: [domain()],
    mailboxes: 2,
    aliases: 1,
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

describe('MailPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(data: MailOverview) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['mail.view', 'mail.manage']);
      }
      if (url.includes('/mailboxes')) {
        return envelopeResponse({ mailboxes: [] });
      }
      if (url.includes('/aliases')) {
        return envelopeResponse({ aliases: [] });
      }
      return envelopeResponse(data);
    });
  }

  it('offers to install the scanner when the package is missing', async () => {
    mockApi(overview({ status: status({ antivirus_present: false }) }));
    renderWithProviders(<MailPage />);

    expect(await screen.findByText(/no virus scanner/)).toBeInTheDocument();
  });

  it('offers no install once the scanner is on the host', async () => {
    // The state the panel could not tell from the one above. Scanning is
    // still switched off here - antivirus.installed is false in both - so
    // keying the offer on that showed a several-hundred-megabyte download to
    // an operator who only needed to turn a toggle on, and went on showing it
    // after the install had finished.
    mockApi(overview({ status: status({ antivirus_present: true }) }));
    renderWithProviders(<MailPage />);

    await screen.findByText('The mail server');
    expect(screen.queryByText(/no virus scanner/)).not.toBeInTheDocument();
  });

  it('leads with an open relay, because it is the one failure that takes every customer offline', async () => {
    mockApi(
      overview({
        status: status({
          open_relay: {
            checked: true,
            open: true,
            detail:
              'this server accepted mail for a domain it does not host, from an unauthenticated client',
          },
        }),
      }),
    );
    renderWithProviders(<MailPage />);

    expect(await screen.findByText('This server is an open relay')).toBeInTheDocument();
  });

  it('says when the relay check could not run rather than reporting a pass', async () => {
    // "Not checked" and "checked and safe" are different facts, and only one of
    // them means the server is not relaying.
    mockApi(
      overview({
        status: status({
          open_relay: {
            checked: false,
            open: false,
            detail: 'the panel could not find a non-loopback address to test from',
          },
        }),
      }),
    );
    renderWithProviders(<MailPage />);

    expect(await screen.findByText('The relay check could not run')).toBeInTheDocument();
    expect(
      screen.getByText(/could not find a non-loopback address/),
    ).toBeInTheDocument();
  });

  it('shows a domain whose records are published as published', async () => {
    mockApi(overview());
    renderWithProviders(<MailPage />);

    expect(await screen.findByText('example.com')).toBeInTheDocument();
    expect(screen.getByText('MX published')).toBeInTheDocument();
    expect(screen.getByText('DKIM published')).toBeInTheDocument();
  });

  it('says a domain signs with a key nobody can fetch', async () => {
    // The phase's central failure. The host signs every message correctly, the
    // signature fails to verify everywhere, and nothing on this machine reports
    // an error.
    mockApi(
      overview({
        domains: [
          domain({
            dkim_published: false,
            problems: [
              'this domain signs its mail with a key that is not published in DNS, so ' +
                'every signature fails to verify — which is worse than not signing',
            ],
          }),
        ],
      }),
    );
    renderWithProviders(<MailPage />);

    expect(await screen.findByText('DKIM not published')).toBeInTheDocument();
    expect(screen.getByText(/every signature fails to verify/)).toBeInTheDocument();
  });

  it('says it cannot check a domain whose DNS is somewhere else', async () => {
    // "Not known" and "not published" are different answers: one is a fault and
    // the other is a limit of what this host can see.
    mockApi(overview({ domains: [domain({ dns_managed: false, problems: [] })] }));
    renderWithProviders(<MailPage />);

    expect(
      await screen.findByText(/does not serve DNS for example.com/),
    ).toBeInTheDocument();
    expect(screen.queryByText('MX published')).not.toBeInTheDocument();
  });

  it('distinguishes a configured port from one that is actually listening', async () => {
    // They differ when a daemon failed to start, which is exactly the case the
    // page has to be able to show.
    mockApi(
      overview({
        status: status({
          dovecot: { installed: true, running: false, detail: 'installed and not running' },
          ports: [
            { name: 'SMTP', port: 25, configured: true, listening: true, requires_tls: false },
            {
              name: 'IMAP (implicit TLS)',
              port: 993,
              configured: true,
              listening: false,
              requires_tls: true,
            },
          ],
          warnings: [
            'mail is being accepted and Dovecot is not running, so nothing is being ' +
              'delivered into a mailbox and nobody can read their mail',
          ],
        }),
      }),
    );
    renderWithProviders(<MailPage />);

    expect(await screen.findByText('25 · SMTP')).toBeInTheDocument();
    expect(
      screen.getByTitle('IMAP (implicit TLS) is configured and nothing is listening'),
    ).toHaveTextContent('993');
    expect(screen.getByText(/nobody can read their mail/)).toBeInTheDocument();
  });

  it('offers to install a mail server rather than showing an empty page', async () => {
    mockApi(
      overview({
        status: status({
          available: false,
          reason: 'this host has no mail server installed',
        }),
        domains: [],
      }),
    );
    renderWithProviders(<MailPage />);

    expect(await screen.findByText('This host has no mail server')).toBeInTheDocument();
  });
});
