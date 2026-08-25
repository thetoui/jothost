import type { ReactNode } from 'react';

import { useProfile } from '@/features/auth/hooks';
import { hasPermission, type PermissionName } from '@/features/auth/permissions';

interface RequirePermissionProps {
  permission: PermissionName;
  children: ReactNode;
  /** Rendered instead of children when the permission is missing. */
  fallback?: ReactNode;
}

/**
 * RequirePermission hides UI the current user cannot act on.
 *
 * Hiding a control is usability, not enforcement: the API checks the same
 * permission on every request, so a user who reaches the endpoint another way
 * is still refused.
 */
export function RequirePermission({ permission, children, fallback = null }: RequirePermissionProps) {
  const { data: profile } = useProfile();

  if (!hasPermission(profile, permission)) {
    return <>{fallback}</>;
  }
  return <>{children}</>;
}
