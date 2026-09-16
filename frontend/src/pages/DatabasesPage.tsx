import { Fragment, useMemo, useState } from 'react';
import {
  ChevronDown,
  ChevronRight,
  Database as DatabaseIcon,
  KeyRound,
  Pencil,
  Plus,
  Search,
  Server,
  Table2,
  Trash2,
  UserPlus,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField } from '@/components/ui/Field';
import { IconButton } from '@/components/ui/IconButton';
import { TextLink } from '@/components/ui/Link';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { Tabs } from '@/components/ui/Tabs';
import { TextButton } from '@/components/ui/TextButton';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { ConsoleCard } from '@/features/databases/components/ConsoleCard';
import { useOpenConsole } from '@/features/databases/components/ConsoleLauncher';
import { CreateDatabaseForm } from '@/features/databases/components/CreateDatabaseForm';
import { DatabasePanel } from '@/features/databases/components/DatabasePanel';
import { PasswordReveal } from '@/features/databases/components/PasswordReveal';
import {
  useAssignDatabase,
  useDatabaseConsole,
  useDatabaseEngines,
  useDatabaseUsers,
  useDatabases,
  useDeleteDatabase,
  useDeleteDatabaseUser,
  useSetDatabasePassword,
} from '@/features/databases/hooks';
import {
  databaseStatusPill,
  engineLabel,
  engineVersion,
  formatBytes,
  privilegeLabels,
  privilegeTone,
} from '@/features/databases/status';
import { useWebsites } from '@/features/websites/hooks';
import type { Database, DatabaseCreated, DatabasePrivilege } from '@/types/api';

type Tab = 'databases' | 'users';

/**
 * DatabasesPage lists the databases on this server and the accounts that reach
 * them.
 *
 * The arrangement follows what a hosting operator already knows from Plesk: two
 * tabs, a list whose rows expand in place into that database's tools, and the
 * site each one belongs to shown and editable in the list itself. The value is
 * not the resemblance — it is that the shape is already in the muscle memory of
 * the people who run these machines.
 */
