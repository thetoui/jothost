import type { ReactNode } from 'react';
import { Loader2 } from 'lucide-react';

/**
 * Skeleton is a placeholder shaped like the content that is coming.
 *
 * It is used instead of a spinner wherever the shape of the result is known in
 * advance, because a skeleton tells the reader what to expect and holds the
 * layout still — a spinner that is replaced by content makes the page jump.
 */
export function Skeleton({ className = '' }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`relative block overflow-hidden rounded bg-surface-sunken ${className}`}
    >
      <span className="absolute inset-0 -translate-x-full animate-shimmer bg-gradient-to-r from-transparent via-white/70 to-transparent" />
    </span>
  );
}

interface SkeletonTextProps {
  lines?: number;
  className?: string;
}

/** SkeletonText stands in for a paragraph. */
export function SkeletonText({ lines = 3, className = '' }: SkeletonTextProps) {
  return (
    <div className={`space-y-2 ${className}`}>
      {Array.from({ length: lines }, (_, index) => (
        <Skeleton
          key={index}
          // The last line is short, the way a real paragraph ends.
          className={`h-3 ${index === lines - 1 ? 'w-2/5' : 'w-full'}`}
        />
      ))}
    </div>
  );
}

/** SkeletonRows stands in for a table or list that is loading. */
export function SkeletonRows({ rows = 4, className = '' }: { rows?: number; className?: string }) {
  return (
    <div className={`divide-y divide-surface-border ${className}`} role="status" aria-live="polite">
      <span className="sr-only">Loading</span>
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="flex items-center gap-3 px-5 py-3.5">
          <Skeleton className="h-8 w-8 shrink-0 rounded-md" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton className="h-3 w-1/3" />
            <Skeleton className="h-2.5 w-1/2" />
          </div>
          <Skeleton className="h-5 w-16 shrink-0 rounded-full" />
        </div>
      ))}
    </div>
  );
}

interface SpinnerProps {
  className?: string;
  /** Announced to screen readers; the spinner itself is decorative. */
  label?: string;
}

/** Spinner is for waits whose result has no predictable shape. */
export function Spinner({ className = 'h-5 w-5', label }: SpinnerProps) {
  return (
    <>
      <Loader2 aria-hidden="true" className={`animate-spin text-slate-400 ${className}`} />
      {label && <span className="sr-only">{label}</span>}
    </>
  );
}

interface ProgressBarProps {
  /** 0–100. Omit for work whose duration is unknown. */
  value?: number;
  label?: string;
  tone?: 'brand' | 'ok' | 'warn' | 'danger';
  className?: string;
}

const progressTones: Record<NonNullable<ProgressBarProps['tone']>, string> = {
  brand: 'bg-brand-500',
  ok: 'bg-ok-500',
  warn: 'bg-warn-500',
  danger: 'bg-danger-500',
};

/**
 * ProgressBar reports how far along a job is.
 *
 * Without a value it animates indeterminately: the panel dispatches work to a
 * host that does not always report progress, and a bar frozen at 0% reads as
 * "stuck" when the truth is "running, and it cannot say how far".
 */
export function ProgressBar({ value, label, tone = 'brand', className = '' }: ProgressBarProps) {
  const determinate = typeof value === 'number' && Number.isFinite(value);
  const clamped = determinate ? Math.min(100, Math.max(0, value)) : 0;

  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={determinate ? 0 : undefined}
      aria-valuemax={determinate ? 100 : undefined}
      aria-valuenow={determinate ? Math.round(clamped) : undefined}
      className={`h-1.5 overflow-hidden rounded-full bg-surface-sunken ${className}`}
    >
      {determinate ? (
        <div
          className={`h-full rounded-full transition-[width] duration-500 ease-out ${progressTones[tone]}`}
          style={{ width: `${clamped}%` }}
        />
      ) : (
        <div className={`h-full w-full animate-indeterminate rounded-full ${progressTones[tone]}`} />
      )}
    </div>
  );
}

interface EmptyStateProps {
  icon: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
}

/** EmptyState explains an empty list rather than showing nothing. */
export function EmptyState({ icon, title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center px-6 py-12 text-center">
      <span
        aria-hidden="true"
        className="grid h-12 w-12 place-items-center rounded-full bg-surface-sunken text-slate-400"
      >
        {icon}
      </span>
      <p className="mt-3 text-sm font-semibold text-slate-900">{title}</p>
      {description && <p className="mt-1 max-w-sm text-sm text-slate-500">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
