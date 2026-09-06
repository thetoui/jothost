import { useState } from 'react';
import {
  AlertTriangle,
  AtSign,
  Globe,
  Inbox,
  KeyRound,
  Mails,
  Plus,
  RefreshCw,
  Server,
  Trash2,
} from 'lucide-react';

import { Alert, Alert as AlertBanner } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { TextButton } from '@/components/ui/TextButton';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useAliases, useCreateAlias, useCreateMailDomain, useCreateMailbox, useDeleteAlias, useDeleteMailDomain, useDeleteMailbox, useInstallMail, useMailOverview, useMailboxes, useRotateDKIM, useSaveMailSettings, useSetMailboxPassword } from '@/features/mail/hooks';
import { ApiError } from '@/services/apiClient';
import type {
  Mailbox,
  MailDomain,
  MailOverview,
  MailStatus,
  DMARCPolicy,
  SPFPolicy,
} from '@/types/api';

/**
 * MailPage shows what this host does with mail, and what the world can see of
 * it.
 *
 * The second half is why the page is shaped the way it is. Every other page in
 * this panel can tell you whether the thing it manages works: a website serves
 * a page, a certificate handshakes, a backup is read back. A mail server with a
 * missing SPF record sends mail perfectly — and it is filed as spam by the
 * recipient, weeks before anybody reports it as "some people say they never got
 * my email".
 *
 * So the panel does not show what was configured. It shows what was configured
 * next to what DNS actually publishes, and leads with the difference.
 */
export function MailPage() {
  const { data, isPending, isError, error } = useMailOverview();

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Mail</h1>
        <p className="mt-1 text-sm text-slate-500">
          The mailboxes on this host, and whether the rest of the world believes them.
        </p>
      </header>

      {isError && (
        <AlertBanner tone="danger" title="The mail settings could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </AlertBanner>
      )}

      {isPending && !data ? (
        <Card>
          <CardBody>
            <SkeletonRows rows={4} />
          </CardBody>
        </Card>
      ) : !data ? null : (
        <>
          <Health overview={data} />
          <ServerSettings overview={data} />
          <Domains overview={data} />
        </>
      )}
    </div>
  );
}

/**
 * Health leads with the two things that are emergencies.
 *
 * An open relay is first because it is the only failure here that takes every
 * customer on the host off the internet at once — a relaying server is on a
 * blocklist within hours and off it in weeks.
 */
function Health({ overview }: { overview: MailOverview }) {
  const status = overview.status;

  if (!status.available) {
    return <InstallMailServer status={status} />;
  }

  const unsigned = (overview.domains ?? []).filter(
    (domain) => domain.active && !domain.signing,
  );
  const unpublished = (overview.domains ?? []).filter(
    (domain) => domain.active && (domain.problems?.length ?? 0) > 0,
  );

  return (
    <>
      {status.open_relay.open && (
        <AlertBanner tone="danger" title="This server is an open relay">
          {status.open_relay.detail}
        </AlertBanner>
      )}

      {!status.open_relay.checked && (
        <AlertBanner tone="warning" title="The relay check could not run">
          {status.open_relay.detail ??
            'The panel could not confirm that this server refuses to carry a stranger’s mail.'}
        </AlertBanner>
      )}

      {unpublished.length > 0 && (
        <AlertBanner
          tone="warning"
          title={`${unpublished.length} domain(s) are not set up as the world sees them`}
        >
          The settings here are only half of it. What decides whether mail from these
          domains is believed is what DNS publishes, and for these it does not match.
        </AlertBanner>
      )}

      {unsigned.length > 0 && (
        <AlertBanner tone="warning" title="Some domains send unsigned mail">
          {unsigned.length} active domain(s) have no signing key on this host, so their
          mail is more likely to be filed as spam.
        </AlertBanner>
      )}

      <Card>
        <CardHeader
          icon={<TintedIcon tone="brand" icon={<Server className="h-4 w-4" />} />}
          title="The mail server"
          description={status.hostname ? `Greeting the world as ${status.hostname}.` : undefined}
        />
        <CardBody className="space-y-4">
          <dl className="grid gap-4 sm:grid-cols-4">
            <Daemon label="Transport" daemon={status.postfix} />
            <Daemon label="Mailboxes" daemon={status.dovecot} />
            <Daemon label="Filter" daemon={status.rspamd} />
            <Daemon label="Virus scanner" daemon={status.antivirus} />
          </dl>

          <Ports status={status} />

          {status.queue_length > 0 && (
            <p className="text-xs text-slate-500">
              {status.queue_length} message(s) waiting to be delivered
              {status.queue_oldest_seconds > 3600
                ? `, the oldest for ${Math.floor(status.queue_oldest_seconds / 3600)} hour(s) — a queue that is not moving`
                : '.'}
            </p>
          )}

          {(status.warnings ?? []).map((warning) => (
            <p key={warning} className="flex gap-2 text-xs text-warn-700">
              <AlertTriangle aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{warning}</span>
            </p>
          ))}
        </CardBody>
      </Card>
    </>
  );
}