export function DatabasesPage() {
  const [tab, setTab] = useState<Tab>('databases');
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState('');
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

  const term = query.trim().toLowerCase();
  const visible = term
    ? databases.filter(
        (database) =>
          database.name.includes(term) ||
          (database.website_domain ?? '').toLowerCase().includes(term),
      )
    : databases;

  const onCreated = (result: DatabaseCreated) => {
    setCreating(false);
    setJustCreated(result);
    setExpanded(result.database.id);
  };

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">Databases</h1>
        <p className="mt-1 text-sm text-ink-muted">
          {databases.length} {databases.length === 1 ? 'item' : 'items'} total
          {data && data.total_size_bytes > 0 ? `, ${formatBytes(data.total_size_bytes)}` : ''}. Each
          has its own accounts and grants.
        </p>
      </header>

      <div className="flex flex-wrap items-end justify-between gap-3 border-b border-surface-border">
        <Tabs
          label="Databases sections"
          value={tab}
          onChange={setTab}
          className="border-b-0"
          items={[
            { value: 'databases', label: 'Databases' },
            { value: 'users', label: 'User Management' },
          ]}
        />

        {/* Plesk puts "Database Servers" and "Backup Manager" here. Only the
            first has a counterpart in this build, and what it would show is
            short enough to be the line itself rather than a page of its own.
            An engine this host does not run is named and greyed rather than
            omitted: leaving it out makes the panel look like it only knows
            about one, and leaves someone hunting for the other. */}
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 pb-2 text-xs">
          <Server aria-hidden="true" className="h-3.5 w-3.5 text-ink-dim" />
          {(engines.data?.engines ?? []).length === 0 ? (
            <span className="text-ink-muted">no database server</span>
          ) : (
            engines.data?.engines.map((engine, index) => (
              <Fragment key={engine.engine}>
                {index > 0 && <span className="text-ink-dim">·</span>}
                <span
                  title={engine.detail}
                  className={engine.available ? 'text-ink' : 'text-ink-dim'}
                >
                  {engineLabel(engine.engine)}{' '}
                  {engine.available ? engineVersion(engine.version) : 'unavailable'}
                </span>
              </Fragment>
            ))
          )}
        </p>
      </div>

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
            afterwards it can still be read from the account, but every read is recorded.
          </p>
          {justCreated.user && (
            <PasswordReveal userId={justCreated.user.id} initial={justCreated.password} />
          )}
        </Alert>
      )}

      {tab === 'databases' ? (
        <>
          <div className="flex flex-wrap items-center justify-between gap-2">
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

            <div className="relative">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-dim"
              />
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Find database..."
                aria-label="Find database"
                className="h-9 w-56 rounded-md border border-surface-border bg-surface pl-8 pr-3 text-sm shadow-card placeholder:text-ink-dim focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
              />
            </div>
          </div>

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
            ) : visible.length === 0 ? (
              <EmptyState
                icon={<Search className="h-6 w-6" />}
                title="No matching databases"
                description={`Nothing matches “${query.trim()}”.`}
                action={<Button onClick={() => setQuery('')}>Clear search</Button>}
              />
            ) : (
              <DatabaseTable
                databases={visible}
                expanded={expanded}
                onToggle={(id) => setExpanded((current) => (current === id ? null : id))}
                onDelete={setConfirmDelete}
              />
            )}
          </Card>

          <ConsoleCard />
        </>
      ) : (
        <UserManagement />
      )}

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
          <p className="text-sm text-ink">
            {confirmDelete.user_count}{' '}
            {confirmDelete.user_count === 1 ? 'account has' : 'accounts have'} access to it. Their
            grants go with it; the accounts themselves stay.
          </p>
        )}
      </ConfirmDialog>
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
  const consoleStatus = useDatabaseConsole();
  // Served, not merely installed: phpMyAdmin unpacked into a directory no
  // vhost points at is not something a browser can open.
  const consoleServed = Boolean(consoleStatus.data?.served && consoleStatus.data.url);
  const console_ = useOpenConsole();

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[46rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-ink-muted">
            <th scope="col" className="w-8 px-2 py-2">
              <span className="sr-only">Expand</span>
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Database
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Related to
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
                    <IconButton
                      size="sm"
                      onClick={() => onToggle(database.id)}
                      aria-expanded={open}
                      label={`${open ? 'Collapse' : 'Expand'} ${database.name}`}
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
                    <div className="flex items-center gap-2">
                      <DatabaseIcon aria-hidden="true" className="h-4 w-4 shrink-0 text-brand-500" />
                      <button
                        type="button"
                        onClick={() => onToggle(database.id)}
                        className={`truncate rounded-sm font-mono font-medium text-ink-strong underline-offset-2 hover:text-brand-700 hover:underline ${focusRingTight}`}
                      >
                        {database.name}
                      </button>
                      <span className="shrink-0 text-xs text-ink-dim">
                        {engineLabel(database.engine)}
                      </span>
                    </div>
                  </td>

                  <td className="px-3 py-2">
                    <RelatedTo database={database} />
                  </td>

                  <td className="px-3 py-2 text-ink">{formatBytes(database.size_bytes)}</td>

                  <td className="px-3 py-2">
                    <StatusPill
                      label={pill.label}
                      tone={pill.tone}
                      dot
                      pulse={database.status === 'creating' || database.status === 'deleting'}
                    />
                  </td>

                  {/* The quick actions Plesk puts at the end of a row. */}
                  <td className="px-3 py-2">
                    <div className="flex items-center justify-end gap-1">
                      {consoleServed && (
                        // A button, not a link. This was an <a> to phpMyAdmin's
                        // own address: it signed nobody in and named no
                        // database, so it dropped the operator on a login form
                        // — or, once a session existed, on whichever database
                        // that session's account happened to be looking at.
                        // The panel is meant to open *this* database as an
                        // account that can reach it, which is a POST and not a
                        // href.
                        <IconButton
                          onClick={() => console_.open(database.id)}
                          disabled={console_.isPending}
                          label={`Open ${database.name} in phpMyAdmin`}
                          icon={<Table2 aria-hidden="true" className="h-4 w-4" />}
                        />
                      )}
                      <RequirePermission permission={Permission.DatabaseManage}>
                        <IconButton
                          tone="danger"
                          onClick={() => onDelete(database)}
                          label={`Delete ${database.name}`}
                          icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                        />
                      </RequirePermission>
                    </div>
                  </td>
                </tr>

                {open && (
                  <tr>
                    <td colSpan={6} className="p-0">
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

/**
 * RelatedTo shows and changes the website a database belongs to.
 *
 * Editable in the list rather than behind a settings page, which is where Plesk
 * puts it — and it is the right place: "which site is this for" is the question
 * people actually have when looking at a list of databases named db11 and elmp.
 */
function RelatedTo({ database }: { database: Database }) {
  const [editing, setEditing] = useState(false);
  const websites = useWebsites();
  const assign = useAssignDatabase();

  const options = useMemo(() => websites.data?.websites ?? [], [websites.data]);

  if (editing) {
    return (
      <div className="flex items-end gap-1">
        <SelectField
          id={`related-${database.id}`}
          label="Related to"
          value={database.website_id ?? ''}
          onChange={(event) =>
            assign.mutate(
              { id: database.id, websiteId: event.target.value },
              { onSuccess: () => setEditing(false) },
            )
          }
        >
          <option value="">Not related to a website</option>
          {options.map((website) => (
            <option key={website.id} value={website.id}>
              {website.primary_domain}
            </option>
          ))}
        </SelectField>
        <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
          Cancel
        </Button>
      </div>
    );
  }

  return (
    <div className="flex items-center gap-1.5">
      {database.website_id && database.website_domain ? (
        <>
          <span className="text-ink">Related to</span>
          <TextLink to={`/websites/${database.website_id}`} className="truncate">
            {database.website_domain}
          </TextLink>
          <RequirePermission permission={Permission.DatabaseManage}>
            <IconButton
              size="sm"
              onClick={() => setEditing(true)}
              label={`Change the website for ${database.name}`}
              icon={<Pencil aria-hidden="true" className="h-3 w-3" />}
            />
          </RequirePermission>
        </>
      ) : (
        <RequirePermission
          permission={Permission.DatabaseManage}
          fallback={<span className="text-ink-dim">—</span>}
        >
          <TextButton onClick={() => setEditing(true)}>
            Assign this database to a website
          </TextButton>
        </RequirePermission>
      )}
    </div>
  );
}

/**
 * UserManagement is the second tab: every account on the server, and what each
 * one can reach.
 */
function UserManagement() {
  const { data, isPending } = useDatabaseUsers();
  const removeUser = useDeleteDatabaseUser();
  const setPassword = useSetDatabasePassword();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [rotated, setRotated] = useState<Record<string, string>>({});

  const users = data?.users ?? [];
  const target = users.find((user) => user.id === confirming);

  return (
    <>
      <Card label="Database users">
        {isPending ? (
          <SkeletonRows rows={4} />
        ) : users.length === 0 ? (
          <EmptyState
            icon={<UserPlus className="h-6 w-6" />}
            title="No database users yet"
            description="An account is created with each database, or added to one from its row on the Databases tab."
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[46rem] border-collapse text-sm">
              <thead>
                <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-ink-muted">
                  <th scope="col" className="px-3 py-2 font-medium">
                    Name
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    Database
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    Database server
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    Password
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>

              <tbody>
                {users.map((user) => (
                  <tr key={user.id} className="border-b border-surface-border/60">
                    <td className="px-3 py-2">
                      <span className="font-mono text-ink-strong">
                        {user.username}
                        {user.host && <span className="text-ink-dim">@{user.host}</span>}
                      </span>
                    </td>

                    <td className="px-3 py-2">
                      {user.grants && user.grants.length > 0 ? (
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                          {user.grants.map((grant) => (
                            <span
                              key={grant.database_id}
                              className="inline-flex items-center gap-1"
                            >
                              <span className="font-mono text-ink">
                                {grant.database_name}
                              </span>
                              <StatusPill
                                label={privilegeLabels[grant.privilege as DatabasePrivilege]}
                                tone={privilegeTone(grant.privilege as DatabasePrivilege)}
                              />
                            </span>
                          ))}
                        </div>
                      ) : (
                        // An account with no grant can sign in and reach
                        // nothing. Saying so is more use than an empty cell.
                        <span className="text-ink-dim">No access granted</span>
                      )}
                    </td>

                    <td className="px-3 py-2 text-ink">
                      localhost ({engineLabel(user.engine)})
                    </td>

                    <td className="px-3 py-2">
                      <PasswordReveal userId={user.id} initial={rotated[user.id]} />
                    </td>

                    <td className="px-3 py-2">
                      <RequirePermission permission={Permission.DatabaseManage}>
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            loading={setPassword.isPending}
                            onClick={() =>
                              setPassword.mutate(
                                { userId: user.id },
                                {
                                  onSuccess: (result) =>
                                    setRotated((current) => ({
                                      ...current,
                                      [user.id]: result.password,
                                    })),
                                },
                              )
                            }
                            icon={<KeyRound aria-hidden="true" className="h-3.5 w-3.5" />}
                          >
                            New password
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => setConfirming(user.id)}
                            icon={<Trash2 aria-hidden="true" className="h-3.5 w-3.5" />}
                          >
                            Remove
                          </Button>
                        </div>
                      </RequirePermission>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <ConfirmDialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          if (!confirming) {
            return;
          }
          removeUser.mutate(confirming, { onSuccess: () => setConfirming(null) });
        }}
        title={`Remove ${target?.username ?? 'this account'}?`}
        description="The account is dropped from the database server. Anything connecting with it stops working immediately."
        confirmLabel="Remove user"
        destructive
        loading={removeUser.isPending}
        error={removeUser.isError ? String((removeUser.error as Error).message) : null}
      />
    </>
  );
}
