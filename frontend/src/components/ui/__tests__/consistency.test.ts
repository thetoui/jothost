import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';

import { describe, expect, it } from 'vitest';

/**
 * The consistency suite.
 *
 * Every other test in this directory renders a component and asks what it
 * does. This one reads the source and asks what it *looks like*, because the
 * failure it guards against is not a component behaving wrongly — it is the
 * fourteenth hand-rolled button, styled from the nearest example rather than
 * from the design system, that nobody notices until a row of controls is three
 * different heights.
 *
 * The whole point is that it reads the tree rather than a list kept in the
 * test. A list drifts within a week; a sweep of the source fails the first
 * time somebody adds a control that does not use the shared primitives. It is
 * the same argument as the route sweep in the Phase 24 security suite, applied
 * to the frontend.
 *
 * Each rule below carries its exceptions inline, with the reason. An exception
 * with a reason is a decision; an exception without one is a rule nobody
 * believes in.
 */

const SRC = join(__dirname, '..', '..', '..');

function sources(): string[] {
  const out: string[] = [];
  (function walk(dir: string) {
    for (const entry of readdirSync(dir)) {
      const full = join(dir, entry);
      if (statSync(full).isDirectory()) {
        if (entry !== '__tests__' && entry !== 'test') {
          walk(full);
        }
      } else if (entry.endsWith('.tsx')) {
        out.push(full);
      }
    }
  })(join(SRC, 'components'));
  for (const dir of ['pages', 'features']) {
    (function walk(d: string) {
      for (const entry of readdirSync(d)) {
        const full = join(d, entry);
        if (statSync(full).isDirectory()) {
          if (entry !== '__tests__') {
            walk(full);
          }
        } else if (entry.endsWith('.tsx')) {
          out.push(full);
        }
      }
    })(join(SRC, dir));
  }
  return out;
}

function name(file: string): string {
  return relative(SRC, file).split(sep).join('/');
}

/**
 * The source with its comments removed.
 *
 * Needed because these rules match on class names, and the comments in this
 * codebase quote the class names they are explaining. The first run of the
 * colour rule failed on the sentence in TextButton.tsx describing the very
 * drift it had just been written to prevent.
 */
function code(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
}

/** Every opening tag for `tag`, as written, including its attributes. */
function openingTags(source: string, tag: string): string[] {
  const found: string[] = [];
  const pattern = new RegExp(`<${tag}\\b`, 'g');
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(source)) !== null) {
    let depth = 0;
    let i = match.index;
    for (; i < source.length; i += 1) {
      const c = source[i];
      if (c === '{') depth += 1;
      else if (c === '}') depth -= 1;
      else if (c === '>' && depth === 0) break;
    }
    found.push(source.slice(match.index, i + 1));
  }
  return found;
}

const files = sources();

