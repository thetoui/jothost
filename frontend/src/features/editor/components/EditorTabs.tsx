import { X } from 'lucide-react';

import { isDirty, type EditorTab } from '@/stores/editorStore';

interface EditorTabsProps {
  tabs: EditorTab[];
  activePath: string | null;
  onActivate: (path: string) => void;
  onClose: (tab: EditorTab) => void;
}

/** EditorTabs shows the open files and which of them have unsaved changes. */
export function EditorTabs({ tabs, activePath, onActivate, onClose }: EditorTabsProps) {
  if (tabs.length === 0) {
    return null;
  }

  return (
    <div
      role="tablist"
      aria-label="Open files"
      className="flex items-stretch overflow-x-auto border-b border-surface-border bg-surface-sunken"
    >
      {tabs.map((tab) => {
        const active = tab.path === activePath;
        const dirty = isDirty(tab);

        return (
          <div
            key={tab.path}
            className={[
              'group flex shrink-0 items-center gap-1.5 border-r border-surface-border px-3 py-1.5 text-sm',
              active ? 'bg-surface text-slate-900' : 'text-slate-600 hover:bg-surface/60',
            ].join(' ')}
          >
            <button
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => onActivate(tab.path)}
              title={tab.path}
              className="max-w-[14rem] truncate"
            >
              {tab.name}
              {/* The marker is text, not only a colour: "which of these has
                  unsaved work" must survive a colour-blind reader. */}
              {dirty && (
                <span className="ml-1 text-brand-600" aria-label="unsaved changes">
                  •
                </span>
              )}
            </button>

            <button
              type="button"
              onClick={() => onClose(tab)}
              aria-label={`Close ${tab.name}${dirty ? ' (unsaved changes)' : ''}`}
              className="rounded p-0.5 text-slate-400 opacity-0 transition-opacity hover:bg-surface-border hover:text-slate-700 focus:opacity-100 group-hover:opacity-100"
            >
              <X aria-hidden="true" className="h-3.5 w-3.5" />
            </button>
          </div>
        );
      })}
    </div>
  );
}
