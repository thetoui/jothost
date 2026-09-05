import { Fragment } from 'react';
import {
  ChevronDown,
  ChevronRight,
  ExternalLink,
  FileCode2,
  FolderOpen,
  Globe,
  Lock,
  Settings2,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { IconButton } from '@/components/ui/IconButton';
import { IconLink } from '@/components/ui/Link';
import { focusRingTight } from '@/components/ui/focus';
import { DomainPanel } from '@/features/websites/components/DomainPanel';
import { websiteStatusPill } from '@/features/websites/status';
import type { Website } from '@/types/api';

interface DomainListProps {
  sites: Website[];
  /** Paths of the rows whose panel is open. */
  expanded: Set<string>;
  onToggle: (id: string) => void;
}

/**
 * DomainList is the Websites & Domains table.
 *
 * Each row expands in place rather than navigating away, which is the shape a
 * Plesk operator expects: the list stays put and the site's tools open under
 * it, so working through several sites does not mean a round trip per site.
 */
export function DomainList({ sites, expanded, onToggle }: DomainListProps) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[44rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-slate-500">
            <th scope="col" className="w-8 px-2 py-2">
              <span className="sr-only">Expand</span>
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Domain name
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Status
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              PHP
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              HTTPS
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              <span className="sr-only">Tools</span>
            </th>
          </tr>
        </thead>

        <tbody>
          {sites.map((site) => {
            const open = expanded.has(site.id);
            const pill = websiteStatusPill(site.status);

            return (
              <Fragment key={site.id}>
                <tr
                  className={[
                    'border-b border-surface-border/60 transition-colors',
                    open ? 'bg-brand-50/40' : 'hover:bg-surface-sunken',
                  ].join(' ')}
                >
                  <td className="px-2 py-2 align-middle">
                    <IconButton
                      size="sm"
                      onClick={() => onToggle(site.id)}
                      aria-expanded={open}
                      label={`${open ? 'Collapse' : 'Expand'} ${site.primary_domain}`}
                      icon={
                        open ? (
                          <ChevronDown aria-hidden="true" className="h-4 w-4" />
                        ) : (
                          <ChevronRight aria-hidden="true" className="h-4 w-4" />
                        )
                      }
                    />
                  </td>

                  <td className="px-3 py-2">
                    <div className="flex items-center gap-2">
                      <Globe aria-hidden="true" className="h-4 w-4 shrink-0 text-brand-500" />
                      <button
                        type="button"
                        onClick={() => onToggle(site.id)}
                        className={`truncate rounded-sm font-medium text-slate-800 underline-offset-2 hover:text-brand-700 hover:underline ${focusRingTight}`}
                      >
                        {site.primary_domain}
                      </button>
                      <IconLink
                        href={`http://${site.primary_domain}`}
                        size="sm"
                        label={`Open ${site.primary_domain} in a new tab`}
                        icon={<ExternalLink aria-hidden="true" className="h-3.5 w-3.5" />}
                      />
                    </div>
                  </td>

                  <td className="px-3 py-2">
                    <StatusPill
                      label={pill.label}
                      tone={pill.tone}
                      dot
                      pulse={site.status === 'creating' || site.status === 'deleting'}
                    />
                  </td>

                  <td className="px-3 py-2 text-slate-600">
                    {site.php_version ?? <span className="text-slate-400">—</span>}
                  </td>

                  <td className="px-3 py-2">
                    {site.ssl_enabled ? (
                      <span className="inline-flex items-center gap-1 text-ok-700">
                        <Lock aria-hidden="true" className="h-3.5 w-3.5" />
                        On
                      </span>
                    ) : (
                      <span className="text-slate-400">Off</span>
                    )}
                  </td>

                  {/* The quick actions Plesk puts at the end of a row: the two
                      or three things someone opens a domain for. */}
                  <td className="px-3 py-2">
                    <div className="flex items-center justify-end gap-1">
                      <RowAction
                        to={`/files?path=${encodeURIComponent(site.document_root)}`}
                        label={`Files for ${site.primary_domain}`}
                        icon={<FolderOpen aria-hidden="true" className="h-4 w-4" />}
                      />
                      <RowAction
                        to="/editor"
                        label={`Code editor for ${site.primary_domain}`}
                        icon={<FileCode2 aria-hidden="true" className="h-4 w-4" />}
                      />
                      <RowAction
                        to={`/websites/${site.id}`}
                        label={`Settings for ${site.primary_domain}`}
                        icon={<Settings2 aria-hidden="true" className="h-4 w-4" />}
                      />
                    </div>
                  </td>
                </tr>

                {open && (
                  <tr>
                    <td colSpan={6} className="p-0">
                      <DomainPanel site={site} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function RowAction({ to, label, icon }: { to: string; label: string; icon: React.ReactNode }) {
  return (
    <IconLink to={to} label={label} icon={icon} />
  );
}
