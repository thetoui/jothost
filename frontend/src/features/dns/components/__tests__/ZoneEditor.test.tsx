import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { ZoneEditor } from '@/features/dns/components/ZoneEditor';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { DNSRecord, DNSZoneDetail } from '@/types/api';

function record(overrides: Partial<DNSRecord> = {}): DNSRecord {
  return {
    id: 'record-1',
    zone_id: 'zone-1',
    name: 'www',
    type: 'A',
    ttl: 0,
    value: '203.0.113.10',
    priority: 0,
    weight: 0,
    port: 0,
    flags: 0,
    tag: '',
    provider: 'local',
    managed: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function detail(overrides: Partial<DNSZoneDetail> = {}): DNSZoneDetail {
  return {
    zone: {
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
      nameservers: ['ns1.example.com.'],
      dnssec: false,
      allow_transfer: [],
      also_notify: [],
      masters: [],
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      record_count: 1,
      records: [record()],
    },
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
    ...overrides,
  };
}

function mockProfile() {
  return envelopeResponse({
    id: 'user-1',
    username: 'admin',
    email: null,
    status: 'active',
    two_factor_enabled: false,
    roles: ['admin'],
    permissions: ['server.view', 'dns.manage'],
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  });
}

describe('ZoneEditor', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    detail?: DNSZoneDetail;
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile();
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
        return envelopeResponse({ ok: true });
      }
      if (url.includes('/dns/zones/')) {
        return envelopeResponse(options.detail ?? detail());
      }
      // The overview, for the signing and provider sections.
      return envelopeResponse({
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
        listen_on: [],
        zones: [],
        host_zones: [],
        firewall_open: true,
        firewall_reason: '',
        warnings: null,
        settings: {
          server_id: 'server-1',
          listen_on: [],
          allow_transfer: [],
          dnssec_policy: 'default',
          default_ns: [],
          default_ttl: 3600,
          hostmaster: '',
        },
        providers: [],
      });
    });
  }

  it('shows a record with the zone default standing in for a TTL of zero', async () => {
    // Zero means "the zone's default", which is a number the operator can act
    // on. Printing "0" would say the opposite of what it means.
    mockApi({});
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(await screen.findByText('www')).toBeInTheDocument();
    expect(screen.getByText('3600 (zone)')).toBeInTheDocument();
  });

  it('renders a composite record the way a zone file would', async () => {
    mockApi({
      detail: (() => {
        const base = detail();
        base.zone.records = [
          record({ id: 'r2', name: '@', type: 'MX', priority: 10, value: 'mail.example.com.' }),
          record({
            id: 'r3',
            name: '_sip._tcp',
            type: 'SRV',
            priority: 10,
            weight: 5,
            port: 5060,
            value: 'sip.example.com.',
          }),
        ];
        return base;
      })(),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(await screen.findByText('10 mail.example.com.')).toBeInTheDocument();
    expect(screen.getByText('10 5 5060 sip.example.com.')).toBeInTheDocument();
  });

  it('shows a managed record and offers no way to edit it', async () => {
    // A subdomain's record in its parent's zone. Editing it by hand would leave
    // the panel and the zone disagreeing about a name the panel maintains.
    mockApi({
      detail: (() => {
        const base = detail();
        base.zone.records = [record({ name: 'shop', managed: true })];
        return base;
      })(),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(await screen.findByText('managed')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Edit shop/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Delete shop/ })).not.toBeInTheDocument();
  });

  it('keeps the two serials apart on a signed zone', async () => {
    // Measured, not assumed: with inline signing named keeps its own serial on
    // the signed copy and it runs ahead. One number would look like drift.
    mockApi({
      detail: (() => {
        const base = detail();
        base.zone.dnssec = true;
        base.state.signed_serial = 1788390004;
        base.state.secure = true;
        return base;
      })(),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(await screen.findByText('1788390000')).toBeInTheDocument();
    expect(screen.getByText('1788390004')).toBeInTheDocument();
    expect(screen.getByText('(signing keeps its own)')).toBeInTheDocument();
  });

  it('shows the DS record to give the registrar', async () => {
    // Until it is in the parent zone the signatures mean nothing to any
    // resolver, and it is the one step the name server cannot do itself.
    mockApi({
      detail: (() => {
        const base = detail();
        base.zone.dnssec = true;
        base.signing = {
          zone: 'example.com',
          policy: 'default',
          keys: [
            {
              id: 4079,
              algorithm: 'ECDSAP256SHA256',
              role: 'CSK',
              published: true,
              key_signing: true,
              zone_signing: true,
              rollover: 'No rollover scheduled',
            },
          ],
          ds: [
            {
              key_tag: 4079,
              algorithm: 13,
              digest_type: 2,
              digest: '814CBB08',
              record: 'example.com. IN DS 4079 13 2 814CBB08',
            },
          ],
          reason: '',
        };
        return base;
      })(),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(await screen.findByText('Give this to the registrar')).toBeInTheDocument();
    expect(screen.getByText('example.com. IN DS 4079 13 2 814CBB08')).toBeInTheDocument();
    expect(screen.getByText(/Key 4079 \(ECDSAP256SHA256, CSK\)/)).toBeInTheDocument();
  });

  it('refuses to edit a secondary zone’s records', async () => {
    mockApi({
      detail: (() => {
        const base = detail();
        base.zone.kind = 'slave';
        base.zone.masters = ['198.51.100.5'];
        return base;
      })(),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(
      await screen.findByText("This zone's records come from its primary"),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add record' })).not.toBeInTheDocument();
  });

  it('says when the server is not serving a zone the panel has', async () => {
    mockApi({
      detail: (() => {
        const base = detail();
        base.state.loaded = false;
        base.state.reason = 'not found';
        return base;
      })(),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    expect(
      await screen.findByText('The name server is not serving this zone'),
    ).toBeInTheDocument();
  });

  it('sends an SRV record with all three of its numbers', async () => {
    const writes: Array<{ url: string; body: Record<string, unknown> }> = [];
    mockApi({
      onWrite: (url, init) => writes.push({ url, body: JSON.parse(String(init?.body)) }),
    });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    await userEvent.click(await screen.findByRole('button', { name: 'Add record' }));
    await userEvent.selectOptions(await screen.findByLabelText('Type'), 'SRV');
    await userEvent.clear(screen.getByLabelText('Name'));
    await userEvent.type(screen.getByLabelText('Name'), '_sip._tcp');
    await userEvent.type(screen.getByLabelText('Target'), 'sip.example.com.');
    await userEvent.clear(screen.getByLabelText('Port'));
    await userEvent.type(screen.getByLabelText('Port'), '5060');
    await userEvent.click(screen.getByRole('button', { name: 'Save record' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.url).toContain('/dns/zones/zone-1/records');
    expect(writes[0]?.body).toMatchObject({
      name: '_sip._tcp',
      type: 'SRV',
      port: 5060,
      value: 'sip.example.com.',
    });
  });

  it('shows the reason a record was refused', async () => {
    mockApi({ refuse: 'a CNAME must be the only record at its name' });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    await userEvent.click(await screen.findByRole('button', { name: 'Add record' }));
    await userEvent.type(await screen.findByLabelText('Address'), '203.0.113.9');
    await userEvent.click(screen.getByRole('button', { name: 'Save record' }));

    expect(
      await screen.findByText('a CNAME must be the only record at its name'),
    ).toBeInTheDocument();
  });

  it('saves who may transfer the zone', async () => {
    const writes: Array<{ body: Record<string, unknown> }> = [];
    mockApi({ onWrite: (_url, init) => writes.push({ body: JSON.parse(String(init?.body)) }) });
    renderWithProviders(<ZoneEditor zoneId="zone-1" onClose={() => {}} />);

    await userEvent.type(await screen.findByLabelText('Allow transfers to'), '198.51.100.5');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.body).toMatchObject({ allow_transfer: ['198.51.100.5'] });
  });
});
