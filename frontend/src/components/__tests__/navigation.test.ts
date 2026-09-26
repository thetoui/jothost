import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  TOOLS_PATH,
  allDestinations,
  locate,
  searchDestinations,
  sidebarGroups,
  sidebarTarget,
  toolGroups,
  visibleGroups,
  visibleSidebar,
  type Can,
} from '@/components/navigation';

/**
 * The sidebar is short because Tools & Settings holds the rest. That only works
 * if nothing falls between the two, so this reads the router itself rather than
 * a list kept here: a page added to the router and forgotten in navigation.ts
 * fails the first run, instead of shipping as a page nobody can find.
 */
function routedPaths(): string[] {
  const source = readFileSync(join(__dirname, '..', '..', 'app', 'router.tsx'), 'utf8');
  return [...source.matchAll(/path:\s*'([^']+)'/g)]
    .map((match) => match[1] ?? '')
    .filter((path) => path !== '/' && path !== '/login' && path !== '*' && !path.includes(':'))
    .map((path) => (path.startsWith('/') ? path : `/${path}`));
}

describe('navigation', () => {
  it('reaches every routed page from the sidebar or Tools & Settings', () => {
    const reachable = new Set(allDestinations.map((destination) => destination.to));
    const orphans = routedPaths().filter((path) => !reachable.has(path));

    expect(orphans).toEqual([]);
  });

  it('links only to pages that are routed', () => {
    const routed = new Set(['/', ...routedPaths()]);
    const dead = allDestinations.map((d) => d.to).filter((to) => !routed.has(to));

    expect(dead).toEqual([]);
  });

  it('lists each page once', () => {
    const paths = allDestinations.map((destination) => destination.to);

    expect(new Set(paths).size).toBe(paths.length);
  });

  it('keeps the sidebar short and Tools & Settings in it', () => {
    const sidebar = sidebarGroups.flatMap((group) => group.items);

    expect(sidebar.length).toBeLessThanOrEqual(10);
    expect(sidebar.map((item) => item.to)).toContain(TOOLS_PATH);
    expect(toolGroups.every((group) => group.items.length > 0)).toBe(true);
  });

  it('places a page under Tools & Settings when that is where it lives', () => {
    expect(locate('/firewall')).toMatchObject({ underTools: true });
    expect(sidebarTarget('/firewall')).toBe(TOOLS_PATH);
    expect(sidebarTarget('/websites/abc')).toBe('/websites');
    expect(sidebarTarget('/')).toBe('/');
    // "/" matches itself only, or every page would belong to the dashboard.
    expect(locate('/no-such-page')).toBeUndefined();
    // The longest prefix wins: /security-center is not /security.
    expect(locate('/security-center')?.destination.label).toBe('Security Center');
    expect(locate('/security')?.destination.label).toBe('Account security');
  });

  // ------------------------------------------------ what each person sees

  const only =
    (...granted: string[]): Can =>
    (permission) =>
      granted.includes(permission);
  const paths = (groups: { items: { to: string }[] }[]) =>
    groups.flatMap((group) => group.items.map((item) => item.to));

  it('gives every page a permission except the two everybody may open', () => {
    // A page added without one would be shown to everybody, which is the
    // mistake this whole filter exists to prevent.
    const open = allDestinations.filter((d) => d.permission === undefined).map((d) => d.to);
    expect(open.sort()).toEqual(['/security', TOOLS_PATH].sort());
  });

  it('shows a website customer only what their permissions open', () => {
    const can = only('website.view', 'file.read');
    const sidebar = paths(visibleSidebar(can));

    expect(sidebar).toEqual(expect.arrayContaining(['/websites', '/files', TOOLS_PATH, '/security']));
    // server.view, database.manage: not theirs.
    expect(sidebar).not.toContain('/');
    expect(sidebar).not.toContain('/databases');
    expect(sidebar).not.toContain('/monitoring');

    const tools = visibleGroups(toolGroups, can);
    expect(paths(tools).sort()).toEqual(['/editor', '/node', '/ssl']);
    // A group with nothing left in it goes too, rather than showing a heading.
    expect(tools.map((group) => group.title)).not.toContain('Security');
  });

  it('drops Tools & Settings when nothing on it can be opened', () => {
    expect(paths(visibleSidebar(only()))).toEqual(['/security']);
  });

  it('keeps search from reaching round the menu', () => {
    expect(searchDestinations('fire', only('website.view')).map((d) => d.to)).not.toContain(
      '/firewall',
    );
    expect(searchDestinations('fire').map((d) => d.to)).toContain('/firewall');
  });
});
