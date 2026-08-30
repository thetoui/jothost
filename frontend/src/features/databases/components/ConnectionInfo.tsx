import { PasswordReveal } from '@/features/databases/components/PasswordReveal';
import { engineLabel } from '@/features/databases/status';
import type { Database, DatabaseUser } from '@/types/api';

interface ConnectionInfoProps {
  database: Database;
  users: DatabaseUser[];
}

/** The port each engine listens on by default. */
const defaultPorts: Record<string, string> = {
  mysql: '3306',
  mariadb: '3306',
  postgres: '5432',
};

/**
 * ConnectionInfo is everything an application needs to reach a database.
 *
 * The password is not shown with the rest. It sits behind the same deliberate,
 * audited read as everywhere else in the panel — a dialog that displayed it
 * alongside the host and port would make "who read this credential" a question
 * nobody could answer, just because somebody opened a panel.
 */
export function ConnectionInfo({ database, users }: ConnectionInfoProps) {
  const port = defaultPorts[database.engine] ?? '';
  const user = users[0];

  return (
    <div className="space-y-4">
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <Row label="Host" value="localhost" mono />
        <Row label="Port" value={port} mono />
        <Row label="Engine" value={engineLabel(database.engine)} />
        <Row label="Database" value={database.name} mono />
      </dl>

      <div className="rounded-md border border-surface-border bg-surface-sunken/50 p-3">
        {user ? (
          <>
            <p className="mb-2 text-sm font-medium text-slate-800">
              {user.username}
              {user.host && <span className="text-slate-400">@{user.host}</span>}
            </p>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="text-xs text-slate-500">Password</span>
              <PasswordReveal userId={user.id} />
            </div>
            {users.length > 1 && (
              <p className="mt-2 text-xs text-slate-500">
                {users.length - 1} other{' '}
                {users.length === 2 ? 'account has' : 'accounts have'} access; their passwords are
                on the Users tab.
              </p>
            )}
          </>
        ) : (
          <p className="text-sm text-slate-500">
            No account can reach this database yet, so nothing can connect to it. Add one from the
            Users tab.
          </p>
        )}
      </div>

      <p className="text-xs text-slate-500">
        {database.engine === 'postgres'
          ? 'PostgreSQL accepts connections over the local socket and, where the server is configured for it, over the port above.'
          : 'The account is limited to connecting from this server unless it was created to accept any host.'}
      </p>
    </div>
  );
}

function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-slate-500">{label}</dt>
      <dd className={['truncate text-slate-800', mono ? 'font-mono text-xs' : ''].join(' ')}>
        {value || '—'}
      </dd>
    </div>
  );
}
