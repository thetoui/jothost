import { useEffect, useState } from 'react';
import { ExternalLink, ShieldAlert, Table2, Trash2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { TextField } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useDatabaseConsole,
  useInstallConsole,
  useUninstallConsole,
} from '@/features/databases/hooks';

/**
 * ConsoleCard installs and links to phpMyAdmin.
 *
 * It is off until someone turns it on, and says why in the card rather than in
 * documentation nobody reads: phpMyAdmin is among the most probed paths on the
 * public internet, so a panel that installed it by default would be shipping
 * every operator an exposure they did not ask for.
 */
export function ConsoleCard() {
  const [serverName, setServerName] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [watching, setWatching] = useState(false);

  const { data, isPending } = useDatabaseConsole(watching);
  const install = useInstallConsole();
  const uninstall = useUninstallConsole();

  // Both operations are jobs on the host. The card polls only while one is in
  // flight, and stops as soon as what it is waiting for has happened.
  useEffect(() => {
    if (!watching || !data) {
      return;
    }
    if (install.isSuccess && data.served) {
      setWatching(false);
    }
    if (uninstall.isSuccess && !data.installed) {
      setWatching(false);
    }
  }, [watching, data, install.isSuccess, uninstall.isSuccess]);

  if (isPending) {
    return null;
  }

  const served = data?.served ?? false;
  const installed = data?.installed ?? false;
  const nameError = validateServerName(serverName);

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon icon={<Table2 className="h-4 w-4" />} tone={served ? 'ok' : 'neutral'} />}
        title="phpMyAdmin"
        description={
          served
            ? 'Installed and served. Sign in with a database account.'
            : 'Browse and edit table contents in a browser. Not installed.'
        }
        action={
          served && data?.url ? (
            <a
              href={data.url}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1.5 rounded-md border border-surface-border bg-surface px-3 py-1.5 text-sm text-slate-700 shadow-card transition-colors hover:bg-surface-muted"
            >
              Open
              <ExternalLink aria-hidden="true" className="h-3.5 w-3.5" />
            </a>
          ) : undefined
        }
      />

      <div className="space-y-3 p-5">
        {served ? (
          <>
            <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
              <Fact label="Address" value={data?.server_name ?? ''} mono />
              <Fact label="Running on" value={data?.php_version ? `PHP ${data.php_version}` : '—'} />
            </dl>

            <Alert tone="warning" title="This page is a database console">
              Anyone who can reach it can try passwords against your database server. Restrict it to
              a name that is not published, or to an address range you control.
            </Alert>

            <RequirePermission permission={Permission.DatabaseManage}>
              <div className="flex justify-end">
                <Button
                  onClick={() => setConfirming(true)}
                  icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                >
                  Remove phpMyAdmin
                </Button>
              </div>
            </RequirePermission>
          </>
        ) : data?.can_install === false ? (
          <Alert tone="info" title="phpMyAdmin cannot be installed on this server">
            {data.detail ?? 'The host is missing something it needs.'}
          </Alert>
        ) : (
          <RequirePermission permission={Permission.DatabaseManage}>
            <form
              className="space-y-3"
              onSubmit={(event) => {
                event.preventDefault();
                if (nameError) {
                  return;
                }
                setWatching(true);
                install.mutate(serverName.trim().toLowerCase());
              }}
            >
              {install.isError && (
                <Alert tone="danger" title="phpMyAdmin could not be installed">
                  {install.error instanceof Error
                    ? install.error.message
                    : 'Try again in a moment.'}
                </Alert>
              )}

              {installed && !served && (
                <Alert tone="info" title="The files are there but nothing serves them">
                  Installing again writes the site configuration and publishes it.
                </Alert>
              )}

              <TextField
                id="console-server-name"
                label="Address to serve it on"
                value={serverName}
                onChange={(event) => setServerName(event.target.value)}
                placeholder="phpmyadmin.example.com"
                autoComplete="off"
                spellCheck={false}
                error={serverName ? nameError : null}
                hint="A name you control and point at this server. There is no default on purpose: a database console on an address nobody chose is one somebody else finds first."
              />

              <div className="flex items-center justify-between gap-3">
                <p className="flex items-start gap-1.5 text-xs text-slate-500">
                  <ShieldAlert aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  Signing in needs a database account. The panel stores no credentials in its
                  configuration, and the server&rsquo;s root account cannot sign in here at all.
                </p>
                <Button
                  type="submit"
                  variant="primary"
                  loading={install.isPending || watching}
                  disabled={Boolean(nameError)}
                >
                  Install
                </Button>
              </div>
            </form>
          </RequirePermission>
        )}
      </div>

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          setWatching(true);
          uninstall.mutate(undefined, { onSuccess: () => setConfirming(false) });
        }}
        title="Remove phpMyAdmin?"
        description="The site stops being served and the package is removed. No database or account is touched."
        confirmLabel="Remove"
        destructive
        loading={uninstall.isPending}
        error={uninstall.isError ? String((uninstall.error as Error).message) : null}
      />
    </Card>
  );
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-slate-500">{label}</dt>
      <dd className={['truncate text-slate-800', mono ? 'font-mono text-xs' : ''].join(' ')}>
        {value}
      </dd>
    </div>
  );
}

/** The same rule the API applies, checked here so a typo costs no round trip. */
const DOMAIN_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

function validateServerName(value: string): string | null {
  const trimmed = value.trim().toLowerCase();
  if (!trimmed) {
    return 'An address is required.';
  }
  if (!DOMAIN_PATTERN.test(trimmed)) {
    return 'Enter a full host name, such as phpmyadmin.example.com.';
  }
  return null;
}
