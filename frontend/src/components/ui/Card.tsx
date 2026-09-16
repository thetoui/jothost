import type { ReactNode } from 'react';

interface CardProps {
  children: ReactNode;
  className?: string;
  /** Labels the section for screen readers when there is no visible title. */
  label?: string;
}

/** Card is the standard content panel: one idea per card. */
export function Card({ children, className = '', label }: CardProps) {
  return (
    <section
      aria-label={label}
      className={`rounded-card border border-surface-border bg-surface shadow-card ${className}`}
    >
      {children}
    </section>
  );
}

interface CardHeaderProps {
  title: ReactNode;
  description?: ReactNode;
  /** Rendered at the right of the header row. */
  action?: ReactNode;
  /** Rendered before the title, typically a small tinted icon. */
  icon?: ReactNode;
}

/** CardHeader is the titled strip at the top of a Card. */
export function CardHeader({ title, description, action, icon }: CardHeaderProps) {
  return (
    <div className="flex items-start justify-between gap-3 border-b border-surface-border px-5 py-3.5">
      <div className="flex min-w-0 items-start gap-3">
        {icon}
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold text-ink-strong">{title}</h2>
          {description && <p className="mt-0.5 text-xs text-ink-muted">{description}</p>}
        </div>
      </div>
      {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
    </div>
  );
}

/** CardBody is the padded content region of a Card. */
export function CardBody({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`p-5 ${className}`}>{children}</div>;
}

/** CardFooter holds a card's actions, separated from its content. */
export function CardFooter({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={`flex flex-wrap items-center gap-2 border-t border-surface-border bg-surface-muted px-5 py-3 ${className}`}
    >
      {children}
    </div>
  );
}

interface TintedIconProps {
  icon: ReactNode;
  tone?: 'brand' | 'ok' | 'warn' | 'danger' | 'neutral';
}

const tintClasses: Record<NonNullable<TintedIconProps['tone']>, string> = {
  brand: 'bg-brand-50 text-brand-600',
  ok: 'bg-ok-50 text-ok-600',
  warn: 'bg-warn-50 text-warn-600',
  danger: 'bg-danger-50 text-danger-600',
  neutral: 'bg-surface-sunken text-ink-muted',
};

/** TintedIcon is the small coloured square that fronts a card or list row. */
export function TintedIcon({ icon, tone = 'neutral' }: TintedIconProps) {
  return (
    <span
      aria-hidden="true"
      className={`grid h-8 w-8 shrink-0 place-items-center rounded-md ${tintClasses[tone]}`}
    >
      {icon}
    </span>
  );
}
