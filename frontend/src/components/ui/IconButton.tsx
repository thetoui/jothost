import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';

import { iconControlClasses, type ControlSize } from '@/components/ui/controlStyles';

export type IconButtonTone = 'neutral' | 'danger';
export type IconButtonSize = ControlSize;

interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
  /** The icon. Give it `aria-hidden` — the button is named by `label`. */
  icon: ReactNode;
  /**
   * What the button does, for screen readers and as the tooltip.
   *
   * Required rather than optional: an icon-only button with no accessible name
   * is announced as "button", and there were four of those in the panel before
   * this component existed.
   */
  label: string;
  tone?: IconButtonTone;
  size?: IconButtonSize;
}

/**
 * IconButton is a square, icon-only control: expand a row, delete a record,
 * edit a field in place.
 *
 * It exists because the same fourteen-class string was pasted into six pages,
 * drifting a little each time — `p-1` here and `p-1.5` there, `hover:bg-
 * surface-border` in one table and `hover:bg-surface-sunken` in the next — so
 * two identical rows in two tables had targets of different sizes.
 *
 * For something that navigates rather than acts, use IconLink.
 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { icon, label, tone = 'neutral', size = 'md', className = '', ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={props.type ?? 'button'}
      aria-label={label}
      title={label}
      className={`${iconControlClasses(tone, size)} ${className}`}
      {...props}
    >
      {icon}
    </button>
  );
});