function Daemon({
  label,
  daemon,
}: {
  label: string;
  daemon: MailStatus['postfix'];
}) {
  const state = !daemon.installed ? 'not installed' : daemon.running ? 'running' : 'stopped';
  const colour = daemon.running
    ? 'text-ok-700'
    : daemon.installed
      ? 'text-danger-700'
      : 'text-slate-400';
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className={`mt-1 text-sm font-semibold ${colour}`}>{state}</dd>
      {daemon.detail && <p className="mt-0.5 text-xs text-slate-500">{daemon.detail}</p>}
    </div>
  );
}

/**
 * Ports shows what is configured next to what is actually listening.
 *
 * The two differ in exactly the case somebody needs to see: a daemon that
 * failed to start leaves every port configured and none of them answering.
 */
/**
 * InstallMailServer offers to put a mail server on a host that has none.
 *
 * The panel could install one from the first day of Phase 26 — the operation,
 * the API route and the hook all existed — and nothing ever called it, so the
 * page said "this host has no mail server" and stopped there. Which is true,
 * and useless.
 */
function InstallMailServer({ status }: { status: MailStatus }) {
  const install = useInstallMail();
  const [filtering, setFiltering] = useState(true);
  const [antivirus, setAntivirus] = useState(false);

  if (!status.can_install) {
    return (
      <AlertBanner tone="warning" title="This host has no mail server">
        {status.reason ??
          'Postfix and Dovecot are not installed, and this host has no package manager the panel can install them with.'}
      </AlertBanner>
    );
  }

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<Server className="h-4 w-4" />} />}
        title="This host has no mail server"
        description="Postfix carries the mail and Dovecot holds the mailboxes. Both are installed together."
      />
      <CardBody className="space-y-4">
        <Toggle
          id="mail-install-filtering"
          label="Install spam filtering"
          description="Rspamd scores each message and rejects what it is confident about."
          checked={filtering}
          onChange={(next) => {
            setFiltering(next);
            // Virus scanning is scored and acted on by Rspamd, so it cannot be
            // installed without it. Turning filtering off takes it with it
            // rather than leaving a checkbox that would silently do nothing.
            if (!next) setAntivirus(false);
          }}
          disabled={install.isPending}
        />
        <Toggle
          id="mail-install-antivirus"
          label="Install virus scanning"
          description="ClamAV, scored through Rspamd. The signature database is several hundred megabytes and is downloaded during the install, so this takes a few minutes longer."
          checked={antivirus}
          onChange={setAntivirus}
          disabled={install.isPending || !filtering}
        />

        {install.isError && (
          <Alert tone="danger" title="The mail server could not be installed">
            {install.error instanceof Error
              ? install.error.message
              : 'The host refused the install.'}
          </Alert>
        )}

        <div className="flex items-center gap-3">
          <Button
            variant="primary"
            loading={install.isPending}
            onClick={() => install.mutate({ filtering, antivirus })}
          >
            Install the mail server
          </Button>
          {install.isPending && (
            <span className="text-xs text-slate-500">
              {antivirus
                ? 'Installing, and downloading virus signatures. This can take several minutes.'
                : 'Installing. This takes a moment.'}
            </span>
          )}
        </div>
      </CardBody>
    </Card>
  );
}

function Ports({ status }: { status: MailStatus }) {
  const ports = status.ports ?? [];
  if (ports.length === 0) return null;

  return (
    <div className="flex flex-wrap gap-2">
      {ports.map((port) => (
        <span
          key={port.port}
          className={`rounded px-2 py-1 text-xs ${
            port.listening
              ? 'bg-ok-50 text-ok-800'
              : port.configured
                ? 'bg-danger-50 text-danger-800'
                : 'bg-slate-100 text-slate-500'
          }`}
          title={
            port.listening
              ? `${port.name} is answering`
              : port.configured
                ? `${port.name} is configured and nothing is listening`
                : `${port.name} is not offered`
          }
        >
          {port.port} · {port.name}
        </span>
      ))}
    </div>
  );
}

