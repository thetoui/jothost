# UI conventions

What a control looks like, which one to reach for, and what stops the answer
drifting.

```bash
make fe-test
```

---

## 1. The problem this fixes

Twenty-nine pages had been built one phase at a time, and each one had been
styled from whatever the nearest existing page happened to do. Nobody did
anything wrong; the drift is what happens by default.

By the time it was measured:

| | |
|---|---|
| Hand-rolled `<button>` elements outside the shared component | **44** across 14 files |
| Separate implementations of tabbed sections | **3** — underline, a different underline, and pills |
| Separate implementations of a dropdown menu row | **2**, with different row heights |
| Colour utilities naming a hue where a meaning was intended | **87** across 18 files |
| **Controls with any keyboard-focus styling at all** | **0** |

The last line is the one that matters. Outside form fields, nothing in the
panel styled `:focus` — not a button, not a tab, not a menu row, not a single
navigation link. Every one of them fell back to the browser's default outline,
which is drawn in the user agent's own colour and, on the dark navigation rail,
was very nearly not drawn at all. For anybody working by keyboard that is not a
rough edge; it is the whole interface.

---

## 2. Which control to reach for

Two axes: what it looks like, and whether it *acts* or *goes*. Every row below
is a real pair — the two forms are drawn from the same class table in
`controlStyles.ts`, so they are identical to look at and different in the
markup.

| Shape | Acts (a `<button>`) | Goes (an `<a>`) |
|---|---|---|
| A labelled rectangle | `Button` | `LinkButton` |
| A square icon on its own | `IconButton` | `IconLink` |
| Reads as running text | `TextButton` | `TextLink` |
| A row in a dropdown | `MenuItem` | `MenuItem to="…"` |
| A section switcher | `Tabs` | — |

**The element is not a styling decision.** A link opens in a new tab on a
middle click, offers "copy link address", and is announced as a link and listed
among them. A `<button onClick={() => navigate(…)}>` does none of that. A
`<Link>` styled as a button that submits a form does the opposite damage. The
pairs exist so that matching the *look* never costs the *semantics* — which is
exactly the trade six call sites had already made, badly, in both directions.

### Variants

`primary` for the one action a screen is for. `secondary` for everything
ordinary. `ghost` for a control that must not compete — a toolbar toggle, a
Cancel. `subtle` for a tinted action inside a card.

Destructive actions get two:

- **`danger`** — outlined, for a destructive action sitting among ordinary ones,
  so it does not shout from a toolbar.
- **`destructive`** — filled, for the single button in a confirmation dialog
  that actually does the irreversible thing.

`destructive` exists because `ConfirmDialog` did not have it. It rendered
`variant={destructive ? 'primary' : 'primary'}` — a ternary with the same
answer on both sides — and then patched the colour on through `className`. The
result was a red button that was a different red from every other red button.

---

## 3. Focus

One treatment, three grounds, in `components/ui/focus.ts`:

| | Where | Why it is separate |
|---|---|---|
| `focusRing` | Light panel surfaces | Offset ring, sitting outside the control |
| `focusRingRail` | The dark navigation rail | The offset must be the rail's colour, or every sidebar link gets a white halo |
| `focusRingTight` | Table cells, list rows | No offset: it would overlap the neighbours |

`focus-visible`, never `focus`. A ring that appears on click reads as a stuck
selection, and that is the reason people reach for `outline: none` — which is
how interfaces lose keyboard focus in the first place.

---

## 4. Colour

`tailwind.config.js` states the rule in its own comment: status colours are
"named by meaning rather than hue, so a change of palette does not require
finding every 'green' in the codebase". Eighty-seven usages had drifted back to
raw hues anyway.

**Use `ok`, `warn`, `danger`, `info`, `brand`, `surface`, `rail`.** Not
`emerald`, `amber`, `rose`, `sky`, `slate-200`.

