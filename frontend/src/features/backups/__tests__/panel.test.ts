import { describe, expect, it } from 'vitest';

import { isPanelBackup, panelBackupOption } from '@/features/backups/panel';
import type { BackupCapabilities, UserProfile } from '@/types/api';

function capabilities(overrides: Partial<BackupCapabilities> = {}): BackupCapabilities {
  return {
    available: true,
    local: true,
    s3: true,
    sftp: true,
    mysql_dump: true,
    postgres_dump: true,
    engines: ['postgres'],
    panel: true,
    ...overrides,
  };
}

function profile(permissions: string[]): UserProfile {
  return {
    id: 'user-1',
    username: 'someone',
    email: null,
    status: 'active',
    two_factor_enabled: false,
    recovery_codes_remaining: 0,
    roles: [],
    permissions,
    created_at: '2026-01-01T00:00:00Z',
    last_login_at: null,
  };
}

describe('panelBackupOption', () => {
  it('is not offered without server.manage, whatever the host can do', () => {
    expect(panelBackupOption(capabilities(), profile(['backup.manage']))).toEqual({
      offered: false,
      usable: false,
    });
    expect(panelBackupOption(capabilities(), undefined).offered).toBe(false);
  });

  it('is usable with server.manage on a host that can take one', () => {
    expect(
      panelBackupOption(capabilities(), profile(['backup.manage', 'server.manage'])),
    ).toEqual({ offered: true, usable: true });
  });

  it('is offered but unusable, with a reason, on a host that cannot', () => {
    const option = panelBackupOption(
      capabilities({ panel: false, panel_reason: 'pg_dump is not available' }),
      profile(['backup.manage', 'server.manage']),
    );
    expect(option).toEqual({ offered: true, usable: false, reason: 'pg_dump is not available' });
  });

  it('recognises a panel backup by its type', () => {
    expect(isPanelBackup({ type: 'panel' })).toBe(true);
    expect(isPanelBackup({ type: 'full' })).toBe(false);
  });
});
