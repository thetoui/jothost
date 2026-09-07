import { useState } from 'react';
import { LayoutTemplate, Plus, Trash2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useDNSTemplates, useDeleteDNSTemplate, useSaveDNSTemplate } from '@/features/dns/hooks';
import { ApiError } from '@/services/apiClient';
import type { DNSPlaceholder, DNSRecordType, DNSTemplate, DNSTemplateRecord } from '@/types/api';

const RECORD_TYPES: DNSRecordType[] = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'CAA', 'SRV'];

/**
 * TemplateCard is where the records a new zone starts with are decided.
 *
 * Before this, a zone was seeded with two records hard-coded in Go: an A for
 * the apex and one for www. Anybody wanting every new domain to start with an
 * MX or an SPF record added them by hand to each one, and anybody not wanting
 * www deleted it each time.
 */
export function TemplateCard() {
  const { data, isPending, isError, error } = useDNSTemplates();
  const remove = useDeleteDNSTemplate();
  const [editing, setEditing] = useState<DNSTemplate | 'new' | null>(null);
  const [pending, setPending] = useState<DNSTemplate | null>(null);

  const templates = data?.templates ?? [];
  const placeholders = data?.placeholders ?? [];

  return (
    <Card label="Zone templates">
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<LayoutTemplate className="h-4 w-4" />} />}
        title="Zone templates"
        description="What a new zone starts with. The default one is applied whenever a zone is created with seeding on."
        action={
          <RequirePermission permission={Permission.DNSManage}>
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              onClick={() => setEditing('new')}
            >
              New template
            </Button>
          </RequirePermission>
        }
      />

      <CardBody className="space-y-3">
        {isError && (
          <Alert tone="danger" title="The templates could not be read">
            {error instanceof Error ? error.message : 'Try again in a moment.'}
          </Alert>
        )}
        {remove.error instanceof ApiError && (
          <Alert tone="danger" title="The template was not removed">
            {remove.error.message}
          </Alert>
        )}

        {isPending ? (
          <SkeletonRows rows={2} />
        ) : templates.length === 0 ? (
          <p className="text-sm text-slate-600">
            None. New zones will be created empty &mdash; the delegation and the SOA record, and
            nothing pointing anywhere.
          </p>
        ) : (
          <ul className="divide-y divide-surface-border rounded-md border border-surface-border">
            {templates.map((template) => (
              <TemplateRow
                key={template.id}
                template={template}
                onEdit={() => setEditing(template)}
                onRemove={() => setPending(template)}
              />
            ))}
          </ul>
        )}
      </CardBody>

      {editing && (
        <TemplateDialog
          template={editing === 'new' ? null : editing}
          placeholders={placeholders}
          onClose={() => setEditing(null)}
        />
      )}

      <ConfirmDialog
        open={Boolean(pending)}
        onClose={() => setPending(null)}
        onConfirm={() => {
          if (!pending) return;
          remove.mutate(pending.id, { onSuccess: () => setPending(null) });
        }}
        title={`Delete ${pending?.name ?? ''}?`}
        description="Zones already created from it keep their records; only what future zones start with changes."
        confirmLabel="Delete template"
        destructive
        loading={remove.isPending}
        error={remove.error instanceof ApiError ? remove.error.message : null}
      />
    </Card>
  );
}

