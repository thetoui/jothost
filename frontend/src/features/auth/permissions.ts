import type { UserProfile } from '@/types/api';

/**
 * Permission names mirrored from api/internal/rbac. The UI uses them to hide
 * controls a user cannot use; the API is what actually enforces access.
 */
export const Permission = {
  ServerView: 'server.view',
  ServerManage: 'server.manage',
  WebsiteView: 'website.view',
  WebsiteCreate: 'website.create',
  WebsiteUpdate: 'website.update',
  WebsiteDelete: 'website.delete',
  DatabaseManage: 'database.manage',
  SSLManage: 'ssl.manage',
  FirewallManage: 'firewall.manage',
  BackupManage: 'backup.manage',
  FileRead: 'file.read',
  FileWrite: 'file.write',
  CronManage: 'cron.manage',
  FTPManage: 'ftp.manage',
  DNSManage: 'dns.manage',
  UpdateManage: 'update.manage',
  MonitorManage: 'monitor.manage',
  AuditView: 'audit.view',
  UserManage: 'user.manage',
} as const;

export type PermissionName = (typeof Permission)[keyof typeof Permission];

/**
 * hasPermission reports whether a profile grants a permission.
 *
 * Matching is exact, mirroring the server: no prefixes, no wildcards. Hiding a
 * control is a usability nicety, never a security boundary.
 */
export function hasPermission(
  profile: UserProfile | undefined,
  permission: PermissionName,
): boolean {
  return profile?.permissions.includes(permission) ?? false;
}
