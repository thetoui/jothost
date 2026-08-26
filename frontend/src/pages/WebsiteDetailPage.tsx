import { useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ArrowLeft } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
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

/** WebsiteDetailPage shows one site, its domains, and its recent work. */
export function WebsiteDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const { data: site, isLoading, isError, error } = useWebsite(id);
  const { data: jobList } = useWebsiteJobs(id);
  const deleteWebsite = useDeleteWebsite();

  if (isLoading) {
    return <p className="text-sm text-slate-500">Loading website…</p>;
  }

  if (isError || !site) {
    return (
      <div className="space-y-4">
        <BackLink />
        <p role="alert" className="text-sm text-rose-600">
          {error instanceof Error ? error.message : 'This website could not be loaded.'}
        </p>
      </div>
    );
  }

  const pill = websiteStatusPill(site.status);
  const jobs = jobList?.jobs ?? [];

  function handleDelete() {
    if (!site) {
      return;
    }
    // Deleting a site removes its files and its system account. That cannot be
    // undone from the panel, so it is confirmed before it is queued.
    const confirmed = window.confirm(
      `Delete ${site.primary_domain}? Its files, system account, and web server ` +
        `configuration are removed from the host. This cannot be undone.`,
    );
    if (!confirmed) {
      return;
    }
    deleteWebsite.mutate(site.id, {
      onSuccess: () => navigate('/websites'),
    });
  }

  return (
    <div className="space-y-6">
      <BackLink />

      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold text-slate-900">{site.primary_domain}</h1>
            <StatusPill label={pill.label} tone={pill.tone} />
          </div>
          {site.name && <p className="mt-1 text-sm text-slate-500">{site.name}</p>}
        </div>

        <RequirePermission permission={Permission.WebsiteDelete}>
          <button
            type="button"
            onClick={handleDelete}
            disabled={deleteWebsite.isPending || site.status === 'deleting'}
            className="rounded-md border border-rose-200 px-3 py-2 text-sm font-medium text-rose-700 hover:bg-rose-50 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {site.status === 'deleting' ? 'Deleting…' : 'Delete website'}
          </button>
        </RequirePermission>
      </header>

      {site.status === 'failed' && (
        <p role="alert" className="rounded-md bg-rose-50 px-4 py-3 text-sm text-rose-700">
          The last operation on this website did not finish. Check the activity below for
          what went wrong.
        </p>
      )}

      <section
        aria-label="Details"
        className="rounded-lg border border-surface-border bg-surface p-4 shadow-sm"
      >
        <dl className="grid gap-4 sm:grid-cols-2">
          <Detail label="Document root" value={site.document_root} mono />
          <Detail label="System user" value={site.system_user} mono />
          <Detail label="HTTPS" value={site.ssl_enabled ? 'Enabled' : 'Not configured'} />
          <Detail label="PHP" value={site.php_version ?? 'Static site'} />
        </dl>
      </section>

      <DomainSection websiteId={site.id} />

      <section
        aria-label="Activity"
        className="rounded-lg border border-surface-border bg-surface shadow-sm"
      >
        <h2 className="border-b border-surface-border px-4 py-3 text-sm font-semibold text-slate-900">
          Activity
        </h2>
        {jobs.length === 0 ? (
          <p className="px-4 py-4 text-sm text-slate-500">Nothing has run for this site yet.</p>
        ) : (
          <ul className="divide-y divide-surface-border">
            {jobs.map((job) => {
              const jobPill = jobStatusPill(job.status);
              return (
                <li key={job.id} className="px-4 py-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="text-sm font-medium text-slate-900">
                      {jobLabel(job.type)}
                    </span>
                    <StatusPill label={jobPill.label} tone={jobPill.tone} />
                  </div>
                  {job.message && <p className="mt-1 text-xs text-slate-500">{job.message}</p>}
                  {job.error && (
                    <p className="mt-1 text-xs text-rose-600">{job.error}</p>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}

function BackLink() {
  return (
    <Link
      to="/websites"
      className="inline-flex items-center gap-1 text-sm font-medium text-slate-600 hover:text-slate-900"
    >
      <ArrowLeft aria-hidden="true" className="h-4 w-4" />
      All websites
    </Link>
  );
}

function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className={`mt-1 text-sm text-slate-900 ${mono ? 'font-mono text-xs' : ''}`}>
        {value}
      </dd>
    </div>
  );
}

/** DomainSection lists a site's hostnames and attaches new ones. */
function DomainSection({ websiteId }: { websiteId: string }) {
  const { data: site } = useWebsite(websiteId);
  const addDomain = useAddDomain();
  const removeDomain = useRemoveDomain(websiteId);

  const [value, setValue] = useState('');
  const [validationError, setValidationError] = useState<string | null>(null);

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
  const error = validationError ?? serverError;

  return (
    <section
      aria-label="Domains"
      className="rounded-lg border border-surface-border bg-surface shadow-sm"
    >
      <h2 className="border-b border-surface-border px-4 py-3 text-sm font-semibold text-slate-900">
        Domains
      </h2>

      <ul className="divide-y divide-surface-border">
        {domains.map((domain) => (
          <li key={domain.id} className="flex items-center justify-between gap-2 px-4 py-3">
            <div>
              <span className="text-sm text-slate-900">{domain.domain}</span>
              <span className="ml-2 text-xs text-slate-500">{domain.type}</span>
              {domain.redirect_to && (
                <span className="ml-2 text-xs text-slate-500">→ {domain.redirect_to}</span>
              )}
            </div>
            {/* The primary domain is the site's identity; removing it would
                leave a vhost with no server_name, so it offers no control. */}
            {domain.type !== 'primary' && (
              <RequirePermission permission={Permission.WebsiteUpdate}>
                <button
                  type="button"
                  onClick={() => removeDomain.mutate(domain.id)}
                  disabled={removeDomain.isPending}
                  className="text-xs font-medium text-rose-700 hover:underline disabled:opacity-60"
                >
                  Remove
                </button>
              </RequirePermission>
            )}
          </li>
        ))}
      </ul>

      <RequirePermission permission={Permission.WebsiteUpdate}>
        <form onSubmit={handleSubmit} noValidate className="border-t border-surface-border p-4">
          <label htmlFor="alias-domain" className="block text-sm font-medium text-slate-700">
            Add an alias
          </label>
          <div className="mt-1 flex flex-wrap gap-2">
            <input
              id="alias-domain"
              type="text"
              autoComplete="off"
              spellCheck={false}
              placeholder="www.example.com"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? 'alias-error' : undefined}
              className="min-w-56 flex-1 rounded-md border border-surface-border px-3 py-2 text-sm shadow-sm focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500"
            />
            <button
              type="submit"
              disabled={addDomain.isPending}
              className="rounded-md bg-brand-600 px-3 py-2 text-sm font-medium text-white shadow-sm hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {addDomain.isPending ? 'Adding…' : 'Add'}
            </button>
          </div>
          {error && (
            <p id="alias-error" role="alert" className="mt-2 text-sm text-rose-600">
              {error}
            </p>
          )}
        </form>
      </RequirePermission>
    </section>
  );
}
