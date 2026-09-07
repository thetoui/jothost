import { useState, type FormEvent } from 'react';
import { Lock, RefreshCw, ShieldCheck, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { Skeleton } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useConfigureSSL,
  useIssueCertificate,
  useRenewCertificate,
  useRevokeCertificate,
  useSSLProviders,
  useWebsiteSSL,
} from '@/features/ssl/hooks';
import {
  certificateStatusPill,
  expiryLabel,
  expiryTone,
  providerLabel,
} from '@/features/ssl/status';
import { ApiError } from '@/services/apiClient';
import type { DNSAlignment, SSLCertificate, SSLProvider } from '@/types/api';

interface WebsiteSSLPanelProps {
  websiteId: string;
  domain: string;
}

/** WebsiteSSLPanel issues and manages a website's certificate. */
export function WebsiteSSLPanel({ websiteId, domain }: WebsiteSSLPanelProps) {
  const { data: state, isPending } = useWebsiteSSL(websiteId);

  return (
    <Card>
      <CardHeader
        title="HTTPS"
        description={
          state?.enabled
            ? `Served over HTTPS with a ${providerLabel(state.certificate!.provider).toLowerCase()} certificate`
            : 'This site is served over plain HTTP'
        }
        icon={
          <TintedIcon
            tone={state?.enabled ? 'ok' : 'neutral'}
            icon={state?.enabled ? <ShieldCheck className="h-4 w-4" /> : <Lock className="h-4 w-4" />}
          />
        }
      />

      <CardBody>
        {isPending ? (
          <div className="space-y-3">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="h-9 w-56" />
          </div>
        ) : state?.enabled && state.certificate ? (
          <CertificateDetail
            websiteId={websiteId}
            domain={domain}
            certificate={state.certificate}
          />
        ) : (
          <IssueForm websiteId={websiteId} domain={domain} />
        )}
      </CardBody>
    </Card>
  );
}

/** IssueForm obtains a first certificate for a site. */
function IssueForm({ websiteId, domain }: { websiteId: string; domain: string }) {
  const { data: providers } = useSSLProviders();
  const issue = useIssueCertificate();

  const [provider, setProvider] = useState<SSLProvider>('selfsigned');
  const [email, setEmail] = useState('');
  const [staging, setStaging] = useState(false);
  const [redirect, setRedirect] = useState(true);

  const letsEncryptAvailable = providers?.letsencrypt ?? false;

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    issue.mutate({
      websiteId,
      provider,
      ...(provider === 'letsencrypt' && email.trim() ? { email: email.trim() } : {}),
      ...(provider === 'letsencrypt' ? { staging } : {}),
      https_redirect: redirect,
      auto_renew: true,
    });
  }

  const error =
    issue.error instanceof ApiError
      ? issue.error.message
      : issue.error
        ? 'The certificate could not be requested.'
        : null;

  return (
    <RequirePermission
      permission={Permission.SSLManage}
      fallback={
        <p className="text-sm text-slate-500">
          This website is served over plain HTTP. Issuing a certificate needs the SSL
          management permission.
        </p>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <SelectField
            id="ssl-provider"
            label="Certificate type"
            value={provider}
            onChange={(event) => setProvider(event.target.value as SSLProvider)}
            hint={
              provider === 'letsencrypt'
                ? 'Publicly trusted. The domain must resolve to this server.'
                : 'Encrypts traffic, but browsers will warn. For internal hosts.'
            }
          >
            <option value="selfsigned">Self-signed</option>
            <option value="letsencrypt" disabled={!letsEncryptAvailable}>
              Let&apos;s Encrypt{letsEncryptAvailable ? '' : ' — certbot not installed'}
            </option>
          </SelectField>

          {provider === 'letsencrypt' && (
            <TextField
              id="ssl-email"
              label="Contact email"
              suffix="optional"
              type="email"
              autoComplete="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              hint="Receives expiry warnings from the certificate authority."
            />
          )}
        </div>

        {provider === 'letsencrypt' && (
          <>
            <Alert tone="info">
              <span className="font-medium">{domain}</span> must resolve to this server over
              the public internet. The authority fetches a challenge over plain HTTP before
              issuing. Where this host serves the zone, the panel adds the address record
              first; where the DNS is somewhere else, it has to be right already.
            </Alert>
            <Toggle
              id="ssl-staging"
              label="Use the staging environment"
              description="Issues an untrusted certificate without consuming the strict production rate limits. Useful while setting a site up."
              checked={staging}
              onChange={setStaging}
            />
          </>
        )}

        <Toggle
          id="ssl-redirect"
          label="Redirect HTTP to HTTPS"
          description="Sends every plain request to the secure site. The certificate renewal path stays reachable."
          checked={redirect}
          onChange={setRedirect}
        />

        {error && <Alert tone="danger">{error}</Alert>}

        {issue.data?.dns && <DNSReport alignment={issue.data.dns} />}

        <Button
          type="submit"
          variant="primary"
          loading={issue.isPending}
          icon={<Lock aria-hidden="true" className="h-4 w-4" />}
        >
          {issue.isPending ? 'Requesting…' : 'Enable HTTPS'}
        </Button>
      </form>
    </RequirePermission>
  );
}

/**
 * DNSReport says what issuance did to this host's zones, and what it did not.
 *
 * Shown after the request rather than before it because until the names are
 * known there is nothing to say. A name pointing at another machine is the
 * usual reason issuance fails, and it fails several minutes later with a
 * message from certbot — by which time nobody is looking at this form.
 */