/** ServerSettings is the host-wide form. */
function ServerSettings({ overview }: { overview: MailOverview }) {
  const { settings } = overview;
  const save = useSaveMailSettings();
  const [hostname, setHostname] = useState(settings.hostname);
  const [enabled, setEnabled] = useState(settings.enabled);
  const [requireTLS, setRequireTLS] = useState(settings.require_tls);
  const [spam, setSpam] = useState(settings.spam_enabled);
  const [virus, setVirus] = useState(settings.virus_enabled);

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="neutral" icon={<Mails className="h-4 w-4" />} />}
        title="Settings"
        description="What this server calls itself, and what it refuses."
      />
      <CardBody className="space-y-4">
        {save.isError && (
          <AlertBanner tone="danger" title="The settings were not saved">
            {save.error instanceof ApiError ? save.error.message : 'Try again.'}
          </AlertBanner>
        )}

        <TextField
          id="mail-hostname"
          label="Mail hostname"
          value={hostname}
          onChange={(event) => setHostname(event.target.value)}
          hint="The name this server greets other mail servers with. It has to resolve, and it should be what this machine's address resolves back to — that pair is the first thing a receiving server checks."
        />

        <Toggle
          id="mail-enabled"
          label="Accept mail"
          description="Off means this host stops accepting mail for its domains. Existing mailboxes and their messages are untouched."
          checked={enabled}
          onChange={setEnabled}
        />

        <Toggle
          id="mail-require-tls"
          label="Require encryption for passwords"
          description="A password may never cross an unencrypted connection. Mail from other servers on port 25 is unaffected — that has to work in the clear or this host receives nothing."
          checked={requireTLS}
          onChange={setRequireTLS}
        />

        <Toggle
          id="mail-spam"
          label="Filter spam"
          description="Messages above the reject score are refused at delivery time, so the sender is told. A false positive that bounces gets a phone call; one filed in a folder nobody opens is silence."
          checked={spam}
          onChange={setSpam}
        />

        <Toggle
          id="mail-virus"
          label="Scan for viruses"
          description="Needs the scanner installed. Its signature database is several hundred megabytes."
          checked={virus}
          onChange={setVirus}
        />

        <RequirePermission permission={Permission.MailManage}>
          <Button
            loading={save.isPending}
            onClick={() =>
              save.mutate({
                enabled,
                hostname,
                require_tls: requireTLS,
                spam_enabled: spam,
                spam_reject_score: settings.spam_reject_score,
                virus_enabled: virus,
                max_message_mb: settings.max_message_mb,
                tls_website_id: settings.tls_website_id ?? '',
              })
            }
          >
            Save settings
          </Button>
        </RequirePermission>
      </CardBody>
    </Card>
  );
}

/** Domains lists each mail domain with what DNS says about it. */
function Domains({ overview }: { overview: MailOverview }) {
  const domains = overview.domains ?? [];
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [removing, setRemoving] = useState<MailDomain | null>(null);

  const remove = useDeleteMailDomain();
  const rotate = useRotateDKIM();

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="neutral" icon={<Globe className="h-4 w-4" />} />}
        title="Domains"
        description="Every domain this host accepts mail for."
        action={
          <RequirePermission permission={Permission.MailManage}>
            <Button
              onClick={() => setAdding(true)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add domain
            </Button>
          </RequirePermission>
        }
      />

      {domains.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Globe className="h-6 w-6" />}
            title="No mail domains"
            description="Add one to start accepting mail here."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {domains.map((domain) => (
            <li key={domain.id} className="px-5 py-4">
              <div className="flex items-start gap-4">
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium text-slate-900">
                    {domain.domain}
                    {!domain.active && (
                      <span className="ml-2 text-xs text-slate-500">· not accepting mail</span>
                    )}
                  </p>
                  <p className="mt-0.5 text-xs text-slate-500">
                    {domain.mailboxes} mailbox(es) · {domain.aliases} forwarder(s)
                  </p>
                  <Publication domain={domain} />
                </div>
                <div className="flex shrink-0 gap-2">
                  <Button
                    variant="secondary"
                    onClick={() => setOpen(open === domain.id ? null : domain.id)}
                  >
                    {open === domain.id ? 'Hide' : 'Mailboxes'}
                  </Button>
                  <RequirePermission permission={Permission.MailManage}>
                    <Button
                      variant="secondary"
                      loading={rotate.isPending && rotate.variables === domain.id}
                      onClick={() => rotate.mutate(domain.id)}
                      icon={<RefreshCw aria-hidden="true" className="h-4 w-4" />}
                      title="Generate a new signing key and publish it"
                    >
                      New key
                    </Button>
                    <Button
                      variant="secondary"
                      onClick={() => setRemoving(domain)}
                      icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                    >
                      Remove
                    </Button>
                  </RequirePermission>
                </div>
              </div>

              {open === domain.id && <DomainDetail domain={domain} />}
            </li>
          ))}
        </ul>
      )}

      {adding && <AddDomain onClose={() => setAdding(false)} />}

      <ConfirmDialog
        open={removing !== null}
        title={`Remove ${removing?.domain ?? ''}?`}
        confirmLabel="Remove domain"
        destructive
        onConfirm={() => {
          if (removing) remove.mutate(removing.id);
          setRemoving(null);
        }}
        onClose={() => setRemoving(null)}
      >
        Every mailbox and forwarder on this domain goes with it, and this host stops
        accepting its mail. The messages already delivered stay on the disk — the panel
        does not delete anybody’s mail.
      </ConfirmDialog>
    </Card>
  );
}

