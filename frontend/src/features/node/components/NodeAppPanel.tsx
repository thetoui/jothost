import { useState } from 'react';
import { Download, Eye, Plus, ScrollText, Trash2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardHeader } from '@/components/ui/Card';
import { TextField } from '@/components/ui/Field';
import { SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useInstallDependencies,
  useNodeApp,
  useNodeLogs,
  useRemoveNodeEnv,
  useRevealNodeEnv,
  useSetNodeEnv,
} from '@/features/node/hooks';
import { formatUptime, managedByLabel, runtimeNote } from '@/features/node/status';
import type { NodeApp } from '@/types/api';

/**
 * NodeAppPanel is what opens under an application in the list: what it is
 * doing, what it is configured with, and what it has been saying.
 */
export function NodeAppPanel({ app }: { app: NodeApp }) {
  const detail = useNodeApp(app.id);
  const install = useInstallDependencies();

  const current = detail.data?.application ?? app;
  const note = runtimeNote(current);

  return (
    <div className="space-y-3 border-t border-surface-border bg-surface-sunken/40 px-5 py-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <dl className="flex flex-wrap items-center gap-x-6 gap-y-1 text-xs text-ink-muted">
          <Fact label="Directory" value={current.application_root} />
          <Fact label="Entry point" value={current.startup_file} />
          <Fact label="Account" value={current.system_user ?? '—'} />
          {current.runtime?.pid ? <Fact label="Process" value={String(current.runtime.pid)} /> : null}
          {current.runtime?.uptime_seconds ? (
            <Fact label="Up" value={formatUptime(current.runtime.uptime_seconds)} />
          ) : null}
          <Fact label="Run by" value={managedByLabel(current.runtime?.managed_by ?? '')} />
        </dl>

        <RequirePermission permission={Permission.WebsiteUpdate}>
          <Button
            size="sm"
            loading={install.isPending}
            onClick={() => install.mutate(current.id)}
            icon={<Download aria-hidden="true" className="h-3.5 w-3.5" />}
          >
            Install dependencies
          </Button>
        </RequirePermission>
      </div>

      {/* "Running" and "answering" are not the same thing, and a process that
          is up but not listening is the most confusing state to be in. */}
      {note && (
        <Alert tone="warning" title="This application is not answering">
          {note}
        </Alert>
      )}

      {current.last_error && (
        <Alert tone="danger" title="The last attempt failed">
          {current.last_error}
        </Alert>
      )}

      {install.isError && (
        <Alert tone="danger" title="The dependencies could not be installed">
          {install.error instanceof Error ? install.error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <div className="grid gap-3 lg:grid-cols-2">
        <EnvironmentCard app={current} />
        <LogsCard app={current} />
      </div>
    </div>
  );
}

/** EnvironmentCard shows and edits an application's configuration. */
function EnvironmentCard({ app }: { app: NodeApp }) {
  const setEnv = useSetNodeEnv();
  const removeEnv = useRemoveNodeEnv();
  const reveal = useRevealNodeEnv();

  const [adding, setAdding] = useState(false);
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const [revealed, setRevealed] = useState<Record<string, string> | null>(null);

  const keys = app.environment ?? [];

  return (
    <Card>
      <CardHeader
        title="Environment"
        description="Read by the process when it starts."
        action={
          <RequirePermission permission={Permission.WebsiteUpdate}>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setAdding((current) => !current)}
              icon={<Plus aria-hidden="true" className="h-3.5 w-3.5" />}
            >
              Add
            </Button>
          </RequirePermission>
        }
      />

      <div className="space-y-3 p-4">
        {adding && (
          <form
            className="space-y-2 rounded-md border border-surface-border bg-surface-sunken/50 p-3"
            onSubmit={(event) => {
              event.preventDefault();
              setEnv.mutate(
                { id: app.id, key: key.trim(), value },
                {
                  onSuccess: () => {
                    setKey('');
                    setValue('');
                    setAdding(false);
                  },
                },
              );
            }}
          >
            {setEnv.isError && (
              <Alert tone="danger" title="That variable was not accepted">
                {setEnv.error instanceof Error ? setEnv.error.message : 'Try again.'}
              </Alert>
            )}
            <TextField
              id={`env-key-${app.id}`}
              label="Name"
              value={key}
              onChange={(event) => setKey(event.target.value.toUpperCase())}
              placeholder="DATABASE_URL"
              autoComplete="off"
              spellCheck={false}
              hint="Upper case, digits, and underscores."
            />
            <TextField
              id={`env-value-${app.id}`}
              label="Value"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
            <div className="flex justify-end gap-2">
              <Button type="button" size="sm" onClick={() => setAdding(false)}>
                Cancel
              </Button>
              <Button type="submit" size="sm" variant="primary" loading={setEnv.isPending}>
                Save
              </Button>
            </div>
          </form>
        )}

        {keys.length === 0 ? (
          <p className="text-sm text-ink-muted">
            Nothing set. The panel always provides <code className="font-mono">PORT</code>,{' '}
            <code className="font-mono">HOME</code>, and{' '}
            <code className="font-mono">NODE_ENV</code>.
          </p>
        ) : (
          <ul className="divide-y divide-surface-border text-sm">
            {keys.map((name) => (
              <li key={name} className="flex items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <p className="font-mono text-ink-strong">{name}</p>
                  {revealed?.[name] !== undefined && (
                    <code className="mt-0.5 block select-all break-all rounded bg-surface-sunken px-1.5 py-0.5 font-mono text-xs text-ink">
                      {revealed[name] || <span className="text-ink-dim">(empty)</span>}
                    </code>
                  )}
                </div>
                <RequirePermission permission={Permission.WebsiteUpdate}>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => removeEnv.mutate({ id: app.id, key: name })}
                    aria-label={`Remove ${name}`}
                    icon={<Trash2 aria-hidden="true" className="h-3.5 w-3.5" />}
                  >
                    Remove
                  </Button>
                </RequirePermission>
              </li>
            ))}
          </ul>
        )}

        {keys.length > 0 && revealed === null && (
          // Values are hidden until asked for: they are the application's
          // credentials, and reading them writes an audit record.
          <RequirePermission permission={Permission.ServerManage}>
            <Button
              size="sm"
              variant="ghost"
              loading={reveal.isPending}
              onClick={() =>
                reveal.mutate(app.id, { onSuccess: (result) => setRevealed(result.environment) })
              }
              icon={<Eye aria-hidden="true" className="h-3.5 w-3.5" />}
            >
              Show values
            </Button>
          </RequirePermission>
        )}

        <p className="text-xs text-ink-muted">
          A change takes effect the next time the application is restarted — a process reads its
          environment once, at startup.
        </p>
      </div>
    </Card>
  );
}

/** LogsCard shows the tail of an application's output. */
function LogsCard({ app }: { app: NodeApp }) {
  const [open, setOpen] = useState(false);
  const logs = useNodeLogs(open ? app.id : undefined);

  return (
    <Card>
      <CardHeader
        title="Output"
        description="What the application has written."
        action={
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setOpen((current) => !current)}
            icon={<ScrollText aria-hidden="true" className="h-3.5 w-3.5" />}
          >
            {open ? 'Hide' : 'Show'}
          </Button>
        }
      />

      {open && (
        <div className="p-4">
          {logs.isPending ? (
            <SkeletonRows rows={3} />
          ) : logs.data?.detail ? (
            // Under systemd the output is in the journal, which this panel
            // does not read. Saying where it is beats returning nothing.
            <Alert tone="info" title="The output is in the system journal">
              {logs.data.detail}
            </Alert>
          ) : (logs.data?.lines.length ?? 0) === 0 ? (
            <p className="text-sm text-ink-muted">Nothing written yet.</p>
          ) : (
            <pre className="max-h-64 overflow-auto rounded bg-console p-3 text-xs leading-relaxed text-console">
              {logs.data?.lines.join('\n')}
            </pre>
          )}

          {(logs.data?.error_lines.length ?? 0) > 0 && (
            <>
              <p className="mt-3 text-xs font-medium text-ink">Errors</p>
              <pre className="mt-1 max-h-40 overflow-auto rounded bg-console p-3 text-xs leading-relaxed text-danger-200">
                {logs.data?.error_lines.join('\n')}
              </pre>
            </>
          )}
        </div>
      )}
    </Card>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-1.5">
      <dt>{label}</dt>
      <dd className="font-mono font-medium text-ink">{value}</dd>
    </div>
  );
}
