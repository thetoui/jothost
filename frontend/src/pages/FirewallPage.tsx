import { useState, type FormEvent } from 'react';
import { AlertTriangle, Plus, Shield, ShieldOff, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, Skeleton } from '@/components/ui/Loading';
import { SelectField, TextField } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useFirewall,
  useFirewallChange,
  useRollbackChange,
} from '@/features/firewall/hooks';
import { ApiError } from '@/services/apiClient';
import type { FirewallAction, FirewallRule, FirewallStatus } from '@/types/api';

/**
 * FirewallPage manages the host's packet filter.
 *
 * Every change here is provisional until the browser has proved the panel is
 * still reachable through it. That is not a formality: a firewall mistake takes
 * away the connection the fix would arrive over, and it is the one class of
 * change this panel cannot undo for you afterwards.
 */
export function FirewallPage() {
  const { data, isPending, isError, error } = useFirewall();

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">Firewall</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Which traffic this host accepts. Every change is undone automatically unless the
          panel is still reachable afterwards.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The firewall could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {isPending ? (
        <Skeleton className="h-64 w-full rounded-card" />
      ) : data ? (
        data.available ? (
          <FirewallView status={data} />
        ) : (
          <Card>
            <EmptyState
              icon={<ShieldOff className="h-6 w-6" />}
              title="This host has no firewall the panel can manage"
              description={
                data.reason ||
                'ufw is not installed, or the Agent cannot reach the packet filter.'
              }
            />
          </Card>
        )
      ) : null}
    </div>
  );
}

function FirewallView({ status }: { status: FirewallStatus }) {
  const change = useFirewallChange();
  const rollback = useRollbackChange();
  const [adding, setAdding] = useState(false);
  const [confirmingToggle, setConfirmingToggle] = useState(false);

  const error =
    change.error instanceof ApiError
      ? change.error.message
      : change.error
        ? 'The change could not be made.'
        : null;

  return (
    <div className="space-y-4">
      {status.pending && (
        <Alert tone="warning" title="A change is waiting to be confirmed">
          <p>
            It is live now, and it will be undone automatically at{' '}
            {new Date(status.pending.deadline).toLocaleTimeString()} unless the panel
            confirms it. If you can read this, the panel is still reachable.
          </p>
          <div className="mt-2">
            <Button
              variant="secondary"
              loading={rollback.isPending}
              onClick={() => rollback.mutate(status.pending!.id)}
            >
              Undo it now
            </Button>
          </div>
        </Alert>
      )}

      {change.stage === 'reverting' && (
        <Alert tone="danger" title="The panel could not be reached after the change">
          The change is being undone automatically. Nothing needs to be done — the rules
          this host had before will be back within a minute.
        </Alert>
      )}

      {error && (
        <Alert tone="danger" title="The change was refused">
          {error}
        </Alert>
      )}

      <Card>
        <CardHeader
          title={status.enabled ? 'The firewall is on' : 'The firewall is off'}
          description={
            status.enabled
              ? `Incoming traffic is ${status.default_incoming} by default; outgoing is ${status.default_outgoing}.`
              : 'Nothing is being filtered. The rules below are staged and take effect when it is switched on.'
          }
          icon={
            <TintedIcon
              tone={status.enabled ? 'ok' : 'warn'}
              icon={status.enabled ? <Shield className="h-4 w-4" /> : <ShieldOff className="h-4 w-4" />}
            />
          }
          action={
            <RequirePermission permission={Permission.ServerManage}>
              <Button
                variant={status.enabled ? 'danger' : 'primary'}
                loading={change.isPending}
                disabled={Boolean(status.pending)}
                onClick={() => setConfirmingToggle(true)}
              >
                {status.enabled ? 'Switch off' : 'Switch on'}
              </Button>
            </RequirePermission>
          }
        />
        <CardBody>
          <p className="text-xs text-ink-muted">
            Ports {status.guarded_ports.join(', ')} can never be closed from here: they are
            how this host is administered, and the panel cannot put them back.
          </p>
        </CardBody>
      </Card>

      <Card>
        <CardHeader
          title="Rules"
          description={`${status.rules.length} rule${status.rules.length === 1 ? '' : 's'}`}
          action={
            <RequirePermission permission={Permission.ServerManage}>
              <Button
                variant="secondary"
                onClick={() => setAdding((open) => !open)}
                disabled={Boolean(status.pending)}
                icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              >
                New rule
              </Button>
            </RequirePermission>
          }
        />
        <CardBody className="space-y-4">
          {adding && (
            <AddRuleForm
              busy={change.isPending}
              onCancel={() => setAdding(false)}
              onSubmit={(rule) => {
                change.mutate(
                  { kind: 'add', rule },
                  { onSuccess: () => setAdding(false) },
                );
              }}
            />
          )}

          {status.rules.length === 0 ? (
            <EmptyState
              icon={<Shield className="h-5 w-5" />}
              title="No rules"
              description="Nothing is allowed or denied specifically; the default policy decides."
            />
          ) : (
            <ul className="divide-y divide-surface-border">
              {status.rules.map((rule) => (
                <RuleRow
                  key={`${rule.direction}-${rule.action}-${rule.protocol}-${rule.port}-${rule.source}`}
                  rule={rule}
                  busy={change.isPending || Boolean(status.pending)}
                  onDelete={() =>
                    change.mutate({
                      kind: 'delete',
                      rule: {
                        action: rule.action,
                        direction: rule.direction,
                        protocol: rule.protocol,
                        port: rule.port,
                        source: rule.source,
                      },
                    })
                  }
                />
              ))}
            </ul>
          )}
        </CardBody>
      </Card>

      <ConfirmDialog
        open={confirmingToggle}
        onClose={() => setConfirmingToggle(false)}
        onConfirm={() => {
          change.mutate(
            { kind: status.enabled ? 'disable' : 'enable' },
            { onSuccess: () => setConfirmingToggle(false) },
          );
        }}
        title={status.enabled ? 'Switch the firewall off?' : 'Switch the firewall on?'}
        confirmLabel={status.enabled ? 'Switch off' : 'Switch on'}
        destructive={status.enabled}
        loading={change.isPending}
        error={error}
      >
        {status.enabled ? (
          <p>
            Nothing will be filtered: every port on this host becomes reachable from
            anywhere it is routable.
          </p>
        ) : (
          <p>
            The rules above start being enforced, and everything else incoming is denied.
            If that leaves the panel unreachable, the change is undone automatically.
          </p>
        )}
      </ConfirmDialog>
    </div>
  );
}

