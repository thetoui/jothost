import { useState } from 'react';
import { Download, FolderKey, Lock, Plug, Settings2, Trash2, Users } from 'lucide-react';
import { Link } from 'react-router-dom';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useDeleteFTPUser,
  useDisconnectFTPSession,
  useFTPOverview,
  useFTPSessions,
  useInstallFTP,
  useSaveFTPSettings,
  useUpdateFTPUser,
} from '@/features/ftp/hooks';
import { ApiError } from '@/services/apiClient';
import type { FTPOverview, FTPSession, FTPUser } from '@/types/api';

/**
 * FtpPage shows and changes who can reach this host's files over FTP.
 *
 * The accounts here are virtual: they exist in the FTP server's own password
 * file and nowhere else, and each maps to the system account that owns its
 * website. That is what the page is really about, and why it says so — "FTP
 * access" and "a login to the server" are different things, and a panel that
 * blurred them would be handing out the second while offering the first.
 *
 * Accounts are added from a website's own FTP tab, not from here: an account
 * belongs to a site, and a form that asked which site would be asking a
 * question the site's own page already answers.
 */
export function FtpPage() {
  const { data, isPending, isError, error } = useFTPOverview();
  const install = useInstallFTP();

  const status = data;
  const sessions = useFTPSessions(Boolean(status?.running));

  const installError = install.error instanceof ApiError ? install.error.message : null;

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">FTP</h1>
        <p className="mt-1 text-sm text-slate-500">
          Who can reach this host&rsquo;s files over FTP, and who is connected now.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The FTP settings could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {installError && (
        <Alert tone="danger" title="The FTP server could not be installed">
          {installError}
        </Alert>
      )}

      {isPending ? (
        <Card>
          <SkeletonRows rows={4} />
        </Card>
      ) : !status?.available ? (
        <Card>
          <CardBody>
            <EmptyState
              icon={<FolderKey className="h-6 w-6" />}
              title="No FTP server is installed"
              description={
                status?.can_install
                  ? 'An FTP server lets people upload to a website with a client such as FileZilla, without a login to the machine itself.'
                  : status?.reason || 'This host has no package manager the panel can install with.'
              }
              action={
                status?.can_install ? (
                  <RequirePermission permission={Permission.FTPManage}>
                    <Button
                      onClick={() => install.mutate()}
                      disabled={install.isPending}
                      icon={<Download aria-hidden="true" className="h-4 w-4" />}
                    >
                      {install.isPending ? 'Installing…' : 'Install an FTP server'}
                    </Button>
                  </RequirePermission>
                ) : undefined
              }
            />
          </CardBody>
        </Card>
      ) : (
        <>
          {!status.running && (
            <Alert tone="warning" title="The FTP server is not running">
              The accounts below are configured and nobody can connect until it is
              started, which is done from the{' '}
              <Link to="/services" className="underline">
                Services page
              </Link>
              .
            </Alert>
          )}

          <FirewallWarning status={status} />

          <Conflicts status={status} />

          <Accounts users={status.users} supportsQuota={status.supports_quota} />

          <Sessions
            sessions={sessions.data?.sessions ?? []}
            running={status.running}
            loading={sessions.isPending && Boolean(status.running)}
          />

          <ServerSettings status={status} />
        </>
      )}
    </div>
  );
}

/**
 * FirewallWarning says when the ports FTP needs are not open.
 *
 * The panel reports rather than opens them. A firewall change is its own
 * deliberate act with its own protocol — a backup, a verified apply and an
 * automatic rollback — and doing one as a side effect of saving an FTP setting
 * would open ports without the operator seeing which, and without the audit
 * trail recording a firewall change.
 *
 * Worth a banner because of how this fails: the login succeeds and the first
 * directory listing hangs until the client gives up, which looks like anything
 * except a closed port.
 */
function FirewallWarning({ status }: { status: FTPOverview }) {
  if (status.firewall_open) return null;

  return (
    <Alert tone="warning" title="The firewall is blocking FTP">
      <p>
        {status.firewall_reason} Open{' '}
        <span className="font-mono">
          {status.settings.passive_from}-{status.settings.passive_to}
        </span>{' '}
        and port 21 on the{' '}
        <Link to="/firewall" className="underline">
          Firewall page
        </Link>
        .
      </p>
    </Alert>
  );
}

/**
 * Conflicts warns when another configuration file sets what the panel sets.
 *
 * Worth a banner rather than a log line: the failure it describes is silent.
 * The panel writes a correct file, the server accepts it and restarts, and the
 * setting in force is somebody else's.
 */
