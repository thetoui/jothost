import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { DeploymentsPage } from '@/pages/DeploymentsPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { Deployment, GitRepository } from '@/types/api';

function deployment(overrides: Partial<Deployment> = {}): Deployment {
  return {
    id: 'deployment-1',
    repository_id: 'repo-1',
    website_id: 'site-1',
    trigger: 'manual',
    branch: 'main',
    commit_sha: '5f2e8a19c4b7d0e3f6a2b9c8d1e4f7a0b3c6d9e2',
    commit_message: 'Add the billing page',
    commit_author: 'Somebody',
    previous_commit: 'aaaaaaa1111111111111111111111111111111111',
    status: 'success',
    log_truncated: false,
    rolled_back: false,
    created_at: '2026-09-04T10:00:00Z',
    ...overrides,
  };
}

function repository(overrides: Partial<GitRepository> = {}): GitRepository {
  return {
    id: 'repo-1',
    server_id: 'server-1',
    website_id: 'site-1',
    remote_url: 'git@github.com:owner/repo.git',
    branch: 'main',
    provider: 'github',
    auto_deploy: false,
    deploy_script: '',
    script_timeout_seconds: 600,
    current_commit: '5f2e8a19c4b7d0e3f6a2b9c8d1e4f7a0b3c6d9e2',
    current_branch: 'main',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    website: 'example.com',
    actions: [],
    recent: [deployment()],
    status: {
      available: true,
      cloned: true,
      dirty: false,
      has_key: true,
      commit: '5f2e8a19c4b7d0e3f6a2b9c8d1e4f7a0b3c6d9e2',
      branch: 'main',
      message: 'Add the billing page',
      tools: { 'composer.install': true, 'npm.ci': true },
      warnings: [],
    },
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

describe('DeploymentsPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(repositories: GitRepository[], permissions = ['deploy.view', 'deploy.manage']) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(permissions);
      }
      if (url.includes('/websites')) {
        return envelopeResponse({ websites: [] });
      }
      return envelopeResponse({ repositories });
    });
  }

  it('says when a working tree has changes a deployment would destroy', async () => {
    // Deploying is a hard reset. Somebody who edited a file on the server
    // directly is about to lose it, and the panel says so before they press
    // the button rather than after.
    mockApi([
      repository({
        status: {
          available: true,
          cloned: true,
          dirty: true,
          dirty_files: [' M config/app.php'],
          has_key: true,
          tools: {},
          warnings: [],
        },
      }),
    ]);
    renderWithProviders(<DeploymentsPage />);

    expect(
      await screen.findByText(/uncommitted changes.*would be lost/i),
    ).toBeInTheDocument();
  });

  it('marks a repository that deploys on push, because that is the consequential setting', async () => {
    // With it on, a person with write access to the repository can run code on
    // this host without touching the panel.
    mockApi([repository({ auto_deploy: true })]);
    renderWithProviders(<DeploymentsPage />);

    expect(await screen.findByText('deploys on push')).toBeInTheDocument();
  });

  it('shows the host warning when a repository has no key to authenticate with', async () => {
    mockApi([
      repository({
        status: {
          available: true,
          cloned: true,
          dirty: false,
          has_key: false,
          tools: {},
          warnings: [
            'this repository is reached over SSH and this host has no deploy key for it, ' +
              'so every deployment will fail to authenticate',
          ],
        },
      }),
    ]);
    renderWithProviders(<DeploymentsPage />);

    expect(await screen.findByText(/no deploy key for it/)).toBeInTheDocument();
  });

  it('says what a rollback did and did not restore', async () => {
    // "Rolled back" on a page reads as "nothing happened", and that is not
    // true: the source is restored and files the build wrote are not.
    mockApi([
      repository({
        recent: [deployment({ status: 'failed', rolled_back: true })],
      }),
    ]);
    renderWithProviders(<DeploymentsPage />);

    await userEvent.click(await screen.findByText('History'));
    expect(
      await screen.findByText(/files the build wrote are still there/i),
    ).toBeInTheDocument();
  });

  it('leads with a rollback that itself failed', async () => {
    // The state an operator most needs to be told about: the site is then on
    // neither commit.
    mockApi([
      repository({
        recent: [
          deployment({
            status: 'failed',
            rolled_back: false,
            rollback_error: 'check out aaaaaaa: unknown revision',
          }),
        ],
      }),
    ]);
    renderWithProviders(<DeploymentsPage />);

    await userEvent.click(await screen.findByText('History'));
    expect(await screen.findByText(/The rollback failed/)).toBeInTheDocument();
  });

  it('offers to connect a repository when there are none', async () => {
    mockApi([]);
    renderWithProviders(<DeploymentsPage />);

    expect(await screen.findByText('No repositories')).toBeInTheDocument();
  });

  it('hides every control that changes something from a viewer', async () => {
    // Deploying runs code on the host as the website's account. A viewer can
    // see that a deployment failed and cannot start one — and cannot read the
    // log, which is where a build's secrets end up.
    mockApi([repository()], ['deploy.view']);
    renderWithProviders(<DeploymentsPage />);

    expect(await screen.findByText('example.com')).toBeInTheDocument();
    expect(screen.queryByText('Deploy')).not.toBeInTheDocument();
    expect(screen.queryByText('Connect a repository')).not.toBeInTheDocument();
    expect(screen.queryByText('Disconnect')).not.toBeInTheDocument();
  });
});
