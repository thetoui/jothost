import { useState } from 'react';
import { DownloadCloud, KeyRound, Pencil, Plus, Trash2, UploadCloud } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { TextLink } from '@/components/ui/Link';
import { SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useCreateDNSRecord,
  useDNSOverview,
  useDNSZone,
  useDeleteDNSRecord,
  useImportDNSZone,
  useSyncDNSZone,
  useUpdateDNSRecord,
  useUpdateDNSZone,
} from '@/features/dns/hooks';
import { ApiError } from '@/services/apiClient';
import type { DNSRecord, DNSRecordInput, DNSRecordType, DNSZoneDetail } from '@/types/api';

/** The types the form offers, in the order somebody reaches for them. */
const RECORD_TYPES: DNSRecordType[] = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'CAA', 'SRV', 'PTR'];

/**
 * ZoneEditor is the zone's records and its settings.
 *
 * It shows what the *server* is answering alongside what the panel recorded,
 * because those are different questions and the difference is the useful part:
 * a zone the panel has and the server has not loaded is the failure this page
 * exists to make visible.
 */
export function ZoneEditor({ zoneId, onClose }: { zoneId: string; onClose: () => void }) {
  const { data, isPending, isError, error } = useDNSZone(zoneId);

  // Gated on the zone rather than on the response: a reply without one is a
  // reply this editor cannot render, and dereferencing it would take the whole
  // page down rather than showing the error underneath.
  const zone = data?.zone;

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={zone ? zone.name : 'Zone'}
      description={
        zone
          ? zone.kind === 'slave'
            ? `Transferred from ${zone.masters.join(', ')}`
            : `Serial ${zone.serial}`
          : undefined
      }
      footer={<Button onClick={onClose}>Close</Button>}
    >
      {isError && (
        <Alert tone="danger" title="The zone could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}
      {isPending && !zone ? (
        <SkeletonRows rows={5} />
      ) : data && zone ? (
        <div className="space-y-5">
          <ZoneState detail={data} />
          {zone.kind === 'slave' ? (
            <Alert tone="info" title="This zone's records come from its primary">
              A secondary holds a copy that is replaced at every transfer, so there is nothing here
              to edit — changes belong on the server it is transferred from.
            </Alert>
          ) : (
            <>
              <Records detail={data} />
              <Signing detail={data} />
              <Transfers detail={data} />
              <RemoteSync detail={data} />
            </>
          )}
        </div>
      ) : null}
    </Modal>
  );
}

/** ZoneState is what the running server says. */
function ZoneState({ detail }: { detail: DNSZoneDetail }) {
  const { state } = detail;

  if (!state.loaded) {
    return (
      <Alert tone="warning" title="The name server is not serving this zone">
        {state.reason ||
          'The panel has the zone recorded and the running server does not have it loaded.'}
      </Alert>
    );
  }

  return (
    <dl className="grid gap-3 text-sm sm:grid-cols-3">
      <div>
        <dt className="text-xs uppercase tracking-wide text-slate-400">Loaded</dt>
        <dd className="mt-0.5 text-slate-700">{state.last_loaded || 'yes'}</dd>
      </div>
      <div>
        <dt className="text-xs uppercase tracking-wide text-slate-400">Serial in the file</dt>
        <dd className="mt-0.5 text-slate-700">{detail.zone.serial}</dd>
      </div>
      <div>
        {/* The two differ on every signed zone: named keeps its own serial on
            the signed copy and it runs ahead. Showing one number would make
            that look like drift. */}
        <dt className="text-xs uppercase tracking-wide text-slate-400">Serial being served</dt>
        <dd className="mt-0.5 text-slate-700">
          {state.signed_serial > 0 ? state.signed_serial : state.serial}
          {state.signed_serial > 0 && state.signed_serial !== state.serial && (
            <span className="ml-2 text-xs text-slate-500">(signing keeps its own)</span>
          )}
        </dd>
      </div>
    </dl>
  );
}

/** Records is the zone's contents. */
function Records({ detail }: { detail: DNSZoneDetail }) {
  const records = detail.zone.records ?? [];
  const remove = useDeleteDNSRecord();
  const [editing, setEditing] = useState<DNSRecord | null>(null);
  const [adding, setAdding] = useState(false);

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-slate-900">Records</h3>
        <RequirePermission permission={Permission.DNSManage}>
          <Button
            onClick={() => setAdding(true)}
            icon={<Plus aria-hidden="true" className="h-4 w-4" />}
          >
            Add record
          </Button>
        </RequirePermission>
      </div>

      {records.length === 0 ? (
        <p className="text-sm text-slate-500">This zone has no records yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="text-xs uppercase tracking-wide text-slate-400">
              <tr>
                <th className="py-1 pr-3 font-normal">Name</th>
                <th className="py-1 pr-3 font-normal">Type</th>
                <th className="py-1 pr-3 font-normal">Value</th>
                <th className="py-1 pr-3 font-normal">TTL</th>
                <th className="py-1" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {records.map((record) => (
                <tr key={record.id}>
                  <td className="py-1.5 pr-3 font-mono text-xs text-slate-700">{record.name}</td>
                  <td className="py-1.5 pr-3 text-slate-700">{record.type}</td>
                  <td className="py-1.5 pr-3 font-mono text-xs text-slate-700">
                    {describeValue(record)}
                  </td>
                  <td className="py-1.5 pr-3 text-slate-500">
                    {record.ttl === 0 ? `${detail.zone.ttl} (zone)` : record.ttl}
                  </td>
                  <td className="py-1.5 text-right">
                    {record.managed ? (
                      <span
                        className="text-xs text-slate-400"
                        title="Maintained by the panel for a subdomain"
                      >
                        managed
                      </span>
                    ) : (
                      <RequirePermission permission={Permission.DNSManage}>
                        <span className="inline-flex gap-1">
                          <Button
                            variant="ghost"
                            aria-label={`Edit ${record.name} ${record.type}`}
                            onClick={() => setEditing(record)}
                            icon={<Pencil aria-hidden="true" className="h-4 w-4" />}
                          />
                          <Button
                            variant="ghost"
                            aria-label={`Delete ${record.name} ${record.type}`}
                            onClick={() => remove.mutate(record.id)}
                            icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                          />
                        </span>
                      </RequirePermission>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {remove.error instanceof ApiError && (
        <Alert tone="danger" title="The record could not be removed">
          {remove.error.message}
        </Alert>
      )}

      {(adding || editing) && (
        <RecordDialog
          zoneId={detail.zone.id}
          record={editing}
          onClose={() => {
            setAdding(false);
            setEditing(null);
          }}
        />
      )}
    </section>
  );
}

/** describeValue renders a record's rdata the way a zone file would. */
function describeValue(record: DNSRecord): string {
  switch (record.type) {
    case 'MX':
      return `${record.priority} ${record.value}`;
    case 'SRV':
      return `${record.priority} ${record.weight} ${record.port} ${record.value}`;
    case 'CAA':
      return `${record.flags} ${record.tag} "${record.value}"`;
    default:
      return record.value;
  }
}

/** RecordDialog adds or replaces one record. */
function RecordDialog({
  zoneId,
  record,
  onClose,
}: {
  zoneId: string;
  record: DNSRecord | null;
  onClose: () => void;
}) {
  const create = useCreateDNSRecord(zoneId);
  const update = useUpdateDNSRecord();
  const [form, setForm] = useState<DNSRecordInput>({
    name: record?.name ?? '@',
    type: record?.type ?? 'A',
    ttl: record?.ttl ?? 0,
    value: record?.value ?? '',
    priority: record?.priority ?? 10,
    weight: record?.weight ?? 0,
    port: record?.port ?? 0,
    flags: record?.flags ?? 0,
    tag: record?.tag ?? 'issue',
  });

  const busy = create.isPending || update.isPending;
  const failure =
    create.error instanceof ApiError
      ? create.error.message
      : update.error instanceof ApiError
        ? update.error.message
        : null;

  const submit = () => {
    if (record) {
      update.mutate({ id: record.id, input: form }, { onSuccess: onClose });
    } else {
      create.mutate(form, { onSuccess: onClose });
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      busy={busy}
      title={record ? 'Edit record' : 'Add record'}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy}>
            Save record
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {failure && (
          <Alert tone="danger" title="The record was refused">
            {failure}
          </Alert>
        )}

        <div className="grid gap-4 sm:grid-cols-2">
          <TextField
            id="dns-record-name"
            label="Name"
            hint='Relative to the zone. "@" is the zone itself.'
            value={form.name}
            onChange={(event) => setForm({ ...form, name: event.target.value })}
          />
          <SelectField
            id="dns-record-type"
            label="Type"
            value={form.type}
            onChange={(event) =>
              setForm({ ...form, type: event.target.value as DNSRecordType })
            }
          >
            {RECORD_TYPES.map((type) => (
              <option key={type} value={type}>
                {type}
              </option>
            ))}
          </SelectField>
        </div>

        <TextField
          id="dns-record-value"
          label={valueLabel(form.type)}
          hint={valueHint(form.type)}
          value={form.value}
          onChange={(event) => setForm({ ...form, value: event.target.value })}
        />

        {(form.type === 'MX' || form.type === 'SRV') && (
          <TextField
            id="dns-record-priority"
            label="Priority"
            type="number"
            value={String(form.priority ?? 0)}
            onChange={(event) => setForm({ ...form, priority: Number(event.target.value) })}
          />
        )}

        {form.type === 'SRV' && (
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              id="dns-record-weight"
              label="Weight"
              type="number"
              value={String(form.weight ?? 0)}
              onChange={(event) => setForm({ ...form, weight: Number(event.target.value) })}
            />
            <TextField
              id="dns-record-port"
              label="Port"
              type="number"
              value={String(form.port ?? 0)}
              onChange={(event) => setForm({ ...form, port: Number(event.target.value) })}
            />
          </div>
        )}

        {form.type === 'CAA' && (
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              id="dns-record-flags"
              label="Flags"
              type="number"
              value={String(form.flags ?? 0)}
              onChange={(event) => setForm({ ...form, flags: Number(event.target.value) })}
            />
            <SelectField
              id="dns-record-tag"
              label="Tag"
              value={form.tag ?? 'issue'}
              onChange={(event) => setForm({ ...form, tag: event.target.value })}
            >
              <option value="issue">issue</option>
              <option value="issuewild">issuewild</option>
              <option value="iodef">iodef</option>
            </SelectField>
          </div>
        )}

        <TextField
          id="dns-record-ttl"
          label="TTL"
          type="number"
          hint="Seconds. Zero uses the zone's own default."
          value={String(form.ttl ?? 0)}
          onChange={(event) => setForm({ ...form, ttl: Number(event.target.value) })}
        />
      </div>
    </Modal>
  );
}

function valueLabel(type: DNSRecordType): string {
  switch (type) {
    case 'A':
    case 'AAAA':
      return 'Address';
    case 'TXT':
      return 'Text';
    case 'CAA':
      return 'Certificate authority';
    default:
      return 'Target';
  }
}

function valueHint(type: DNSRecordType): string {
  switch (type) {
    case 'A':
      return 'An IPv4 address.';
    case 'AAAA':
      return 'An IPv6 address.';
    case 'TXT':
      return 'Any text. Longer than 255 characters is fine — it is split as the format requires.';
    case 'CAA':
      return 'The authority allowed to issue, such as letsencrypt.org.';
    default:
      return 'A host name. End it with a dot to mean exactly that name rather than one inside this zone.';
  }
}

/**
 * Signing is DNSSEC, and the one step it cannot do for the operator.
 *
 * named generates the keys, signs, and rolls them over. What it cannot do is
 * put the DS record in the parent zone — that belongs to a registrar this panel
 * has no account with — so the record is shown to be copied, because until it
 * is there the signatures mean nothing to any resolver.
 */
function Signing({ detail }: { detail: DNSZoneDetail }) {
  const update = useUpdateDNSZone(detail.zone.id);
  const overview = useDNSOverview();
  const supported = overview.data?.supports_dnssec ?? true;
  const ds = detail.signing.ds ?? [];
  const keys = detail.signing.keys ?? [];

  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold text-slate-900">DNSSEC</h3>
      <RequirePermission permission={Permission.DNSManage}>
        <Toggle
          id="dns-zone-dnssec"
          label="Sign this zone"
          description={
            supported
              ? 'The name server generates the keys, keeps the zone signed, and rolls the keys over on its own schedule.'
              : 'This name server cannot sign zones.'
          }
          checked={detail.zone.dnssec}
          onChange={(checked) => update.mutate({ dnssec: checked })}
          disabled={!supported || update.isPending}
        />
      </RequirePermission>

      {update.error instanceof ApiError && (
        <Alert tone="danger" title="Signing could not be changed">
          {update.error.message}
        </Alert>
      )}

      {detail.zone.dnssec && keys.length > 0 && (
        <div className="rounded border border-surface-border p-3 text-sm">
          {keys.map((key) => (
            <p key={key.id} className="text-slate-700">
              <KeyRound aria-hidden="true" className="mr-1 inline h-3.5 w-3.5" />
              Key {key.id} ({key.algorithm}, {key.role}) — {key.rollover || 'no rollover scheduled'}
            </p>
          ))}
        </div>
      )}

      {detail.zone.dnssec && ds.length > 0 && (
        <Alert tone="info" title="Give this to the registrar">
          Until the DS record below is in the parent zone, no resolver knows to check these
          signatures. It is the one step the name server cannot do for you.
          {ds.map((record) => (
            <pre
              key={record.key_tag}
              className="mt-2 overflow-x-auto rounded bg-slate-900 p-2 font-mono text-xs text-slate-100"
            >
              {record.record}
            </pre>
          ))}
        </Alert>
      )}
    </section>
  );
}

/** Transfers is who may pull the whole zone. */
function Transfers({ detail }: { detail: DNSZoneDetail }) {
  const update = useUpdateDNSZone(detail.zone.id);
  const [allow, setAllow] = useState(detail.zone.allow_transfer.join(', '));
  const [notify, setNotify] = useState(detail.zone.also_notify.join(', '));

  const split = (value: string) =>
    value
      .split(',')
      .map((entry) => entry.trim())
      .filter(Boolean);

  return (
    <section className="space-y-3">
      <h3 className="text-sm font-semibold text-slate-900">Secondary servers</h3>
      <TextField
        id="dns-zone-transfer"
        label="Allow transfers to"
        hint="Addresses, separated by commas. Empty means nobody: a transfer hands over every name in the zone."
        placeholder="198.51.100.5"
        value={allow}
        onChange={(event) => setAllow(event.target.value)}
      />
      <TextField
        id="dns-zone-notify"
        label="Also notify"
        hint="Secondaries to tell about a change beyond those named by NS records."
        placeholder="198.51.100.5"
        value={notify}
        onChange={(event) => setNotify(event.target.value)}
      />
      <RequirePermission permission={Permission.DNSManage}>
        <Button
          loading={update.isPending}
          onClick={() =>
            update.mutate({ allow_transfer: split(allow), also_notify: split(notify) })
          }
        >
          Save
        </Button>
      </RequirePermission>
    </section>
  );
}

/**
 * RemoteSync moves the zone between the panel and a provider.
 *
 * Two buttons, deliberately, and never one control with a direction picker.
 * Publishing overwrites the provider and importing overwrites the panel; a
 * single button whose meaning depended on a dropdown is how somebody
 * eventually presses it with the dropdown they did not read.
 */
function RemoteSync({ detail }: { detail: DNSZoneDetail }) {
  const overview = useDNSOverview();
  const sync = useSyncDNSZone(detail.zone.id);
  const importZone = useImportDNSZone(detail.zone.id);
  const providers = overview.data?.providers ?? [];
  const [providerId, setProviderId] = useState('');
  const [prune, setPrune] = useState(false);
  const [replace, setReplace] = useState(false);

  // Said rather than hidden. Returning null here left an operator with a zone
  // editor that never mentioned synchronisation and a panel with nowhere to
  // enter a credential — the feature existed and nothing pointed at it.
  if (providers.length === 0) {
    return (
      <section className="space-y-2">
        <h3 className="text-sm font-semibold text-slate-900">Publish elsewhere</h3>
        <p className="text-sm text-slate-600">
          No DNS provider is connected. Connect one under{' '}
          <TextLink to="/dns">Providers</TextLink> to publish this zone to it, or to import
          the records it already has.
        </p>
      </section>
    );
  }

  const result = sync.data;

  return (
    <section className="space-y-3">
      <h3 className="text-sm font-semibold text-slate-900">Publish elsewhere</h3>
      <SelectField
        id="dns-sync-provider"
        label="Provider"
        value={providerId}
        onChange={(event) => setProviderId(event.target.value)}
      >
        <option value="">Choose a provider</option>
        {providers.map((provider) => (
          <option key={provider.id} value={provider.id}>
            {provider.label || provider.kind}
          </option>
        ))}
      </SelectField>

      <Toggle
        id="dns-sync-prune"
        label="Remove records the panel does not have"
        description="Off by default: a provider's zone usually holds records added there by hand, and removing what the panel does not recognise would break them with no warning."
        checked={prune}
        onChange={setPrune}
      />

      <RequirePermission permission={Permission.DNSManage}>
        <Button
          loading={sync.isPending}
          disabled={!providerId}
          icon={<UploadCloud aria-hidden="true" className="h-4 w-4" />}
          onClick={() => sync.mutate({ provider_id: providerId, prune })}
        >
          Publish
        </Button>
      </RequirePermission>

      {sync.error instanceof ApiError && (
        <Alert tone="danger" title="The provider refused the push">
          {sync.error.message}
        </Alert>
      )}

      <div className="space-y-3 border-t border-surface-border pt-3">
        <div>
          <h4 className="text-sm font-medium text-slate-900">Import from the provider</h4>
          <p className="mt-0.5 text-xs text-slate-500">
            The other direction, for a zone that already exists there. Nothing runs this on its
            own — publishing never decides to import instead.
          </p>
        </div>

        <Toggle
          id="dns-import-replace"
          label="Replace what the panel holds"
          description="Off by default: the import only adds records the panel does not already have. On, the panel's copy of this zone becomes the provider's copy exactly."
          checked={replace}
          onChange={setReplace}
        />

        <RequirePermission permission={Permission.DNSManage}>
          <Button
            loading={importZone.isPending}
            disabled={!providerId}
            icon={<DownloadCloud aria-hidden="true" className="h-4 w-4" />}
            onClick={() => importZone.mutate({ provider_id: providerId, replace })}
          >
            Import
          </Button>
        </RequirePermission>

        {importZone.error instanceof ApiError && (
          <Alert tone="danger" title="The zone could not be imported">
            {importZone.error.message}
          </Alert>
        )}
        {importZone.data && (
          <Alert tone="success" title="Imported">
            {importZone.data.imported} record(s) added
            {importZone.data.replaced > 0 && `, ${importZone.data.replaced} replaced`}.
            {importZone.data.skipped.length > 0 && (
              <>
                {' '}
                {importZone.data.skipped.length} were not brought in:
                <ul className="ml-4 mt-1 list-disc text-xs">
                  {importZone.data.skipped.slice(0, 5).map((item) => (
                    <li key={item}>{item}</li>
                  ))}
                </ul>
              </>
            )}
          </Alert>
        )}
      </div>
      {result && (
        <Alert tone="success" title="Published">
          {result.created} created, {result.updated} updated, {result.deleted} removed,{' '}
          {result.unchanged} already correct.
          {result.skipped && result.skipped.length > 0 && (
            <> Skipped: {result.skipped.join('; ')}.</>
          )}
        </Alert>
      )}
    </section>
  );
}
