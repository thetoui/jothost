import { useMemo, useState } from 'react';
import { Clock, FileCode2, Globe, Play, Plus, ScrollText, Terminal, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { LinkButton } from '@/components/ui/Link';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { TextButton } from '@/components/ui/TextButton';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useCreateCronJob,
  useCronJobs,
  useDeleteCronJob,
  useRunCronJob,
  useUpdateCronJob,
} from '@/features/cron/hooks';
import { useWebsites } from '@/features/websites/hooks';
import { ApiError } from '@/services/apiClient';
import type { CronJob, CronJobType, CronRunResult } from '@/types/api';

/**
 * The schedules an operator picks from, and what each means.
 *
 * Presets rather than a five-field form, because a cron expression is the part
 * people get wrong: "0 0 * * 0" is weekly and "0 0 0 * *" is never. Custom is
 * still there for anything the list does not cover, and the panel shows the
 * next run for whatever is chosen, which is how a mistake is caught before it
 * matters.
 */
const presets: { label: string; value: string }[] = [
  { label: 'Every minute', value: '* * * * *' },
  { label: 'Every 5 minutes', value: '*/5 * * * *' },
  { label: 'Every 15 minutes', value: '*/15 * * * *' },
  { label: 'Hourly, on the hour', value: '0 * * * *' },
  { label: 'Daily, at midnight', value: '0 0 * * *' },
  { label: 'Daily, at 3am', value: '0 3 * * *' },
  { label: 'Weekly, Sunday at midnight', value: '0 0 * * 0' },
  { label: 'Monthly, on the 1st', value: '0 0 1 * *' },
];

/** What each job type asks for, in the operator's words. */
const typeLabels: Record<CronJobType, { label: string; field: string; hint: string }> = {
  php: {
    label: 'PHP script',
    field: 'Script',
    hint: "Relative to the site's directory, e.g. cron.php. Runs with the site's PHP version.",
  },
  url: {
    label: 'Fetch a URL',
    field: 'URL',
    hint: 'Fetched with curl. A response that is not a success makes the run fail.',
  },
  command: {
    label: 'Command',
    field: 'Command',
    hint: "Run by the site's own account, exactly as cron would run it.",
  },
};

/**
 * CronPage lists and manages scheduled jobs.
 *
 * Every job belongs to a website and runs as that site's own account. There is
 * no field for a user, and that is the point rather than an omission: a panel
 * that could schedule work as root would be a panel where one careless job is a
 * compromised machine.
 */
export function CronPage() {
  const { data, isPending, isError, error } = useCronJobs();
  const websites = useWebsites();

  const [editing, setEditing] = useState<CronJob | null>(null);
  const [creating, setCreating] = useState(false);
  const [confirming, setConfirming] = useState<CronJob | null>(null);
  const [lastRun, setLastRun] = useState<{ job: CronJob; result: CronRunResult } | null>(null);

  const jobs = useMemo(() => data?.jobs ?? [], [data]);
  const sites = useMemo(
    () => (websites.data?.websites ?? []).filter((site) => site.status === 'active'),
    [websites.data],
  );

  const remove = useDeleteCronJob();

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Scheduled jobs</h1>
          <p className="mt-1 text-sm text-slate-500">
            Work the host runs on a schedule, as the website&rsquo;s own account.
          </p>
        </div>
        <RequirePermission permission={Permission.CronManage}>
          <Button
            onClick={() => setCreating(true)}
            disabled={sites.length === 0}
            icon={<Plus aria-hidden="true" className="h-4 w-4" />}
          >
            New job
          </Button>
        </RequirePermission>
      </header>

      {isError && (
        <Alert tone="danger" title="The scheduled jobs could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {!isPending && sites.length === 0 && (
        <Alert tone="info" title="There is nothing to schedule work for yet">
          A job belongs to a website and runs as that site&rsquo;s account. Create a website
          first.
        </Alert>
      )}

      {lastRun && (
        <Alert
          tone={lastRun.result.status === 'success' ? 'success' : 'danger'}
          title={`${lastRun.job.name} ${
            lastRun.result.status === 'success' ? 'ran' : 'failed'
          } (exit ${lastRun.result.exit_code})`}
        >
          {lastRun.result.output ? (
            <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs">
              {lastRun.result.output}
            </pre>
          ) : (
            'It printed nothing.'
          )}
        </Alert>
      )}

      {isPending ? (
        <Card>
          <SkeletonRows rows={4} />
        </Card>
      ) : jobs.length === 0 ? (
        <Card>
          <EmptyState
            icon={<Clock className="h-6 w-6" />}
            title="No scheduled jobs"
            description="Nothing runs on a schedule on this host yet."
          />
        </Card>
      ) : (
        <Card>
          <CardHeader
            title={`${jobs.length} job${jobs.length === 1 ? '' : 's'}`}
            icon={<TintedIcon tone="brand" icon={<Clock className="h-4 w-4" />} />}
          />
          <CardBody className="divide-y divide-surface-border p-0">
            {jobs.map((job) => (
              <JobRow
                key={job.id}
                job={job}
                onEdit={() => setEditing(job)}
                onDelete={() => setConfirming(job)}
                onRan={(result) => setLastRun({ job, result })}
              />
            ))}
          </CardBody>
        </Card>
      )}

      {(creating || editing) && (
        <JobDialog
          job={editing}
          websites={sites.map((site) => ({ id: site.id, domain: site.primary_domain }))}
          types={data?.types ?? ['php', 'url', 'command']}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
        />
      )}

      <ConfirmDialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          if (confirming) {
            remove.mutate(confirming.id);
          }
          setConfirming(null);
        }}
        title={`Delete ${confirming?.name ?? 'this job'}?`}
        confirmLabel="Delete job"
        destructive
        loading={remove.isPending}
      >
        <p>
          The entry is removed from the host&rsquo;s crontab and the job stops running. What
          it has already printed is discarded with it.
        </p>
      </ConfirmDialog>
    </div>
  );
}

