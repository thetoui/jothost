import { useRef, type KeyboardEvent, type ReactNode } from 'react';

import { focusRingTight } from '@/components/ui/focus';

export interface TabItem<T extends string> {
  value: T;
  label: ReactNode;
  /** Shown after the label, greyed. Omit rather than passing 0 for "none". */
  count?: number;
}

interface TabsProps<T extends string> {
  items: readonly TabItem<T>[];
  value: T;
  onChange: (value: T) => void;
  /** Names the tablist, e.g. "Databases sections". Required by ARIA. */
  label: string;
  className?: string;
}

/**
 * Tabs switches between sections of one page.
 *
 * There were three of these before this component: an underline set on the
 * databases page, a slightly different underline set on the website detail
 * panel, and a pill set on the tenancy page. Same job, three visual languages
 * — and a reader who learned one of them learned nothing about the next.
 *
 * The underline form won because it is the one that survives a page whose tabs
 * sit directly above a table: pills float, and an underline draws the line the
 * table already needed.
 *
 * All three also declared `role="tablist"` without implementing what that
 * promises. A tablist is a single stop in the tab order, moved through with
 * the arrow keys; a set of plain buttons wearing the role tells a screen
 * reader user to press Left and Right, and then does nothing when they do.
 * That is worse than no role at all, so the keyboard behaviour lives here.
 */
export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
  className = '',
}: TabsProps<T>) {
  const listRef = useRef<HTMLDivElement>(null);

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const index = items.findIndex((item) => item.value === value);
    if (index < 0) {
      return;
    }

    let next = index;
    switch (event.key) {
      case 'ArrowLeft':
        next = (index - 1 + items.length) % items.length;
        break;
      case 'ArrowRight':
        next = (index + 1) % items.length;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = items.length - 1;
        break;
      default:
        return;
    }

    event.preventDefault();
    const target = items[next];
    if (!target) {
      return;
    }
    onChange(target.value);
    // Selection follows focus, so focus has to follow selection too, or the
    // next arrow press starts from wherever the browser left the tab stop.
    listRef.current
      ?.querySelector<HTMLButtonElement>(`[data-tab-value="${target.value}"]`)
      ?.focus();
  }

  return (
    <div
      ref={listRef}
      role="tablist"
      aria-label={label}
      onKeyDown={handleKeyDown}
      className={`flex gap-5 border-b border-surface-border ${className}`}
    >
      {items.map((item) => {
        const active = item.value === value;
        return (
          <button
            key={item.value}
            type="button"
            role="tab"
            aria-selected={active}
            data-tab-value={item.value}
            // Exactly one tab is reachable by Tab; the rest by arrow keys.
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(item.value)}
            className={[
              '-mb-px shrink-0 border-b-2 px-0.5 pb-2 text-sm transition-colors',
              focusRingTight,
              active
                ? 'border-brand-600 font-medium text-slate-900'
                : 'border-transparent text-slate-500 hover:border-surface-strong hover:text-slate-800',
            ].join(' ')}
          >
            {item.label}
            {item.count !== undefined && (
              <span className="ml-1.5 text-xs text-slate-400">{item.count}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
