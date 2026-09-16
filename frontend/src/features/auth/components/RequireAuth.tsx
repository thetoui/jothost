import { Navigate, Outlet, useLocation } from 'react-router-dom';
import { Loader2 } from 'lucide-react';

import { useProfile } from '@/features/auth/hooks';
import { useAuthStore } from '@/stores/authStore';

/**
 * RequireAuth gates the authenticated part of the app.
 *
 * This is a routing convenience, not a security control: the API rejects
 * unauthenticated requests regardless of what the client renders.
 */
export function RequireAuth() {
  const status = useAuthStore((state) => state.status);
  const location = useLocation();
  const { isLoading } = useProfile();

  // On a cold reload the stored refresh token has not been validated yet.
  // Redirecting now would sign out a user who is still authenticated.
  if (status === 'unknown' || (status === 'authenticated' && isLoading)) {
    return (
      <div className="flex h-full items-center justify-center" role="status" aria-live="polite">
        <Loader2 aria-hidden="true" className="h-5 w-5 animate-spin text-ink-dim" />
        <span className="sr-only">Checking your session</span>
      </div>
    );
  }

  if (status !== 'authenticated') {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }

  return <Outlet />;
}
