import { forwardRef, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from 'react';

const controlClasses =
  'w-full rounded-md border bg-surface px-3 text-sm text-ink-strong shadow-card transition-colors ' +
  'placeholder:text-ink-dim focus:outline-none focus:ring-2 focus:ring-brand-500/30 disabled:bg-surface-muted ' +
  'disabled:text-ink-muted';

function borderFor(invalid: boolean): string {
  return invalid
    ? 'border-danger-500 focus:border-danger-500 focus:ring-danger-500/25'
    : 'border-surface-border focus:border-brand-500';
}

interface FieldShellProps {
  id: string;
  label: ReactNode;
  hint?: ReactNode | undefined;
  error?: string | null | undefined;
  /** Rendered after the label, e.g. "optional". */
  suffix?: ReactNode | undefined;
  children: ReactNode;
}

/**
 * FieldShell renders a label, its control, and whichever of hint or error
 * applies.
 *
 * Only one of the two is shown at a time: an error that appears below a hint
 * pushes the form around and buries the thing the reader needs to act on.
 */
export function FieldShell({ id, label, hint, error, suffix, children }: FieldShellProps) {
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <label htmlFor={id} className="text-sm font-medium text-ink">
          {label}
        </label>
        {suffix && <span className="text-xs text-ink-dim">{suffix}</span>}
      </div>

      {children}

      {error ? (
        <p id={`${id}-error`} role="alert" className="mt-1 text-xs text-danger-600">
          {error}
        </p>
      ) : (
        hint && (
          <p id={`${id}-hint`} className="mt-1 text-xs text-ink-muted">
            {hint}
          </p>
        )
      )}
    </div>
  );
}

interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'id'> {
  id: string;
  label: ReactNode;
  hint?: ReactNode | undefined;
  error?: string | null | undefined;
  suffix?: ReactNode | undefined;
  /** Rendered inside the control, at its left edge. */
  adornment?: ReactNode | undefined;
}

/** TextField is a labelled text input with its hint and error wiring. */
export const TextField = forwardRef<HTMLInputElement, TextFieldProps>(function TextField(
  { id, label, hint, error, suffix, adornment, className = '', ...props },
  ref,
) {
  const invalid = Boolean(error);

  return (
    <FieldShell id={id} label={label} hint={hint} error={error} suffix={suffix}>
      <div className="relative">
        {adornment && (
          <span
            aria-hidden="true"
            className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-ink-dim"
          >
            {adornment}
          </span>
        )}
        <input
          ref={ref}
          id={id}
          aria-invalid={invalid || undefined}
          aria-describedby={invalid ? `${id}-error` : hint ? `${id}-hint` : undefined}
          className={[
            controlClasses,
            borderFor(invalid),
            'h-9',
            adornment ? 'pl-9' : '',
            className,
          ].join(' ')}
          {...props}
        />
      </div>
    </FieldShell>
  );
});

interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'id'> {
  id: string;
  label: ReactNode;
  hint?: ReactNode | undefined;
  error?: string | null | undefined;
  children: ReactNode;
}

/** SelectField is a labelled select with matching styling. */
export const SelectField = forwardRef<HTMLSelectElement, SelectFieldProps>(function SelectField(
  { id, label, hint, error, className = '', children, ...props },
  ref,
) {
  const invalid = Boolean(error);

  return (
    <FieldShell id={id} label={label} hint={hint} error={error}>
      <select
        ref={ref}
        id={id}
        aria-invalid={invalid || undefined}
        aria-describedby={invalid ? `${id}-error` : hint ? `${id}-hint` : undefined}
        className={[controlClasses, borderFor(invalid), 'h-9 pr-8', className].join(' ')}
        {...props}
      >
        {children}
      </select>
    </FieldShell>
  );
});

interface ToggleProps {
  id: string;
  label: ReactNode;
  description?: ReactNode;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
}

/**
 * Toggle is a switch for a setting that takes effect on save.
 *
 * It is a real checkbox under the styling, so it is reachable by keyboard and
 * announced correctly, rather than a div that merely looks like a switch.
 */
export function Toggle({ id, label, description, checked, onChange, disabled }: ToggleProps) {
  return (
    <div className="flex items-start gap-3">
      <span className="relative mt-0.5 inline-flex shrink-0">
        <input
          id={id}
          type="checkbox"
          role="switch"
          checked={checked}
          disabled={disabled}
          onChange={(event) => onChange(event.target.checked)}
          className="peer h-5 w-9 cursor-pointer appearance-none rounded-full bg-surface-strong transition-colors checked:bg-brand-500 disabled:cursor-not-allowed disabled:opacity-55"
        />
        <span
          aria-hidden="true"
          className="pointer-events-none absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white shadow-card transition-transform peer-checked:translate-x-4"
        />
      </span>

      <label htmlFor={id} className="cursor-pointer select-none">
        <span className="block text-sm font-medium text-ink">{label}</span>
        {description && <span className="mt-0.5 block text-xs text-ink-muted">{description}</span>}
      </label>
    </div>
  );
}
