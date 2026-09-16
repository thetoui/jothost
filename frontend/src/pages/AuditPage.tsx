import { useMemo, useState } from 'react';
import { ChevronDown, ChevronRight, ScrollText, ShieldAlert, User } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SelectField, TextField } from '@/components/ui/Field';
import { IconButton } from '@/components/ui/IconButton';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import type { AuditQuery } from '@/features/audit/api';
import { useAuditActions, useAuditTrail } from '@/features/audit/hooks';
import type { AuditEntry } from '@/types/api';

/** How many entries a page holds. */
const pageSize = 50;

/**
 * AuditPage shows who did what.
 *
 * The trail has been written since the first phase of this panel and, until
 * now, there was no way to read it: `audit.view` was a permission that guarded
 * nothing, so the answer to "who deleted that website" was to open a database
 * connection. An audit log nobody can read is one nobody reads.
 */
export function AuditPage() {
  const [action, setAction] = useState('');
  const [status, setStatus] = useState('');
  const [resourceType, setResourceType] = useState('');
  const [since, setSince] = useState('');
  const [offset, setOffset] = useState(0);
  const [expanded, setExpanded] = useState<string | null>(null);

  // Changing a filter goes back to the first page. Staying on page four of a
  // result set that no longer has one shows an empty table and no reason.
  const filter = <T,>(set: (value: T) => void) => (value: T) => {
    set(value);
    setOffset(0);
  };

  const params: AuditQuery = useMemo(() => {
    const next: AuditQuery = { limit: pageSize, offset };
    if (action) next.action = action;
    if (status) next.status = status;
    if (resourceType) next.resource_type = resourceType;
    // The input is a date; the API takes RFC 3339. Midnight local is what
    // somebody choosing a date means by it.
    if (since) next.since = new Date(`${since}T00:00:00`).toISOString();
    return next;
  }, [action, status, resourceType, since, offset]);

  const { data, isPending, isError, error, isPlaceholderData } = useAuditTrail(params);
  const { data: actionList } = useAuditActions();

  const entries = data?.entries ?? [];
  const total = data?.total ?? 0;
  const hasFilter = action !== '' || status !== '' || resourceType !== '' || since !== '';

  const resourceTypes = useMemo(() => {
    // Derived from the action names rather than fetched: an action is
    // "website.create", so its subject is the half before the dot, and that
    // is the grouping an operator thinks in.
    const seen = new Set<string>();
    for (const name of actionList?.actions ?? []) {
      const [prefix] = name.split('.');
      if (prefix) seen.add(prefix);
    }
    return [...seen].sort();
  }, [actionList]);

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">Audit trail</h1>
        <p className="mt-1 text-sm text-ink">
          Every sensitive action on this host, with who did it and from where. The record is
          append-only: nothing in this panel can change or remove an entry.
        </p>
      </header>

      <Card label="Audit filters">
        <CardBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <SelectField
            id="audit-action"
            label="Action"
            value={action}
            onChange={(event) => filter(setAction)(event.target.value)}
          >
            <option value="">Everything</option>
            {(actionList?.actions ?? []).map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </SelectField>

          <SelectField
            id="audit-subject"
            label="Subject"
            value={resourceType}
            onChange={(event) => filter(setResourceType)(event.target.value)}
          >
            <option value="">Everything</option>
            {resourceTypes.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </SelectField>

          <SelectField
            id="audit-status"
            label="Outcome"
            value={status}
            onChange={(event) => filter(setStatus)(event.target.value)}
          >
            <option value="">Everything</option>
            <option value="SUCCESS">Succeeded</option>
            <option value="FAILURE">Refused or failed</option>
          </SelectField>

          <TextField
            id="audit-since"
            label="Since"
            type="date"
            value={since}
            onChange={(event) => filter(setSince)(event.target.value)}
          />
        </CardBody>
      </Card>

      {isError && (
        <Alert tone="danger" title="The audit trail could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <Card label="Audit entries">
        <CardHeader
          icon={<TintedIcon tone="brand" icon={<ScrollText className="h-4 w-4" />} />}
          title="Recorded actions"
          description={
            // The total, not the page. "50 entries" and "50 of 9,318" answer
            // very different questions, and only one of them is the truth.
            isPending
              ? 'Reading the trail…'
              : total === 0
                ? hasFilter
                  ? 'Nothing matched this filter'
                  : 'Nothing has been recorded yet'
                : `${entries.length} of ${total.toLocaleString()} ${
                    hasFilter ? 'matching entries' : 'entries'
                  }, newest first`
          }
        />

        {isPending ? (
          <CardBody>
            <SkeletonRows rows={6} />
          </CardBody>
        ) : entries.length === 0 ? (
          <CardBody>
            <EmptyState
              icon={<ScrollText className="h-5 w-5" />}
              title={hasFilter ? 'Nothing matched' : 'The trail is empty'}
              description={
                hasFilter
                  ? 'No recorded action matches these filters. Widen them, or clear the date.'
                  : 'Sensitive actions are recorded here as they happen — creating a website, issuing a certificate, changing the firewall.'
              }
            />
          </CardBody>
        ) : (
          <div className={`overflow-x-auto ${isPlaceholderData ? 'opacity-60' : ''}`}>
            <table className="w-full min-w-[52rem] border-collapse text-sm">
              <thead>
                <tr className="border-b border-surface-border text-left text-xs uppercase tracking-wide text-ink-muted">
                  <th scope="col" className="w-8 px-2 py-2">
                    <span className="sr-only">Detail</span>
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    When
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    Action
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    Who
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    From
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    Outcome
                  </th>
                </tr>
              </thead>
              <tbody>
                {entries.map((entry) => (
                  <AuditRow
                    key={entry.id}
                    entry={entry}
                    open={expanded === entry.id}
                    onToggle={() => setExpanded(expanded === entry.id ? null : entry.id)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}

        {total > pageSize && (
          <div className="flex items-center justify-between gap-3 border-t border-surface-border px-5 py-3">
            <p className="text-xs text-ink-muted">
              {offset + 1}–{Math.min(offset + entries.length, total)} of {total.toLocaleString()}
            </p>
            <div className="flex gap-2">
              <Button
                size="sm"
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - pageSize))}
              >
                Newer
              </Button>
              <Button
                size="sm"
                disabled={offset + pageSize >= total}
                onClick={() => setOffset(offset + pageSize)}
              >
                Older
              </Button>
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}

function AuditRow({
  entry,
  open,
  onToggle,
}: {
  entry: AuditEntry;
  open: boolean;
  onToggle: () => void;
}) {
  const failed = entry.status !== null && entry.status !== 'SUCCESS';
  const detail = entry.metadata && Object.keys(entry.metadata).length > 0;

  return (
    <>
      <tr className={`border-b border-surface-border ${failed ? 'bg-danger-50/40' : ''}`}>
        <td className="px-2 py-2 align-middle">
          {detail && (
            <IconButton
              size="sm"
              onClick={onToggle}
              aria-expanded={open}
              label={`${open ? 'Hide' : 'Show'} the detail of ${entry.action}`}
              icon={
                open ? (
                  <ChevronDown aria-hidden="true" className="h-4 w-4" />
                ) : (
                  <ChevronRight aria-hidden="true" className="h-4 w-4" />
                )
              }
            />
          )}
        </td>
        <td className="whitespace-nowrap px-3 py-2 text-ink">
          <time dateTime={entry.created_at} title={entry.created_at}>
            {new Date(entry.created_at).toLocaleString()}
          </time>
        </td>
        <td className="px-3 py-2">
          <span className="font-mono text-xs text-ink-strong">{entry.action}</span>
        </td>
        <td className="px-3 py-2">
          {entry.username ? (
            <span className="inline-flex items-center gap-1.5 text-ink">
              <User aria-hidden="true" className="h-3.5 w-3.5 text-ink-dim" />
              {entry.username}
            </span>
          ) : (
            // Two different silences, and they are worth telling apart: an
            // action with no actor at all, and one whose actor was deleted
            // afterwards. The row survives; the name cannot.
            <span className="text-xs italic text-ink-dim">
              {entry.user_id ? 'account deleted' : 'not signed in'}
            </span>
          )}
        </td>
        <td className="px-3 py-2 font-mono text-xs text-ink-muted">{entry.ip_address ?? '—'}</td>
        <td className="px-3 py-2">
          {entry.status === null ? (
            <span className="text-xs text-ink-dim">—</span>
          ) : (
            <StatusPill
              label={failed ? 'Failed' : 'Succeeded'}
              tone={failed ? 'error' : 'ok'}
              dot
            />
          )}
        </td>
      </tr>

      {open && detail && (
        <tr className="border-b border-surface-border bg-surface-muted">
          <td />
          <td colSpan={5} className="px-3 py-3">
            <dl className="grid gap-x-6 gap-y-1 sm:grid-cols-2">
              {Object.entries(entry.metadata ?? {}).map(([key, value]) => (
                <div key={key} className="flex gap-2 text-xs">
                  <dt className="shrink-0 text-ink-muted">{key}</dt>
                  <dd className="min-w-0 break-all font-mono text-ink">
                    {typeof value === 'string' ? value : JSON.stringify(value)}
                  </dd>
                </div>
              ))}
              {entry.resource_id && (
                <div className="flex gap-2 text-xs">
                  <dt className="shrink-0 text-ink-muted">
                    {entry.resource_type ?? 'resource'} id
                  </dt>
                  <dd className="min-w-0 break-all font-mono text-ink">
                    {entry.resource_id}
                  </dd>
                </div>
              )}
              {entry.user_agent && (
                <div className="flex gap-2 text-xs sm:col-span-2">
                  <dt className="shrink-0 text-ink-muted">agent</dt>
                  <dd className="min-w-0 break-all text-ink">{entry.user_agent}</dd>
                </div>
              )}
            </dl>
          </td>
        </tr>
      )}
    </>
  );
}

/** Shown in place of the page to somebody without `audit.view`. */
export function AuditDenied() {
  return (
    <Alert tone="warning" title="You cannot read the audit trail">
      <span className="inline-flex items-center gap-1.5">
        <ShieldAlert aria-hidden="true" className="h-4 w-4" />
        Reading it needs the audit.view permission, which is separate from the rest on purpose: it
        records what everyone on this host did, not only you.
      </span>
    </Alert>
  );
}
