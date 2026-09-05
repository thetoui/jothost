import { useEffect, useRef, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { ChevronRight, LogOut, PanelLeftClose, PanelLeftOpen, UserCog } from 'lucide-react';

import { Button } from '@/components/ui/Button';
import { MenuItem, MenuPanel } from '@/components/ui/Menu';
import { focusRing, focusRingTight } from '@/components/ui/focus';
import { useLogout, useProfile } from '@/features/auth/hooks';
import { useApiHealth } from '@/features/system/hooks';
import { useUiStore } from '@/stores/uiStore';

export function Header() {
  const collapsed = useUiStore((state) => state.sidebarCollapsed);
  const toggleSidebar = useUiStore((state) => state.toggleSidebar);
  const { data: health, isPending, isError } = useApiHealth();
  const { data: profile } = useProfile();

  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-4 border-b border-surface-border bg-surface px-4">
      <div className="flex min-w-0 items-center gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={toggleSidebar}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          className="px-1.5"
          icon={
            collapsed ? (
              <PanelLeftOpen aria-hidden="true" className="h-4 w-4" />
            ) : (
              <PanelLeftClose aria-hidden="true" className="h-4 w-4" />
            )
          }
        />
        <Breadcrumbs />
      </div>

      <div className="flex shrink-0 items-center gap-3">
        <ApiStatus pending={isPending} failed={isError} version={health?.version} />
        {profile && <UserMenu username={profile.username} roles={profile.roles} />}
      </div>
    </header>
  );
}

/**
 * ApiStatus is a live dot rather than a labelled pill.
 *
 * It sits in the header on every page, so it has to be readable at a glance
 * and quiet when everything is fine — a full-width badge saying "API online"
 * spends the reader's attention on the least surprising fact on the screen.
 */
function ApiStatus({
  pending,
  failed,
  version,
}: {
  pending: boolean;
  failed: boolean;
  // Explicitly nullable: exactOptionalPropertyTypes distinguishes "absent"
  // from "present and undefined", and health data arrives as the latter.
  version: string | undefined;
}) {
  const tone = pending ? 'bg-slate-300' : failed ? 'bg-danger-500' : 'bg-ok-500';
  const label = pending ? 'Checking the API' : failed ? 'API unreachable' : 'API online';

  return (
    // One live region holding the label once. Rendering it again in an sr-only
    // span made every screen reader announce the status twice.
    <div role="status" className="flex items-center gap-2" title={label}>
      <span aria-hidden="true" className="relative flex h-2 w-2">
        {!pending && !failed && (
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-ok-500 opacity-60" />
        )}
        <span className={`relative inline-flex h-2 w-2 rounded-full ${tone}`} />
      </span>
      {/* Hidden visually on narrow viewports, but never hidden from assistive
          technology: the dot alone carries no meaning without it. */}
      <span className="sr-only text-xs text-slate-500 sm:not-sr-only">{label}</span>
      {version && <span className="hidden text-xs text-slate-400 md:inline">v{version}</span>}
    </div>
  );
}

/** Human labels for the first path segment. */
const SEGMENT_LABELS: Record<string, string> = {
  websites: 'Websites',
  php: 'PHP',
  security: 'Account security',
};

/**
 * Breadcrumbs show where the reader is inside the panel.
 *
 * Only the first segment is named; a website's id is not a useful crumb, so a
 * detail page shows its parent and leaves the page's own heading to say which
 * record is open.
 */
function Breadcrumbs() {
  const { pathname } = useLocation();
  const segments = pathname.split('/').filter(Boolean);

  if (segments.length === 0) {
    return <span className="truncate text-sm font-medium text-slate-900">Dashboard</span>;
  }

  const [first] = segments;
  const label = SEGMENT_LABELS[first ?? ''] ?? first;
  const isDetail = segments.length > 1;

  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1 text-sm">
      <Link to="/" className={`shrink-0 rounded-sm text-slate-500 hover:text-slate-900 ${focusRingTight}`}>
        Dashboard
      </Link>
      <ChevronRight aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-slate-300" />
      {isDetail ? (
        <Link
          to={`/${first}`}
          className={`truncate rounded-sm text-slate-500 hover:text-slate-900 ${focusRingTight}`}
        >
          {label}
        </Link>
      ) : (
        <span aria-current="page" className="truncate font-medium text-slate-900">
          {label}
        </span>
      )}
    </nav>
  );
}

/** UserMenu holds the actions that belong to the signed-in person. */
function UserMenu({ username, roles }: { username: string; roles: string[] }) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const logout = useLogout();

  // A menu that stays open after a click elsewhere follows the reader around
  // the page.
  useEffect(() => {
    if (!open) {
      return;
    }

    function handlePointerDown(event: MouseEvent) {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    }
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    }

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="menu"
        aria-expanded={open}
        className={`flex items-center gap-2 rounded-md py-1 pl-1 pr-2 text-sm transition-colors hover:bg-surface-sunken ${focusRing}`}
      >
        <span
          aria-hidden="true"
          className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-brand-100 text-xs font-semibold uppercase text-brand-700"
        >
          {username.slice(0, 2)}
        </span>
        <span className="hidden max-w-32 truncate text-slate-700 sm:inline">{username}</span>
      </button>

      {open && (
        <MenuPanel label={`Account menu for ${username}`}>
          <div className="border-b border-surface-border px-3.5 py-2.5">
            <p className="truncate text-sm font-medium text-slate-900">{username}</p>
            <p className="mt-0.5 truncate text-xs capitalize text-slate-500">
              {roles.length > 0 ? roles.join(', ') : 'No role assigned'}
            </p>
          </div>

          <div className="p-1">
            <MenuItem
              to="/security"
              icon={<UserCog className="h-4 w-4" />}
              onClick={() => setOpen(false)}
            >
              Account security
            </MenuItem>
            <MenuItem
              icon={<LogOut className="h-4 w-4" />}
              onClick={() => logout.mutate()}
              disabled={logout.isPending}
            >
              {logout.isPending ? 'Signing out…' : 'Sign out'}
            </MenuItem>
          </div>
        </MenuPanel>
      )}
    </div>
  );
}
