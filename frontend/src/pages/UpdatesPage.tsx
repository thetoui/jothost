import { useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Clock,
  Download,
  History,
  Lock,
  PackageSearch,
  RefreshCw,
  ShieldAlert,
} from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useApplyUpdates,
  useCheckUpdates,
  useSaveUpdateSettings,
  useUpdateOverview,
} from '@/features/updates/hooks';
import { ApiError } from '@/services/apiClient';
import type { UpdateOverview, UpdatePackage, UpdateRun } from '@/types/api';

/**
 * UpdatesPage shows how far behind this host is, and applies what is waiting.
 *
 * The page is built around one distinction, because getting it wrong is the
 * whole failure mode: "nothing outstanding" and "we could not find out" look
 * identical in a package manager's output, and only one of them means the
 * machine is safe. Everything here that could say "up to date" checks first
 * whether the panel actually knows.
 */
export function UpdatesPage() {
  const { data, isPending, isError, error } = useUpdateOverview();
  const check = useCheckUpdates();

  const checkError = check.error instanceof ApiError ? check.error.message : null;

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-ink-strong">System updates</h1>
          <p className="mt-1 text-sm text-ink-muted">
            What this host has waiting, and what has been applied.
          </p>
        </div>
        <RequirePermission permission={Permission.UpdateManage}>
          <Button
            onClick={() => check.mutate()}
            loading={check.isPending}
            icon={<RefreshCw aria-hidden="true" className="h-4 w-4" />}
          >
            Check now
          </Button>
        </RequirePermission>
      </header>

      {isError && (
        <Alert tone="danger" title="The update status could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}
      {checkError && (
        <Alert tone="danger" title="The check could not be run">
          {checkError}
        </Alert>
      )}

      {isPending && !data ? (
        <Card>
          <CardBody>
            <SkeletonRows rows={4} />
          </CardBody>
        </Card>
      ) : !data ? null : (
        <>
          <Summary overview={data} />
          {data.running && <RunningNotice run={data.running} />}
          <PendingList overview={data} />
          <RuntimeList overview={data} />
          <HeldList overview={data} />
          <AutomaticUpdates overview={data} />
          <RecentRuns overview={data} />
        </>
      )}
    </div>
  );
}

/**
 * Summary is the one sentence somebody reads before deciding they are safe.
 *
 * Which is why it has three states rather than two. A host nobody has checked
 * and a host whose check failed are both "not known", and neither is "up to
 * date" — saying otherwise would be the panel making a claim it cannot support.
 */
function Summary({ overview }: { overview: UpdateOverview }) {
  const { check, has_check: hasCheck } = overview;

  if (!hasCheck) {
    return (
      <Alert tone="info" title="This host has not been checked yet">
        Nothing is known about what it has waiting. Checking refreshes its package index, so it
        reaches the network and takes a moment.
      </Alert>
    );
  }

  if (!check.succeeded) {
    return (
      <Alert tone="warning" title="What this host needs is not known">
        {check.reason ||
          'The last check could not read this host&rsquo;s package repositories.'}{' '}
        {check.unavailable_repositories > 0 && (
          <>
            {check.unavailable_repositories}{' '}
            {check.unavailable_repositories === 1 ? 'repository' : 'repositories'} could not be
            reached.{' '}
          </>
        )}
        An empty list here would mean the same thing as &ldquo;nothing to do&rdquo;, so the panel
        says nothing rather than the wrong thing.
      </Alert>
    );
  }

  const packages = check.packages ?? [];

  if (packages.length === 0) {
    return (
      <Alert tone="success" title="This host is up to date">
        Checked {formatWhen(check.checked_at)}. Its package manager is {check.manager}.
        {check.reboot_required && ' A restart is still needed to finish applying what is installed.'}
      </Alert>
    );
  }

  return (
    <Alert
      tone={check.security_count > 0 ? 'danger' : 'warning'}
      title={`${packages.length} ${packages.length === 1 ? 'update' : 'updates'} available`}
    >
      {check.security_known ? (
        check.security_count > 0 ? (
          <>
            {check.security_count} of them{' '}
            {check.security_count === 1 ? 'is a security fix' : 'are security fixes'}.
          </>
        ) : (
          <>None of them are marked as security fixes.</>
        )
      ) : (
        // Saying "0 security updates" here would answer a question this host's
        // package manager was never asked.
        <>
          This host&rsquo;s package manager ({check.manager}) does not mark security updates, so
          which of these are security fixes cannot be told apart here.
        </>
      )}{' '}
      Checked {formatWhen(check.checked_at)}.
    </Alert>
  );
}

/** RunningNotice says an update is in flight. */
function RunningNotice({ run }: { run: UpdateRun }) {
  return (
    <Alert tone="info" title="An update is running">
      Started {formatWhen(run.started_at)}
      {run.trigger === 'scheduled' && ' by the schedule'}. Nothing else can be applied until it
      finishes.
    </Alert>
  );
}

/** PendingList is what is outstanding, and the buttons that apply it. */
function PendingList({ overview }: { overview: UpdateOverview }) {
  const apply = useApplyUpdates();
  const [confirming, setConfirming] = useState<'all' | 'security' | null>(null);

  const packages = overview.check.packages ?? [];
  const running = Boolean(overview.running);
  const failure = apply.error instanceof ApiError ? apply.error.message : null;

  if (!overview.has_check || !overview.check.succeeded) {
    return null;
  }

  return (
    <Card label="Available updates">
      <CardHeader
        title="Available updates"
        description={
          packages.length === 0
            ? 'Nothing outstanding.'
            : `${packages.length} ${packages.length === 1 ? 'package' : 'packages'}`
        }
        icon={<TintedIcon icon={<PackageSearch className="h-5 w-5" />} tone="neutral" />}
        action={
          packages.length > 0 ? (
            <RequirePermission permission={Permission.UpdateManage}>
              <span className="flex flex-wrap gap-2">
                {overview.check.security_known && overview.check.security_count > 0 && (
                  <Button
                    onClick={() => setConfirming('security')}
                    disabled={running}
                    icon={<ShieldAlert aria-hidden="true" className="h-4 w-4" />}
                  >
                    Apply security fixes
                  </Button>
                )}
                <Button
                  variant="primary"
                  onClick={() => setConfirming('all')}
                  disabled={running}
                  icon={<Download aria-hidden="true" className="h-4 w-4" />}
                >
                  Apply all
                </Button>
              </span>
            </RequirePermission>
          ) : null
        }
      />
      <CardBody>
        {failure && (
          <Alert tone="danger" title="The update could not be applied" className="mb-3">
            {failure}
          </Alert>
        )}
        {packages.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 className="h-6 w-6" />}
            title="Nothing to apply"
            description="Everything this host's package manager offers is already installed."
          />
        ) : (
          <PackageTable packages={packages} securityKnown={overview.check.security_known} />
        )}
      </CardBody>

      <ConfirmDialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          apply.mutate(
            confirming === 'security' ? { security_only: true } : {},
            { onSuccess: () => setConfirming(null) },
          );
        }}
        title={confirming === 'security' ? 'Apply security fixes?' : 'Apply all updates?'}
        description="Installing updates restarts the daemons whose packages change."
        confirmLabel="Apply"
        loading={apply.isPending}
        error={failure}
      >
        <p className="text-sm text-ink">
          A package manager resolves dependencies, so this usually moves more packages than are
          listed — every one that actually changes is recorded in the history below.
        </p>
      </ConfirmDialog>
    </Card>
  );
}

