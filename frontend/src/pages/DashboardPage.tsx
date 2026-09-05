import { useState } from 'react';
import { Cpu, HardDrive, MemoryStick, RefreshCw, Timer } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader } from '@/components/ui/Card';
import { Skeleton, SkeletonText } from '@/components/ui/Loading';
import { focusRingTight } from '@/components/ui/focus';
import { StatTile } from '@/features/dashboard/components/StatTile';
import { AlertList } from '@/features/dashboard/components/AlertList';
import { MetricChart, type ChartSeries } from '@/features/dashboard/components/MetricChart';
import { ServiceList } from '@/features/dashboard/components/ServiceList';
import { UsageBar } from '@/features/dashboard/components/UsageBar';
import { WidgetCard } from '@/features/dashboard/components/WidgetCard';
import {
  formatBytes,
  formatBytesPerSecond,
  formatDateTime,
  formatPercent,
  formatUptime,
} from '@/features/dashboard/format';
import { useDashboard, useMetricHistory } from '@/features/dashboard/hooks';
import { errorMessage } from '@/features/auth/hooks';
import type { DashboardSnapshot, Filesystem, MetricPoint, MetricRange } from '@/types/api';

const RANGES: MetricRange[] = ['1h', '24h', '7d', '30d'];

// Chart colours are fixed hex values rather than Tailwind classes because they
// are passed to SVG stroke attributes, which do not read class names.
const RESOURCE_SERIES: ChartSeries[] = [
  { label: 'CPU', color: '#3b6ef6', value: (p: MetricPoint) => p.cpu_percent },
  { label: 'Memory', color: '#8b5cf6', value: (p: MetricPoint) => p.memory_percent },
  { label: 'Disk', color: '#f59e0b', value: (p: MetricPoint) => p.disk_percent },
];

const NETWORK_SERIES: ChartSeries[] = [
  { label: 'Received', color: '#10b981', value: (p: MetricPoint) => p.network_rx_per_second },
  { label: 'Sent', color: '#3b6ef6', value: (p: MetricPoint) => p.network_tx_per_second },
];

