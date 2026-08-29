import type { FileEntry } from '@/types/api';

/** formatSize renders a byte count the way a file manager should. */
export function formatSize(bytes: number, type?: string): string {
  if (type === 'directory') {
    // A directory's own inode size tells a person nothing, so it is not shown.
    return '—';
  }
  if (bytes < 0) {
    return '—';
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
  // One decimal below 10, none above: "1.4 MB" is useful, "847.3 KB" is noise.
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

/** formatModified renders a timestamp, or a dash when there is not one. */
export function formatModified(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return '—';
  }
  return parsed.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/**
 * describeMode renders an octal mode as the rwx string people read.
 *
 * A hosting panel's users think in "755", but the letters are what makes a
 * wrong value obvious at a glance — 0777 on a document root should look wrong.
 */
export function describeMode(mode: string): string {
  const parsed = Number.parseInt(mode, 8);
  if (Number.isNaN(parsed)) {
    return mode;
  }

  const bits = ['r', 'w', 'x'];
  let out = '';
  for (let group = 2; group >= 0; group -= 1) {
    const value = (parsed >> (group * 3)) & 0o7;
    for (let bit = 0; bit < 3; bit += 1) {
      out += (value >> (2 - bit)) & 1 ? bits[bit] : '-';
    }
  }
  return out;
}

/** isWorldWritable flags a mode that lets anyone on the host change the file. */
export function isWorldWritable(mode: string): boolean {
  const parsed = Number.parseInt(mode, 8);
  if (Number.isNaN(parsed)) {
    return false;
  }
  return (parsed & 0o002) !== 0;
}

/** parentOf returns a path's parent, or an empty string at the top. */
export function parentOf(path: string): string {
  const trimmed = path.replace(/\/+$/, '');
  const index = trimmed.lastIndexOf('/');
  if (index <= 0) {
    return '';
  }
  return trimmed.slice(0, index);
}

/** joinPath appends a name to a directory. */
export function joinPath(directory: string, name: string): string {
  return `${directory.replace(/\/+$/, '')}/${name}`;
}

/**
 * breadcrumbs turns a path into the segments a person can click back through.
 *
 * Segments above the root are not offered: the panel would refuse them, and a
 * link that always fails is worse than no link.
 */
export function breadcrumbs(path: string, root: string): { name: string; path: string }[] {
  if (!path.startsWith(root)) {
    return [{ name: path, path }];
  }

  const crumbs = [{ name: root, path: root }];
  const remainder = path.slice(root.length).split('/').filter(Boolean);

  let current = root;
  for (const segment of remainder) {
    current = joinPath(current, segment);
    crumbs.push({ name: segment, path: current });
  }
  return crumbs;
}

/**
 * canOpen says whether an entry can be navigated into or downloaded.
 *
 * A symlink out of the root is shown but not followed, and a device or socket
 * is neither: acting on one is how a file manager hangs on /dev/zero.
 */
export function canOpen(entry: FileEntry): boolean {
  if (entry.type === 'directory') {
    return true;
  }
  if (entry.type === 'file') {
    return true;
  }
  if (entry.type === 'symlink') {
    return entry.target_inside_root === true;
  }
  return false;
}
