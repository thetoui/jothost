import { Fragment, useState } from 'react';
import {
  ChevronDown,
  ChevronRight,
  ExternalLink,
  Globe,
  Package,
  Play,
  Plus,
  RotateCw,
  ScrollText,
  Server,
  Square,
  Trash2,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { IconButton } from '@/components/ui/IconButton';
import { IconLink, TextLink } from '@/components/ui/Link';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { CreateAppForm } from '@/features/node/components/CreateAppForm';
import { NodeAppPanel } from '@/features/node/components/NodeAppPanel';
import {
  useDeleteNodeApp,
  useNodeApps,
  useNodeVersions,
  useRestartNodeApp,
  useStartNodeApp,
  useStopNodeApp,
} from '@/features/node/hooks';
import { appStatusPill, managedByLabel } from '@/features/node/status';
import type { NodeApp } from '@/types/api';

/**
 * NodePage lists the Node.js applications this server runs.
 *
 * Same arrangement as Websites and Databases: a list whose rows expand in place
 * into that application's tools, so working through several does not mean a
 * round trip per application.
 */
export function NodePage() {
  const [creating, setCreating] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<NodeApp | null>(null);

  const { data, isPending, isError, error } = useNodeApps();
  const versions = useNodeVersions();
  const remove = useDeleteNodeApp();

  const apps = data?.applications ?? [];
  const runtime = versions.data;
  const noRuntime = runtime && !runtime.available;

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-ink-strong">Node.js</h1>
          <p className="mt-1 text-sm text-ink-muted">
            {apps.length} {apps.length === 1 ? 'application' : 'applications'}. Each runs under its
            website&rsquo;s own system account, with nginx in front of it.
          </p>
        </div>
        <RequirePermission permission={Permission.WebsiteUpdate}>
          <Button
            variant="primary"
            onClick={() => setCreating(true)}
            disabled={Boolean(noRuntime)}
            icon={<Plus aria-hidden="true" className="h-4 w-4" />}
          >
            Add Application
          </Button>
        </RequirePermission>
      </header>

      {isError && (
        <Alert tone="danger" title="The application list could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <RuntimeSummary />

      {/* A host with no runtime is a configuration, not a fault. It is said
          plainly rather than left for someone to discover by pressing a button
          that always fails. */}
      {noRuntime && (
        <Alert tone="warning" title="No Node.js runtime is installed">
          {runtime?.can_install
            ? 'Install one from the summary above before adding an application.'
            : 'This host has no package manager the panel can install one with.'}
        </Alert>
      )}

      <Card label="Applications">
        {isPending ? (
          <SkeletonRows rows={3} />
        ) : apps.length === 0 ? (
          <EmptyState
            icon={<Package className="h-6 w-6" />}
            title="No applications yet"
            description="Add one to run a Node.js application behind one of your websites."
            action={
              <RequirePermission permission={Permission.WebsiteUpdate}>
                <Button
                  variant="primary"
                  onClick={() => setCreating(true)}
                  disabled={Boolean(noRuntime)}
                  icon={<Plus aria-hidden="true" className="h-4 w-4" />}
                >
                  Add Application
                </Button>
              </RequirePermission>
            }
          />
        ) : (
          <AppTable
            apps={apps}
            expanded={expanded}
            onToggle={(id) => setExpanded((current) => (current === id ? null : id))}
            onDelete={setConfirmDelete}
          />
        )}
      </Card>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="Add Node.js Application"
        description="The application is prepared on the host and left stopped, so you can check it before it starts serving."
      >
        <CreateAppForm onCreated={() => setCreating(false)} onCancel={() => setCreating(false)} />
      </Modal>

      <ConfirmDialog
        open={confirmDelete !== null}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => {
          if (!confirmDelete) {
            return;
          }
          remove.mutate(confirmDelete.id, { onSuccess: () => setConfirmDelete(null) });
        }}
        title={`Remove ${confirmDelete?.name ?? 'this application'}?`}
        description="The process is stopped and the website goes back to serving its files. Your code and its node_modules are left exactly where they are."
        confirmLabel="Remove application"
        destructive
        loading={remove.isPending}
        error={remove.isError ? String((remove.error as Error).message) : null}
      />
    </div>
  );
}

/** RuntimeSummary names what the host runs and how it runs it. */
function RuntimeSummary() {
  const { data } = useNodeVersions();
  if (!data) {
    return null;
  }

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-ink-muted">
      <span className="inline-flex items-center gap-1.5">
        <Server aria-hidden="true" className="h-3.5 w-3.5" />
        {data.versions.length > 0
          ? data.versions.map((version) => `Node ${version.full_version}`).join(' · ')
          : 'no Node.js runtime'}
      </span>
      {data.versions[0]?.npm_version && <span>npm {data.versions[0].npm_version}</span>}
      {/* Which mechanism runs the applications decides where the logs are, so
          the reader is told rather than left to find out from an empty page. */}
      <span>Run by {managedByLabel(data.managed_by)}</span>
    </div>
  );
}

