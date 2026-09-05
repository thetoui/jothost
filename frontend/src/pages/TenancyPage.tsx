import { useMemo, useState } from 'react';
import {
  Boxes,
  Building2,
  CircleUser,
  Gauge,
  PauseCircle,
  PlayCircle,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  UserCog,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SelectField, TextField } from '@/components/ui/Field';
import { EmptyState, ProgressBar, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { Tabs } from '@/components/ui/Tabs';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { errorMessage } from '@/features/auth/hooks';
import {
  byteLabel,
  countedDimensions,
  fullness,
  isolationLabel,
  limitLabel,
  usedOf,
} from '@/features/tenancy/format';
import {
  useAddAddon,
  useCreateAccount,
  useCreatePlan,
  useCreateSubscription,
  useDeletePlan,
  useDeleteSubscription,
  useMeasureSubscription,
  useSetSubscriptionStatus,
  useStartImpersonation,
  useTenancy,
} from '@/features/tenancy/hooks';
import type { AccountTier, ServicePlan, Subscription, TenantAccount } from '@/types/api';

type Tab = 'subscriptions' | 'plans' | 'accounts';

/**
 * TenancyPage is who has what: the accounts on this server, the plans they are
 * sold, and what each subscription is actually using.
 */
export function TenancyPage() {
  const { data, isPending, isError, error } = useTenancy();
  const [tab, setTab] = useState<Tab>('subscriptions');

  const accounts = data?.accounts ?? [];
  const plans = data?.plans ?? [];
  const subscriptions = data?.subscriptions ?? [];
  const host = data?.host;

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Accounts &amp; plans</h1>
        <p className="mt-1 text-sm text-slate-500">
          Who this server hosts, what they were sold, and what they are using. Countable
          limits are enforced when somebody asks for one more; disk and bandwidth are
          measured on the host and reported.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The tenancy could not be loaded">
          {errorMessage(error, 'Try again in a moment.')}
        </Alert>
      )}

      {host && !host.isolation_available && (
        <Alert tone="warning" title="Resource limits are recorded, not enforced">
          {host.isolation_detail} A plan&rsquo;s CPU and memory caps are written down and
          nothing on this host is applying them.
        </Alert>
      )}
      {host?.isolation_available && !host.placement && (
        <Alert tone="warning" title="Nothing runs inside the subscription slices yet">
          The caps are installed and systemd has read them, but the web and PHP processes
          are not placed in a subscription&rsquo;s slice, so nothing is capped in practice.
        </Alert>
      )}

      <Tabs
        label="Tenancy sections"
        value={tab}
        onChange={setTab}
        items={[
          { value: 'subscriptions', label: 'Subscriptions', count: subscriptions.length },
          { value: 'plans', label: 'Plans', count: plans.length },
          { value: 'accounts', label: 'Accounts', count: accounts.length },
        ]}
      />

      {isPending && <SkeletonRows rows={4} />}

      {!isPending && tab === 'subscriptions' && (
        <SubscriptionsTab subscriptions={subscriptions} accounts={accounts} plans={plans} />
      )}
      {!isPending && tab === 'plans' && <PlansTab plans={plans} />}
      {!isPending && tab === 'accounts' && <AccountsTab accounts={accounts} />}
    </div>
  );
}

// ------------------------------------------------------------ subscriptions

function SubscriptionsTab({
  subscriptions,
  accounts,
  plans,
}: {
  subscriptions: Subscription[];
  accounts: TenantAccount[];
  plans: ServicePlan[];
}) {
  const [creating, setCreating] = useState(false);

  return (
    <Card>
      <CardHeader
        title="Subscriptions"
        description="What each customer has, and how much of it is in use."
        icon={<TintedIcon tone="brand" icon={<Boxes className="h-4 w-4" />} />}
        action={
          <RequirePermission permission={Permission.TenantManage}>
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus className="h-4 w-4" /> New subscription
            </Button>
          </RequirePermission>
        }
      />

      {subscriptions.length === 0 ? (
        <EmptyState
          icon={<Boxes className="h-6 w-6" />}
          title="No subscriptions yet"
          description="Create a plan, then put a customer on it."
        />
      ) : (
        <ul className="divide-y divide-surface-border">
          {subscriptions.map((subscription) => (
            <SubscriptionRow key={subscription.id} subscription={subscription} plans={plans} />
          ))}
        </ul>
      )}

      <NewSubscriptionDialog
        open={creating}
        onClose={() => setCreating(false)}
        accounts={accounts}
        plans={plans}
      />
    </Card>
  );
}

