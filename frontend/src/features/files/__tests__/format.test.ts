import { describe, expect, it } from 'vitest';

import {
  breadcrumbs,
  canOpen,
  describeMode,
  formatModified,
  formatSize,
  isWorldWritable,
  joinPath,
  parentOf,
} from '@/features/files/format';
import type { FileEntry } from '@/types/api';

function entry(overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    name: 'index.php',
    path: '/var/www/site/public/index.php',
    type: 'file',
    size: 1024,
    mode: '0644',
    modified: '2026-08-29T10:00:00Z',
    owner: 'web_site',
    group: 'nginx',
    uid: 1001,
    gid: 101,
    editable: true,
    ...overrides,
  };
}

describe('formatSize', () => {
  it('shows bytes below a kilobyte', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(999)).toBe('999 B');
  });

  it('scales to larger units', () => {
    expect(formatSize(1024)).toBe('1.0 KB');
    expect(formatSize(1024 * 1024 * 1.5)).toBe('1.5 MB');
    expect(formatSize(1024 ** 3 * 2)).toBe('2.0 GB');
  });

  it('drops the decimal once the number is big enough not to need it', () => {
    expect(formatSize(1024 * 512)).toBe('512 KB');
  });

  // A directory's own inode size tells a person nothing.
  it('does not pretend a directory has a meaningful size', () => {
    expect(formatSize(4096, 'directory')).toBe('—');
  });
});

describe('describeMode', () => {
  it('renders the letters people read permissions in', () => {
    expect(describeMode('0644')).toBe('rw-r--r--');
    expect(describeMode('0755')).toBe('rwxr-xr-x');
    expect(describeMode('0600')).toBe('rw-------');
    expect(describeMode('0777')).toBe('rwxrwxrwx');
    expect(describeMode('0000')).toBe('---------');
  });

  it('returns the input unchanged when it is not a mode', () => {
    expect(describeMode('nonsense')).toBe('nonsense');
  });
});

// World-writable inside a document root is almost always a mistake, and it is
// the one a person most needs to spot in a long listing.
describe('isWorldWritable', () => {
  it('flags a mode anyone on the host can write to', () => {
    expect(isWorldWritable('0777')).toBe(true);
    expect(isWorldWritable('0666')).toBe(true);
    expect(isWorldWritable('0002')).toBe(true);
  });

  it('leaves ordinary modes alone', () => {
    expect(isWorldWritable('0644')).toBe(false);
    expect(isWorldWritable('0755')).toBe(false);
    expect(isWorldWritable('0640')).toBe(false);
  });
});

describe('parentOf', () => {
  it('walks one level up', () => {
    expect(parentOf('/var/www/site/public')).toBe('/var/www/site');
    expect(parentOf('/var/www/site/public/')).toBe('/var/www/site');
  });

  it('stops at the top rather than returning something unusable', () => {
    expect(parentOf('/var')).toBe('');
    expect(parentOf('/')).toBe('');
  });
});

describe('joinPath', () => {
  it('appends a name without doubling the separator', () => {
    expect(joinPath('/var/www', 'site')).toBe('/var/www/site');
    expect(joinPath('/var/www/', 'site')).toBe('/var/www/site');
  });
});

describe('breadcrumbs', () => {
  it('walks from the root to the current folder', () => {
    expect(breadcrumbs('/var/www/site/public', '/var/www')).toEqual([
      { name: '/var/www', path: '/var/www' },
      { name: 'site', path: '/var/www/site' },
      { name: 'public', path: '/var/www/site/public' },
    ]);
  });

  it('is just the root when that is where you are', () => {
    expect(breadcrumbs('/var/www', '/var/www')).toEqual([{ name: '/var/www', path: '/var/www' }]);
  });

  // Offering a crumb above the root would be a link the panel always refuses.
  it('does not offer segments above the root', () => {
    const crumbs = breadcrumbs('/etc/nginx', '/var/www');
    expect(crumbs).toHaveLength(1);
    expect(crumbs[0]?.path).toBe('/etc/nginx');
  });
});

describe('canOpen', () => {
  it('allows files and directories', () => {
    expect(canOpen(entry())).toBe(true);
    expect(canOpen(entry({ type: 'directory' }))).toBe(true);
  });

  // A link out of the root is shown so it can be deleted, but never followed.
  it('refuses a symlink that leaves the root', () => {
    expect(canOpen(entry({ type: 'symlink', target_inside_root: false }))).toBe(false);
    expect(canOpen(entry({ type: 'symlink', target_inside_root: true }))).toBe(true);
  });

  // Opening a device can block forever.
  it('refuses anything that is not a file, directory or link', () => {
    expect(canOpen(entry({ type: 'other' }))).toBe(false);
  });
});

describe('formatModified', () => {
  it('does not render an invalid date as "Invalid Date"', () => {
    expect(formatModified('not a date')).toBe('—');
  });

  it('renders a real timestamp', () => {
    expect(formatModified('2026-08-29T10:00:00Z')).not.toBe('—');
  });
});
