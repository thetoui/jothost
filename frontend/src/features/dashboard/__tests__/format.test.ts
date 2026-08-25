import { describe, expect, it } from 'vitest';

import {
  formatBytes,
  formatBytesPerSecond,
  formatPercent,
  formatUptime,
  usageTone,
} from '@/features/dashboard/format';

describe('formatBytes', () => {
  it('scales to the largest sensible unit', () => {
    // Binary units, because every other tool on the host reports them that way.
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1024)).toBe('1.0 KB');
    expect(formatBytes(1024 ** 2)).toBe('1.0 MB');
    expect(formatBytes(1024 ** 3)).toBe('1.0 GB');
    expect(formatBytes(16 * 1024 ** 3)).toBe('16.0 GB');
  });

  it('renders a placeholder for a missing value', () => {
    // A missing reading must not render as "0 B", which reads as a real
    // measurement of nothing.
    expect(formatBytes(null)).toBe('—');
    expect(formatBytes(undefined)).toBe('—');
    expect(formatBytes(Number.NaN)).toBe('—');
  });

  it('formats a rate', () => {
    expect(formatBytesPerSecond(2048)).toBe('2.0 KB/s');
    expect(formatBytesPerSecond(null)).toBe('—');
  });
});

describe('formatPercent', () => {
  it('renders one decimal place', () => {
    expect(formatPercent(12.34)).toBe('12.3%');
    expect(formatPercent(100)).toBe('100.0%');
  });

  it('renders a placeholder for a missing value', () => {
    expect(formatPercent(null)).toBe('—');
  });
});

describe('formatUptime', () => {
  it('shows the two largest units that matter', () => {
    expect(formatUptime(90_000)).toBe('1d 1h');
    expect(formatUptime(3_600)).toBe('1h 0m');
    expect(formatUptime(300)).toBe('5m');
  });

  it('rejects nonsense', () => {
    expect(formatUptime(-1)).toBe('—');
    expect(formatUptime(null)).toBe('—');
  });
});

describe('usageTone', () => {
  it('mirrors the server alert thresholds', () => {
    // A bar turning red and an alert appearing must be the same event, not two
    // systems disagreeing.
    expect(usageTone(50)).toBe('ok');
    expect(usageTone(79.9)).toBe('ok');
    expect(usageTone(80)).toBe('warn');
    expect(usageTone(89.9)).toBe('warn');
    expect(usageTone(90)).toBe('error');
    expect(usageTone(100)).toBe('error');
  });

  it('treats a missing value as ok rather than alarming', () => {
    expect(usageTone(null)).toBe('ok');
  });
});
