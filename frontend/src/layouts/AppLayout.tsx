import { Outlet } from 'react-router-dom';

import { ErrorBoundary } from '@/components/ErrorBoundary';
import { Header } from '@/components/Header';
import { Sidebar } from '@/components/Sidebar';
import { ImpersonationBanner } from '@/features/tenancy/components/ImpersonationBanner';

/**
 * Desktop-first shell: a rail that stays put, a header that stays put, and a
 * page that scrolls.
 *
 * The page scrolls with the *document*, which is worth stating because it used
 * to scroll inside <main>. That looks identical and behaves differently: the
 * wheel only works while the pointer is over the one element that scrolls, so
 * it did nothing over the rail or the header, and nothing at all on a short
 * page whose content had not yet overflowed. Sticky positioning gets the same
 * layout without taking the scroll away from the window.
 */
export function AppLayout() {
  return (
    <div className="flex min-h-screen bg-surface-muted">
      {/* The rail scrolls its own overflowing list of destinations, but is
          otherwise pinned: sticky rather than fixed, so it still occupies its
          column and the content beside it needs no offset. */}
      <div className="sticky top-0 h-screen shrink-0 self-start">
        <Sidebar />
      </div>

      <div className="flex min-w-0 flex-1 flex-col">
        {/* z-30 so the header passes over page content rather than under it,
            and under the z-40 menus it opens. */}
        <div className="sticky top-0 z-30">
          <Header />
          {/* Above the content and below the header, so it is on every page.
              The failure mode of impersonation is forgetting you are in it. */}
          <ImpersonationBanner />
        </div>
        <main className="flex-1">
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
