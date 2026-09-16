import { useState } from 'react';
import { Cloud, Plus, Trash2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SelectField, TextField } from '@/components/ui/Field';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useAddDNSProvider, useRemoveDNSProvider } from '@/features/dns/hooks';
import { ApiError } from '@/services/apiClient';
import type { DNSOverview, DNSProvider } from '@/types/api';

/**
 * The providers the panel knows how to talk to.
 *
 * One today. It is a list rather than a Cloudflare-shaped form because the
 * second one should be an entry here and not a second dialog.
 */
const kinds = [{ value: 'cloudflare', label: 'Cloudflare' }] as const;

/**
 * ProviderCard is where a remote DNS provider's credentials are entered.
 *
 * Until this existed there was no way to enter them at all. The endpoint, the
 * API client method and the hooks were all there, the zone editor's publish
 * and import controls were written and wired — and they returned null when no
 * provider was configured, which was always. The panel offered synchronisation
 * it gave nobody any means of switching on.
 */
export function ProviderCard({ overview }: { overview: DNSOverview }) {
  const [connecting, setConnecting] = useState(false);
  const providers = overview.providers ?? [];
  const remove = useRemoveDNSProvider();

  return (
    <Card label="DNS providers">
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<Cloud className="h-4 w-4" />} />}
        title="Providers"
        description="Somewhere else that answers for these zones. Connect one to publish a zone to it, or import a zone from it."
        action={
          <RequirePermission permission={Permission.DNSManage}>
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              onClick={() => setConnecting(true)}
            >
              Connect
            </Button>
          </RequirePermission>
        }
      />

      <CardBody className="space-y-3">
        {remove.error instanceof ApiError && (
          <Alert tone="danger" title="The provider was not removed">
            {remove.error.message}
          </Alert>
        )}

        {providers.length === 0 ? (
          <p className="text-sm text-ink">
            None connected. The panel serves these zones itself; a provider is only needed to
            keep somebody else&rsquo;s copy of them in step.
          </p>
        ) : (
          <ul className="divide-y divide-surface-border rounded-md border border-surface-border">
            {providers.map((provider) => (
              <ProviderRow
                key={provider.id}
                provider={provider}
                onRemove={() => remove.mutate(provider.id)}
                removing={remove.isPending}
              />
            ))}
          </ul>
        )}
      </CardBody>

      <ConnectDialog open={connecting} onClose={() => setConnecting(false)} />
    </Card>
  );
}

function ProviderRow({
  provider,
  onRemove,
  removing,
}: {
  provider: DNSProvider;
  onRemove: () => void;
  removing: boolean;
}) {
  const label = provider.label || provider.kind;

  return (
    <li className="flex items-center justify-between gap-3 px-3 py-2">
      <div className="min-w-0">
        <p className="truncate text-sm font-medium text-ink-strong">{label}</p>
        <p className="mt-0.5 text-xs text-ink-muted">
          {provider.kind}
          {provider.account_id ? ` · account ${provider.account_id}` : ''}
          {/* The last attempt, not just the last success: a provider that has
              been failing for a week looks identical to a new one otherwise. */}
          {provider.last_sync_status ? ` · last sync ${provider.last_sync_status}` : ' · never synced'}
        </p>
        {provider.last_sync_error && (
          <p className="mt-0.5 text-xs text-danger-600">{provider.last_sync_error}</p>
        )}
      </div>

      <RequirePermission permission={Permission.DNSManage}>
        <Button size="sm" variant="danger" onClick={onRemove} disabled={removing}>
          <Trash2 className="h-4 w-4" />
          <span className="sr-only">Disconnect {label}</span>
        </Button>
      </RequirePermission>
    </li>
  );
}

/**
 * ConnectDialog takes the credentials.
 *
 * The token is a password field and is never read back: the panel encrypts it
 * on arrival and has no endpoint that returns it. A token that can rewrite
 * every DNS record in somebody's account is not something to show again for
 * confirmation.
 */
function ConnectDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const add = useAddDNSProvider();
  const [kind, setKind] = useState<string>(kinds[0].value);
  const [label, setLabel] = useState('');
  const [token, setToken] = useState('');
  const [accountID, setAccountID] = useState('');

  const close = () => {
    // The token does not outlive the dialog, whether it was accepted or not.
    setToken('');
    setLabel('');
    setAccountID('');
    add.reset();
    onClose();
  };

  return (
    <Modal
      open={open}
      onClose={close}
      title="Connect a DNS provider"
      description="The panel keeps serving these zones itself. A provider is where a copy of them is published."
      busy={add.isPending}
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={add.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!token.trim() || add.isPending}
            loading={add.isPending}
            onClick={() =>
              add.mutate(
                {
                  kind,
                  label: label.trim(),
                  token: token.trim(),
                  ...(accountID.trim() ? { account_id: accountID.trim() } : {}),
                },
                { onSuccess: close },
              )
            }
          >
            Connect
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {add.error instanceof ApiError && (
          <Alert tone="danger" title="The provider was not connected">
            {add.error.message}
          </Alert>
        )}

        <SelectField
          id="dns-provider-kind"
          label="Provider"
          value={kind}
          onChange={(event) => setKind(event.target.value)}
        >
          {kinds.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </SelectField>

        <TextField
          id="dns-provider-label"
          label="Name"
          value={label}
          onChange={(event) => setLabel(event.target.value)}
          placeholder="Cloudflare"
          hint="What to call it in the panel. Optional."
        />

        <TextField
          id="dns-provider-token"
          label="API token"
          type="password"
          value={token}
          onChange={(event) => setToken(event.target.value)}
          autoComplete="off"
          hint="Stored encrypted and never shown again. A token scoped to editing the zones you want synchronised is enough — it does not need account-wide access."
        />

        <TextField
          id="dns-provider-account"
          label="Account ID"
          value={accountID}
          onChange={(event) => setAccountID(event.target.value)}
          hint="Only needed where an account holds several zones of the same name. Optional."
        />
      </div>
    </Modal>
  );
}
