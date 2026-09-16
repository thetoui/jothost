import { useEffect, useState } from 'react';
import { ExternalLink, ShieldAlert, Table2, Trash2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { LinkButton } from '@/components/ui/Link';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { CONSOLE_MOUNT } from '@/features/databases/console';
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
          served ? (
            <LinkButton href={data?.url || CONSOLE_MOUNT}>
              Open
              <ExternalLink aria-hidden="true" className="h-3.5 w-3.5" />
            </LinkButton>
          ) : undefined
        }
      />

      <div className="space-y-3 p-5">
        {served ? (
          <>
            <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
              <Fact label="Address" value={CONSOLE_MOUNT} mono />
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
                setWatching(true);
                install.mutate('');
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

              {/* No address to choose. phpMyAdmin is served by the panel's
                  own nginx on the loopback and reached only through the
                  panel's /phpmyadmin/ location, so a name typed here reached
                  nothing — it was displayed as an address and no request could
                  ever arrive with it. */}
              <p className="text-sm text-ink">
                It will be served by this panel, at{' '}
                <code className="font-mono text-xs text-ink-strong">{CONSOLE_MOUNT}</code> on the
                address you are reading this on. It is reachable only by people who can already
                sign in here.
              </p>

              <div className="flex items-center justify-between gap-3">
                <p className="flex items-start gap-1.5 text-xs text-ink-muted">
                  <ShieldAlert aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  Signing in needs a database account. The panel stores no credentials in its
                  configuration, and the server&rsquo;s root account cannot sign in here at all.
                </p>
                <Button
                  type="submit"
                  variant="primary"
                  loading={install.isPending || watching}
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
      <dt className="text-ink-muted">{label}</dt>
      <dd className={['truncate text-ink-strong', mono ? 'font-mono text-xs' : ''].join(' ')}>
        {value}
      </dd>
    </div>
  );
}

