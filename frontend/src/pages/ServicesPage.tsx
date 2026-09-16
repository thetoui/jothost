import { useState } from 'react';
import {
  Database,
  FileCode2,
  Globe,
  Play,
  RotateCw,
  ServerCog,
  Shield,
  Square,
  Zap,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Toggle } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useServiceAction, useServices } from '@/features/services/hooks';
import { ApiError } from '@/services/apiClient';
import type { HostService, ServiceAction } from '@/types/api';

/** The order the roles are shown in, and what each section is called. */
const sections: { role: HostService['role']; title: string; icon: React.ReactNode }[] = [
  { role: 'web', title: 'Web', icon: <Globe className="h-4 w-4" /> },
  { role: 'runtime', title: 'Runtimes', icon: <FileCode2 className="h-4 w-4" /> },
  { role: 'database', title: 'Databases', icon: <Database className="h-4 w-4" /> },
  { role: 'cache', title: 'Cache', icon: <Zap className="h-4 w-4" /> },
  { role: 'system', title: 'System', icon: <Shield className="h-4 w-4" /> },
];

/**
 * ServicesPage lists the daemons on this host and controls them.
 *
 * Only services the panel knows about are listed — the ones an earlier phase
 * installs and configures, plus the machine's own. A panel that offered to stop
 * any unit on the host would be offering to stop the one it runs as.
 */
export function ServicesPage() {
  const { data, isPending, isError, error } = useServices();

  const services = data?.services ?? [];
  const controllable = data?.controllable ?? false;

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">Services</h1>
        <p className="mt-1 text-sm text-ink-muted">
          The daemons this host runs, and whether they start at boot.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The services could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {!isPending && !isError && !controllable && services.length > 0 && (
        <Alert tone="info" title="This host has no service manager">
          The states below are read from the process table and are accurate. Starting and
          stopping needs an init system — systemd or OpenRC — and this host has neither.
        </Alert>
      )}

      {isPending ? (
        <Card>
          <SkeletonRows rows={4} />
        </Card>
      ) : services.length === 0 ? (
        <Card>
          <EmptyState
            icon={<ServerCog className="h-6 w-6" />}
            title="No services were found"
            description="Nothing this panel manages is installed on the host yet."
          />
        </Card>
      ) : (
        // Two across on wide screens: a host runs several service roles and a
        // single column left the right half of the page empty while pushing the
        // lower roles below the fold. items-start so a role with one daemon does
        // not stretch to match a taller neighbour.
        <div className="grid items-start gap-4 lg:grid-cols-2">
          {sections
            .map((section) => ({
              ...section,
              entries: services.filter((service) => service.role === section.role),
            }))
            .filter((section) => section.entries.length > 0)
            .map((section) => (
              <Card key={section.role}>
                <CardHeader
                  title={section.title}
                  icon={<TintedIcon tone="brand" icon={section.icon} />}
                />
                <CardBody className="divide-y divide-surface-border p-0">
                  {section.entries.map((service) => (
                    <ServiceRow key={service.key} service={service} />
                  ))}
                </CardBody>
              </Card>
            ))}
        </div>
      )}
    </div>
  );
}

function ServiceRow({ service }: { service: HostService }) {
  const act = useServiceAction();
  const [confirming, setConfirming] = useState<ServiceAction | null>(null);

  const error =
    act.error instanceof ApiError
      ? act.error.message
      : act.error
        ? 'The service did not respond as expected.'
        : null;

  const pill = service.running
    ? { label: 'Running', tone: 'ok' as const }
    : service.active_state === 'failed'
      ? { label: 'Failed', tone: 'error' as const }
      : service.active_state === 'activating'
        ? { label: 'Starting', tone: 'warn' as const }
        : { label: 'Stopped', tone: 'neutral' as const };

  function run(action: ServiceAction) {
    act.mutate({ key: service.key, action });
  }

  return (
    <div className="px-5 py-3.5">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-ink-strong">{service.label}</span>
            <StatusPill label={pill.label} tone={pill.tone} dot />
            {service.protected && (
              <span className="rounded-full bg-surface-sunken px-2 py-0.5 text-xs text-ink">
                Protected
              </span>
            )}
          </div>
          <p className="mt-0.5 text-sm text-ink-muted">{service.summary}</p>
          <p className="mt-0.5 text-xs text-ink-dim">
            {service.unit || service.units[0]}
            {service.pid > 0 && <> · pid {service.pid}</>}
          </p>
        </div>

        <RequirePermission permission={Permission.ServerManage}>
          <div className="flex shrink-0 items-center gap-2">
            {service.self_managed ? (
              <p className="max-w-xs text-right text-xs text-ink-muted">
                Started and stopped by {service.self_managed_by || 'the panel'}, not from
                here.
              </p>
            ) : !service.controllable && !service.unit ? (
              // Installed, and its state above is read from the process table,
              // but the init system has no service for it — so there is nothing
              // here to start, and a button would only fail.
              <p className="max-w-xs text-right text-xs text-ink-muted">
                This host&rsquo;s init system does not know about it, so it cannot be
                started or stopped here.
              </p>
            ) : service.running ? (
              <>
                <Button
                  variant="secondary"
                  disabled={!service.controllable || act.isPending}
                  onClick={() => run('restart')}
                  icon={<RotateCw aria-hidden="true" className="h-4 w-4" />}
                >
                  Restart
                </Button>
                <Button
                  variant="secondary"
                  disabled={!service.controllable || service.protected || act.isPending}
                  onClick={() => setConfirming('stop')}
                  icon={<Square aria-hidden="true" className="h-4 w-4" />}
                >
                  Stop
                </Button>
              </>
            ) : (
              <Button
                variant="secondary"
                disabled={!service.controllable || act.isPending}
                onClick={() => run('start')}
                icon={<Play aria-hidden="true" className="h-4 w-4" />}
              >
                Start
              </Button>
            )}
          </div>
        </RequirePermission>
      </div>

      {/* Boot behaviour is withheld for the same reason the buttons are: what
          starts PHP-FPM at boot is the panel, and a toggle claiming otherwise
          would be describing a decision the init system does not make. */}
      {/* A toggle is also withheld where the init system has no service to
          enable: an unchecked box would be claiming it does not start at boot,
          which is not the same as nothing being able to say. */}
      {!service.self_managed && (service.controllable || service.unit !== '') && (
        <RequirePermission permission={Permission.ServerManage}>
          <div className="mt-2">
            <Toggle
              id={`boot-${service.key}`}
              label="Start at boot"
              checked={service.enabled === true}
              disabled={!service.controllable || service.protected || act.isPending}
              onChange={(checked) => run(checked ? 'enable' : 'disable')}
            />
            {service.enabled === null && service.controllable && (
              <p className="mt-1 text-xs text-ink-muted">
                This host cannot say whether it starts at boot.
              </p>
            )}
          </div>
        </RequirePermission>
      )}

      {error && (
        <Alert tone="danger" title={`${service.label} could not be changed`}>
          {error}
        </Alert>
      )}

      <ConfirmDialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          run('stop');
          setConfirming(null);
        }}
        title={`Stop ${service.label}?`}
        confirmLabel={`Stop ${service.label}`}
        destructive
        loading={act.isPending}
        error={error}
      >
        <p>
          {service.role === 'web'
            ? 'Every website on this host stops being served until it is started again.'
            : service.role === 'database'
              ? 'Every site that uses this database stops working until it is started again.'
              : 'Anything depending on this service stops working until it is started again.'}
        </p>
      </ConfirmDialog>
    </div>
  );
}
