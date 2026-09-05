import { useState } from 'react';
import { Activity, Cpu, HardDrive, MemoryStick, Network, Server, Terminal } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Tabs } from '@/components/ui/Tabs';
import {
  formatBytes,
  formatBytesPerSecond,
  formatPercent,
  formatUptime,
} from '@/features/dashboard/format';
import { useDashboard } from '@/features/dashboard/hooks';
import type { ProcessSort } from '@/features/server/api';
import { useHostProcesses } from '@/features/server/hooks';
import type { HostProcess } from '@/types/api';

/** How many processes the table shows. */
const processLimit = 25;

/**
 * ServerPage is the host itself: what it is, what it is using, and what is
 * running on it.
 *
 * It was the one entry in the navigation that went nowhere. PRD section 6
 * names a Server module — server information, CPU, RAM, disk, network, uptime,
 * processes, services, system information — and every part of it had been
 * built except the last question anybody asks of a server, which is what is
 * running on it. The Agent had been collecting processes since Phase 2 and the
 * API had a client method for it that nothing ever called.
 *
 * The resource figures are read from the dashboard's snapshot rather than a
 * second set of endpoints: it is the same host and the query is already
 * cached, so this page costs one extra request.
 */
export function ServerPage() {
  const [sort, setSort] = useState<ProcessSort>('memory');

  const dashboard = useDashboard();
  const snapshot = dashboard.data;

  const system = snapshot?.system.available ? snapshot.system.data : undefined;
  const cpu = snapshot?.cpu.available ? snapshot.cpu.data : undefined;
  const memory = snapshot?.memory.available ? snapshot.memory.data : undefined;
  const disk = snapshot?.disk.available ? snapshot.disk.data : undefined;
  const network = snapshot?.network.available ? snapshot.network.data : undefined;

  // The host reports disk as totals and network as a rate per interface. A
  // percentage and a host total are what a reader wants, so they are derived
  // here — and guarded, because a host that reports a zero-byte disk should
  // show nothing rather than NaN%.
  const diskPercent =
    disk && disk.total_bytes > 0 ? (disk.used_bytes / disk.total_bytes) * 100 : undefined;
  const rxPerSecond = network?.interfaces.reduce(
    (total, iface) => total + iface.rx_bytes_per_second,
    0,
  );
  const txPerSecond = network?.interfaces.reduce(
    (total, iface) => total + iface.tx_bytes_per_second,
    0,
  );

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Server</h1>
        <p className="mt-1 text-sm text-slate-600">
          What this machine is, what it is using, and what is running on it.
        </p>
      </header>

      {dashboard.isError && (
        <Alert tone="danger" title="The host could not be reached">
          {dashboard.error instanceof Error
            ? dashboard.error.message
            : 'The panel could not read this server’s state.'}
        </Alert>
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr),20rem]">
        {/* min-w-0 on the column, not only overflow-x-auto on the table: a grid
            track sized 1fr will not shrink below its content's minimum width,
            so the wide process table pushed the whole page sideways and took
            the card beside it off the screen. */}
        <div className="min-w-0 space-y-4">
          <Card label="Resources">
            <CardHeader
              icon={<TintedIcon tone="brand" icon={<Activity className="h-4 w-4" />} />}
              title="Resources"
              description="Now, on this host"
            />
            <CardBody className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
              <Resource
                icon={<Cpu className="h-4 w-4" />}
                label="CPU"
                value={cpu ? formatPercent(cpu.usage_percent) : '—'}
                {...(cpu ? { detail: `${cpu.cores} cores` } : {})}
              />
              <Resource
                icon={<MemoryStick className="h-4 w-4" />}
                label="Memory"
                value={memory ? formatPercent(memory.used_percent) : '—'}
                {...(memory
                  ? {
                      detail: `${formatBytes(memory.used_bytes)} of ${formatBytes(memory.total_bytes)}`,
                    }
                  : {})}
              />
              <Resource
                icon={<HardDrive className="h-4 w-4" />}
                label="Disk"
                value={diskPercent === undefined ? '—' : formatPercent(diskPercent)}
                {...(disk
                  ? { detail: `${formatBytes(disk.used_bytes)} of ${formatBytes(disk.total_bytes)}` }
                  : {})}
              />
              <Resource
                icon={<Network className="h-4 w-4" />}
                label="Network"
                value={rxPerSecond === undefined ? '—' : `${formatBytesPerSecond(rxPerSecond)} in`}
                {...(txPerSecond === undefined
                  ? {}
                  : { detail: `${formatBytesPerSecond(txPerSecond)} out` })}
              />
            </CardBody>
          </Card>

          <ProcessTable sort={sort} onSort={setSort} />
        </div>

        <Card label="System information" className="h-fit">
          <CardHeader
            icon={<TintedIcon icon={<Server className="h-4 w-4" />} />}
            title="System"
          />
          <CardBody>
            {dashboard.isPending ? (
              <SkeletonRows rows={5} />
            ) : system ? (
              <dl className="space-y-2 text-xs">
                <Fact label="Hostname" value={system.hostname} />
                <Fact label="OS" value={`${system.os_name} ${system.os_version}`} />
                <Fact label="Kernel" value={system.kernel_version} />
                <Fact label="Architecture" value={system.architecture} />
                <Fact label="Uptime" value={formatUptime(system.uptime_seconds)} />
              </dl>
            ) : (
              <p className="text-sm text-slate-500">
                The host did not report its system information.
              </p>
            )}
          </CardBody>
        </Card>
      </div>
    </div>
  );
}

