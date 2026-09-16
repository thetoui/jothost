import { focusRing, focusRingTight } from '@/components/ui/focus';

/**
 * The class tables behind every control in the panel.
 *
 * They live apart from the components because a control comes in two forms —
 * something that *does* a thing and something that *goes* somewhere — and the
 * two must be indistinguishable to look at while staying different in the
 * markup. "Delete" is a button; "Open in phpMyAdmin" is a link, and no amount
 * of styling should make it a button, because a link is what the middle mouse
 * button, the context menu and a screen reader's link list all expect.
 *
 * Before this file the panel had it the other way round: the appearance was
 * duplicated per call site and the semantics were whatever the nearest example
 * happened to use. Six places drew a bordered rectangle with a label in it, at
 * four different heights.
 */

export type ControlVariant =
  | 'primary'
  | 'secondary'
  | 'ghost'
  | 'danger'
  | 'destructive'
  | 'subtle';
export type ControlSize = 'sm' | 'md';
export type ControlTone = 'brand' | 'danger' | 'neutral';

const variantClasses: Record<ControlVariant, string> = {
  // On the inverted dark ramp the vivid hue lives at 500; 600+ are light text
  // shades. Dark ink on green reads better than white and clears 4.5:1.
  primary:
    'bg-brand-500 text-[#04140a] shadow-card hover:bg-brand-400 active:bg-brand-300 disabled:hover:bg-brand-500',
  secondary:
    'border border-surface-border bg-surface text-ink shadow-card hover:bg-surface-muted hover:text-ink-strong disabled:hover:bg-surface',
  ghost: 'text-ink hover:bg-surface-sunken hover:text-ink-strong',
  // `danger` is the outlined form: an action that is destructive but sits
  // among ordinary ones, so it must not shout from a toolbar.
  danger:
    'border border-danger-200 bg-surface text-danger-700 shadow-card hover:bg-danger-50 disabled:hover:bg-surface',
  // `destructive` is the filled form, for the one button in a confirmation
  // dialog that actually does the irreversible thing. It exists so callers
  // stop reaching for `primary` and patching the colour through className,
  // which is what ConfirmDialog did and what made a red primary button and a
  // red danger button two different reds.
  destructive:
    'bg-danger-500 text-white shadow-card hover:bg-danger-400 active:bg-danger-300 disabled:hover:bg-danger-500',
  subtle: 'bg-brand-50 text-brand-700 hover:bg-brand-100',
};

const sizeClasses: Record<ControlSize, string> = {
  sm: 'h-8 gap-1.5 px-2.5 text-xs',
  md: 'h-9 gap-2 px-3.5 text-sm',
};

/** A labelled rectangle: `<Button>` and `<LinkButton>`. */
export function controlClasses(variant: ControlVariant, size: ControlSize): string {
  return [
    'inline-flex shrink-0 items-center justify-center rounded-md font-medium transition-colors',
    'disabled:cursor-not-allowed disabled:opacity-55',
    focusRing,
    sizeClasses[size],
    variantClasses[variant],
  ].join(' ');
}

const iconToneClasses: Record<'neutral' | 'danger', string> = {
  neutral: 'text-ink-dim hover:bg-surface-sunken hover:text-ink',
  danger: 'text-ink-dim hover:bg-danger-50 hover:text-danger-600',
};

const iconSizeClasses: Record<ControlSize, string> = {
  sm: 'h-6 w-6',
  md: 'h-7 w-7',
};

/**
 * A square icon-only control: `<IconButton>` and `<IconLink>`.
 *
 * The size is fixed rather than derived from padding, which is what keeps a
 * row of them aligned when their icons differ.
 */
export function iconControlClasses(
  tone: 'neutral' | 'danger',
  size: ControlSize,
): string {
  return [
    'inline-grid shrink-0 place-items-center rounded transition-colors',
    'disabled:cursor-not-allowed disabled:opacity-55',
    focusRingTight,
    iconSizeClasses[size],
    iconToneClasses[tone],
  ].join(' ');
}

const textToneClasses: Record<ControlTone, string> = {
  brand: 'text-brand-700 hover:text-brand-800',
  danger: 'text-danger-700 hover:text-danger-800',
  neutral: 'text-ink hover:text-ink-strong',
};

/** An action or destination that reads as running text: `<TextButton>`, `<TextLink>`. */
export function textControlClasses(tone: ControlTone, size: 'xs' | 'sm'): string {
  return [
    'inline rounded-sm font-medium underline-offset-2 transition-colors hover:underline',
    'disabled:cursor-not-allowed disabled:opacity-55 disabled:hover:no-underline',
    focusRingTight,
    size === 'xs' ? 'text-xs' : 'text-sm',
    textToneClasses[tone],
  ].join(' ');
}
