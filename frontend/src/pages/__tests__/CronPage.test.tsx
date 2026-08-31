import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { CronPage } from '@/pages/CronPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { CronJob } from '@/types/api';

function job(overrides: Partial<CronJob> = {}): CronJob {
  return {
    id: 'job-1',
    server_id: 'server-1',
    website_id: 'site-1',
    name: 'Nightly dump',
    job_type: 'command',
    schedule: '0 3 * * *',
    target: 'php cron.php',
    command: 'php cron.php',
    enabled: true,
    last_run_at: null,
    last_status: null,
    last_exit_code: null,
    last_duration_ms: null,
    created_at: '2026-08-31T00:00:00Z',
    updated_at: '2026-08-31T00:00:00Z',
    website_domain: 'shop.example',
    system_user: 'web_shop_example',
    next_run_at: '2026-09-01T03:00:00Z',
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

describe('CronPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(
    jobs: CronJob[],
    options: { onWrite?: (url: string, init?: RequestInit) => void; runResult?: unknown } = {},
  ) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['cron.manage', 'website.view']);
      }
      if (url.includes('/websites')) {
        return envelopeResponse({
          websites: [
            {
              id: 'site-1',
              server_id: 'server-1',
              name: null,
              primary_domain: 'shop.example',
              document_root: '/var/www/shop.example/public',
              system_user: 'web_shop_example',
              php_version: '8.4',
              status: 'active',
              ssl_enabled: false,
              https_redirect: false,
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-01T00:00:00Z',
            },
          ],
          count: 1,
        });
      }
      if (url.includes('/cron/') && url.endsWith('/run')) {
        options.onWrite?.(url, init);
        return envelopeResponse(
          options.runResult ?? {
            job_id: 'job-1',
            exit_code: 0,
            output: 'dumped 4 tables',
            truncated: false,
            duration_ms: 812,
            timed_out: false,
            status: 'success',
          },
        );
      }
      if (init?.method && init.method !== 'GET') {
        options.onWrite?.(url, init);
        return envelopeResponse(job());
      }
      return envelopeResponse({ jobs, count: jobs.length, types: ['php', 'url', 'command'] });
    });
  }

  it('lists jobs with what they run, when, and as whom', async () => {
    mockApi([job()]);
    renderWithProviders(<CronPage />);

    expect(await screen.findByText('Nightly dump')).toBeInTheDocument();
    expect(screen.getByText('php cron.php')).toBeInTheDocument();
    // The account is shown because a job that ran as root would be the thing
    // worth noticing, and the only way to notice is for it to be on screen.
    expect(screen.getByText(/runs as web_shop_example/)).toBeInTheDocument();
    expect(screen.getByText(/0 3 \* \* \*/)).toBeInTheDocument();
  });

  it('says a job has never run rather than implying it succeeded', async () => {
    mockApi([job()]);
    renderWithProviders(<CronPage />);

    expect(await screen.findByText('Not run yet')).toBeInTheDocument();
  });

  it('shows the exit code of a job that failed', async () => {
    mockApi([job({ last_status: 'failed', last_exit_code: 127, last_run_at: '2026-08-31T03:00:00Z' })]);
    renderWithProviders(<CronPage />);

    expect(await screen.findByText(/Last run failed \(127\)/)).toBeInTheDocument();
  });

  // A schedule that never fires is a valid expression — 31 February — and a job
  // that looks scheduled and never happens is the worst outcome here.
  it('says so when a schedule can never fire', async () => {
    mockApi([job({ next_run_at: null })]);
    renderWithProviders(<CronPage />);

    expect(await screen.findByText(/This schedule never fires/)).toBeInTheDocument();
  });

  it('does not promise a next run for a disabled job', async () => {
    mockApi([job({ enabled: false, next_run_at: null })]);
    renderWithProviders(<CronPage />);

    expect(await screen.findByText(/Disabled, so it will not run/)).toBeInTheDocument();
  });

  it('runs a job and shows what it printed', async () => {
    const calls: string[] = [];
    mockApi([job()], { onWrite: (url) => calls.push(url) });
    renderWithProviders(<CronPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Run now/ }));

    await waitFor(() => expect(calls.some((url) => url.endsWith('/cron/job-1/run'))).toBe(true));
    // The output is what the operator asked for by pressing the button; a reply
    // of "started" would have answered a different question.
    expect(await screen.findByText('dumped 4 tables')).toBeInTheDocument();
  });

  it('reports a failed run as a failure', async () => {
    mockApi([job()], {
      runResult: {
        job_id: 'job-1',
        exit_code: 127,
        output: 'sh: php: not found',
        truncated: false,
        duration_ms: 4,
        timed_out: false,
        status: 'failed',
      },
    });
    renderWithProviders(<CronPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Run now/ }));
    expect(await screen.findByText(/failed \(exit 127\)/)).toBeInTheDocument();
    expect(screen.getByText('sh: php: not found')).toBeInTheDocument();
  });

  it('creates a job from the form', async () => {
    const bodies: string[] = [];
    mockApi([], {
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<CronPage />);

    await userEvent.click(await screen.findByRole('button', { name: /New job/ }));

    const dialog = await screen.findByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Name'), 'Queue worker');
    await userEvent.type(within(dialog).getByLabelText('Command'), 'php artisan queue:work');
    await userEvent.selectOptions(within(dialog).getByLabelText('Schedule'), '*/5 * * * *');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create job' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    const sent = JSON.parse(bodies[0] as string) as Record<string, unknown>;
    expect(sent).toMatchObject({
      website_id: 'site-1',
      name: 'Queue worker',
      job_type: 'command',
      schedule: '*/5 * * * *',
      target: 'php artisan queue:work',
    });
  });

  // The field an operator fills in changes with the type, because "cron.php"
  // and "https://…/cron" are not the same kind of answer.
  it('asks for a script when the job runs PHP', async () => {
    mockApi([]);
    renderWithProviders(<CronPage />);

    await userEvent.click(await screen.findByRole('button', { name: /New job/ }));
    const dialog = await screen.findByRole('dialog');

    await userEvent.selectOptions(within(dialog).getByLabelText('What it runs'), 'php');
    expect(within(dialog).getByLabelText('Script')).toBeInTheDocument();
    expect(
      within(dialog).getByText(/Relative to the site's directory/),
    ).toBeInTheDocument();
  });

  it('toggles a job without opening the form', async () => {
    const bodies: string[] = [];
    mockApi([job()], {
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<CronPage />);

    await userEvent.click(await screen.findByLabelText('Enabled'));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(JSON.parse(bodies[0] as string)).toEqual({ enabled: false });
  });

  it('asks before deleting a job', async () => {
    const calls: string[] = [];
    mockApi([job()], { onWrite: (url) => calls.push(url) });
    renderWithProviders(<CronPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Delete/ }));
    expect(await screen.findByText(/removed from the host/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Delete job' }));
    await waitFor(() => expect(calls.some((url) => url.endsWith('/cron/job-1'))).toBe(true));
  });
});
