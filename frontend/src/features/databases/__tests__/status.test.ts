import { describe, expect, it } from 'vitest';

import { engineVersion, formatBytes } from '@/features/databases/status';

describe('engineVersion', () => {
  it('drops the fork name the server appends to its version', () => {
    // Otherwise a chip built as label + version reads "MariaDB 11.4.12-MariaDB".
    expect(engineVersion('11.4.12-MariaDB')).toBe('11.4.12');
    expect(engineVersion('8.0.36-log')).toBe('8.0.36');
  });

  it('leaves a plain version alone', () => {
    expect(engineVersion('16.15')).toBe('16.15');
  });

  it('renders nothing for a version the host did not report', () => {
    expect(engineVersion(undefined)).toBe('');
  });
});

describe('formatBytes', () => {
  // "Never measured" and "empty" are different facts, and only one of them is
  // worth an operator's attention.
  it('distinguishes an unmeasured size from an empty one', () => {
    expect(formatBytes(null)).toBe('Not measured');
    expect(formatBytes(0)).toBe('0 B');
  });

  it('scales to the largest unit that keeps the number readable', () => {
    expect(formatBytes(65536)).toBe('64 KB');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(7_340_032)).toBe('7.0 MB');
  });
});
