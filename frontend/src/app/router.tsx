import { createBrowserRouter } from 'react-router-dom';

import { RequireAuth } from '@/features/auth/components/RequireAuth';
import { AppLayout } from '@/layouts/AppLayout';
import { DashboardPage } from '@/pages/DashboardPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { WebsiteDetailPage } from '@/pages/WebsiteDetailPage';
import { WebsitesPage } from '@/pages/WebsitesPage';
import { LoginPage } from '@/pages/auth/LoginPage';
import { SecurityPage } from '@/pages/auth/SecurityPage';

// Opt into React Router v7 behaviour now so the upgrade is not a breaking
// change later in the project.
const routerFuture = {
  v7_relativeSplatPath: true,
  v7_startTransition: true,
} as const;

export const router = createBrowserRouter(
  [
    { path: '/login', element: <LoginPage /> },
    {
      // Everything below requires a session. The API enforces this too; the
      // guard only avoids rendering a shell the user cannot use.
      element: <RequireAuth />,
      children: [
        {
          path: '/',
          element: <AppLayout />,
          children: [
            { index: true, element: <DashboardPage /> },
            { path: 'websites', element: <WebsitesPage /> },
            { path: 'websites/:id', element: <WebsiteDetailPage /> },
            { path: 'security', element: <SecurityPage /> },
            { path: '*', element: <NotFoundPage /> },
          ],
        },
      ],
    },
  ],
  { future: routerFuture },
);
