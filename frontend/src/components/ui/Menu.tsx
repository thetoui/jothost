import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

import { focusRingTight } from '@/components/ui/focus';

/**
 * A row in a dropdown menu.
 *
 * Menus in this panel mix destinations and actions — "Account security" is a
 * link, "Sign out" is a request — and both were written by hand, so the same
 * row was styled twice and the two copies had already begun to differ. Here
 * the shape is decided once and the element is chosen by which props arrive.
 */

const rowClasses = [
  'flex w-full items-center gap-2.5 rounded px-2.5 py-2 text-left text-sm transition-colors',
  'disabled:cursor-not-allowed disabled:opacity-60',
  focusRingTight,
].join(' ');

const toneClasses = {
  neutral: 'text-ink hover:bg-surface-sunken',
  danger: 'text-danger-700 hover:bg-danger-50',
} as const;

interface MenuItemProps {
  /** Rendered before the label, greyed to match its neighbours. */
  icon?: ReactNode;
  children: ReactNode;
  tone?: keyof typeof toneClasses;
  /** A destination. Mutually exclusive with `onClick`. */
  to?: string;
  onClick?: () => void;
  disabled?: boolean;
  /** Native tooltip — used to say *why* a row is disabled. */
  title?: string;
  /** Draws a rule above the row, separating a destructive action from the rest. */
  separated?: boolean;
}

export function MenuItem({
  icon,
  children,
  tone = 'neutral',
  to,
  onClick,
  disabled = false,
  title,
  separated = false,
}: MenuItemProps) {
  const className = [
    rowClasses,
    toneClasses[tone],
    separated ? 'mt-1 border-t border-surface-border pt-2' : '',
  ].join(' ');
  const body = (
    <>
      {icon && (
        <span aria-hidden="true" className="shrink-0 text-ink-dim">
          {icon}
        </span>
      )}
      <span className="truncate">{children}</span>
    </>
  );

  if (to) {
    return (
      // tabIndex -1 so the menu is arrowed through rather than tabbed through,
      // which is what role="menu" promises a screen reader.
      <Link to={to} role="menuitem" tabIndex={-1} onClick={onClick} title={title} className={className}>
        {body}
      </Link>
    );
  }

  return (
    <button
      type="button"
      role="menuitem"
      tabIndex={-1}
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={className}
    >
      {body}
    </button>
  );
}

/** The floating panel a set of MenuItems sits in. */
export function MenuPanel({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div
      role="menu"
      aria-label={label}
      className="absolute right-0 top-full z-40 mt-1.5 w-56 animate-fade-in overflow-hidden rounded-card border border-surface-border bg-surface shadow-menu"
    >
      {children}
    </div>
  );
}