interface AppTableProps {
  apps: NodeApp[];
  expanded: string | null;
  onToggle: (id: string) => void;
  onDelete: (app: NodeApp) => void;
}

function AppTable({ apps, expanded, onToggle, onDelete }: AppTableProps) {
  const start = useStartNodeApp();
  const stop = useStopNodeApp();
  const restart = useRestartNodeApp();

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[48rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-ink-muted">
            <th scope="col" className="w-8 px-2 py-2">
              <span className="sr-only">Expand</span>
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Application
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Website
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Node
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Port
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Status
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>

        <tbody>
          {apps.map((app) => {
            const open = expanded === app.id;
            const pill = appStatusPill(app.status);
            const running = app.status === 'running';

            return (
              <Fragment key={app.id}>
                <tr
                  className={[
                    'border-b border-surface-border/60 transition-colors',
                    open ? 'bg-brand-50/40' : 'hover:bg-surface-sunken',
                  ].join(' ')}
                >
                  <td className="px-2 py-2 align-middle">
                    <IconButton
                      size="sm"
                      onClick={() => onToggle(app.id)}
                      aria-expanded={open}
                      label={`${open ? 'Collapse' : 'Expand'} ${app.name}`}
                      icon={
                        open ? (
                          <ChevronDown aria-hidden="true" className="h-4 w-4" />
                        ) : (
                          <ChevronRight aria-hidden="true" className="h-4 w-4" />
                        )
                      }
                    />
                  </td>

                  <td className="px-3 py-2">
                    <button
                      type="button"
                      onClick={() => onToggle(app.id)}
                      className={`truncate rounded-sm font-mono font-medium text-ink-strong underline-offset-2 hover:text-brand-700 hover:underline ${focusRingTight}`}
                    >
                      {app.name}
                    </button>
                  </td>

                  <td className="px-3 py-2">
                    {app.website_domain ? (
                      <span className="inline-flex items-center gap-1.5">
                        <TextLink
                          to={`/websites/${app.website_id}`}
                          className="inline-flex items-center gap-1"
                        >
                          <Globe aria-hidden="true" className="h-3.5 w-3.5" />
                          {app.website_domain}
                        </TextLink>
                        {running && (
                          <IconLink
                            href={`http://${app.website_domain}`}
                            size="sm"
                            label={`Open ${app.website_domain} in a new tab`}
                            icon={<ExternalLink aria-hidden="true" className="h-3 w-3" />}
                          />
                        )}
                      </span>
                    ) : (
                      <span className="text-ink-dim">—</span>
                    )}
                  </td>

                  <td className="px-3 py-2 text-ink">{app.node_version}</td>
                  <td className="px-3 py-2 font-mono text-ink">{app.port}</td>

                  <td className="px-3 py-2">
                    <StatusPill
                      label={pill.label}
                      tone={pill.tone}
                      dot
                      pulse={app.status === 'starting'}
                    />
                  </td>

                  <td className="px-3 py-2">
                    <RequirePermission permission={Permission.WebsiteUpdate}>
                      <div className="flex items-center justify-end gap-1">
                        {running ? (
                          <>
                            <Button
                              size="sm"
                              variant="ghost"
                              loading={restart.isPending}
                              onClick={() => restart.mutate(app.id)}
                              icon={<RotateCw aria-hidden="true" className="h-3.5 w-3.5" />}
                            >
                              Restart
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              loading={stop.isPending}
                              onClick={() => stop.mutate(app.id)}
                              icon={<Square aria-hidden="true" className="h-3.5 w-3.5" />}
                            >
                              Stop
                            </Button>
                          </>
                        ) : (
                          <Button
                            size="sm"
                            variant="ghost"
                            loading={start.isPending}
                            onClick={() => start.mutate(app.id)}
                            icon={<Play aria-hidden="true" className="h-3.5 w-3.5" />}
                          >
                            Start
                          </Button>
                        )}
                        <IconButton
                          tone="danger"
                          onClick={() => onDelete(app)}
                          label={`Remove ${app.name}`}
                          icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                        />
                      </div>
                    </RequirePermission>
                  </td>
                </tr>

                {open && (
                  <tr>
                    <td colSpan={7} className="p-0">
                      <NodeAppPanel app={app} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>

      {(start.isError || stop.isError || restart.isError) && (
        <div className="border-t border-surface-border p-3">
          <Alert tone="danger" title="The application did not respond as expected">
            {String(
              ((start.error ?? stop.error ?? restart.error) as Error | undefined)?.message ??
                'Try again in a moment.',
            )}
          </Alert>
        </div>
      )}
    </div>
  );
}

/** Re-exported so the panel can show the same icon for logs. */
export { ScrollText as LogsIcon };
