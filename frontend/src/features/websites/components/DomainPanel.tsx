import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Activity,
  Clock,
  Database,
  ExternalLink,
  FileCode2,
  FileText,
  FolderOpen,
  GitBranch,
  Globe,
  KeyRound,
  Lock,
  Package,
  Plug,
  ScrollText,
  Settings2,
  ShieldCheck,
  Terminal,
} from 'lucide-react';

import { ToolGroup, ToolTile } from '@/components/ui/ToolTile';
import type { Website } from '@/types/api';

interface DomainPanelProps {
  site: Website;
}

type PanelTab = 'dashboard' | 'hosting';

/**
 * DomainPanel is the dashboard that opens under a domain in the list.
 *
 * It follows the arrangement a Plesk operator already knows: the site's own
 * summary on the left, and its tools grouped into bands on the right, so the
 * thing you came to do is one click from the list rather than a page away.
 */
export function DomainPanel({ site }: DomainPanelProps) {
  const [tab, setTab] = useState<PanelTab>('dashboard');

  const files = `/files?path=${encodeURIComponent(site.document_root)}`;
  const detail = `/websites/${site.id}`;

  return (
    <div className="border-t border-surface-border bg-surface-sunken/40 px-4 py-4">
      <div className="grid gap-5 lg:grid-cols-[17rem,1fr]">
        <DomainSummary site={site} />

        <div className="min-w-0 space-y-4">
          <div role="tablist" aria-label={`${site.primary_domain} sections`} className="flex gap-4 border-b border-surface-border">
            <PanelTabButton active={tab === 'dashboard'} onClick={() => setTab('dashboard')}>
              Dashboard
            </PanelTabButton>
            <PanelTabButton active={tab === 'hosting'} onClick={() => setTab('hosting')}>
              Hosting &amp; DNS
            </PanelTabButton>
          </div>

          {tab === 'dashboard' ? (
            <div className="space-y-4">
              <ToolGroup title="Files &amp; Databases">
                <ToolTile
                  icon={<FolderOpen className="h-4 w-4" />}
                  label="Files"
                  detail={site.document_root}
                  tone="blue"
                  to={files}
                />
                <ToolTile
                  icon={<FileCode2 className="h-4 w-4" />}
                  label="Code editor"
                  detail="Edit files in place"
                  tone="violet"
                  to="/editor"
                />
                <ToolTile
                  icon={<Database className="h-4 w-4" />}
                  label="Databases"
                  detail="Create and grant access"
                  tone="green"
                  to="/databases"
                />
                <ToolTile
                  icon={<Plug className="h-4 w-4" />}
                  label="Connection info"
                  unavailable="Needs FTP, added later"
                />
                <ToolTile
                  icon={<Package className="h-4 w-4" />}
                  label="Backup & restore"
                  unavailable="Added in Phase 14"
                />
                <ToolTile
                  icon={<Terminal className="h-4 w-4" />}
                  label="FTP"
                  unavailable="Not part of this build"
                />
              </ToolGroup>

              <ToolGroup title="Dev Tools">
                <ToolTile
                  icon={<FileCode2 className="h-4 w-4" />}
                  label="PHP"
                  detail={site.php_version ? `Version ${site.php_version}` : 'Static site'}
                  tone={site.php_version ? 'violet' : 'slate'}
                  to={detail}
                />
                <ToolTile
                  icon={<ScrollText className="h-4 w-4" />}
                  label="Logs"
                  detail="Access and error"
                  tone="amber"
                  to={detail}
                />
                <ToolTile
                  icon={<Clock className="h-4 w-4" />}
                  label="Scheduled tasks"
                  unavailable="Added in Phase 10"
                />
                <ToolTile
                  icon={<Activity className="h-4 w-4" />}
                  label="Monitoring"
                  unavailable="Added in Phase 19"
                />
                <ToolTile
                  icon={<GitBranch className="h-4 w-4" />}
                  label="Git"
                  unavailable="Not part of this build"
                />
                <ToolTile
                  icon={<Globe className="h-4 w-4" />}
                  label="Node.js"
                  detail="Run an application"
                  tone="green"
                  to="/node"
                />
              </ToolGroup>

              <ToolGroup title="Security">
                <ToolTile
                  icon={<Lock className="h-4 w-4" />}
                  label="SSL/TLS certificates"
                  detail={site.ssl_enabled ? 'HTTPS is on' : 'Not secured'}
                  tone={site.ssl_enabled ? 'green' : 'rose'}
                  to={detail}
                />
                <ToolTile
                  icon={<KeyRound className="h-4 w-4" />}
                  label="Password-protected directories"
                  unavailable="Not part of this build"
                />
                <ToolTile
                  icon={<ShieldCheck className="h-4 w-4" />}
                  label="Web application firewall"
                  unavailable="Added in Phase 16"
                />
              </ToolGroup>
            </div>
          ) : (
            <HostingFacts site={site} />
          )}
        </div>
      </div>

      {/* The footer strip Plesk puts under a domain: where the site actually
          lives, and who owns it. It answers "what is the path for this domain"
          without making anyone open a settings page. */}
      <dl className="mt-4 flex flex-wrap items-center gap-x-6 gap-y-1 border-t border-surface-border pt-3 text-xs text-slate-500">
        <div className="flex gap-1.5">
          <dt>Website at</dt>
          <dd>
            <Link to={files} className="font-mono text-slate-700 hover:text-brand-700 hover:underline">
              {site.document_root}
            </Link>
          </dd>
        </div>
        <div className="flex gap-1.5">
          <dt>System user</dt>
          <dd className="font-mono text-slate-700">{site.system_user}</dd>
        </div>
        <div className="flex gap-1.5">
          <dt>Logs at</dt>
          <dd className="font-mono text-slate-700">{logsDirFor(site.document_root)}</dd>
        </div>
      </dl>
    </div>
  );
}

