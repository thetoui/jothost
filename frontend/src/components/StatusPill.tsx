interface StatusPillProps {
  label: string;
  tone: 'ok' | 'warn' | 'error' | 'neutral';
}

const toneClasses: Record<StatusPillProps['tone'], string> = {
  ok: 'bg-emerald-50 text-emerald-700 ring-emerald-600/20',
  warn: 'bg-amber-50 text-amber-700 ring-amber-600/20',
  error: 'bg-rose-50 text-rose-700 ring-rose-600/20',
  neutral: 'bg-slate-100 text-slate-600 ring-slate-500/20',
};

/** Small status badge reused across the panel. */
export function StatusPill({ label, tone }: StatusPillProps) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset ${toneClasses[tone]}`}
    >
      {label}
    </span>
  );
}
