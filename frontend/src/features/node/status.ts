import type { StatusTone } from '@/components/StatusPill';
import type { NodeApp, NodeAppStatus } from '@/types/api';

/** How an application's state is shown. */
export function appStatusPill(status: NodeAppStatus): { label: string; tone: StatusTone } {
  switch (status) {
    case 'running':
      return { label: 'Running', tone: 'ok' };
    case 'starting':
      return { label: 'Starting', tone: 'info' };
    case 'failed':
      return { label: 'Failed', tone: 'error' };
    case 'stopped':
      return { label: 'Stopped', tone: 'neutral' };
    default:
      return { label: status, tone: 'neutral' };
  }
}

/**
 * What to show about an application beyond its state.
 *
 * "Running" and "answering" are not the same thing. A process that is up but
 * not listening is the most confusing state an application can be in, and the
 * panel should name it rather than let someone conclude the site is fine.
 */
export function runtimeNote(app: NodeApp): string | null {
  if (!app.runtime) {
    return null;
  }
  if (app.runtime.state === 'running' && !app.runtime.listening) {
    return 'The process is running but nothing is listening on its port.';
  }
  if (app.runtime.detail) {
    return app.runtime.detail;
  }
  return null;
}

/** formatUptime renders how long a process has been up. */
export function formatUptime(seconds: number): string {
  if (seconds <= 0) {
    return 'just started';
  }
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours}h ${minutes % 60}m`;
  }
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

/**
 * How the host runs applications, in words rather than a keyword.
 *
 * It matters to the reader because it decides where the logs are: under
 * systemd they are in the journal, which this panel does not read.
 */
export function managedByLabel(managedBy: string): string {
  return managedBy === 'systemd' ? 'systemd' : 'the panel’s agent';
}