/** logsDirFor derives a site's log directory from its document root. */
export function logsDirFor(documentRoot: string): string {
  // The Agent's layout is <root>/public alongside <root>/logs. Deriving it
  // rather than storing it keeps the two from drifting apart in the UI.
  const parent = documentRoot.replace(/\/+$/, '').split('/').slice(0, -1).join('/');
  return parent ? `${parent}/logs` : documentRoot;
}

function PanelTabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={[
        '-mb-px border-b-2 px-0.5 pb-2 text-sm transition-colors',
        active
          ? 'border-brand-600 font-medium text-slate-900'
          : 'border-transparent text-slate-500 hover:text-slate-800',
      ].join(' ')}
    >
      {children}
    </button>
  );
}

function DomainSummary({ site }: { site: Website }) {
  return (
    <div className="space-y-3">
      <div className="rounded-card border border-surface-border bg-surface p-3">
        <div className="flex items-center justify-between">
          <h4 className="text-sm font-semibold text-slate-900">At a glance</h4>
          <a
            href={`http://${site.primary_domain}`}
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1 text-xs text-brand-700 hover:underline"
          >
            Open in web
            <ExternalLink aria-hidden="true" className="h-3 w-3" />
          </a>
        </div>

        <dl className="mt-2 space-y-1.5 text-sm">
          <SummaryRow label="PHP" value={site.php_version ?? 'Off'} />
          <SummaryRow label="HTTPS" value={site.ssl_enabled ? 'On' : 'Off'} />
          <SummaryRow
            label="Redirect"
            value={site.https_redirect ? 'HTTP → HTTPS' : 'None'}
          />
          <SummaryRow label="Aliases" value={String(Math.max(0, (site.domains?.length ?? 1) - 1))} />
        </dl>
      </div>

      <Link
        to={`/websites/${site.id}`}
        className="flex items-center justify-center gap-1.5 rounded-md border border-surface-border bg-surface px-3 py-1.5 text-sm text-slate-700 shadow-card transition-colors hover:bg-surface-muted"
      >
        <Settings2 aria-hidden="true" className="h-4 w-4" />
        Manage this site
      </Link>
    </div>
  );
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-slate-500">{label}</dt>
      <dd className="truncate font-medium text-slate-800">{value}</dd>
    </div>
  );
}

/**
 * HostingFacts is the "Hosting & DNS" tab.
 *
 * It shows what this build actually knows about how the site is served. Plesk
 * puts DNS here too; this panel does not manage DNS yet, and says so rather
 * than showing an empty section.
 */
function HostingFacts({ site }: { site: Website }) {
  return (
    <div className="space-y-4">
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <Fact label="Document root" value={site.document_root} mono />
        <Fact label="Log directory" value={logsDirFor(site.document_root)} mono />
        <Fact label="System user" value={site.system_user} mono />
        <Fact label="PHP version" value={site.php_version ?? 'Static site, no PHP'} />
        <Fact label="Primary domain" value={site.primary_domain} />
        <Fact label="Status" value={site.status} />
      </dl>

      <p className="flex items-center gap-1.5 rounded-md bg-surface-sunken px-3 py-2 text-xs text-slate-500">
        <FileText aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
        DNS records are managed outside the panel in this build. Phase 13 adds them.
      </p>
    </div>
  );
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-slate-500">{label}</dt>
      <dd className={['truncate text-slate-800', mono ? 'font-mono text-xs' : ''].join(' ')}>
        {value}
      </dd>
    </div>
  );
}
