import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { BackupsPage } from '@/pages/BackupsPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Backup, BackupDestination, BackupOverview, BackupSchedule } from '@/types/api';

function destination(overrides: Partial<BackupDestination> = {}): BackupDestination {
  return {
    id: 'dest-1',
    server_id: 'server-1',
    name: 'Local disk',
    kind: 'local',
    config: { directory: '/var/lib/jothost/backups' },
    has_credentials: false,
    last_check_at: '2026-09-03T08:00:00Z',
    last_check_ok: true,
    last_check_detail: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function backup(overrides: Partial<Backup> = {}): Backup {
  return {
    id: 'backup-1',
    server_id: 'server-1',
    website_id: 'site-1',
    database_id: null,
    subject: 'example.com',
    type: 'website',
    destination_id: 'dest-1',
    destination: 'Local disk',
    path: 'website/example.com/2026-09-03/031500.tar.gz',
    size_bytes: 4194304,
    checksum: 'a'.repeat(64),
    status: 'completed',
    verified_at: '2026-09-03T03:16:00Z',
    verify_detail: null,
    error: null,
    job_id: 'job-1',
    schedule_id: null,
    created_by: 'user-1',
    started_at: '2026-09-03T03:15:00Z',
    completed_at: '2026-09-03T03:16:00Z',
    created_at: '2026-09-03T03:15:00Z',
    ...overrides,
  };
}

function schedule(overrides: Partial<BackupSchedule> = {}): BackupSchedule {
  return {
    id: 'schedule-1',
    server_id: 'server-1',
    name: 'Nightly',
    type: 'full',
    website_id: null,
    database_id: null,
    destination_id: 'dest-1',
    destination_name: 'Local disk',
    hour: 3,
    minute: 0,
    day_of_week: -1,
    retention_days: 14,
    keep_last: 3,
    enabled: true,
    last_run_at: null,
    last_status: null,
    last_backup_id: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function overview(overrides: Partial<BackupOverview> = {}): BackupOverview {
  return {
    capabilities: {
      available: true,
      local: true,
      s3: true,
      sftp: true,
      mysql_dump: true,
      postgres_dump: true,
      engines: ['mariadb', 'postgres'],
      work_dir: '/var/lib/jothost/backups',
    },
    backups: [],
    destinations: [destination()],
    schedules: [],
    stats: {
      total: 0,
      completed: 0,
      verified: 0,
      failed: 0,
      running: 0,
      bytes: 0,
      latest_at: null,
    },
    types: ['website', 'database', 'full'],
    destination_kinds: ['local', 's3', 'sftp'],
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

describe('BackupsPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: BackupOverview;
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'backup.manage']);
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
        return envelopeResponse(backup());
      }
      if (url.includes('/websites')) {
        return envelopeResponse({ websites: [], count: 0 });
      }
      if (url.includes('/databases')) {
        return envelopeResponse({ databases: [], count: 0, engines: [] });
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('says a host that cannot take backups cannot, with the reason', async () => {
    // A page that showed an empty list would read as "nothing is configured",
    // which is a very different thing.
    mockApi({
      overview: overview({
        capabilities: {
          available: false,
          reason: 'the working directory could not be created',
          local: false,
          s3: false,
          sftp: false,
          mysql_dump: false,
          postgres_dump: false,
          engines: null,
        },
      }),
    });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByText('This host cannot take backups')).toBeInTheDocument();
    expect(screen.getByText(/the working directory could not be created/)).toBeInTheDocument();
  });

  it('warns about a destination the panel has never reached', async () => {
    // The most dangerous object on this page: it looks like protection and
    // is not.
    mockApi({
      overview: overview({
        destinations: [destination({ last_check_at: null, last_check_ok: null })],
      }),
    });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByText(/Never reached/)).toBeInTheDocument();
  });

  it('shows why a destination could not be used', async () => {
    mockApi({
      overview: overview({
        destinations: [
          destination({
            last_check_ok: false,
            last_check_detail: 'the bucket does not exist',
          }),
        ],
      }),
    });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByText(/the bucket does not exist/)).toBeInTheDocument();
  });

  it('distinguishes a backup that was written from one that was confirmed', async () => {
    // "The upload returned success" and "the bytes are there and correct" are
    // different claims, and only the second is a backup.
    mockApi({
      overview: overview({
        backups: [
          backup({ id: 'confirmed' }),
          backup({ id: 'unconfirmed', subject: 'other.example', verified_at: null }),
        ],
        stats: {
          total: 2,
          completed: 2,
          verified: 1,
          failed: 0,
          running: 0,
          bytes: 8388608,
          latest_at: '2026-09-03T03:16:00Z',
        },
      }),
    });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByText(/Read back and confirmed/)).toBeInTheDocument();
    expect(screen.getByText(/Written, not confirmed/)).toBeInTheDocument();
    expect(screen.getByText(/1 of 2 backups have been read back/)).toBeInTheDocument();
  });

  it('offers no restore for a backup that was never confirmed', async () => {
    // Offering to restore an archive the panel could not read back is offering
    // something it has no reason to believe will work.
    mockApi({
      overview: overview({
        backups: [backup({ verified_at: null })],
      }),
    });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByRole('button', { name: /Verify/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Restore/ })).not.toBeInTheDocument();
    expect(
      screen.getByText(/Only a backup the panel has read back and confirmed can be restored/),
    ).toBeInTheDocument();
  });

  it('confirms before restoring over live files, and says what happens', async () => {
    const writes: string[] = [];
    mockApi({
      overview: overview({ backups: [backup()] }),
      onWrite: (url) => writes.push(url),
    });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Restore/ }));
    expect(await screen.findByText('Restore example.com?')).toBeInTheDocument();
    expect(screen.getByText(/checks the whole archive before it changes anything/)).toBeInTheDocument();
    expect(writes).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Restore over the live files' }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toContain('/restore');
  });

  it('sends the backup id as the restore confirmation, not a boolean', async () => {
    // A confirm: true is something a caller sets once and forgets.
    const bodies: Array<Record<string, unknown>> = [];
    mockApi({
      overview: overview({ backups: [backup()] }),
      onWrite: (_url, init) => bodies.push(JSON.parse(String(init?.body))),
    });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Restore/ }));
    await userEvent.click(screen.getByRole('button', { name: 'Restore over the live files' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ confirm: 'backup-1' });
  });

  it('confirms before deleting an archive, and says it cannot be got back', async () => {
    const writes: string[] = [];
    mockApi({
      overview: overview({ backups: [backup()] }),
      onWrite: (url) => writes.push(url),
    });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(
      await screen.findByRole('button', { name: 'Delete the backup of example.com' }),
    );
    expect(await screen.findByText(/It cannot be got back/)).toBeInTheDocument();
    expect(writes).toHaveLength(0);
  });

  it('describes a schedule as a sentence, including its retention floor', async () => {
    // The floor is what makes keeping by age safe, so it is on the face of the
    // schedule rather than hidden in an editor.
    mockApi({ overview: overview({ schedules: [schedule()] }) });
    renderWithProviders(<BackupsPage />);

    expect(
      await screen.findByText(
        /Every day at 03:00 UTC — everything on this host, to Local disk, kept 14 days \(always keeping the last 3\)/,
      ),
    ).toBeInTheDocument();
  });

  it('says when a schedule has never run', async () => {
    mockApi({ overview: overview({ schedules: [schedule()] }) });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByText('Has not run yet')).toBeInTheDocument();
  });

  it('will not let a schedule be added before there is anywhere to put a backup', async () => {
    mockApi({ overview: overview({ destinations: [] }) });
    renderWithProviders(<BackupsPage />);

    expect(await screen.findByRole('button', { name: /Add schedule/ })).toBeDisabled();
    expect(screen.getByText('Nowhere to put a backup')).toBeInTheDocument();
  });

  it('checks a destination by writing and reading back', async () => {
    const writes: string[] = [];
    mockApi({ onWrite: (url) => writes.push(url) });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Check/ }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toContain('/backup-destinations/dest-1/check');
    expect(
      screen.getByText(/Checking writes a small file and reads it back/),
    ).toBeInTheDocument();
  });

  it('creates a local destination without asking for a credential', async () => {
    const bodies: Array<Record<string, unknown>> = [];
    mockApi({ onWrite: (_url, init) => bodies.push(JSON.parse(String(init?.body))) });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add destination/ }));
    await userEvent.type(await screen.findByLabelText('Name'), 'Second disk');
    await userEvent.click(screen.getByRole('button', { name: 'Save destination' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ name: 'Second disk', kind: 'local' });
    expect(bodies[0]).not.toHaveProperty('secret_key');
  });

  it('asks an SFTP destination for a host key', async () => {
    // Without it there is no way to tell the intended server from whatever
    // answers on port 22.
    mockApi({});
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add destination/ }));
    await userEvent.selectOptions(await screen.findByLabelText('Kind'), 'sftp');

    expect(await screen.findByLabelText('Host key')).toBeInTheDocument();
    expect(
      screen.getByText(/no way to tell the intended server from whatever answers/),
    ).toBeInTheDocument();
  });

  it('shows the reason a destination was refused', async () => {
    mockApi({ refuse: 'an endpoint that is not on this machine must be https' });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add destination/ }));
    await userEvent.type(await screen.findByLabelText('Name'), 'Offsite');
    await userEvent.click(screen.getByRole('button', { name: 'Save destination' }));

    expect(await screen.findByText(/must be https/)).toBeInTheDocument();
  });

  it('includes a website’s databases by default when taking a backup', async () => {
    // A site restored without the schema its application expects is broken in a
    // more confusing way than one that is simply gone.
    const bodies: Array<Record<string, unknown>> = [];
    mockApi({ onWrite: (_url, init) => bodies.push(JSON.parse(String(init?.body))) });
    renderWithProviders(<BackupsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Take a backup/ }));
    await userEvent.selectOptions(await screen.findByLabelText('Back up'), 'website');
    // A regex, because the toggle's label carries its description too.
    expect(await screen.findByLabelText(/Include its databases/)).toBeChecked();

    await userEvent.click(screen.getByRole('button', { name: 'Start backup' }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ type: 'website', include_databases: true });
  });

  it('shows why a backup failed', async () => {
    mockApi({
      overview: overview({
        backups: [
          backup({
            status: 'failed',
            verified_at: null,
            error: 'the copy read back does not match the archive checksum',
          }),
        ],
      }),
    });
    renderWithProviders(<BackupsPage />);

    expect(
      await screen.findByText(/the copy read back does not match the archive checksum/),
    ).toBeInTheDocument();
  });
});
