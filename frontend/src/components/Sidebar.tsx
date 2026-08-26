import { NavLink } from 'react-router-dom';
import {
  Database,
  FileCode2,
  Files,
  Globe,
  LayoutDashboard,
  Lock,
  Server,
  ShieldCheck,
  UserCog,
} from 'lucide-react';
import type { LucideIcon } from 'lucide-react';

import { useUiStore } from '@/stores/uiStore';

interface NavItem {
  label: string;
  to: string;
  icon: LucideIcon;
  /** Phase that implements this destination; disabled until then. */
  enabled: boolean;
}

// Navigation reflects the module list in PRD.md section 6. Only destinations
// implemented in the current phase are enabled; the rest are shown so the
// information architecture is visible without pretending to work.
const navItems: NavItem[] = [
  { label: 'Dashboard', to: '/', icon: LayoutDashboard, enabled: true },
  { label: 'Server', to: '/server', icon: Server, enabled: false },
  { label: 'Websites', to: '/websites', icon: Globe, enabled: true },
  { label: 'Databases', to: '/databases', icon: Database, enabled: false },
  { label: 'Files', to: '/files', icon: Files, enabled: false },
  { label: 'Editor', to: '/editor', icon: FileCode2, enabled: false },
  { label: 'SSL', to: '/ssl', icon: Lock, enabled: false },
  { label: 'Security Center', to: '/security-center', icon: ShieldCheck, enabled: false },
  { label: 'Account security', to: '/security', icon: UserCog, enabled: true },
];

export function Sidebar() {
  const collapsed = useUiStore((state) => state.sidebarCollapsed);

  return (
    <nav
      aria-label="Main navigation"
      className={`flex shrink-0 flex-col border-r border-surface-border bg-surface transition-all ${
        collapsed ? 'w-16' : 'w-60'
      }`}
    >
      <div className="flex h-14 items-center gap-2 border-b border-surface-border px-4">
        <span className="grid h-8 w-8 place-items-center rounded-md bg-brand-600 text-sm font-bold text-white">
          J
        </span>
        {!collapsed && <span className="text-sm font-semibold text-slate-900">JotHost Panel</span>}
      </div>

      <ul className="flex flex-1 flex-col gap-1 overflow-y-auto p-2">
        {navItems.map((item) => (
          <li key={item.to}>
            {item.enabled ? (
              <NavLink
                to={item.to}
                className={({ isActive }) =>
                  `flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors ${
                    isActive
                      ? 'bg-brand-50 text-brand-700'
                      : 'text-slate-600 hover:bg-surface-muted hover:text-slate-900'
                  }`
                }
                title={collapsed ? item.label : undefined}
              >
                <item.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
                {!collapsed && <span>{item.label}</span>}
              </NavLink>
            ) : (
              <span
                aria-disabled="true"
                title={`${item.label} — not yet implemented`}
                className="flex cursor-not-allowed items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-slate-400"
              >
                <item.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
                {!collapsed && <span>{item.label}</span>}
              </span>
            )}
          </li>
        ))}
      </ul>
    </nav>
  );
}
