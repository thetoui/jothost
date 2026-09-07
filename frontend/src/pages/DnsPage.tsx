import { useState } from 'react';
import { Globe, KeyRound, Plus, Server, Settings2, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { ZoneEditor } from '@/features/dns/components/ZoneEditor';
import {
  useCreateDNSZone,
  useDNSOverview,
  useDNSTemplates,
  useDeleteDNSZone,
  useInstallDNS,
  useSaveDNSSettings,
} from '@/features/dns/hooks';
import { ProviderCard } from '@/features/dns/components/ProviderCard';
import { TemplateCard } from '@/features/dns/components/TemplateCard';
import { ApiError } from '@/services/apiClient';
import type { DNSOverview, DNSZone } from '@/types/api';

/**
 * DnsPage shows and changes what this host tells the internet about its names.
 *
 * A zone here is not a website: they are usually the same name, but a zone can
 * point every one of its names at machines this panel has never heard of, and
 * that is the whole reason somebody runs their own name server. The page is
 * organised around that — zones first, and a website is a label on one.
 */
export function DnsPage() {
  const { data, isPending, isError, error } = useDNSOverview();
  const install = useInstallDNS();
  const [openZone, setOpenZone] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const installError = install.error instanceof ApiError ? install.error.message : null;

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">DNS</h1>
          <p className="mt-1 text-sm text-slate-500">
            The zones this host answers for, and what they say.
          </p>
        </div>
        {data?.available && (
          <RequirePermission permission={Permission.DNSManage}>
            <Button
              variant="primary"
              onClick={() => setCreating(true)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add Zone
            </Button>
          </RequirePermission>
        )}
      </header>

      {isError && (
        <Alert tone="danger" title="The DNS settings could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {isPending && !data ? (
        <Card>
          <CardBody>
            <SkeletonRows rows={4} />
          </CardBody>
        </Card>
      ) : !data ? null : !data.available ? (
        <Card>
          <CardHeader
            title="No DNS server is installed"
            description={
              data.can_install
                ? 'The panel can install BIND on this host. Zones are files it reads, so what is being served stays readable.'
                : 'This host has no package manager the panel can install one with.'
            }
            icon={<TintedIcon icon={<Server className="h-5 w-5" />} tone="neutral" />}
          />
          <CardBody>
            {installError && (
              <Alert tone="danger" title="The name server could not be installed" className="mb-3">
                {installError}
              </Alert>
            )}
            <RequirePermission permission={Permission.DNSManage}>
              <Button
                variant="primary"
                onClick={() => install.mutate()}
                loading={install.isPending}
                disabled={!data.can_install}
              >
                Install DNS Server
              </Button>
            </RequirePermission>
          </CardBody>
        </Card>
      ) : (
        <>
          <HostWarnings overview={data} />
          <ServerCard overview={data} />
          <ZoneList
            overview={data}
            onOpen={(id) => setOpenZone(id)}
          />
          <TemplateCard />
          <ProviderCard overview={data} />
          <ServerSettings overview={data} />
        </>
      )}

      {creating && (
        <CreateZoneDialog
          overview={data}
          onClose={() => setCreating(false)}
          onCreated={(id) => {
            setCreating(false);
            setOpenZone(id);
          }}
        />
      )}

      {openZone && <ZoneEditor zoneId={openZone} onClose={() => setOpenZone(null)} />}
    </div>
  );
}

/**
 * HostWarnings says what is wrong with this host's DNS that the panel will not
 * change on its own.
 *
 * All of these look healthy from the inside — the panel's own checks pass and
 * `dig` from the server answers perfectly — which is exactly why they are said
 * out loud rather than left to be discovered.
 */
function HostWarnings({ overview }: { overview: DNSOverview }) {
  const warnings = overview.warnings ?? [];
  const closed = !overview.firewall_open;

  if (warnings.length === 0 && !closed && overview.config_included !== false) {
    return null;
  }

  return (
    <div className="space-y-3">
      {closed && (
        <Alert tone="warning" title="Port 53 is closed">
          {overview.firewall_reason ||
            'The firewall does not admit DNS queries, so nothing outside this machine can look anything up here.'}{' '}
          The panel reports this rather than opening the port: a firewall change is its own
          deliberate act, and it is one click away on the Firewall page.
        </Alert>
      )}
      {!overview.config_included && (
        <Alert tone="warning" title="The name server is not reading the panel's zones">
          <code className="font-mono text-xs">{overview.config_path}</code> does not include{' '}
          <code className="font-mono text-xs">{overview.include_path}</code>, so every zone below is
          written to disk and served by nobody.
        </Alert>
      )}
      {warnings.map((warning) => (
        <Alert key={warning} tone="warning" title="This name server's configuration">
          {warning}
        </Alert>
      ))}
    </div>
  );
}

/** ServerCard is what the host reports about its name server. */
function ServerCard({ overview }: { overview: DNSOverview }) {
  return (
    <Card>
      <CardHeader
        title="Name server"
        description={overview.version ? `BIND ${overview.version}` : 'BIND'}
        icon={<TintedIcon icon={<Server className="h-5 w-5" />} tone="neutral" />}
        action={
          <StatusPill
            label={overview.running ? 'Running' : 'Stopped'}
            tone={overview.running ? 'ok' : 'warn'}
            dot
          />
        }
      />
      <CardBody className="grid gap-3 text-sm sm:grid-cols-2">
        <Detail label="Configuration">
          <code className="font-mono text-xs">{overview.config_path}</code>
          {!overview.managed_config && (
            <span className="ml-2 text-xs text-slate-500">
              (this host&rsquo;s own; the panel added one include line)
            </span>
          )}
        </Detail>
        <Detail label="Zone files">
          <code className="font-mono text-xs">{overview.zone_dir}</code>
        </Detail>
        <Detail label="Answers on">
          {overview.listen_on && overview.listen_on.length > 0
            ? overview.listen_on.join(', ')
            : 'every address'}
        </Detail>
        <Detail label="Signing">
          {overview.supports_dnssec
            ? 'Available — named generates the keys and rolls them over itself'
            : 'Not available on this build'}
        </Detail>
      </CardBody>
    </Card>
  );
}

function Detail({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-xs uppercase tracking-wide text-slate-400">{label}</div>
      <div className="mt-0.5 text-slate-700">{children}</div>
    </div>
  );
}

/** ZoneList is the zones the panel has recorded. */
function ZoneList({
  overview,
  onOpen,
}: {
  overview: DNSOverview;
  onOpen: (id: string) => void;
}) {
  const remove = useDeleteDNSZone();
  const [pending, setPending] = useState<DNSZone | null>(null);
  const zones = overview.zones ?? [];

  // A zone the host serves and the panel does not know about. It is not listed
  // as a zone — the panel's record is what the next reconcile makes true — but
  // saying nothing about it would leave an operator wondering where it went.
  const unmanaged = (overview.host_zones ?? []).filter(
    (name) => !zones.some((zone) => zone.name === name),
  );

  return (
    <Card label="Zones">
      <CardHeader title="Zones" description={`${zones.length} on this host`} />
      {zones.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Globe className="h-6 w-6" />}
            title="No zones yet"
            description="Add one to have this host answer for a domain."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {zones.map((zone) => (
            <li key={zone.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <button
                type="button"
                onClick={() => onOpen(zone.id)}
                className={`flex-1 rounded-sm text-left ${focusRingTight}`}
              >
                <div className="flex items-center gap-2 font-medium text-slate-900">
                  {zone.name}
                  {zone.dnssec && (
                    <span
                      title="Signed with DNSSEC"
                      className="inline-flex items-center gap-1 rounded bg-ok-50 px-1.5 py-0.5 text-xs font-normal text-ok-700"
                    >
                      <KeyRound aria-hidden="true" className="h-3 w-3" />
                      Signed
                    </span>
                  )}
                  {zone.kind === 'slave' && (
                    <span className="rounded bg-slate-100 px-1.5 py-0.5 text-xs font-normal text-slate-600">
                      Secondary
                    </span>
                  )}
                </div>
                <div className="mt-0.5 text-xs text-slate-500">
                  {zone.kind === 'slave'
                    ? `Transferred from ${zone.masters.join(', ')}`
                    : `${zone.record_count} ${zone.record_count === 1 ? 'record' : 'records'} · serial ${zone.serial}`}
                  {zone.website_domain ? ` · ${zone.website_domain}` : ''}
                </div>
              </button>
              <RequirePermission permission={Permission.DNSManage}>
                <Button
                  variant="ghost"
                  onClick={() => setPending(zone)}
                  aria-label={`Delete ${zone.name}`}
                  icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                />
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      {unmanaged.length > 0 && (
        <CardBody>
          <Alert tone="info" title="Zones on this host the panel does not manage">
            {unmanaged.join(', ')}. They are served and are not the panel&rsquo;s; nothing here
            changes them.
          </Alert>
        </CardBody>
      )}

      <ConfirmDialog
        open={Boolean(pending)}
        onClose={() => setPending(null)}
        onConfirm={() => {
          if (!pending) return;
          remove.mutate(pending.id, { onSuccess: () => setPending(null) });
        }}
        title={`Stop serving ${pending?.name ?? ''}?`}
        description="This host will stop answering for the zone, and every record in it goes with it."
        confirmLabel="Delete zone"
        destructive
        loading={remove.isPending}
        error={remove.error instanceof ApiError ? remove.error.message : null}
      >
        <p className="text-sm text-slate-600">
          Anything pointing at these names stops resolving as soon as the caches expire, which can
          take as long as the zone&rsquo;s longest TTL.
        </p>
      </ConfirmDialog>
    </Card>
  );
}

/** CreateZoneDialog asks for a new zone. */
function CreateZoneDialog({
  overview,
  onClose,
  onCreated,
}: {
  overview: DNSOverview | undefined;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const create = useCreateDNSZone();
  const [kind, setKind] = useState<'forward' | 'reverse' | 'secondary'>('forward');
  const [name, setName] = useState('');
  const [network, setNetwork] = useState('');
  const [masters, setMasters] = useState('');

  const defaultNS = overview?.settings.default_ns ?? [];
  const { data: templates } = useDNSTemplates();
  const seed = (templates?.templates ?? []).find((template) => template.is_default);
  const failure = create.error instanceof ApiError ? create.error.message : null;

  const submit = () => {
    const body =
      kind === 'reverse'
        ? { reverse_network: network.trim() }
        : kind === 'secondary'
          ? {
              name: name.trim(),
              kind: 'slave' as const,
              masters: masters
                .split(',')
                .map((entry) => entry.trim())
                .filter(Boolean),
            }
          : { name: name.trim() };

    create.mutate(body, { onSuccess: (zone) => onCreated(zone.id) });
  };

  return (
    <Modal
      open
      onClose={onClose}
      busy={create.isPending}
      title="Add a zone"
      description="This host will answer for it."
      footer={
        <>
          <Button onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={create.isPending}>
            Create zone
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {failure && (
          <Alert tone="danger" title="The zone could not be created">
            {failure}
          </Alert>
        )}

        {kind !== 'secondary' && defaultNS.length === 0 && (
          <Alert tone="warning" title="No default name servers are set">
            A zone needs name servers to be delegated to, and the panel will not invent them. Set
            them in Name server settings below first.
          </Alert>
        )}

        <SelectField
          id="dns-zone-kind"
          label="Kind"
          value={kind}
          onChange={(event) => setKind(event.target.value as typeof kind)}
        >
          <option value="forward">Forward — names to addresses</option>
          <option value="reverse">Reverse — addresses to names</option>
          <option value="secondary">Secondary — transferred from another server</option>
        </SelectField>

        {kind === 'reverse' ? (
          <TextField
            id="dns-zone-network"
            label="Network"
            hint="In CIDR form, and byte-aligned: a /8, /16 or /24. The zone is named after it."
            placeholder="203.0.113.0/24"
            value={network}
            onChange={(event) => setNetwork(event.target.value)}
          />
        ) : (
          <TextField
            id="dns-zone-name"
            label="Zone"
            hint="The domain this host answers for."
            placeholder="example.com"
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        )}

        {kind === 'secondary' && (
          <TextField
            id="dns-zone-masters"
            label="Transfer from"
            hint="The addresses of the servers holding the zone, separated by commas."
            placeholder="198.51.100.5"
            value={masters}
            onChange={(event) => setMasters(event.target.value)}
          />
        )}

        {kind === 'forward' && defaultNS.length > 0 && (
          <p className="text-xs text-slate-500">
            It will be delegated to {defaultNS.join(', ')}
            {/* Named rather than described: what a new zone starts with is the
                default template's business now, and saying "the zone itself and
                www" here would go quietly out of date the first time somebody
                edits it. */}
            {seed
              ? `, and starts from the ${seed.name} template's ${(seed.records ?? []).length} records.`
              : ', and starts empty — no template is set as the default.'}
          </p>
        )}
      </div>
    </Modal>
  );
}

/**
 * ServerSettings are the defaults every new zone starts from.
 *
 * The name servers are here rather than on each zone because they are the same
 * for every zone a host serves, and asking for them once is the difference
 * between setting up DNS and setting up DNS repeatedly.
 */
function ServerSettings({ overview }: { overview: DNSOverview }) {
  const save = useSaveDNSSettings();
  const [nameservers, setNameservers] = useState(overview.settings.default_ns.join(', '));
  const [hostmaster, setHostmaster] = useState(overview.settings.hostmaster);
  const [listen, setListen] = useState((overview.settings.listen_on ?? []).join(', '));

  const failure = save.error instanceof ApiError ? save.error.message : null;
  const split = (value: string) =>
    value
      .split(',')
      .map((entry) => entry.trim())
      .filter(Boolean);

  return (
    <Card label="Name server settings">
      <CardHeader
        title="Name server settings"
        description="What every new zone starts from."
        icon={<TintedIcon icon={<Settings2 className="h-5 w-5" />} tone="neutral" />}
      />
      <CardBody className="space-y-4">
        {failure && (
          <Alert tone="danger" title="The settings could not be saved">
            {failure}
          </Alert>
        )}

        <TextField
          id="dns-default-ns"
          label="Name servers"
          hint="Separated by commas. These become each zone's NS records, and a zone with none is one no resolver can be sent to."
          placeholder="ns1.example.com., ns2.example.com."
          value={nameservers}
          onChange={(event) => setNameservers(event.target.value)}
        />
        <TextField
          id="dns-hostmaster"
          label="Hostmaster address"
          hint="Published in every zone's SOA record. An email address, which the panel converts to the form a zone file wants."
          placeholder="hostmaster@example.com"
          value={hostmaster}
          onChange={(event) => setHostmaster(event.target.value)}
        />
        <TextField
          id="dns-listen-on"
          label="Answer on"
          hint="Addresses, separated by commas. Empty means every address on this host."
          placeholder="203.0.113.10"
          value={listen}
          onChange={(event) => setListen(event.target.value)}
        />

        <RequirePermission permission={Permission.DNSManage}>
          <Button
            variant="primary"
            loading={save.isPending}
            onClick={() =>
              save.mutate({
                default_ns: split(nameservers),
                hostmaster: hostmaster.trim(),
                listen_on: split(listen),
                dnssec_policy: overview.settings.dnssec_policy,
                default_ttl: overview.settings.default_ttl,
                allow_transfer: overview.settings.allow_transfer,
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
