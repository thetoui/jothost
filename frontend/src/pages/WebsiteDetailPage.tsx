import { useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  ArrowLeft,
  ExternalLink,
  Folder,
  Globe,
  History,
  Lock,
  Plus,
  Trash2,
  UserRound,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, ProgressBar, Skeleton, SkeletonRows } from '@/components/ui/Loading';
import { TextField, Toggle } from '@/components/ui/Field';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { WebsitePHPPanel } from '@/features/php/components/WebsitePHPPanel';
import { WebsiteDNSPanel } from '@/features/dns/components/WebsiteDNSPanel';
import { WebsiteFTPPanel } from '@/features/ftp/components/WebsiteFTPPanel';
import { WebsiteSSLPanel } from '@/features/ssl/components/WebsiteSSLPanel';
import { SubdomainPanel } from '@/features/websites/components/SubdomainPanel';
import { HtaccessPanel } from '@/features/websites/components/HtaccessPanel';
import {
  isJobRunning,
  useAddDomain,
  useDeleteWebsite,
  useRemoveDomain,
  useWebsite,
  useWebsiteJobs,
} from '@/features/websites/hooks';
import {
  domainError,
  jobLabel,
  jobStatusPill,
  normalizeDomain,
  websiteStatusPill,
} from '@/features/websites/status';
import { ApiError } from '@/services/apiClient';
import type { Job } from '@/types/api';

