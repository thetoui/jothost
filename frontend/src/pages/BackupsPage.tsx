import { useState } from 'react';
import {
  Archive,
  CalendarClock,
  CheckCircle2,
  HardDrive,
  Play,
  Plus,
  RotateCcw,
  ShieldCheck,
  Trash2,
} from 'lucide-react';

import { Alert as AlertBanner } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { useProfile } from '@/features/auth/hooks';
import { Permission } from '@/features/auth/permissions';
import {
  isPanelBackup,
  PANEL_RESTORE_HINT,
  panelBackupOption,
} from '@/features/backups/panel';
import {
  useBackupOverview,
  useCheckDestination,
  useCreateBackup,
  useCreateDestination,
  useCreateSchedule,
  useDeleteBackup,
  useDeleteDestination,
  useDeleteSchedule,
  useRestoreBackup,
  useRunSchedule,
  useVerifyBackup,
} from '@/features/backups/hooks';
import { useDatabases } from '@/features/databases/hooks';
import { useWebsites } from '@/features/websites/hooks';
import { ApiError } from '@/services/apiClient';
import type {
  Backup,
  BackupCapabilities,
  BackupDestination,
  BackupDestinationInput,
  BackupOverview,
  BackupSchedule,
  BackupScheduleInput,
  BackupType,
  DestinationKind,
} from '@/types/api';

/**
 * BackupsPage shows what has been backed up, where it went, and whether the
 * panel has confirmed it is still there.
 *
 * The distinction the whole page turns on is between "completed" and
 * "verified". An upload that returned success and a set of bytes that are
 * actually at the destination and actually match are different claims, and only
 * the second is a backup. Every list here says which it is.
 */
export function BackupsPage() {
  const { data, isPending, isError, error } = useBackupOverview();

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Backups</h1>
        <p className="mt-1 text-sm text-slate-500">
          What has been copied off this host, where it went, and whether the panel has read it
          back.
        </p>
      </header>

      {isError && (
        <AlertBanner tone="danger" title="The backups could not be read">
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
          <Capabilities overview={data} />
          <Destinations overview={data} />
          <Schedules overview={data} />
          <Backups overview={data} />
        </>
      )}
    </div>
  );
}

/** Capabilities says what this host can actually do, before anything is promised. */
function Capabilities({ overview }: { overview: BackupOverview }) {
  const { capabilities, stats } = overview;

  if (!capabilities.available) {
    return (
      <AlertBanner tone="danger" title="This host cannot take backups">
        {capabilities.reason ?? 'The host agent did not say why.'}
      </AlertBanner>
    );
  }

  return (
    <Card>
      <CardHeader
        icon={
          <TintedIcon tone="brand" icon={<Archive className="h-4 w-4" />} />
        }
        title="This host"
        description={
          stats.total === 0
            ? 'Nothing has been backed up yet.'
            : `${stats.verified} of ${stats.total} backups have been read back and confirmed.`
        }
      />
      <CardBody>
        <dl className="grid gap-4 sm:grid-cols-3">
          <Stat label="Confirmed intact" value={String(stats.verified)} />
          <Stat label="Failed" value={String(stats.failed)} />
          <Stat label="Stored" value={formatBytes(stats.bytes)} />
        </dl>

        {/* Named rather than hidden: a destination kind that cannot work has to
            be visible before somebody configures one and finds out at 3am. */}
        <p className="mt-4 text-xs text-slate-500">
          Destinations available here: local disk
          {capabilities.s3 ? ', S3-compatible storage' : ''}
          {capabilities.sftp ? ', SFTP' : ' (no sftp client is installed, so SFTP is unavailable)'}.
          {capabilities.engines && capabilities.engines.length > 0
            ? ` Databases that can be dumped: ${capabilities.engines.join(', ')}.`
            : ' No database engine on this host can be dumped.'}
        </p>
      </CardBody>
    </Card>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-1 text-lg font-semibold text-slate-900">{value}</dd>
    </div>
  );
}