export function DashboardPage() {
  const { data: snapshot, isPending, isError, error, isFetching, refetch } = useDashboard();
  const [range, setRange] = useState<MetricRange>('1h');
  const history = useMetricHistory(snapshot?.server.id, range);

  if (isPending) {
    return <DashboardSkeleton />;
  }

  if (isError || !snapshot) {
    return (
      <div className="space-y-4">
        <h1 className="text-xl font-semibold text-slate-900">Dashboard</h1>
        <Alert tone="danger" title="The dashboard could not be loaded">
          {errorMessage(error, 'Try again in a moment.')}
        </Alert>
      </div>
    );
  }

  const { server } = snapshot;

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">{server.hostname}</h1>
          <p className="mt-1 text-sm text-slate-500">
            Updated {formatDateTime(snapshot.generated_at)}
          </p>
        </div>

        <div className="flex items-center gap-3">
          <StatusPill
            label={server.status === 'online' ? 'Online' : server.status === 'offline' ? 'Offline' : 'Unknown'}
            tone={server.status === 'online' ? 'ok' : server.status === 'offline' ? 'error' : 'neutral'}
            dot
            pulse={server.status === 'online'}
          />
          <Button
            variant="secondary"
            onClick={() => void refetch()}
            disabled={isFetching}
            icon={
              <RefreshCw
                aria-hidden="true"
                className={`h-4 w-4 ${isFetching ? 'animate-spin' : ''}`}
              />
            }
          >
            Refresh
          </Button>
        </div>
      </header>

      {/* The headline row: the state of the host in one glance, before any of
          the detail panels below are read. */}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="CPU"
          icon={<Cpu className="h-4 w-4" />}
          tone={usageTone(snapshot.cpu.data?.usage_percent)}
          {...(snapshot.cpu.available && snapshot.cpu.data
            ? {
                value: formatPercent(snapshot.cpu.data.usage_percent),
                detail: `${snapshot.cpu.data.cores} core${snapshot.cpu.data.cores === 1 ? '' : 's'}`,
                percent: snapshot.cpu.data.usage_percent,
              }
            : { value: undefined, unavailable: 'Not collected' })}
        />
        <StatTile
          label="Memory"
          icon={<MemoryStick className="h-4 w-4" />}
          tone={usageTone(snapshot.memory.data?.used_percent)}
          {...(snapshot.memory.available && snapshot.memory.data
            ? {
                value: formatPercent(snapshot.memory.data.used_percent),
                detail: `${formatBytes(snapshot.memory.data.used_bytes)} of ${formatBytes(snapshot.memory.data.total_bytes)}`,
                percent: snapshot.memory.data.used_percent,
              }
            : { value: undefined, unavailable: 'Not collected' })}
        />
        <StatTile
          label="Disk"
          icon={<HardDrive className="h-4 w-4" />}
          tone={usageTone(primaryFilesystem(snapshot)?.used_percent)}
          {...(primaryFilesystem(snapshot)
            ? {
                value: formatPercent(primaryFilesystem(snapshot)?.used_percent ?? 0),
                detail: primaryFilesystem(snapshot)?.mount_point,
                percent: primaryFilesystem(snapshot)?.used_percent ?? 0,
              }
            : { value: undefined, unavailable: 'Not collected' })}
        />
        <StatTile
          label="Uptime"
          icon={<Timer className="h-4 w-4" />}
          tone="brand"
          {...(snapshot.system.available && snapshot.system.data
            ? {
                value: formatUptime(snapshot.system.data.uptime_seconds),
                detail: `${snapshot.system.data.os_name} ${snapshot.system.data.os_version}`.trim(),
              }
            : { value: undefined, unavailable: 'Not collected' })}
        />
      </div>

      <WidgetCard title="Alerts" widget={{ available: true, data: snapshot.alerts }}>
        {(alerts) => <AlertList alerts={alerts} />}
      </WidgetCard>

      <div className="grid gap-5 lg:grid-cols-2">
        <WidgetCard title="CPU" widget={snapshot.cpu}>
          {(cpu) => (
            <div className="space-y-4">
              <UsageBar
                percent={cpu.usage_percent}
                label="Usage"
                detail={`${cpu.cores} core${cpu.cores === 1 ? '' : 's'}`}
              />
              <dl className="grid grid-cols-3 gap-3 text-xs">
                <Stat label="User" value={formatPercent(cpu.user_percent)} />
                <Stat label="System" value={formatPercent(cpu.system_percent)} />
                <Stat label="I/O wait" value={formatPercent(cpu.iowait_percent)} />
              </dl>
            </div>
          )}
        </WidgetCard>

        <WidgetCard title="Memory" widget={snapshot.memory}>
          {(memory) => (
            <div className="space-y-4">
              <UsageBar
                percent={memory.used_percent}
                label="Used"
                detail={`${formatBytes(memory.used_bytes)} of ${formatBytes(memory.total_bytes)}`}
              />
              {memory.swap_total_bytes > 0 && (
                <UsageBar
                  percent={memory.swap_used_percent}
                  label="Swap"
                  detail={`${formatBytes(memory.swap_used_bytes)} of ${formatBytes(memory.swap_total_bytes)}`}
                />
              )}
              <dl className="grid grid-cols-2 gap-3 text-xs">
                <Stat label="Available" value={formatBytes(memory.available_bytes)} />
                <Stat label="Cached" value={formatBytes(memory.cached_bytes)} />
              </dl>
            </div>
          )}
        </WidgetCard>

        <WidgetCard title="Disk" widget={snapshot.disk}>
          {(disk) => (
            <div className="space-y-4">
              {disk.filesystems.length === 0 ? (
                <p className="text-sm text-slate-500">No filesystems reported.</p>
              ) : (
                disk.filesystems.map((fs) => (
                  <UsageBar
                    key={fs.mount_point}
                    percent={fs.used_percent}
                    label={fs.mount_point}
                    detail={`${formatBytes(fs.used_bytes)} of ${formatBytes(fs.total_bytes)}`}
                  />
                ))
              )}
            </div>
          )}
        </WidgetCard>

        <WidgetCard title="Network" widget={snapshot.network}>
          {(network) => (
            <div className="space-y-3">
              {network.interfaces.length === 0 ? (
                <p className="text-sm text-slate-500">No interfaces reported.</p>
              ) : (
                <ul className="divide-y divide-surface-border">
                  {network.interfaces.map((iface) => (
                    <li key={iface.name} className="flex items-center justify-between py-2 text-sm">
                      <span className="font-medium text-slate-800">{iface.name}</span>
                      <span className="tabular-nums text-xs text-slate-600">
                        ↓ {formatBytesPerSecond(iface.rx_bytes_per_second)} · ↑{' '}
                        {formatBytesPerSecond(iface.tx_bytes_per_second)}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </WidgetCard>

        <WidgetCard title="Services" widget={snapshot.services}>
          {(services) => (
            <ServiceList
              services={services}
              {...(snapshot.services.unsupported && snapshot.services.error
                ? { unsupportedNote: snapshot.services.error }
                : {})}
            />
          )}
        </WidgetCard>

        <WidgetCard title="Server" widget={snapshot.system}>
          {(system) => (
            <dl className="grid grid-cols-2 gap-3 text-xs">
              <Stat label="Operating system" value={`${system.os_name} ${system.os_version}`.trim()} />
              <Stat label="Kernel" value={system.kernel_version} />
              <Stat label="Architecture" value={system.architecture} />
              <Stat label="Cores" value={String(system.cores)} />
              <Stat label="Uptime" value={formatUptime(system.uptime_seconds)} />
              <Stat label="Agent" value={server.agent_version ?? '—'} />
              {snapshot.load.available && snapshot.load.data && (
                <Stat
                  label="Load average"
                  value={`${snapshot.load.data.load_1.toFixed(2)} / ${snapshot.load.data.load_5.toFixed(2)} / ${snapshot.load.data.load_15.toFixed(2)}`}
                />
              )}
            </dl>
          )}
        </WidgetCard>
      </div>

      <WidgetCard
        title="History"
        widget={{ available: true, data: history.data?.points ?? [] }}
        action={
          <div className="flex gap-1" role="group" aria-label="History range">
            {RANGES.map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setRange(option)}
                aria-pressed={range === option}
                className={`rounded-md px-2 py-1 text-xs font-medium transition-colors ${focusRingTight} ${
                  range === option
                    ? 'bg-brand-50 text-brand-700'
                    : 'text-slate-500 hover:bg-surface-muted hover:text-slate-800'
                }`}
              >
                {option}
              </button>
            ))}
          </div>
        }
      >
        {(points) => (
          <div className="space-y-6">
            <div>
              <h3 className="mb-2 text-xs font-medium uppercase tracking-wide text-slate-500">
                Resource usage
              </h3>
              <MetricChart
                points={points}
                series={RESOURCE_SERIES}
                max={100}
                formatValue={(value) => `${value.toFixed(0)}%`}
              />
            </div>

            <div>
              <h3 className="mb-2 text-xs font-medium uppercase tracking-wide text-slate-500">
                Network throughput
              </h3>
              <MetricChart
                points={points}
                series={NETWORK_SERIES}
                formatValue={(value) => formatBytes(value, 0)}
              />
            </div>
          </div>
        )}
      </WidgetCard>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="font-medium uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-0.5 text-slate-900">{value}</dd>
    </div>
  );
}

/**
 * usageTone colours a headline figure by how close it is to full.
 *
 * The thresholds match the API's own alert bands, so a tile and the alert list
 * beside it never disagree about whether something is a problem.
 */
function usageTone(percent: number | undefined): 'ok' | 'warn' | 'danger' | 'neutral' {
  if (percent === undefined) {
    return 'neutral';
  }
  if (percent >= 90) {
    return 'danger';
  }
  if (percent >= 75) {
    return 'warn';
  }
  return 'ok';
}

/**
 * primaryFilesystem picks the filesystem worth putting in the headline row.
 *
 * The fullest one is chosen rather than the root: a host runs out of space on
 * whichever mount fills first, and that is the number an operator needs to see
 * without opening the disk panel.
 */
function primaryFilesystem(snapshot: DashboardSnapshot): Filesystem | undefined {
  const filesystems = snapshot.disk.available ? (snapshot.disk.data?.filesystems ?? []) : [];
  if (filesystems.length === 0) {
    return undefined;
  }
  return filesystems.reduce((fullest, candidate) =>
    candidate.used_percent > fullest.used_percent ? candidate : fullest,
  );
}

/** DashboardSkeleton holds the page's shape while the first snapshot loads. */
function DashboardSkeleton() {
  return (
    <div className="space-y-5" role="status" aria-live="polite">
      <span className="sr-only">Loading the dashboard</span>

      <div className="flex items-center justify-between">
        <div className="space-y-2">
          <Skeleton className="h-6 w-48" />
          <Skeleton className="h-3 w-32" />
        </div>
        <Skeleton className="h-9 w-24 rounded-md" />
      </div>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, index) => (
          <Card key={index} className="p-4">
            <div className="flex items-start justify-between gap-3">
              <div className="flex-1 space-y-2">
                <Skeleton className="h-2.5 w-16" />
                <Skeleton className="h-7 w-20" />
              </div>
              <Skeleton className="h-8 w-8 rounded-md" />
            </div>
            <Skeleton className="mt-3 h-1.5 w-full rounded-full" />
          </Card>
        ))}
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        {Array.from({ length: 4 }, (_, index) => (
          <Card key={index}>
            <CardHeader title={<Skeleton className="h-3.5 w-24" />} />
            <CardBody>
              <SkeletonText lines={4} />
            </CardBody>
          </Card>
        ))}
      </div>
    </div>
  );
}
