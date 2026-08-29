import type { JobStatus, WebsiteStatus } from '@/types/api';

type Tone = 'ok' | 'warn' | 'error' | 'neutral';

/**
 * How a website's status is presented.
 *
 * "failed" is deliberately an error tone rather than a neutral one: a site
 * whose vhost was never written does not work, and showing that quietly would
 * leave someone believing it does.
 */
const websiteTones: Record<WebsiteStatus, { label: string; tone: Tone }> = {
  creating: { label: 'Creating', tone: 'warn' },
  active: { label: 'Active', tone: 'ok' },
  suspended: { label: 'Suspended', tone: 'neutral' },
  failed: { label: 'Failed', tone: 'error' },
  deleting: { label: 'Deleting', tone: 'warn' },
};

export function websiteStatusPill(status: WebsiteStatus): { label: string; tone: Tone } {
  return websiteTones[status] ?? { label: status, tone: 'neutral' };
}

const jobTones: Record<JobStatus, { label: string; tone: Tone }> = {
  PENDING: { label: 'Queued', tone: 'neutral' },
  RUNNING: { label: 'Running', tone: 'warn' },
  SUCCESS: { label: 'Done', tone: 'ok' },
  FAILED: { label: 'Failed', tone: 'error' },
  CANCELLED: { label: 'Cancelled', tone: 'neutral' },
};

export function jobStatusPill(status: JobStatus): { label: string; tone: Tone } {
  return jobTones[status] ?? { label: status, tone: 'neutral' };
}

/** Human labels for the operations a job can run. */
const jobLabels: Record<string, string> = {
  'website.create': 'Create website',
  'website.update': 'Update configuration',
  'website.delete': 'Delete website',
  'website.php.set': 'Change PHP version',
  'website.php.unset': 'Disable PHP',
  'php.install': 'Install PHP',
  'php.uninstall': 'Remove PHP',
  'ssl.issue': 'Issue certificate',
  'ssl.renew': 'Renew certificate',
  'ssl.revoke': 'Revoke certificate',
};

export function jobLabel(type: string): string {
  return jobLabels[type] ?? type;
}

/**
 * Domain validation mirrored from shared/validate.
 *
 * This is a courtesy so a user is told before a round trip; the API and the
 * Agent both validate again, and they are what actually decide.
 */
const DOMAIN_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

export function domainError(raw: string): string | null {
  const domain = raw.trim().toLowerCase().replace(/\.$/, '');

  if (domain === '') {
    return 'Enter a domain name.';
  }
  if (domain.length > 253) {
    return 'A domain name may be at most 253 characters.';
  }
  if (!domain.includes('.')) {
    return 'Enter a full domain, such as example.com.';
  }
  if (!DOMAIN_PATTERN.test(domain)) {
    return 'Use letters, digits, and hyphens only, as in example.com.';
  }
  if (domain.split('.').some((label) => label.length > 63)) {
    return 'Each part of a domain may be at most 63 characters.';
  }
  return null;
}

/** normalizeDomain matches the server's normalisation. */
export function normalizeDomain(raw: string): string {
  return raw.trim().toLowerCase().replace(/\.$/, '');
}