function JobRow({
  job,
  onEdit,
  onDelete,
  onRan,
}: {
  job: CronJob;
  onEdit: () => void;
  onDelete: () => void;
  onRan: (result: CronRunResult) => void;
}) {
  const update = useUpdateCronJob();
  const run = useRunCronJob();

  const icon =
    job.job_type === 'php' ? (
      <FileCode2 className="h-4 w-4" />
    ) : job.job_type === 'url' ? (
      <Globe className="h-4 w-4" />
    ) : (
      <Terminal className="h-4 w-4" />
    );

  const outcome =
    job.last_status === 'success'
      ? { label: 'Last run ok', tone: 'ok' as const }
      : job.last_status === 'failed'
        ? { label: `Last run failed (${job.last_exit_code ?? '?'})`, tone: 'error' as const }
        : { label: 'Not run yet', tone: 'neutral' as const };

  const error =
    run.error instanceof ApiError
      ? run.error.message
      : update.error instanceof ApiError
        ? update.error.message
        : null;

  return (
    <div className="px-5 py-3.5">
      <div className="flex flex-wrap items-start gap-3">
        <span className="mt-0.5 text-slate-400">{icon}</span>

        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-slate-900">{job.name}</span>
            <StatusPill label={outcome.label} tone={outcome.tone} dot />
            {!job.enabled && (
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                Disabled
              </span>
            )}
          </div>

          <p className="mt-0.5 font-mono text-xs text-slate-600">{job.command}</p>
          <p className="mt-0.5 text-xs text-slate-400">
            {job.schedule} · {job.website_domain}
            {job.system_user && <> · runs as {job.system_user}</>}
          </p>
          <p className="mt-0.5 text-xs text-slate-400">
            {job.enabled && job.next_run_at ? (
              <>Next run {formatWhen(job.next_run_at)}</>
            ) : job.enabled ? (
              <>This schedule never fires</>
            ) : (
              <>Disabled, so it will not run</>
            )}
            {job.last_run_at && <> · last ran {formatWhen(job.last_run_at)}</>}
          </p>
        </div>

        <RequirePermission permission={Permission.CronManage}>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              disabled={run.isPending}
              onClick={() =>
                run.mutate(job.id, {
                  onSuccess: (result) => onRan(result),
                })
              }
              icon={<Play aria-hidden="true" className="h-4 w-4" />}
            >
              {run.isPending ? 'Running…' : 'Run now'}
            </Button>
            <LinkButton to={`/logs?source=cron.${job.id}`}>
              <ScrollText aria-hidden="true" className="h-4 w-4" />
              Log
            </LinkButton>
            <Button variant="secondary" onClick={onEdit}>
              Edit
            </Button>
            <Button
              variant="secondary"
              onClick={onDelete}
              icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
            >
              Delete
            </Button>
          </div>
        </RequirePermission>
      </div>

      <RequirePermission permission={Permission.CronManage}>
        <div className="mt-2">
          <Toggle
            id={`enabled-${job.id}`}
            label="Enabled"
            checked={job.enabled}
            disabled={update.isPending}
            onChange={(checked) => update.mutate({ id: job.id, input: { enabled: checked } })}
          />
        </div>
      </RequirePermission>

      {error && (
        <Alert tone="danger" title={`${job.name} could not be run`}>
          {error}
        </Alert>
      )}
    </div>
  );
}

