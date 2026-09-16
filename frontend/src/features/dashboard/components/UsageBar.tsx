import { usageTone } from '@/features/dashboard/format';

interface UsageBarProps {
  /** 0-100. */
  percent: number;
  label: string;
  /** Rendered on the right of the label row, e.g. "4.2 GB of 16 GB". */
  detail?: string;
}

const toneClasses = {
  ok: 'bg-ok-500',
  warn: 'bg-warn-500',
  error: 'bg-danger-500',
} as const;

/**
 * UsageBar shows a percentage as a labelled bar.
 *
 * The bar carries progressbar semantics so a screen reader announces the value
 * rather than describing a decorative div, and the numeric value is also
 * printed as text — colour alone must never be the only signal.
 */
export function UsageBar({ percent, label, detail }: UsageBarProps) {
  const clamped = Math.min(100, Math.max(0, percent));
  const tone = usageTone(clamped);

  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <span className="text-xs font-medium text-ink">{label}</span>
        <span className="text-xs tabular-nums text-ink">
          {clamped.toFixed(1)}%{detail ? ` · ${detail}` : ''}
        </span>
      </div>

      <div
        role="progressbar"
        aria-label={label}
        aria-valuenow={Math.round(clamped)}
        aria-valuemin={0}
        aria-valuemax={100}
        className="h-2 w-full overflow-hidden rounded-full bg-surface-muted"
      >
        <div
          className={`h-full rounded-full transition-all ${toneClasses[tone]}`}
          style={{ width: `${clamped}%` }}
        />
      </div>
    </div>
  );
}
