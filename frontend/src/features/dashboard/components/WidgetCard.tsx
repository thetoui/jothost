import type { ReactNode } from 'react';
import { AlertTriangle, MinusCircle } from 'lucide-react';

import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SkeletonText } from '@/components/ui/Loading';
import type { Widget } from '@/types/api';

interface WidgetCardProps<T> {
  title: string;
  widget: Widget<T> | undefined;
  /** Rendered in the header, beside the title. */
  action?: ReactNode;
  /** A small tinted icon fronting the title. */
  icon?: ReactNode;
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
export function WidgetCard<T>({ title, widget, action, icon, children }: WidgetCardProps<T>) {
  return (
    // Labelled with the title so the panel stays a named region: a card whose
    // heading is only visual is unreachable by a screen reader's landmark list.
    <Card label={title}>
      <CardHeader
        title={title}
        action={action}
        {...(icon ? { icon: <TintedIcon icon={icon} /> } : {})}
      />
      <CardBody>{renderBody(widget, children)}</CardBody>
    </Card>
  );
}

function renderBody<T>(widget: Widget<T> | undefined, children: (data: T) => ReactNode) {
  if (!widget) {
    // Shaped like the content that is coming, so the card does not resize when
    // it arrives.
    return <SkeletonText lines={3} />;
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
      <p role="status" className="flex items-center gap-2 text-sm text-warn-700">
        <AlertTriangle aria-hidden="true" className="h-4 w-4 shrink-0" />
        {widget.error ?? 'Could not be collected'}
      </p>
    );
  }

  return <>{children(widget.data)}</>;
}
