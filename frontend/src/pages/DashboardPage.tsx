import { StatusPill } from '@/components/StatusPill';
import { useApiHealth } from '@/features/system/hooks';

/**
 * Phase 0 dashboard. It shows only what the foundation can truthfully report:
 * API reachability and version. The CPU/RAM/disk widgets described in
 * PRD.md section 7 arrive in Phase 3, once the Agent collectors exist.
 */
export function DashboardPage() {
  const { data, isPending, isError, error } = useApiHealth();

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold text-slate-900">Dashboard</h1>
        <p className="mt-1 text-sm text-slate-600">
          Foundation build. Server metrics and service widgets arrive in a later phase.
        </p>
      </div>

      <section
        aria-labelledby="api-status-heading"
        className="rounded-lg border border-surface-border bg-surface p-5"
      >
        <div className="flex items-center justify-between">
          <h2 id="api-status-heading" className="text-sm font-semibold text-slate-900">
            API status
          </h2>
          {isPending && <StatusPill label="Checking" tone="neutral" />}
          {isError && <StatusPill label="Unreachable" tone="error" />}
          {data && <StatusPill label="Online" tone="ok" />}
        </div>

        <dl className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
          <div>
            <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">Service</dt>
            <dd className="mt-1 text-sm text-slate-900">{data?.service ?? '—'}</dd>
          </div>
          <div>
            <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">Status</dt>
            <dd className="mt-1 text-sm text-slate-900">{data?.status ?? '—'}</dd>
          </div>
          <div>
            <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">Version</dt>
            <dd className="mt-1 text-sm text-slate-900">{data?.version ?? '—'}</dd>
          </div>
        </dl>

        {isError && (
          <p role="alert" className="mt-4 text-sm text-rose-700">
            {error instanceof Error ? error.message : 'The API did not respond.'}
          </p>
        )}
      </section>
    </div>
  );
}
