import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Globe, Plus } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Permission } from '@/features/auth/permissions';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { CreateWebsiteForm } from '@/features/websites/components/CreateWebsiteForm';
import { useWebsites } from '@/features/websites/hooks';
import { websiteStatusPill } from '@/features/websites/status';

/** WebsitesPage lists hosted sites and creates new ones. */
export function WebsitesPage() {
  const [creating, setCreating] = useState(false);
  const { data, isLoading, isError, error } = useWebsites();

  const websites = data?.websites ?? [];

  return (
    <div className="space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Websites</h1>
          <p className="mt-1 text-sm text-slate-500">
            Sites hosted on this server. Creating one provisions its directories, system
            account, and web server configuration.
          </p>
        </div>

        <RequirePermission permission={Permission.WebsiteCreate}>
          <button
            type="button"
            onClick={() => setCreating((open) => !open)}
            className="inline-flex shrink-0 items-center gap-2 rounded-md bg-brand-600 px-3 py-2 text-sm font-medium text-white shadow-sm hover:bg-brand-700"
          >
            <Plus aria-hidden="true" className="h-4 w-4" />
            New website
          </button>
        </RequirePermission>
      </header>

      {creating && (
        <section
          aria-label="Create a website"
          className="rounded-lg border border-surface-border bg-surface p-4 shadow-sm"
        >
          <CreateWebsiteForm
            onCreated={() => setCreating(false)}
            onCancel={() => setCreating(false)}
          />
        </section>
      )}

      {isError && (
        <p role="alert" className="text-sm text-rose-600">
          {error instanceof Error ? error.message : 'The website list could not be loaded.'}
        </p>
      )}

      {isLoading && <p className="text-sm text-slate-500">Loading websites…</p>}

      {!isLoading && websites.length === 0 && !isError && (
        <div className="rounded-lg border border-dashed border-surface-border p-8 text-center">
          <Globe aria-hidden="true" className="mx-auto h-8 w-8 text-slate-300" />
          <p className="mt-2 text-sm font-medium text-slate-900">No websites yet</p>
          <p className="mt-1 text-sm text-slate-500">
            Create one to serve a domain from this server.
          </p>
        </div>
      )}

      {websites.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-surface-border bg-surface shadow-sm">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Hosted websites</caption>
            <thead className="border-b border-surface-border bg-surface-muted text-xs uppercase tracking-wide text-slate-500">
              <tr>
                <th scope="col" className="px-4 py-2 font-medium">
                  Domain
                </th>
                <th scope="col" className="px-4 py-2 font-medium">
                  Status
                </th>
                <th scope="col" className="px-4 py-2 font-medium">
                  Document root
                </th>
                <th scope="col" className="px-4 py-2 font-medium">
                  System user
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-surface-border">
              {websites.map((site) => {
                const pill = websiteStatusPill(site.status);
                return (
                  <tr key={site.id} className="hover:bg-surface-muted">
                    <td className="px-4 py-3">
                      <Link
                        to={`/websites/${site.id}`}
                        className="font-medium text-brand-700 hover:underline"
                      >
                        {site.primary_domain}
                      </Link>
                      {site.name && <p className="text-xs text-slate-500">{site.name}</p>}
                    </td>
                    <td className="px-4 py-3">
                      <StatusPill label={pill.label} tone={pill.tone} />
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-slate-600">
                      {site.document_root}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-slate-600">
                      {site.system_user}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
