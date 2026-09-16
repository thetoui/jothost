import { useEffect, useState } from 'react';
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
  Pencil,
  Plug,
  ScrollText,
  Settings2,
  ShieldCheck,
  Terminal,
} from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { TextField } from '@/components/ui/Field';
import { IconButton } from '@/components/ui/IconButton';
import { LinkButton, TextLink } from '@/components/ui/Link';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { SiteLogsDialog } from '@/features/websites/components/SiteLogsDialog';
import { useSetDocumentRoot } from '@/features/websites/hooks';
import { logsDirFor, relativeRoot } from '@/features/websites/paths';
import { ApiError } from '@/services/apiClient';
import { Tabs } from '@/components/ui/Tabs';
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
  const [movingRoot, setMovingRoot] = useState(false);
  const [showingLogs, setShowingLogs] = useState(false);

  const files = `/files?path=${encodeURIComponent(site.document_root)}`;
  const detail = `/websites/${site.id}`;

  return (
    <div className="border-t border-surface-border bg-surface-sunken/40 px-4 py-4">
      <div className="grid gap-5 lg:grid-cols-[17rem,1fr]">
        <DomainSummary site={site} />

        <div className="min-w-0 space-y-4">
          <Tabs
            label={`${site.primary_domain} sections`}
            value={tab}
            onChange={setTab}
            items={[
              { value: 'dashboard', label: 'Dashboard' },
              { value: 'hosting', label: 'Hosting & DNS' },
            ]}
          />

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
                  detail="FTP and paths for this site"
                  tone="slate"
                  to={detail}
                />
                <ToolTile
                  icon={<Package className="h-4 w-4" />}
                  label="Backup & restore"
                  detail="Take and restore backups"
                  tone="amber"
                  to="/backups"
                />
                <ToolTile
                  icon={<Terminal className="h-4 w-4" />}
                  label="FTP"
                  detail="Accounts for this site"
                  tone="blue"
                  to={detail}
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
                {/* This site's own logs, not the host's. It used to link to
                    the site's detail page, which does not show them. */}
                <ToolTile
                  icon={<ScrollText className="h-4 w-4" />}
                  label="Logs"
                  detail="This site's requests and errors"
                  tone="amber"
                  onClick={() => setShowingLogs(true)}
                />
                <ToolTile
                  icon={<Clock className="h-4 w-4" />}
                  label="Scheduled tasks"
                  detail="Cron jobs on this host"
                  tone="slate"
                  to="/cron"
                />
                <ToolTile
                  icon={<Activity className="h-4 w-4" />}
                  label="Monitoring"
                  detail="Alert rules and history"
                  tone="rose"
                  to="/monitoring"
                />
                <ToolTile
                  icon={<GitBranch className="h-4 w-4" />}
                  label="Git"
                  detail="Deploy from a repository"
                  tone="violet"
                  to="/deployments"
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
                  unavailable="This panel does not manage these yet"
                />
                <ToolTile
                  icon={<ShieldCheck className="h-4 w-4" />}
                  label="Web application firewall"
                  unavailable="Not available — the host firewall is under Firewall"
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
      <dl className="mt-4 flex flex-wrap items-center gap-x-6 gap-y-1 border-t border-surface-border pt-3 text-xs text-ink-muted">
        <div className="flex gap-1.5">
          <dt>Website at</dt>
          <dd className="flex items-center gap-1.5">
            <TextLink to={files} tone="neutral" className="font-mono">
              {site.document_root}
            </TextLink>
            <RequirePermission permission={Permission.WebsiteUpdate}>
              <IconButton
                size="sm"
                onClick={() => setMovingRoot(true)}
                label={`Change the document root for ${site.primary_domain}`}
                icon={<Pencil aria-hidden="true" className="h-3 w-3" />}
              />
            </RequirePermission>
          </dd>
        </div>
        <div className="flex gap-1.5">
          <dt>System user</dt>
          <dd className="font-mono text-ink">{site.system_user}</dd>
        </div>
        <div className="flex gap-1.5">
          <dt>Logs at</dt>
          <dd className="font-mono text-ink">{logsDirFor(site.primary_domain)}</dd>
        </div>
      </dl>

      <DocumentRootDialog
        site={site}
        open={movingRoot}
        onClose={() => setMovingRoot(false)}
      />

      <SiteLogsDialog site={site} open={showingLogs} onClose={() => setShowingLogs(false)} />
    </div>
  );
}