/**
 * Publication is the phase's central comparison, in one line per domain.
 *
 * Four facts about what the world can see, and the reason they are shown rather
 * than the settings that produced them: a policy in the panel has no effect at
 * all. A receiving server reads DNS.
 */
function Publication({ domain }: { domain: MailDomain }) {
  if (!domain.dns_managed) {
    return (
      <p className="mt-1.5 text-xs text-warn-700">
        This host does not serve DNS for {domain.domain}, so the panel cannot check its
        mail records. They have to be added wherever its DNS is.
      </p>
    );
  }

  return (
    <div className="mt-1.5 space-y-1">
      <div className="flex flex-wrap gap-1.5">
        <Badge label="MX" ok={domain.mx_published} />
        <Badge label="SPF" ok={domain.spf_published} skipped={domain.spf_policy === 'none'} />
        <Badge label="DKIM" ok={domain.dkim_published && domain.signing} />
        <Badge label="DMARC" ok={domain.dmarc_published} skipped={domain.dmarc_policy === 'off'} />
      </div>
      {(domain.problems ?? []).map((problem) => (
        <p key={problem} className="text-xs text-warn-700">
          {problem}
        </p>
      ))}
    </div>
  );
}

function Badge({ label, ok, skipped }: { label: string; ok: boolean; skipped?: boolean }) {
  const tone = skipped
    ? 'bg-slate-100 text-slate-500'
    : ok
      ? 'bg-ok-50 text-ok-800'
      : 'bg-warn-50 text-warn-800';
  const suffix = skipped ? 'off' : ok ? 'published' : 'not published';
  return <span className={`rounded px-1.5 py-0.5 text-xs ${tone}`}>{`${label} ${suffix}`}</span>;
}

