import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { NotificationsPage } from '@/pages/NotificationsPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, renderWithProviders } from '@/test/utils';
import type {
  NotificationChannel,
  NotificationDelivery,
  NotificationOverview,
} from '@/types/api';

function channel(overrides: Partial<NotificationChannel> = {}): NotificationChannel {
  return {
    id: 'channel-1',
    server_id: 'server-1',
    name: 'Ops mail',
    kind: 'email',
    config: { host: 'mail.example', from: 'panel@example.com' },
    enabled: true,
    min_severity: 'warning',
    kinds: [],
    last_success_at: '2026-09-04T09:00:00Z',
    last_failure_at: null,
    last_error: null,
    failure_streak: 0,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function delivery(overrides: Partial<NotificationDelivery> = {}): NotificationDelivery {
  return {
    id: 'delivery-1',
    event_id: 'event-1',
    channel_id: 'channel-1',
    status: 'sent',
    attempts: 1,
    next_attempt_at: '2026-09-04T09:00:00Z',
    last_error: null,
    sent_at: '2026-09-04T09:00:05Z',
    created_at: '2026-09-04T09:00:00Z',
    updated_at: '2026-09-04T09:00:05Z',
    channel_name: 'Ops mail',
    channel_kind: 'email',
    event_title: 'Disk nearly full (/var): 96% is above 90%',
    event_kind: 'alert.opened',
    severity: 'critical',
    ...overrides,
  };
}

function overview(overrides: Partial<NotificationOverview> = {}): NotificationOverview {
  return {
    channels: [channel()],
    deliveries: [delivery()],
    events: [],
    stats: {
      sent: 1,
      failed: 0,
      pending: 0,
      broken_channels: 0,
      untested_channels: 0,
    },
    channel_kinds: ['email', 'telegram', 'line'],
    event_kinds: [
      'alert.opened',
      'alert.resolved',
      'backup.failed',
      'ssl.expiring',
      'security.finding',
    ],
    severities: ['critical', 'high', 'warning', 'info'],
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

describe('NotificationsPage', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch');
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  function mockApi(options: {
    overview?: NotificationOverview;
    onWrite?: (url: string, init?: RequestInit) => void;
    refuse?: string;
  }) {
    vi.mocked(globalThis.fetch).mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/auth/me')) {
        return mockProfile(['server.view', 'notification.manage']);
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
        return envelopeResponse(channel());
      }
      return envelopeResponse(options.overview ?? overview());
    });
  }

  it('says when nothing is set up to receive anything', async () => {
    // A panel watching a host with nowhere to tell anybody is a panel whose
    // silence means nothing.
    mockApi({ overview: overview({ channels: [] }) });
    renderWithProviders(<NotificationsPage />);

    expect(
      await screen.findByText('Nothing is set up to receive notifications'),
    ).toBeInTheDocument();
    expect(screen.getByText(/nothing will reach you/)).toBeInTheDocument();
  });

  it('leads with broken channels, and says why nothing else could have told you', async () => {
    // The failure this whole phase is built around: a channel that is not
    // working cannot deliver the message saying it is not working.
    mockApi({
      overview: overview({
        channels: [
          channel({
            failure_streak: 3,
            last_error: 'dial tcp: connection refused',
            last_success_at: null,
          }),
        ],
        stats: {
          sent: 0,
          failed: 3,
          pending: 0,
          broken_channels: 1,
          untested_channels: 1,
        },
      }),
    });
    renderWithProviders(<NotificationsPage />);

    expect(await screen.findByText('Notifications are not arriving')).toBeInTheDocument();
    expect(
      screen.getByText(/a broken channel cannot deliver the message saying it is broken/),
    ).toBeInTheDocument();
  });

  it('warns about a channel nobody has ever got a message through', async () => {
    // The same failure as an unreached backup destination: it looks like
    // protection and is not.
    mockApi({
      overview: overview({
        channels: [channel({ last_success_at: null })],
        stats: {
          sent: 0,
          failed: 0,
          pending: 0,
          broken_channels: 0,
          untested_channels: 1,
        },
      }),
    });
    renderWithProviders(<NotificationsPage />);

    expect(
      await screen.findByText('Some channels have never delivered anything'),
    ).toBeInTheDocument();
    expect(screen.getByText(/looks like protection and is not/)).toBeInTheDocument();
    expect(screen.getByText(/Never delivered anything/)).toBeInTheDocument();
  });

  it('shows a working channel as having delivered, not merely as configured', async () => {
    mockApi({});
    renderWithProviders(<NotificationsPage />);

    expect(await screen.findByText(/Last delivered/)).toBeInTheDocument();
  });

  it('shows why a channel is failing', async () => {
    mockApi({
      overview: overview({
        channels: [
          channel({
            failure_streak: 2,
            last_error: 'the mail server rejected the credentials',
          }),
        ],
        stats: {
          sent: 0,
          failed: 2,
          pending: 0,
          broken_channels: 1,
          untested_channels: 0,
        },
      }),
    });
    renderWithProviders(<NotificationsPage />);

    expect(
      await screen.findByText(/Failing \(2 in a row\): the mail server rejected/),
    ).toBeInTheDocument();
  });

  it('shows the delivery record, including what did not arrive', async () => {
    // This list is the only place a failed notification is visible.
    mockApi({
      overview: overview({
        deliveries: [
          delivery(),
          delivery({
            id: 'delivery-2',
            status: 'failed',
            attempts: 4,
            last_error: 'the mail server rejected the recipient "ops@example.com"',
            sent_at: null,
            event_title: 'Backup failed: example.com',
          }),
        ],
      }),
    });
    renderWithProviders(<NotificationsPage />);

    expect(await screen.findByText('Backup failed: example.com')).toBeInTheDocument();
    expect(screen.getByText(/rejected the recipient/)).toBeInTheDocument();
    expect(screen.getByText(/4 attempts/)).toBeInTheDocument();
    expect(
      screen.getByText(/only place a failed notification is visible/),
    ).toBeInTheDocument();
  });

  it('sends a test through a channel when asked', async () => {
    // The only way to find out that a channel works before the night it has to.
    const writes: string[] = [];
    mockApi({ onWrite: (url) => writes.push(url) });
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Send a test/ }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toContain('/notification-channels/channel-1/test');
    expect(screen.getByText(/while you are watching/)).toBeInTheDocument();
  });

  it('describes what a channel will and will not be told about', async () => {
    mockApi({
      overview: overview({
        channels: [channel({ min_severity: 'critical', kinds: ['backup.failed'] })],
      }),
    });
    renderWithProviders(<NotificationsPage />);

    expect(
      await screen.findByText(/critical and above, backup.failed/),
    ).toBeInTheDocument();
  });

  it('says a channel with no kinds listed hears about everything', async () => {
    mockApi({});
    renderWithProviders(<NotificationsPage />);

    expect(await screen.findByText(/warning and above, everything/)).toBeInTheDocument();
  });

  it('creates an email channel without asking for a webhook URL', async () => {
    // There is deliberately no webhook kind: a channel that accepted a URL
    // would be a request forger sitting inside the panel.
    const bodies: Array<Record<string, unknown>> = [];
    mockApi({ onWrite: (_url, init) => bodies.push(JSON.parse(String(init?.body))) });
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add channel/ }));

    const kind = await screen.findByLabelText('Send by');
    expect(kind).toBeInTheDocument();
    expect(screen.queryByLabelText(/URL/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/webhook/i)).not.toBeInTheDocument();

    await userEvent.type(screen.getByLabelText('Name'), 'Ops mail');
    await userEvent.type(screen.getByLabelText('Mail server'), 'mail.example');
    await userEvent.type(screen.getByLabelText('From'), 'panel@example.com');
    await userEvent.type(screen.getByLabelText('To'), 'ops@example.com');
    await userEvent.click(screen.getByRole('button', { name: 'Save channel' }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      name: 'Ops mail',
      kind: 'email',
      host: 'mail.example',
      to: ['ops@example.com'],
    });
  });

  it('warns that a mail server off this machine must use TLS', async () => {
    mockApi({});
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add channel/ }));
    expect(
      await screen.findByText(/must use TLS, or the password and every alert travel in the clear/),
    ).toBeInTheDocument();
  });

  it('offers a severity floor rather than a set of checkboxes', async () => {
    // The question an operator actually has is "how bad does it have to be
    // before you wake me".
    mockApi({});
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add channel/ }));
    const floor = await screen.findByLabelText('Tell me about');
    expect(floor).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'critical and above' })).toBeInTheDocument();
  });

  it('says a bot token is stored encrypted and never shown again', async () => {
    mockApi({});
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add channel/ }));
    await userEvent.selectOptions(await screen.findByLabelText('Send by'), 'telegram');

    expect(await screen.findByLabelText('Bot token')).toBeInTheDocument();
    expect(
      screen.getByText(/anyone holding it can post as the bot/),
    ).toBeInTheDocument();
  });

  it('shows the reason a channel was refused', async () => {
    // A message distinct from the form's own hint about TLS, so this asserts
    // the server's answer rather than the text that was already on screen.
    mockApi({ refuse: 'an email channel needs at least one recipient' });
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: /Add channel/ }));
    await userEvent.type(await screen.findByLabelText('Name'), 'Bad');
    await userEvent.click(screen.getByRole('button', { name: 'Save channel' }));

    expect(await screen.findByText(/needs at least one recipient/)).toBeInTheDocument();
  });

  it('confirms before deleting a channel, and says what is kept', async () => {
    const writes: string[] = [];
    mockApi({ onWrite: (url) => writes.push(url) });
    renderWithProviders(<NotificationsPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Delete Ops mail' }));
    expect(await screen.findByText('Delete "Ops mail"?')).toBeInTheDocument();
    expect(
      screen.getByText(/record of what happened on this host is\s+kept/),
    ).toBeInTheDocument();
    expect(writes).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: 'Delete channel' }));
    await waitFor(() => expect(writes).toHaveLength(1));
  });

  it('says nothing has been sent without claiming nothing went wrong', async () => {
    mockApi({
      overview: overview({
        deliveries: [],
        stats: {
          sent: 0,
          failed: 0,
          pending: 0,
          broken_channels: 0,
          untested_channels: 0,
        },
      }),
    });
    renderWithProviders(<NotificationsPage />);

    expect(await screen.findByText('Nothing has been sent')).toBeInTheDocument();
    expect(
      screen.getByText(/Either nothing has gone wrong, or nothing is configured/),
    ).toBeInTheDocument();
  });
});