function Resource({
  icon,
  label,
  value,
  detail,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  detail?: string;
}) {
  return (
    <div className="rounded-md border border-surface-border bg-surface-muted p-3">
      <p className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-slate-500">
        <span aria-hidden="true" className="text-slate-400">
          {icon}
        </span>
        {label}
      </p>
      <p className="mt-1 text-xl font-semibold text-slate-900">{value}</p>
      {detail && <p className="mt-0.5 truncate text-xs text-slate-500">{detail}</p>}
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="shrink-0 text-slate-500">{label}</dt>
      <dd className="min-w-0 truncate text-right font-medium text-slate-800">{value}</dd>
    </div>
  );
}

function ProcessTable({
  sort,
  onSort,
}: {
  sort: ProcessSort;
  onSort: (value: ProcessSort) => void;
}) {
  const { data, isPending, isError } = useHostProcesses(processLimit, sort);
  const processes = data?.processes ?? [];

  return (
    <Card label="Processes">
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<Terminal className="h-4 w-4" />} />}
        title="Processes"
        description={
          isPending
            ? 'Reading the process table…'
            : `The ${processes.length} heaviest by ${sort === 'memory' ? 'memory' : 'CPU time'}`
        }
      />

      <div className="px-5">
        <Tabs
          label="Process ordering"
          value={sort}
          onChange={onSort}
          items={[
            { value: 'memory' as const, label: 'By memory' },
            { value: 'cpu' as const, label: 'By CPU time' },
          ]}
        />
      </div>

      {isError ? (
        <CardBody>
          <Alert tone="danger" title="The process list could not be read">
            The host agent did not answer. Processes are read live; there is nothing cached to
            show instead.
          </Alert>
        </CardBody>
      ) : isPending ? (
        <CardBody>
          <SkeletonRows rows={6} />
        </CardBody>
      ) : processes.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Terminal className="h-5 w-5" />}
            title="Nothing to show"
            description="The host reported no processes, which almost certainly means the panel could not read /proc rather than that the machine is idle."
          />
        </CardBody>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[44rem] border-collapse text-sm">
            <thead>
              <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-slate-500">
                <th scope="col" className="px-5 py-2 font-medium">
                  Process
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  User
                </th>
                <th scope="col" className="px-3 py-2 text-right font-medium">
                  Memory
                </th>
                <th scope="col" className="px-3 py-2 text-right font-medium">
                  CPU time
                </th>
                <th scope="col" className="px-3 py-2 text-right font-medium">
                  PID
                </th>
              </tr>
            </thead>
            <tbody>
              {processes.map((process) => (
                <ProcessRow key={process.pid} process={process} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function ProcessRow({ process }: { process: HostProcess }) {
  return (
    <tr className="border-b border-surface-border last:border-0">
      <td className="max-w-0 px-5 py-2">
        <p className="truncate font-medium text-slate-800">{process.name}</p>
        {/* The command line is host data returned verbatim. React escapes it,
            and it is truncated rather than wrapped: a process started with a
            very long argument list would otherwise push the table's own
            columns off the screen. */}
        <p className="truncate font-mono text-xs text-slate-400" title={process.command}>
          {process.command}
        </p>
      </td>
      <td className="whitespace-nowrap px-3 py-2 text-slate-600">{process.user}</td>
      <td className="whitespace-nowrap px-3 py-2 text-right text-slate-700">
        {formatBytes(process.memory_rss_bytes)}
        <span className="ml-1.5 text-xs text-slate-400">
          {formatPercent(process.memory_percent)}
        </span>
      </td>
      {/* Cumulative, not a rate, and labelled that way in the header. Showing
          it as a percentage would be inventing a second sample. */}
      <td className="whitespace-nowrap px-3 py-2 text-right text-slate-600">
        {formatDuration(process.cpu_time_seconds)}
      </td>
      <td className="whitespace-nowrap px-3 py-2 text-right font-mono text-xs text-slate-500">
        {process.pid}
      </td>
    </tr>
  );
}

/** formatDuration renders CPU seconds as h/m/s, largest unit first. */
function formatDuration(seconds: number): string {
  if (seconds < 1) {
    return '<1s';
  }
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = Math.floor(seconds % 60);

  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  if (minutes > 0) {
    return `${minutes}m ${rest}s`;
  }
  return `${rest}s`;
}