function Conflicts({ status }: { status: FTPOverview }) {
  const losing = (status.conflicts ?? []).filter((conflict) => !conflict.panel_wins);
  if (losing.length === 0) return null;

  return (
    <Alert tone="warning" title="Another configuration file overrides the panel">
      <p>
        {losing.map((conflict) => `${conflict.directive} (in ${conflict.file})`).join(', ')}{' '}
        {losing.length === 1 ? 'is' : 'are'} set in a file the server reads before{' '}
        {status.config_path}, so what this page shows is not what the server is using.
      </p>
    </Alert>
  );
}

function Accounts({ users, supportsQuota }: { users: FTPUser[]; supportsQuota: boolean }) {
  return (
    <Card>
      <CardHeader
        title="Accounts"
        description="Each belongs to one website and can reach only that website's files."
        icon={<TintedIcon tone="brand" icon={<Users className="h-4 w-4" />} />}
      />
      <CardBody className="divide-y divide-surface-border p-0">
        {users.length === 0 ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            No FTP accounts yet. They are added from a website&rsquo;s own FTP tab, because
            an account belongs to a site.
          </p>
        ) : (
          users.map((user) => (
            <AccountRow key={user.id} user={user} supportsQuota={supportsQuota} />
          ))
        )}
      </CardBody>
    </Card>
  );
}

function AccountRow({ user, supportsQuota }: { user: FTPUser; supportsQuota: boolean }) {
  const update = useUpdateFTPUser();
  const remove = useDeleteFTPUser();
  const [confirming, setConfirming] = useState(false);

  const message =
    update.error instanceof ApiError
      ? update.error.message
      : remove.error instanceof ApiError
        ? remove.error.message
        : null;

  return (
    <div className="px-5 py-3.5">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-slate-900">{user.username}</span>
            {user.missing_on_host ? (
              <StatusPill label="Needs a new password" tone="warn" dot />
            ) : user.suspended ? (
              <StatusPill label="Suspended" tone="warn" dot />
            ) : (
              <StatusPill label="Active" tone="ok" dot />
            )}
            {user.access_level === 'readonly' && (
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                Read-only
              </span>
            )}
          </div>

          <p className="mt-0.5 text-sm text-slate-500">
            {user.website_domain} &middot;{' '}
            <span className="font-mono text-xs">{user.home}</span>
          </p>

          {user.missing_on_host && (
            <p className="mt-0.5 text-xs text-amber-600">
              The FTP server does not have this account. Nothing stores its
              password, so setting a new one is what puts it back.
            </p>
          )}

          <p className="mt-0.5 text-xs text-slate-400">
            {/* The system account is shown because it is the answer to "what can
                this credential actually touch". */}
            uploads as {user.system_user}
            {supportsQuota && user.quota_mb > 0 && (
              <>
                {' '}
                &middot; {user.used_mb.toFixed(1)} MB of {user.quota_mb} MB used
              </>
            )}
          </p>
        </div>

        <RequirePermission permission={Permission.FTPManage}>
          <div className="flex items-center gap-3">
            <Toggle
              id={`ftp-suspend-${user.id}`}
              label="Enabled"
              checked={!user.suspended}
              disabled={update.isPending}
              onChange={(checked) =>
                update.mutate({ id: user.id, change: { suspended: !checked } })
              }
            />
            <Button
              variant="ghost"
              onClick={() => setConfirming(true)}
              icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
            >
              Delete
            </Button>
          </div>
        </RequirePermission>
      </div>

      {message && (
        <p className="mt-2 text-sm text-red-600" role="alert">
          {message}
        </p>
      )}

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          remove.mutate(user.id, { onSuccess: () => setConfirming(false) });
        }}
        title={`Delete ${user.username}?`}
        description="Anyone using this account will stop being able to connect. The files it uploaded are left where they are."
        confirmLabel="Delete"
        destructive
        loading={remove.isPending}
      />
    </div>
  );
}

function Sessions({
  sessions,
  running,
  loading,
}: {
  sessions: FTPSession[];
  running: boolean;
  loading: boolean;
}) {
  return (
    <Card>
      <CardHeader
        title="Connected now"
        description="Sessions in progress on this server."
        icon={<TintedIcon tone="brand" icon={<Plug className="h-4 w-4" />} />}
      />
      <CardBody className="divide-y divide-surface-border p-0">
        {!running ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            Nobody can be connected while the FTP server is stopped.
          </p>
        ) : loading ? (
          <SkeletonRows rows={2} />
        ) : sessions.length === 0 ? (
          <p className="px-5 py-4 text-sm text-slate-500">Nobody is connected.</p>
        ) : (
          sessions.map((session) => <SessionRow key={session.pid} session={session} />)
        )}
      </CardBody>
    </Card>
  );
}

