import type { ReactNode } from 'react';
import { AlertTriangle, CheckCircle2, Info, XCircle } from 'lucide-react';

export type AlertTone = 'info' | 'success' | 'warning' | 'danger';

const toneClasses: Record<AlertTone, { wrapper: string; icon: ReactNode }> = {
  info: {
    wrapper: 'border-brand-100 bg-brand-50 text-brand-800',
    icon: <Info aria-hidden="true" className="h-4 w-4 shrink-0 text-brand-600" />,
  },
  success: {
    wrapper: 'border-ok-100 bg-ok-50 text-ok-700',
    icon: <CheckCircle2 aria-hidden="true" className="h-4 w-4 shrink-0 text-ok-600" />,
  },
  warning: {
    wrapper: 'border-warn-100 bg-warn-50 text-warn-700',
    icon: <AlertTriangle aria-hidden="true" className="h-4 w-4 shrink-0 text-warn-600" />,
  },
  danger: {
    wrapper: 'border-danger-100 bg-danger-50 text-danger-700',
    icon: <XCircle aria-hidden="true" className="h-4 w-4 shrink-0 text-danger-600" />,
  },
};

interface AlertProps {
  tone?: AlertTone;
  title?: string;
  children: ReactNode;
  action?: ReactNode;
  className?: string;
}

/**
 * Alert states something the reader needs to know, inline where it applies.
 *
 * Danger and warning carry role="alert" so a screen reader announces them;
 * info and success do not, because an interruption is not warranted for
 * something the user just did on purpose.
 */
export function Alert({ tone = 'info', title, children, action, className = '' }: AlertProps) {
  const { wrapper, icon } = toneClasses[tone];
  const assertive = tone === 'danger' || tone === 'warning';

  return (
    <div
      role={assertive ? 'alert' : 'status'}
      className={`flex items-start gap-2.5 rounded-md border px-3.5 py-2.5 text-sm ${wrapper} ${className}`}
    >
      <span className="mt-0.5">{icon}</span>
      <div className="min-w-0 flex-1">
        {title && <p className="font-medium">{title}</p>}
        <div className={title ? 'mt-0.5' : undefined}>{children}</div>
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}
