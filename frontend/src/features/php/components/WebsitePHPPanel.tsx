import { useEffect, useState, type FormEvent } from 'react';
import { FileCode2, Save } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { Skeleton } from '@/components/ui/Loading';
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
import type { PHPPool } from '@/types/api';

interface WebsitePHPPanelProps {
  websiteId: string;
}

/** WebsitePHPPanel selects a website's PHP version and its php.ini values. */
export function WebsitePHPPanel({ websiteId }: WebsitePHPPanelProps) {
  const { data: state, isPending } = useWebsitePHP(websiteId);
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
    setPHP.mutate({ websiteId, version: selected === '' ? null : selected });
  }

  const versionError =
    setPHP.error instanceof ApiError
      ? setPHP.error.message
      : setPHP.error
        ? 'The PHP version could not be changed.'
        : null;

  return (
    <Card>
      <CardHeader
        title="PHP"
        description={
          state?.enabled ? `Running PHP ${current} in its own pool` : 'This site serves static content'
        }
        icon={<TintedIcon tone={state?.enabled ? 'brand' : 'neutral'} icon={<FileCode2 className="h-4 w-4" />} />}
      />

      {isPending ? (
        <CardBody className="space-y-3">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-9 w-56" />
        </CardBody>
      ) : (
        <CardBody className="space-y-5">
          {available.length === 0 ? (
            <Alert tone="info">
              No PHP version is installed on this server, so this site serves static content
              only.
            </Alert>
          ) : (
            <RequirePermission
              permission={Permission.WebsiteUpdate}
              fallback={
                <p className="text-sm text-ink">
                  {state?.enabled
                    ? `This website runs PHP ${current}.`
                    : 'This website serves static content.'}
                </p>
              }
            >
              <form onSubmit={handleVersionSubmit} className="space-y-3">
                <div className="flex flex-wrap items-end gap-2">
                  <div className="w-56">
                    <SelectField
                      id="php-select"
                      label="Version"
                      value={selected}
                      onChange={(event) => setSelected(event.target.value)}
                    >
                      <option value="">None — static site</option>
                      {available.map((version) => (
                        <option key={version.id} value={version.version}>
                          PHP {version.version}
                        </option>
                      ))}
                    </SelectField>
                  </div>
                  <Button
                    type="submit"
                    variant="primary"
                    loading={setPHP.isPending}
                    disabled={selected === current}
                  >
                    {setPHP.isPending ? 'Applying…' : 'Apply'}
                  </Button>
                </div>

                {versionError && <Alert tone="danger">{versionError}</Alert>}
              </form>
            </RequirePermission>
          )}

          {state?.enabled && state.pool && (
            <RequirePermission permission={Permission.WebsiteUpdate}>
              <ConfigForm websiteId={websiteId} pool={state.pool} />
            </RequirePermission>
          )}
        </CardBody>
      )}
    </Card>
  );
}

/** ConfigForm edits a site's php.ini values. */
function ConfigForm({ websiteId, pool }: { websiteId: string; pool: PHPPool }) {
  const setConfig = useSetPHPConfig(websiteId);

  const [memoryLimit, setMemoryLimit] = useState(pool.memory_limit ?? '256M');
  const [uploadSize, setUploadSize] = useState(pool.upload_max_filesize ?? '64M');
  const [executionTime, setExecutionTime] = useState(String(pool.max_execution_time ?? 30));
  const [opcache, setOpcache] = useState(pool.opcache_enabled);
  const [validationError, setValidationError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const seconds = Number.parseInt(executionTime, 10);
    const problem =
      memoryLimitError(memoryLimit) ??
      uploadSizeError(uploadSize) ??
      executionTimeError(Number.isNaN(seconds) ? -1 : seconds);

    if (problem) {
      setValidationError(problem);
      setSaved(false);
      return;
    }
    setValidationError(null);

    setConfig.mutate(
      {
        memory_limit: memoryLimit.trim(),
        upload_max_filesize: uploadSize.trim(),
        max_execution_time: seconds,
        opcache,
      },
      { onSuccess: () => setSaved(true) },
    );
  }

  const serverError =
    setConfig.error instanceof ApiError
      ? setConfig.error.message
      : setConfig.error
        ? 'The configuration could not be saved.'
        : null;
  const error = validationError ?? serverError;

  return (
    <form onSubmit={handleSubmit} noValidate className="space-y-4 border-t border-surface-border pt-5">
      <h3 className="text-sm font-semibold text-ink-strong">Configuration</h3>

      <div className="grid gap-4 sm:grid-cols-3">
        <TextField
          id="php-memory"
          label="Memory limit"
          value={memoryLimit}
          onChange={(event) => setMemoryLimit(event.target.value)}
          hint="e.g. 256M, or -1"
        />
        <TextField
          id="php-upload"
          label="Max upload"
          value={uploadSize}
          onChange={(event) => setUploadSize(event.target.value)}
          hint="e.g. 64M"
        />
        <TextField
          id="php-exec"
          label="Max execution"
          value={executionTime}
          onChange={(event) => setExecutionTime(event.target.value)}
          hint="Seconds. 0 is unlimited."
        />
      </div>

      <Toggle
        id="php-opcache"
        label="Enable OPcache"
        description="Caches compiled scripts. Recommended for production sites."
        checked={opcache}
        onChange={(value) => {
          setOpcache(value);
          setSaved(false);
        }}
      />

      {error && <Alert tone="danger">{error}</Alert>}
      {saved && !error && !setConfig.isPending && (
        <Alert tone="success">
          Saved. The pool reloads in the background; requests in flight are not dropped.
        </Alert>
      )}

      <Button
        type="submit"
        variant="secondary"
        loading={setConfig.isPending}
        icon={<Save aria-hidden="true" className="h-4 w-4" />}
      >
        {setConfig.isPending ? 'Saving…' : 'Save configuration'}
      </Button>
    </form>
  );
}
