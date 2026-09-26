import { Link, useLocation } from 'react-router-dom';

import { sidebarTarget, visibleSidebar, type Destination } from '@/components/navigation';
import { focusRingRail } from '@/components/ui/focus';
import { useCan } from '@/features/auth/hooks';
import { useUiStore } from '@/stores/uiStore';

/**
 * Sidebar is the panel's main navigation.
 *
 * Short on purpose: the pages somebody opens every day, then Tools & Settings,
 * which holds everything that administers the server itself. The list lives in
 * navigation.ts, shared with that page, the breadcrumbs and the tool search.
 */
export function Sidebar() {
  const collapsed = useUiStore((state) => state.sidebarCollapsed);
  const { pathname } = useLocation();
  const current = sidebarTarget(pathname);
  // Only the pages this person can open; see visibleSidebar.
  const groups = visibleSidebar(useCan());

  return (
    <nav
      aria-label="Main navigation"
      className={`flex h-full shrink-0 flex-col bg-rail transition-[width] duration-200 ${
        collapsed ? 'w-16' : 'w-60'
      }`}
    >
      <div className="flex h-14 shrink-0 items-center gap-2.5 border-b border-rail-border px-4">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-md bg-brand-500 text-sm font-bold text-[#04140a]">
          J
        </span>
        {!collapsed && (
          <span className="truncate text-sm font-semibold text-rail-bright">JotHost Panel</span>
        )}
      </div>

      <div className="flex-1 overflow-y-auto overflow-x-hidden px-2 py-3">
        {groups.map((group, index) => (
          <div key={group.title ?? `group-${index}`} className={index > 0 ? 'mt-5' : undefined}>
            {group.title && !collapsed && (
              <p className="mb-1.5 px-3 text-[0.6875rem] font-semibold uppercase tracking-wider text-rail-text/70">
                {group.title}
              </p>
            )}
            {/* Collapsed, a group needs a divider instead of a heading, or the
                sections run together into one undifferentiated column. */}
            {group.title && collapsed && (
              <div aria-hidden="true" className="mx-2 mb-2 border-t border-rail-border" />
            )}

            <ul className="flex flex-col gap-0.5">
              {group.items.map((item) => (
                <li key={item.to}>
                  <NavItemLink
                    item={item}
                    collapsed={collapsed}
                    active={current === item.to}
                    exact={pathname === item.to}
                  />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </nav>
  );
}

function NavItemLink({
  item,
  collapsed,
  active,
  exact,
}: {
  item: Destination;
  collapsed: boolean;
  /** This entry, or the section it opens, holds the current page. */
  active: boolean;
  /** The current page is this entry itself. */
  exact: boolean;
}) {
  return (
    <Link
      to={item.to}
      title={collapsed ? item.label : undefined}
      // "page" only for the page itself. Tools & Settings is lit while the
      // Firewall is open, but the Firewall is not Tools & Settings.
      aria-current={exact ? 'page' : active ? 'true' : undefined}
      className={`group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors ${focusRingRail} ${
        collapsed ? 'justify-center' : ''
      } ${
        active
          ? 'bg-rail-active text-white'
          : 'text-rail-text hover:bg-rail-hover hover:text-rail-bright'
      }`}
    >
      {/* The active marker is a rail edge rather than a background alone,
          which stays legible when the sidebar is collapsed to icons. */}
      <span
        aria-hidden="true"
        className={`absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r bg-brand-400 transition-opacity ${
          active ? 'opacity-100' : 'opacity-0'
        }`}
      />
      <item.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
      {!collapsed && <span className="truncate">{item.label}</span>}
    </Link>
  );
}
