import { useState } from 'react';
import {
  AlertTriangle,
  Bell,
  BellRing,
  CheckCircle2,
  Send,
  Plus,
  Trash2,
  XCircle,
} from 'lucide-react';

import { Alert as AlertBanner } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useCreateChannel,
  useDeleteChannel,
  useNotificationOverview,
  useTestChannel,
} from '@/features/notifications/hooks';
import { ApiError } from '@/services/apiClient';
import type {
  ChannelKind,
  NotificationChannel,
  NotificationChannelInput,
  NotificationDelivery,
  NotificationOverview,
  NotifySeverity,
} from '@/types/api';

/**
 * NotificationsPage shows where alerts go, and — more importantly — whether
 * they are arriving.
 *
 * The page is built around one fact: a notification system cannot report its
 * own failure through itself. When delivery is broken, the message saying so
 * does not arrive, and the operator's experience is silence — which is
 * indistinguishable from a machine with nothing wrong. So the delivery record
 * is not a footnote here, it is the thing at the top.
 */
export function NotificationsPage() {
  const { data, isPending, isError, error } = useNotificationOverview();

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Notifications</h1>
        <p className="mt-1 text-sm text-slate-500">
          Where this panel sends what it finds out, and whether it is getting through.
        </p>
      </header>

      {isError && (
        <AlertBanner tone="danger" title="The notification settings could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </AlertBanner>
      )}

      {isPending && !data ? (
        <Card>
          <CardBody>
            <SkeletonRows rows={4} />
          </CardBody>
        </Card>
      ) : !data ? null : (
        <>
          <Health overview={data} />
          <Channels overview={data} />
          <Deliveries overview={data} />
        </>
      )}
    </div>
  );
}

/** Health leads with whether notifications are working at all. */
function Health({ overview }: { overview: NotificationOverview }) {
  const { stats } = overview;
  const channels = overview.channels ?? [];

  if (channels.length === 0) {
    return (
      <AlertBanner tone="warning" title="Nothing is set up to receive notifications">
        This panel is watching the host and has nowhere to tell you about it. Everything
        it finds is recorded on its own pages and nothing will reach you.
      </AlertBanner>
    );
  }

  return (
    <>
      {stats.broken_channels > 0 && (
        <AlertBanner tone="danger" title="Notifications are not arriving">
          {stats.broken_channels} channel(s) are failing. Nothing could have told you
          this except this page — a broken channel cannot deliver the message saying it
          is broken.
        </AlertBanner>
      )}

      {stats.untested_channels > 0 && (
        <AlertBanner tone="warning" title="Some channels have never delivered anything">
          {stats.untested_channels} channel(s) have never got a message through. A
          channel nobody has tested looks like protection and is not — send a test.
        </AlertBanner>
      )}

      <Card>
        <CardHeader
          icon={<TintedIcon tone="brand" icon={<Bell className="h-4 w-4" />} />}
          title="The last seven days"
          description="What this panel tried to tell you."
        />
        <CardBody>
          <dl className="grid gap-4 sm:grid-cols-4">
            <Stat label="Delivered" value={stats.sent} />
            <Stat label="Not delivered" value={stats.failed} tone="danger" />
            <Stat label="Queued" value={stats.pending} />
            <Stat label="Channels" value={channels.length} />
          </dl>
        </CardBody>
      </Card>
    </>
  );
}

function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: 'danger';
}) {
  const colour =
    value === 0 ? 'text-slate-400' : tone === 'danger' ? 'text-danger-700' : 'text-slate-900';
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className={`mt-1 text-lg font-semibold ${colour}`}>{value}</dd>
    </div>
  );
}