function DNSReport({ alignment }: { alignment: DNSAlignment }) {
  const names = alignment.names ?? [];
  if (names.length === 0) {
    return null;
  }

  const blocked = names.filter((name) => name.status === 'elsewhere' || name.status === 'no_address');
  const added = names.filter((name) => name.status === 'added');

  return (
    <Alert tone={blocked.length > 0 ? 'warning' : 'info'} title="DNS">
      {added.length > 0 && (
        <p>
          Added an address record for {added.map((name) => name.name).join(', ')}
          {alignment.address ? `, pointing at ${alignment.address}` : ''}.
        </p>
      )}
      {blocked.length > 0 && (
        <ul className="mt-1 space-y-1">
          {blocked.map((name) => (
            <li key={name.name}>
              <span className="font-medium">{name.name}</span> {name.detail}
            </li>
          ))}
        </ul>
      )}
      {added.length === 0 && blocked.length === 0 && (
        <p>Every name on the certificate already resolves here.</p>
      )}
    </Alert>
  );
}

/** CertificateDetail shows a live certificate and manages it. */
function CertificateDetail({
  websiteId,
  domain,
  certificate,
}: {
  websiteId: string;
  domain: string;
  certificate: SSLCertificate;
}) {
  const renew = useRenewCertificate();
  const revoke = useRevokeCertificate();
  const configure = useConfigureSSL();
  const [confirmingRevoke, setConfirmingRevoke] = useState(false);

  const pill = certificateStatusPill(certificate.status);
  const tone = expiryTone(certificate.days_remaining);
  const settling = certificate.status === 'issuing' || certificate.status === 'pending';

  const revokeError =
    revoke.error instanceof ApiError
      ? revoke.error.message
      : revoke.error
        ? 'The certificate could not be revoked.'
        : null;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <StatusPill label={pill.label} tone={pill.tone} dot pulse={settling} />
        <span
          className={`text-sm font-medium ${
            tone === 'error'
              ? 'text-danger-600'
              : tone === 'warn'
                ? 'text-warn-700'
                : 'text-slate-600'
          }`}
        >
          {expiryLabel(certificate.days_remaining)}
        </span>
      </div>

      {certificate.status === 'expired' && (
        <Alert tone="danger" title="This certificate has expired">
          Every visitor is being shown a security warning. Renew it to restore the site.
        </Alert>
      )}

      {certificate.last_error && <Alert tone="danger">{certificate.last_error}</Alert>}

      <dl className="grid gap-4 sm:grid-cols-2">
        <Detail label="Issuer" value={certificate.issuer ?? 'Unknown'} />
        <Detail
          label="Expires"
          value={
            certificate.expires_at
              ? new Date(certificate.expires_at).toLocaleDateString()
              : 'Unknown'
          }
        />
        <Detail label="Covers" value={certificate.domains.join(', ')} />
        <Detail label="Fingerprint" value={certificate.fingerprint ?? 'Unknown'} mono truncate />
      </dl>

      <RequirePermission permission={Permission.SSLManage}>
        <div className="space-y-4 border-t border-surface-border pt-4">
          <Toggle
            id="ssl-auto-renew"
            label="Renew automatically"
            description="Renews within 30 days of expiry. Turning this off means renewing by hand before it lapses."
            checked={certificate.auto_renew}
            disabled={configure.isPending}
            onChange={(value) => configure.mutate({ websiteId, auto_renew: value })}
          />

          <div className="flex flex-wrap gap-2">
            <Button
              variant="secondary"
              onClick={() => renew.mutate(websiteId)}
              loading={renew.isPending}
              disabled={settling}
              icon={<RefreshCw aria-hidden="true" className="h-4 w-4" />}
            >
              Renew now
            </Button>
            <Button
              variant="danger"
              onClick={() => setConfirmingRevoke(true)}
              disabled={settling}
              icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
            >
              Revoke
            </Button>
          </div>
        </div>
      </RequirePermission>

      <ConfirmDialog
        open={confirmingRevoke}
        onClose={() => setConfirmingRevoke(false)}
        onConfirm={() =>
          revoke.mutate(websiteId, { onSuccess: () => setConfirmingRevoke(false) })
        }
        title="Revoke this certificate?"
        description="The site returns to plain HTTP."
        confirmLabel="Revoke certificate"
        destructive
        loading={revoke.isPending}
        error={revokeError}
      >
        <p className="mb-2">
          <span className="font-medium text-slate-900">{domain}</span> stops serving HTTPS,
          and the certificate is withdrawn.
        </p>
        <p className="text-slate-600">
          Revocation cannot be undone: the certificate is refused by every client that
          checks, and restoring HTTPS means issuing a new one.
        </p>
      </ConfirmDialog>
    </div>
  );
}

interface DetailProps {
  label: string;
  value: string;
  mono?: boolean;
  truncate?: boolean;
}

function Detail({ label, value, mono, truncate }: DetailProps) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium uppercase tracking-wide text-slate-400">{label}</dt>
      <dd
        className={`mt-1 text-sm text-slate-900 ${mono ? 'font-mono text-xs' : ''} ${
          truncate ? 'truncate' : 'break-words'
        }`}
        title={truncate ? value : undefined}
      >
        {value}
      </dd>
    </div>
  );
}
