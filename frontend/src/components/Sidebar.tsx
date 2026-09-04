import { NavLink } from 'react-router-dom';
import {
  Activity,
  BellRing,
  Clock,
  Code2,
  Database,
  FileCode2,
  Files,
  FolderKey,
  Globe,
  HardDrive,
  Hexagon,
  KeyRound,
  LayoutDashboard,
  Lock,
  GitBranch,
  Layers,
  Mails,
  Network,
  PackageSearch,
  Server,
  ScrollText,
  ServerCog,
  Shield,
  ShieldBan,
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

interface NavGroup {
  /** Omitted for the first group, which needs no heading above the logo. */
  label?: string;
  items: NavItem[];
}

// Navigation reflects the module list in PRD.md section 6, grouped the way an
// operator thinks about the server: what it is doing, what it hosts, what it
// stores, and how it is secured. Only destinations implemented in the current
// phase are enabled; the rest are shown so the information architecture is
// visible without pretending to work.
const navGroups: NavGroup[] = [
  {
    items: [{ label: 'Dashboard', to: '/', icon: LayoutDashboard, enabled: true }],
  },
  {
    label: 'Hosting',
    items: [
      { label: 'Websites', to: '/websites', icon: Globe, enabled: true },
      { label: 'PHP', to: '/php', icon: FileCode2, enabled: true },
      { label: 'Node.js', to: '/node', icon: Hexagon, enabled: true },
      { label: 'Databases', to: '/databases', icon: Database, enabled: true },
      { label: 'SSL', to: '/ssl', icon: Lock, enabled: true },
      { label: 'DNS', to: '/dns', icon: Network, enabled: true },
    ],
  },
  {
    label: 'Files & Jobs',
    items: [
      { label: 'Files', to: '/files', icon: Files, enabled: true },
      { label: 'Editor', to: '/editor', icon: Code2, enabled: true },
      { label: 'Scheduled jobs', to: '/cron', icon: Clock, enabled: true },
      { label: 'FTP', to: '/ftp', icon: FolderKey, enabled: true },
      { label: 'Backups', to: '/backups', icon: HardDrive, enabled: true },
      { label: 'Mail', to: '/mail', icon: Mails, enabled: true },
      { label: 'Deployments', to: '/deployments', icon: GitBranch, enabled: true },
    ],
  },
  {
    label: 'Server',
    items: [
      { label: 'Web server', to: '/webserver', icon: Layers, enabled: true },
      { label: 'Services', to: '/services', icon: ServerCog, enabled: true },
      { label: 'Logs', to: '/logs', icon: ScrollText, enabled: true },
      { label: 'Firewall', to: '/firewall', icon: Shield, enabled: true },
      { label: 'SSH', to: '/ssh', icon: KeyRound, enabled: true },
      { label: 'Intrusion prevention', to: '/fail2ban', icon: ShieldBan, enabled: true },
      { label: 'System updates', to: '/updates', icon: PackageSearch, enabled: true },
      { label: 'Server', to: '/server', icon: Server, enabled: false },
      { label: 'Monitoring', to: '/monitoring', icon: Activity, enabled: true },
      { label: 'Security Center', to: '/security-center', icon: ShieldCheck, enabled: true },
      { label: 'Notifications', to: '/notifications', icon: BellRing, enabled: true },
      { label: 'Account security', to: '/security', icon: UserCog, enabled: true },
    ],
  },
];

export function Sidebar() {
  const collapsed = useUiStore((state) => state.sidebarCollapsed);

  return (
    <nav
      aria-label="Main navigation"
      className={`flex shrink-0 flex-col bg-rail transition-[width] duration-200 ${
        collapsed ? 'w-16' : 'w-60'
      }`}
    >
      <div className="flex h-14 shrink-0 items-center gap-2.5 border-b border-rail-border px-4">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-md bg-brand-500 text-sm font-bold text-white">
          J
        </span>
        {!collapsed && (
          <span className="truncate text-sm font-semibold text-rail-bright">JotHost Panel</span>
        )}
      </div>

      <div className="flex-1 overflow-y-auto overflow-x-hidden px-2 py-3">
        {navGroups.map((group, index) => (
          <div key={group.label ?? `group-${index}`} className={index > 0 ? 'mt-5' : undefined}>
            {group.label && !collapsed && (
              <p className="mb-1.5 px-3 text-[0.6875rem] font-semibold uppercase tracking-wider text-rail-text/70">
                {group.label}
              </p>
            )}
            {/* Collapsed, a group needs a divider instead of a heading, or the
                sections run together into one undifferentiated column. */}
            {group.label && collapsed && index > 0 && (
              <div aria-hidden="true" className="mx-2 mb-2 border-t border-rail-border" />
            )}

            <ul className="flex flex-col gap-0.5">
              {group.items.map((item) => (
                <li key={item.to}>
                  <NavItemLink item={item} collapsed={collapsed} />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </nav>
  );
}

function NavItemLink({ item, collapsed }: { item: NavItem; collapsed: boolean }) {
  const base = `group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors ${
    collapsed ? 'justify-center' : ''
  }`;

  if (!item.enabled) {
    return (
      <span
        aria-disabled="true"
        title={`${item.label} — not yet implemented`}
        className={`${base} cursor-not-allowed text-rail-text/45`}
      >
        <item.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
        {!collapsed && <span className="truncate">{item.label}</span>}
      </span>
    );
  }

  return (
    <NavLink
      to={item.to}
      end={item.to === '/'}
      title={collapsed ? item.label : undefined}
      className={({ isActive }) =>
        `${base} ${
          isActive
            ? 'bg-rail-active text-white'
            : 'text-rail-text hover:bg-rail-hover hover:text-rail-bright'
        }`
      }
    >
      {({ isActive }) => (
        <>
          {/* The active marker is a rail edge rather than a background alone,
              which stays legible when the sidebar is collapsed to icons. */}
          <span
            aria-hidden="true"
            className={`absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r bg-brand-400 transition-opacity ${
              isActive ? 'opacity-100' : 'opacity-0'
            }`}
          />
          <item.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
          {!collapsed && <span className="truncate">{item.label}</span>}
        </>
      )}
    </NavLink>
  );
}
