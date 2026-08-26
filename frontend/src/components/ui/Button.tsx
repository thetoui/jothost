import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { Loader2 } from 'lucide-react';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'subtle';
export type ButtonSize = 'sm' | 'md';

const variantClasses: Record<ButtonVariant, string> = {
  primary:
    'bg-brand-600 text-white shadow-card hover:bg-brand-700 active:bg-brand-800 disabled:hover:bg-brand-600',
  secondary:
    'border border-surface-border bg-surface text-slate-700 shadow-card hover:bg-surface-muted hover:text-slate-900 disabled:hover:bg-surface',
  ghost: 'text-slate-600 hover:bg-surface-sunken hover:text-slate-900',
  danger:
    'border border-danger-200 bg-surface text-danger-700 shadow-card hover:bg-danger-50 disabled:hover:bg-surface',
  subtle: 'bg-brand-50 text-brand-700 hover:bg-brand-100',
};

const sizeClasses: Record<ButtonSize, string> = {
  sm: 'h-8 gap-1.5 px-2.5 text-xs',
  md: 'h-9 gap-2 px-3.5 text-sm',
};

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
      className={[
        'inline-flex shrink-0 items-center justify-center rounded-md font-medium transition-colors',
        'disabled:cursor-not-allowed disabled:opacity-55',
        sizeClasses[size],
        variantClasses[variant],
        className,
      ].join(' ')}
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