The drift was not cosmetic. `text-rose-700` and `text-danger-700` had come to
sit two lines apart in the same mailbox list, both meaning "this removes
something", in two different reds. `info` was added in this pass, because
several places needed a state that is neither good nor bad — queued, scheduled,
unknown — and had been reaching for `sky` to say it.

**One exception, and it is deliberate:** `ToolTile` and the file-type icons in
`FileTable` keep hue names. Their colour is *categorical* — it tells one kind of
thing from another and claims nothing about health. Green on a databases tile
is not a promise that the databases are well. Both keep their scale in a single
table rather than scattering it, and the consistency suite names them with this
reason attached.

---

## 5. Tabs

There were three implementations. All three declared `role="tablist"`, and none
of them implemented what that promises.

A tablist is a **single stop in the tab order**, moved through with the arrow
keys. A row of plain buttons wearing the role tells a screen reader user to
press Left and Right — and then does nothing when they do. That is worse than
having no role at all, because it is a specific instruction that fails.

`Tabs` implements it: roving `tabIndex`, Left/Right/Home/End, and selection
following focus. Pass `items`, `value`, `onChange` and a `label`.

The underline form won over the pills because these tabs usually sit directly
above a table, and an underline draws the line the table already needed.

`EditorTabs` stays separate and is exempt in the suite: the editor's file tabs
open, close and reorder documents. That is chrome, not section navigation.

---

## 6. What keeps it true

`src/components/ui/__tests__/consistency.test.ts` reads the component tree and
fails on drift. Six rules:

1. Every shared control style contains a focus ring.
2. Every control outside `components/ui` styles focus.
3. No status colour is named by hue.
4. One tablist implementation.
5. One menu-row implementation.
6. No button sets its own height through padding.

Two things make it worth having rather than decorative.

**It reads the tree, not a list.** A list of known files drifts within a week.
A sweep fails the first time somebody adds a control that skips the primitives
— which is the only moment at which the fix is cheap. It is the same argument
as the route sweep in the Phase 24 security suite, pointed at the frontend.

**It asserts it read something.** The first rule checks the file count before
anything is concluded from it, because a sweep that reads nothing reports a
pass — silently, and convincingly. That failure has already happened once in
this repository, when BusyBox `grep` ignored an `--include` flag and the
security suite announced that all zero routes refused correctly.

The suite was verified the only way a guard can be: **each of the five rules
was broken on purpose, and each one failed.** A rule nobody has watched fail is
a rule nobody has tested. Its own first run also caught two bugs in itself — it
flagged the primitives, which get their focus ring indirectly through a helper,
and it matched class names quoted inside the comments explaining them.

---

## 7. Known limitations

- **The sidebar does not filter by permission.** Every destination is listed to
  every account, so a customer sees "Firewall" and is refused on arrival. The
  pages already gate their controls with `RequirePermission`; the menu does
  not. Left alone here deliberately — it changes what people see rather than
  how it looks, which is a product decision and not a consistency pass — but it
  is the clearest remaining inconsistency between the menu and the pages.
- **Two search inputs are hand-rolled** rather than using `Field`, on the
  websites and databases pages. Inputs were out of scope for this pass, so the
  drift is recorded rather than fixed. `Field` is the one to use.
- **The dropdown menus do not roam with arrow keys.** `MenuItem` sets
  `tabIndex={-1}` as `role="menu"` requires, and Escape closes, but the
  Up/Down handling that `Tabs` now has was not added. The menus are two and
  four items long, so this is a gap rather than a wound — but it is the same
  gap the tabs had.
- **Contrast is not measured.** The palette is used consistently now; nothing
  checks any pair against WCAG. A contrast rule in the consistency suite would
  be a genuine addition.
- **Nothing here tests appearance.** The suite reads source, and the pages were
  checked by hand in a browser. There are no visual-regression snapshots, so a
  change that keeps every class name and still looks wrong would pass.
