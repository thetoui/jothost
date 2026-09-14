import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { byteLabel, fullness, limitLabel } from '@/features/tenancy/format';
import { TenancyPage } from '@/pages/TenancyPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Subscription, TenancyHostStatus, TenancyOverview } from '@/types/api';

function subscription(overrides: Partial<Subscription> = {}): Subscription {
  return {
    id: 'sub-1',
    owner_user_id: 'user-1',
    owner_username: 'acme',
    plan_id: 'plan-1',
    plan_name: 'Starter',
    name: 'Acme hosting',
    status: 'active',
    suspended_reason: '',
    suspended_at: null,
    slice_name: 'jothost-sub-1.slice',
    isolation_state: 'declared',
    isolation_detail: 'this host does not run systemd',
    isolation_applied_at: null,
    enforcement: 'hard',
    limits: {
      disk_mb: 1024,
      bandwidth_mb: null,
      max_websites: 2,
      // Zero, not null: this plan includes no databases at all, which is the
      // opposite promise from "no limit on databases".
      max_databases: 0,
      max_mailboxes: null,
      max_ftp_users: 5,
      max_cron_jobs: 5,
      max_subdomains: 5,
    },
    isolation: { cpu_percent: 50, memory_mb: 512, io_weight: null },
    addons: [],
    usage: {
      websites: 2,
      databases: 0,
      mailboxes: 0,
      ftp_users: 1,
      cron_jobs: 0,
      subdomains: 0,
      disk_bytes: null,
      bandwidth_bytes: null,
      period_start: null,
      measured_at: null,
      measure_error: '',
    },
    websites: [],
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...overrides,
  };
}

function overview(overrides: Partial<TenancyOverview> = {}): TenancyOverview {
  return {
    accounts: [
      {
        id: 'user-1',
        username: 'acme',
        email: null,
        tier: 'customer',
        parent_id: 'admin-1',
        parent_username: 'admin',
        full_name: null,
        company: null,
        status: 'active',
        roles: [],
        created_at: '2026-09-01T00:00:00Z',
        last_login_at: null,
        subscriptions: 1,
      },
    ],
    plans: [],
    subscriptions: [subscription()],
    host: {
      isolation_available: false,
      isolation_detail:
        'this host does not run systemd, so a resource limit cannot be applied',
      placement: false,
    },
    actor: { tier: 'admin', user_id: 'admin-1', impersonated: false },
    ...overrides,
  };
}

function mockProfile(permissions: string[]) {
  return envelopeResponse({
    id: 'admin-1',
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

describe('TenancyPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(data: TenancyOverview = overview()) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['tenant.view', 'tenant.manage', 'tenant.impersonate']);
      }
      if (url.includes('/tenancy/impersonation')) {
        return envelopeResponse({ impersonation: null });
      }
      return envelopeResponse(data);
    });
  }

  it('says plainly that the host is not enforcing the resource limits', async () => {
    // The distinction this whole phase turns on: written down is not applied.
    // A page that showed a tick here would be claiming enforcement nothing on
    // the host is performing.
    mockApi();
    renderWithProviders(<TenancyPage />);

    expect(
      await screen.findByText('Resource limits are recorded, not enforced'),
    ).toBeInTheDocument();
    expect(await screen.findByText('Recorded, not enforced')).toBeInTheDocument();
  });

  it('warns when a slice exists and nothing runs inside it', async () => {
    // Applied and empty is still a limit on nothing, and the two facts are
    // reported apart so the page cannot imply one from the other.
    const host: TenancyHostStatus = {
      isolation_available: true,
      isolation_detail: '',
      placement: false,
    };
    mockApi(overview({ host }));
    renderWithProviders(<TenancyPage />);

    expect(
      await screen.findByText('Nothing runs inside the subscription slices yet'),
    ).toBeInTheDocument();
  });

  it('shows what a subscription uses of what it was sold', async () => {
    mockApi();
    renderWithProviders(<TenancyPage />);

    expect(await screen.findByText('Acme hosting')).toBeInTheDocument();
    expect(screen.getByText('2 / 2')).toBeInTheDocument();
    // None, not zero: the plan includes no databases, and "0 / 0" would read
    // as an unset limit rather than a deliberate one.
    expect(screen.getByText('0 / None')).toBeInTheDocument();
    // Unlimited mailboxes, which is the opposite of the line above.
    expect(screen.getByText('0 / Unlimited')).toBeInTheDocument();
  });

  it('says a measurement has not happened rather than showing nothing used', async () => {
    mockApi();
    renderWithProviders(<TenancyPage />);

    expect(
      await screen.findByText('Disk and bandwidth have not been measured yet.'),
    ).toBeInTheDocument();
    expect(screen.getAllByText(/Not measured/).length).toBeGreaterThan(0);
  });

  it('names why a subscription is suspended', async () => {
    mockApi(
      overview({
        subscriptions: [
          subscription({
            status: 'suspended',
            suspended_reason: 'unpaid invoice 42',
            suspended_at: '2026-09-02T00:00:00Z',
          }),
        ],
      }),
    );
    renderWithProviders(<TenancyPage />);

    expect(await screen.findByText('Suspended')).toBeInTheDocument();
    expect(screen.getByText('unpaid invoice 42')).toBeInTheDocument();
  });
});

describe('tenancy formatting', () => {
  it('keeps unlimited and none apart', () => {
    // The whole reason a limit is nullable. A plan with no mailbox limit and a
    // plan including no mailboxes are opposite promises, and rendering both as
    // "0" would tell a customer the wrong one.
    expect(limitLabel(null)).toBe('Unlimited');
    expect(limitLabel(0)).toBe('None');
    expect(limitLabel(5)).toBe('5');
  });

  it('keeps not-measured and zero apart', () => {
    expect(byteLabel(null)).toBe('Not measured');
    expect(byteLabel(0)).toBe('0 B');
    expect(byteLabel(2048)).toBe('2.0 KB');
    expect(byteLabel(20480)).toBe('20 KB');
  });

  it('draws no bar where there is no limit', () => {
    // Null rather than 0: an empty bar reads as "nothing used", and what it
    // would mean here is "there is nothing to fill".
    expect(fullness(3, null)).toBeNull();
    expect(fullness(1, 2)).toBe(50);
    expect(fullness(5, 2)).toBe(100);
    expect(fullness(1, 0)).toBe(100);
  });
});