function SubscriptionRow({
  subscription,
  plans,
}: {
  subscription: Subscription;
  plans: ServicePlan[];
}) {
  const measure = useMeasureSubscription();
  const setStatus = useSetSubscriptionStatus();
  const remove = useDeleteSubscription();
  const addAddon = useAddAddon();
  const [addonId, setAddonId] = useState('');

  const suspended = subscription.status === 'suspended';
  const isolation = isolationLabel(subscription);
  const addons = useMemo(() => plans.filter((plan) => plan.kind === 'addon'), [plans]);

  return (
    <li className="px-4 py-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-sm font-medium text-slate-900">
              {subscription.name}
            </span>
            <StatusPill
              label={suspended ? 'Suspended' : 'Active'}
              tone={suspended ? 'warn' : 'ok'}
              dot
            />
            <StatusPill
              label={isolation.text}
              tone={
                isolation.tone === 'success'
                  ? 'ok'
                  : isolation.tone === 'warning'
                    ? 'warn'
                    : isolation.tone === 'danger'
                      ? 'error'
                      : 'neutral'
              }
            />
          </div>
          <p className="mt-0.5 text-xs text-slate-500">
            {subscription.owner_username} &middot; {subscription.plan_name} &middot;{' '}
            {subscription.enforcement === 'hard' ? 'hard limits' : 'soft limits'}
          </p>
          {suspended && subscription.suspended_reason && (
            <p className="mt-1 text-xs text-warn-700">{subscription.suspended_reason}</p>
          )}
        </div>

        <RequirePermission permission={Permission.TenantManage}>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="secondary"
              onClick={() => measure.mutate(subscription.id)}
              disabled={measure.isPending}
            >
              <RefreshCw className="h-4 w-4" /> Measure
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onClick={() =>
                setStatus.mutate({
                  id: subscription.id,
                  status: suspended ? 'active' : 'suspended',
                  reason: suspended ? '' : 'Suspended from the panel',
                })
              }
              disabled={setStatus.isPending}
            >
              {suspended ? (
                <>
                  <PlayCircle className="h-4 w-4" /> Resume
                </>
              ) : (
                <>
                  <PauseCircle className="h-4 w-4" /> Suspend
                </>
              )}
            </Button>
            <Button
              size="sm"
              variant="danger"
              onClick={() => remove.mutate(subscription.id)}
              disabled={remove.isPending || subscription.websites.length > 0}
              title={
                subscription.websites.length > 0
                  ? 'Move or delete its websites first'
                  : undefined
              }
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
        </RequirePermission>
      </div>

      <div className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {countedDimensions.map((dimension) => {
          const used = usedOf(subscription.usage, dimension.key);
          const limit = subscription.limits[dimension.limit];
          const percent = fullness(used, limit);
          return (
            <div key={dimension.key}>
              <div className="flex items-baseline justify-between text-xs">
                <span className="text-slate-600">{dimension.label}</span>
                <span className="text-slate-500">
                  {used} / {limitLabel(limit)}
                </span>
              </div>
              {percent !== null && (
                <ProgressBar
                  value={percent}
                  tone={percent >= 100 ? 'danger' : percent >= 80 ? 'warn' : 'brand'}
                />
              )}
            </div>
          );
        })}

        <MeasuredFigure
          label="Disk"
          used={byteLabel(subscription.usage.disk_bytes)}
          limit={subscription.limits.disk_mb}
        />
        <MeasuredFigure
          label="Bandwidth this month"
          used={byteLabel(subscription.usage.bandwidth_bytes)}
          limit={subscription.limits.bandwidth_mb}
        />
      </div>

      {subscription.usage.measure_error && (
        <p className="mt-2 text-xs text-warn-700">{subscription.usage.measure_error}</p>
      )}
      {!subscription.usage.measured_at && (
        <p className="mt-2 text-xs text-slate-500">
          Disk and bandwidth have not been measured yet.
        </p>
      )}

      {subscription.addons.length > 0 && (
        <p className="mt-2 text-xs text-slate-500">
          Add-ons:{' '}
          {subscription.addons
            .map((addon) => `${addon.name}${addon.quantity > 1 ? ` ×${addon.quantity}` : ''}`)
            .join(', ')}
        </p>
      )}

      <RequirePermission permission={Permission.TenantManage}>
        {addons.length > 0 && (
          <div className="mt-3 flex flex-wrap items-end gap-2">
            <div className="w-64">
              <SelectField
                id={`addon-${subscription.id}`}
                label="Add an add-on"
                value={addonId}
                onChange={(event) => setAddonId(event.target.value)}
              >
                <option value="">Choose an add-on…</option>
                {addons.map((addon) => (
                  <option key={addon.id} value={addon.id}>
                    {addon.name}
                  </option>
                ))}
              </SelectField>
            </div>
            <Button
              size="sm"
              variant="secondary"
              disabled={!addonId || addAddon.isPending}
              onClick={() =>
                addAddon.mutate(
                  { id: subscription.id, planId: addonId, quantity: 1 },
                  { onSuccess: () => setAddonId('') },
                )
              }
            >
              Add
            </Button>
          </div>
        )}
      </RequirePermission>
    </li>
  );
}