function DomainSummary({ site }: { site: Website }) {
  return (
    <div className="space-y-3">
      <div className="rounded-card border border-surface-border bg-surface p-3">
        <div className="flex items-center justify-between">
          <h4 className="text-sm font-semibold text-ink-strong">At a glance</h4>
          <TextLink href={`http://${site.primary_domain}`} size="xs" className="inline-flex items-center gap-1">
            Open in web
            <ExternalLink aria-hidden="true" className="h-3 w-3" />
          </TextLink>
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

      <LinkButton to={`/websites/${site.id}`} className="w-full">
        <Settings2 aria-hidden="true" className="h-4 w-4" />
        Manage this site
      </LinkButton>
    </div>
  );
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-ink-muted">{label}</dt>
      <dd className="truncate font-medium text-ink-strong">{value}</dd>
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
        <Fact label="Log directory" value={logsDirFor(site.primary_domain)} mono />
        <Fact label="System user" value={site.system_user} mono />
        <Fact label="PHP version" value={site.php_version ?? 'Static site, no PHP'} />
        <Fact label="Primary domain" value={site.primary_domain} />
        <Fact label="Status" value={site.status} />
      </dl>

      <p className="flex items-center gap-1.5 rounded-md bg-surface-sunken px-3 py-2 text-xs text-ink-muted">
        <FileText aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
        DNS records for this site are managed from its settings page, and the host's zones
        under DNS.
      </p>
    </div>
  );
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-ink-muted">{label}</dt>
      <dd className={['truncate text-ink-strong', mono ? 'font-mono text-xs' : ''].join(' ')}>
        {value}
      </dd>
    </div>
  );
}

/**
 * DocumentRootDialog moves where a site is served from.
 *
 * The field is relative to the site's own directory, and the prefix is shown
 * beside it rather than being editable. An operator setting "public/dist"
 * cannot name another site's files or anywhere outside their own — not because
 * a check catches it, but because there is nothing to catch: they are not
 * naming a directory, only a subpath of the one already theirs.
 */
function DocumentRootDialog({
  site,
  open,
  onClose,
}: {
  site: Website;
  open: boolean;
  onClose: () => void;
}) {
  const move = useSetDocumentRoot(site.id);
  const base = `/var/www/${site.primary_domain}`;
  const current = relativeRoot(site.document_root, base);
  const [value, setValue] = useState(current);

  useEffect(() => {
    if (open) {
      setValue(current);
      move.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, current]);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Document root"
      description="Where the web server serves this site from. Somewhere inside the site's own directory."
      busy={move.isPending}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={move.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={move.isPending}
            onClick={() => move.mutate(value.trim(), { onSuccess: onClose })}
          >
            Save
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {move.error instanceof ApiError && (
          <Alert tone="danger" title="The document root was not changed">
            {move.error.message}
          </Alert>
        )}

        <TextField
          id="document-root"
          label="Served from"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder="public"
          // adornment, not prefix: "prefix" is a real HTML attribute, so
          // TypeScript accepts it, React passes it to the input, and it
          // renders nothing at all.
          adornment={<span className="font-mono text-xs text-ink-muted">{base}/</span>}
          hint="A path inside the site, such as public or public/dist. Leave it empty to serve the site's own directory. It is created if it does not exist yet."
        />

        <p className="text-xs text-ink-muted">
          The site&rsquo;s logs stay where they are, beside the site rather than inside what is
          served — an access log under the document root would be a file anybody could fetch.
        </p>
      </div>
    </Modal>
  );
}

