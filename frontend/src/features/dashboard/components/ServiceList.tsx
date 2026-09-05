import { CircleDot, CircleSlash, XCircle } from 'lucide-react';

import type { ServiceState } from '@/types/api';

interface ServiceListProps {
  services: ServiceState[];
  /** Set when the host has no service manager, so units could not be read. */
  unsupportedNote?: string;
}

/**
 * ServiceList shows what is running.
 *
 * "Not installed" is rendered as a neutral state rather than a failure: an
 * optional unit that is simply absent is a configuration, and colouring it red
 * would train an operator to ignore the red.
 */
export function ServiceList({ services, unsupportedNote }: ServiceListProps) {
  if (services.length === 0) {
    return <p className="text-sm text-slate-500">{unsupportedNote ?? 'No services monitored.'}</p>;
  }

  return (
    <div className="space-y-3">
      <ul className="divide-y divide-surface-border">
        {services.map((service) => (
          <li key={`${service.kind}-${service.name}`} className="flex items-center justify-between py-2">
            <span className="flex items-center gap-2 text-sm text-slate-800">
              <StatusIcon service={service} />
              {service.name}
            </span>
            <span className="text-xs text-slate-500">{service.status}</span>
          </li>
        ))}
      </ul>

      {unsupportedNote && <p className="text-xs text-slate-500">{unsupportedNote}</p>}
    </div>
  );
}

function StatusIcon({ service }: { service: ServiceState }) {
  if (service.status === 'not installed') {
    return <CircleSlash aria-label="Not installed" className="h-4 w-4 shrink-0 text-slate-400" />;
  }
  if (service.running) {
    return <CircleDot aria-label="Running" className="h-4 w-4 shrink-0 text-ok-600" />;
  }
  return <XCircle aria-label="Not running" className="h-4 w-4 shrink-0 text-danger-600" />;
}
