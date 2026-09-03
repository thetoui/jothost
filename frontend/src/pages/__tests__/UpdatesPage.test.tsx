import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { UpdatesPage } from '@/pages/UpdatesPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { UpdateCheck, UpdateOverview, UpdateRun } from '@/types/api';

function check(overrides: Partial<UpdateCheck> = {}): UpdateCheck {
  return {
    id: 'check-1',
    server_id: 'server-1',
    manager: 'apk',
    succeeded: true,
    security_known: false,
    package_count: 0,
    security_count: 0,
    held_count: 0,
    unavailable_repositories: 0,
    stale_repositories: 0,
    reboot_required: false,
    packages: [],
    held: [],
    checked_at: '2026-09-03T09:00:00Z',
    ...overrides,
  };
}

function overview(overrides: Partial<UpdateOverview> = {}): UpdateOverview {
  return {
    check: check(),
    has_check: true,
    settings: {
      server_id: 'server-1',
      policy: 'off',
      check_interval_hours: 6,
      day_of_week: -1,
      hour: 3,
      minute: 0,
      excluded: [],
    },
    runtime: { php: [], node: [] },
    recent: [],
    ...overrides,
  };
}

function run(overrides: Partial<UpdateRun> = {}): UpdateRun {
  return {
    id: 'run-1',
    server_id: 'server-1',
    trigger: 'manual',
    status: 'succeeded',
    requested: ['git'],
    changes: [{ name: 'git', from: '2.45.4-r0', to: '2.47.3-r0' }],
    reboot_required: false,
    started_at: '2026-09-03T09:00:00Z',
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

describe('UpdatesPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: UpdateOverview;
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'update.manage']);
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
        return envelopeResponse(run());
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('does not claim a host is up to date when the check failed', async () => {
    // The failure this whole phase is built around: an empty package list from
    // a check that could not reach the repositories looks exactly like a host
    // with nothing to do.
    mockApi({
      overview: overview({
        check: check({
          succeeded: false,
          reason: 'the package index could not be refreshed',
          unavailable_repositories: 2,
        }),
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('What this host needs is not known')).toBeInTheDocument();
    expect(screen.queryByText('This host is up to date')).not.toBeInTheDocument();
    expect(screen.getByText(/2 repositories could not be reached/)).toBeInTheDocument();
  });

  it('says a host has never been checked rather than showing nothing', async () => {
    mockApi({ overview: overview({ has_check: false }) });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('This host has not been checked yet')).toBeInTheDocument();
    expect(screen.queryByText('This host is up to date')).not.toBeInTheDocument();
  });

  it('says a host is up to date only when a good check found nothing', async () => {
    mockApi({});
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('This host is up to date')).toBeInTheDocument();
  });

  it('lists the pending packages with the versions they move between', async () => {
    mockApi({
      overview: overview({
        check: check({
          package_count: 2,
          packages: [
            { name: 'git', installed: '2.45.4-r0', available: '2.47.3-r0', security: false },
            { name: 'nginx', installed: '1.26.3-r3', available: '1.26.3-r4', security: false },
          ],
        }),
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('2 updates available')).toBeInTheDocument();
    expect(screen.getByText('git')).toBeInTheDocument();
    expect(screen.getByText('2.45.4-r0')).toBeInTheDocument();
    expect(screen.getByText('2.47.3-r0')).toBeInTheDocument();
  });

  it('says security fixes cannot be told apart rather than reporting none', async () => {
    // "0 security updates" on a host whose package manager has no security
    // channel would be answering a question nothing asked, and it reads as
    // "nothing urgent".
    mockApi({
      overview: overview({
        check: check({
          manager: 'apk',
          security_known: false,
          package_count: 1,
          packages: [
            { name: 'git', installed: '2.45.4-r0', available: '2.47.3-r0', security: false },
          ],
        }),
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText(/does not mark security updates/)).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Apply security fixes' }),
    ).not.toBeInTheDocument();
  });

  it('offers a security-only apply when the host can identify them', async () => {
    mockApi({
      overview: overview({
        check: check({
          manager: 'apt-get',
          security_known: true,
          package_count: 2,
          security_count: 1,
          packages: [
            {
              name: 'libgcrypt20',
              installed: '1.10.1-3',
              available: '1.10.1-3+deb12u1',
              security: true,
            },
            {
              name: 'base-files',
              installed: '12.4+deb12u5',
              available: '12.4+deb12u15',
              security: false,
            },
          ],
        }),
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText(/1 of them is a security fix/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Apply security fixes' })).toBeInTheDocument();
    // The word appears as a column heading too, so the badge is counted
    // rather than matched loosely.
    expect(screen.getAllByText('Security')).toHaveLength(2);
  });

  it('lists held packages separately from outstanding work', async () => {
    // A pinned package would otherwise be a queue that never empties.
    mockApi({
      overview: overview({
        check: check({
          held_count: 1,
          held: [
            {
              name: 'git',
              installed: '2.45.4-r0',
              available: '2.47.3-r0',
              reason: 'pinned to 2.45.4-r0 in /etc/apk/world',
            },
          ],
        }),
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('Held back')).toBeInTheDocument();
    expect(screen.getByText(/pinned to 2\.45\.4-r0/)).toBeInTheDocument();
    // And the summary still says the host is up to date, because it is: these
    // are not work outstanding.
    expect(screen.getByText('This host is up to date')).toBeInTheDocument();
  });

  it('confirms before applying, and says dependencies may move too', async () => {
    const writes: string[] = [];
    mockApi({
      overview: overview({
        check: check({
          package_count: 1,
          packages: [
            { name: 'git', installed: '2.45.4-r0', available: '2.47.3-r0', security: false },
          ],
        }),
      }),
      onWrite: (url) => writes.push(url),
    });
    renderWithProviders(<UpdatesPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Apply all/ }));
    expect(await screen.findByText('Apply all updates?')).toBeInTheDocument();
    expect(screen.getByText(/moves more packages than are listed/)).toBeInTheDocument();
    expect(writes).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(writes.length).toBeGreaterThan(0));
    expect(writes[0]).toContain('/updates/apply');
  });

  it('shows the history with the exact versions that moved', async () => {
    // This is the phase's answer to rollback: neither package manager keeps
    // what it replaced, so the versions are what makes recovery possible.
    mockApi({
      overview: overview({
        recent: [
          run({
            changes: [
              { name: 'git', from: '2.45.4-r0', to: '2.47.3-r0' },
              { name: 'perl-git', from: '2.45.4-r0', to: '2.47.3-r0' },
            ],
          }),
        ],
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText(/^git 2\.45\.4-r0 → 2\.47\.3-r0$/)).toBeInTheDocument();
    // A package that moved but was never asked for is in the record too.
    expect(screen.getByText(/^perl-git 2\.45\.4-r0 → 2\.47\.3-r0$/)).toBeInTheDocument();
  });

  it('shows a failed run with its reason rather than hiding it', async () => {
    mockApi({
      overview: overview({
        recent: [run({ status: 'failed', error: 'the update could not be applied', changes: [] })],
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('Failed')).toBeInTheDocument();
    expect(screen.getByText('the update could not be applied')).toBeInTheDocument();
  });

  it('says an update is running and blocks another', async () => {
    mockApi({
      overview: overview({
        running: run({ status: 'running', changes: [] }),
        check: check({
          package_count: 1,
          packages: [
            { name: 'git', installed: '2.45.4-r0', available: '2.47.3-r0', security: false },
          ],
        }),
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('An update is running')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Apply all/ })).toBeDisabled();
  });

  it('warns that a security-only policy would do nothing on this host', async () => {
    mockApi({
      overview: overview({ check: check({ manager: 'apk', security_known: false }) }),
    });
    renderWithProviders(<UpdatesPage />);

    await userEvent.selectOptions(
      await screen.findByLabelText(/Apply automatically/),
      'security',
    );
    expect(
      await screen.findByText('This host cannot identify security fixes'),
    ).toBeInTheDocument();
  });

  it('saves the schedule with every-day as -1 rather than as Sunday', async () => {
    // Zero is Sunday. An "every day" that arrived as zero would quietly become
    // a weekly schedule.
    const writes: Array<{ url: string; body: Record<string, unknown> }> = [];
    mockApi({ onWrite: (url, init) => writes.push({ url, body: JSON.parse(String(init?.body)) }) });
    renderWithProviders(<UpdatesPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Save settings' }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]?.url).toContain('/updates/settings');
    expect(writes[0]?.body).toMatchObject({ day_of_week: -1, hour: 3, policy: 'off' });
  });

  it('shows PHP updates picked out of the same list', async () => {
    mockApi({
      overview: overview({
        check: check({
          package_count: 1,
          packages: [
            { name: 'php84-fpm', installed: '8.4.1-r0', available: '8.4.2-r0', security: false },
          ],
        }),
        runtime: {
          php: [
            { name: 'php84-fpm', installed: '8.4.1-r0', available: '8.4.2-r0', security: false },
          ],
          node: [],
        },
      }),
    });
    renderWithProviders(<UpdatesPage />);

    expect(await screen.findByText('Runtimes')).toBeInTheDocument();
    expect(screen.getByText('PHP')).toBeInTheDocument();
  });
});