/**
 * A measured figure shows what was read and what was sold, and never a bar.
 *
 * No progress bar because the number is a sample: it is as old as the last
 * measurement, and a bar implies a live reading that this is not.
 */
function MeasuredFigure({
  label,
  used,
  limit,
}: {
  label: string;
  used: string;
  limit: number | null;
}) {
  return (
    <div>
      <div className="flex items-baseline justify-between text-xs">
        <span className="text-slate-600">{label}</span>
        <span className="text-slate-500">
          {used} / {limit === null ? 'Unlimited' : `${limit} MB`}
        </span>
      </div>
    </div>
  );
}

function NewSubscriptionDialog({
  open,
  onClose,
  accounts,
  plans,
}: {
  open: boolean;
  onClose: () => void;
  accounts: TenantAccount[];
  plans: ServicePlan[];
}) {
  const create = useCreateSubscription();
  const [owner, setOwner] = useState('');
  const [plan, setPlan] = useState('');
  const [name, setName] = useState('');

  const customers = accounts.filter((account) => account.tier !== 'admin');
  const sellable = plans.filter((entry) => entry.kind === 'plan');

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New subscription"
      description="Put an account on a plan. Its websites are then counted against that plan's limits."
      busy={create.isPending}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            onClick={() =>
              create.mutate(
                { owner_user_id: owner, plan_id: plan, name },
                {
                  onSuccess: () => {
                    setOwner('');
                    setPlan('');
                    setName('');
                    onClose();
                  },
                },
              )
            }
            disabled={!owner || !plan || !name.trim() || create.isPending}
          >
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {create.isError && (
          <Alert tone="danger" title="The subscription was not created">
            {errorMessage(create.error)}
          </Alert>
        )}
        <SelectField
          id="subscription-owner"
          label="Account"
          value={owner}
          onChange={(event) => setOwner(event.target.value)}
        >
          <option value="">Choose an account…</option>
          {customers.map((account) => (
            <option key={account.id} value={account.id}>
              {account.username} ({account.tier})
            </option>
          ))}
        </SelectField>
        <SelectField
          id="subscription-plan"
          label="Plan"
          value={plan}
          onChange={(event) => setPlan(event.target.value)}
        >
          <option value="">Choose a plan…</option>
          {sellable.map((entry) => (
            <option key={entry.id} value={entry.id}>
              {entry.name}
            </option>
          ))}
        </SelectField>
        <TextField
          id="subscription-name"
          label="Name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="Acme hosting"
        />
      </div>
    </Modal>
  );
}