describe('UI consistency', () => {
  // The sweep is worthless if it reads nothing, and reading nothing is exactly
  // what a broken glob does — silently, and reported as a pass. So the size of
  // what was read is asserted before anything is concluded from it.
  it('reads the whole component tree', () => {
    expect(files.length).toBeGreaterThan(60);
  });

  it('builds a focus ring into every shared control style', () => {
    // The primitives are checked at their definition rather than in their
    // markup, because they compose their classes through a helper: a tag
    // reading `className={controlClasses(variant, size)}` says nothing about
    // what is in it. This is the half of the rule the sweep below cannot see,
    // so it is asserted directly — and the sweep skips components/ui for
    // exactly that reason.
    const styles = readFileSync(join(SRC, 'components', 'ui', 'controlStyles.ts'), 'utf8');
    for (const helper of ['controlClasses', 'iconControlClasses', 'textControlClasses']) {
      const from = styles.indexOf(`export function ${helper}`);
      expect(from, `${helper} is missing`).toBeGreaterThan(-1);
      expect(styles.slice(from, styles.indexOf('\n}', from))).toMatch(/focusRing/);
    }

    // The two primitives that build their own shell rather than using the
    // table above.
    for (const file of ['Menu.tsx', 'ToolTile.tsx']) {
      expect(
        readFileSync(join(SRC, 'components', 'ui', file), 'utf8'),
        `${file} has no focus treatment`,
      ).toMatch(/focusRing/);
    }
  });

  it('styles keyboard focus on every control outside the primitives', () => {
    // A control with no focus style is invisible to somebody navigating by
    // keyboard. The browser default is not a design decision, and on the dark
    // navigation rail it is very nearly not a visible one either. Before this
    // pass the panel had no focus styling at all beyond its form fields.
    const offenders: string[] = [];

    for (const file of files) {
      // The primitives define the treatment; the test above checks them.
      if (name(file).startsWith('components/ui/')) continue;
      const source = code(readFileSync(file, 'utf8'));
      for (const tag of [...openingTags(source, 'button'), ...openingTags(source, 'a')]) {
        // No className at all means the styling comes from a parent or from a
        // shared constant, and there is nothing here to have got wrong.
        if (!tag.includes('className')) continue;
        if (tag.includes('focusRing') || tag.includes('focus-visible:')) continue;
        offenders.push(`${name(file)}: ${tag.replace(/\s+/g, ' ').slice(0, 110)}`);
      }
    }

    expect(offenders).toEqual([]);
  });

  it('names status colour by meaning rather than by hue', () => {
    // tailwind.config.js says so in as many words: "named by meaning rather
    // than hue, so a change of palette does not require finding every 'green'
    // in the codebase". Eighty-seven usages had drifted back to raw hues,
    // which is how `text-rose-700` and `text-danger-700` came to sit two lines
    // apart in the same list, both meaning "this removes something".
    const decorative = new Set([
      // Categorical hues that tell one *kind* of tool from another. Nothing
      // here claims a thing is good or bad, and both files keep the scale in
      // one table rather than scattering it.
      'components/ui/ToolTile.tsx',
      'features/files/components/FileTable.tsx',
    ]);
    const hue = /\b(?:hover:|focus:|focus-visible:|group-hover:)?(?:text|bg|border|ring|from|to|fill|stroke)-(?:rose|emerald|amber|red|sky|violet|green|orange|lime|teal)-\d{2,3}\b/g;

    const offenders: string[] = [];
    for (const file of files) {
      if (decorative.has(name(file))) continue;
      const source = code(readFileSync(file, 'utf8'));
      for (const match of source.match(hue) ?? []) {
        offenders.push(`${name(file)}: ${match}`);
      }
    }

    expect(offenders).toEqual([]);
  });

  it('keeps one implementation of tabbed sections', () => {
    // Three of these existed: underline tabs on the databases page, slightly
    // different underline tabs on the website panel, and pills on the tenancy
    // page — same job, three visual languages. Worse, all three declared
    // role="tablist" without the arrow-key behaviour the role promises.
    const allowed = new Set([
      'components/ui/Tabs.tsx',
      // The editor's file tabs are document chrome rather than section
      // navigation: they open, close and reorder, which Tabs does not do.
      'features/editor/components/EditorTabs.tsx',
    ]);

    const offenders = files
      .filter((file) => !allowed.has(name(file)))
      .filter((file) => code(readFileSync(file, 'utf8')).includes('role="tablist"'))
      .map(name);

    expect(offenders).toEqual([]);
  });

  it('keeps one implementation of a dropdown menu row', () => {
    const allowed = new Set(['components/ui/Menu.tsx']);

    const offenders = files
      .filter((file) => !allowed.has(name(file)))
      .filter((file) => code(readFileSync(file, 'utf8')).includes('role="menuitem"'))
      .map(name);

    expect(offenders).toEqual([]);
  });

  it('leaves button geometry to the shared control styles', () => {
    // The tell of a hand-rolled button is padding where a height should be.
    // `px-4 py-2` on a button is somebody who did not know Button existed, and
    // it produced a control two pixels taller than the one beside it.
    const offenders: string[] = [];
    for (const file of files) {
      if (name(file) === 'components/ui/controlStyles.ts') continue;
      const source = code(readFileSync(file, 'utf8'));
      for (const tag of openingTags(source, 'button')) {
        if (/\bpy-2\b/.test(tag) && /\bpx-[34]\b/.test(tag) && /\brounded-md\b/.test(tag)) {
          offenders.push(`${name(file)}: ${tag.replace(/\s+/g, ' ').slice(0, 110)}`);
        }
      }
    }

    expect(offenders).toEqual([]);
  });
});
