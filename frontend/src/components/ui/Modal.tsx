import { useEffect, useId, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';

import { Button } from '@/components/ui/Button';

export type ModalSize = 'sm' | 'md' | 'lg';

const sizeClasses: Record<ModalSize, string> = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
};

interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  /** Action buttons, rendered right-aligned in the footer. */
  footer?: ReactNode;
  size?: ModalSize;
  /**
   * Blocks closing by backdrop click or Escape.
   *
   * Used while work is in flight: dismissing a dialog mid-submit leaves the
   * user with no idea whether what they asked for happened.
   */
  busy?: boolean;
}

/**
 * Modal is an accessible dialog rendered in a portal.
 *
 * It is a real dialog rather than window.confirm because a native prompt
 * cannot show what is about to change, cannot be styled, and cannot report
 * progress — and a panel that deletes a website deserves to show which one.
 *
 * Focus is moved into the dialog on open, trapped while it is open, and
 * returned to the trigger on close, so keyboard users are never left behind on
 * a page they cannot see.
 */
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = 'md',
  busy = false,
}: ModalProps) {
  const titleId = useId();
  const descriptionId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);

  // Remember where focus came from, and put it back on close.
  useEffect(() => {
    if (!open) {
      return;
    }
    restoreFocusRef.current = document.activeElement as HTMLElement | null;

    return () => {
      restoreFocusRef.current?.focus?.();
    };
  }, [open]);

  // Move focus into the dialog once it is on screen.
  //
  // A form dialog focuses its first field rather than the close button, which
  // is what the first-focusable rule would otherwise pick: opening "New
  // website" should leave the cursor ready to type a domain, not one Tab away
  // from dismissing the thing that was just opened.
  useEffect(() => {
    if (!open) {
      return;
    }
    const panel = panelRef.current;
    const target =
      panel?.querySelector<HTMLElement>(FIRST_FIELD) ??
      panel?.querySelector<HTMLElement>(FOCUSABLE) ??
      panel;
    target?.focus();
  }, [open]);

  // The page behind must not scroll while a dialog is over it.
  useEffect(() => {
    if (!open) {
      return;
    }
    const previous = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = previous;
    };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab') {
        return;
      }

      // Tab must cycle within the dialog rather than escaping to the page
      // behind it, which is still rendered and still focusable.
      const items = panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE);
      if (!items || items.length === 0) {
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      if (!first || !last) {
        return;
      }

      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [open, busy, onClose]);

  if (!open) {
    return null;
  }

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto p-4 sm:items-center">
      <div
        className="fixed inset-0 animate-fade-in bg-rail/50 backdrop-blur-[2px]"
        onClick={busy ? undefined : onClose}
        aria-hidden="true"
      />

      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        className={`relative w-full animate-dialog-in rounded-card bg-surface shadow-dialog focus:outline-none ${sizeClasses[size]}`}
      >
        <div className="flex items-start justify-between gap-4 px-5 pb-3 pt-4">
          <div className="min-w-0">
            <h2 id={titleId} className="text-base font-semibold text-slate-900">
              {title}
            </h2>
            {description && (
              <p id={descriptionId} className="mt-1 text-sm text-slate-600">
                {description}
              </p>
            )}
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={onClose}
            disabled={busy}
            aria-label="Close dialog"
            className="-mr-1 -mt-1 px-1.5"
            icon={<X aria-hidden="true" className="h-4 w-4" />}
          />
        </div>

        {children && <div className="px-5 pb-1 text-sm text-slate-700">{children}</div>}

        {footer && (
          <div className="mt-3 flex flex-wrap items-center justify-end gap-2 rounded-b-card border-t border-surface-border bg-surface-muted px-5 py-3">
            {footer}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

const FIRST_FIELD =
  'input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled])';

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