/** Channels lists where notifications go and whether each one works. */
function Channels({ overview }: { overview: NotificationOverview }) {
  const channels = overview.channels ?? [];
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<NotificationChannel | null>(null);

  const test = useTestChannel();
  const remove = useDeleteChannel();

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="neutral" icon={<BellRing className="h-4 w-4" />} />}
        title="Channels"
        description="Where alerts, failed backups and security findings are sent."
        action={
          <RequirePermission permission={Permission.NotificationManage}>
            <Button
              onClick={() => setAdding(true)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add channel
            </Button>
          </RequirePermission>
        }
      />

      {channels.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<BellRing className="h-6 w-6" />}
            title="No channels"
            description="Add one so this panel can tell you when something goes wrong."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {channels.map((channel) => (
            <li key={channel.id} className="flex items-start gap-4 px-5 py-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-slate-900">
                  {channel.name}{' '}
                  <span className="font-normal text-slate-500">({channel.kind})</span>
                  {!channel.enabled && (
                    <span className="ml-2 text-xs text-slate-500">· disabled</span>
                  )}
                </p>
                <p className="mt-0.5 text-xs text-slate-500">
                  {describeChannel(channel)}
                </p>
                <p className="mt-1 text-xs">{describeHealth(channel)}</p>
              </div>
              <RequirePermission permission={Permission.NotificationManage}>
                <div className="flex shrink-0 gap-2">
                  <Button
                    variant="secondary"
                    onClick={() => test.mutate(channel.id)}
                    loading={test.isPending && test.variables === channel.id}
                    icon={<Send aria-hidden="true" className="h-4 w-4" />}
                  >
                    Send a test
                  </Button>
                  <Button
                    variant="ghost"
                    aria-label={`Delete ${channel.name}`}
                    onClick={() => setRemoving(channel)}
                    icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                  />
                </div>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      <CardBody>
        <p className="text-xs text-slate-500">
          Testing sends a real message through the channel now, while you are watching.
          It is the only way to find out that a channel works before the night it has to.
        </p>
      </CardBody>

      {adding && <ChannelForm overview={overview} onClose={() => setAdding(false)} />}

      <ConfirmDialog
        open={removing !== null}
        title={removing ? `Delete "${removing.name}"?` : ''}
        confirmLabel="Delete channel"
        destructive
        loading={remove.isPending}
        onClose={() => setRemoving(null)}
        onConfirm={() => {
          if (!removing) return;
          remove.mutate(removing.id, { onSuccess: () => setRemoving(null) });
        }}
      >
        Nothing will be sent here again. The record of what happened on this host is
        kept; the record of what was sent to this channel goes with it.
      </ConfirmDialog>
    </Card>
  );
}

function describeChannel(channel: NotificationChannel): string {
  const config = channel.config as Record<string, string | number | undefined>;
  const scope =
    channel.kinds && channel.kinds.length > 0
      ? channel.kinds.join(', ')
      : 'everything';
  const floor = `${channel.min_severity} and above`;

  switch (channel.kind) {
    case 'email':
      return `${String(config.from ?? '')} via ${String(config.host ?? '')} — ${floor}, ${scope}`;
    case 'telegram':
      return `Telegram chat ${String(config.chat_id ?? '')} — ${floor}, ${scope}`;
    case 'line':
      return `LINE ${String(config.to ?? '')} — ${floor}, ${scope}`;
    default:
      return `${floor}, ${scope}`;
  }
}

function describeHealth(channel: NotificationChannel): JSX.Element {
  if (channel.failure_streak > 0) {
    return (
      <span className="text-danger-700">
        Failing ({channel.failure_streak} in a row): {channel.last_error ?? 'no reason given'}
      </span>
    );
  }
  if (channel.last_success_at === null) {
    return (
      <span className="text-warn-700">
        Never delivered anything — send a test before relying on it
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 text-ok-700">
      <CheckCircle2 aria-hidden="true" className="h-3.5 w-3.5" />
      Last delivered {formatWhen(channel.last_success_at)}
    </span>
  );
}

/** Deliveries is the record of what arrived and what did not. */
function Deliveries({ overview }: { overview: NotificationOverview }) {
  const deliveries = overview.deliveries ?? [];

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="neutral" icon={<Send className="h-4 w-4" />} />}
        title="Delivery record"
        description="Every message this panel tried to send."
      />

      {deliveries.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Bell className="h-6 w-6" />}
            title="Nothing has been sent"
            description="Either nothing has gone wrong, or nothing is configured to hear about it."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {deliveries.map((delivery) => (
            <li key={delivery.id} className="flex items-start gap-3 px-5 py-3">
              <DeliveryIcon status={delivery.status} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-slate-900">{delivery.event_title}</p>
                <p className="mt-0.5 text-xs text-slate-500">
                  {delivery.channel_name} · {formatWhen(delivery.created_at)}
                  {delivery.attempts > 1 ? ` · ${delivery.attempts} attempts` : ''}
                </p>
                {delivery.status === 'failed' && delivery.last_error && (
                  <p className="mt-1 text-xs text-danger-700">{delivery.last_error}</p>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      <CardBody>
        {/* The reason this list is on the page at all. */}
        <p className="text-xs text-slate-500">
          This is the only place a failed notification is visible. A channel that is not
          working cannot deliver the message saying it is not working.
        </p>
      </CardBody>
    </Card>
  );
}

function DeliveryIcon({ status }: { status: NotificationDelivery['status'] }) {
  if (status === 'sent') {
    return (
      <CheckCircle2
        aria-label="delivered"
        className="mt-0.5 h-4 w-4 shrink-0 text-ok-600"
      />
    );
  }
  if (status === 'failed') {
    return (
      <XCircle aria-label="not delivered" className="mt-0.5 h-4 w-4 shrink-0 text-danger-600" />
    );
  }
  return (
    <AlertTriangle aria-label="queued" className="mt-0.5 h-4 w-4 shrink-0 text-slate-400" />
  );
}

/** ChannelForm collects a new channel. */
function ChannelForm({
  overview,
  onClose,
}: {
  overview: NotificationOverview;
  onClose: () => void;
}) {
  const create = useCreateChannel();
  const [kind, setKind] = useState<ChannelKind>('email');
  const [form, setForm] = useState({
    name: '',
    host: '',
    port: 587,
    security: 'starttls',
    username: '',
    password: '',
    from: '',
    to: '',
    token: '',
    chatID: '',
    recipient: '',
    minSeverity: 'warning' as NotifySeverity,
    allowInsecure: false,
  });

  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const submit = () => {
    const body: NotificationChannelInput = {
      name: form.name,
      kind,
      min_severity: form.minSeverity,
    };
    if (kind === 'email') {
      body.host = form.host;
      body.port = form.port;
      body.security = form.security;
      body.allow_insecure = form.allowInsecure;
      body.username = form.username;
      body.password = form.password;
      body.from = form.from;
      body.to = form.to
        .split(',')
        .map((address) => address.trim())
        .filter(Boolean);
    }
    if (kind === 'telegram') {
      body.token = form.token;
      body.chat_id = form.chatID;
    }
    if (kind === 'line') {
      body.token = form.token;
      body.recipient = form.recipient;
    }
    create.mutate(body, { onSuccess: onClose });
  };

  return (
    <Modal open title="Add a channel" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="That channel was not accepted">
            {create.error instanceof ApiError
              ? create.error.message
              : 'Check the details and try again.'}
          </AlertBanner>
        )}

        <TextField
          id="channel-name"
          label="Name"
          value={form.name}
          onChange={(event) => set('name', event.target.value)}
        />

        <SelectField
          id="channel-kind"
          label="Send by"
          value={kind}
          onChange={(event) => setKind(event.target.value as ChannelKind)}
        >
          <option value="email">Email</option>
          <option value="telegram">Telegram</option>
          <option value="line">LINE</option>
        </SelectField>

        <SelectField
          id="channel-severity"
          label="Tell me about"
          hint="How bad something has to be before this channel hears about it."
          value={form.minSeverity}
          onChange={(event) => set('minSeverity', event.target.value as NotifySeverity)}
        >
          {overview.severities.map((severity) => (
            <option key={severity} value={severity}>
              {severity} and above
            </option>
          ))}
        </SelectField>

        {kind === 'email' && (
          <>
            <TextField
              id="channel-host"
              label="Mail server"
              value={form.host}
              onChange={(event) => set('host', event.target.value)}
            />
            <TextField
              id="channel-port"
              label="Port"
              type="number"
              value={String(form.port)}
              onChange={(event) => set('port', Number(event.target.value))}
            />
            <SelectField
              id="channel-security"
              label="Encryption"
              hint="A mail server that is not on this machine must use TLS, or the password and every alert travel in the clear."
              value={form.security}
              onChange={(event) => set('security', event.target.value)}
            >
              <option value="starttls">STARTTLS (usually port 587)</option>
              <option value="tls">TLS from the start (usually port 465)</option>
              <option value="none">None — only for a server on this machine</option>
            </SelectField>
            {/* Off by default and never inferred from the host: accepting
                it is a decision somebody makes about a password and every
                alert travelling where anyone on the path can read them. */}
            {form.security === 'none' && (
              <Toggle
                id="channel-allow-insecure"
                label="Accept an unencrypted connection"
                description="Only for a relay on a private network. Without TLS the password and every alert travel in the clear."
                checked={form.allowInsecure}
                onChange={(value) => set('allowInsecure', value)}
              />
            )}
            <TextField
              id="channel-from"
              label="From"
              value={form.from}
              onChange={(event) => set('from', event.target.value)}
            />
            <TextField
              id="channel-to"
              label="To"
              hint="One or more addresses, separated by commas."
              value={form.to}
              onChange={(event) => set('to', event.target.value)}
            />
            <TextField
              id="channel-username"
              label="Username"
              value={form.username}
              onChange={(event) => set('username', event.target.value)}
            />
            <TextField
              id="channel-password"
              label="Password"
              type="password"
              hint="Stored encrypted and never shown again."
              value={form.password}
              onChange={(event) => set('password', event.target.value)}
            />
          </>
        )}

        {kind === 'telegram' && (
          <>
            <TextField
              id="channel-token"
              label="Bot token"
              type="password"
              hint="From BotFather. Stored encrypted and never shown again — anyone holding it can post as the bot."
              value={form.token}
              onChange={(event) => set('token', event.target.value)}
            />
            <TextField
              id="channel-chat"
              label="Chat ID"
              hint="The chat the bot posts to. A group's ID is negative."
              value={form.chatID}
              onChange={(event) => set('chatID', event.target.value)}
            />
          </>
        )}

        {kind === 'line' && (
          <>
            <TextField
              id="channel-token"
              label="Channel access token"
              type="password"
              hint="From the LINE Developers console. Stored encrypted and never shown again."
              value={form.token}
              onChange={(event) => set('token', event.target.value)}
            />
            <TextField
              id="channel-recipient"
              label="Send to"
              hint="A LINE user or group ID."
              value={form.recipient}
              onChange={(event) => set('recipient', event.target.value)}
            />
          </>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={submit} loading={create.isPending}>
            Save channel
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function formatWhen(value: string): string {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return value;
  return at.toLocaleString();
}
