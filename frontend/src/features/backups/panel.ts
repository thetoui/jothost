import { hasPermission, Permission } from '@/features/auth/permissions';
import type { BackupCapabilities, BackupType, UserProfile } from '@/types/api';

/**
 * Backups of the panel's own database (docs/PANEL_BACKUP.md).
 *
 * The API enforces all of this; what lives here is only what the page shows,
 * so that nobody is offered a button the API is going to refuse.
 */

export const PANEL_BACKUP: BackupType = 'panel';

/** What the backup forms should do about the panel option. */
export interface PanelBackupOption {
  /** Whether to list the option at all. */
  offered: boolean;
  /** Whether it can be chosen. */
  usable: boolean;
  /** Why it cannot, when it is listed but not usable. */
  reason?: string;
}

/**
 * panelBackupOption decides how a form offers a panel backup.
 *
 * Not offered without server.manage: a panel backup holds every account's
 * password hash, and the operator role that has backup.manage is deliberately
 * withheld that. Offered but unusable when the host cannot take one, with the
 * reason shown - an option that silently disappears reads as a feature that
 * does not exist.
 */
export function panelBackupOption(
  capabilities: BackupCapabilities,
  profile: UserProfile | undefined,
): PanelBackupOption {
  if (!hasPermission(profile, Permission.ServerManage)) {
    return { offered: false, usable: false };
  }
  if (!capabilities.panel) {
    return {
      offered: true,
      usable: false,
      reason: capabilities.panel_reason ?? "This host cannot back up the panel's database.",
    };
  }
  return { offered: true, usable: true };
}

/** isPanelBackup reports whether a backup or schedule is of the panel itself. */
export function isPanelBackup(item: { type: BackupType }): boolean {
  return item.type === PANEL_BACKUP;
}

/** How a panel backup is restored, since the panel cannot restore itself. */
export const PANEL_RESTORE_HINT =
  'Restored from the host with install.sh restore-panel, with the panel stopped.';