/** WebsiteDetailPage shows one site, its domains, and its recent work. */
export function WebsiteDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const { data: site, isPending, isError, error } = useWebsite(id);
  const { data: jobList } = useWebsiteJobs(id);
  const deleteWebsite = useDeleteWebsite();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  // Off by default, and reset every time the dialog opens: a destructive
  // option that remembers its last answer is one somebody agrees to twice
  // without reading it the second time.
  const [removeFiles, setRemoveFiles] = useState(false);

  if (isPending) {
    return <DetailSkeleton />;
  }

  if (isError || !site) {
    return (
      <div className="space-y-4">
        <BackLink />
        <Alert tone="danger" title="This website could not be loaded">
          {error instanceof Error ? error.message : 'It may have been removed.'}
        </Alert>
      </div>
    );
  }

  const pill = websiteStatusPill(site.status);
  const settling = site.status === 'creating' || site.status === 'deleting';
  const jobs = jobList?.jobs ?? [];

  const deleteError =
    deleteWebsite.error instanceof ApiError
      ? deleteWebsite.error.message
      : deleteWebsite.error
        ? 'The website could not be deleted.'
        : null;

  return (
    <div className="space-y-5">
      <BackLink />

      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 items-start gap-3">
          <TintedIcon
            tone={site.status === 'failed' ? 'danger' : settling ? 'warn' : 'brand'}
            icon={<Globe className="h-4 w-4" />}
          />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="truncate text-xl font-semibold text-slate-900">
                {site.primary_domain}
              </h1>
              <StatusPill label={pill.label} tone={pill.tone} dot pulse={settling} />
            </div>
            {site.name && <p className="mt-0.5 text-sm text-slate-500">{site.name}</p>}
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <Button
            variant="secondary"
            onClick={() => window.open(`http://${site.primary_domain}`, '_blank', 'noreferrer')}
            icon={<ExternalLink aria-hidden="true" className="h-4 w-4" />}
          >
            Open site
          </Button>
          <RequirePermission permission={Permission.WebsiteDelete}>
            <Button
              variant="danger"
              onClick={() => {
                setRemoveFiles(false);
                setConfirmingDelete(true);
              }}
              disabled={site.status === 'deleting'}
              icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
            >
              {site.status === 'deleting' ? 'Deleting…' : 'Delete'}
            </Button>
          </RequirePermission>
        </div>
      </header>

      {site.status === 'failed' && (
        <Alert tone="danger" title="The last operation did not finish">
          Check the activity below for what went wrong, then retry the change.
        </Alert>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="space-y-5 lg:col-span-2">
          <Card>
            <CardHeader title="Hosting" />
            <CardBody className="grid gap-4 sm:grid-cols-2">
              <Detail
                label="Document root"
                value={site.document_root}
                mono
                icon={<Folder className="h-3.5 w-3.5" />}
              />
              <Detail
                label="System user"
                value={site.system_user}
                mono
                icon={<UserRound className="h-3.5 w-3.5" />}
              />
              <Detail
                label="HTTPS"
                value={
                  site.ssl_enabled
                    ? site.https_redirect
                      ? 'Enabled, HTTP redirected'
                      : 'Enabled'
                    : 'Not configured'
                }
                icon={<Lock className="h-3.5 w-3.5" />}
              />
              <Detail
                label="Created"
                value={new Date(site.created_at).toLocaleDateString()}
                icon={<History className="h-3.5 w-3.5" />}
              />
            </CardBody>
          </Card>

          <HtaccessPanel site={site} />
          <WebsiteSSLPanel websiteId={site.id} domain={site.primary_domain} />
          <WebsitePHPPanel websiteId={site.id} />
          <WebsiteFTPPanel websiteId={site.id} domain={site.primary_domain} />
          <WebsiteDNSPanel websiteId={site.id} domain={site.primary_domain} />
          {/* A subdomain is a site of its own, so it belongs on the parent's
              page as a list of sites rather than as another kind of name. A
              subdomain has none of its own: one level is the whole model. */}
          {!site.parent_website_id && <SubdomainPanel site={site} />}
          <DomainSection websiteId={site.id} />
        </div>

        <ActivityCard jobs={jobs} />
      </div>

      <ConfirmDialog
        open={confirmingDelete}
        onClose={() => setConfirmingDelete(false)}
        onConfirm={() =>
          deleteWebsite.mutate(
            { id: site.id, removeFiles },
            {
              onSuccess: () => {
                setConfirmingDelete(false);
                navigate('/websites');
              },
            },
          )
        }
        title="Delete this website?"
        description="This cannot be undone from the panel."
        confirmLabel={removeFiles ? 'Delete website and files' : 'Delete website'}
        destructive
        loading={deleteWebsite.isPending}
        error={deleteError}
      >
        <p className="mb-2">
          <span className="font-medium text-slate-900">{site.primary_domain}</span> is removed from
          the host:
        </p>
        <ul className="ml-4 list-disc space-y-1 text-slate-600">
          <li>
            its system account <span className="font-mono text-xs">{site.system_user}</span>
          </li>
          <li>its web server configuration and any PHP pool</li>
          <li>its certificate, scheduled jobs and FTP accounts</li>
        </ul>

        <div className="mt-4 rounded-md border border-surface-border bg-surface-muted p-3">
          <Toggle
            id="delete-remove-files"
            label="Also delete the website's files"
            description={`Everything under ${site.document_root}. Leave this off to keep the files.`}
            checked={removeFiles}
            onChange={setRemoveFiles}
            disabled={deleteWebsite.isPending}
          />
          {/* Both answers have a consequence, so both are stated. The panel used
              to say the files were deleted and then keep them, which is the one
              option that was never on offer. */}
          <p className="mt-2.5 text-xs text-slate-500">
            {removeFiles
              ? 'The content is deleted permanently. Take a backup first if you may want it.'
              : 'The files are kept and reassigned to root. The domain cannot be created again until the directory is cleared.'}
          </p>
        </div>
      </ConfirmDialog>
    </div>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-5">
      <Skeleton className="h-4 w-28" />
      <div className="flex items-center gap-3">
        <Skeleton className="h-8 w-8 rounded-md" />
        <Skeleton className="h-6 w-56" />
      </div>
      <div className="grid gap-5 lg:grid-cols-3">
        <div className="space-y-5 lg:col-span-2">
          <Card>
            <CardHeader title={<Skeleton className="h-3.5 w-20" />} />
            <CardBody className="grid gap-4 sm:grid-cols-2">
              {Array.from({ length: 4 }, (_, index) => (
                <div key={index} className="space-y-2">
                  <Skeleton className="h-2.5 w-24" />
                  <Skeleton className="h-3.5 w-40" />
                </div>
              ))}
            </CardBody>
          </Card>
          <Card>
            <SkeletonRows rows={2} />
          </Card>
        </div>
        <Card>
          <SkeletonRows rows={3} />
        </Card>
      </div>
    </div>
  );
}

function BackLink() {
  return (
    <Link
      to="/websites"
      className={`inline-flex items-center gap-1.5 rounded-sm text-sm font-medium text-slate-500 hover:text-slate-900 ${focusRingTight}`}
    >
      <ArrowLeft aria-hidden="true" className="h-4 w-4" />
      All websites
    </Link>
  );
}

interface DetailProps {
  label: string;
  value: string;
  mono?: boolean;
  icon?: React.ReactNode;
}

function Detail({ label, value, mono, icon }: DetailProps) {
  return (
    <div className="min-w-0">
      <dt className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-slate-400">
        {icon}
        {label}
      </dt>
      <dd className={`mt-1 truncate text-sm text-slate-900 ${mono ? 'font-mono text-xs' : ''}`}>
        {value}
      </dd>
    </div>
  );
}

/** ActivityCard lists the work run against this site, newest first. */
function ActivityCard({ jobs }: { jobs: Job[] }) {
  return (
    <Card className="h-fit">
      <CardHeader
        title="Activity"
        icon={<TintedIcon icon={<History className="h-4 w-4" />} />}
      />

      {jobs.length === 0 ? (
        <EmptyState
          icon={<History className="h-6 w-6" />}
          title="Nothing has run yet"
          description="Changes to this site will appear here."
        />
      ) : (
        <ul className="divide-y divide-surface-border">
          {jobs.map((job) => {
            const pill = jobStatusPill(job.status);
            const running = isJobRunning(job);

            return (
              <li key={job.id} className="px-5 py-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-sm font-medium text-slate-900">{jobLabel(job.type)}</span>
                  <StatusPill label={pill.label} tone={pill.tone} dot pulse={running} />
                </div>

                {running && (
                  <ProgressBar
                    // Progress is only meaningful once the host has reported
                    // some; before that an indeterminate bar is the honest
                    // shape, rather than a bar pinned at zero.
                    {...(job.progress > 0 ? { value: job.progress } : {})}
                    label={`${jobLabel(job.type)} progress`}
                    className="mt-2"
                  />
                )}

                {job.message && <p className="mt-1.5 text-xs text-slate-500">{job.message}</p>}
                {job.error && <p className="mt-1.5 text-xs text-danger-600">{job.error}</p>}
                <p className="mt-1 text-xs text-slate-400">
                  {new Date(job.created_at).toLocaleString()}
                </p>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

/** DomainSection lists a site's hostnames and attaches new ones. */
function DomainSection({ websiteId }: { websiteId: string }) {
  const { data: site } = useWebsite(websiteId);
  const addDomain = useAddDomain();
  const removeDomain = useRemoveDomain(websiteId);

  const [value, setValue] = useState('');
  const [validationError, setValidationError] = useState<string | null>(null);
  const [removing, setRemoving] = useState<{ id: string; domain: string } | null>(null);

  const domains = site?.domains ?? [];

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const problem = domainError(value);
    if (problem) {
      setValidationError(problem);
      return;
    }
    setValidationError(null);

    addDomain.mutate(
      { websiteId, domain: normalizeDomain(value), type: 'alias' },
      { onSuccess: () => setValue('') },
    );
  }

  const serverError =
    addDomain.error instanceof ApiError
      ? addDomain.error.message
      : addDomain.error
        ? 'The domain could not be added.'
        : null;

  return (
    <Card>
      <CardHeader
        title="Domains"
        description="Every name this site answers to."
        icon={<TintedIcon icon={<Globe className="h-4 w-4" />} />}
      />

      <ul className="divide-y divide-surface-border">
        {domains.map((domain) => (
          <li key={domain.id} className="flex items-center justify-between gap-3 px-5 py-3">
            <div className="flex min-w-0 items-center gap-2.5">
              <span className="truncate text-sm text-slate-900">{domain.domain}</span>
              <StatusPill
                label={domain.type}
                tone={domain.type === 'primary' ? 'info' : 'neutral'}
              />
              {domain.redirect_to && (
                <span className="truncate text-xs text-slate-500">→ {domain.redirect_to}</span>
              )}
            </div>

            {/* The primary domain is the site's identity and its vhost's
                server_name, so it offers no removal control at all. */}
            {domain.type !== 'primary' && (
              <RequirePermission permission={Permission.WebsiteUpdate}>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setRemoving({ id: domain.id, domain: domain.domain })}
                  className="text-danger-600 hover:bg-danger-50 hover:text-danger-700"
                >
                  Remove
                </Button>
              </RequirePermission>
            )}
          </li>
        ))}
      </ul>

      <RequirePermission permission={Permission.WebsiteUpdate}>
        <form onSubmit={handleSubmit} noValidate className="border-t border-surface-border p-5">
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-56 flex-1">
              <TextField
                id="alias-domain"
                label="Add an alias"
                type="text"
                autoComplete="off"
                spellCheck={false}
                placeholder="www.example.com"
                value={value}
                onChange={(event) => setValue(event.target.value)}
                error={validationError}
              />
            </div>
            <Button
              type="submit"
              variant="secondary"
              loading={addDomain.isPending}
              className={validationError ? 'mb-5' : undefined}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add
            </Button>
          </div>

          {serverError && (
            <Alert tone="danger" className="mt-3">
              {serverError}
            </Alert>
          )}
        </form>
      </RequirePermission>

      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        onConfirm={() => {
          if (removing) {
            removeDomain.mutate(removing.id, { onSuccess: () => setRemoving(null) });
          }
        }}
        title="Remove this domain?"
        description="The site stops answering to it once the web server reloads."
        confirmLabel="Remove domain"
        destructive
        loading={removeDomain.isPending}
      >
        <p>
          <span className="font-mono text-xs text-slate-900">{removing?.domain}</span> is detached
          from this website. Its content is not affected.
        </p>
      </ConfirmDialog>
    </Card>
  );
}
