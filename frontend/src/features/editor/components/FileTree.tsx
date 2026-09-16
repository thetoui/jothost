import { useCallback, useState } from 'react';
import { ChevronDown, ChevronRight, File as FileIcon, Folder, FolderOpen } from 'lucide-react';

import { Spinner } from '@/components/ui/Loading';
import { focusRingTight } from '@/components/ui/focus';
import { useDirectory } from '@/features/files/hooks';
import type { FileEntry } from '@/types/api';

interface FileTreeProps {
  root: string;
  /** Path of the file currently in the editor, highlighted in the tree. */
  activePath: string | null;
  onOpen: (entry: FileEntry) => void;
}

/**
 * FileTree lists a directory and expands folders in place.
 *
 * Each level fetches only when it is opened. A document root can hold tens of
 * thousands of files, and loading the whole tree to show the top of it would
 * cost a request per directory before anything appeared on screen.
 */
export function FileTree({ root, activePath, onOpen }: FileTreeProps) {
  return (
    <div role="tree" aria-label="Files" className="text-sm">
      <TreeLevel path={root} depth={0} activePath={activePath} onOpen={onOpen} defaultOpen />
    </div>
  );
}

interface TreeLevelProps {
  path: string;
  depth: number;
  activePath: string | null;
  onOpen: (entry: FileEntry) => void;
  defaultOpen?: boolean;
}

function TreeLevel({ path, depth, activePath, onOpen, defaultOpen = false }: TreeLevelProps) {
  const [open, setOpen] = useState(defaultOpen);
  // Only fetch once the level is actually visible.
  const listing = useDirectory(open ? path : '', 0, 500);

  const toggle = useCallback(() => setOpen((current) => !current), []);

  if (!open) {
    return null;
  }
  if (listing.isLoading) {
    return (
      <div className="flex items-center gap-2 py-1 text-ink-muted" style={indent(depth)}>
        <Spinner className="h-3.5 w-3.5" />
        <span className="text-xs">Loading…</span>
      </div>
    );
  }
  if (listing.isError) {
    return (
      <p className="py-1 text-xs text-danger-600" style={indent(depth)}>
        This folder could not be read.
      </p>
    );
  }

  const entries = listing.data?.entries ?? [];
  if (entries.length === 0) {
    return (
      <p className="py-1 text-xs text-ink-dim" style={indent(depth)}>
        Empty
      </p>
    );
  }

  return (
    <ul role="group" className="list-none">
      {entries.map((entry) => (
        <TreeNode
          key={entry.path}
          entry={entry}
          depth={depth}
          activePath={activePath}
          onOpen={onOpen}
        />
      ))}
      {listing.data?.truncated && (
        <li className="py-1 text-xs text-ink-dim" style={indent(depth)}>
          Showing the first {entries.length}. Use the file manager to see the rest.
        </li>
      )}
      <li className="hidden">{toggle.name}</li>
    </ul>
  );
}

interface TreeNodeProps {
  entry: FileEntry;
  depth: number;
  activePath: string | null;
  onOpen: (entry: FileEntry) => void;
}

function TreeNode({ entry, depth, activePath, onOpen }: TreeNodeProps) {
  const [expanded, setExpanded] = useState(false);
  const isDirectory = entry.type === 'directory';
  const isActive = entry.path === activePath;

  // A symlink out of the root, a device or a socket cannot be opened at all;
  // showing them as clickable would promise something the panel will refuse.
  const openable =
    entry.type === 'file' || (entry.type === 'symlink' && entry.target_inside_root === true);

  return (
    <li role="none">
      <div
        role="treeitem"
        aria-selected={isActive}
        aria-expanded={isDirectory ? expanded : undefined}
      >
        <button
          type="button"
          onClick={() => {
            if (isDirectory) {
              setExpanded((current) => !current);
              return;
            }
            if (openable) {
              onOpen(entry);
            }
          }}
          disabled={!isDirectory && !openable}
          title={entry.editable === false && entry.type === 'file' ? 'Too large to edit' : entry.path}
          className={[
            'flex w-full items-center gap-1.5 rounded px-2 py-1 text-left transition-colors',
            focusRingTight,
            isActive ? 'bg-brand-50 font-medium text-brand-800' : 'text-ink',
            openable || isDirectory ? 'hover:bg-surface-sunken' : 'cursor-not-allowed opacity-50',
          ].join(' ')}
          style={indent(depth)}
        >
          {isDirectory ? (
            <>
              {expanded ? (
                <ChevronDown aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-ink-dim" />
              ) : (
                <ChevronRight aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-ink-dim" />
              )}
              {expanded ? (
                <FolderOpen aria-hidden="true" className="h-4 w-4 shrink-0 text-brand-500" />
              ) : (
                <Folder aria-hidden="true" className="h-4 w-4 shrink-0 text-brand-500" />
              )}
            </>
          ) : (
            <>
              <span className="w-3.5 shrink-0" aria-hidden="true" />
              <FileIcon aria-hidden="true" className="h-4 w-4 shrink-0 text-ink-dim" />
            </>
          )}
          <span className="truncate">{entry.name}</span>
        </button>
      </div>

      {isDirectory && expanded && (
        <TreeLevel
          path={entry.path}
          depth={depth + 1}
          activePath={activePath}
          onOpen={onOpen}
          defaultOpen
        />
      )}
    </li>
  );
}

/** indent shifts a level right without nesting padding containers. */
function indent(depth: number) {
  return { paddingLeft: `${0.5 + depth * 0.85}rem` };
}
