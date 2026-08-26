import type { ReactNode } from 'react';
import { AlertTriangle, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';

interface ConfirmDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: ReactNode;
  /** What is about to happen, in plain language. */
  description?: ReactNode;
  /** Extra detail: what exactly is affected, and what cannot be undone. */
  children?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Destructive actions get a red button and a warning icon. */
  destructive?: boolean;
  loading?: boolean;
  /** Shown in place of the description when the action failed. */
  error?: string | null;
}

/**
 * ConfirmDialog asks before something irreversible happens.
 *
 * Unlike window.confirm it can name what is affected and stay open while the
 * work runs, which matters here: these actions queue a job on the host, and a
 * dialog that vanished on click would leave the user guessing whether it took.
 */
export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  children,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  destructive = false,
  loading = false,
  error = null,
}: ConfirmDialogProps) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      size="sm"
      busy={loading}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={loading}>
            {cancelLabel}
          </Button>
          <Button
            variant={destructive ? 'primary' : 'primary'}
            onClick={onConfirm}
            loading={loading}
            className={
              destructive ? 'bg-danger-600 hover:bg-danger-700 active:bg-danger-700' : undefined
            }
            icon={
              destructive ? <Trash2 aria-hidden="true" className="h-4 w-4" /> : undefined
            }
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children && <div className="pb-2">{children}</div>}

      {error && (
        <p
          role="alert"
          className="mb-2 flex items-start gap-2 rounded-md bg-danger-50 px-3 py-2 text-sm text-danger-700"
        >
          <AlertTriangle aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />
          {error}
        </p>
      )}
    </Modal>
  );
}
