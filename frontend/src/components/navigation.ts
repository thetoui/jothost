import {
  Activity,
  BellRing,
  Clock,
  Code2,
  Database,
  FileCode2,
  Files,
  FolderKey,
  GitBranch,
  Globe,
  HardDrive,
  Hexagon,
  KeyRound,
  LayoutDashboard,
  Layers,
  Lock,
  Mails,
  Network,
  PackageSearch,
  ScrollText,
  Server,
  ServerCog,
  Shield,
  ShieldBan,
  ShieldCheck,
  SlidersHorizontal,
  UserCog,
  Users,
} from 'lucide-react';
import type { LucideIcon } from 'lucide-react';

import { Permission, type PermissionName } from '@/features/auth/permissions';

/**
 * Where every page in the panel lives, in one place.
 *
 * The layout follows the one hosting operators already know from Plesk: a
 * short sidebar holding the things somebody opens every day, and everything
 * that administers the server itself gathered on a Tools & Settings page in
 * titled groups. Twenty-eight entries in a sidebar is a list nobody scans; nine
 * is a list somebody can find their way around with their eyes closed.
 *
 * The sidebar, the Tools & Settings page, the breadcrumbs and the header's tool
 * search all read this file, so a page is added in one place and appears in all
 * four. A consistency test sweeps the router and fails if a page is routed but
 * reachable from none of them.
 */

export interface Destination {
  /** Matches the page's own heading, so the menu and the page agree. */
  label: string;
  to: string;
  icon: LucideIcon;
  /** One line on the Tools & Settings page and in search results. */
  description: string;
  /** Extra words somebody might search for that the label does not contain. */
  keywords?: string;
  /**
   * The permission the page's own data needs — taken from the API's guard on
   * the endpoint the page loads, not guessed. Omitted for a page everybody
   * signed in can open.
   */
  permission?: PermissionName;
}

export interface DestinationGroup {
  /** Omitted for the first sidebar group. */
  title?: string;
  items: Destination[];
}

/** A group on Tools & Settings, which always has a heading. */
export interface TitledGroup extends DestinationGroup {
  title: string;
}

export const TOOLS_PATH = '/tools';

/** The sidebar: what gets opened every day, and the way into everything else. */
export const sidebarGroups: DestinationGroup[] = [
  {
    items: [
      {
        label: 'Dashboard',
        to: '/',
        permission: Permission.ServerView,
        icon: LayoutDashboard,
        description: 'The state of this server at a glance.',
        keywords: 'home overview cpu memory disk',
      },
      {
        label: 'Websites & Domains',
        to: '/websites',
        permission: Permission.WebsiteView,
        icon: Globe,
        description: 'Every site on this server, and the tools for each.',
        keywords: 'sites domains subdomains hosting',
      },
      {
        label: 'Mail',
        to: '/mail',
        permission: Permission.MailView,
        icon: Mails,
        description: 'Mail domains, mailboxes, forwarders and webmail.',
        keywords: 'email mailbox smtp imap dkim spf dmarc webmail',
      },
      {
        label: 'Files',
        to: '/files',
        permission: Permission.FileRead,
        icon: Files,
        description: "Browse, upload and edit a site's files.",
        keywords: 'file manager upload download',
      },
      {
        label: 'Databases',
        to: '/databases',
        permission: Permission.DatabaseManage,
        icon: Database,
        description: 'Databases, their users, imports and exports.',
        keywords: 'mysql mariadb postgresql phpmyadmin sql',
      },
    ],
  },
  {
    title: 'Server Management',
    items: [
      {
        label: 'Tools & Settings',
        to: TOOLS_PATH,
        icon: SlidersHorizontal,
        description: 'Everything that administers the server itself.',
        keywords: 'server settings administration',
      },
      {
        label: 'Monitoring',
        to: '/monitoring',
        permission: Permission.ServerView,
        icon: Activity,
        description: 'Resource history, alert rules and open alerts.',
        keywords: 'statistics graphs alerts grafana',
      },
      {
        label: 'Accounts & plans',
        to: '/tenancy',
        permission: Permission.TenantView,
        icon: Users,
        description: 'Resellers, customers, hosting plans and their limits.',
        keywords: 'customers resellers service plans subscriptions quotas',
      },
    ],
  },
  {
    title: 'My Profile',
    items: [
      {
        label: 'Account security',
        to: '/security',
        icon: UserCog,
        description: 'Your password, two-factor sign-in and recovery codes.',
        keywords: 'profile password 2fa totp',
      },
    ],
  },
];

