import { Fragment, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  ChevronDown,
  ChevronRight,
  Database as DatabaseIcon,
  Globe,
  KeyRound,
  Plus,
  RefreshCw,
  Trash2,
  UserPlus,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardHeader } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { SelectField, TextField } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { CreateDatabaseForm } from '@/features/databases/components/CreateDatabaseForm';
import { PasswordReveal } from '@/features/databases/components/PasswordReveal';
import {
  useAddDatabaseUser,
  useDatabase,
  useDatabaseEngines,
  useDatabases,
  useDeleteDatabase,
  useDeleteDatabaseUser,
  useRefreshDatabaseSize,
  useSetDatabaseGrant,
  useSetDatabasePassword,
} from '@/features/databases/hooks';
import {
  databaseStatusPill,
  engineLabel,
  engineVersion,
  formatBytes,
  privilegeDescriptions,
  privilegeLabels,
  privilegeTone,
} from '@/features/databases/status';
import type { Database, DatabaseCreated, DatabasePrivilege, DatabaseUser } from '@/types/api';

/**
 * DatabasesPage lists the databases on this server and what may reach them.
 *
 * It follows the same shape as Websites & Domains: a list whose rows expand in
 * place into the thing you came to do, rather than a page per database. A
 * database has few enough properties that a whole route for one would be mostly
 * empty space.
 */
export function DatabasesPage() {
  const [creating, setCreating] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Database | null>(null);
  // Held after creation so the generated password can be shown once, in the
  // only moment anyone can copy it without a deliberate audited read.
  const [justCreated, setJustCreated] = useState<DatabaseCreated | null>(null);

  const { data, isPending, isError, error } = useDatabases();
  const engines = useDatabaseEngines();
  const remove = useDeleteDatabase();

  const databases = data?.databases ?? [];
  const unavailable = engines.data && !engines.data.available;

  const onCreated = (result: DatabaseCreated) => {
    setCreating(false);
    setJustCreated(result);
    setExpanded(result.database.id);
  };

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-slate-900">Databases</h1>
          <p className="mt-1 text-sm text-slate-500">
            {databases.length} {databases.length === 1 ? 'database' : 'databases'}
            {data ? `, ${formatBytes(data.total_size_bytes)} in total` : ''}. Each has its own
            accounts and grants.
          </p>
        </div>
        <RequirePermission permission={Permission.DatabaseManage}>
          <Button
            variant="primary"
            onClick={() => setCreating(true)}
            disabled={unavailable}
            icon={<Plus aria-hidden="true" className="h-4 w-4" />}
          >
            Add Database
          </Button>
        </RequirePermission>
      </header>

      {isError && (
        <Alert tone="danger" title="The database list could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {/* A host with no database server is a configuration, not a fault. It is
          said plainly rather than left for someone to discover by pressing a
          button that always fails. */}
      {unavailable && (
        <Alert tone="warning" title="This server runs no database engine">
          {engines.data?.engines.find((item) => item.detail)?.detail ??
            'Install MariaDB, MySQL, or PostgreSQL on the host and restart the agent.'}
        </Alert>
      )}

      {justCreated?.password && (
        <Alert tone="success" title={`${justCreated.database.name} is ready`}>
          <p className="mb-2">
            This password is shown once. The server stores only a hash of it, so copy it now —
            afterwards it can still be read from the account below, but every read is recorded.
          </p>
          {justCreated.user && (
            <PasswordReveal userId={justCreated.user.id} initial={justCreated.password} />
          )}
        </Alert>
      )}

      <EngineSummary />

      <Card label="Databases">
        {isPending ? (
          <SkeletonRows rows={4} />
        ) : databases.length === 0 ? (
          <EmptyState
            icon={<DatabaseIcon className="h-6 w-6" />}
            title="No databases yet"
            description="Add one to give a website somewhere to store its data."
            action={
              <RequirePermission permission={Permission.DatabaseManage}>
                <Button
                  variant="primary"
                  onClick={() => setCreating(true)}
                  disabled={unavailable}
                  icon={<Plus aria-hidden="true" className="h-4 w-4" />}
                >
                  Add Database
                </Button>
              </RequirePermission>
            }
          />
        ) : (
          <DatabaseTable
            databases={databases}
            expanded={expanded}
            onToggle={(id) => setExpanded((current) => (current === id ? null : id))}
            onDelete={setConfirmDelete}
          />
        )}
      </Card>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="Add Database"
        description="The database is created on the host immediately, along with an account for it."
      >
        <CreateDatabaseForm onCreated={onCreated} onCancel={() => setCreating(false)} />
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
        title={`Delete ${confirmDelete?.name ?? 'this database'}?`}
        description="The database and everything in it is dropped on the server. This cannot be undone, and the panel has no copy."
        confirmLabel="Delete database"
        destructive
        loading={remove.isPending}
        error={remove.isError ? String((remove.error as Error).message) : null}
      >
        {confirmDelete && confirmDelete.user_count > 0 && (
          <p className="text-sm text-slate-600">
            {confirmDelete.user_count}{' '}
            {confirmDelete.user_count === 1 ? 'account has' : 'accounts have'} access to it. Their
            grants go with it; the accounts themselves stay.
          </p>
        )}
      </ConfirmDialog>
    </div>
  );
}

