import type { ReactNode } from 'react';
import { AlertTriangle, MinusCircle } from 'lucide-react';

import type { Widget } from '@/types/api';

interface WidgetCardProps<T> {
  title: string;
  widget: Widget<T> | undefined;
  /** Rendered in the header, beside the title. */
  action?: ReactNode;
  children: (data: T) => ReactNode;
}

/**
 * WidgetCard renders one dashboard panel, or why it is empty.
 *
 * The three empty states are shown differently on purpose: "loading" is
 * transient, "unavailable" means something is wrong and worth investigating,
 * and "unsupported" means the host simply cannot answer — telling an operator
 * to fix that would send them chasing a non-problem.
 */
export function WidgetCard<T>({ title, widget, action, children }: WidgetCardProps<T>) {
  return (
    <section
      aria-labelledby={`widget-${slug(title)}`}
      className="rounded-lg border border-surface-border bg-surface p-5"
    >
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 id={`widget-${slug(title)}`} className="text-sm font-semibold text-slate-900">
          {title}
        </h2>
        {action}
      </div>

      {renderBody(widget, children)}
    </section>
  );
}

function renderBody<T>(widget: Widget<T> | undefined, children: (data: T) => ReactNode) {
  if (!widget) {
    return <p className="text-sm text-slate-400">Loading…</p>;
  }

  if (widget.unsupported && !widget.data) {
    return (
      <p className="flex items-center gap-2 text-sm text-slate-500">
        <MinusCircle aria-hidden="true" className="h-4 w-4 shrink-0" />
        {widget.error ?? 'Not available on this host'}
      </p>
    );
  }

  if (!widget.available || widget.data === undefined) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-amber-700">
        <AlertTriangle aria-hidden="true" className="h-4 w-4 shrink-0" />
        {widget.error ?? 'Could not be collected'}
      </p>
    );
  }

  return <>{children(widget.data)}</>;
}

function slug(title: string): string {
  return title.toLowerCase().replace(/[^a-z0-9]+/g, '-');
}