/** The Tools & Settings page, in the groups it shows. */
export const toolGroups: TitledGroup[] = [
  {
    title: 'Hosting Settings',
    items: [
      {
        label: 'PHP',
        to: '/php',
        permission: Permission.ServerView,
        icon: FileCode2,
        description: 'PHP versions installed on the server and their settings.',
        keywords: 'php-fpm handler version',
      },
      {
        label: 'Node.js',
        to: '/node',
        permission: Permission.WebsiteView,
        icon: Hexagon,
        description: 'Node.js runtimes and the applications that use them.',
        keywords: 'node npm javascript',
      },
      {
        label: 'Web server',
        to: '/webserver',
        permission: Permission.ServerView,
        icon: Layers,
        description: 'nginx, and Apache behind it where a site needs .htaccess.',
        keywords: 'nginx apache htaccess',
      },
      {
        label: 'DNS',
        to: '/dns',
        permission: Permission.ServerView,
        icon: Network,
        description: 'Zones, records, DNSSEC and external DNS providers.',
        keywords: 'zone records nameserver bind dnssec',
      },
      {
        label: 'SSL certificates',
        to: '/ssl',
        permission: Permission.WebsiteView,
        icon: Lock,
        description: "Every site's certificate, its issuer and when it renews.",
        keywords: 'tls https lets encrypt certificate',
      },
      {
        label: 'FTP',
        to: '/ftp',
        permission: Permission.ServerView,
        icon: FolderKey,
        description: 'FTP accounts and the FTP server.',
        keywords: 'ftp sftp file sharing',
      },
    ],
  },
  {
    title: 'Tools & Resources',
    items: [
      {
        label: 'Backups',
        to: '/backups',
        permission: Permission.BackupManage,
        icon: HardDrive,
        description: 'Backups, schedules, destinations and restores.',
        keywords: 'backup manager restore s3 schedule',
      },
      {
        label: 'Scheduled jobs',
        to: '/cron',
        permission: Permission.CronManage,
        icon: Clock,
        description: 'Commands that run on a schedule, and their history.',
        keywords: 'cron scheduled tasks',
      },
      {
        label: 'Code editor',
        to: '/editor',
        permission: Permission.FileRead,
        icon: Code2,
        description: 'Edit files in the browser.',
        keywords: 'editor monaco',
      },
      {
        label: 'Deployments',
        to: '/deployments',
        permission: Permission.DeployView,
        icon: GitBranch,
        description: 'Deploy sites from Git, on a push or by hand.',
        keywords: 'git webhook deploy',
      },
    ],
  },
  {
    title: 'Security',
    items: [
      {
        label: 'Security Center',
        to: '/security-center',
        permission: Permission.SecurityView,
        icon: ShieldCheck,
        description: "What this server's last scan found, and its score.",
        keywords: 'scan findings score',
      },
      {
        label: 'Firewall',
        to: '/firewall',
        permission: Permission.ServerView,
        icon: Shield,
        description: 'Rules for what may reach this server.',
        keywords: 'ufw iptables nftables ports',
      },
      {
        label: 'Intrusion prevention',
        to: '/fail2ban',
        permission: Permission.ServerView,
        icon: ShieldBan,
        description: 'Banning addresses that keep failing to sign in.',
        keywords: 'fail2ban ip address banning jails',
      },
      {
        label: 'SSH',
        to: '/ssh',
        permission: Permission.ServerView,
        icon: KeyRound,
        description: 'SSH keys and how the SSH server lets people in.',
        keywords: 'ssh keys sshd',
      },
    ],
  },
  {
    title: 'Server Management',
    items: [
      {
        label: 'Server',
        to: '/server',
        permission: Permission.ServerView,
        icon: Server,
        description: 'What this server is: its system, hardware and agent.',
        keywords: 'server information hostname kernel',
      },
      {
        label: 'Services',
        to: '/services',
        permission: Permission.ServerView,
        icon: ServerCog,
        description: 'The daemons this server runs, and whether they start at boot.',
        keywords: 'services management systemd restart',
      },
      {
        label: 'System updates',
        to: '/updates',
        permission: Permission.ServerView,
        icon: PackageSearch,
        description: 'Operating system packages waiting to be updated.',
        keywords: 'apt packages upgrade',
      },
    ],
  },
  {
    title: 'Logs & Notifications',
    items: [
      {
        label: 'Logs',
        to: '/logs',
        permission: Permission.ServerView,
        icon: ScrollText,
        description: 'Server and site logs, live or downloaded.',
        keywords: 'log browser errors access',
      },
      {
        label: 'Audit trail',
        to: '/audit',
        permission: Permission.AuditView,
        icon: ScrollText,
        description: 'Who changed what on this panel, and when.',
        keywords: 'action log history',
      },
      {
        label: 'Notifications',
        to: '/notifications',
        permission: Permission.NotificationManage,
        icon: BellRing,
        description: 'Where alerts and reports are sent.',
        keywords: 'email slack webhook alerts channels',
      },
    ],
  },
];

