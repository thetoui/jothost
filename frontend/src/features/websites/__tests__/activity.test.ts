import { describe, expect, it } from 'vitest';

import { groupActivity } from '@/features/websites/activity';
import type { Job } from '@/types/api';

function job(overrides: Partial<Job> = {}): Job {
  return {
    id: Math.random().toString(36).slice(2),
    type: 'website.update',
    status: 'SUCCESS',
    payload: {},
    error: null,
    progress: 100,
    message: 'Website updated',
    created_by: 'user-1',
    resource_type: 'website',
    resource_id: 'site-1',
    created_at: '2026-09-05T09:00:00Z',
    started_at: '2026-09-05T09:00:01Z',
    completed_at: '2026-09-05T09:00:05Z',
    ...overrides,
  } as Job;
}

describe('groupActivity', () => {
  it('collapses a run of identical entries into one row', () => {
    // The case from the screenshot: a site whose settings were changed a dozen
    // times shows a dozen identical lines and nothing else fits on the card.
    const groups = groupActivity([job(), job(), job(), job()]);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.repeats).toBe(4);
  });

  it('keeps a single entry as a single entry', () => {
    const groups = groupActivity([job()]);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.repeats).toBe(1);
  });

  it('only collapses entries that are next to each other', () => {
    // The order is the information. Two updates either side of a certificate
    // issue are two separate things that happened, and merging them would say
    // the site was updated twice with nothing in between.
    const groups = groupActivity([
      job(),
      job({ type: 'ssl.issue', message: 'Certificate issued' }),
      job(),
    ]);

    expect(groups).toHaveLength(3);
    expect(groups.every((group) => group.repeats === 1)).toBe(true);
  });

  it('never collapses a failure', () => {
    // Two failures in a row may have failed for different reasons, and a count
    // beside one of them would claim they were the same event.
    const groups = groupActivity([
      job({ status: 'FAILED', error: 'nginx refused the configuration' }),
      job({ status: 'FAILED', error: 'the agent was unavailable' }),
    ]);

    expect(groups).toHaveLength(2);
  });

  it('never collapses work that is still running', () => {
    // A running job carries live progress. Folding it into a count would hide
    // the one row somebody has the page open to watch.
    const groups = groupActivity([
      job({ status: 'RUNNING', progress: 40, message: 'Creating directories' }),
      job({ status: 'RUNNING', progress: 40, message: 'Creating directories' }),
    ]);

    expect(groups).toHaveLength(2);
  });

  it('treats a different message as a different event', () => {
    const groups = groupActivity([
      job({ message: 'Website updated' }),
      job({ message: 'Reloading the web server' }),
    ]);

    expect(groups).toHaveLength(2);
  });

  it('keeps the newest of a run as the row, and remembers the oldest', () => {
    // The row is dated by its newest entry, because a history is read newest
    // first; the oldest is kept so the row can say what span it covers.
    const newest = job({ created_at: '2026-09-05T12:00:00Z' });
    const oldest = job({ created_at: '2026-09-05T08:00:00Z' });

    const groups = groupActivity([newest, job(), oldest]);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.job.created_at).toBe('2026-09-05T12:00:00Z');
    expect(groups[0]?.oldest.created_at).toBe('2026-09-05T08:00:00Z');
  });

  it('handles an empty history', () => {
    expect(groupActivity([])).toEqual([]);
  });
});
