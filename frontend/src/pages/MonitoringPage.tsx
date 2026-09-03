import { useState } from 'react';
import {
  Activity,
  AlertTriangle,
  BellOff,
  CheckCircle2,
  Clock,
  Plus,
  ServerCog,
  Trash2,
} from 'lucide-react';

import { Alert as AlertBanner } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useAcknowledgeAlert,
  useAlertRules,
  useCreateAlertRule,
  useDeleteAlertRule,
  useMonitoringOverview,
  useUpdateAlertRule,
} from '@/features/monitoring/hooks';
import { ApiError } from '@/services/apiClient';
import type {
  Alert,
  AlertMetric,
  AlertRule,
  AlertRuleInput,
  MonitoringOverview,
} from '@/types/api';

/**
 * MonitoringPage shows what is wrong, what has been wrong, and what is watched.
 *
 * It is deliberately not the dashboard. The dashboard answers "what is this
 * machine doing right now" from a live reading; this page answers "what has
 * been wrong, since when, and is it still" from rows — and only the second can
 * be acknowledged or looked back at.
 */
export function MonitoringPage() {
  const { data, isPending, isError, error } = useMonitoringOverview();

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Monitoring</h1>
        <p className="mt-1 text-sm text-slate-500">
          What has gone wrong on this host, and what the panel is watching for.
        </p>
      </header>

      {isError && (
        <AlertBanner tone="danger" title="The monitor could not be read">
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
          <OpenAlerts overview={data} />
          <Services overview={data} />
          <Rules overview={data} />
          <RecentAlerts overview={data} />
        </>
      )}
    </div>
  );
}

/** OpenAlerts is what has not cleared. */
function OpenAlerts({ overview }: { overview: MonitoringOverview }) {
  const acknowledge = useAcknowledgeAlert();
  const open = overview.open ?? [];

  if (open.length === 0) {
    return (
      <AlertBanner tone="success" title="Nothing is wrong">
        Every rule below is satisfied. This is what the panel says when it has looked and found
        nothing, which is not the same as not having looked.
      </AlertBanner>
    );
  }

  return (
    <Card label="Open alerts">
      <CardHeader
        title={`${open.length} open ${open.length === 1 ? 'alert' : 'alerts'}`}
        description={
          overview.counts.unacknowledged > 0
            ? `${overview.counts.unacknowledged} nobody has acknowledged`
            : 'All acknowledged'
        }
        icon={<TintedIcon icon={<AlertTriangle className="h-5 w-5" />} tone="danger" />}
      />
      <ul className="divide-y divide-slate-100">
        {open.map((alert) => (
          <li key={alert.id} className="flex flex-wrap items-start gap-3 px-4 py-3">
            <div className="flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <SeverityPill severity={alert.severity} />
                <span className="font-medium text-slate-900">{alert.message}</span>
              </div>
              <div className="mt-0.5 text-xs text-slate-500">
                Since {formatWhen(alert.opened_at)}
                {alert.worst != null && ` · worst ${formatNumber(alert.worst)}`}
                {alert.acknowledged_at && ' · acknowledged'}
              </div>
            </div>
            {!alert.acknowledged_at && (
              <RequirePermission permission={Permission.MonitorManage}>
                <Button
                  onClick={() => acknowledge.mutate(alert.id)}
                  loading={acknowledge.isPending}
                  icon={<BellOff aria-hidden="true" className="h-4 w-4" />}
                >
                  Acknowledge
                </Button>
              </RequirePermission>
            )}
          </li>
        ))}
      </ul>
      <CardBody>
        {/* Said out loud, because a panel where a person can mark a full disk
            as fine is a panel that will one day say a full disk is fine. */}
        <p className="text-xs text-slate-500">
          Acknowledging says you have seen it. Alerts clear on their own when the condition
          does — there is no way to mark one as fine by hand.
        </p>
      </CardBody>
    </Card>
  );
}

