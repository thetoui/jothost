import { useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Eye,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Undo2,
} from 'lucide-react';

import { Alert as AlertBanner } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { TextField } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useAcceptFinding,
  useReopenFinding,
  useRunScan,
  useSecurityOverview,
} from '@/features/security/hooks';
import { ApiError } from '@/services/apiClient';
import type {
  FindingSeverity,
  ScannerOutcome,
  SecurityFinding,
  SecurityOverview,
} from '@/types/api';

/**
 * SecurityCenterPage says how exposed this host is, and what to do first.
 *
 * The rule the page is built around is that a check which did not run is not a
 * check that passed. The score never appears without the number of checks
 * behind it, and a partial scan says outright that it is incomplete rather than
 * reassuring — because the misreading is always in the reassuring direction.
 */
export function SecurityCenterPage() {
  const { data, isPending, isError, error } = useSecurityOverview();

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">Security Center</h1>
        <p className="mt-1 text-sm text-ink-muted">
          What is exposed on this host, how it was found, and what to do about it.
        </p>
      </header>

      {isError && (
        <AlertBanner tone="danger" title="The security posture could not be read">
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
          <ScoreCard overview={data} />
          <Findings overview={data} />
          <AcceptedRisks overview={data} />
        </>
      )}
    </div>
  );
}

/** ScoreCard shows the number, and never without what it is built from. */
function ScoreCard({ overview }: { overview: SecurityOverview }) {
  const scan = useRunScan();
  const { score, counts, last_scan: lastScan } = overview;
  const scanners = overview.scanners ?? [];
  const neverScanned = lastScan === null;

  return (
    <Card>
      <CardHeader
        icon={
          <TintedIcon
            tone={toneForGrade(score.grade)}
            icon={
              score.grade === 'good' ? (
                <ShieldCheck className="h-4 w-4" />
              ) : (
                <ShieldAlert className="h-4 w-4" />
              )
            }
          />
        }
        title="Security score"
        description={
          neverScanned
            ? 'This host has not been scanned yet.'
            : `Last scanned ${formatWhen(lastScan.created_at)}.`
        }
        action={
          <RequirePermission permission={Permission.SecurityView}>
            <Button
              onClick={() => scan.mutate()}
              loading={scan.isPending}
              icon={<RefreshCw aria-hidden="true" className="h-4 w-4" />}
            >
              Run a scan
            </Button>
          </RequirePermission>
        }
      />
      <CardBody>
        {scan.isError && (
          <AlertBanner tone="danger" title="The scan did not run">
            {scan.error instanceof ApiError ? scan.error.message : 'Try again in a moment.'}
          </AlertBanner>
        )}

        {neverScanned ? (
          <EmptyState
            icon={<ShieldAlert className="h-6 w-6" />}
            title="Nothing has been checked"
            description="A host nobody has looked at is not a host with no problems. Run a scan to find out which it is."
          />
        ) : (
          <>
            <div className="flex flex-wrap items-baseline gap-3">
              <span className="text-4xl font-semibold text-ink-strong">{score.value}</span>
              <span className="text-sm font-medium uppercase tracking-wide text-ink-muted">
                {score.grade}
              </span>
              {/* The checks behind the number, always. A 100 from two checks is
                  not a 100, and this is the one place that can say so. */}
              <span className="text-xs text-ink-muted">
                from {score.checks_run} of {score.checks_total} checks
              </span>
            </div>

            {!score.complete ? (
              <AlertBanner tone="warning" title="This score is incomplete">
                {score.summary}
              </AlertBanner>
            ) : (
              <p className="mt-2 text-sm text-ink">{score.summary}</p>
            )}

            <dl className="mt-4 grid gap-4 sm:grid-cols-5">
              <Stat label="Critical" value={counts.critical} tone="danger" />
              <Stat label="High" value={counts.high} tone="warn" />
              <Stat label="Medium" value={counts.medium} />
              <Stat label="Low" value={counts.low} />
              <Stat label="Accepted" value={counts.accepted} />
            </dl>

            <ScannerList scanners={scanners} />
          </>
        )}
      </CardBody>
    </Card>
  );
}

