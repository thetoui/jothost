import { useState, type FormEvent } from 'react';
import { ChevronDown, ChevronRight, ExternalLink, Layers, Plus, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { TextLink } from '@/components/ui/Link';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { SelectField, TextField } from '@/components/ui/Field';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useCreateSubdomain,
  useDeleteSubdomain,
  useSubdomains,
} from '@/features/websites/hooks';
import { websiteStatusPill } from '@/features/websites/status';
import { ApiError } from '@/services/apiClient';
import type { DocumentRootMode, PHPPoolMode, SystemUserMode, Website } from '@/types/api';

interface SubdomainPanelProps {
  site: Website;
}

/**
 * SubdomainPanel lists and manages the sites beneath a website.
 *
 * A subdomain here is a website in its own right — its own directory, its own
 * certificate, its own PHP — so each row links to the same detail page any
 * other site has, rather than to a cut-down version of one.
 */
export function SubdomainPanel({ site }: SubdomainPanelProps) {
  const { data, isPending, isError, error } = useSubdomains(site.id);
  const [adding, setAdding] = useState(false);

  const subdomains = data?.subdomains ?? [];

  return (
    <Card>
      <CardHeader
        title="Subdomains"
        description="Each one is a site of its own, served under this domain."
        action={
          <RequirePermission permission={Permission.WebsiteCreate}>
            <Button
              variant="secondary"
              onClick={() => setAdding((open) => !open)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              disabled={site.status !== 'active' && site.status !== 'failed'}
            >
              Add subdomain
            </Button>
          </RequirePermission>
        }
      />
      <CardBody className="space-y-4">
        {adding && (
          <CreateSubdomainForm
            parent={site}
            onDone={() => setAdding(false)}
            onCancel={() => setAdding(false)}
          />
        )}

        {isPending ? (
          <SkeletonRows rows={2} />
        ) : isError ? (
          <Alert tone="danger" title="The subdomains could not be loaded">
            {error instanceof Error ? error.message : 'Try again in a moment.'}
          </Alert>
        ) : subdomains.length === 0 ? (
          <EmptyState
            icon={<Layers className="h-5 w-5" />}
            title="No subdomains yet"
            description={`A subdomain such as shop.${site.primary_domain} is hosted separately, with its own files and certificate.`}
          />
        ) : (
          <ul className="divide-y divide-surface-border">
            {subdomains.map((subdomain) => (
              <SubdomainRow key={subdomain.id} parentId={site.id} subdomain={subdomain} />
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

function SubdomainRow({ parentId, subdomain }: { parentId: string; subdomain: Website }) {
  const [expanded, setExpanded] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const remove = useDeleteSubdomain(parentId);

  const pill = websiteStatusPill(subdomain.status);
  const settling = subdomain.status === 'creating' || subdomain.status === 'deleting';
  const wildcard = subdomain.primary_domain.startsWith('*.');

  const removeError =
    remove.error instanceof ApiError
      ? remove.error.message
      : remove.error
        ? 'The subdomain could not be removed.'
        : null;

  return (
    <li className="py-2.5">
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={() => setExpanded((open) => !open)}
          aria-expanded={expanded}
          className={`flex min-w-0 flex-1 items-center gap-2 rounded-sm text-left ${focusRingTight}`}
        >
          {expanded ? (
            <ChevronDown aria-hidden="true" className="h-4 w-4 shrink-0 text-ink-dim" />
          ) : (
            <ChevronRight aria-hidden="true" className="h-4 w-4 shrink-0 text-ink-dim" />
          )}
          <span className="truncate font-medium text-ink-strong">{subdomain.primary_domain}</span>
          <StatusPill label={pill.label} tone={pill.tone} dot pulse={settling} />
          {wildcard && (
            <span className="rounded-full bg-surface-sunken px-2 py-0.5 text-xs text-ink">
              Catch-all
            </span>
          )}
        </button>

        <div className="flex shrink-0 items-center gap-2">
          {/* A wildcard has no single address to open: it answers for every
              name beneath the parent that nothing else claims. */}
          {!wildcard && (
            <TextLink
              href={`http://${subdomain.primary_domain}`}
              size="xs"
              className="inline-flex items-center gap-1"
            >
              Open
              <ExternalLink aria-hidden="true" className="h-3 w-3" />
            </TextLink>
          )}
          <TextLink to={`/websites/${subdomain.id}`} size="xs">
            Manage
          </TextLink>
          <RequirePermission permission={Permission.WebsiteDelete}>
            <Button
              variant="ghost"
              onClick={() => setConfirming(true)}
              disabled={subdomain.status === 'deleting'}
              aria-label={`Remove ${subdomain.primary_domain}`}
              icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
            />
          </RequirePermission>
        </div>
      </div>

      {expanded && (
        <dl className="mt-2 grid gap-x-6 gap-y-1 pl-6 text-xs text-ink-muted sm:grid-cols-2">
          <Fact label="Files" value={subdomain.document_root} mono />
          <Fact label="System user" value={subdomain.system_user} mono />
          <Fact
            label="Layout"
            value={
              subdomain.document_root_mode === 'isolated'
                ? 'Isolated — its own directory'
                : "Nested — inside the parent's directory"
            }
          />
          <Fact
            label="PHP"
            value={
              subdomain.php_pool_mode === 'dedicated'
                ? `Its own pool${subdomain.php_version ? ` — ${subdomain.php_version}` : ''}`
                : "The parent's pool"
            }
          />
        </dl>
      )}

      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() =>
          remove.mutate(subdomain.id, { onSuccess: () => setConfirming(false) })
        }
        title="Remove this subdomain?"
        description="Its vhost is removed and the name stops being served."
        confirmLabel="Remove subdomain"
        destructive
        loading={remove.isPending}
        error={removeError}
      >
        <p>
          <span className="font-medium text-ink-strong">{subdomain.primary_domain}</span> stops being
          served.{' '}
          {subdomain.system_user_mode === 'inherit'
            ? "The account it shares with the parent site is kept, because the parent's files belong to it."
            : 'Its own system account is removed with it.'}
        </p>
      </ConfirmDialog>
    </li>
  );
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-1.5">
      <dt className="shrink-0">{label}</dt>
      <dd className={mono ? 'truncate font-mono text-ink' : 'truncate text-ink'}>
        {value}
      </dd>
    </div>
  );
}

function CreateSubdomainForm({
  parent,
  onDone,
  onCancel,
}: {
  parent: Website;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState('');
  const [rootMode, setRootMode] = useState<DocumentRootMode>('nested');
  const [poolMode, setPoolMode] = useState<PHPPoolMode>('inherit');
  const [userMode, setUserMode] = useState<SystemUserMode>('inherit');
  const create = useCreateSubdomain();

  // A dedicated account with the parent's pool would run PHP as one user over
  // files owned by another. The API refuses it; the form moves the pool with
  // the account so nobody has to discover that by being refused.
  function chooseUserMode(mode: SystemUserMode) {
    setUserMode(mode);
    if (mode === 'dedicated') {
      setPoolMode('dedicated');
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    create.mutate(
      {
        parentId: parent.id,
        name: name.trim(),
        documentRootMode: rootMode,
        phpPoolMode: poolMode,
        systemUserMode: userMode,
      },
      {
        onSuccess: () => {
          setName('');
          onDone();
        },
      },
    );
  }

  const error =
    create.error instanceof ApiError
      ? create.error.message
      : create.error
        ? 'The subdomain could not be created.'
        : null;

  return (
    <form
      onSubmit={submit}
      className="space-y-3 rounded-card border border-surface-border bg-surface-sunken/40 p-3"
    >
      <TextField
        id="subdomain-name"
        label="Name"
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder="shop"
        autoComplete="off"
        required
        hint={
          name.trim()
            ? `${name.trim().replace(/^\.+|\.+$/g, '')}.${parent.primary_domain}`
            : `The label only — it is created under ${parent.primary_domain}. Use * for a catch-all.`
        }
      />

      <div className="grid gap-3 sm:grid-cols-3">
        <SelectField
          id="subdomain-root-mode"
          label="Files"
          value={rootMode}
          onChange={(event) => setRootMode(event.target.value as DocumentRootMode)}
        >
          <option value="nested">Inside the parent&apos;s directory</option>
          <option value="isolated">In a directory of its own</option>
        </SelectField>
        <SelectField
          id="subdomain-user-mode"
          label="System account"
          value={userMode}
          onChange={(event) => chooseUserMode(event.target.value as SystemUserMode)}
        >
          <option value="inherit">The parent&apos;s account</option>
          <option value="dedicated">Its own account</option>
        </SelectField>
        <SelectField
          id="subdomain-pool-mode"
          label="PHP pool"
          value={poolMode}
          disabled={userMode === 'dedicated'}
          onChange={(event) => setPoolMode(event.target.value as PHPPoolMode)}
        >
          <option value="inherit">The parent&apos;s pool</option>
          <option value="dedicated">Its own pool</option>
        </SelectField>
      </div>

      {userMode === 'dedicated' && (
        <p className="text-xs text-ink-muted">
          Its own account means the parent site cannot read its files, and it needs its own PHP
          pool to run as itself.
        </p>
      )}

      {error && (
        <Alert tone="danger" title="The subdomain could not be created">
          {error}
        </Alert>
      )}

      <div className="flex gap-2">
        <Button type="submit" loading={create.isPending} disabled={!name.trim()}>
          Create subdomain
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