/** Services is what each watched service is doing, and since when. */
function Services({ overview }: { overview: MonitoringOverview }) {
  const services = overview.services ?? [];
  if (services.length === 0) {
    return null;
  }

  return (
    <Card label="Services">
      <CardHeader
        title="Services"
        description="What each one is doing, and how long it has been doing it."
        icon={<TintedIcon icon={<ServerCog className="h-5 w-5" />} tone="neutral" />}
      />
      <CardBody>
        <ul className="grid gap-2 sm:grid-cols-2">
          {services.map((service) => (
            <li
              key={service.service}
              className="flex items-center justify-between rounded border border-slate-200 px-3 py-2 text-sm"
            >
              <span className="font-medium text-slate-800">{service.service}</span>
              <span
                className={
                  service.running
                    ? 'text-xs text-emerald-700'
                    : service.status === 'not installed'
                      ? 'text-xs text-slate-400'
                      : 'text-xs text-rose-700'
                }
              >
                {service.status} for {formatSeconds(service.for_seconds)}
              </span>
            </li>
          ))}
        </ul>
      </CardBody>
    </Card>
  );
}

/** Rules is what the panel watches for. */
function Rules({ overview }: { overview: MonitoringOverview }) {
  const { data } = useAlertRules();
  const remove = useDeleteAlertRule();
  const update = useUpdateAlertRule();
  const [editing, setEditing] = useState<AlertRule | null>(null);
  const [adding, setAdding] = useState(false);
  const [pending, setPending] = useState<AlertRule | null>(null);

  const rules = data?.rules ?? overview.rules ?? [];
  const metrics = data?.metrics ?? [];

  return (
    <Card label="Alert rules">
      <CardHeader
        title="Alert rules"
        description="What counts as a problem on this host."
        icon={<TintedIcon icon={<Activity className="h-5 w-5" />} tone="neutral" />}
        action={
          <RequirePermission permission={Permission.MonitorManage}>
            <Button
              onClick={() => setAdding(true)}
              icon={<Plus aria-hidden="true" className="h-4 w-4" />}
            >
              Add rule
            </Button>
          </RequirePermission>
        }
      />
      {rules.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<Activity className="h-6 w-6" />}
            title="Nothing is being watched"
            description="Add a rule to have the panel tell you when something goes wrong."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-slate-100">
          {rules.map((rule) => (
            <li key={rule.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <button
                type="button"
                onClick={() => setEditing(rule)}
                className="flex-1 text-left"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <SeverityPill severity={rule.severity} />
                  <span className="font-medium text-slate-900">{rule.name}</span>
                  {!rule.enabled && (
                    <span className="rounded bg-slate-100 px-1.5 py-0.5 text-xs text-slate-500">
                      off
                    </span>
                  )}
                </div>
                <div className="mt-0.5 text-xs text-slate-500">
                  {describeRule(rule)}
                </div>
              </button>
              <RequirePermission permission={Permission.MonitorManage}>
                <span className="inline-flex items-center gap-2">
                  <Toggle
                    id={`rule-${rule.id}`}
                    label="Enabled"
                    checked={rule.enabled}
                    onChange={(enabled) =>
                      update.mutate({ id: rule.id, input: { enabled } })
                    }
                  />
                  <Button
                    variant="ghost"
                    aria-label={`Delete ${rule.name}`}
                    onClick={() => setPending(rule)}
                    icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                  />
                </span>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      {(adding || editing) && (
        <RuleDialog
          rule={editing}
          metrics={metrics}
          onClose={() => {
            setAdding(false);
            setEditing(null);
          }}
        />
      )}

      <ConfirmDialog
        open={Boolean(pending)}
        onClose={() => setPending(null)}
        onConfirm={() => {
          if (!pending) return;
          remove.mutate(pending.id, { onSuccess: () => setPending(null) });
        }}
        title={`Stop watching with "${pending?.name ?? ''}"?`}
        description="Nothing will be checked against this rule any more."
        confirmLabel="Delete rule"
        destructive
        loading={remove.isPending}
        error={remove.error instanceof ApiError ? remove.error.message : null}
      >
        <p className="text-sm text-slate-600">
          The alerts it has already opened stay in the history — deleting a rule does not erase
          what it caught.
        </p>
      </ConfirmDialog>
    </Card>
  );
}

/** describeRule renders a rule as a sentence. */
function describeRule(rule: AlertRule): string {
  const unit = ['cpu', 'memory', 'disk', 'swap'].includes(rule.metric) ? '%' : '';
  const target = rule.target ? ` on ${rule.target}` : '';
  const held =
    rule.for_seconds > 0
      ? ` for ${formatSeconds(rule.for_seconds)}`
      : ' on the first reading';
  return `${rule.metric}${target} ${rule.comparison} ${formatNumber(rule.threshold)}${unit}${held}`;
}

/** RuleDialog adds or changes a rule. */
function RuleDialog({
  rule,
  metrics,
  onClose,
}: {
  rule: AlertRule | null;
  metrics: AlertMetric[];
  onClose: () => void;
}) {
  const create = useCreateAlertRule();
  const update = useUpdateAlertRule();

  // Every field is present rather than optional: this is a form, and a form
  // whose fields can be undefined is one whose payload has holes in it.
  const [form, setForm] = useState<Required<AlertRuleInput>>({
    name: rule?.name ?? '',
    metric: rule?.metric ?? 'disk',
    target: rule?.target ?? '',
    comparison: rule?.comparison ?? 'above',
    threshold: rule?.threshold ?? 80,
    for_seconds: rule?.for_seconds ?? 300,
    severity: rule?.severity ?? 'warning',
    enabled: rule?.enabled ?? true,
  });

  const busy = create.isPending || update.isPending;
  const failure =
    create.error instanceof ApiError
      ? create.error.message
      : update.error instanceof ApiError
        ? update.error.message
        : null;

  const submit = () => {
    if (rule) {
      // The metric and target are fixed for the life of a rule, so they are not
      // sent: a rule that watched something else would be a different rule, and
      // the alerts it had already opened would describe a condition it never
      // observed.
      update.mutate(
        {
          id: rule.id,
          input: {
            name: form.name,
            comparison: form.comparison,
            threshold: form.threshold,
            for_seconds: form.for_seconds,
            severity: form.severity,
            enabled: form.enabled,
          },
        },
        { onSuccess: onClose },
      );
    } else {
      create.mutate(form, { onSuccess: onClose });
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      busy={busy}
      title={rule ? 'Edit rule' : 'Add rule'}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy}>
            Save rule
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {failure && (
          <AlertBanner tone="danger" title="The rule was refused">
            {failure}
          </AlertBanner>
        )}

        <TextField
          id="rule-name"
          label="Name"
          hint="What an alert from this rule will be called."
          value={form.name}
          onChange={(event) => setForm({ ...form, name: event.target.value })}
        />

        <div className="grid gap-4 sm:grid-cols-2">
          <SelectField
            id="rule-metric"
            label="Watch"
            hint={rule ? 'A rule cannot be pointed at something else.' : undefined}
            value={form.metric}
            disabled={Boolean(rule)}
            onChange={(event) =>
              setForm({ ...form, metric: event.target.value as AlertMetric })
            }
          >
            {(metrics.length > 0
              ? metrics
              : (['cpu', 'memory', 'disk', 'swap', 'load', 'service'] as AlertMetric[])
            ).map((metric) => (
              <option key={metric} value={metric}>
                {metric}
              </option>
            ))}
          </SelectField>
          <TextField
            id="rule-target"
            label="Which one"
            hint={
              form.metric === 'service'
                ? 'The service to watch.'
                : 'A mount point, or empty for every one.'
            }
            value={form.target}
            disabled={Boolean(rule)}
            onChange={(event) => setForm({ ...form, target: event.target.value })}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <SelectField
            id="rule-comparison"
            label="When it is"
            value={form.comparison}
            onChange={(event) =>
              setForm({ ...form, comparison: event.target.value as 'above' | 'below' })
            }
          >
            <option value="above">above</option>
            <option value="below">below</option>
          </SelectField>
          <TextField
            id="rule-threshold"
            label="Threshold"
            type="number"
            value={String(form.threshold)}
            onChange={(event) => setForm({ ...form, threshold: Number(event.target.value) })}
          />
        </div>

        <TextField
          id="rule-for"
          label="For at least"
          type="number"
          hint="Seconds. This is what stops a one-off spike becoming an alert somebody mutes. Zero fires on the first reading, which suits a service being down."
          value={String(form.for_seconds)}
          onChange={(event) => setForm({ ...form, for_seconds: Number(event.target.value) })}
        />

        <SelectField
          id="rule-severity"
          label="Severity"
          value={form.severity}
          onChange={(event) =>
            setForm({ ...form, severity: event.target.value as 'warning' | 'critical' })
          }
        >
          <option value="warning">warning</option>
          <option value="critical">critical</option>
        </SelectField>
      </div>
    </Modal>
  );
}

/** RecentAlerts is what has been wrong, including what has cleared. */
function RecentAlerts({ overview }: { overview: MonitoringOverview }) {
  const recent = overview.recent ?? [];
  const resolved = recent.filter((alert) => alert.status === 'resolved');

  return (
    <Card label="History">
      <CardHeader
        title="History"
        description="Including what has already cleared, so a host that keeps flapping is visible."
        icon={<TintedIcon icon={<Clock className="h-5 w-5" />} tone="neutral" />}
      />
      <CardBody>
        {resolved.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 className="h-6 w-6" />}
            title="Nothing has cleared yet"
            description="Alerts that resolve are kept here with how long they lasted."
          />
        ) : (
          <ul className="space-y-2 text-sm">
            {resolved.map((alert) => (
              <li key={alert.id} className="flex flex-wrap items-baseline gap-2">
                <SeverityPill severity={alert.severity} />
                <span className="text-slate-700">{alert.message}</span>
                <span className="text-xs text-slate-500">{describeIncident(alert)}</span>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

/** describeIncident says how long an alert lasted and how bad it got. */
function describeIncident(alert: Alert): string {
  const parts: string[] = [];
  if (alert.resolved_at) {
    const lasted =
      (new Date(alert.resolved_at).getTime() - new Date(alert.opened_at).getTime()) / 1000;
    parts.push(`lasted ${formatSeconds(Math.max(0, Math.round(lasted)))}`);
  }
  if (alert.worst != null) {
    parts.push(`worst ${formatNumber(alert.worst)}`);
  }
  return parts.join(' · ');
}

function SeverityPill({ severity }: { severity: 'warning' | 'critical' }) {
  return (
    <span
      className={
        severity === 'critical'
          ? 'rounded bg-rose-50 px-1.5 py-0.5 text-xs font-medium text-rose-700'
          : 'rounded bg-amber-50 px-1.5 py-0.5 text-xs font-medium text-amber-700'
      }
    >
      {severity}
    </span>
  );
}

/** formatSeconds renders a span the way somebody would say it. */
function formatSeconds(seconds: number): string {
  if (seconds < 60) {
    return `${seconds}s`;
  }
  if (seconds < 3600) {
    return `${Math.round(seconds / 60)} min`;
  }
  if (seconds < 172800) {
    const hours = Math.round(seconds / 3600);
    return hours === 1 ? '1 hour' : `${hours} hours`;
  }
  return `${Math.round(seconds / 86400)} days`;
}

function formatNumber(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

function formatWhen(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? 'an unknown time' : parsed.toLocaleString();
}
