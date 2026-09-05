import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Download, FileCode, Pencil, Shield, Trash2 } from 'lucide-react';

import { MenuItem } from '@/components/ui/Menu';
import type { FileEntry } from '@/types/api';

export type FileAction = 'editor' | 'download' | 'rename' | 'permissions' | 'delete';

interface FileActionMenuProps {
  entry: FileEntry;
  /** The control the menu belongs to, used to position it. */
  anchor: HTMLElement;
  /** Whether the current user may change anything. */
  canWrite: boolean;
  onSelect: (action: FileAction, entry: FileEntry) => void;
  onClose: () => void;
}

interface MenuItem {
  action: FileAction;
  label: string;
  icon: typeof Download;
  /** Destructive items are separated and coloured. */
  destructive?: boolean | undefined;
  /** Why the item is unavailable, shown instead of hiding it. */
  disabledReason?: string | undefined;
}

/** Roughly the menu's own height, used to decide whether it opens upward. */
const ESTIMATED_HEIGHT = 190;
const WIDTH = 224;

/**
 * FileActionMenu is what opens when a file is clicked.
 *
 * Clicking a file used to download it immediately, which is the wrong default:
 * download is one of five things someone might want, and it is the only one
 * that cannot be undone by closing a dialog. A menu makes the choice explicit,
 * and puts the editor where someone looking at a file would reach for it.
 *
 * It renders in a portal because the table it belongs to scrolls horizontally,
 * and a popover inside an `overflow: auto` container is clipped by it — which
 * showed up as a menu with only its first item visible.
 */
export function FileActionMenu({
  entry,
  anchor,
  canWrite,
  onSelect,
  onClose,
}: FileActionMenuProps) {
  const container = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null);

  // Positioned before paint so the menu never appears in the wrong place first.
  useLayoutEffect(() => {
    const rect = anchor.getBoundingClientRect();
    const openUpward = rect.bottom + ESTIMATED_HEIGHT > window.innerHeight;

    setPosition({
      top: openUpward ? Math.max(8, rect.top - ESTIMATED_HEIGHT) : rect.bottom + 4,
      // Kept on screen when the row is near the right edge.
      left: Math.min(rect.left, window.innerWidth - WIDTH - 8),
    });
  }, [anchor]);

  // Close on Escape and on a click anywhere else, so the menu never strands
  // someone with a popover they cannot dismiss.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
    };
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node;
      if (container.current?.contains(target) || anchor.contains(target)) {
        return;
      }
      onClose();
    };
    // A menu positioned against a row cannot follow it, so scrolling closes it
    // rather than leaving it floating over unrelated rows.
    const onScroll = () => onClose();

    document.addEventListener('keydown', onKeyDown);
    document.addEventListener('mousedown', onPointerDown);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onScroll);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.removeEventListener('mousedown', onPointerDown);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onScroll);
    };
  }, [onClose, anchor]);

  // Move focus into the menu so a keyboard user is not left behind on the row.
  useEffect(() => {
    container.current?.querySelector<HTMLButtonElement>('button:not([disabled])')?.focus();
  }, [position]);

  const isFile = entry.type === 'file';

  const items: MenuItem[] = [
    {
      action: 'editor',
      label: 'Open in editor',
      icon: FileCode,
      disabledReason: !isFile
        ? 'Only a file can be edited'
        : entry.editable === false
          ? 'Too large to edit — download it instead'
          : undefined,
    },
    {
      action: 'download',
      label: 'Download',
      icon: Download,
      disabledReason: isFile ? undefined : 'Only a file can be downloaded',
    },
    {
      action: 'rename',
      label: 'Rename',
      icon: Pencil,
      disabledReason: canWrite ? undefined : 'Needs the file.write permission',
    },
    {
      action: 'permissions',
      label: 'Permissions',
      icon: Shield,
      disabledReason: canWrite ? undefined : 'Needs the file.write permission',
    },
    {
      action: 'delete',
      label: 'Delete',
      icon: Trash2,
      destructive: true,
      disabledReason: canWrite ? undefined : 'Needs the file.write permission',
    },
  ];

  const menu = (
    <div
      ref={container}
      role="menu"
      aria-label={`Actions for ${entry.name}`}
      style={{
        position: 'fixed',
        top: position?.top ?? -9999,
        left: position?.left ?? -9999,
        width: WIDTH,
      }}
      className="z-50 overflow-hidden rounded-md border border-surface-border bg-surface p-1 shadow-menu"
    >
      {items.map((item) => {
        const Icon = item.icon;
        const disabled = item.disabledReason !== undefined;

        return (
          <MenuItem
            key={item.action}
            tone={item.destructive ? 'danger' : 'neutral'}
            separated={item.destructive ?? false}
            disabled={disabled}
            {...(item.disabledReason !== undefined ? { title: item.disabledReason } : {})}
            onClick={() => onSelect(item.action, entry)}
            icon={<Icon className="h-4 w-4" />}
          >
            {item.label}
          </MenuItem>
        );
      })}
    </div>
  );

  return createPortal(menu, document.body);
}