function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: 'danger' | 'warn';
}) {
  const colour =
    value === 0
      ? 'text-ink-dim'
      : tone === 'danger'
        ? 'text-danger-700'
        : tone === 'warn'
          ? 'text-warn-700'
          : 'text-ink-strong';
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-ink-muted">{label}</dt>
      <dd className={`mt-1 text-lg font-semibold ${colour}`}>{value}</dd>
    </div>
  );
}

/** ScannerList says which checks ran and which could not, and why. */
function ScannerList({ scanners }: { scanners: ScannerOutcome[] }) {
  if (scanners.length === 0) return null;

  return (
    <div className="mt-5 border-t border-line pt-4">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-ink-muted">
        Checks
      </h3>
      <ul className="mt-2 space-y-1.5">
        {scanners.map((outcome) => (
          <li key={outcome.scanner} className="flex items-start gap-2 text-xs">
            {outcome.ran ? (
              <CheckCircle2
                aria-hidden="true"
                className="mt-0.5 h-3.5 w-3.5 shrink-0 text-ok-600"
              />
            ) : (
              <AlertTriangle
                aria-hidden="true"
                className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn-600"
              />
            )}
            <span className="font-medium text-ink">{outcome.scanner}</span>
            <span className="text-ink-muted">
              {outcome.ran
                ? `${outcome.findings} finding(s)`
                : `could not be run — ${outcome.reason ?? 'no reason was given'}`}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Findings lists what is outstanding, worst first. */
function Findings({ overview }: { overview: SecurityOverview }) {
  const findings = overview.findings ?? [];
  const [accepting, setAccepting] = useState<SecurityFinding | null>(null);

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="neutral" icon={<ShieldAlert className="h-4 w-4" />} />}
        title="Findings"
        description="Outstanding, worst first."
      />

      {findings.length === 0 ? (
        <CardBody>
          <EmptyState
            icon={<ShieldCheck className="h-6 w-6" />}
            title="Nothing outstanding"
            description="Every check that ran found nothing to report."
          />
        </CardBody>
      ) : (
        <ul className="divide-y divide-line">
          {findings.map((finding) => (
            <li key={finding.id} className="flex items-start gap-4 px-5 py-4">
              <SeverityBadge severity={finding.severity} />
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-ink-strong">{finding.title}</p>
                <p className="mt-0.5 text-xs text-ink">{finding.description}</p>
                {finding.remediation && (
                  <p className="mt-1.5 text-xs text-ink">
                    <span className="font-medium">What to do: </span>
                    {finding.remediation}
                  </p>
                )}
                <p className="mt-1 text-xs text-ink-dim">
                  Found by the {finding.scanner} check · first seen{' '}
                  {formatWhen(finding.first_seen_at)}
                </p>
              </div>
              <RequirePermission permission={Permission.SecurityView}>
                <Button
                  variant="secondary"
                  onClick={() => setAccepting(finding)}
                  icon={<Eye aria-hidden="true" className="h-4 w-4" />}
                >
                  Accept
                </Button>
              </RequirePermission>
            </li>
          ))}
        </ul>
      )}

      <CardBody>
        {/* Said out loud, where somebody is about to look for the button. */}
        <p className="text-xs text-ink-muted">
          There is no way to mark a finding as fixed. One disappears when a scan no longer
          finds it — whether a weakness still exists is the scanner&rsquo;s to decide.
          Accepting records that a risk is known and deliberate, and keeps it in view.
        </p>
      </CardBody>

      {accepting && (
        <AcceptDialog finding={accepting} onClose={() => setAccepting(null)} />
      )}
    </Card>
  );
}

/** AcceptDialog asks why, and will not proceed without an answer. */
function AcceptDialog({
  finding,
  onClose,
}: {
  finding: SecurityFinding;
  onClose: () => void;
}) {
  const accept = useAcceptFinding();
  const [reason, setReason] = useState('');

  return (
    <Modal open title="Accept this risk" onClose={onClose}>
      <div className="space-y-4">
        {accept.isError && (
          <AlertBanner tone="danger" title="That was not accepted">
            {accept.error instanceof ApiError
              ? accept.error.message
              : 'Try again in a moment.'}
          </AlertBanner>
        )}

        <div>
          <p className="text-sm font-medium text-ink-strong">{finding.title}</p>
          <p className="mt-1 text-xs text-ink">{finding.description}</p>
        </div>

        <TextField
          id="accept-reason"
          label="Why is this acceptable?"
          hint="Somebody will read this in a year and need to know it was a decision rather than an oversight."
          value={reason}
          onChange={(event) => setReason(event.target.value)}
        />

        {/* The two properties that make accepting safe to offer at all. */}
        <p className="text-xs text-ink-muted">
          It stays on this page under accepted risks, and it comes back on its own if the
          scan ever finds it worse than it is now.
        </p>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            onClick={() =>
              accept.mutate({ id: finding.id, reason }, { onSuccess: onClose })
            }
            loading={accept.isPending}
          >
            Accept the risk
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/** AcceptedRisks keeps accepted findings visible forever. */
function AcceptedRisks({ overview }: { overview: SecurityOverview }) {
  const accepted = overview.accepted ?? [];
  const reopen = useReopenFinding();

  if (accepted.length === 0) return null;

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="neutral" icon={<Eye className="h-4 w-4" />} />}
        title="Accepted risks"
        description="Known, deliberate, and still true."
      />
      <ul className="divide-y divide-line">
        {accepted.map((finding) => (
          <li key={finding.id} className="flex items-start gap-4 px-5 py-4">
            <SeverityBadge severity={finding.severity} />
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium text-ink-strong">{finding.title}</p>
              <p className="mt-1 text-xs text-ink">
                <span className="font-medium">Accepted: </span>
                {finding.accepted_reason ?? 'no reason was recorded'}
              </p>
              <p className="mt-1 text-xs text-ink-dim">
                {finding.accepted_at ? formatWhen(finding.accepted_at) : ''}
              </p>
            </div>
            <RequirePermission permission={Permission.SecurityView}>
              <Button
                variant="ghost"
                onClick={() => reopen.mutate(finding.id)}
                loading={reopen.isPending && reopen.variables === finding.id}
                icon={<Undo2 aria-hidden="true" className="h-4 w-4" />}
              >
                Reopen
              </Button>
            </RequirePermission>
          </li>
        ))}
      </ul>
      <CardBody>
        <p className="text-xs text-ink-muted">
          Accepted risks are never hidden. A score carried by accepted risk is a different
          thing from a clean one.
        </p>
      </CardBody>
    </Card>
  );
}

function SeverityBadge({ severity }: { severity: FindingSeverity }) {
  const styles: Record<FindingSeverity, string> = {
    critical: 'bg-danger-100 text-danger-800',
    high: 'bg-warn-100 text-warn-800',
    medium: 'bg-info-100 text-info-800',
    low: 'bg-surface-sunken text-ink',
    info: 'bg-surface-sunken text-ink-muted',
  };
  return (
    <span
      className={`mt-0.5 shrink-0 rounded px-2 py-0.5 text-xs font-medium uppercase ${styles[severity]}`}
    >
      {severity}
    </span>
  );
}

function toneForGrade(grade: string): 'brand' | 'ok' | 'warn' | 'danger' | 'neutral' {
  switch (grade) {
    case 'good':
      return 'ok';
    case 'fair':
      return 'warn';
    case 'poor':
    case 'at risk':
      return 'danger';
    default:
      return 'neutral';
  }
}

function formatWhen(value: string): string {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return value;
  return at.toLocaleString();
}
