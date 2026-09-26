import { Navigate } from 'react-router-dom';

import { visibleSidebar } from '@/components/navigation';
import { useCan } from '@/features/auth/hooks';
import { Permission } from '@/features/auth/permissions';
import { DashboardPage } from '@/pages/DashboardPage';

/**
 * HomePage is where signing in lands.
 *
 * The dashboard, for anybody who can see the server. Everybody else went there
 * too, and a customer's first screen after signing in was "Permission denied:
 * server.view". They are sent to the first page they can open instead —
 * Websites & Domains for a hosting customer, which is also where Plesk puts
 * them — and to their own account security when they can open nothing else.
 */
export function HomePage() {
  const can = useCan();
  if (can(Permission.ServerView)) {
    return <DashboardPage />;
  }
  const first = visibleSidebar(can)
    .flatMap((group) => group.items)
    .find((item) => item.to !== '/');
  return <Navigate to={first?.to ?? '/security'} replace />;
}
