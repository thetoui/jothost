import type { Job } from '@/types/api';

/**
 * One row of an activity list: a job, and how many identical ones it stands
 * for.
 */
export interface ActivityGroup {
  /** The newest job in the run, which is the one whose detail is shown. */
  job: Job;
  /** How many consecutive identical jobs this row represents. Always ≥ 1. */
  repeats: number;
  /** The oldest job in the run, for the "x to y" timestamp range. */
  oldest: Job;
}

/**
 * groupActivity collapses consecutive identical jobs into one row.
 *
 * A site that has had its PHP version changed, its aliases edited and its
 * certificate renewed accumulates a run of "Update configuration — Website
 * updated — Done", over and over, each one true and none of them worth its own
 * line. Twenty of those push the entries that differ off the bottom of the
 * card, which is the opposite of what a history is for: the reader is looking
 * for the thing that is *not* like the others.
 *
 * Only *consecutive* runs collapse, and only settled ones. Two updates either
 * side of a failed certificate issue stay two rows, because the order is the
 * information — collapsing them would say the site was updated twice and hide
 * that something happened in between.
 */
export function groupActivity(jobs: Job[]): ActivityGroup[] {
  const groups: ActivityGroup[] = [];

  for (const job of jobs) {
    const previous = groups[groups.length - 1];

    if (previous && canCollapse(previous.oldest, job)) {
      previous.repeats += 1;
      previous.oldest = job;
      continue;
    }

    groups.push({ job, repeats: 1, oldest: job });
  }

  return groups;
}

/**
 * canCollapse reports whether two adjacent jobs are the same event twice.
 *
 * A running job never collapses into anything: it carries live progress and a
 * message of its own, and folding it away would hide the one row the reader
 * came to watch. A failure never collapses either — two failures in a row may
 * have different reasons, and a count would imply they were the same.
 */
function canCollapse(previous: Job, next: Job): boolean {
  if (previous.type !== next.type || previous.status !== next.status) {
    return false;
  }
  if (previous.status !== 'SUCCESS') {
    return false;
  }
  // Messages differ per job on some operations ("Reloading nginx" vs
  // "Website updated"), and a differing message means a differing event.
  return (previous.message ?? '') === (next.message ?? '');
}

/**
 * How many rows an activity list shows before it asks.
 *
 * Enough to see a pattern, few enough that the card does not become the page.
 */
export const activityPreviewCount = 8;
