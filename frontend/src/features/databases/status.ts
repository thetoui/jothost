import type { StatusTone } from '@/components/StatusPill';
import type { DatabasePrivilege, DatabaseStatus } from '@/types/api';

/** How a database's lifecycle state is shown. */
export function databaseStatusPill(status: DatabaseStatus): { label: string; tone: StatusTone } {
  switch (status) {
    case 'active':
      return { label: 'Active', tone: 'ok' };
    case 'creating':
      return { label: 'Creating', tone: 'info' };
    case 'deleting':
      return { label: 'Deleting', tone: 'warn' };
    case 'failed':
      return { label: 'Failed', tone: 'error' };
    default:
      return { label: status, tone: 'neutral' };
  }
}

/**
 * What a privilege level actually permits, in the words someone choosing it
 * needs.
 *
 * "readwrite" tells a reader nothing about whether their application's
 * migrations will run; "cannot create or drop tables" tells them exactly.
 */
export const privilegeDescriptions: Record<DatabasePrivilege, string> = {
  readonly: 'Read only — SELECT, nothing else',
  readwrite: 'Read and write rows, but cannot create or drop tables',
  full: 'Everything inside this database, including schema changes',
};

/** A short label for a privilege level. */
export const privilegeLabels: Record<DatabasePrivilege, string> = {
  readonly: 'Read only',
  readwrite: 'Read/write',
  full: 'Full access',
};

/** The tone a privilege is shown in: more access, more prominence. */
export function privilegeTone(privilege: DatabasePrivilege): StatusTone {
  switch (privilege) {
    case 'readonly':
      return 'neutral';
    case 'readwrite':
      return 'info';
    case 'full':
      return 'warn';
    default:
      return 'neutral';
  }
}

/**
 * Trims the engine's own name out of its version string.
 *
 * MariaDB reports "11.4.12-MariaDB", so a chip built as name + version reads
 * "MariaDB 11.4.12-MariaDB". The suffix is the server telling a client which
 * fork it is, which the label beside it has already said.
 */
export function engineVersion(version: string | undefined): string {
  if (!version) {
    return '';
  }
  return version.replace(/-(MariaDB|MySQL|log)\b.*$/i, '');
}

/** How an engine is named in the interface. */
export function engineLabel(engine: string): string {
  switch (engine) {
    case 'mariadb':
      return 'MariaDB';
    case 'mysql':
      return 'MySQL';
    case 'postgres':
      return 'PostgreSQL';
    default:
      return engine;
  }
}

/**
 * formatBytes renders a size for a person.
 *
 * null is "not measured yet", which is a different fact from zero: a database
 * the panel has never sized and an empty one would otherwise both read "0 B",
 * and only one of them is worth investigating.
 */
export function formatBytes(bytes: number | null): string {
  if (bytes === null) {
    return 'Not measured';
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }

  const units = ['KB', 'MB', 'GB', 'TB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}