/** PackageTable lists packages with their versions. */
function PackageTable({
  packages,
  securityKnown,
}: {
  packages: UpdatePackage[];
  securityKnown: boolean;
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-sm">
        <thead className="text-xs uppercase tracking-wide text-ink-dim">
          <tr>
            <th className="py-1 pr-3 font-normal">Package</th>
            <th className="py-1 pr-3 font-normal">Installed</th>
            <th className="py-1 pr-3 font-normal">Available</th>
            {securityKnown && <th className="py-1 font-normal">Security</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {packages.map((pkg) => (
            <tr key={pkg.name}>
              <td className="py-1.5 pr-3 font-medium text-ink-strong">{pkg.name}</td>
              <td className="py-1.5 pr-3 font-mono text-xs text-ink-muted">{pkg.installed}</td>
              <td className="py-1.5 pr-3 font-mono text-xs text-ink">{pkg.available}</td>
              {securityKnown && (
                <td className="py-1.5">
                  {pkg.security && (
                    <span className="inline-flex items-center gap-1 rounded bg-danger-50 px-1.5 py-0.5 text-xs text-danger-700">
                      <ShieldAlert aria-hidden="true" className="h-3 w-3" />
                      Security
                    </span>
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * RuntimeList is the PHP and Node updates, picked out of the same list.
 *
 * A view rather than a second check: a PHP update *is* a package update, and
 * asking the host twice would be two answers that can disagree. What the panel
 * adds is knowing which packages are PHP, because it installed them.
 */
function RuntimeList({ overview }: { overview: UpdateOverview }) {
  const php = overview.runtime.php ?? [];
  const node = overview.runtime.node ?? [];

  if (php.length === 0 && node.length === 0) {
    return null;
  }

  return (
    <Card label="Runtime updates">
      <CardHeader
        title="Runtimes"
        description="Updates to the language runtimes your sites run on. Applying these restarts them."
      />
      <CardBody className="space-y-4">
        {php.length > 0 && (
          <div>
            <h3 className="mb-1 text-sm font-semibold text-ink-strong">PHP</h3>
            <PackageTable packages={php} securityKnown={overview.check.security_known} />
          </div>
        )}
        {node.length > 0 && (
          <div>
            <h3 className="mb-1 text-sm font-semibold text-ink-strong">Node.js</h3>
            <PackageTable packages={node} securityKnown={overview.check.security_known} />
          </div>
        )}
      </CardBody>
    </Card>
  );
}

/**
 * HeldList is what has something newer available that the host will not move.
 *
 * Its own section, because these are not outstanding work: somebody pinned
 * them. Listing them with the pending updates would show a queue that never
 * empties however many times it was applied.
 */
function HeldList({ overview }: { overview: UpdateOverview }) {
  const held = overview.check.held ?? [];
  if (held.length === 0) {
    return null;
  }

  return (
    <Card label="Held back">
      <CardHeader
        title="Held back"
        description="Newer versions exist and this host will not install them."
        icon={<TintedIcon icon={<Lock className="h-5 w-5" />} tone="neutral" />}
      />
      <CardBody>
        <p className="mb-3 text-sm text-ink-muted">
          These were pinned deliberately, so the panel leaves them alone. They are listed here
          rather than with the updates above, where they would look like work that never gets done.
        </p>
        <ul className="space-y-2 text-sm">
          {held.map((entry) => (
            <li key={entry.name} className="flex flex-wrap items-baseline gap-2">
              <span className="font-medium text-ink-strong">{entry.name}</span>
              <span className="font-mono text-xs text-ink-muted">
                {entry.installed} → {entry.available}
              </span>
              <span className="text-xs text-ink-muted">{entry.reason}</span>
            </li>
          ))}
        </ul>
      </CardBody>
    </Card>
  );
}

/** AutomaticUpdates is the policy and the window. */
function AutomaticUpdates({ overview }: { overview: UpdateOverview }) {
  const save = useSaveUpdateSettings();
  const { settings } = overview;

  const [policy, setPolicy] = useState(settings.policy);
  const [day, setDay] = useState(String(settings.day_of_week));
  const [hour, setHour] = useState(String(settings.hour));
  const [minute, setMinute] = useState(String(settings.minute));
  const [interval, setInterval] = useState(String(settings.check_interval_hours));
  const [excluded, setExcluded] = useState(settings.excluded.join(', '));

  const failure = save.error instanceof ApiError ? save.error.message : null;
  const securityImpossible = overview.has_check && !overview.check.security_known;

  return (
    <Card label="Automatic updates">
      <CardHeader
        title="Automatic updates"
        description="What the panel applies on its own, and when."
        icon={<TintedIcon icon={<Clock className="h-5 w-5" />} tone="neutral" />}
      />
      <CardBody className="space-y-4">
        {failure && (
          <Alert tone="danger" title="The settings could not be saved">
            {failure}
          </Alert>
        )}

        <SelectField
          id="updates-policy"
          label="Apply automatically"
          hint="Installing updates restarts daemons, which is why the default is to apply nothing."
          value={policy}
          onChange={(event) => setPolicy(event.target.value as typeof policy)}
        >
          <option value="off">Nothing — report only</option>
          <option value="security">Security fixes only</option>
          <option value="all">Everything</option>
        </SelectField>

        {policy === 'security' && securityImpossible && (
          <Alert tone="warning" title="This host cannot identify security fixes">
            Its package manager ({overview.check.manager}) does not mark them, so this setting
            would apply nothing at all. &ldquo;Everything&rdquo; or a manual apply are the two that
            do something here.
          </Alert>
        )}

        <div className="grid gap-4 sm:grid-cols-3">
          <SelectField
            id="updates-day"
            label="Day"
            value={day}
            onChange={(event) => setDay(event.target.value)}
          >
            <option value="-1">Every day</option>
            <option value="0">Sunday</option>
            <option value="1">Monday</option>
            <option value="2">Tuesday</option>
            <option value="3">Wednesday</option>
            <option value="4">Thursday</option>
            <option value="5">Friday</option>
            <option value="6">Saturday</option>
          </SelectField>
          <TextField
            id="updates-hour"
            label="Hour"
            type="number"
            hint="0–23, this host's clock."
            value={hour}
            onChange={(event) => setHour(event.target.value)}
          />
          <TextField
            id="updates-minute"
            label="Minute"
            type="number"
            value={minute}
            onChange={(event) => setMinute(event.target.value)}
          />
        </div>

        <TextField
          id="updates-interval"
          label="Check every"
          type="number"
          hint="Hours. Checking refreshes the package index, so this is hours rather than minutes."
          value={interval}
          onChange={(event) => setInterval(event.target.value)}
        />

        <TextField
          id="updates-excluded"
          label="Never apply automatically"
          hint="Package names, separated by commas. The panel's own list — it does not pin anything on the host, so these can still be updated by hand."
          placeholder="php84-fpm, mariadb"
          value={excluded}
          onChange={(event) => setExcluded(event.target.value)}
        />

        <p className="text-xs text-ink-muted">
          {settings.last_checked_at
            ? `Last checked ${formatWhen(settings.last_checked_at)}.`
            : 'Not checked yet.'}{' '}
          {settings.last_run_at && `Last applied ${formatWhen(settings.last_run_at)}.`}
        </p>

        <RequirePermission permission={Permission.UpdateManage}>
          <Button
            variant="primary"
            loading={save.isPending}
            onClick={() =>
              save.mutate({
                policy,
                day_of_week: Number(day),
                hour: Number(hour),
                minute: Number(minute),
                check_interval_hours: Number(interval),
                excluded: excluded
                  .split(',')
                  .map((entry) => entry.trim())
                  .filter(Boolean),
              })
            }
          >
            Save settings
          </Button>
        </RequirePermission>
      </CardBody>
    </Card>
  );
}

/**
 * RecentRuns is what has been applied.
 *
 * It is the phase's answer to "rollback", and the page says so: neither package
 * manager keeps what it replaced, so the exact versions are what makes a manual
 * recovery possible.
 */
function RecentRuns({ overview }: { overview: UpdateOverview }) {
  const runs = overview.recent ?? [];

  return (
    <Card label="History">
      <CardHeader
        title="History"
        description="What was applied, and exactly which versions moved."
        icon={<TintedIcon icon={<History className="h-5 w-5" />} tone="neutral" />}
      />
      <CardBody>
        {runs.length === 0 ? (
          <EmptyState
            icon={<History className="h-6 w-6" />}
            title="Nothing applied yet"
            description="Updates applied here, or by the schedule, are recorded with the versions they replaced."
          />
        ) : (
          <ul className="space-y-3">
            {runs.map((run) => (
              <li key={run.id} className="rounded border border-surface-border p-3">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <span
                    className={
                      run.status === 'failed'
                        ? 'font-medium text-danger-700'
                        : run.status === 'running'
                          ? 'font-medium text-warn-700'
                          : 'font-medium text-ok-700'
                    }
                  >
                    {run.status === 'failed'
                      ? 'Failed'
                      : run.status === 'running'
                        ? 'Running'
                        : 'Applied'}
                  </span>
                  <span className="text-ink-muted">{formatWhen(run.started_at)}</span>
                  <span className="rounded bg-surface-sunken px-1.5 py-0.5 text-xs text-ink">
                    {run.trigger}
                  </span>
                  {run.reboot_required && (
                    <span className="inline-flex items-center gap-1 text-xs text-warn-700">
                      <AlertTriangle aria-hidden="true" className="h-3 w-3" />
                      restart needed
                    </span>
                  )}
                </div>

                {run.error && <p className="mt-1 text-sm text-danger-700">{run.error}</p>}

                {run.changes.length > 0 ? (
                  <ul className="mt-2 space-y-0.5 font-mono text-xs text-ink">
                    {run.changes.map((change) => (
                      <li key={change.name}>
                        {change.name} {change.from || '—'} → {change.to || 'removed'}
                      </li>
                    ))}
                  </ul>
                ) : (
                  run.status !== 'running' && (
                    <p className="mt-1 text-xs text-ink-muted">Nothing moved.</p>
                  )
                )}
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

/** formatWhen renders a timestamp for a person. */
function formatWhen(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return 'at an unknown time';
  }
  return parsed.toLocaleString();
}
