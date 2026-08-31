import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { SSHPage } from '@/pages/SSHPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { SSHAccount, SSHConfig, SSHFinding, SSHKey } from '@/types/api';

function config(overrides: Partial<SSHConfig> = {}): SSHConfig {
  return {
    available: true,
    managed: true,
    reason: '',
    ports: [22],
    root_login: 'without-password',
    password_authentication: true,
    pubkey_authentication: true,
    permit_empty_passwords: false,
    x11_forwarding: false,
    max_auth_tries: 6,
    login_grace_time: 120,
    config_path: '/etc/ssh/sshd_config',
    drop_in_path: '/etc/ssh/sshd_config.d/10-jothost.conf',
    running: true,
    ...overrides,
  };
}

function account(overrides: Partial<SSHAccount> = {}): SSHAccount {
  return { name: 'root', uid: 0, home: '/root', shell: '/bin/sh', keys: 1, ...overrides };
}

function key(overrides: Partial<SSHKey> = {}): SSHKey {
  return {
    fingerprint: 'SHA256:TudVThr2HD30XIK5NF3naO+dRlV3nMQ6NbcAPRbziS0',
    type: 'ssh-ed25519',
    comment: 'operator@laptop',
    bits: 256,
    account: 'root',
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

describe('SSHPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    config?: SSHConfig;
    accounts?: SSHAccount[];
    findings?: SSHFinding[];
    keys?: SSHKey[];
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (url.includes('/security/ssh/keys')) {
        if (init?.method && init.method !== 'GET') {
          options.onWrite?.(url, init);
          return envelopeResponse(key());
        }
        return envelopeResponse({
          account: 'root',
          keys: options.keys ?? [],
          count: (options.keys ?? []).length,
        });
      }
      if (init?.method === 'PATCH') {
        options.onWrite?.(url, init);
        if (options.refuse) {
          return new Response(
            JSON.stringify({
              success: false,
              error: { code: 'CONFLICT', message: options.refuse },
              request_id: 'req_test',
            }),
            { status: 409, headers: { 'Content-Type': 'application/json' } },
          );
        }
        return envelopeResponse({
          config: options.config ?? config(),
          changed: ['PasswordAuthentication'],
          backup: '',
          reloaded: true,
        });
      }
      return envelopeResponse({
        config: options.config ?? config(),
        accounts: options.accounts ?? [account()],
        findings: options.findings ?? [],
      });
    });
  }

  it('shows the settings the server actually resolved', async () => {
    mockApi({ config: config({ ports: [2222], root_login: 'no' }) });
    renderWithProviders(<SSHPage />);

    expect(await screen.findByDisplayValue('2222')).toBeInTheDocument();
    // sshd prints "without-password" for what its documentation calls
    // "prohibit-password"; the page shows one of them, not both.
    expect(screen.getByLabelText('Root may log in')).toHaveValue('no');
  });

  it('maps sshd\'s two spellings of the same root setting onto one', async () => {
    mockApi({ config: config({ root_login: 'without-password' }) });
    renderWithProviders(<SSHPage />);

    await waitFor(() =>
      expect(screen.getByLabelText('Root may log in')).toHaveValue('prohibit-password'),
    );
  });

  // The refusal is the whole value of this phase: its text says what to do, and
  // a page that showed "conflict" would throw that away.
  it('shows the reason a change was refused', async () => {
    mockApi({
      refuse:
        'No account on this host has an authorised SSH key, so turning off password ' +
        'authentication would leave no way in. Add a key first',
    });
    renderWithProviders(<SSHPage />);

    await userEvent.click(
      await screen.findByLabelText(/Passwords may be used to log in/),
    );

    expect(await screen.findByText(/Add a key first/)).toBeInTheDocument();
  });

  it('sends only the setting that changed', async () => {
    const bodies: string[] = [];
    mockApi({
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<SSHPage />);

    await userEvent.click(await screen.findByLabelText(/X11 forwarding/));

    await waitFor(() => expect(bodies).toHaveLength(1));
    // Not every field: a body carrying the others would rewrite settings the
    // operator did not touch, and one of those decides whether anybody can log
    // in.
    expect(JSON.parse(bodies[0] as string)).toEqual({ x11_forwarding: true });
  });

  it('lists the recommendations worst first', async () => {
    mockApi({
      findings: [
        { id: 'ssh.default-port', severity: 'info', title: 'On the default port', detail: 'd', action: 'a' },
        { id: 'ssh.empty-passwords', severity: 'high', title: 'Empty passwords allowed', detail: 'd', action: 'a' },
        { id: 'ssh.no-keys', severity: 'warn', title: 'No account has a key', detail: 'd', action: 'a' },
      ],
    });
    renderWithProviders(<SSHPage />);

    const titles = await screen.findAllByText(
      /Empty passwords allowed|No account has a key|On the default port/,
    );
    expect(titles.map((node) => node.textContent)).toEqual([
      'Empty passwords allowed',
      'No account has a key',
      'On the default port',
    ]);
  });

  it('says so when there is nothing to recommend', async () => {
    mockApi({ findings: [] });
    renderWithProviders(<SSHPage />);

    expect(await screen.findByText('Nothing to recommend')).toBeInTheDocument();
  });

  it('shows a key by its fingerprint and lets it be removed', async () => {
    const calls: string[] = [];
    mockApi({ keys: [key()], onWrite: (url) => calls.push(url) });
    renderWithProviders(<SSHPage />);

    expect(await screen.findByText('operator@laptop')).toBeInTheDocument();
    expect(screen.getByText(/SHA256:TudVThr2/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /Remove/ }));
    // Removing the last key when passwords are off locks everybody out, so it
    // asks first.
    expect(await screen.findByText(/nobody can/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toContain('/security/ssh/keys/SHA256');
  });

  it('adds a key', async () => {
    const bodies: string[] = [];
    mockApi({
      keys: [],
      onWrite: (_url, init) => {
        if (typeof init?.body === 'string') bodies.push(init.body);
      },
    });
    renderWithProviders(<SSHPage />);

    await userEvent.type(
      await screen.findByLabelText('Add a key'),
      'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 me@laptop',
    );
    await userEvent.click(screen.getByRole('button', { name: /Authorise key/ }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(JSON.parse(bodies[0] as string)).toMatchObject({
      account: 'root',
      key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 me@laptop',
    });
  });

  // A host whose sshd_config has no Include would ignore anything the panel
  // wrote, so the page says so rather than offering controls that do nothing.
  it('says when the settings are read-only on this host', async () => {
    mockApi({
      config: config({
        managed: false,
        reason: "this host's sshd_config has no Include for /etc/ssh/sshd_config.d",
      }),
    });
    renderWithProviders(<SSHPage />);

    expect(await screen.findByText(/read-only on this host/)).toBeInTheDocument();
    expect(await screen.findByLabelText(/X11 forwarding/)).toBeDisabled();
  });

  it('says when this host has no SSH server at all', async () => {
    mockApi({
      config: config({ available: false, reason: 'the SSH server is not installed on this host' }),
    });
    renderWithProviders(<SSHPage />);

    expect(await screen.findByText('This host has no SSH server')).toBeInTheDocument();
    expect(screen.queryByLabelText(/X11 forwarding/)).not.toBeInTheDocument();
  });
});