/** DomainDetail is the mailboxes and forwarders on one domain. */
function DomainDetail({ domain }: { domain: MailDomain }) {
  const mailboxes = useMailboxes(domain.id);
  const aliases = useAliases(domain.id);
  const [addingBox, setAddingBox] = useState(false);
  const [addingAlias, setAddingAlias] = useState(false);
  const [resetting, setResetting] = useState<Mailbox | null>(null);
  const [removingBox, setRemovingBox] = useState<Mailbox | null>(null);

  const removeBox = useDeleteMailbox();
  const removeAlias = useDeleteAlias();

  return (
    <div className="mt-4 space-y-4 rounded-md bg-surface-muted p-4">
      <div>
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
            Mailboxes
          </h3>
          <RequirePermission permission={Permission.MailManage}>
            <Button variant="secondary" onClick={() => setAddingBox(true)}>
              Add mailbox
            </Button>
          </RequirePermission>
        </div>

        {mailboxes.isPending ? (
          <SkeletonRows rows={2} />
        ) : (mailboxes.data?.mailboxes ?? []).length === 0 ? (
          <p className="mt-2 text-xs text-slate-500">No mailboxes yet.</p>
        ) : (
          <ul className="mt-2 space-y-1.5">
            {(mailboxes.data?.mailboxes ?? []).map((box) => (
              <li key={box.id} className="flex items-center gap-3 text-sm">
                <Inbox aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-slate-400" />
                <span className="min-w-0 flex-1 truncate text-slate-900">{box.address}</span>
                <span className="shrink-0 text-xs text-slate-500">{describeUsage(box)}</span>
                {!box.active && <span className="shrink-0 text-xs text-slate-500">suspended</span>}
                <RequirePermission permission={Permission.MailManage}>
                  <TextButton size="xs" className="shrink-0" onClick={() => setResetting(box)}>
                    Set password
                  </TextButton>
                  <TextButton
                    size="xs"
                    tone="danger"
                    className="shrink-0"
                    onClick={() => setRemovingBox(box)}
                  >
                    Remove
                  </TextButton>
                </RequirePermission>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div>
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
            Forwarders
          </h3>
          <RequirePermission permission={Permission.MailManage}>
            <Button variant="secondary" onClick={() => setAddingAlias(true)}>
              Add forwarder
            </Button>
          </RequirePermission>
        </div>

        {(aliases.data?.aliases ?? []).length === 0 ? (
          <p className="mt-2 text-xs text-slate-500">No forwarders.</p>
        ) : (
          <ul className="mt-2 space-y-1.5">
            {(aliases.data?.aliases ?? []).map((alias) => (
              <li key={alias.id} className="flex items-center gap-3 text-sm">
                <AtSign aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-slate-400" />
                <span className="min-w-0 flex-1 truncate text-slate-900">
                  {alias.source}@{domain.domain} → {alias.destination}
                </span>
                <RequirePermission permission={Permission.MailManage}>
                  <TextButton
                    size="xs"
                    tone="danger"
                    className="shrink-0"
                    onClick={() => removeAlias.mutate(alias.id)}
                  >
                    Remove
                  </TextButton>
                </RequirePermission>
              </li>
            ))}
          </ul>
        )}
      </div>

      {addingBox && <AddMailbox domainID={domain.id} onClose={() => setAddingBox(false)} />}
      {addingAlias && <AddAlias domainID={domain.id} onClose={() => setAddingAlias(false)} />}
      {resetting && <SetPassword mailbox={resetting} onClose={() => setResetting(null)} />}

      <ConfirmDialog
        open={removingBox !== null}
        title={`Remove ${removingBox?.address ?? ''}?`}
        confirmLabel="Remove mailbox"
        destructive
        onConfirm={() => {
          if (removingBox) removeBox.mutate(removingBox.id);
          setRemovingBox(null);
        }}
        onClose={() => setRemovingBox(null)}
      >
        The login stops working immediately. The messages stay on the disk — deleting
        somebody’s mail is not something the panel does as a side effect.
      </ConfirmDialog>
    </div>
  );
}

/**
 * describeUsage says how full a mailbox is, and says nothing when it does not
 * know.
 *
 * "Unknown" rather than zero, because telling a customer their full mailbox has
 * room is worse than telling them nothing.
 */
function describeUsage(box: Mailbox): string {
  if (!box.quota_known) return 'usage unknown';
  if (box.quota_mb === 0) return `${box.used_mb} MB · no limit`;
  return `${box.used_mb} / ${box.quota_mb} MB`;
}

function AddDomain({ onClose }: { onClose: () => void }) {
  const create = useCreateMailDomain();
  const [domain, setDomain] = useState('');
  const [spf, setSPF] = useState<SPFPolicy>('soft');
  const [dmarc, setDMARC] = useState<DMARCPolicy>('none');

  return (
    <Modal open title="Add a mail domain" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="The domain was not added">
            {create.error instanceof ApiError ? create.error.message : 'Try again.'}
          </AlertBanner>
        )}

        <TextField
          id="mail-domain"
          label="Domain"
          value={domain}
          onChange={(event) => setDomain(event.target.value)}
          placeholder="example.com"
          hint="A signing key is generated for it, and its MX, SPF, DKIM and DMARC records are published if this host serves its DNS."
        />

        <SelectField
          id="mail-spf"
          label="SPF"
          value={spf}
          onChange={(event) => setSPF(event.target.value as SPFPolicy)}
          hint="Start with the first one unless you are certain nothing else sends as this domain — a newsletter provider or an invoicing system will stop being delivered under the second."
        >
          <option value="soft">Mark mail from anywhere else (~all)</option>
          <option value="strict">Refuse mail from anywhere else (-all)</option>
          <option value="none">Publish no SPF record</option>
        </SelectField>

        <SelectField
          id="mail-dmarc"
          label="DMARC"
          value={dmarc}
          onChange={(event) => setDMARC(event.target.value as DMARCPolicy)}
          hint="Report-only is not a no-op: the reports are how you find out what else sends as this domain, before you tell the world to reject it."
        >
          <option value="none">Report only</option>
          <option value="quarantine">Treat failures as suspicious</option>
          <option value="reject">Refuse failures</option>
          <option value="off">Publish no DMARC record</option>
        </SelectField>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            loading={create.isPending}
            onClick={() =>
              create.mutate(
                { domain, spf_policy: spf, dmarc_policy: dmarc },
                { onSuccess: onClose },
              )
            }
          >
            Add domain
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function AddMailbox({ domainID, onClose }: { domainID: string; onClose: () => void }) {
  const create = useCreateMailbox();
  const [local, setLocal] = useState('');
  const [password, setPassword] = useState('');
  const [quota, setQuota] = useState('2048');

  return (
    <Modal open title="Add a mailbox" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="The mailbox was not created">
            {create.error instanceof ApiError ? create.error.message : 'Try again.'}
          </AlertBanner>
        )}

        <TextField
          id="mailbox-name"
          label="Name"
          value={local}
          onChange={(event) => setLocal(event.target.value)}
          placeholder="sales"
        />
        <TextField
          id="mailbox-password"
          label="Password"
          type="password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          hint="At least 12 characters. This is the credential that unlocks the customer's correspondence, and it is reachable from the whole internet."
        />
        <TextField
          id="mailbox-quota"
          label="Size limit (MB)"
          value={quota}
          onChange={(event) => setQuota(event.target.value)}
          hint="0 means no limit. An unlimited mailbox fills the disk with mail somebody else sent, and the first thing that stops working is every other service on this host."
        />

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            loading={create.isPending}
            onClick={() =>
              create.mutate(
                {
                  domainID,
                  body: {
                    local_part: local,
                    password,
                    quota_mb: Number.parseInt(quota, 10) || 0,
                  },
                },
                { onSuccess: onClose },
              )
            }
          >
            Add mailbox
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function AddAlias({ domainID, onClose }: { domainID: string; onClose: () => void }) {
  const create = useCreateAlias();
  const [source, setSource] = useState('');
  const [destination, setDestination] = useState('');

  return (
    <Modal open title="Add a forwarder" onClose={onClose}>
      <div className="space-y-4">
        {create.isError && (
          <AlertBanner tone="danger" title="The forwarder was not created">
            {create.error instanceof ApiError ? create.error.message : 'Try again.'}
          </AlertBanner>
        )}

        <TextField
          id="alias-source"
          label="From"
          value={source}
          onChange={(event) => setSource(event.target.value)}
          placeholder="info"
        />
        <TextField
          id="alias-destination"
          label="To"
          value={destination}
          onChange={(event) => setDestination(event.target.value)}
          placeholder="somebody@example.net"
          hint="A forwarder to the same name as a mailbox delivers to both — which is how 'keep a copy' works."
        />

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            loading={create.isPending}
            onClick={() =>
              create.mutate({ domainID, body: { source, destination } }, { onSuccess: onClose })
            }
          >
            Add forwarder
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function SetPassword({ mailbox, onClose }: { mailbox: Mailbox; onClose: () => void }) {
  const set = useSetMailboxPassword();
  const [password, setPassword] = useState('');

  return (
    <Modal open title={`Set the password for ${mailbox.address}`} onClose={onClose}>
      <div className="space-y-4">
        {set.isError && (
          <AlertBanner tone="danger" title="The password was not changed">
            {set.error instanceof ApiError ? set.error.message : 'Try again.'}
          </AlertBanner>
        )}

        <AlertBanner tone="warning" title="This is recorded">
          Setting a mailbox password gives whoever knows it the ability to read every
          message in this mailbox, and the owner sees no trace of it. The audit log
          records that you did it.
        </AlertBanner>

        <TextField
          id="mailbox-new-password"
          label="New password"
          type="password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          hint="At least 12 characters."
        />

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            loading={set.isPending}
            onClick={() => set.mutate({ id: mailbox.id, password }, { onSuccess: onClose })}
            icon={<KeyRound aria-hidden="true" className="h-4 w-4" />}
          >
            Set password
          </Button>
        </div>
      </div>
    </Modal>
  );
}
