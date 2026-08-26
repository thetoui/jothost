import { describe, expect, it } from 'vitest';

import {
  MAX_EXECUTION_SECONDS,
  MAX_MEMORY_MB,
  executionTimeError,
  memoryLimitError,
  uploadSizeError,
  versionStatusPill,
} from '@/features/php/settings';

describe('memoryLimitError', () => {
  it('accepts PHP size shorthand', () => {
    for (const value of ['128M', '512M', '1G', '268435456', '512K']) {
      expect(memoryLimitError(value), value).toBeNull();
    }
  });

  it('accepts -1, which PHP uses for no limit', () => {
    expect(memoryLimitError('-1')).toBeNull();
  });

  it('rejects anything that is not a size', () => {
    for (const value of ['', '   ', 'unlimited', '512MB', '512 M', '-2', 'abc']) {
      expect(memoryLimitError(value), value).not.toBeNull();
    }
  });

  // These strings reach an FPM pool file, where a newline would let a caller
  // append directives of their choosing.
  it('rejects injection attempts', () => {
    for (const value of ['512M\nuser = root', '512M; evil', '512M[pool]', '512M\r\n[x]']) {
      expect(memoryLimitError(value), value).not.toBeNull();
    }
  });

  it('rejects a limit past the ceiling', () => {
    expect(memoryLimitError(`${MAX_MEMORY_MB + 1}M`)).not.toBeNull();
    expect(memoryLimitError('99G')).not.toBeNull();
  });
});

describe('uploadSizeError', () => {
  it('accepts a size', () => {
    expect(uploadSizeError('64M')).toBeNull();
  });

  it('rejects -1, which is not valid for a size', () => {
    expect(uploadSizeError('-1')).not.toBeNull();
  });

  it('rejects an unbounded size', () => {
    expect(uploadSizeError('99G')).not.toBeNull();
  });
});

describe('executionTimeError', () => {
  it('accepts a bounded number of seconds', () => {
    expect(executionTimeError(30)).toBeNull();
    // 0 means no limit in PHP.
    expect(executionTimeError(0)).toBeNull();
  });

  it('rejects a negative or fractional value', () => {
    expect(executionTimeError(-1)).not.toBeNull();
    expect(executionTimeError(1.5)).not.toBeNull();
  });

  // A site that can hold a worker this long starves every other request to it,
  // because the pool has a bounded number of workers.
  it('rejects a value past the ceiling', () => {
    expect(executionTimeError(MAX_EXECUTION_SECONDS + 1)).not.toBeNull();
  });
});

describe('versionStatusPill', () => {
  it('marks an installed version as ok', () => {
    expect(versionStatusPill('available', true)).toEqual({ label: 'Installed', tone: 'ok' });
  });

  // A version whose row survives but is no longer on the host must not read as
  // "Installed": a site pointed at it would return 502.
  it('does not call a removed version installed', () => {
    const pill = versionStatusPill('available', false);
    expect(pill.label).toBe('Not installed');
    expect(pill.tone).not.toBe('ok');
  });

  it('shows work in flight and failure distinctly', () => {
    expect(versionStatusPill('installing', false).tone).toBe('warn');
    expect(versionStatusPill('failed', false).tone).toBe('error');
  });
});
