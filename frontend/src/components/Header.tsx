import { LogOut, PanelLeftClose, PanelLeftOpen } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { useLogout, useProfile } from '@/features/auth/hooks';
import { useApiHealth } from '@/features/system/hooks';
import { useUiStore } from '@/stores/uiStore';

export function Header() {
  const collapsed = useUiStore((state) => state.sidebarCollapsed);
  const toggleSidebar = useUiStore((state) => state.toggleSidebar);
  const { data: health, isPending, isError } = useApiHealth();
  const { data: profile } = useProfile();
  const logout = useLogout();

  const apiTone = isPending ? 'neutral' : isError ? 'error' : 'ok';
  const apiLabel = isPending ? 'API checking' : isError ? 'API unreachable' : 'API online';

  return (
    <header className="flex h-14 shrink-0 items-center justify-between border-b border-surface-border bg-surface px-4">
      <button
        type="button"
        onClick={toggleSidebar}
        aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        className="rounded-md p-2 text-slate-500 hover:bg-surface-muted hover:text-slate-900"
      >
        {collapsed ? (
          <PanelLeftOpen aria-hidden="true" className="h-4 w-4" />
        ) : (
          <PanelLeftClose aria-hidden="true" className="h-4 w-4" />
        )}
      </button>

      <div className="flex items-center gap-3">
        <StatusPill label={apiLabel} tone={apiTone} />
        {health?.version && <span className="text-xs text-slate-500">v{health.version}</span>}

        {profile && (
          <>
            <span className="hidden text-sm text-slate-700 sm:inline">{profile.username}</span>
            <button
              type="button"
              onClick={() => logout.mutate()}
              disabled={logout.isPending}
              aria-label="Sign out"
              className="rounded-md p-2 text-slate-500 hover:bg-surface-muted hover:text-slate-900 disabled:opacity-60"
            >
              <LogOut aria-hidden="true" className="h-4 w-4" />
            </button>
          </>
        )}
      </div>
    </header>
  );
}
