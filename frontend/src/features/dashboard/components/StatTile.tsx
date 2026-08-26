import type { ReactNode } from 'react';

import { Card } from '@/components/ui/Card';
import { ProgressBar, Skeleton } from '@/components/ui/Loading';
import { TintedIcon } from '@/components/ui/Card';

export type TileTone = 'brand' | 'ok' | 'warn' | 'danger' | 'neutral';

interface StatTileProps {
  label: string;
  /** The headline figure. Undefined renders a placeholder. */
  value: string | undefined;
  detail?: string | undefined;
  icon: ReactNode;
  tone?: TileTone;
  /** 0–100. Adds a bar under the figure. */
  percent?: number | undefined;
  /** Shown when the reading could not be collected. */
  unavailable?: string | undefined;
}

/**
 * StatTile is one headline number in the dashboard's top row.
 *
 * The row exists so the state of the server is legible in one glance, before
 * any of the detail panels are read. Each tile therefore carries exactly one
 * figure — a tile with two numbers in it is a panel, not a tile.
 */
export function StatTile({
  label,
  value,
  detail,
  icon,
  tone = 'neutral',
  percent,
  unavailable,
}: StatTileProps) {
  return (
    <Card className="p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-xs font-medium uppercase tracking-wide text-slate-400">{label}</p>

          {unavailable ? (
            <p className="mt-1.5 text-sm text-slate-400">{unavailable}</p>
          ) : value === undefined ? (
            <Skeleton className="mt-2 h-6 w-20" />
          ) : (
            <p className="mt-1 truncate text-2xl font-semibold tabular-nums text-slate-900">
              {value}
            </p>
          )}

          {detail && !unavailable && (
            <p className="mt-0.5 truncate text-xs text-slate-500">{detail}</p>
          )}
        </div>

        <TintedIcon tone={tone} icon={icon} />
      </div>

      {typeof percent === 'number' && !unavailable && (
        <ProgressBar value={percent} tone={tone === 'neutral' ? 'brand' : tone} className="mt-3" />
      )}
    </Card>
  );
}
