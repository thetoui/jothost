import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { ServicesPage } from '@/pages/ServicesPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type { HostService } from '@/types/api';

function service(overrides: Partial<HostService> = {}): HostService {
  return {
    key: 'nginx',
    label: 'nginx',
    role: 'web',
    summary: 'Serves every website on this host, and holds the public ports.',
    units: ['nginx.service'],
    protected: false,
    self_managed: false,
    self_managed_by: '',
    installed: true,
    running: true,
    pid: 10,
    unit: 'nginx.service',
    enabled: true,
    active_state: 'active',
    sub_state: 'running',
    controllable: true,
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

describe('ServicesPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(
    services: HostService[],
    options: { controllable?: boolean; onAction?: (url: string) => void } = {},
  ) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'server.manage']);
      }
      if (init?.method === 'POST') {
        options.onAction?.(url);
        return envelopeResponse({
          service: 'nginx',
          unit: 'nginx.service',
          action: 'restart',
          running: true,
          enabled: true,
          state: 'active',
        });
      }
      return envelopeResponse({
        services,
        count: services.length,
        controllable: options.controllable ?? true,
        manager: options.controllable === false ? '' : 'systemd',
      });
    });
  }

  it('groups services by what they do and shows their state', async () => {
    mockApi([
      service(),
      service({ key: 'mariadb', label: 'MariaDB', role: 'database', running: false,
        active_state: 'inactive', pid: 0, unit: 'mariadb.service',
        units: ['mariadb.service'] }),
    ]);
    renderWithProviders(<ServicesPage />);

    expect(await screen.findByText('Web')).toBeInTheDocument();
    expect(screen.getByText('Databases')).toBeInTheDocument();
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getByText('Stopped')).toBeInTheDocument();
    // The unit and pid are shown, because "restart nginx" is a thing an
    // operator then goes and checks at a terminal.
    expect(screen.getByText(/nginx\.service/)).toBeInTheDocument();
  });

  it('restarts without a confirmation and stops with one', async () => {
    const calls: string[] = [];
    mockApi([service()], { onAction: (url) => calls.push(url) });
    renderWithProviders(<ServicesPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Restart/ }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toContain('/services/nginx/restart');

    // Stopping takes every site offline, so it asks first.
    await userEvent.click(screen.getByRole('button', { name: /^Stop/ }));
    expect(
      await screen.findByText(/Every website on this host stops being served/),
    ).toBeInTheDocument();
    expect(calls).toHaveLength(1);

    await userEvent.click(screen.getByRole('button', { name: 'Stop nginx' }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1]).toContain('/services/nginx/stop');
  });

  // Stopping SSH on a remote host locks the operator out of the machine they
  // are administering, and nothing in the panel can put them back.
  it('will not offer to stop a protected service', async () => {
    mockApi([
      service({ key: 'sshd', label: 'SSH', role: 'system', protected: true,
        summary: 'Remote shell access to this host.' }),
    ]);
    renderWithProviders(<ServicesPage />);

    expect(await screen.findByText('Protected')).toBeInTheDocument();
    // findBy rather than getBy: the controls appear with the profile that says
    // which of them this user may see.
    expect(await screen.findByRole('button', { name: /^Stop/ })).toBeDisabled();
    // Restart is still offered: it is how a configuration change is applied.
    expect(screen.getByRole('button', { name: /Restart/ })).toBeEnabled();
  });

  // A host with no service manager still has true states to show. The panel
  // says what it cannot do rather than showing a page of dead buttons with no
  // explanation.
  it('says why nothing can be controlled on a host without systemd', async () => {
    mockApi([service({ controllable: false, enabled: null })], { controllable: false });
    renderWithProviders(<ServicesPage />);

    expect(await screen.findByText('This host has no service manager')).toBeInTheDocument();
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /Restart/ })).toBeDisabled();
  });

  // PHP-FPM is started by the PHP manager. A stop button here would hand one
  // process two owners: the init system would report success, the PHP manager
  // would carry on, and the panel would show a state matching neither.
  it('explains a service the panel starts itself instead of offering controls', async () => {
    mockApi([
      service({
        key: 'php-fpm8.4',
        label: 'PHP-FPM 8.4',
        role: 'runtime',
        summary: 'Runs PHP for the websites on version 8.4.',
        unit: 'php-fpm84.service',
        units: ['php-fpm84.service'],
        self_managed: true,
        self_managed_by: 'the PHP manager',
        controllable: false,
      }),
    ]);
    renderWithProviders(<ServicesPage />);

    expect(await screen.findByText(/Started and stopped by the PHP manager/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Restart/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Stop/ })).not.toBeInTheDocument();
    // Nor a boot toggle: what starts it at boot is not the init system either.
    expect(screen.queryByLabelText('Start at boot')).not.toBeInTheDocument();
    // Its state is still shown, because the state is true.
    expect(screen.getByText('Running')).toBeInTheDocument();
  });

  // A host can have a service manager and still have nothing it can act on for
  // one particular daemon — a binary installed with no unit or init script.
  it('says so when the init system has no service for one entry', async () => {
    mockApi([
      service({
        key: 'cron',
        label: 'Cron',
        role: 'system',
        summary: 'Runs scheduled tasks.',
        unit: '',
        running: false,
        pid: 0,
        enabled: null,
        active_state: 'inactive',
        controllable: false,
      }),
    ]);
    renderWithProviders(<ServicesPage />);

    expect(
      await screen.findByText(/init system does not know about it/),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Start/ })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Start at boot')).not.toBeInTheDocument();
    // And no host-wide notice, because the host does have a service manager.
    expect(screen.queryByText('This host has no service manager')).not.toBeInTheDocument();
  });

  it('toggles whether a service starts at boot', async () => {
    const calls: string[] = [];
    mockApi([service({ enabled: true })], { onAction: (url) => calls.push(url) });
    renderWithProviders(<ServicesPage />);

    await userEvent.click(await screen.findByLabelText('Start at boot'));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toContain('/services/nginx/disable');
  });
});
