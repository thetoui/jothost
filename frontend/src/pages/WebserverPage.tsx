import { useState } from 'react';
import { Check, Download, Layers, Server, Zap } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Skeleton } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useInstallApache,
  useSetWebserverMode,
  useWebserver,
} from '@/features/webserver/hooks';
import { ApiError } from '@/services/apiClient';
import type { WebserverMode, WebserverStatus } from '@/types/api';

/**
 * WebserverPage chooses how this host serves every site on it.
 *
 * It is one setting for the whole machine rather than a per-site switch,
 * because both servers are one process tree: a panel that let one site opt in
 * would be running Apache for that site and charging everyone else the memory.
 */
export function WebserverPage() {
  const { data, isPending, isError, error } = useWebserver();

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Web server</h1>
        <p className="mt-1 text-sm text-slate-500">
          How this host serves its websites. The choice applies to every site on the machine.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The web server settings could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {isPending ? <Skeleton className="h-64 w-full rounded-card" /> : data ? <Arrangement status={data} /> : null}
    </div>
  );
}

function Arrangement({ status }: { status: WebserverStatus }) {
  const [pending, setPending] = useState<WebserverMode | null>(null);
  const setMode = useSetWebserverMode();
  const install = useInstallApache();

  const modeError =
    setMode.error instanceof ApiError
      ? setMode.error.message
      : setMode.error
        ? 'The arrangement could not be changed.'
        : null;

  const installError =
    install.error instanceof ApiError
      ? install.error.message
      : install.error
        ? 'Apache could not be installed.'
        : null;

  return (
    <div className="grid gap-5 lg:grid-cols-3">
      <div className="space-y-4 lg:col-span-2">
        <ModeCard
          mode="nginx"
          current={status.mode}
          title="nginx only"
          icon={<Zap className="h-4 w-4" />}
          summary="nginx serves every site itself: static files, and PHP through each site's own pool."
          points={[
            'The fewest moving parts, and the least memory per site.',
            'No .htaccess: rewrites and access rules live in the site configuration the panel writes.',
          ]}
          onChoose={() => setPending('nginx')}
        />

        <ModeCard
          mode="hybrid"
          current={status.mode}
          title="nginx + Apache"
          icon={<Layers className="h-4 w-4" />}
          summary="nginx holds the public ports and proxies to Apache on the loopback, which serves the files and the PHP."
          points={[
            '.htaccess works, which is what most existing applications expect.',
            'Apache sees the real visitor address, not the proxy.',
            'A second server per request: more memory, and a little more latency.',
          ]}
          disabled={!status.apache.available}
          disabledReason="Apache is not installed on this host."
          onChoose={() => setPending('hybrid')}
        />

        {modeError && (
          <Alert tone="danger" title="The arrangement could not be changed">
            {modeError}
          </Alert>
        )}
      </div>

      <div className="space-y-4">
        <Card>
          <CardHeader
            title="Apache"
            icon={<TintedIcon tone="brand" icon={<Server className="h-4 w-4" />} />}
          />
          <CardBody className="space-y-3 text-sm">
            <Fact
              label="Installed"
              value={status.apache.available ? (status.apache.version ?? 'Yes') : 'No'}
            />
            <Fact
              label="Running"
              value={
                status.apache.available
                  ? status.apache.running
                    ? 'Yes'
                    : 'Stopped'
                  : '—'
              }
            />
            <Fact label="Sites behind it" value={String(status.apache.sites)} />
            <Fact label="Websites on this host" value={String(status.sites)} />

            {!status.apache.available && (
              <RequirePermission permission={Permission.ServerManage}>
                <div className="pt-1">
                  <Button
                    variant="secondary"
                    loading={install.isPending}
                    disabled={!status.apache.can_install}
                    onClick={() => install.mutate()}
                    icon={<Download aria-hidden="true" className="h-4 w-4" />}
                  >
                    Install Apache
                  </Button>
                  <p className="mt-2 text-xs text-slate-500">
                    {status.apache.can_install
                      ? 'Installs Apache and the FastCGI proxy module. It changes nothing about how sites are served until the arrangement is switched.'
                      : 'This host has no package manager the panel can use.'}
                  </p>
                </div>
              </RequirePermission>
            )}

            {installError && (
              <Alert tone="danger" title="Apache could not be installed">
                {installError}
              </Alert>
            )}
          </CardBody>
        </Card>
      </div>

      <ConfirmDialog
        open={pending !== null}
        onClose={() => setPending(null)}
        onConfirm={() => {
          if (!pending) {
            return;
          }
          setMode.mutate(pending, { onSuccess: () => setPending(null) });
        }}
        title={pending === 'hybrid' ? 'Put Apache behind nginx?' : 'Serve everything from nginx?'}
        confirmLabel={pending === 'hybrid' ? 'Switch to nginx + Apache' : 'Switch to nginx only'}
        loading={setMode.isPending}
        error={modeError}
      >
        <p className="mb-2">
          Every website on this host — {status.sites} of them — has its configuration rewritten.
        </p>
        <p className="text-slate-600">
          Sites keep serving throughout: each is reconfigured and reloaded in turn, and a site
          that fails to reload keeps the configuration it already had.
        </p>
      </ConfirmDialog>
    </div>
  );
}

function ModeCard({
  mode,
  current,
  title,
  icon,
  summary,
  points,
  disabled,
  disabledReason,
  onChoose,
}: {
  mode: WebserverMode;
  current: WebserverMode;
  title: string;
  icon: React.ReactNode;
  summary: string;
  points: string[];
  disabled?: boolean;
  disabledReason?: string;
  onChoose: () => void;
}) {
  const active = current === mode;

  return (
    <Card className={active ? 'ring-1 ring-brand-500' : ''}>
      <CardHeader
        title={title}
        icon={<TintedIcon tone={active ? 'brand' : 'neutral'} icon={icon} />}
        action={
          active ? (
            <StatusPill label="In use" tone="ok" dot />
          ) : (
            <RequirePermission permission={Permission.ServerManage}>
              <Button variant="secondary" onClick={onChoose} disabled={disabled}>
                Use this
              </Button>
            </RequirePermission>
          )
        }
      />
      <CardBody className="space-y-2 text-sm text-slate-600">
        <p>{summary}</p>
        <ul className="space-y-1">
          {points.map((point) => (
            <li key={point} className="flex gap-2">
              <Check aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0 text-slate-400" />
              <span>{point}</span>
            </li>
          ))}
        </ul>
        {disabled && disabledReason && (
          <p className="text-xs text-slate-500">{disabledReason}</p>
        )}
      </CardBody>
    </Card>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <span className="text-slate-500">{label}</span>
      <span className="font-medium text-slate-900">{value}</span>
    </div>
  );
}
