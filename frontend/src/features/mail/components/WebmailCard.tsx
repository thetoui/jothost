import { useState } from 'react';
import { Globe, Trash2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField } from '@/components/ui/Field';
import { TextLink } from '@/components/ui/Link';
import { ProgressBar } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useInstallWebmail, useRemoveWebmail, useWebmailJob } from '@/features/mail/hooks';
import { useWebsites } from '@/features/websites/hooks';
import { ApiError } from '@/services/apiClient';
import type { MailOverview, Website } from '@/types/api';

function messageOf(error: unknown, fallback: string): string {
  return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

/**
 * WebmailCard installs Roundcube into one of this host's websites, and says
 * whether the one installed can actually be used.
 *
 * Webmail is installed into a website the operator already made rather than a
 * site of its own, so the three things that decide whether it works belong to
 * that site: its PHP, its certificate, and what was in its document root —
 * which installing replaces. The card says each of those out loud, because
 * each is a way for "installed" to mean a page nobody can log in to, or one
 * that sends every password over the network in the clear.
 */
export function WebmailCard({ overview }: { overview: MailOverview }) {
  const websites = useWebsites();
  const install = useInstallWebmail();
  const remove = useRemoveWebmail();
  const [jobID, setJobID] = useState<string>();
  const job = useWebmailJob(jobID);
  const [chosen, setChosen] = useState('');
  const [confirmInstall, setConfirmInstall] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);

  // Top-level sites that are up: a subdomain shares its parent's account, and
  // a site being created or deleted has no document root to put anything in.
  const candidates = (websites.data?.websites ?? []).filter(
    (site) => site.parent_website_id === null && site.status === 'active',
  );
  const installedID = overview.settings.webmail_website_id;
  const installed = installedID
    ? (websites.data?.websites ?? []).find((site) => site.id === installedID)
    : undefined;
  const target = candidates.find((site) => site.id === (chosen || candidates[0]?.id));
  const running = job.data !== undefined && !['SUCCESS', 'FAILED', 'CANCELLED'].includes(job.data.status);

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<Globe className="h-4 w-4" />} />}
        title="Webmail"
        description={`Roundcube, served from one of your websites, for reading this host's mailboxes in a browser.`}
      />
      <CardBody className="space-y-4">
        {installedID ? (
          <InstalledWebmail
            site={installed}
            version={overview.settings.webmail_version}
            detail={overview.status.webmail?.detail}
            onRemove={() => setConfirmRemove(true)}
          />
        ) : (
          <p className="text-sm text-ink-muted">
            Webmail is not installed. Pick the website it should be served from — usually one made
            for it, such as <span className="font-mono">webmail.example.com</span>.
          </p>
        )}

        {job.data?.status === 'FAILED' && (
          <Alert tone="danger" title="Webmail was not installed">
            {job.data.error ?? 'The host did not say why.'} Nothing on the site was recorded as
            webmail.
          </Alert>
        )}
        {job.data?.status === 'SUCCESS' && (
          <Alert tone="success" title="Webmail is installed">
            It is served from {String(job.data.payload?.domain ?? 'the site')}.
          </Alert>
        )}
        {running && (
          <div className="space-y-1.5">
            <ProgressBar value={job.data?.progress} label="Installing webmail" />
            <p className="text-xs text-ink-muted">
              {job.data?.message ?? 'Downloading and checking the release.'}
            </p>
          </div>
        )}
        {job.isError && (
          <Alert tone="warning" title="The install is running, but its progress cannot be read">
            {messageOf(job.error, 'Reload the page in a minute to see whether it finished.')}
          </Alert>
        )}

        <RequirePermission permission={Permission.MailManage}>
          {candidates.length === 0 ? (
            <p className="text-sm text-ink-muted">
              Webmail needs a website to be served from.{' '}
              <TextLink to="/websites">Create one</TextLink>{' '}
              first.
            </p>
          ) : (
            <div className="space-y-3">
              <SelectField
                id="webmail-website"
                label={installedID ? 'Install again, or into another site' : 'Website'}
                value={target?.id ?? ''}
                onChange={(event) => setChosen(event.target.value)}
                disabled={running || install.isPending}
              >
                {candidates.map((site) => (
                  <option key={site.id} value={site.id}>
                    {site.primary_domain}
                  </option>
                ))}
              </SelectField>
              {/* Already said above for the site webmail is on. */}
              {target && target.id !== installedID && <Readiness site={target} />}
              {install.isError && (
                <Alert tone="danger" title="The install was not started">
                  {messageOf(install.error, 'The panel refused the request.')}
                </Alert>
              )}
              <Button
                variant="primary"
                disabled={!target || running}
                loading={install.isPending}
                onClick={() => setConfirmInstall(true)}
              >
                {installedID === target?.id ? 'Reinstall webmail' : 'Install webmail'}
              </Button>
            </div>
          )}
        </RequirePermission>
      </CardBody>

      <ConfirmDialog
        open={confirmInstall}
        onClose={() => setConfirmInstall(false)}
        title={`Install webmail into ${target?.primary_domain ?? 'this site'}?`}
        description={
          installedID === target?.id
            ? 'The application is replaced with a fresh copy. Contacts and preferences people saved are kept.'
            : 'Everything in the site’s document root is replaced by webmail.'
        }
        confirmLabel="Install webmail"
        destructive={installedID !== target?.id}
        loading={install.isPending}
        onConfirm={() => {
          if (!target) return;
          install.mutate(target.id, {
            onSuccess: (accepted) => {
              setJobID(accepted.job.id);
              setConfirmInstall(false);
            },
            onError: () => setConfirmInstall(false),
          });
        }}
      >
        {target && installedID !== target.id && (
          <p className="text-sm text-ink-muted">
            Any files in <span className="font-mono">{target.document_root}</span> are deleted.
          </p>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={confirmRemove}
        onClose={() => setConfirmRemove(false)}
        title="Remove webmail?"
        description="The application is deleted from its website, together with everything people stored in it: contacts, identities and preferences. Mailboxes and the mail in them are not touched."
        confirmLabel="Remove webmail"
        destructive
        loading={remove.isPending}
        error={remove.isError ? messageOf(remove.error, 'Webmail could not be removed.') : null}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => setConfirmRemove(false) })}
      />
    </Card>
  );
}

