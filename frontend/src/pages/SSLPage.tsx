import { Link } from 'react-router-dom';
import { Lock, RefreshCw, ShieldAlert } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardHeader, TintedIcon } from '@/components/ui/Card';
import { EmptyState, ProgressBar, SkeletonRows } from '@/components/ui/Loading';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useCertificates, useRenewCertificate } from '@/features/ssl/hooks';
import {
  certificateStatusPill,
  expiryLabel,
  expiryTone,
  providerLabel,
} from '@/features/ssl/status';
import type { SSLCertificate } from '@/types/api';

/** SSLPage lists every certificate, soonest to expire first. */
export function SSLPage() {
  const { data, isPending, isError, error } = useCertificates();

  const certificates = data?.certificates ?? [];
  const needingAttention = data?.needing_attention ?? 0;

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">SSL certificates</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Certificates for the sites on this server. Renewal is automatic; what is listed
          first is what runs out first.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The certificates could not be loaded">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {needingAttention > 0 && (
        <Alert tone="warning" title="Certificates need attention">
          {needingAttention === 1
            ? 'One certificate is expiring or has expired.'
            : `${needingAttention} certificates are expiring or have expired.`}{' '}
          An expired certificate shows every visitor a security warning.
        </Alert>
      )}

      <Card>
        <CardHeader
          title="Certificates"
          description={
            certificates.length > 0
              ? `${certificates.length} across this server`
              : undefined
          }
          icon={<TintedIcon tone="brand" icon={<Lock className="h-4 w-4" />} />}
        />

        {isPending && <SkeletonRows rows={3} />}

        {!isPending && certificates.length === 0 && !isError && (
          <EmptyState
            icon={<Lock className="h-6 w-6" />}
            title="No certificates yet"
            description="Issue one from a website's page to serve it over HTTPS."
          />
        )}

        {certificates.length > 0 && (
          <ul className="divide-y divide-surface-border">
            {certificates.map((certificate) => (
              <CertificateRow key={certificate.id} certificate={certificate} />
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

/** CertificateRow is one certificate and what can be done with it. */
function CertificateRow({ certificate }: { certificate: SSLCertificate }) {
  const renew = useRenewCertificate();
  const pill = certificateStatusPill(certificate.status);
  const settling = certificate.status === 'issuing' || certificate.status === 'pending';
  const tone = expiryTone(certificate.days_remaining);

  return (
    <li className="px-5 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <TintedIcon
            tone={tone === 'error' ? 'danger' : tone === 'warn' ? 'warn' : 'ok'}
            icon={
              tone === 'error' ? (
                <ShieldAlert className="h-4 w-4" />
              ) : (
                <Lock className="h-4 w-4" />
              )
            }
          />
          <div className="min-w-0">
            <Link
              to={`/websites/${certificate.website_id}`}
              className={`truncate rounded-sm text-sm font-semibold text-ink-strong hover:text-brand-700 ${focusRingTight}`}
            >
              {certificate.primary_domain}
            </Link>
            <p className="mt-0.5 truncate text-xs text-ink-muted">
              {providerLabel(certificate.provider)}
              {certificate.domains.length > 1 && ` · ${certificate.domains.length} names`}
              {!certificate.auto_renew && ' · auto-renewal off'}
            </p>
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-3">
          <span
            className={`text-xs font-medium ${
              tone === 'error'
                ? 'text-danger-600'
                : tone === 'warn'
                  ? 'text-warn-700'
                  : 'text-ink-muted'
            }`}
          >
            {expiryLabel(certificate.days_remaining)}
          </span>
          <StatusPill label={pill.label} tone={pill.tone} dot pulse={settling} />

          <RequirePermission permission={Permission.SSLManage}>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => renew.mutate(certificate.website_id)}
              loading={renew.isPending}
              disabled={settling}
              icon={<RefreshCw aria-hidden="true" className="h-3.5 w-3.5" />}
            >
              Renew
            </Button>
          </RequirePermission>
        </div>
      </div>

      {settling && (
        <ProgressBar
          label={`Issuing a certificate for ${certificate.primary_domain}`}
          className="mt-2.5"
        />
      )}

      {certificate.last_error && (
        <p className="mt-2 text-xs text-danger-600">{certificate.last_error}</p>
      )}
    </li>
  );
}
