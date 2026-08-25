/**
 * Display formatting for host metrics.
 *
 * These live together so every byte count and duration in the panel reads the
 * same way, rather than each widget inventing its own.
 */

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

/** formatBytes renders a byte count at a human scale. */
export function formatBytes(bytes: number | null | undefined, fractionDigits = 1): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) {
    return '—';
  }
  if (bytes < 1024) {
    return `${Math.round(bytes)} B`;
  }

  let value = bytes;
  let unit = 0;
  // 1024 rather than 1000: these are memory and filesystem figures, which
  // every other tool on the host reports in binary units.
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(fractionDigits)} ${BYTE_UNITS[unit]}`;
}

/** formatBytesPerSecond renders a transfer rate. */
export function formatBytesPerSecond(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) {
    return '—';
  }
  return `${formatBytes(bytes, 1)}/s`;
}

/** formatPercent renders a percentage with one decimal place. */
export function formatPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '—';
  }
  return `${value.toFixed(1)}%`;
}

/**
 * formatUptime renders a duration in the largest two units that matter.
 *
 * "12d 4h" is what an operator wants; "12d 4h 37m 12s" is noise on a figure
 * that changes every second anyway.
 */
export function formatUptime(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) {
    return '—';
  }

  const days = Math.floor(seconds / 86_400);
  const hours = Math.floor((seconds % 86_400) / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);

  if (days > 0) {
    return `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  return `${minutes}m`;
}

/** formatDateTime renders an ISO timestamp in the viewer's locale. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) {
    return '—';
  }
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) {
    return '—';
  }
  return parsed.toLocaleString();
}

/** formatClockTime renders just the time, for a chart axis. */
export function formatClockTime(iso: string): string {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) {
    return '';
  }
  return parsed.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/**
 * usageTone maps a percentage to a severity colour band.
 *
 * The thresholds mirror the server's alert defaults so a bar turning red and
 * an alert appearing are the same event, not two systems disagreeing.
 */
export function usageTone(percent: number | null | undefined): 'ok' | 'warn' | 'error' {
  if (percent === null || percent === undefined) {
    return 'ok';
  }
  if (percent >= 90) {
    return 'error';
  }
  if (percent >= 80) {
    return 'warn';
  }
  return 'ok';
}
