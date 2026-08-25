import { Outlet } from 'react-router-dom';

import { ErrorBoundary } from '@/components/ErrorBoundary';
import { Header } from '@/components/Header';
import { Sidebar } from '@/components/Sidebar';

/** Desktop-first shell: fixed sidebar, sticky header, scrolling content. */
export function AppLayout() {
  return (
    <div className="flex h-full">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Header />
        <main className="flex-1 overflow-y-auto p-6">
          {/* A crash in one page must not take down the shell. */}
          <ErrorBoundary>
            <Outlet />
          </ErrorBoundary>
        </main>
      </div>
    </div>
  );
}