/** EngineSummary names what the host actually runs. */
function EngineSummary() {
  const engines = useDatabaseEngines();
  const list = engines.data?.engines ?? [];

  if (list.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-wrap gap-2">
      {list.map((engine) => (
        <span
          key={engine.engine}
          title={engine.detail}
          className={[
            'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs',
            engine.available
              ? 'border-surface-border bg-surface text-slate-700'
              : 'border-dashed border-surface-border bg-surface-sunken text-slate-400',
          ].join(' ')}
        >
          <DatabaseIcon aria-hidden="true" className="h-3.5 w-3.5" />
          <span className="font-medium">{engineLabel(engine.engine)}</span>
          <span>{engine.available ? engineVersion(engine.version) : 'unavailable'}</span>
        </span>
      ))}
    </div>
  );
}

interface DatabaseTableProps {
  databases: Database[];
  expanded: string | null;
  onToggle: (id: string) => void;
  onDelete: (database: Database) => void;
}

function DatabaseTable({ databases, expanded, onToggle, onDelete }: DatabaseTableProps) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[46rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-slate-500">
            <th scope="col" className="w-8 px-2 py-2">
              <span className="sr-only">Expand</span>
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Name
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Engine
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Website
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Users
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Size
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
          {databases.map((database) => {
            const open = expanded === database.id;
            const pill = databaseStatusPill(database.status);

            return (
              <Fragment key={database.id}>
                <tr
                  className={[
                    'border-b border-surface-border/60 transition-colors',
                    open ? 'bg-brand-50/40' : 'hover:bg-surface-sunken',
                  ].join(' ')}
                >
                  <td className="px-2 py-2 align-middle">
                    <button
                      type="button"
                      onClick={() => onToggle(database.id)}
                      aria-expanded={open}
                      aria-label={`${open ? 'Collapse' : 'Expand'} ${database.name}`}
                      className="rounded p-1 text-slate-400 transition-colors hover:bg-surface-border hover:text-slate-700"
                    >
                      {open ? (
                        <ChevronDown aria-hidden="true" className="h-4 w-4" />
                      ) : (
                        <ChevronRight aria-hidden="true" className="h-4 w-4" />
                      )}
                    </button>
                  </td>
                  <td className="px-3 py-2">
                    <button
                      type="button"
                      onClick={() => onToggle(database.id)}
                      className="font-mono font-medium text-slate-800 hover:text-brand-700 hover:underline"
                    >
                      {database.name}
                    </button>
                  </td>
                  <td className="px-3 py-2 text-slate-600">{engineLabel(database.engine)}</td>
                  <td className="px-3 py-2">
                    {database.website_id && database.website_domain ? (
                      <Link
                        to={`/websites/${database.website_id}`}
                        className="inline-flex items-center gap-1 text-brand-700 hover:underline"
                      >
                        <Globe aria-hidden="true" className="h-3.5 w-3.5" />
                        {database.website_domain}
                      </Link>
                    ) : (
                      <span className="text-slate-400">—</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-slate-600">{database.user_count}</td>
                  <td className="px-3 py-2 text-slate-600">{formatBytes(database.size_bytes)}</td>
                  <td className="px-3 py-2">
                    <StatusPill
                      label={pill.label}
                      tone={pill.tone}
                      dot
                      pulse={database.status === 'creating' || database.status === 'deleting'}
                    />
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex items-center justify-end">
                      <RequirePermission permission={Permission.DatabaseManage}>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => onDelete(database)}
                          aria-label={`Delete ${database.name}`}
                          icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                        >
                          Delete
                        </Button>
                      </RequirePermission>
                    </div>
                  </td>
                </tr>

                {open && (
                  <tr>
                    <td colSpan={8} className="p-0">
                      <DatabasePanel database={database} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** DatabasePanel is the accounts and grants that open under a database. */
function DatabasePanel({ database }: { database: Database }) {
  const detail = useDatabase(database.id);
  const refresh = useRefreshDatabaseSize();
  const [adding, setAdding] = useState(false);

  const users = detail.data?.users ?? [];

  return (
    <div className="space-y-3 border-t border-surface-border bg-surface-sunken/40 px-4 py-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <dl className="flex flex-wrap items-center gap-x-6 gap-y-1 text-xs text-slate-500">
          <Fact label="Character set" value={database.charset ?? 'server default'} />
          <Fact label="Collation" value={database.collation ?? 'server default'} />
          <Fact
            label="Size"
            value={`${formatBytes(database.size_bytes)}${
              database.size_checked_at
                ? ` · measured ${new Date(database.size_checked_at).toLocaleString()}`
                : ''
            }`}
          />
        </dl>
        <Button
          size="sm"
          onClick={() => refresh.mutate(database.id)}
          loading={refresh.isPending}
          icon={<RefreshCw aria-hidden="true" className="h-3.5 w-3.5" />}
        >
          Re-measure
        </Button>
      </div>

      <Card>
        <CardHeader
          title="Users"
          description="Accounts that may connect to this database, and how much they may do."
          action={
            <RequirePermission permission={Permission.DatabaseManage}>
              <Button
                size="sm"
                variant="primary"
                onClick={() => setAdding((current) => !current)}
                icon={<UserPlus aria-hidden="true" className="h-3.5 w-3.5" />}
              >
                Add user
              </Button>
            </RequirePermission>
          }
        />

        {adding && (
          <div className="border-b border-surface-border p-4">
            <AddUserForm database={database} onDone={() => setAdding(false)} />
          </div>
        )}

        {detail.isPending ? (
          <SkeletonRows rows={2} />
        ) : users.length === 0 ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            No account can reach this database yet, so nothing can use it.
          </p>
        ) : (
          <ul className="divide-y divide-surface-border">
            {users.map((user) => (
              <UserRow key={user.id} databaseId={database.id} user={user} />
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function UserRow({
  databaseId,
  user,
}: {
  databaseId: string;
  user: DatabaseUser;
}) {
  const setGrant = useSetDatabaseGrant();
  const setPassword = useSetDatabasePassword();
  const removeUser = useDeleteDatabaseUser();
  const [rotated, setRotated] = useState<string | undefined>(undefined);
  const [confirming, setConfirming] = useState(false);

  const privilege = (user.grants?.[0]?.privilege ?? 'readonly') as DatabasePrivilege;

  return (
    <li className="space-y-2 px-5 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="font-mono text-sm text-slate-800">
            {user.username}
            {user.host && <span className="text-slate-400">@{user.host}</span>}
          </p>
          <p className="mt-0.5 text-xs text-slate-500">
            Password last changed {new Date(user.password_updated_at).toLocaleString()}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <StatusPill label={privilegeLabels[privilege]} tone={privilegeTone(privilege)} />
          <RequirePermission permission={Permission.DatabaseManage}>
            <SelectField
              id={`grant-${user.id}`}
              label="Access"
              value={privilege}
              onChange={(event) =>
                setGrant.mutate({
                  databaseId,
                  userId: user.id,
                  privilege: event.target.value as DatabasePrivilege,
                })
              }
            >
              {(Object.keys(privilegeLabels) as DatabasePrivilege[]).map((level) => (
                <option key={level} value={level}>
                  {privilegeDescriptions[level]}
                </option>
              ))}
            </SelectField>
          </RequirePermission>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <PasswordReveal userId={user.id} initial={rotated} />
        <RequirePermission permission={Permission.DatabaseManage}>
          <div className="flex items-center gap-1">
            <Button
              size="sm"
              variant="ghost"
              loading={setPassword.isPending}
              onClick={() =>
                setPassword.mutate(
                  { userId: user.id },
                  { onSuccess: (result) => setRotated(result.password) },
                )
              }
              icon={<KeyRound aria-hidden="true" className="h-3.5 w-3.5" />}
            >
              New password
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setConfirming(true)}
              icon={<Trash2 aria-hidden="true" className="h-3.5 w-3.5" />}
            >
              Remove
            </Button>
          </div>
        </RequirePermission>
      </div>

      {setGrant.isError && (
        <p className="text-xs text-danger-600">
          {setGrant.error instanceof Error ? setGrant.error.message : 'The grant did not change.'}
        </p>
      )}

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() =>
          removeUser.mutate(user.id, { onSuccess: () => setConfirming(false) })
        }
        title={`Remove ${user.username}?`}
        description="The account is dropped from the database server. Anything connecting with it stops working immediately."
        confirmLabel="Remove user"
        destructive
        loading={removeUser.isPending}
        error={removeUser.isError ? String((removeUser.error as Error).message) : null}
      />
    </li>
  );
}

function AddUserForm({ database, onDone }: { database: Database; onDone: () => void }) {
  const add = useAddDatabaseUser();
  const engines = useDatabaseEngines();
  const [username, setUsername] = useState('');
  const [host, setHost] = useState('localhost');
  const [privilege, setPrivilege] = useState<DatabasePrivilege>('full');

  const engine = engines.data?.engines.find((item) => item.engine === database.engine);
  const usesHostPatterns = engine?.supports_host_patterns ?? false;

  return (
    <form
      className="space-y-3"
      onSubmit={(event) => {
        event.preventDefault();
        add.mutate(
          {
            databaseId: database.id,
            ...(username ? { username: username.trim().toLowerCase() } : {}),
            ...(usesHostPatterns ? { host } : {}),
            privilege,
          },
          { onSuccess: onDone },
        );
      }}
    >
      {add.isError && (
        <Alert tone="danger" title="The user could not be added">
          {add.error instanceof Error ? add.error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <div className="grid gap-3 sm:grid-cols-3">
        <TextField
          id={`add-user-${database.id}`}
          label="User name"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          placeholder={database.name}
          autoComplete="off"
          spellCheck={false}
          suffix="optional"
        />
        {usesHostPatterns && (
          <SelectField
            id={`add-host-${database.id}`}
            label="Connect from"
            value={host}
            onChange={(event) => setHost(event.target.value)}
          >
            <option value="localhost">This server only</option>
            <option value="%">Any host</option>
          </SelectField>
        )}
        <SelectField
          id={`add-privilege-${database.id}`}
          label="Access"
          value={privilege}
          onChange={(event) => setPrivilege(event.target.value as DatabasePrivilege)}
        >
          {(Object.keys(privilegeLabels) as DatabasePrivilege[]).map((level) => (
            <option key={level} value={level}>
              {privilegeDescriptions[level]}
            </option>
          ))}
        </SelectField>
      </div>

      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" onClick={onDone} disabled={add.isPending}>
          Cancel
        </Button>
        <Button type="submit" size="sm" variant="primary" loading={add.isPending}>
          Add user
        </Button>
      </div>
    </form>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-1.5">
      <dt>{label}</dt>
      <dd className="font-medium text-slate-700">{value}</dd>
    </div>
  );
}
