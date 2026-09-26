import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  TOOLS_PATH,
  allDestinations,
  locate,
  sidebarGroups,
  sidebarTarget,
  toolGroups,
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
});
