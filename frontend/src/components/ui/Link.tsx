import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

import {
  controlClasses,
  iconControlClasses,
  textControlClasses,
  type ControlSize,
  type ControlTone,
  type ControlVariant,
} from '@/components/ui/controlStyles';

/**
 * The link forms of the panel's controls.
 *
 * Each one is drawn from the same class table as its button twin, so a
 * destination and an action that sit side by side in a toolbar are the same
 * height, the same weight and the same colour — and each is still the element
 * it ought to be. That distinction is not pedantry: a link opens in a new tab
 * on a middle click, offers "copy link address", and is announced as a link.
 * A `<button onClick={() => navigate(…)}>` does none of that, and a
 * `<Link>` styled as a button that submits a form does the opposite damage.
 *
 * An `href` gets a plain `<a>`; a `to` gets the router's `Link`. The rule is
 * the same one the router has: leaving the app is a document navigation.
 */

interface CommonProps {
  children?: ReactNode;
  className?: string;
  /** An in-app destination. Exactly one of `to` or `href`. */
  to?: string;
  /** An address outside the panel. Opened in a new tab. */
  href?: string;
  /** Overrides the accessible name. Required when the content is only an icon. */
  label?: string;
  title?: string;
}

function render(props: CommonProps, classes: string, content: ReactNode) {
  const { to, href, label, title } = props;

  if (href) {
    return (
      <a
        href={href}
        target="_blank"
        // `noopener` is what stops the opened page reaching back through
        // window.opener; `noreferrer` keeps the panel's URL out of its logs.
        rel="noreferrer noopener"
        className={classes}
        {...(label ? { 'aria-label': label } : {})}
        {...(title ?? label ? { title: title ?? label } : {})}
      >
        {content}
      </a>
    );
  }

  return (
    <Link
      to={to ?? '#'}
      className={classes}
      {...(label ? { 'aria-label': label } : {})}
      {...(title ?? label ? { title: title ?? label } : {})}
    >
      {content}
    </Link>
  );
}

interface LinkButtonProps extends CommonProps {
  variant?: ControlVariant;
  size?: ControlSize;
  /** Rendered before the label. */
  icon?: ReactNode;
}

/** A destination that looks exactly like a Button. */
export function LinkButton({
  variant = 'secondary',
  size = 'md',
  icon,
  children,
  className = '',
  ...rest
}: LinkButtonProps) {
  return render(
    rest,
    `${controlClasses(variant, size)} ${className}`,
    <>
      {icon}
      {children}
    </>,
  );
}

interface IconLinkProps extends CommonProps {
  icon: ReactNode;
  /** Required: an icon-only link with no accessible name announces as "link". */
  label: string;
  tone?: 'neutral' | 'danger';
  size?: ControlSize;
}

/** A destination that looks exactly like an IconButton. */
export function IconLink({
  icon,
  tone = 'neutral',
  size = 'md',
  className = '',
  ...rest
}: IconLinkProps) {
  return render(rest, `${iconControlClasses(tone, size)} ${className}`, icon);
}

interface TextLinkProps extends CommonProps {
  tone?: ControlTone;
  size?: 'xs' | 'sm';
}

/** A destination that reads as running text, matching TextButton. */
export function TextLink({
  tone = 'brand',
  size = 'sm',
  children,
  className = '',
  ...rest
}: TextLinkProps) {
  return render(rest, `${textControlClasses(tone, size)} ${className}`, children);
}
