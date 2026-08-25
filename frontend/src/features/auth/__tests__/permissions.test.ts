import { describe, expect, it } from 'vitest';

import { Permission, hasPermission } from '@/features/auth/permissions';
import type { UserProfile } from '@/types/api';

function profile(permissions: string[]): UserProfile {
  return {
    id: 'u1',
    username: 'admin',
    email: null,
    status: 'active',
    roles: [],
    permissions,
    two_factor_enabled: false,
    last_login_at: null,
    created_at: '2026-01-01T00:00:00Z',
  };
}

describe('hasPermission', () => {
  it('accepts an exact match', () => {
    expect(hasPermission(profile([Permission.WebsiteCreate]), Permission.WebsiteCreate)).toBe(true);
  });

  it('requires an exact match', () => {
    // Mirrors the server: no prefix or wildcard matching, ever.
    const user = profile(['website.create.draft', 'website', 'WEBSITE.CREATE', 'website.*']);
    expect(hasPermission(user, Permission.WebsiteCreate)).toBe(false);
  });

  it('returns false for a missing permission', () => {
    expect(hasPermission(profile([Permission.ServerView]), Permission.UserManage)).toBe(false);
  });

  it('returns false when the profile has not loaded', () => {
    expect(hasPermission(undefined, Permission.ServerView)).toBe(false);
  });
});
