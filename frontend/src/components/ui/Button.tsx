import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { Loader2 } from 'lucide-react';

import {
  controlClasses,
  type ControlSize,
  type ControlVariant,
} from '@/components/ui/controlStyles';

export type ButtonVariant = ControlVariant;
export type ButtonSize = ControlSize;

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Shows a spinner and disables the button. */
  loading?: boolean;
  /** Rendered before the label. Hidden while loading, so the width holds. */
  icon?: ReactNode;
}

/**
 * Button is the one place button styling is decided.
 *
 * The loading state replaces the icon rather than adding a spinner beside it,
 * so the button does not change width mid-click and shift what is underneath.
 *
 * For something that navigates rather than acts, use LinkButton: it is drawn
 * from the same class table and looks identical, but stays an anchor.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', loading = false, icon, children, className = '', disabled, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={props.type ?? 'button'}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={`${controlClasses(variant, size)} ${className}`}
      {...props}
    >
      {loading ? (
        <Loader2 aria-hidden="true" className="h-4 w-4 shrink-0 animate-spin" />
      ) : (
        icon
      )}
      {children}
    </button>
  );
});
