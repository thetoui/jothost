import { Outlet } from 'react-router-dom';

import { ErrorBoundary } from '@/components/ErrorBoundary';
import { Header } from '@/components/Header';
import { Sidebar } from '@/components/Sidebar';
import { ImpersonationBanner } from '@/features/tenancy/components/ImpersonationBanner';

/** Desktop-first shell: fixed rail, sticky header, scrolling content. */
export function AppLayout() {
  return (
    <div className="flex h-full bg-surface-muted">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Header />
        {/* Above the content and below the header, so it is on every page.
            The failure mode of impersonation is forgetting you are in it. */}
        <ImpersonationBanner />
        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1400px] p-6">
            {/* A crash in one page must not take down the shell. */}
            <ErrorBoundary>
              <Outlet />
            </ErrorBoundary>
          </div>
        </main>
      </div>
    </div>
  );
}