function RuleRow({
  rule,
  busy,
  onDelete,
}: {
  rule: FirewallRule;
  busy: boolean;
  onDelete: () => void;
}) {
  const tone =
    rule.action === 'allow' ? 'ok' : rule.action === 'limit' ? 'warn' : 'error';

  return (
    <li className="flex flex-wrap items-center gap-3 py-2.5">
      <StatusPill label={rule.action} tone={tone} />
      <span className="font-mono text-sm text-ink-strong">
        {rule.port || 'any port'}
        {rule.protocol !== 'any' && `/${rule.protocol}`}
      </span>
      <span className="text-sm text-ink-muted">
        {rule.direction === 'in' ? 'from' : 'to'} {rule.source}
      </span>
      {rule.comment && <span className="text-xs text-ink-dim">{rule.comment}</span>}

      <div className="ml-auto">
        <RequirePermission permission={Permission.ServerManage}>
          <Button
            variant="ghost"
            disabled={busy}
            onClick={onDelete}
            aria-label={`Delete the rule for ${rule.port || 'every port'}`}
            icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
          />
        </RequirePermission>
      </div>
    </li>
  );
}

function AddRuleForm({
  busy,
  onSubmit,
  onCancel,
}: {
  busy: boolean;
  onSubmit: (rule: {
    action: FirewallAction;
    direction: 'in' | 'out';
    protocol: 'tcp' | 'udp' | 'any';
    port: string;
    source: string;
    comment: string;
  }) => void;
  onCancel: () => void;
}) {
  const [action, setAction] = useState<FirewallAction>('allow');
  const [direction, setDirection] = useState<'in' | 'out'>('in');
  const [protocol, setProtocol] = useState<'tcp' | 'udp' | 'any'>('tcp');
  const [port, setPort] = useState('');
  const [source, setSource] = useState('any');
  const [comment, setComment] = useState('');

  function submit(event: FormEvent) {
    event.preventDefault();
    onSubmit({ action, direction, protocol, port: port.trim(), source: source.trim() || 'any', comment: comment.trim() });
  }

  return (
    <form
      onSubmit={submit}
      className="space-y-3 rounded-card border border-surface-border bg-surface-sunken/40 p-3"
    >
      <div className="grid gap-3 sm:grid-cols-3">
        <SelectField
          id="fw-action"
          label="Action"
          value={action}
          onChange={(event) => setAction(event.target.value as FirewallAction)}
        >
          <option value="allow">Allow</option>
          <option value="deny">Deny (drop silently)</option>
          <option value="reject">Reject (refuse openly)</option>
          <option value="limit">Limit (allow, then rate-limit)</option>
        </SelectField>
        <SelectField
          id="fw-direction"
          label="Direction"
          value={direction}
          onChange={(event) => setDirection(event.target.value as 'in' | 'out')}
        >
          <option value="in">Incoming</option>
          <option value="out">Outgoing</option>
        </SelectField>
        <SelectField
          id="fw-protocol"
          label="Protocol"
          value={protocol}
          onChange={(event) => setProtocol(event.target.value as 'tcp' | 'udp' | 'any')}
        >
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
          <option value="any">Any</option>
        </SelectField>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <TextField
          id="fw-port"
          label="Port"
          value={port}
          onChange={(event) => setPort(event.target.value)}
          placeholder="8080"
          hint="One port, or a range like 7080:7090. Leave empty for every port."
        />
        <TextField
          id="fw-source"
          label="From"
          value={source}
          onChange={(event) => setSource(event.target.value)}
          placeholder="any"
          hint="An address, a CIDR block, or any."
        />
        <TextField
          id="fw-comment"
          label="Note"
          value={comment}
          onChange={(event) => setComment(event.target.value)}
          placeholder="what this is for"
        />
      </div>

      <div className="flex gap-2">
        <Button type="submit" loading={busy}>
          Add rule
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>

      <p className="flex items-start gap-1.5 text-xs text-ink-muted">
        <AlertTriangle aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        The rule takes effect immediately and is undone automatically unless this page can
        still reach the panel afterwards.
      </p>
    </form>
  );
}
