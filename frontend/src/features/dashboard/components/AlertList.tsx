import { AlertOctagon, AlertTriangle, CheckCircle2 } from 'lucide-react';

import type { DashboardAlert } from '@/types/api';

interface AlertListProps {
  alerts: DashboardAlert[];
}

/**
 * AlertList shows what needs attention.
 *
 * Critical entries sort first so the worst problem is the first thing read,
 * and each carries an icon as well as a colour — colour alone is not a signal
 * every reader receives.
 */
export function AlertList({ alerts }: AlertListProps) {
  if (alerts.length === 0) {
    return (
      <p className="flex items-center gap-2 text-sm text-ok-700">
        <CheckCircle2 aria-hidden="true" className="h-4 w-4 shrink-0" />
        No alerts. Everything looks healthy.
      </p>
    );
  }

  const sorted = [...alerts].sort((a, b) => severityRank(b) - severityRank(a));

  return (
    <ul className="space-y-2">
      {sorted.map((alert, index) => {
        const critical = alert.severity === 'critical';
        const Icon = critical ? AlertOctagon : AlertTriangle;

        return (
          <li
            key={`${alert.category}-${alert.message}-${index}`}
            className={`flex items-start gap-2 rounded-md border px-3 py-2 text-sm ${
              critical
                ? 'border-danger-200 bg-danger-50 text-danger-800'
                : 'border-warn-200 bg-warn-50 text-warn-800'
            }`}
          >
            <Icon aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />
            <span>
              <span className="sr-only">{critical ? 'Critical: ' : 'Warning: '}</span>
              {alert.message}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function severityRank(alert: DashboardAlert): number {
  return alert.severity === 'critical' ? 1 : 0;
}
