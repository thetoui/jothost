import { useState } from 'react';
import { Loader2, RefreshCw } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
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
import type { MetricPoint, MetricRange } from '@/types/api';

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
    return (
      <div className="flex h-full items-center justify-center" role="status" aria-live="polite">
        <Loader2 aria-hidden="true" className="h-5 w-5 animate-spin text-slate-400" />
        <span className="sr-only">Loading the dashboard</span>
      </div>
    );
  }

  if (isError || !snapshot) {
    return (
      <div className="mx-auto max-w-3xl">
        <h1 className="text-2xl font-semibold text-slate-900">Dashboard</h1>
        <p role="alert" className="mt-4 rounded-md border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800">
          {errorMessage(error, 'The dashboard could not be loaded.')}
        </p>
      </div>
    );
  }

  const { server } = snapshot;

  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-slate-900">{server.hostname}</h1>
          <p className="mt-1 text-sm text-slate-600">
            Updated {formatDateTime(snapshot.generated_at)}
          </p>
        </div>

        <div className="flex items-center gap-3">
          <StatusPill
            label={server.status === 'online' ? 'Online' : server.status === 'offline' ? 'Offline' : 'Unknown'}
            tone={server.status === 'online' ? 'ok' : server.status === 'offline' ? 'error' : 'neutral'}
          />
          <button
            type="button"
            onClick={() => void refetch()}
            disabled={isFetching}
            className="inline-flex items-center gap-2 rounded-md border border-surface-border px-3 py-1.5 text-sm text-slate-700 hover:bg-surface-muted disabled:opacity-60"
          >
            <RefreshCw
              aria-hidden="true"
              className={`h-4 w-4 ${isFetching ? 'animate-spin' : ''}`}
            />
            Refresh
          </button>
        </div>
      </header>

      <WidgetCard title="Alerts" widget={{ available: true, data: snapshot.alerts }}>
        {(alerts) => <AlertList alerts={alerts} />}
      </WidgetCard>

      <div className="grid gap-6 lg:grid-cols-2">
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
                className={`rounded-md px-2 py-1 text-xs font-medium transition-colors ${
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