function TemplateRow({
  template,
  onEdit,
  onRemove,
}: {
  template: DNSTemplate;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const records = template.records ?? [];

  return (
    <li className="flex flex-wrap items-center justify-between gap-3 px-3 py-2">
      <div className="min-w-0">
        <p className="flex items-center gap-2 text-sm font-medium text-slate-900">
          <span className="truncate">{template.name}</span>
          {template.is_default && (
            <span className="rounded bg-brand-50 px-1.5 py-0.5 text-xs font-normal text-brand-700">
              Default
            </span>
          )}
          {template.builtin && (
            <span className="rounded bg-slate-100 px-1.5 py-0.5 text-xs font-normal text-slate-600">
              Built in
            </span>
          )}
        </p>
        <p className="mt-0.5 text-xs text-slate-500">
          {records.length} {records.length === 1 ? 'record' : 'records'}
          {template.description ? ` · ${template.description}` : ''}
        </p>
        {records.length > 0 && (
          <p className="mt-1 truncate font-mono text-xs text-slate-400">
            {records
              .slice(0, 4)
              .map((record) => `${record.name} ${record.type}`)
              .join(' · ')}
            {records.length > 4 ? ' …' : ''}
          </p>
        )}
      </div>

      <RequirePermission permission={Permission.DNSManage}>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="secondary" onClick={onEdit}>
            Edit
          </Button>
          {/* The built-in can be edited but not removed: an operator wanting
              different defaults should not have to make a second template, and
              deleting the only one leaves new zones with nothing at all. */}
          {!template.builtin && (
            <Button size="sm" variant="danger" onClick={onRemove}>
              <Trash2 className="h-4 w-4" />
              <span className="sr-only">Delete {template.name}</span>
            </Button>
          )}
        </div>
      </RequirePermission>
    </li>
  );
}

/** blankRecord is what a newly added line starts as. */
function blankRecord(): DNSTemplateRecord {
  return { name: '@', type: 'A', ttl: 0, value: '{ip}', priority: 0, weight: 0, port: 0 };
}

/**
 * TemplateDialog edits a template's whole record list.
 *
 * Whole, rather than one record at a time: the API replaces the list on save,
 * so reordering or removing a line needs no vocabulary of its own, and a
 * half-applied edit cannot leave a template seeding partial zones.
 */
function TemplateDialog({
  template,
  placeholders,
  onClose,
}: {
  template: DNSTemplate | null;
  placeholders: DNSPlaceholder[];
  onClose: () => void;
}) {
  const save = useSaveDNSTemplate();
  const [name, setName] = useState(template?.name ?? '');
  const [description, setDescription] = useState(template?.description ?? '');
  const [isDefault, setIsDefault] = useState(template?.is_default ?? false);
  const [records, setRecords] = useState<DNSTemplateRecord[]>(
    template?.records?.length ? template.records : [blankRecord()],
  );

  const failure = save.error instanceof ApiError ? save.error.message : null;

  const change = (index: number, patch: Partial<DNSTemplateRecord>) =>
    setRecords((current) =>
      current.map((record, i) => (i === index ? { ...record, ...patch } : record)),
    );

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      busy={save.isPending}
      title={template ? `Edit ${template.name}` : 'New zone template'}
      description="Every record here is written into a new zone the template is applied to."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={save.isPending}
            disabled={!name.trim() || save.isPending}
            onClick={() =>
              save.mutate(
                {
                  ...(template ? { id: template.id } : {}),
                  input: {
                    name: name.trim(),
                    description: description.trim(),
                    is_default: isDefault,
                    records,
                  },
                },
                { onSuccess: onClose },
              )
            }
          >
            Save template
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {failure && (
          <Alert tone="danger" title="The template was refused">
            {failure}
          </Alert>
        )}

        <div className="grid gap-4 sm:grid-cols-2">
          <TextField
            id="dns-template-name"
            label="Name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="With mail"
          />
          <TextField
            id="dns-template-description"
            label="Description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            hint="What this one is for. Optional."
          />
        </div>

        <Toggle
          id="dns-template-default"
          label="Use for new zones"
          description="One template is the default. Turning this on takes it off whichever one holds it now."
          checked={isDefault}
          onChange={setIsDefault}
        />

        {placeholders.length > 0 && (
          <p className="text-xs text-slate-500">
            {/* Listed from the API rather than written here: a form offering a
                placeholder the panel cannot fill produces a zone containing
                that literal text, weeks later and in every new domain. */}
            In a name or a value you can write{' '}
            {placeholders.map((placeholder, index) => (
              <span key={placeholder.token}>
                {index > 0 ? ', ' : ''}
                <code className="font-mono text-slate-700">{placeholder.token}</code> for{' '}
                {placeholder.means}
              </span>
            ))}
            . Anything else in braces is refused when you save.
          </p>
        )}

        <div className="space-y-3">
          {records.map((record, index) => (
            <div key={index} className="rounded-md border border-surface-border p-3">
              <div className="grid gap-3 sm:grid-cols-[1fr_8rem]">
                <TextField
                  id={`dns-template-record-name-${index}`}
                  label="Name"
                  value={record.name}
                  onChange={(event) => change(index, { name: event.target.value })}
                  hint={'Relative to the zone. "@" is the zone itself.'}
                />
                <SelectField
                  id={`dns-template-record-type-${index}`}
                  label="Type"
                  value={record.type}
                  onChange={(event) => change(index, { type: event.target.value as DNSRecordType })}
                >
                  {RECORD_TYPES.map((type) => (
                    <option key={type} value={type}>
                      {type}
                    </option>
                  ))}
                </SelectField>
              </div>

              <div className="mt-3">
                <TextField
                  id={`dns-template-record-value-${index}`}
                  label="Value"
                  value={record.value}
                  onChange={(event) => change(index, { value: event.target.value })}
                />
              </div>

              {(record.type === 'MX' || record.type === 'SRV') && (
                <div className="mt-3 grid gap-3 sm:grid-cols-3">
                  <TextField
                    id={`dns-template-record-priority-${index}`}
                    label="Priority"
                    type="number"
                    value={String(record.priority)}
                    onChange={(event) => change(index, { priority: Number(event.target.value) })}
                  />
                  {record.type === 'SRV' && (
                    <>
                      <TextField
                        id={`dns-template-record-weight-${index}`}
                        label="Weight"
                        type="number"
                        value={String(record.weight)}
                        onChange={(event) => change(index, { weight: Number(event.target.value) })}
                      />
                      <TextField
                        id={`dns-template-record-port-${index}`}
                        label="Port"
                        type="number"
                        value={String(record.port)}
                        onChange={(event) => change(index, { port: Number(event.target.value) })}
                      />
                    </>
                  )}
                </div>
              )}

              <div className="mt-3 flex items-end justify-between gap-3">
                <div className="w-32">
                  <TextField
                    id={`dns-template-record-ttl-${index}`}
                    label="TTL"
                    type="number"
                    value={String(record.ttl)}
                    onChange={(event) => change(index, { ttl: Number(event.target.value) })}
                    hint="0 uses the zone's."
                  />
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setRecords((current) => current.filter((_, i) => i !== index))}
                >
                  <Trash2 aria-hidden="true" className="h-4 w-4" />
                  <span className="sr-only">Remove record {index + 1}</span>
                </Button>
              </div>
            </div>
          ))}

          <Button
            size="sm"
            variant="secondary"
            icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            onClick={() => setRecords((current) => [...current, blankRecord()])}
          >
            Add record
          </Button>

          {records.length === 0 && (
            <p className="text-xs text-slate-500">
              With no records, a zone created from this template gets its delegation and its SOA
              and nothing else.
            </p>
          )}
        </div>
      </div>
    </Modal>
  );
}
