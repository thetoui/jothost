import { Suspense, lazy } from 'react';
import { createBrowserRouter } from 'react-router-dom';

import { Spinner } from '@/components/ui/Loading';
import { RequireAuth } from '@/features/auth/components/RequireAuth';
import { AppLayout } from '@/layouts/AppLayout';
import { DashboardPage } from '@/pages/DashboardPage';
import { DatabasesPage } from '@/pages/DatabasesPage';
import { FilesPage } from '@/pages/FilesPage';
import { NodePage } from '@/pages/NodePage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { PHPPage } from '@/pages/PHPPage';
import { WebserverPage } from '@/pages/WebserverPage';
import { SSLPage } from '@/pages/SSLPage';
import { WebsiteDetailPage } from '@/pages/WebsiteDetailPage';
import { WebsitesPage } from '@/pages/WebsitesPage';
import { LoginPage } from '@/pages/auth/LoginPage';
import { SecurityPage } from '@/pages/auth/SecurityPage';

/**
 * The editor is loaded on demand.
 *
 * Monaco is roughly 3.5 MB of JavaScript. Imported directly it lands in the
 * main bundle, which means the login page ships a code editor to someone who
 * has not signed in yet. Splitting it here keeps the rest of the panel at the
 * size it was before Phase 7.5 and costs one short load the first time somebody
 * opens the editor.
 */
const EditorPage = lazy(() =>
  import('@/pages/EditorPage').then((module) => ({ default: module.EditorPage })),
);

/**
 * routeFallback fills the layout while a split route loads.
 *
 * An element rather than a component: this file's export is the router, and a
 * component declared beside it costs fast refresh for the whole module.
 */
const routeFallback = (
  <div className="flex min-h-[24rem] items-center justify-center">
    <Spinner label="Loading" />
  </div>
);

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
            { path: 'files', element: <FilesPage /> },
            {
              path: 'editor',
              element: (
                <Suspense fallback={routeFallback}>
                  <EditorPage />
                </Suspense>
              ),
            },
            { path: 'php', element: <PHPPage /> },
            { path: 'webserver', element: <WebserverPage /> },
            { path: 'databases', element: <DatabasesPage /> },
            { path: 'node', element: <NodePage /> },
            { path: 'ssl', element: <SSLPage /> },
            { path: 'security', element: <SecurityPage /> },
            { path: '*', element: <NotFoundPage /> },
          ],
        },
      ],
    },
  ],
  { future: routerFuture },
);