/** Every destination, sidebar first, each once. */
export const allDestinations: Destination[] = [
  ...sidebarGroups.flatMap((group) => group.items),
  ...toolGroups.flatMap((group) => group.items),
];

const toolPaths = new Set(toolGroups.flatMap((group) => group.items.map((item) => item.to)));

export interface Location {
  destination: Destination;
  /** Set when the page lives on the Tools & Settings page. */
  underTools: boolean;
}

/**
 * locate finds the destination a URL belongs to.
 *
 * A page's own sub-paths belong to it — a website's detail page is part of
 * Websites & Domains — so the longest matching prefix wins, and "/" only ever
 * matches itself.
 */
export function locate(pathname: string): Location | undefined {
  let best: Destination | undefined;
  for (const destination of allDestinations) {
    const matches =
      destination.to === '/'
        ? pathname === '/'
        : pathname === destination.to || pathname.startsWith(`${destination.to}/`);
    if (matches && (!best || destination.to.length > best.to.length)) {
      best = destination;
    }
  }
  return best ? { destination: best, underTools: toolPaths.has(best.to) } : undefined;
}

/**
 * sidebarTarget is the sidebar entry to highlight for a URL.
 *
 * A page reached through Tools & Settings lights up Tools & Settings, the way
 * Plesk does. Otherwise the highlight would vanish the moment somebody opened
 * the Firewall, and nothing on screen would say where they were.
 */
export function sidebarTarget(pathname: string): string | undefined {
  const found = locate(pathname);
  if (!found) {
    return undefined;
  }
  return found.underTools ? TOOLS_PATH : found.destination.to;
}

const MAX_SEARCH_RESULTS = 8;

/**
 * searchDestinations ranks the panel's pages against what somebody typed.
 *
 * A label that starts with the term beats one that merely contains it, which
 * beats a keyword match, which beats the description — so "fire" puts the
 * Firewall first, and "fail2ban" still finds Intrusion prevention.
 */
export function searchDestinations(query: string, can: Can = everything): Destination[] {
  const term = query.trim().toLowerCase();
  if (term === '') {
    return [];
  }
  return allDestinations
    .filter((destination) => allowed(destination, can))
    .map((destination) => {
      const label = destination.label.toLowerCase();
      let score = -1;
      if (label.startsWith(term)) score = 3;
      else if (label.includes(term)) score = 2;
      else if ((destination.keywords ?? '').toLowerCase().includes(term)) score = 1;
      else if (destination.description.toLowerCase().includes(term)) score = 0;
      return { destination, score };
    })
    .filter((entry) => entry.score >= 0)
    .sort((a, b) => b.score - a.score)
    .slice(0, MAX_SEARCH_RESULTS)
    .map((entry) => entry.destination);
}

/** Can reports whether the signed-in person holds a permission. */
export type Can = (permission: PermissionName) => boolean;

/** everything is the Can for when there is no profile to ask. */
export const everything: Can = () => true;

function allowed(destination: Destination, can: Can): boolean {
  return destination.permission === undefined || can(destination.permission);
}

/**
 * visibleGroups keeps the pages somebody can open, and drops a group left
 * with none.
 *
 * Cosmetic, not a control: every endpoint checks the same permission. What it
 * stops is a customer meeting a Firewall link that can only ever answer
 * "forbidden".
 */
export function visibleGroups<G extends DestinationGroup>(groups: G[], can: Can): G[] {
  return groups
    .map((group) => ({ ...group, items: group.items.filter((item) => allowed(item, can)) }))
    .filter((group) => group.items.length > 0);
}

/**
 * visibleSidebar is the sidebar for one person. Tools & Settings stays only
 * while at least one tool on it is theirs to open: an entry leading to an
 * empty page would be the same dead end in a different place.
 */
export function visibleSidebar(can: Can): DestinationGroup[] {
  const anyTool = visibleGroups(toolGroups, can).length > 0;
  return visibleGroups(sidebarGroups, can)
    .map((group) => ({
      ...group,
      items: group.items.filter((item) => item.to !== TOOLS_PATH || anyTool),
    }))
    .filter((group) => group.items.length > 0);
}
