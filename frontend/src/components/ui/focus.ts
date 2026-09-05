/**
 * One focus treatment, used by everything that can be focused.
 *
 * Before this existed the panel had no focus styling at all outside form
 * fields: every button, tab, menu row and navigation link fell back to the
 * browser's default outline. That is not a small omission. It is invisible to
 * anybody using a mouse, and it is the *entire* interface to somebody using a
 * keyboard — and the default outline is drawn in the user agent's colour, so
 * on the dark navigation rail it was very nearly not drawn at all.
 *
 * `focus-visible` rather than `focus`, so a ring appears when somebody tabs to
 * a control and not when they click it. A ring on click reads as a stuck
 * selection and is the reason people reach for `outline: none`, which is how
 * interfaces lose keyboard focus in the first place.
 *
 * The ring is drawn with an offset so it sits *outside* the control. An inset
 * ring on a filled button disappears into the fill.
 */

/** For controls on light panel surfaces. */
export const focusRing =
  'focus:outline-none focus-visible:outline-none focus-visible:ring-2 ' +
  'focus-visible:ring-brand-500/60 focus-visible:ring-offset-2 focus-visible:ring-offset-surface';

/**
 * For controls on the dark navigation rail.
 *
 * Same ring, offset against the rail rather than the page: an offset in the
 * wrong colour draws a white halo around every sidebar link.
 */
export const focusRingRail =
  'focus:outline-none focus-visible:outline-none focus-visible:ring-2 ' +
  'focus-visible:ring-brand-400 focus-visible:ring-offset-2 focus-visible:ring-offset-rail';

/**
 * For controls inside an already-tight row — a table cell, a list item — where
 * an offset ring would overlap its neighbours. Drawn tight to the control.
 */
export const focusRingTight =
  'focus:outline-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/60';
