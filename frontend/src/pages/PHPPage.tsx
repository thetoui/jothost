import { useState, type FormEvent } from 'react';
import { Download, FileCode2, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, ProgressBar, SkeletonRows } from '@/components/ui/Loading';
import { TextField } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useInstallPHP, usePHPVersions, useUninstallPHP } from '@/features/php/hooks';
import { versionStatusPill } from '@/features/php/settings';
import { ApiError } from '@/services/apiClient';
import type { PHPVersion } from '@/types/api';

/** PHPPage lists the PHP versions this server has and manages them. */
export function PHPPage() {
  const { data, isPending, isError, error } = usePHPVersions();
  const versions = data?.versions ?? [];

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">PHP</h1>
        <p className="mt-1 text-sm text-slate-500">
          Versions installed on this server. Each website chooses one and runs it in its own
          pool, under its own account.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The PHP versions could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <Card>
            <CardHeader
              title="Installed versions"
              description={
                versions.length > 0
                  ? `${versions.filter((v) => v.installed).length} available on this host`
                  : undefined
              }
              icon={<TintedIcon tone="brand" icon={<FileCode2 className="h-4 w-4" />} />}
            />

            {isPending && <SkeletonRows rows={3} />}

            {!isPending && versions.length === 0 && !isError && (
              <EmptyState
                icon={<FileCode2 className="h-6 w-6" />}
                title="No PHP is installed"
                description="Websites can serve static content until a version is installed."
              />
            )}

            {versions.length > 0 && (
              <ul className="divide-y divide-surface-border">
                {versions.map((version) => (
                  <VersionRow key={version.id} version={version} />
                ))}
              </ul>
            )}
          </Card>
        </div>

        <RequirePermission
          permission={Permission.ServerManage}
          fallback={
            <Card className="h-fit">
              <CardHeader title="Installing versions" />
              <CardBody>
                <p className="text-sm text-slate-500">
                  Installing or removing a PHP version changes the whole server, so it needs the
                  server management permission.
                </p>
              </CardBody>
            </Card>
          }
        >
          <InstallCard />
        </RequirePermission>
      </div>
    </div>
  );
}

/** VersionRow is one PHP version and what can be done with it. */
function VersionRow({ version }: { version: PHPVersion }) {
  const [confirming, setConfirming] = useState(false);
  const uninstall = useUninstallPHP();
  const pill = versionStatusPill(version.status, version.installed);
  const settling = version.status === 'installing' || version.status === 'removing';

  const error =
    uninstall.error instanceof ApiError
      ? uninstall.error.message
      : uninstall.error
        ? 'That version could not be removed.'
        : null;

  return (
    <li className="px-5 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <TintedIcon
            tone={version.status === 'failed' ? 'danger' : version.installed ? 'ok' : 'neutral'}
            icon={<FileCode2 className="h-4 w-4" />}
          />
          <div className="min-w-0">
            <p className="text-sm font-semibold text-slate-900">PHP {version.version}</p>
            {version.fpm_service && (
              <p className="truncate font-mono text-xs text-slate-500">{version.fpm_service}</p>
            )}
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-3">
          <StatusPill label={pill.label} tone={pill.tone} dot pulse={settling} />

          {!version.installed ? (
            // Nothing to remove. This row is here to say the version exists
            // and could be installed - offering Remove on it was a control
            // whose only possible outcome was an error, and it read as though
            // the panel thought the version was there.
            null
          ) : version.in_use > 0 ? (
            // Removing a version websites still run would take every one of
            // them offline, so the row says why instead of offering an action
            // the API will refuse.
            <span className="text-xs text-slate-500">
              In use by {version.in_use} website{version.in_use === 1 ? '' : 's'}
            </span>
          ) : (
            <RequirePermission permission={Permission.ServerManage}>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setConfirming(true)}
                disabled={settling}
                className="text-danger-600 hover:bg-danger-50 hover:text-danger-700"
                icon={<Trash2 aria-hidden="true" className="h-3.5 w-3.5" />}
              >
                Remove
              </Button>
            </RequirePermission>
          )}
        </div>
      </div>

      {settling && (
        <ProgressBar
          label={`PHP ${version.version} is ${version.status}`}
          tone={version.status === 'removing' ? 'danger' : 'brand'}
          className="mt-2.5"
        />
      )}

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() =>
          uninstall.mutate(version.version, { onSuccess: () => setConfirming(false) })
        }
        title={`Remove PHP ${version.version}?`}
        description="The package is removed from the server."
        confirmLabel="Remove version"
        destructive
        loading={uninstall.isPending}
        error={error}
      >
        <p>
          No website currently runs PHP {version.version}, so nothing goes offline. Reinstalling
          it later downloads the package again.
        </p>
      </ConfirmDialog>
    </li>
  );
}

/** InstallCard queues installation of a version. */
function InstallCard() {
  const [version, setVersion] = useState('');
  const install = useInstallPHP();

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const value = version.trim();
    if (value === '') {
      return;
    }
    install.mutate(value, { onSuccess: () => setVersion('') });
  }

  const message =
    install.error instanceof ApiError
      ? install.error.message
      : install.error
        ? 'That version could not be installed.'
        : null;

  return (
    <Card className="h-fit">
      <CardHeader
        title="Install a version"
        icon={<TintedIcon tone="brand" icon={<Download className="h-4 w-4" />} />}
      />
      <CardBody>
        <form onSubmit={handleSubmit} noValidate className="space-y-3">
          <TextField
            id="php-version"
            label="Version"
            type="text"
            autoComplete="off"
            spellCheck={false}
            placeholder="8.3"
            value={version}
            onChange={(event) => setVersion(event.target.value)}
            error={message}
            hint="A major.minor release, such as 8.3."
          />

          <Button
            type="submit"
            variant="primary"
            loading={install.isPending}
            className="w-full"
            icon={<Download aria-hidden="true" className="h-4 w-4" />}
          >
            {install.isPending ? 'Queuing…' : 'Install'}
          </Button>

          <p className="text-xs text-slate-500">
            Installation runs in the background and can take several minutes. Existing sites keep
            serving throughout.
          </p>
        </form>
      </CardBody>
    </Card>
  );
}
