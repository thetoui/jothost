import {
  File as FileIcon,
  FileArchive,
  FileCode,
  FileImage,
  Folder,
  Link2,
  HelpCircle,
} from 'lucide-react';

import { useState } from 'react';

import {
  FileActionMenu,
  type FileAction,
} from '@/features/files/components/FileActionMenu';
import { focusRingTight } from '@/components/ui/focus';
import { describeMode, formatModified, formatSize, isWorldWritable } from '@/features/files/format';
import type { FileEntry } from '@/types/api';

interface FileTableProps {
  entries: FileEntry[];
  selected: Set<string>;
  onToggle: (path: string) => void;
  onToggleAll: () => void;
  /** A directory navigates; a file opens its action menu. */
  onOpen: (entry: FileEntry) => void;
  /** Path whose action menu is open, if any. */
  menuFor: string | null;
  onMenuAction: (action: FileAction, entry: FileEntry) => void;
  onMenuClose: () => void;
  canWrite: boolean;
}

/** iconFor picks an icon from the entry type, then the extension. */
function iconFor(entry: FileEntry) {
  if (entry.type === 'directory') {
    return <Folder aria-hidden="true" className="h-4 w-4 text-brand-500" />;
  }
  if (entry.type === 'symlink') {
    return <Link2 aria-hidden="true" className="h-4 w-4 text-slate-400" />;
  }
  if (entry.type === 'other') {
    return <HelpCircle aria-hidden="true" className="h-4 w-4 text-slate-400" />;
  }

  const extension = entry.name.split('.').pop()?.toLowerCase() ?? '';
  if (['zip', 'gz', 'tar', 'tgz', 'bz2', 'xz', '7z', 'rar'].includes(extension)) {
    return <FileArchive aria-hidden="true" className="h-4 w-4 text-amber-500" />;
  }
  if (['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico', 'avif'].includes(extension)) {
    return <FileImage aria-hidden="true" className="h-4 w-4 text-violet-500" />;
  }
  if (
    ['php', 'js', 'ts', 'tsx', 'jsx', 'css', 'html', 'json', 'yml', 'yaml', 'sh', 'sql'].includes(
      extension,
    )
  ) {
    return <FileCode aria-hidden="true" className="h-4 w-4 text-sky-500" />;
  }
  return <FileIcon aria-hidden="true" className="h-4 w-4 text-slate-400" />;
}

/**
 * FileTable lists a directory.
 *
 * A symlink is shown as a symlink with its target, never resolved into what it
 * points at: showing a link's target as though it were the link's own content
 * is how a file manager gets someone to delete the wrong thing.
 */
export function FileTable({
  entries,
  selected,
  onToggle,
  onToggleAll,
  onOpen,
  menuFor,
  onMenuAction,
  onMenuClose,
  canWrite,
}: FileTableProps) {
  const allSelected = entries.length > 0 && entries.every((entry) => selected.has(entry.path));
  // The menu positions itself against the control that opened it, so the
  // element is kept rather than the path alone.
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[46rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-slate-500">
            <th scope="col" className="w-10 px-3 py-2">
              <input
                type="checkbox"
                checked={allSelected}
                onChange={onToggleAll}
                aria-label={allSelected ? 'Clear selection' : 'Select everything here'}
                className="h-4 w-4 rounded border-surface-border text-brand-600 focus:ring-brand-500"
              />
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Name
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Size
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Modified
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Permissions
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Owner
            </th>
          </tr>
        </thead>

        <tbody>
          {entries.map((entry) => {
            const isSelected = selected.has(entry.path);
            const dangling = entry.type === 'symlink' && entry.target_inside_root !== true;

            return (
              <tr
                key={entry.path}
                className={[
                  'border-b border-surface-border/60 transition-colors',
                  isSelected ? 'bg-brand-50/60' : 'hover:bg-surface-sunken',
                ].join(' ')}
              >
                <td className="px-3 py-2">
                  <input
                    type="checkbox"
                    checked={isSelected}
                    onChange={() => onToggle(entry.path)}
                    aria-label={`Select ${entry.name}`}
                    className="h-4 w-4 rounded border-surface-border text-brand-600 focus:ring-brand-500"
                  />
                </td>

                <td className="relative px-3 py-2">
                  <div className="flex items-center gap-2">
                    {iconFor(entry)}
                    {entry.type === 'directory' || entry.type === 'file' ? (
                      <button
                        type="button"
                        onClick={(event) => {
                          setAnchor(event.currentTarget);
                          onOpen(entry);
                        }}
                        aria-haspopup={entry.type === 'file' ? 'menu' : undefined}
                        aria-expanded={entry.type === 'file' ? menuFor === entry.path : undefined}
                        className={`truncate rounded-sm text-left font-medium text-slate-800 underline-offset-2 hover:text-brand-700 hover:underline ${focusRingTight}`}
                      >
                        {entry.name}
                      </button>
                    ) : (
                      <span className="truncate text-slate-600">{entry.name}</span>
                    )}

                    {entry.type === 'symlink' && (
                      <span
                        className={[
                          'shrink-0 rounded px-1.5 py-0.5 text-[11px]',
                          dangling ? 'bg-warn-100 text-warn-800' : 'bg-slate-100 text-slate-600',
                        ].join(' ')}
                        title={entry.target ?? ''}
                      >
                        {dangling ? 'link (outside)' : 'link'}
                      </span>
                    )}
                  </div>

                  {menuFor === entry.path && anchor && (
                    <FileActionMenu
                      entry={entry}
                      anchor={anchor}
                      canWrite={canWrite}
                      onSelect={onMenuAction}
                      onClose={onMenuClose}
                    />
                  )}
                </td>

                <td className="px-3 py-2 tabular-nums text-slate-600">
                  {formatSize(entry.size, entry.type)}
                </td>
                <td className="px-3 py-2 text-slate-600">{formatModified(entry.modified)}</td>
                <td className="px-3 py-2">
                  <span
                    className={[
                      'font-mono text-xs',
                      // World-writable inside a document root is almost always
                      // a mistake, and the one a person most needs to spot.
                      isWorldWritable(entry.mode) ? 'text-danger-600' : 'text-slate-600',
                    ].join(' ')}
                    title={isWorldWritable(entry.mode) ? 'Writable by anyone on this host' : undefined}
                  >
                    {entry.mode} {describeMode(entry.mode)}
                  </span>
                </td>
                <td className="px-3 py-2 text-slate-600">
                  {entry.owner}:{entry.group}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
