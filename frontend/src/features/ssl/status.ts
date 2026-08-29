import type { SSLProvider, SSLStatus } from '@/types/api';

type Tone = 'ok' | 'warn' | 'error' | 'neutral' | 'info';

const statusTones: Record<SSLStatus, { label: string; tone: Tone }> = {
  pending: { label: 'Pending', tone: 'neutral' },
  issuing: { label: 'Issuing', tone: 'warn' },
  valid: { label: 'Valid', tone: 'ok' },
  expiring: { label: 'Expiring', tone: 'warn' },
  // An expired certificate shows every visitor a security warning, so it is an
  // error rather than a caution.
  expired: { label: 'Expired', tone: 'error' },
  revoked: { label: 'Revoked', tone: 'neutral' },
  failed: { label: 'Failed', tone: 'error' },
};

export function certificateStatusPill(status: SSLStatus): { label: string; tone: Tone } {
  return statusTones[status] ?? { label: status, tone: 'neutral' };
}

const providerLabels: Record<SSLProvider, string> = {
  letsencrypt: "Let's Encrypt",
  selfsigned: 'Self-signed',
};

export function providerLabel(provider: SSLProvider): string {
  return providerLabels[provider] ?? provider;
}

/**
 * expiryLabel renders how long a certificate has left.
 *
 * Past expiry it counts up rather than showing "0 days", because "expired 3
 * days ago" tells an operator how long the site has been broken.
 */
export function expiryLabel(daysRemaining: number | null): string {
  if (daysRemaining === null) {
    return 'Unknown';
  }
  if (daysRemaining < 0) {
    const days = Math.abs(daysRemaining);
    return `Expired ${days === 1 ? '1 day' : `${days} days`} ago`;
  }
  if (daysRemaining === 0) {
    return 'Expires today';
  }
  return daysRemaining === 1 ? '1 day left' : `${daysRemaining} days left`;
}

/** Days at which the panel starts warning, matching the API's own threshold. */
export const EXPIRING_WITHIN_DAYS = 21;

/** expiryTone colours the remaining time the same way the status pill does. */
export function expiryTone(daysRemaining: number | null): Tone {
  if (daysRemaining === null) {
    return 'neutral';
  }
  if (daysRemaining < 0) {
    return 'error';
  }
  if (daysRemaining <= 7) {
    return 'error';
  }
  if (daysRemaining <= EXPIRING_WITHIN_DAYS) {
    return 'warn';
  }
  return 'ok';
}
