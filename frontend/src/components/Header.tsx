import { PanelLeftClose, PanelLeftOpen } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { useApiHealth } from '@/features/system/hooks';
import { useUiStore } from '@/stores/uiStore';

export function Header() {
  const collapsed = useUiStore((state) => state.sidebarCollapsed);
  const toggleSidebar = useUiStore((state) => state.toggleSidebar);
  const { data, isPending, isError } = useApiHealth();

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
        {data?.version && <span className="text-xs text-slate-500">v{data.version}</span>}
      </div>
    </header>
  );
}
