import { useState, type FormEvent } from 'react';
import { FileCode2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useInstallPHP, usePHPVersions, useUninstallPHP } from '@/features/php/hooks';
import { versionStatusPill } from '@/features/php/settings';
import { ApiError } from '@/services/apiClient';

/** PHPPage lists the PHP versions this server has and manages them. */
export function PHPPage() {
  const { data, isLoading, isError, error } = usePHPVersions();
  const versions = data?.versions ?? [];

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">PHP</h1>
        <p className="mt-1 text-sm text-slate-500">
          Versions installed on this server. Each website chooses one, and runs it in its
          own pool under its own account.
        </p>
      </header>

      <RequirePermission permission={Permission.ServerManage}>
        <InstallForm />
      </RequirePermission>

      {isError && (
        <p role="alert" className="text-sm text-rose-600">
          {error instanceof Error ? error.message : 'The PHP versions could not be loaded.'}
        </p>
      )}

      {isLoading && <p className="text-sm text-slate-500">Loading PHP versions…</p>}

      {!isLoading && versions.length === 0 && !isError && (
        <div className="rounded-lg border border-dashed border-surface-border p-8 text-center">
          <FileCode2 aria-hidden="true" className="mx-auto h-8 w-8 text-slate-300" />
          <p className="mt-2 text-sm font-medium text-slate-900">No PHP is installed</p>
          <p className="mt-1 text-sm text-slate-500">
            Websites can serve static content until a version is installed.
          </p>
        </div>
      )}

      {versions.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-surface-border bg-surface shadow-sm">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Installed PHP versions</caption>
            <thead className="border-b border-surface-border bg-surface-muted text-xs uppercase tracking-wide text-slate-500">
              <tr>
                <th scope="col" className="px-4 py-2 font-medium">
                  Version
                </th>
                <th scope="col" className="px-4 py-2 font-medium">
                  Status
                </th>
                <th scope="col" className="px-4 py-2 font-medium">
                  Websites
                </th>
                <th scope="col" className="px-4 py-2 font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-surface-border">
              {versions.map((version) => {
                const pill = versionStatusPill(version.status, version.installed);
                return (
                  <tr key={version.id} className="hover:bg-surface-muted">
                    <td className="px-4 py-3 font-medium text-slate-900">
                      PHP {version.version}
                      {version.fpm_service && (
                        <p className="font-mono text-xs font-normal text-slate-500">
                          {version.fpm_service}
                        </p>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <StatusPill label={pill.label} tone={pill.tone} />
                    </td>
                    <td className="px-4 py-3 text-slate-600">{version.in_use}</td>
                    <td className="px-4 py-3 text-right">
                      <RequirePermission permission={Permission.ServerManage}>
                        <RemoveButton version={version.version} inUse={version.in_use} />
                      </RequirePermission>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/** InstallForm queues installation of a version. */
function InstallForm() {
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
    <form
      onSubmit={handleSubmit}
      noValidate
      className="rounded-lg border border-surface-border bg-surface p-4 shadow-sm"
    >
      <label htmlFor="php-version" className="block text-sm font-medium text-slate-700">
        Install a version
      </label>
      <div className="mt-1 flex flex-wrap gap-2">
        <input
          id="php-version"
          type="text"
          autoComplete="off"
          spellCheck={false}
          placeholder="8.3"
          value={version}
          onChange={(event) => setVersion(event.target.value)}
          aria-describedby={message ? 'php-install-error' : 'php-install-hint'}
          className="w-32 rounded-md border border-surface-border px-3 py-2 text-sm shadow-sm focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500"
        />
        <button
          type="submit"
          disabled={install.isPending}
          className="rounded-md bg-brand-600 px-3 py-2 text-sm font-medium text-white shadow-sm hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {install.isPending ? 'Queuing…' : 'Install'}
        </button>
      </div>
      <p id="php-install-hint" className="mt-1 text-xs text-slate-500">
        A major.minor release, such as 8.3. Installation runs in the background and can take
        several minutes.
      </p>
      {message && (
        <p id="php-install-error" role="alert" className="mt-2 text-sm text-rose-600">
          {message}
        </p>
      )}
    </form>
  );
}

/** RemoveButton queues removal of a version that nothing uses. */
function RemoveButton({ version, inUse }: { version: string; inUse: number }) {
  const uninstall = useUninstallPHP();

  if (inUse > 0) {
    // Removing it would take every one of those sites offline, so the control
    // says why rather than offering an action that will be refused.
    return (
      <span className="text-xs text-slate-500">
        In use by {inUse} website{inUse === 1 ? '' : 's'}
      </span>
    );
  }

  return (
    <button
      type="button"
      onClick={() => {
        if (window.confirm(`Remove PHP ${version} from this server?`)) {
          uninstall.mutate(version);
        }
      }}
      disabled={uninstall.isPending}
      className="text-xs font-medium text-rose-700 hover:underline disabled:opacity-60"
    >
      Remove
    </button>
  );
}
