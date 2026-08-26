export type StatusTone = 'ok' | 'warn' | 'error' | 'neutral' | 'info';

interface StatusPillProps {
  label: string;
  tone: StatusTone;
  /** Adds a leading dot, and animates it while work is in flight. */
  dot?: boolean;
  pulse?: boolean;
}

const toneClasses: Record<StatusTone, string> = {
  ok: 'bg-ok-50 text-ok-700 ring-ok-600/20',
  warn: 'bg-warn-50 text-warn-700 ring-warn-600/20',
  error: 'bg-danger-50 text-danger-700 ring-danger-600/20',
  info: 'bg-brand-50 text-brand-700 ring-brand-600/20',
  neutral: 'bg-surface-sunken text-slate-600 ring-slate-500/20',
};

const dotClasses: Record<StatusTone, string> = {
  ok: 'bg-ok-500',
  warn: 'bg-warn-500',
  error: 'bg-danger-500',
  info: 'bg-brand-500',
  neutral: 'bg-slate-400',
};

/** Small status badge reused across the panel. */
export function StatusPill({ label, tone, dot = false, pulse = false }: StatusPillProps) {
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset ${toneClasses[tone]}`}
    >
      {dot && (
        <span aria-hidden="true" className="relative flex h-1.5 w-1.5">
          {/* The ping marks work that is still moving, so a row mid-change is
              distinguishable from one that has settled. */}
          {pulse && (
            <span
              className={`absolute inline-flex h-full w-full animate-ping rounded-full opacity-70 ${dotClasses[tone]}`}
            />
          )}
          <span className={`relative inline-flex h-1.5 w-1.5 rounded-full ${dotClasses[tone]}`} />
        </span>
      )}
      {label}
    </span>
  );
}
