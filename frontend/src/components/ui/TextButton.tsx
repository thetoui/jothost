import { forwardRef, type ButtonHTMLAttributes } from 'react';

import { textControlClasses, type ControlTone } from '@/components/ui/controlStyles';

export type TextButtonTone = ControlTone;

interface TextButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  tone?: TextButtonTone;
  /** Matches the surrounding small print rather than body text. */
  size?: 'xs' | 'sm';
}

/**
 * TextButton is an action that looks like a link but is not one.
 *
 * The panel is full of these — "Set password", "Remove", "Assign this database
 * to a website" — sitting inside a list row where a full button would outweigh
 * the row's content. They were written by hand every time, which is how the
 * panel ended up with `text-rose-700` and `text-danger-700` both meaning
 * "this removes something", in the same list, two lines apart.
 *
 * It stays a `<button>`. A destructive action rendered as an `<a href="#">` is
 * announced as a link, offered in the "open in new tab" menu, and followed by
 * anything crawling the page.
 */
export const TextButton = forwardRef<HTMLButtonElement, TextButtonProps>(function TextButton(
  { tone = 'brand', size = 'sm', className = '', children, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={props.type ?? 'button'}
      className={`${textControlClasses(tone, size)} ${className}`}
      {...props}
    >
      {children}
    </button>
  );
});