/** What is installed, and whether it can be used as it stands. */
function InstalledWebmail({
  site,
  version,
  detail,
  onRemove,
}: {
  site: Website | undefined;
  version: string | undefined;
  detail: string | undefined;
  onRemove: () => void;
}) {
  const url = site ? `${site.ssl_enabled ? 'https' : 'http'}://${site.primary_domain}/` : undefined;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-sm">
          <span className="text-ink-muted">Roundcube {version ?? ''} at </span>
          {url ? (
            <TextLink href={url} className="font-mono">
              {url}
            </TextLink>
          ) : (
            <span className="text-ink-muted">a website that no longer exists</span>
          )}
        </div>
        <RequirePermission permission={Permission.MailManage}>
          <Button variant="ghost" size="sm" icon={<Trash2 className="h-4 w-4" />} onClick={onRemove}>
            Remove
          </Button>
        </RequirePermission>
      </div>
      {detail && <Alert tone="warning">{detail}</Alert>}
      {site && <Readiness site={site} />}
    </div>
  );
}

/**
 * Readiness names what the site still needs for webmail to be usable.
 *
 * Neither is a reason to refuse the install — PHP can be switched on and a
 * certificate issued afterwards — but both are a reason nobody can use it yet.
 */
function Readiness({ site }: { site: Website }) {
  const settings = `/websites/${site.id}`;
  return (
    <>
      {!site.php_version && (
        <Alert tone="warning" title="PHP is off for this site">
          Webmail is a PHP application and shows nothing until PHP is switched on.{' '}
          <TextLink to={settings}>Open {site.primary_domain}</TextLink>
          .
        </Alert>
      )}
      {!site.ssl_enabled && (
        <Alert tone="warning" title="This site has no certificate">
          Every mailbox password typed into webmail would cross the network unencrypted. Issue a
          certificate for {site.primary_domain} before anyone signs in.
        </Alert>
      )}
    </>
  );
}
