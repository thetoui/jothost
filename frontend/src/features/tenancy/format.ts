import type { QuotaLimits, Subscription, SubscriptionUsage } from '@/types/api';

/**
 * How a limit is written for a person.
 *
 * The two special cases are the whole point. `null` is unlimited and `0` is
 * none at all, and a page that rendered either as "0" would be telling a
 * customer the opposite of what they bought.
 */
export function limitLabel(limit: number | null): string {
  if (limit === null) {
    return 'Unlimited';
  }
  if (limit === 0) {
    return 'None';
  }
  return String(limit);
}

/**
 * How a measured figure is written.
 *
 * `null` is "not measured", which is not zero: a subscription whose disk could
 * not be read is not one using no disk, and saying "0 B" would tell a customer
 * they are inside a quota nobody checked.
 */
export function byteLabel(bytes: number | null): string {
  if (bytes === null) {
    return 'Not measured';
  }
  if (bytes === 0) {
    return '0 B';
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

/** The counted dimensions, in the order a plan lists them. */
export const countedDimensions = [
  { key: 'websites', label: 'Websites', limit: 'max_websites' },
  { key: 'subdomains', label: 'Subdomains', limit: 'max_subdomains' },
  { key: 'databases', label: 'Databases', limit: 'max_databases' },
  { key: 'mailboxes', label: 'Mailboxes', limit: 'max_mailboxes' },
  { key: 'ftp_users', label: 'FTP accounts', limit: 'max_ftp_users' },
  { key: 'cron_jobs', label: 'Scheduled jobs', limit: 'max_cron_jobs' },
] as const satisfies ReadonlyArray<{
  key: keyof SubscriptionUsage;
  label: string;
  limit: keyof QuotaLimits;
}>;

/** usedOf reads one counted dimension's usage. */
export function usedOf(usage: SubscriptionUsage, key: (typeof countedDimensions)[number]['key']): number {
  const value = usage[key];
  return typeof value === 'number' ? value : 0;
}

/**
 * How full a dimension is, as a percentage, or null when there is no limit.
 *
 * Null rather than 0 so a bar is not drawn at all for an unlimited dimension:
 * an empty progress bar reads as "nothing used", and what it would mean here
 * is "there is nothing to fill".
 */
export function fullness(used: number, limit: number | null): number | null {
  if (limit === null) {
    return null;
  }
  if (limit === 0) {
    return used > 0 ? 100 : 0;
  }
  return Math.min(100, Math.round((used / limit) * 100));
}

/** Whether a subscription is over any counted limit. */
export function overAnyLimit(subscription: Subscription): boolean {
  return countedDimensions.some((dimension) => {
    const limit = subscription.limits[dimension.limit];
    return limit !== null && usedOf(subscription.usage, dimension.key) > limit;
  });
}

/**
 * What to say about a host's enforcement of resource limits.
 *
 * "declared" is the state that needs words rather than a colour: the limits
 * are recorded and nothing is applying them, which is neither success nor
 * failure and is the answer an operator most needs spelled out.
 */
export function isolationLabel(subscription: Subscription): {
  tone: 'success' | 'warning' | 'danger' | 'neutral';
  text: string;
} {
  switch (subscription.isolation_state) {
    case 'applied':
      return { tone: 'success', text: 'Applied by systemd' };
    case 'declared':
      return { tone: 'warning', text: 'Recorded, not enforced' };
    case 'failed':
      return { tone: 'danger', text: 'Could not be applied' };
    default:
      return { tone: 'neutral', text: 'No resource limits' };
  }
}
