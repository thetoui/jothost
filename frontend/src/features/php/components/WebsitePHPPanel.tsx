import { useEffect, useState, type FormEvent } from 'react';

import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  usePHPVersions,
  useSetPHPConfig,
  useSetWebsitePHP,
  useWebsitePHP,
} from '@/features/php/hooks';
import {
  executionTimeError,
  memoryLimitError,
  uploadSizeError,
} from '@/features/php/settings';
import { ApiError } from '@/services/apiClient';

interface WebsitePHPPanelProps {
  websiteId: string;
}

/** WebsitePHPPanel selects a website's PHP version and its php.ini values. */
export function WebsitePHPPanel({ websiteId }: WebsitePHPPanelProps) {
  const { data: state, isLoading } = useWebsitePHP(websiteId);
  const { data: versionList } = usePHPVersions();
  const setPHP = useSetWebsitePHP();

  // Only versions actually on the host are offered: selecting one that is not
  // installed produces a site that returns 502 on its first request.
  const available = (versionList?.versions ?? []).filter((version) => version.installed);
  const current = state?.pool?.php_version ?? '';

  const [selected, setSelected] = useState('');
  useEffect(() => {
    setSelected(current);
  }, [current]);

  function handleVersionSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPHP.mutate({
      websiteId,
      version: selected === '' ? null : selected,
    });
  }

  const versionError =
    setPHP.error instanceof ApiError
      ? setPHP.error.message
      : setPHP.error
        ? 'The PHP version could not be changed.'
        : null;

  return (
    <section
      aria-label="PHP"
      className="rounded-lg border border-surface-border bg-surface shadow-sm"
    >
      <h2 className="border-b border-surface-border px-4 py-3 text-sm font-semibold text-slate-900">
        PHP
      </h2>

      {isLoading ? (
        <p className="px-4 py-4 text-sm text-slate-500">Loading…</p>
      ) : (
        <div className="space-y-4 p-4">
          {available.length === 0 ? (
            <p className="text-sm text-slate-500">
              No PHP version is installed on this server, so this site serves static content
              only.
            </p>
          ) : (
            <RequirePermission
              permission={Permission.WebsiteUpdate}
              fallback={
                <p className="text-sm text-slate-600">
                  {state?.enabled
                    ? `This website runs PHP ${current}.`
                    : 'This website serves static content.'}
                </p>
              }
            >
              <form onSubmit={handleVersionSubmit} className="space-y-2">
                <label htmlFor="php-select" className="block text-sm font-medium text-slate-700">
                  Version
                </label>
                <div className="flex flex-wrap gap-2">
                  <select
                    id="php-select"
                    value={selected}
                    onChange={(event) => setSelected(event.target.value)}
                    className="rounded-md border border-surface-border px-3 py-2 text-sm shadow-sm focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500"
                  >
                    <option value="">None — static site</option>
                    {available.map((version) => (
                      <option key={version.id} value={version.version}>
                        PHP {version.version}
                      </option>
                    ))}
                  </select>
                  <button
                    type="submit"
                    disabled={setPHP.isPending || selected === current}
                    className="rounded-md bg-brand-600 px-3 py-2 text-sm font-medium text-white shadow-sm hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-60"
                  >
                    {setPHP.isPending ? 'Applying…' : 'Apply'}
                  </button>
                </div>
                {versionError && (
                  <p role="alert" className="text-sm text-rose-600">
                    {versionError}
                  </p>
                )}
              </form>
            </RequirePermission>
          )}

          {state?.enabled && state.pool && (
            <RequirePermission permission={Permission.WebsiteUpdate}>
              <ConfigForm websiteId={websiteId} pool={state.pool} />
            </RequirePermission>
          )}
        </div>
      )}
    </section>
  );
}

interface ConfigFormProps {
  websiteId: string;
  pool: NonNullable<import('@/types/api').WebsitePHP['pool']>;
}

/** ConfigForm edits a site's php.ini values. */
function ConfigForm({ websiteId, pool }: ConfigFormProps) {
  const setConfig = useSetPHPConfig(websiteId);

  const [memoryLimit, setMemoryLimit] = useState(pool.memory_limit ?? '256M');
  const [uploadSize, setUploadSize] = useState(pool.upload_max_filesize ?? '64M');
  const [executionTime, setExecutionTime] = useState(String(pool.max_execution_time ?? 30));
  const [opcache, setOpcache] = useState(pool.opcache_enabled);
  const [validationError, setValidationError] = useState<string | null>(null);

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const seconds = Number.parseInt(executionTime, 10);
    const problem =
      memoryLimitError(memoryLimit) ??
      uploadSizeError(uploadSize) ??
      executionTimeError(Number.isNaN(seconds) ? -1 : seconds);

    if (problem) {
      setValidationError(problem);
      return;
    }
    setValidationError(null);

    setConfig.mutate({
      memory_limit: memoryLimit.trim(),
      upload_max_filesize: uploadSize.trim(),
      max_execution_time: seconds,
      opcache,
    });
  }

  const serverError =
    setConfig.error instanceof ApiError
      ? setConfig.error.message
      : setConfig.error
        ? 'The configuration could not be saved.'
        : null;
  const error = validationError ?? serverError;

  return (
    <form onSubmit={handleSubmit} noValidate className="space-y-3 border-t border-surface-border pt-4">
      <h3 className="text-sm font-medium text-slate-900">Configuration</h3>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          id="php-memory"
          label="Memory limit"
          value={memoryLimit}
          onChange={setMemoryLimit}
          hint="For example 256M, or -1 for no limit."
        />
        <Field
          id="php-upload"
          label="Max upload size"
          value={uploadSize}
          onChange={setUploadSize}
          hint="For example 64M."
        />
        <Field
          id="php-exec"
          label="Max execution time"
          value={executionTime}
          onChange={setExecutionTime}
          hint="Seconds. 0 means no limit."
        />
      </div>

      <label className="flex items-center gap-2 text-sm text-slate-700">
        <input
          type="checkbox"
          checked={opcache}
          onChange={(event) => setOpcache(event.target.checked)}
          className="h-4 w-4 rounded border-surface-border text-brand-600 focus:ring-brand-500"
        />
        Enable OPcache
      </label>

      {error && (
        <p role="alert" className="text-sm text-rose-600">
          {error}
        </p>
      )}

      <button
        type="submit"
        disabled={setConfig.isPending}
        className="rounded-md border border-surface-border px-3 py-2 text-sm font-medium text-slate-700 hover:bg-surface-muted disabled:cursor-not-allowed disabled:opacity-60"
      >
        {setConfig.isPending ? 'Saving…' : 'Save configuration'}
      </button>
    </form>
  );
}

interface FieldProps {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  hint: string;
}

function Field({ id, label, value, onChange, hint }: FieldProps) {
  return (
    <div>
      <label htmlFor={id} className="block text-sm font-medium text-slate-700">
        {label}
      </label>
      <input
        id={id}
        type="text"
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-describedby={`${id}-hint`}
        className="mt-1 w-full rounded-md border border-surface-border px-3 py-2 text-sm shadow-sm focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500"
      />
      <p id={`${id}-hint`} className="mt-1 text-xs text-slate-500">
        {hint}
      </p>
    </div>
  );
}
