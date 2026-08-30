import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

export type ToolTone = 'blue' | 'green' | 'violet' | 'amber' | 'slate' | 'rose';

const toneClasses: Record<ToolTone, string> = {
  blue: 'bg-sky-100 text-sky-700',
  green: 'bg-ok-100 text-ok-700',
  violet: 'bg-violet-100 text-violet-700',
  amber: 'bg-amber-100 text-amber-700',
  slate: 'bg-slate-100 text-slate-600',
  rose: 'bg-rose-100 text-rose-700',
};

interface ToolTileProps {
  icon: ReactNode;
  label: string;
  /** The second line: a version, a state, or what the tool is for. */
  detail?: ReactNode;
  tone?: ToolTone;
  /** Where the tile goes. Omit together with onClick for an inert tile. */
  to?: string;
  /**
   * An address outside the panel, opened in a new tab. Used for a tool the
   * panel installs but does not embed, such as phpMyAdmin.
   */
  href?: string;
  onClick?: () => void;
  /** Marks a tool this build does not have yet, with the phase that adds it. */
  unavailable?: string;
}

/**
 * ToolTile is one entry in a domain's tool grid.
 *
 * A tool the panel does not have yet is shown greyed with the reason rather
 * than hidden. Hiding it makes the panel look complete and leaves someone
 * hunting for a feature; showing it inert says plainly what this build can and
 * cannot do.
 */
export function ToolTile({
  icon,
  label,
  detail,
  tone = 'slate',
  to,
  href,
  onClick,
  unavailable,
}: ToolTileProps) {
  const body = (
    <>
      <span
        className={[
          'flex h-9 w-9 shrink-0 items-center justify-center rounded-md',
          unavailable ? 'bg-slate-100 text-slate-400' : toneClasses[tone],
        ].join(' ')}
        aria-hidden="true"
      >
        {icon}
      </span>
      <span className="min-w-0">
        {/* Labels wrap rather than truncate. A tile reading "Datab..." tells
            nobody anything, and these names are the whole content of the
            control. */}
        <span
          className={[
            'block text-sm font-medium leading-tight',
            unavailable ? 'text-slate-400' : 'text-slate-800',
          ].join(' ')}
        >
          {label}
        </span>
        {(detail || unavailable) && (
          <span className="mt-0.5 block truncate text-xs text-slate-500">
            {unavailable ?? detail}
          </span>
        )}
      </span>
    </>
  );

  const shell =
    'flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors';

  if (unavailable) {
    return (
      <div className={`${shell} cursor-not-allowed`} title={unavailable}>
        {body}
      </div>
    );
  }
  if (to) {
    return (
      <Link to={to} className={`${shell} hover:bg-surface-sunken`}>
        {body}
      </Link>
    );
  }
  if (href) {
    return (
      <a
        href={href}
        target="_blank"
        rel="noreferrer noopener"
        className={`${shell} hover:bg-surface-sunken`}
      >
        {body}
      </a>
    );
  }
  return (
    <button type="button" onClick={onClick} className={`${shell} hover:bg-surface-sunken`}>
      {body}
    </button>
  );
}

interface ToolGroupProps {
  title: string;
  children: ReactNode;
}

/** ToolGroup is a titled three-column band of tiles. */
export function ToolGroup({ title, children }: ToolGroupProps) {
  return (
    <section className="space-y-1">
      <h4 className="text-sm font-semibold text-slate-900">{title}</h4>
      {/* Two columns until there is genuinely room for three. The panel sits
          inside a grid column, so its width is far below the viewport's. */}
      <div className="grid gap-x-4 gap-y-0.5 md:grid-cols-2 2xl:grid-cols-3">{children}</div>
    </section>
  );
}