function SessionRow({ session }: { session: FTPSession }) {
  const disconnect = useDisconnectFTPSession();
  const [confirming, setConfirming] = useState(false);

  const encrypted = session.protocol.toLowerCase() === 'ftps';
  const message = disconnect.error instanceof ApiError ? disconnect.error.message : null;

  return (
    <div className="px-5 py-3.5">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-slate-900">{session.user}</span>
            {/* Worth its own pill: it is the difference between a password that
                crossed the network encrypted and one that did not. */}
            {encrypted ? (
              <StatusPill label="Encrypted" tone="ok" dot />
            ) : (
              <StatusPill label="Not encrypted" tone="warn" dot />
            )}
          </div>
          <p className="mt-0.5 text-sm text-slate-500">
            {session.client} &middot; {session.activity || 'idle'}
          </p>
          <p className="mt-0.5 text-xs text-slate-400">
            {session.elapsed && <>connected {session.elapsed} &middot; </>}
            in <span className="font-mono">{session.location || '/'}</span>
          </p>
        </div>

        <RequirePermission permission={Permission.FTPManage}>
          <Button variant="ghost" onClick={() => setConfirming(true)}>
            Disconnect
          </Button>
        </RequirePermission>
      </div>

      {message && (
        <p className="mt-2 text-sm text-red-600" role="alert">
          {message}
        </p>
      )}

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          disconnect.mutate(session.pid, { onSuccess: () => setConfirming(false) });
        }}
        title={`Disconnect ${session.user}?`}
        description="A transfer in progress will stop part-way, and the client will usually reconnect on its own."
        confirmLabel="Disconnect"
        destructive
        loading={disconnect.isPending}
      />
    </div>
  );
}

function ServerSettings({ status }: { status: FTPOverview }) {
  const save = useSaveFTPSettings();
  const [from, setFrom] = useState(String(status.settings.passive_from));
  const [to, setTo] = useState(String(status.settings.passive_to));
  const [address, setAddress] = useState(status.settings.masquerade_address);

  const message = save.error instanceof ApiError ? save.error.message : null;

  return (
    <Card>
      <CardHeader
        title="Server settings"
        description="How clients reach this server."
        icon={<TintedIcon tone="brand" icon={<Settings2 className="h-4 w-4" />} />}
      />
      <CardBody className="space-y-4">
        <RequirePermission permission={Permission.FTPManage}>
          <>
            <div className="grid gap-3 sm:grid-cols-2">
              <TextField
                id="ftp-passive-from"
                label="Passive ports from"
                value={from}
                inputMode="numeric"
                onChange={(event) => setFrom(event.target.value)}
                hint="Every transfer in progress uses one of these ports, and the firewall opens exactly this range."
              />
              <TextField
                id="ftp-passive-to"
                label="Passive ports to"
                value={to}
                inputMode="numeric"
                onChange={(event) => setTo(event.target.value)}
              />
            </div>

            <TextField
              id="ftp-masquerade"
              label="Public address"
              value={address}
              onChange={(event) => setAddress(event.target.value)}
              hint="Leave empty unless this host is behind NAT. Without it the server hands clients an address they cannot reach, and transfers stall after a successful login."
            />

            <div className="rounded-lg border border-surface-border p-3">
              <Toggle
                id="ftp-require-tls"
                label="Require encryption"
                description={
                  status.supports_tls
                    ? 'Refuses plain FTP outright. Every client still configured for it stops working, and most report that as "login incorrect".'
                    : 'This FTP server has no TLS module, so encryption cannot be offered.'
                }
                checked={status.settings.require_tls}
                disabled={!status.supports_tls || save.isPending}
                onChange={(checked) => save.mutate({ require_tls: checked })}
              />
              {status.settings.tls_domain ? (
                <p className="mt-2 text-xs text-slate-400">
                  Presenting the certificate for {status.settings.tls_domain}.
                </p>
              ) : (
                <p className="mt-2 flex items-center gap-1.5 text-xs text-slate-400">
                  <Lock aria-hidden="true" className="h-3 w-3" />
                  No certificate is bound, so encryption is not offered yet.
                </p>
              )}
            </div>

            {message && (
              <p className="text-sm text-red-600" role="alert">
                {message}
              </p>
            )}

            <Button
              onClick={() =>
                save.mutate({
                  passive_from: Number(from),
                  passive_to: Number(to),
                  masquerade_address: address,
                })
              }
              disabled={save.isPending}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </Button>
          </>
        </RequirePermission>
      </CardBody>
    </Card>
  );
}