/** Destinations lists where backups can go and whether the panel has been there. */
function Destinations({ overview }: { overview: BackupOverview }) {
  const destinations = overview.destinations ?? [];
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<BackupDestination | null>(null);

  const check = useCheckDestination();
  const remove = useDeleteDestination();

  return (
    <Card>
      <CardHeader
        icon={
          <TintedIcon tone="neutral" icon={<HardDrive className="h-4 w-4" />} />
        }
        title="Destinations"
        description="Where archives are written."
        action={
          <RequirePermission permission={Permission.BackupManage}>
            <Button
              onClick={() => setAdding(true)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add destination
            </Button>
          </RequirePermission>
        }
      />

      {destinations.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<HardDrive className="h-6 w-6" />}
            title="Nowhere to put a backup"
            description="Add a destination before scheduling anything, or backups will have nowhere to go."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {destinations.map((destination) => (
            <li key={destination.id} className="flex items-start gap-4 px-5 py-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-slate-900">{destination.name}</p>
                <p className="mt-0.5 text-xs text-slate-500">
                  {describeDestination(destination)}
                </p>
                <p className="mt-1 text-xs">{describeCheck(destination)}</p>
              </div>
              <RequirePermission permission={Permission.BackupManage}>
                <div className="flex shrink-0 gap-2">
                  <Button
                    variant="secondary"
                    onClick={() => check.mutate(destination.id)}
                    loading={check.isPending && check.variables === destination.id}
                    icon={<ShieldCheck aria-hidden="true" className="h-4 w-4" />}
                  >
                    Check
                  </Button>
                  <Button
                    variant="ghost"
                    aria-label={`Delete ${destination.name}`}
                    onClick={() => setRemoving(destination)}
                    icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                  />
                </div>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      <CardBody>
        {/* A destination that has never been reached is the most dangerous
            object on this page: it looks like protection and is not. */}
        <p className="text-xs text-slate-500">
          Checking writes a small file and reads it back, so a destination that cannot work says
          so now rather than at the first scheduled backup.
        </p>
      </CardBody>

      {adding && <DestinationForm onClose={() => setAdding(false)} />}

      <ConfirmDialog
        open={removing !== null}
        title={removing ? `Delete "${removing.name}"?` : ''}
        confirmLabel="Delete destination"
        destructive
        loading={remove.isPending}
        onClose={() => setRemoving(null)}
        onConfirm={() => {
          if (!removing) return;
          remove.mutate(removing.id, { onSuccess: () => setRemoving(null) });
        }}
      >
        The archives already written there are not touched. A destination a schedule still uses
        cannot be deleted.
      </ConfirmDialog>
    </Card>
  );
}

function describeDestination(destination: BackupDestination): string {
  const config = destination.config as Record<string, string | number | boolean | undefined>;
  switch (destination.kind) {
    case 'local':
      return `A directory on this host: ${String(config.directory ?? '')}`;
    case 's3': {
      const insecure = config.allow_insecure === true ? ' (plain http)' : '';
      return `S3 bucket ${String(config.bucket ?? '')} at ${String(
        config.endpoint ?? '',
      )}${insecure}`;
    }
    case 'sftp':
      return `SFTP to ${String(config.user ?? '')}@${String(config.host ?? '')}:${String(
        config.port ?? 22,
      )}${String(config.path ?? '')}`;
    default:
      return destination.kind;
  }
}

function describeCheck(destination: BackupDestination): JSX.Element {
  if (destination.last_check_at === null) {
    return (
      <span className="text-warn-700">
        Never reached — check it before relying on it
      </span>
    );
  }
  if (destination.last_check_ok) {
    return (
      <span className="text-ok-700">
        Written to and read back {formatWhen(destination.last_check_at)}
      </span>
    );
  }
  return (
    <span className="text-danger-700">
      Could not be used: {destination.last_check_detail ?? 'no reason was given'}
    </span>
  );
}

/** DestinationForm collects a new destination. */
function DestinationForm({ onClose }: { onClose: () => void }) {
  const create = useCreateDestination();
  const [kind, setKind] = useState<DestinationKind>('local');
  const [form, setForm] = useState<Required<Omit<BackupDestinationInput, 'kind'>>>({
    name: '',
    directory: '/var/lib/jothost/backups',
    endpoint: '',
    region: 'us-east-1',
    bucket: '',
    prefix: '',
    access_key: '',
    secret_key: '',
    path_style: true,
    allow_insecure: false,
    host: '',
    port: 22,
    user: '',
    path: '',
    private_key: '',
    host_key: '',
  });

  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const submit = () => {
    const body: BackupDestinationInput = { name: form.name, kind };
    if (kind === 'local') {
      body.directory = form.directory;
    }
    if (kind === 's3') {
      body.endpoint = form.endpoint;
      body.region = form.region;
      body.bucket = form.bucket;
      body.prefix = form.prefix;
      body.access_key = form.access_key;
      body.secret_key = form.secret_key;
      body.path_style = form.path_style;
      body.allow_insecure = form.allow_insecure;
    }
    if (kind === 'sftp') {
      body.host = form.host;
      body.port = form.port;
      body.user = form.user;
      body.path = form.path;
      body.private_key = form.private_key;
      body.host_key = form.host_key;
    }
    create.mutate(body, { onSuccess: onClose });
  };

  return (
    <Modal open title="Add a destination" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="That destination was not accepted">
            {create.error instanceof ApiError
              ? create.error.message
              : 'Check the details and try again.'}
          </AlertBanner>
        )}

        <TextField
          id="destination-name"
          label="Name"
          value={form.name}
          onChange={(event) => set('name', event.target.value)}
        />

        <SelectField
          id="destination-kind"
          label="Kind"
          value={kind}
          onChange={(event) => setKind(event.target.value as DestinationKind)}
        >
          <option value="local">A directory on this host</option>
          <option value="s3">S3-compatible storage</option>
          <option value="sftp">Another machine over SFTP</option>
        </SelectField>

        {kind === 'local' && (
          <TextField
            id="destination-directory"
            label="Directory"
            hint="It has to be one of the directories this agent is allowed to write backups into."
            value={form.directory}
            onChange={(event) => set('directory', event.target.value)}
          />
        )}

        {kind === 's3' && (
          <>
            <TextField
              id="destination-endpoint"
              label="Endpoint"
              hint="https, unless the service is on this machine."
              value={form.endpoint}
              onChange={(event) => set('endpoint', event.target.value)}
            />
            <TextField
              id="destination-bucket"
              label="Bucket"
              value={form.bucket}
              onChange={(event) => set('bucket', event.target.value)}
            />
            <TextField
              id="destination-region"
              label="Region"
              hint="Part of the signature. Use us-east-1 if the service ignores it."
              value={form.region}
              onChange={(event) => set('region', event.target.value)}
            />
            <TextField
              id="destination-access-key"
              label="Access key"
              value={form.access_key}
              onChange={(event) => set('access_key', event.target.value)}
            />
            <TextField
              id="destination-secret-key"
              label="Secret key"
              type="password"
              hint="Stored encrypted and never shown again."
              value={form.secret_key}
              onChange={(event) => set('secret_key', event.target.value)}
            />
            <Toggle
              id="destination-path-style"
              label="Address the bucket in the path"
              description="Needed by every self-hosted S3 service. AWS itself does not need it."
              checked={form.path_style}
              onChange={(value) => set('path_style', value)}
            />
            {/* Off by default and never inferred from the URL: accepting it is
                a decision somebody makes about a copy of every site on this
                host travelling where anyone on the path can read it. */}
            <Toggle
              id="destination-allow-insecure"
              label="Accept a plain http endpoint"
              description="Only for storage on a private network. Without https the backup — every file of every site — travels in the clear."
              checked={form.allow_insecure}
              onChange={(value) => set('allow_insecure', value)}
            />
          </>
        )}

        {kind === 'sftp' && (
          <>
            <TextField
              id="destination-host"
              label="Host"
              value={form.host}
              onChange={(event) => set('host', event.target.value)}
            />
            <TextField
              id="destination-port"
              label="Port"
              type="number"
              value={String(form.port)}
              onChange={(event) => set('port', Number(event.target.value))}
            />
            <TextField
              id="destination-user"
              label="Username"
              value={form.user}
              onChange={(event) => set('user', event.target.value)}
            />
            <TextField
              id="destination-path"
              label="Remote directory"
              value={form.path}
              onChange={(event) => set('path', event.target.value)}
            />
            <TextField
              id="destination-host-key"
              label="Host key"
              hint="The server's public key, as one known_hosts line. Without it there is no way to tell the intended server from whatever answers."
              value={form.host_key}
              onChange={(event) => set('host_key', event.target.value)}
            />
            <TextField
              id="destination-private-key"
              label="Private key"
              hint="An OpenSSH private key with no passphrase. Stored encrypted."
              value={form.private_key}
              onChange={(event) => set('private_key', event.target.value)}
            />
          </>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={submit} loading={create.isPending}>
            Save destination
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/** Schedules lists the standing instructions to take a backup. */
function Schedules({ overview }: { overview: BackupOverview }) {
  const schedules = overview.schedules ?? [];
  const destinations = overview.destinations ?? [];
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<BackupSchedule | null>(null);

  const run = useRunSchedule();
  const remove = useDeleteSchedule();

  return (
    <Card>
      <CardHeader
        icon={
          <TintedIcon tone="neutral" icon={<CalendarClock className="h-4 w-4" />} />
        }
        title="Schedules"
        description="When backups are taken, and how long they are kept."
        action={
          <RequirePermission permission={Permission.BackupManage}>
            <Button
              onClick={() => setAdding(true)}
              disabled={destinations.length === 0}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add schedule
            </Button>
          </RequirePermission>
        }
      />

      {schedules.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<CalendarClock className="h-6 w-6" />}
            title="Nothing is backed up automatically"
            description="A backup somebody has to remember to take is a backup that stops happening."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {schedules.map((schedule) => (
            <li key={schedule.id} className="flex items-start gap-4 px-5 py-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-slate-900">{schedule.name}</p>
                <p className="mt-0.5 text-xs text-slate-500">{describeSchedule(schedule)}</p>
                <p className="mt-1 text-xs text-slate-500">
                  {schedule.last_run_at
                    ? `Last run ${formatWhen(schedule.last_run_at)} — ${
                        schedule.last_status ?? 'unknown'
                      }`
                    : 'Has not run yet'}
                </p>
              </div>
              <RequirePermission
                permission={
                  isPanelBackup(schedule) ? Permission.ServerManage : Permission.BackupManage
                }
              >
                <div className="flex shrink-0 gap-2">
                  <Button
                    variant="secondary"
                    onClick={() => run.mutate(schedule.id)}
                    loading={run.isPending && run.variables === schedule.id}
                    icon={<Play aria-hidden="true" className="h-4 w-4" />}
                  >
                    Run now
                  </Button>
                  <Button
                    variant="ghost"
                    aria-label={`Delete ${schedule.name}`}
                    onClick={() => setRemoving(schedule)}
                    icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                  />
                </div>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      {adding && (
        <ScheduleForm
          destinations={destinations}
          capabilities={overview.capabilities}
          onClose={() => setAdding(false)}
        />
      )}

      <ConfirmDialog
        open={removing !== null}
        title={removing ? `Stop backing up with "${removing.name}"?` : ''}
        confirmLabel="Delete schedule"
        destructive
        loading={remove.isPending}
        onClose={() => setRemoving(null)}
        onConfirm={() => {
          if (!removing) return;
          remove.mutate(removing.id, { onSuccess: () => setRemoving(null) });
        }}
      >
        The backups it has already taken are kept. They are the only reason it existed.
      </ConfirmDialog>
    </Card>
  );
}

const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

function describeSchedule(schedule: BackupSchedule): string {
  const when =
    schedule.day_of_week === -1
      ? 'Every day'
      : `Every ${DAY_NAMES[schedule.day_of_week] ?? 'day'}`;
  const time = `${String(schedule.hour).padStart(2, '0')}:${String(schedule.minute).padStart(
    2,
    '0',
  )} UTC`;
  const subject =
    schedule.type === 'panel'
      ? "the panel's own database, sealed"
      : schedule.type === 'full'
        ? 'everything on this host'
        : schedule.type === 'website'
          ? 'one website'
          : 'one database';
  return `${when} at ${time} — ${subject}, to ${schedule.destination_name}, kept ${schedule.retention_days} days (always keeping the last ${schedule.keep_last})`;
}

/** ScheduleForm collects a new schedule. */
function ScheduleForm({
  destinations,
  capabilities,
  onClose,
}: {
  destinations: BackupDestination[];
  capabilities: BackupCapabilities;
  onClose: () => void;
}) {
  const create = useCreateSchedule();
  const { data: profile } = useProfile();
  const panel = panelBackupOption(capabilities, profile);
  const websites = useWebsites();
  const databases = useDatabases();

  const [form, setForm] = useState<Required<BackupScheduleInput>>({
    name: '',
    type: 'full',
    website_id: '',
    database_id: '',
    destination_id: destinations[0]?.id ?? '',
    hour: 3,
    minute: 0,
    day_of_week: -1,
    retention_days: 14,
    keep_last: 3,
    enabled: true,
  });

  const set = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const submit = () => {
    const body: BackupScheduleInput = {
      name: form.name,
      type: form.type,
      destination_id: form.destination_id,
      hour: form.hour,
      minute: form.minute,
      day_of_week: form.day_of_week,
      retention_days: form.retention_days,
      keep_last: form.keep_last,
      enabled: form.enabled,
    };
    if (form.type === 'website') body.website_id = form.website_id;
    if (form.type === 'database') body.database_id = form.database_id;
    create.mutate(body, { onSuccess: onClose });
  };

  return (
    <Modal open title="Add a schedule" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="That schedule was not accepted">
            {create.error instanceof ApiError
              ? create.error.message
              : 'Check the details and try again.'}
          </AlertBanner>
        )}

        <TextField
          id="schedule-name"
          label="Name"
          value={form.name}
          onChange={(event) => set('name', event.target.value)}
        />

        <SelectField
          id="schedule-type"
          label="Back up"
          value={form.type}
          onChange={(event) => set('type', event.target.value as BackupScheduleInput['type'])}
        >
          <option value="full">Everything on this host</option>
          <option value="website">One website</option>
          <option value="database">One database</option>
          {panel.offered && (
            <option value="panel" disabled={!panel.usable}>
              {panel.usable
                ? "The panel's own database (sealed)"
                : `The panel's own database — ${panel.reason ?? 'unavailable'}`}
            </option>
          )}
        </SelectField>

        {form.type === 'website' && (
          <SelectField
            id="schedule-website"
            label="Which website"
            value={form.website_id}
            onChange={(event) => set('website_id', event.target.value)}
          >
            <option value="">Choose a website</option>
            {(websites.data?.websites ?? []).map((site) => (
              <option key={site.id} value={site.id}>
                {site.primary_domain}
              </option>
            ))}
          </SelectField>
        )}

        {form.type === 'database' && (
          <SelectField
            id="schedule-database"
            label="Which database"
            value={form.database_id}
            onChange={(event) => set('database_id', event.target.value)}
          >
            <option value="">Choose a database</option>
            {(databases.data?.databases ?? []).map((database) => (
              <option key={database.id} value={database.id}>
                {database.name}
              </option>
            ))}
          </SelectField>
        )}

        <SelectField
          id="schedule-destination"
          label="Destination"
          value={form.destination_id}
          onChange={(event) => set('destination_id', event.target.value)}
        >
          {destinations.map((destination) => (
            <option key={destination.id} value={destination.id}>
              {destination.name}
            </option>
          ))}
        </SelectField>

        <div className="grid gap-4 sm:grid-cols-3">
          <SelectField
            id="schedule-day"
            label="Day"
            value={String(form.day_of_week)}
            onChange={(event) => set('day_of_week', Number(event.target.value))}
          >
            <option value="-1">Every day</option>
            {DAY_NAMES.map((day, index) => (
              <option key={day} value={String(index)}>
                {day}
              </option>
            ))}
          </SelectField>
          <TextField
            id="schedule-hour"
            label="Hour (UTC)"
            type="number"
            value={String(form.hour)}
            onChange={(event) => set('hour', Number(event.target.value))}
          />
          <TextField
            id="schedule-minute"
            label="Minute"
            type="number"
            value={String(form.minute)}
            onChange={(event) => set('minute', Number(event.target.value))}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <TextField
            id="schedule-retention"
            label="Keep for (days)"
            type="number"
            value={String(form.retention_days)}
            onChange={(event) => set('retention_days', Number(event.target.value))}
          />
          <TextField
            id="schedule-keep-last"
            label="Always keep the last"
            type="number"
            hint="The floor that makes keeping by age safe: a panel that was off for a fortnight must not come back and delete everything."
            value={String(form.keep_last)}
            onChange={(event) => set('keep_last', Number(event.target.value))}
          />
        </div>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={submit} loading={create.isPending}>
            Save schedule
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/** Backups lists the archives themselves. */
function Backups({ overview }: { overview: BackupOverview }) {
  const backups = overview.backups ?? [];
  const destinations = overview.destinations ?? [];
  const [taking, setTaking] = useState(false);
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [removing, setRemoving] = useState<Backup | null>(null);

  const verify = useVerifyBackup();
  const restore = useRestoreBackup();
  const remove = useDeleteBackup();

  return (
    <Card>
      <CardHeader
        icon={
          <TintedIcon tone="neutral" icon={<Archive className="h-4 w-4" />} />
        }
        title="Backups"
        description="Every archive this panel has taken."
        action={
          <RequirePermission permission={Permission.BackupManage}>
            <Button
              onClick={() => setTaking(true)}
              disabled={destinations.length === 0}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Take a backup
            </Button>
          </RequirePermission>
        }
      />

      {backups.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Archive className="h-6 w-6" />}
            title="No backups yet"
            description="Nothing on this host has been copied anywhere."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {backups.map((backup) => (
            <li key={backup.id} className="flex items-start gap-4 px-5 py-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-slate-900">
                  {backup.subject}{' '}
                  <span className="font-normal text-slate-500">({backup.type})</span>
                </p>
                <p className="mt-0.5 text-xs text-slate-500">
                  {formatWhen(backup.created_at)} to {backup.destination}
                  {backup.size_bytes !== null ? ` — ${formatBytes(backup.size_bytes)}` : ''}
                </p>
                <p className="mt-1 text-xs">{describeBackup(backup)}</p>
                {isPanelBackup(backup) && (
                  <p className="mt-1 text-xs text-slate-500">{PANEL_RESTORE_HINT}</p>
                )}
              </div>
              <RequirePermission
                permission={
                  isPanelBackup(backup) ? Permission.ServerManage : Permission.BackupManage
                }
              >
                <div className="flex shrink-0 gap-2">
                  {backup.status === 'completed' && (
                    <Button
                      variant="secondary"
                      onClick={() => verify.mutate(backup.id)}
                      loading={verify.isPending && verify.variables === backup.id}
                      icon={<ShieldCheck aria-hidden="true" className="h-4 w-4" />}
                    >
                      Verify
                    </Button>
                  )}
                  {backup.verified_at !== null && !isPanelBackup(backup) && (
                    <Button
                      variant="secondary"
                      onClick={() => setRestoring(backup)}
                      icon={<RotateCcw aria-hidden="true" className="h-4 w-4" />}
                    >
                      Restore
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    aria-label={`Delete the backup of ${backup.subject}`}
                    onClick={() => setRemoving(backup)}
                    icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                  />
                </div>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      <CardBody>
        {/* The rule the page exists to make visible. */}
        <p className="text-xs text-slate-500">
          Only a backup the panel has read back and confirmed can be restored. An archive that
          was written and could not be checked is listed as failed, because there is no basis
          for saying it would work.
        </p>
      </CardBody>

      {taking && (
        <TakeBackupForm
          destinations={destinations}
          capabilities={overview.capabilities}
          onClose={() => setTaking(false)}
        />
      )}

      <ConfirmDialog
        open={restoring !== null}
        title={restoring ? `Restore ${restoring.subject}?` : ''}
        confirmLabel="Restore over the live files"
        destructive
        loading={restore.isPending}
        onClose={() => setRestoring(null)}
        onConfirm={() => {
          if (!restoring) return;
          restore.mutate(
            { id: restoring.id, keepPrevious: false },
            { onSuccess: () => setRestoring(null) },
          );
        }}
      >
        This replaces what is on the host now with what was in the backup. The panel checks the
        whole archive before it changes anything, and puts the previous files back if the
        restore fails partway.
      </ConfirmDialog>

      <ConfirmDialog
        open={removing !== null}
        title={removing ? `Delete the backup of ${removing.subject}?` : ''}
        confirmLabel="Delete backup"
        destructive
        loading={remove.isPending}
        onClose={() => setRemoving(null)}
        onConfirm={() => {
          if (!removing) return;
          remove.mutate(removing.id, { onSuccess: () => setRemoving(null) });
        }}
      >
        The archive is removed from its destination as well as from this list. It cannot be got
        back.
      </ConfirmDialog>
    </Card>
  );
}

function describeBackup(backup: Backup): JSX.Element {
  if (backup.status === 'pending' || backup.status === 'running') {
    return <span className="text-slate-500">Running…</span>;
  }
  if (backup.status === 'failed') {
    return (
      <span className="text-danger-700">
        Failed: {backup.error ?? backup.verify_detail ?? 'no reason was recorded'}
      </span>
    );
  }
  if (backup.verified_at !== null) {
    return (
      <span className="inline-flex items-center gap-1 text-ok-700">
        <CheckCircle2 aria-hidden="true" className="h-3.5 w-3.5" />
        Read back and confirmed {formatWhen(backup.verified_at)}
      </span>
    );
  }
  return (
    <span className="text-warn-700">
      Written, not confirmed — verify it before relying on it
    </span>
  );
}

/** TakeBackupForm asks what to back up and where. */
function TakeBackupForm({
  destinations,
  capabilities,
  onClose,
}: {
  destinations: BackupDestination[];
  capabilities: BackupCapabilities;
  onClose: () => void;
}) {
  const create = useCreateBackup();
  const { data: profile } = useProfile();
  const panel = panelBackupOption(capabilities, profile);
  const websites = useWebsites();
  const databases = useDatabases();

  const [type, setType] = useState<BackupType>('full');
  const [websiteID, setWebsiteID] = useState('');
  const [databaseID, setDatabaseID] = useState('');
  const [destinationID, setDestinationID] = useState(destinations[0]?.id ?? '');
  const [includeDatabases, setIncludeDatabases] = useState(true);

  const submit = () => {
    const body: {
      type: string;
      website_id?: string;
      database_id?: string;
      destination_id: string;
      include_databases?: boolean;
    } = { type, destination_id: destinationID, include_databases: includeDatabases };
    if (type === 'website') body.website_id = websiteID;
    if (type === 'database') body.database_id = databaseID;
    create.mutate(body, { onSuccess: onClose });
  };

  return (
    <Modal open title="Take a backup" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="That backup could not be started">
            {create.error instanceof ApiError
              ? create.error.message
              : 'Check the details and try again.'}
          </AlertBanner>
        )}

        <SelectField
          id="backup-type"
          label="Back up"
          value={type}
          onChange={(event) => setType(event.target.value as typeof type)}
        >
          <option value="full">Everything on this host</option>
          <option value="website">One website</option>
          <option value="database">One database</option>
          {panel.offered && (
            <option value="panel" disabled={!panel.usable}>
              {panel.usable
                ? "The panel's own database (sealed)"
                : `The panel's own database — ${panel.reason ?? 'unavailable'}`}
            </option>
          )}
        </SelectField>

        {type === 'panel' && (
          <AlertBanner tone="info" title="Keep the encryption key somewhere else">
            This archive is sealed with a key derived from this panel&apos;s ENCRYPTION_KEY. It
            can only be restored with that key, so export it with install.sh export-key and store
            it off this host. It is restored from the host, not from this page.
          </AlertBanner>
        )}

        {type === 'website' && (
          <>
            <SelectField
              id="backup-website"
              label="Which website"
              value={websiteID}
              onChange={(event) => setWebsiteID(event.target.value)}
            >
              <option value="">Choose a website</option>
              {(websites.data?.websites ?? []).map((site) => (
                <option key={site.id} value={site.id}>
                  {site.primary_domain}
                </option>
              ))}
            </SelectField>
            <Toggle
              id="backup-include-databases"
              label="Include its databases"
              description="A site restored without the schema its application expects is broken in a more confusing way than one that is simply gone."
              checked={includeDatabases}
              onChange={setIncludeDatabases}
            />
          </>
        )}

        {type === 'database' && (
          <SelectField
            id="backup-database"
            label="Which database"
            value={databaseID}
            onChange={(event) => setDatabaseID(event.target.value)}
          >
            <option value="">Choose a database</option>
            {(databases.data?.databases ?? []).map((database) => (
              <option key={database.id} value={database.id}>
                {database.name}
              </option>
            ))}
          </SelectField>
        )}

        <SelectField
          id="backup-destination"
          label="Destination"
          value={destinationID}
          onChange={(event) => setDestinationID(event.target.value)}
        >
          {destinations.map((destination) => (
            <option key={destination.id} value={destination.id}>
              {destination.name}
            </option>
          ))}
        </SelectField>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={submit} loading={create.isPending}>
            Start backup
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/** formatBytes renders a size the way an operator reads one. */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let value = bytes;
  for (const unit of units) {
    value /= 1024;
    if (value < 1024) return `${value.toFixed(1)} ${unit}`;
  }
  return `${(value / 1024).toFixed(1)} PiB`;
}

/** formatWhen renders a timestamp as something readable. */
function formatWhen(value: string): string {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return value;
  return at.toLocaleString();
}
