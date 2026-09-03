import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { DnsPage } from '@/pages/DnsPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { DNSOverview, DNSZone } from '@/types/api';

function zone(overrides: Partial<DNSZone> = {}): DNSZone {
  return {
    id: 'zone-1',
    server_id: 'server-1',
    name: 'example.com',
    kind: 'master',
    primary_ns: 'ns1.example.com.',
    hostmaster: 'hostmaster@example.com',
    serial: 1788390000,
    refresh: 3600,
    retry: 900,
    expire: 1209600,
    minimum: 3600,
    ttl: 3600,
    nameservers: ['ns1.example.com.', 'ns2.example.com.'],
    dnssec: false,
    allow_transfer: [],
    also_notify: [],
    masters: [],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    record_count: 4,
    ...overrides,
  };
}

function overview(overrides: Partial<DNSOverview> = {}): DNSOverview {
  return {
    available: true,
    running: true,
    can_install: false,
    supports_dnssec: true,
    version: '9.18.49',
    reason: '',
    config_path: '/etc/bind/named.conf',
    include_path: '/etc/bind/jothost.conf',
    zone_dir: '/var/bind/jothost',
    managed_config: true,
    config_included: true,
    recursion: false,
    listen_on: ['203.0.113.10'],
    zones: [zone()],
    host_zones: ['example.com'],
    firewall_open: true,
    firewall_reason: '',
    warnings: null,
    settings: {
      server_id: 'server-1',
      listen_on: ['203.0.113.10'],
      allow_transfer: [],
      dnssec_policy: 'default',
      default_ns: ['ns1.example.com.', 'ns2.example.com.'],
      default_ttl: 3600,
      hostmaster: 'hostmaster@example.com',
    },
    providers: [],
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
    roles: ['admin'],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('DnsPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: DNSOverview;
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'dns.manage']);
      }
      if (init?.method && init.method !== 'GET') {
        options.onWrite?.(url, init);
        if (options.refuse) {
          return new Response(
            JSON.stringify({
              success: false,
              error: { code: 'VALIDATION_FAILED', message: options.refuse },
              request_id: 'req_test',
            }),
            { status: 422, headers: { 'Content-Type': 'application/json' } },
          );
        }
        return envelopeResponse({ id: 'zone-2', name: 'new.example.com' });
      }
      // A single-zone read has a shape of its own. The editor opens straight
      // after a create, so this is not hypothetical.
      if (/\/dns\/zones\/[^/]+$/.test(url)) {
        return envelopeResponse({
          zone: { ...zone(), records: [] },
          state: {
            zone: 'example.com',
            type: 'primary',
            serial: 1788390000,
            signed_serial: 0,
            secure: false,
            last_loaded: 'Wed, 02 Sep 2026 22:46:44 GMT',
            last_transfer: '',
            loaded: true,
            reason: '',
          },
          signing: { zone: 'example.com', policy: '', keys: null, ds: null, reason: '' },
        });
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('lists each zone with its record count and serial', async () => {
    mockApi({});
    renderWithProviders(<DnsPage />);

    expect(await screen.findByText('example.com')).toBeInTheDocument();
    expect(screen.getByText(/4 records · serial 1788390000/)).toBeInTheDocument();
  });

  it('marks a signed zone and a secondary for what they are', async () => {
    mockApi({
      overview: overview({
        zones: [
          zone({ dnssec: true }),
          zone({
            id: 'zone-2',
            name: 'mirror.example.com',
            kind: 'slave',
            masters: ['198.51.100.5'],
            record_count: 0,
          }),
        ],
        host_zones: ['example.com', 'mirror.example.com'],
      }),
    });
    renderWithProviders(<DnsPage />);

    expect(await screen.findByText('Signed')).toBeInTheDocument();
    expect(screen.getByText('Secondary')).toBeInTheDocument();
    expect(screen.getByText(/Transferred from 198\.51\.100\.5/)).toBeInTheDocument();
  });

  it('says when the firewall is closed, and that it will not open it', async () => {
    // A closed port 53 produces no error anywhere: the panel's checks pass, dig
    // from the server answers, and the zone is simply invisible to the world.
    mockApi({
      overview: overview({
        firewall_open: false,
        firewall_reason: 'the firewall is on and no rule allows port 53',
      }),
    });
    renderWithProviders(<DnsPage />);

    expect(await screen.findByText('Port 53 is closed')).toBeInTheDocument();
    expect(screen.getByText(/one click away on the Firewall page/)).toBeInTheDocument();
  });

  it('warns when the name server is not reading the panel’s zones', async () => {
    // Everything the panel recorded is on disk and served by nobody, and
    // nothing else on the page would look wrong.
    mockApi({ overview: overview({ config_included: false }) });
    renderWithProviders(<DnsPage />);

    expect(
      await screen.findByText("The name server is not reading the panel's zones"),
    ).toBeInTheDocument();
  });

  it('passes the host’s own warnings through', async () => {
    mockApi({
      overview: overview({
        warnings: ['this name server is configured to answer recursive queries'],
      }),
    });
    renderWithProviders(<DnsPage />);

    expect(await screen.findByText(/answer recursive queries/)).toBeInTheDocument();
  });

  it('names the zones the host serves that the panel does not manage', async () => {
    mockApi({
      overview: overview({ host_zones: ['example.com', 'legacy.example.net'] }),
    });
    renderWithProviders(<DnsPage />);

    expect(await screen.findByText(/legacy\.example\.net/)).toBeInTheDocument();
  });

  it('offers to install a name server on a host without one', async () => {
    mockApi({
      overview: overview({
        available: false,
        running: false,
        can_install: true,
        zones: [],
        host_zones: [],
      }),
    });
    renderWithProviders(<DnsPage />);

    expect(await screen.findByText('No DNS server is installed')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Install DNS Server' })).toBeEnabled();
  });

  it('creates a zone and sends the name that was typed', async () => {
    const writes: Array<{ url: string; body: unknown }> = [];
    mockApi({
      onWrite: (url, init) => {
        writes.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null });
      },
    });
    renderWithProviders(<DnsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add Zone/ }));
    await userEvent.type(await screen.findByLabelText('Zone'), 'new.example.com');
    await userEvent.click(screen.getByRole('button', { name: 'Create zone' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.url).toContain('/dns/zones');
    expect(writes[0]?.body).toMatchObject({ name: 'new.example.com' });
  });

  it('asks for a network rather than a name for a reverse zone', async () => {
    // A reverse zone whose name does not match its network is one nobody ever
    // queries, so the name is derived and never typed.
    const writes: Array<{ body: unknown }> = [];
    mockApi({ onWrite: (_url, init) => writes.push({ body: JSON.parse(String(init?.body)) }) });
    renderWithProviders(<DnsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add Zone/ }));
    await userEvent.selectOptions(await screen.findByLabelText('Kind'), 'reverse');
    await userEvent.type(screen.getByLabelText('Network'), '203.0.113.0/24');
    await userEvent.click(screen.getByRole('button', { name: 'Create zone' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.body).toMatchObject({ reverse_network: '203.0.113.0/24' });
  });

  it('warns before creating a zone when no name servers are set', async () => {
    // The panel will not invent them: a zone delegated to nothing is a zone no
    // resolver can be sent to.
    mockApi({
      overview: overview({
        settings: { ...overview().settings, default_ns: [] },
      }),
    });
    renderWithProviders(<DnsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add Zone/ }));
    expect(await screen.findByText('No default name servers are set')).toBeInTheDocument();
  });

  it('shows the reason a zone was refused', async () => {
    mockApi({ refuse: 'this server already serves that zone' });
    renderWithProviders(<DnsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add Zone/ }));
    await userEvent.type(await screen.findByLabelText('Zone'), 'example.com');
    await userEvent.click(screen.getByRole('button', { name: 'Create zone' }));

    expect(await screen.findByText('this server already serves that zone')).toBeInTheDocument();
  });

  it('confirms before a zone stops being served', async () => {
    const writes: string[] = [];
    mockApi({ onWrite: (url) => writes.push(url) });
    renderWithProviders(<DnsPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Delete example.com' }));
    expect(await screen.findByText('Stop serving example.com?')).toBeInTheDocument();
    // Nothing has been sent yet.
    expect(writes).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Delete zone' }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0] ?? '').toContain('/dns/zones/zone-1');
  });

  it('saves the name servers every new zone starts from', async () => {
    const writes: Array<{ url: string; body: unknown }> = [];
    mockApi({
      onWrite: (url, init) => {
        writes.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null });
      },
    });
    renderWithProviders(<DnsPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Save settings' }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.url).toContain('/dns/settings');
    expect(writes[0]?.body).toMatchObject({
      default_ns: ['ns1.example.com.', 'ns2.example.com.'],
      hostmaster: 'hostmaster@example.com',
    });
  });
});