function JobDialog({
  job,
  websites,
  types,
  onClose,
}: {
  job: CronJob | null;
  websites: { id: string; domain: string }[];
  types: CronJobType[];
  onClose: () => void;
}) {
  const create = useCreateCronJob();
  const update = useUpdateCronJob();

  const [websiteID, setWebsiteID] = useState(job?.website_id ?? websites[0]?.id ?? '');
  const [name, setName] = useState(job?.name ?? '');
  const [type, setType] = useState<CronJobType>(job?.job_type ?? 'command');
  const [schedule, setSchedule] = useState(job?.schedule ?? presets[4]?.value ?? '0 0 * * *');
  const [custom, setCustom] = useState(
    job ? !presets.some((preset) => preset.value === job.schedule) : false,
  );
  const [target, setTarget] = useState(job?.target ?? '');

  const pending = create.isPending || update.isPending;
  const failure = create.error ?? update.error;
  const message =
    failure instanceof ApiError
      ? failure.message
      : failure
        ? 'The job could not be saved.'
        : null;

  function submit() {
    if (job) {
      update.mutate(
        { id: job.id, input: { name, schedule, target } },
        { onSuccess: onClose },
      );
      return;
    }
    create.mutate(
      { website_id: websiteID, name, job_type: type, schedule, target },
      { onSuccess: onClose },
    );
  }

  const labels = typeLabels[type];

  return (
    <Modal
      open
      onClose={onClose}
      title={job ? `Edit ${job.name}` : 'New scheduled job'}
      description={
        job
          ? undefined
          : "The job runs as the website's own account, on the host, at the times you choose."
      }
      busy={pending}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending || name === '' || target === ''}>
            {pending ? 'Saving…' : job ? 'Save changes' : 'Create job'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {message && (
          <Alert tone="danger" title="The job could not be saved">
            {message}
          </Alert>
        )}

        {!job && (
          <SelectField
            id="job-website"
            label="Website"
            hint="The job runs as this site's account, in its directory."
            value={websiteID}
            onChange={(event) => setWebsiteID(event.target.value)}
          >
            {websites.map((site) => (
              <option key={site.id} value={site.id}>
                {site.domain}
              </option>
            ))}
          </SelectField>
        )}

        <TextField
          id="job-name"
          label="Name"
          placeholder="Nightly database dump"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />

        {!job && (
          <SelectField
            id="job-type"
            label="What it runs"
            value={type}
            onChange={(event) => setType(event.target.value as CronJobType)}
          >
            {types.map((value) => (
              <option key={value} value={value}>
                {typeLabels[value].label}
              </option>
            ))}
          </SelectField>
        )}

        <TextField
          id="job-target"
          label={labels.field}
          hint={labels.hint}
          value={target}
          onChange={(event) => setTarget(event.target.value)}
        />

        {custom ? (
          <TextField
            id="job-schedule"
            label="Schedule"
            hint="Five fields: minute, hour, day of month, month, day of week."
            value={schedule}
            onChange={(event) => setSchedule(event.target.value)}
            suffix={
              <TextButton
                size="xs"
                onClick={() => {
                  setCustom(false);
                  setSchedule(presets[4]?.value ?? '0 0 * * *');
                }}
              >
                Choose from the list
              </TextButton>
            }
          />
        ) : (
          <SelectField
            id="job-schedule"
            label="Schedule"
            value={schedule}
            onChange={(event) => setSchedule(event.target.value)}
            hint={
              <TextButton size="xs" onClick={() => setCustom(true)}>
                Write a cron expression instead
              </TextButton>
            }
          >
            {presets.map((preset) => (
              <option key={preset.value} value={preset.value}>
                {preset.label}
              </option>
            ))}
          </SelectField>
        )}
      </div>
    </Modal>
  );
}

/** formatWhen renders a timestamp as a short, local, readable string. */
function formatWhen(iso: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) {
    return iso;
  }

  const minutes = Math.round((at.getTime() - Date.now()) / 60_000);
  if (minutes > 0 && minutes < 60) {
    return `in ${minutes} minute${minutes === 1 ? '' : 's'}`;
  }
  if (minutes < 0 && minutes > -60) {
    const ago = Math.abs(minutes);
    return `${ago} minute${ago === 1 ? '' : 's'} ago`;
  }
  return at.toLocaleString();
}
