import type { PHPVersionStatus } from '@/types/api';

type Tone = 'ok' | 'warn' | 'error' | 'neutral';

const versionTones: Record<PHPVersionStatus, { label: string; tone: Tone }> = {
  available: { label: 'Installed', tone: 'ok' },
  installing: { label: 'Installing', tone: 'warn' },
  removing: { label: 'Removing', tone: 'warn' },
  failed: { label: 'Failed', tone: 'error' },
};

export function versionStatusPill(
  status: PHPVersionStatus,
  installed: boolean,
): { label: string; tone: Tone } {
  // A version whose row survives but is no longer on the host must not read as
  // "Installed": a site pointed at it would return 502.
  if (status === 'available' && !installed) {
    return { label: 'Not installed', tone: 'neutral' };
  }
  return versionTones[status] ?? { label: status, tone: 'neutral' };
}

/**
 * Validation mirrored from shared/validate.
 *
 * This is a courtesy so a user is told before a round trip; the API and the
 * Agent both validate again, and they are what actually decide.
 */
const SIZE_PATTERN = /^\d+[KMG]?$/;

/** Limits matching shared/validate. */
export const MAX_MEMORY_MB = 4096;
export const MAX_UPLOAD_MB = 2048;
export const MAX_EXECUTION_SECONDS = 3600;

function sizeInMB(value: string): number | null {
  const match = SIZE_PATTERN.exec(value);
  if (!match) {
    return null;
  }
  const amount = Number.parseInt(value, 10);
  if (Number.isNaN(amount)) {
    return null;
  }
  if (value.endsWith('G')) {
    return amount * 1024;
  }
  if (value.endsWith('M')) {
    return amount;
  }
  if (value.endsWith('K')) {
    return amount / 1024;
  }
  return amount / (1024 * 1024);
}

export function memoryLimitError(raw: string): string | null {
  const value = raw.trim();
  if (value === '') {
    return 'Enter a memory limit, such as 256M.';
  }
  // PHP uses -1 for "no limit", and a hosting panel has reason to allow it.
  if (value === '-1') {
    return null;
  }

  const megabytes = sizeInMB(value);
  if (megabytes === null) {
    return 'Use a size such as 256M or 1G.';
  }
  if (megabytes > MAX_MEMORY_MB) {
    return `The memory limit may not exceed ${MAX_MEMORY_MB}M.`;
  }
  return null;
}

export function uploadSizeError(raw: string): string | null {
  const value = raw.trim();
  if (value === '') {
    return 'Enter an upload limit, such as 64M.';
  }

  const megabytes = sizeInMB(value);
  if (megabytes === null) {
    return 'Use a size such as 64M or 1G.';
  }
  if (megabytes > MAX_UPLOAD_MB) {
    return `The upload limit may not exceed ${MAX_UPLOAD_MB}M.`;
  }
  return null;
}

export function executionTimeError(value: number): string | null {
  if (!Number.isInteger(value) || value < 0) {
    return 'Enter a whole number of seconds.';
  }
  if (value > MAX_EXECUTION_SECONDS) {
    // A site that can hold a worker this long starves every other request to
    // that site, because the pool has a bounded number of workers.
    return `The execution time may not exceed ${MAX_EXECUTION_SECONDS} seconds.`;
  }
  return null;
}
