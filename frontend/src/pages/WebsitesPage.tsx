import { useCallback, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Clock,
  Database,
  FileCode2,
  FolderOpen,
  Globe,
  HardDrive,
  Lock,
  Plus,
  Search,
  ShieldCheck,
} from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { CreateWebsiteForm } from '@/features/websites/components/CreateWebsiteForm';
import { DomainList } from '@/features/websites/components/DomainList';
import { useWebsites } from '@/features/websites/hooks';
import { useDashboard } from '@/features/dashboard/hooks';
import type { Website } from '@/types/api';

/**
 * WebsitesPage is the panel's "Websites & Domains" screen.
 *
 * The arrangement follows what a hosting operator already knows from Plesk: a
 * list of domains that expands in place into that domain's tools, with the
 * server's own summary kept to one side. The value is not the resemblance — it
 * is that the shape is already in the muscle memory of the people who run these
 * machines.
 */
export function WebsitesPage() {
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState('');
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const { data, isPending, isError, error } = useWebsites();

  const websites = data?.websites ?? [];
  const term = query.trim().toLowerCase();
  const visible = term
    ? websites.filter(
        (site) =>
          site.primary_domain.includes(term) || (site.name ?? '').toLowerCase().includes(term),
      )
    : websites;

  const toggle = useCallback((id: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  return (
    <div className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold text-slate-900">Websites &amp; Domains</h1>
        <p className="mt-1 text-sm text-slate-500">
          {websites.length} {websites.length === 1 ? 'item' : 'items'} total. Each site runs under
          its own system account.
        </p>
      </header>

      <div className="grid gap-4 xl:grid-cols-[1fr,17rem]">
        <div className="min-w-0 space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <RequirePermission permission={Permission.WebsiteCreate}>
                <Button
                  variant="primary"
                  onClick={() => setCreating(true)}
                  icon={<Plus aria-hidden="true" className="h-4 w-4" />}
                >
                  Add Website
                </Button>
              </RequirePermission>
              {/* Plesk offers Add Subdomain and Add Domain Alias beside this.
                  They are not shown at all rather than shown dead: a button
                  that does nothing is worse than an absent one. */}
            </div>

            <div className="relative">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400"
              />
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Find domain..."
                aria-label="Find domain"
                className="h-9 w-56 rounded-md border border-surface-border bg-surface pl-8 pr-3 text-sm shadow-card placeholder:text-slate-400 focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
              />
            </div>
          </div>

          {isError && (
            <Alert tone="danger" title="The website list could not be loaded">
              {error instanceof Error ? error.message : 'Try again in a moment.'}
            </Alert>
          )}

          <Card label="Domains">
            {isPending ? (
              <SkeletonRows rows={4} />
            ) : websites.length === 0 ? (
              <EmptyState
                icon={<Globe className="h-6 w-6" />}
                title="No websites yet"
                description="Add one to serve a domain from this server."
                action={
                  <RequirePermission permission={Permission.WebsiteCreate}>
                    <Button
                      variant="primary"
                      onClick={() => setCreating(true)}
                      icon={<Plus aria-hidden="true" className="h-4 w-4" />}
                    >
                      Add Website
                    </Button>
                  </RequirePermission>
                }
              />
            ) : visible.length === 0 ? (
              <EmptyState
                icon={<Search className="h-6 w-6" />}
                title="No matching domains"
                description={`Nothing matches “${query.trim()}”.`}
                action={<Button onClick={() => setQuery('')}>Clear search</Button>}
              />
            ) : (
              <DomainList sites={visible} expanded={expanded} onToggle={toggle} />
            )}
          </Card>
        </div>

        <ServerRail sites={websites} />
      </div>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="Add Website"
        description="The site is provisioned with its own directory, system account, and web server configuration."
      >
        <CreateWebsiteForm onCreated={() => setCreating(false)} onCancel={() => setCreating(false)} />
      </Modal>
    </div>
  );
}

/**
 * ServerRail is the column Plesk keeps to the right of the domain list: the
 * shortcuts that apply to the whole server, then what the server itself is.
 */
function ServerRail({ sites }: { sites: Website[] }) {
  // The server's own facts come from the dashboard snapshot rather than a
  // second endpoint: it is already cached, and this rail is a summary of the
  // same thing the dashboard shows.
  const dashboard = useDashboard();
  const system = dashboard.data?.system;
  const info = system?.available ? system.data : undefined;

  const withPHP = sites.filter((site) => site.php_version !== null).length;
  const withTLS = sites.filter((site) => site.ssl_enabled).length;

  return (
    <aside className="space-y-3" aria-label="Server">
      <Card label="Shortcuts">
        <nav className="p-2">
          <RailLink to="/files" icon={<FolderOpen className="h-4 w-4" />} label="Files" />
          <RailLink to="/editor" icon={<FileCode2 className="h-4 w-4" />} label="Code editor" />
          <RailLink to="/php" icon={<FileCode2 className="h-4 w-4" />} label="PHP" />
          <RailLink to="/ssl" icon={<Lock className="h-4 w-4" />} label="SSL/TLS" />
          <RailLink to="/databases" icon={<Database className="h-4 w-4" />} label="Databases" />
          <RailDisabled icon={<Clock className="h-4 w-4" />} label="Scheduled tasks" phase="Phase 10" />
          <RailDisabled icon={<HardDrive className="h-4 w-4" />} label="Backup & restore" phase="Phase 14" />
        </nav>
      </Card>

      <Card label="System overview">
        <div className="space-y-2 p-3 text-sm">
          <h3 className="text-sm font-semibold text-slate-900">System Overview</h3>
          <dl className="space-y-1.5 text-xs">
            <RailFact label="Hostname" value={info?.hostname ?? '—'} />
            <RailFact
              label="OS"
              value={info ? `${info.os_name} ${info.os_version}` : '—'}
            />
            <RailFact label="Kernel" value={info?.kernel_version ?? '—'} />
            <RailFact label="Sites" value={String(sites.length)} />
            <RailFact label="Running PHP" value={`${withPHP} of ${sites.length}`} />
            <RailFact label="Secured" value={`${withTLS} of ${sites.length}`} />
          </dl>
        </div>
      </Card>

      <Card label="System security">
        <div className="space-y-2 p-3">
          <h3 className="text-sm font-semibold text-slate-900">System Security</h3>
          {/* Only what this build actually enforces. Plesk lists ModSecurity
              and IP banning here; claiming either would be a lie. */}
          <ul className="space-y-1.5 text-xs">
            <SecurityRow label="Per-site system accounts" on />
            <SecurityRow label="HTTPS on every site" on={withTLS === sites.length && sites.length > 0} />
            <SecurityRow label="Firewall" on={false} note="Phase 16" />
            <SecurityRow label="Fail2Ban" on={false} note="Phase 18" />
          </ul>
        </div>
      </Card>
    </aside>
  );
}

function RailLink({ to, icon, label }: { to: string; icon: React.ReactNode; label: string }) {
  return (
    <Link
      to={to}
      className="flex items-center gap-2 rounded px-2 py-1.5 text-sm text-slate-700 transition-colors hover:bg-surface-sunken hover:text-brand-700"
    >
      <span className="text-slate-400" aria-hidden="true">
        {icon}
      </span>
      {label}
    </Link>
  );
}

function RailDisabled({
  icon,
  label,
  phase,
}: {
  icon: React.ReactNode;
  label: string;
  phase: string;
}) {
  return (
    <span
      title={`Added in ${phase}`}
      className="flex cursor-not-allowed items-center gap-2 rounded px-2 py-1.5 text-sm text-slate-400"
    >
      <span aria-hidden="true">{icon}</span>
      {label}
      <span className="ml-auto text-[11px]">{phase}</span>
    </span>
  );
}

function RailFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-2">
      <dt className="text-slate-500">{label}</dt>
      <dd className="truncate font-medium text-slate-800">{value}</dd>
    </div>
  );
}

function SecurityRow({ label, on, note }: { label: string; on: boolean; note?: string }) {
  return (
    <li className="flex items-center justify-between gap-2">
      <span className="text-slate-600">{label}</span>
      <span
        className={[
          'inline-flex items-center gap-1 font-medium',
          on ? 'text-ok-700' : 'text-slate-400',
        ].join(' ')}
      >
        <ShieldCheck aria-hidden="true" className="h-3.5 w-3.5" />
        {on ? 'On' : (note ?? 'Off')}
      </span>
    </li>
  );
}