// -------------------------------------------------------------------- plans

function PlansTab({ plans }: { plans: ServicePlan[] }) {
  const [creating, setCreating] = useState(false);
  const remove = useDeletePlan();

  return (
    <Card>
      <CardHeader
        title="Plans and add-ons"
        description="What a subscription may use. Blank means unlimited; zero means none."
        icon={<TintedIcon tone="brand" icon={<Gauge className="h-4 w-4" />} />}
        action={
          <RequirePermission permission={Permission.TenantManage}>
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus className="h-4 w-4" /> New plan
            </Button>
          </RequirePermission>
        }
      />

      {plans.length === 0 ? (
        <EmptyState
          icon={<Gauge className="h-6 w-6" />}
          title="No plans yet"
          description="A plan is what a subscription is sold: how many websites, databases and mailboxes it includes."
        />
      ) : (
        <ul className="divide-y divide-surface-border">
          {plans.map((plan) => (
            <li key={plan.id} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium text-slate-900">{plan.name}</span>
                  <StatusPill
                    label={plan.kind === 'addon' ? 'Add-on' : 'Plan'}
                    tone={plan.kind === 'addon' ? 'info' : 'neutral'}
                  />
                  {plan.kind === 'plan' && (
                    <StatusPill
                      label={plan.enforcement === 'hard' ? 'Hard limits' : 'Soft limits'}
                      tone={plan.enforcement === 'hard' ? 'neutral' : 'warn'}
                    />
                  )}
                </div>
                <p className="mt-0.5 text-xs text-slate-500">
                  {plan.owner_username
                    ? `Sold by ${plan.owner_username}`
                    : 'In the server catalogue'}
                  {' · '}
                  {plan.subscriptions} subscription{plan.subscriptions === 1 ? '' : 's'}
                </p>
                <p className="mt-1 text-xs text-slate-600">
                  {countedDimensions
                    .map(
                      (dimension) =>
                        `${dimension.label}: ${limitLabel(plan.limits[dimension.limit])}`,
                    )
                    .join(' · ')}
                </p>
                <p className="mt-0.5 text-xs text-slate-600">
                  Disk: {limitLabel(plan.limits.disk_mb)} MB · Bandwidth:{' '}
                  {limitLabel(plan.limits.bandwidth_mb)} MB
                  {plan.isolation.cpu_percent !== null &&
                    ` · CPU: ${plan.isolation.cpu_percent}%`}
                  {plan.isolation.memory_mb !== null && ` · Memory: ${plan.isolation.memory_mb} MB`}
                </p>
              </div>

              <RequirePermission permission={Permission.TenantManage}>
                <Button
                  size="sm"
                  variant="danger"
                  onClick={() => remove.mutate(plan.id)}
                  disabled={remove.isPending || plan.subscriptions > 0}
                  title={plan.subscriptions > 0 ? 'Subscriptions are on this plan' : undefined}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      <NewPlanDialog open={creating} onClose={() => setCreating(false)} />
    </Card>
  );
}

function NewPlanDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const create = useCreatePlan();
  const [name, setName] = useState('');
  const [kind, setKind] = useState<'plan' | 'addon'>('plan');
  const [enforcement, setEnforcement] = useState<'hard' | 'soft'>('hard');
  const [fields, setFields] = useState<Record<string, string>>({});

  const numeric = (key: string): number | null => {
    const raw = fields[key];
    if (raw === undefined || raw.trim() === '') {
      return null;
    }
    const value = Number(raw);
    return Number.isFinite(value) ? value : null;
  };

  const limitInputs = [
    ['max_websites', 'Websites'],
    ['max_subdomains', 'Subdomains'],
    ['max_databases', 'Databases'],
    ['max_mailboxes', 'Mailboxes'],
    ['max_ftp_users', 'FTP accounts'],
    ['max_cron_jobs', 'Scheduled jobs'],
    ['disk_mb', 'Disk (MB)'],
    ['bandwidth_mb', 'Bandwidth (MB/month)'],
  ] as const;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={kind === 'addon' ? 'New add-on' : 'New plan'}
      description="Leave a box empty for no limit. Enter 0 to include none of that thing — they are not the same."
      size="lg"
      busy={create.isPending}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            disabled={!name.trim() || create.isPending}
            onClick={() =>
              create.mutate(
                {
                  name,
                  kind,
                  enforcement,
                  limits: {
                    max_websites: numeric('max_websites'),
                    max_subdomains: numeric('max_subdomains'),
                    max_databases: numeric('max_databases'),
                    max_mailboxes: numeric('max_mailboxes'),
                    max_ftp_users: numeric('max_ftp_users'),
                    max_cron_jobs: numeric('max_cron_jobs'),
                    disk_mb: numeric('disk_mb'),
                    bandwidth_mb: numeric('bandwidth_mb'),
                  },
                  isolation: {
                    cpu_percent: numeric('cpu_percent'),
                    memory_mb: numeric('memory_mb'),
                    io_weight: numeric('io_weight'),
                  },
                },
                {
                  onSuccess: () => {
                    setName('');
                    setFields({});
                    onClose();
                  },
                },
              )
            }
          >
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {create.isError && (
          <Alert tone="danger" title="The plan was not created">
            {errorMessage(create.error)}
          </Alert>
        )}

        <TextField
          id="plan-name"
          label="Name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="Starter"
        />

        <div className="grid gap-3 sm:grid-cols-2">
          <SelectField
            id="plan-kind"
            label="Kind"
            value={kind}
            onChange={(event) => setKind(event.target.value as 'plan' | 'addon')}
            hint="An add-on adds its numbers to a plan a subscription is already on."
          >
            <option value="plan">Plan</option>
            <option value="addon">Add-on</option>
          </SelectField>

          {kind === 'plan' && (
            <SelectField
              id="plan-enforcement"
              label="When a limit is reached"
              value={enforcement}
              onChange={(event) => setEnforcement(event.target.value as 'hard' | 'soft')}
              hint="Hard refuses the request. Soft allows it and records the overage."
            >
              <option value="hard">Refuse</option>
              <option value="soft">Allow and record</option>
            </SelectField>
          )}
        </div>

        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {limitInputs.map(([key, label]) => (
            <TextField
              key={key}
              id={`plan-${key}`}
              label={label}
              inputMode="numeric"
              value={fields[key] ?? ''}
              onChange={(event) =>
                setFields((previous) => ({ ...previous, [key]: event.target.value }))
              }
              placeholder="Unlimited"
            />
          ))}
        </div>

        {kind === 'plan' && (
          <>
            <p className="text-xs text-slate-500">
              Resource limits are applied as a systemd slice per subscription. Leave them
              empty to cap nothing.
            </p>
            <div className="grid gap-3 sm:grid-cols-3">
              <TextField
                id="plan-cpu_percent"
                label="CPU (%)"
                inputMode="numeric"
                value={fields.cpu_percent ?? ''}
                onChange={(event) =>
                  setFields((previous) => ({ ...previous, cpu_percent: event.target.value }))
                }
                placeholder="Uncapped"
                hint="Above 100 means more than one core."
              />
              <TextField
                id="plan-memory_mb"
                label="Memory (MB)"
                inputMode="numeric"
                value={fields.memory_mb ?? ''}
                onChange={(event) =>
                  setFields((previous) => ({ ...previous, memory_mb: event.target.value }))
                }
                placeholder="Uncapped"
              />
              <TextField
                id="plan-io_weight"
                label="IO weight"
                inputMode="numeric"
                value={fields.io_weight ?? ''}
                onChange={(event) =>
                  setFields((previous) => ({ ...previous, io_weight: event.target.value }))
                }
                placeholder="Default"
                hint="1–10000, a share under contention."
              />
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

// ----------------------------------------------------------------- accounts

function AccountsTab({ accounts }: { accounts: TenantAccount[] }) {
  const [creating, setCreating] = useState(false);
  const impersonate = useStartImpersonation();

  return (
    <Card>
      <CardHeader
        title="Accounts"
        description="Every account below yours. An admin owns the server, a reseller sells space on it, a customer buys some."
        icon={<TintedIcon tone="brand" icon={<Building2 className="h-4 w-4" />} />}
        action={
          <RequirePermission permission={Permission.TenantManage}>
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus className="h-4 w-4" /> New account
            </Button>
          </RequirePermission>
        }
      />

      <ul className="divide-y divide-surface-border">
        {accounts.map((account) => (
          <li
            key={account.id}
            className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
          >
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-medium text-slate-900">{account.username}</span>
                <StatusPill label={account.tier} tone={tierTone(account.tier)} />
                {account.status !== 'active' && (
                  <StatusPill label={account.status} tone="warn" />
                )}
              </div>
              <p className="mt-0.5 text-xs text-slate-500">
                {account.full_name ?? account.email ?? '—'}
                {account.parent_username ? ` · under ${account.parent_username}` : ''}
                {` · ${account.subscriptions} subscription${account.subscriptions === 1 ? '' : 's'}`}
              </p>
            </div>

            {account.tier !== 'admin' && (
              <RequirePermission permission={Permission.TenantImpersonate}>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={impersonate.isPending}
                  onClick={() =>
                    impersonate.mutate({
                      userId: account.id,
                      reason: 'Support, from the accounts page',
                    })
                  }
                >
                  <UserCog className="h-4 w-4" /> Sign in as
                </Button>
              </RequirePermission>
            )}
          </li>
        ))}
      </ul>

      {impersonate.isError && (
        <CardBody>
          <Alert tone="danger" title="That account could not be signed in to">
            {errorMessage(impersonate.error)}
          </Alert>
        </CardBody>
      )}

      <NewAccountDialog open={creating} onClose={() => setCreating(false)} accounts={accounts} />
    </Card>
  );
}

function tierTone(tier: AccountTier): 'info' | 'ok' | 'neutral' {
  if (tier === 'admin') {
    return 'info';
  }
  return tier === 'reseller' ? 'ok' : 'neutral';
}

function NewAccountDialog({
  open,
  onClose,
  accounts,
}: {
  open: boolean;
  onClose: () => void;
  accounts: TenantAccount[];
}) {
  const create = useCreateAccount();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [tier, setTier] = useState<AccountTier>('customer');
  const [parent, setParent] = useState('');

  const possibleParents = accounts.filter((account) => account.tier !== 'customer');

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New account"
      description="An account is always created strictly below its parent: an admin owns resellers, a reseller owns customers."
      busy={create.isPending}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button
            disabled={!username.trim() || password.length < 12 || create.isPending}
            onClick={() =>
              create.mutate(
                {
                  username,
                  password,
                  tier,
                  ...(parent ? { parent_id: parent } : {}),
                },
                {
                  onSuccess: () => {
                    setUsername('');
                    setPassword('');
                    onClose();
                  },
                },
              )
            }
          >
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {create.isError && (
          <Alert tone="danger" title="The account was not created">
            {errorMessage(create.error)}
          </Alert>
        )}
        <TextField
          id="account-username"
          label="Username"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          adornment={<CircleUser className="h-4 w-4" />}
        />
        <TextField
          id="account-password"
          label="Password"
          type="password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          hint="At least 12 characters."
        />
        <SelectField
          id="account-tier"
          label="Tier"
          value={tier}
          onChange={(event) => setTier(event.target.value as AccountTier)}
        >
          <option value="customer">Customer</option>
          <option value="reseller">Reseller</option>
        </SelectField>
        <SelectField
          id="account-parent"
          label="Under"
          value={parent}
          onChange={(event) => setParent(event.target.value)}
          hint="Leave as yourself unless this account belongs to one of your resellers."
        >
          <option value="">Me</option>
          {possibleParents.map((account) => (
            <option key={account.id} value={account.id}>
              {account.username}
            </option>
          ))}
        </SelectField>
        <p className="flex items-start gap-2 text-xs text-slate-500">
          <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0" />
          A customer account owns a subscription. It is not given a role that browses this
          panel: signing in as them from this page is how you reach their hosting.
        </p>
      </div>
    </Modal>
  );
}
