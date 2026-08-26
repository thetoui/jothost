import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  ExternalLink,
  FileCode2,
  Folder,
  Globe,
  Plus,
  Search,
  Settings2,
  UserRound,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, TintedIcon } from '@/components/ui/Card';
import { EmptyState, ProgressBar, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { CreateWebsiteForm } from '@/features/websites/components/CreateWebsiteForm';
import { useWebsites } from '@/features/websites/hooks';
import { websiteStatusPill } from '@/features/websites/status';
import type { Website } from '@/types/api';

/** WebsitesPage lists hosted sites and creates new ones. */
export function WebsitesPage() {
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState('');
  const { data, isPending, isError, error } = useWebsites();

  const websites = data?.websites ?? [];
  const term = query.trim().toLowerCase();
  const visible = term
    ? websites.filter(
        (site) =>
          site.primary_domain.includes(term) || (site.name ?? '').toLowerCase().includes(term),
      )
    : websites;

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Websites</h1>
          <p className="mt-1 text-sm text-slate-500">
            Sites hosted on this server. Each runs under its own system account.
          </p>
        </div>

        <div className="flex items-center gap-2">
          {websites.length > 0 && (
            <div className="relative">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400"
              />
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search domains"
                aria-label="Search websites"
                className="h-9 w-48 rounded-md border border-surface-border bg-surface pl-8 pr-3 text-sm shadow-card placeholder:text-slate-400 focus:border-brand-500 focus:outline-none focus:ring-2 focus:ring-brand-500/30"
              />
            </div>
          )}

          <RequirePermission permission={Permission.WebsiteCreate}>
            <Button
              variant="primary"
              onClick={() => setCreating(true)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              New website
            </Button>
          </RequirePermission>
        </div>
      </header>

      {isError && (
        <Alert tone="danger" title="The website list could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {isPending && (
        <Card>
          <SkeletonRows rows={3} />
        </Card>
      )}

      {!isPending && websites.length === 0 && !isError && (
        <Card>
          <EmptyState
            icon={<Globe className="h-6 w-6" />}
            title="No websites yet"
            description="Create one to serve a domain from this server."
            action={
              <RequirePermission permission={Permission.WebsiteCreate}>
                <Button
                  variant="primary"
                  onClick={() => setCreating(true)}
                  icon={<Plus aria-hidden="true" className="h-4 w-4" />}
                >
                  New website
                </Button>
              </RequirePermission>
            }
          />
        </Card>
      )}

      {!isPending && websites.length > 0 && visible.length === 0 && (
        <Card>
          <EmptyState
            icon={<Search className="h-6 w-6" />}
            title="No matching websites"
            description={`Nothing matches “${query.trim()}”.`}
            action={<Button onClick={() => setQuery('')}>Clear search</Button>}
          />
        </Card>
      )}

      {visible.length > 0 && (
        <ul className="space-y-3">
          {visible.map((site) => (
            <li key={site.id}>
              <WebsiteCard site={site} />
            </li>
          ))}
        </ul>
      )}

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="New website"
        description="The site is provisioned with its own directory, system account, and web server configuration."
      >
        <CreateWebsiteForm onCreated={() => setCreating(false)} onCancel={() => setCreating(false)} />
      </Modal>
    </div>
  );
}

/**
 * WebsiteCard is one site, with the shortcuts an operator reaches for.
 *
 * The tools row is the point: a hosting panel is judged by how few clicks it
 * takes to get from "which sites do I have" to "change this one".
 */
function WebsiteCard({ site }: { site: Website }) {
  const pill = websiteStatusPill(site.status);
  const settling = site.status === 'creating' || site.status === 'deleting';

  return (
    <Card className="overflow-hidden transition-shadow hover:shadow-raised">
      <div className="flex flex-wrap items-start justify-between gap-4 p-4">
        <div className="flex min-w-0 items-start gap-3">
          <TintedIcon
            tone={site.status === 'failed' ? 'danger' : settling ? 'warn' : 'brand'}
            icon={<Globe className="h-4 w-4" />}
          />

          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <Link
                to={`/websites/${site.id}`}
                className="truncate text-sm font-semibold text-slate-900 hover:text-brand-700"
              >
                {site.primary_domain}
              </Link>
              <StatusPill label={pill.label} tone={pill.tone} dot pulse={settling} />
            </div>

            {site.name && <p className="mt-0.5 truncate text-xs text-slate-500">{site.name}</p>}

            <dl className="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs text-slate-500">
              <div className="flex items-center gap-1.5">
                <Folder aria-hidden="true" className="h-3.5 w-3.5 text-slate-400" />
                <dt className="sr-only">Document root</dt>
                <dd className="font-mono">{site.document_root}</dd>
              </div>
              <div className="flex items-center gap-1.5">
                <UserRound aria-hidden="true" className="h-3.5 w-3.5 text-slate-400" />
                <dt className="sr-only">System user</dt>
                <dd className="font-mono">{site.system_user}</dd>
              </div>
              <div className="flex items-center gap-1.5">
                <FileCode2 aria-hidden="true" className="h-3.5 w-3.5 text-slate-400" />
                <dt className="sr-only">PHP</dt>
                <dd>{site.php_version ? `PHP ${site.php_version}` : 'Static'}</dd>
              </div>
            </dl>
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            // Opened over plain HTTP because SSL is a later phase; noreferrer
            // keeps the panel's URL out of the site's logs.
            onClick={() => window.open(`http://${site.primary_domain}`, '_blank', 'noreferrer')}
            icon={<ExternalLink aria-hidden="true" className="h-3.5 w-3.5" />}
          >
            Open
          </Button>
          <Link to={`/websites/${site.id}`}>
            <Button
              variant="secondary"
              size="sm"
              icon={<Settings2 aria-hidden="true" className="h-3.5 w-3.5" />}
            >
              Manage
            </Button>
          </Link>
        </div>
      </div>

      {/* A site mid-change gets a live bar rather than a static badge alone, so
          the list shows that something is happening without being refreshed. */}
      {settling && (
        <ProgressBar
          label={`${site.primary_domain} is ${site.status}`}
          tone={site.status === 'deleting' ? 'danger' : 'brand'}
          className="rounded-none"
        />
      )}
    </Card>
  );
}
