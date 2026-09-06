import { useState } from 'react';
import {
  ArrowLeftRight,
  CopyPlus,
  Download,
  Plug,
  RefreshCw,
  Table2,
  Upload,
  Wrench,
} from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { IconButton } from '@/components/ui/IconButton';
import { Modal } from '@/components/ui/Modal';
import { SkeletonRows } from '@/components/ui/Loading';
import { ToolGroup, ToolTile } from '@/components/ui/ToolTile';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { ConnectionInfo } from '@/features/databases/components/ConnectionInfo';
import { useOpenConsole } from '@/features/databases/components/ConsoleLauncher';
import { useDumpTransfer } from '@/features/databases/components/DumpTransfer';
import {
  useDatabase,
  useDatabaseConsole,
  useRefreshDatabaseSize,
} from '@/features/databases/hooks';
import { engineLabel, formatBytes } from '@/features/databases/status';
import type { Database } from '@/types/api';

/**
 * DatabasePanel is what opens under a database in the list.
 *
 * The arrangement is the one a Plesk operator already knows: the things you can
 * do to a database as a grid of tiles, and the facts about it as a single line
 * underneath. A tool this build does not have is shown greyed with the phase
 * that adds it rather than hidden — hiding it makes the panel look finished and
 * leaves someone hunting for it.
 */
export function DatabasePanel({ database }: { database: Database }) {
  const detail = useDatabase(database.id);
  const refresh = useRefreshDatabaseSize();
  const console_ = useDatabaseConsole();
  const consoleSession = useOpenConsole();
  const dump = useDumpTransfer(database);
  const [showingConnection, setShowingConnection] = useState(false);

  const users = detail.data?.users ?? [];
  // Served, not merely installed: phpMyAdmin unpacked into a directory that
  // no vhost points at is not something a browser can open.
  const consoleServed = Boolean(console_.data?.served && console_.data.url);

  return (
    <div className="border-t border-surface-border bg-surface-sunken/40 px-5 py-4">
      <ToolGroup title="Tools">
        <ToolTile
          icon={<Table2 className="h-4 w-4" />}
          label="phpMyAdmin"
          {...(consoleServed
            ? {
                detail: consoleSession.isPending
                  ? 'Signing in…'
                  : `Open ${database.name} signed in`,
                tone: 'blue' as const,
                onClick: () => consoleSession.open(database.id),
              }
            : { unavailable: 'Install it below to use this' })}
        />
        <ToolTile
          icon={<Plug className="h-4 w-4" />}
          label="Connection info"
          detail="Host, port, and account"
          tone="violet"
          onClick={() => setShowingConnection(true)}
        />
        {/* These two were links to the Backups page, which is a different
            thing wearing the same word: a backup is an archive of the host,
            restored whole. This is one database, as a file. */}
        <ToolTile
          icon={<Download className="h-4 w-4" />}
          label="Export dump"
          detail={dump.state.busy ? 'Working…' : `Download ${database.name}.sql`}
          tone="amber"
          onClick={() => void dump.exportDump()}
        />
        <RequirePermission permission={Permission.DatabaseManage}>
          <ToolTile
            icon={<Upload className="h-4 w-4" />}
            label="Import dump"
            detail={dump.state.busy ? 'Working…' : 'Load a .sql file into it'}
            tone="amber"
            onClick={dump.chooseFile}
          />
        </RequirePermission>
        <ToolTile
          icon={<CopyPlus className="h-4 w-4" />}
          label="Copy database"
          unavailable="This panel does not copy databases yet"
        />
        <ToolTile
          icon={<Wrench className="h-4 w-4" />}
          label="Check and repair"
          unavailable="Not part of this build"
        />
        <ToolTile
          icon={<ArrowLeftRight className="h-4 w-4" />}
          label="Move to another site"
          detail="Use the Website column"
          tone="slate"
        />
      </ToolGroup>

      {/* Off-screen, and outside the tile: a file input inside a button is
          not a control anybody can reach with a keyboard. */}
      <input
        ref={dump.input}
        type="file"
        accept=".sql,application/sql,text/plain"
        className="hidden"
        onChange={dump.onFileChosen}
      />

      {dump.state.error && (
        <Alert tone="danger" title="The dump could not be transferred" className="mt-3">
          {dump.state.error}
        </Alert>
      )}

      {dump.state.done && (
        <Alert tone="success" title="Imported" className="mt-3">
          {dump.state.done}. Reload the table list in phpMyAdmin to see it.
        </Alert>
      )}

      {consoleSession.error && (
        <Alert tone="danger" title="phpMyAdmin could not be opened" className="mt-3">
          {consoleSession.error}
        </Alert>
      )}

      {/* The facts strip Plesk puts under a database. It answers "where is this,
          who can reach it, and how big is it" without opening anything. */}
      <dl className="mt-4 flex flex-wrap items-center gap-x-6 gap-y-1 border-t border-surface-border pt-3 text-xs text-slate-500">
        <Fact label="Host" value={`localhost (${engineLabel(database.engine)})`} />
        <Fact
          label="Users"
          value={
            detail.isPending
              ? '…'
              : users.length === 0
                ? 'none'
                : users.map((user) => user.username).join(', ')
          }
        />
        <Fact label="Character set" value={database.charset ?? 'server default'} />
        <Fact label="Collation" value={database.collation ?? 'server default'} />
        <div className="flex items-center gap-1.5">
          <dt>Size</dt>
          <dd className="font-medium text-slate-700">{formatBytes(database.size_bytes)}</dd>
          <RequirePermission permission={Permission.DatabaseManage}>
            <IconButton
              size="sm"
              onClick={() => refresh.mutate(database.id)}
              disabled={refresh.isPending}
              label={`Re-measure ${database.name}`}
              icon={
                <RefreshCw
                  aria-hidden="true"
                  className={`h-3 w-3 ${refresh.isPending ? 'animate-spin' : ''}`}
                />
              }
            />
          </RequirePermission>
        </div>
      </dl>

      {detail.isPending && <SkeletonRows rows={1} />}

      <Modal
        open={showingConnection}
        onClose={() => setShowingConnection(false)}
        title={`Connect to ${database.name}`}
        description="What an application needs to reach this database."
      >
        <ConnectionInfo database={database} users={users} />
        <div className="mt-4 flex justify-end border-t border-surface-border pt-3">
          <Button onClick={() => setShowingConnection(false)}>Close</Button>
        </div>
      </Modal>
    </div>
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
