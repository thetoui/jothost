import { describe, expect, it } from 'vitest';

import {
  domainError,
  jobLabel,
  jobStatusPill,
  normalizeDomain,
  websiteStatusPill,
} from '@/features/websites/status';

describe('domainError', () => {
  it('accepts ordinary domains', () => {
    for (const domain of [
      'example.com',
      'www.example.com',
      'sub.domain.example.co.uk',
      'a-b.example.test',
      'xn--bcher-kva.example',
    ]) {
      expect(domainError(domain), domain).toBeNull();
    }
  });

  it('normalises before judging, so casing and a trailing dot are fine', () => {
    expect(domainError('Example.COM.')).toBeNull();
    expect(normalizeDomain('  Example.COM. ')).toBe('example.com');
  });

  it('rejects a single label', () => {
    // A one-label name never matches a request from the internet, so a vhost
    // built from it would silently never serve anyone.
    expect(domainError('localhost')).not.toBeNull();
  });

  it('rejects names that are not domains at all', () => {
    for (const domain of [
      '',
      '   ',
      'exa mple.com',
      'example.com;rm -rf /',
      '../../etc/passwd',
      '-example.com',
      'example-.com',
      'example..com',
      'http://example.com',
    ]) {
      expect(domainError(domain), domain).not.toBeNull();
    }
  });

  it('rejects an over-long label and an over-long name', () => {
    const longLabel = 'a'.repeat(64);
    expect(domainError(`${longLabel}.com`)).not.toBeNull();

    const longName = `${Array.from({ length: 40 }, () => 'abcdef').join('.')}.com`;
    expect(longName.length).toBeGreaterThan(253);
    expect(domainError(longName)).not.toBeNull();
  });
});

describe('websiteStatusPill', () => {
  it('presents a failed site as an error, not as neutral', () => {
    // A site whose vhost was never written does not work. Showing that quietly
    // would leave someone believing it does.
    expect(websiteStatusPill('failed').tone).toBe('error');
    expect(websiteStatusPill('active').tone).toBe('ok');
    expect(websiteStatusPill('creating').tone).toBe('warn');
  });
});

describe('jobStatusPill', () => {
  it('distinguishes finished work from work in flight', () => {
    expect(jobStatusPill('SUCCESS').tone).toBe('ok');
    expect(jobStatusPill('FAILED').tone).toBe('error');
    expect(jobStatusPill('RUNNING').tone).toBe('warn');
    expect(jobStatusPill('PENDING').label).toBe('Queued');
  });
});

describe('jobLabel', () => {
  it('names known operations and passes unknown ones through', () => {
    expect(jobLabel('website.create')).toBe('Create website');
    // Every operation a phase adds needs a label, or the activity list shows
    // an internal name to the user.
    expect(jobLabel('website.php.set')).toBe('Change PHP version');
    expect(jobLabel('php.install')).toBe('Install PHP');
    expect(jobLabel('ssl.issue')).toBe('Issue certificate');
    // An operation added by a later phase must still render something, rather
    // than an empty cell. "ssl.issue" stood here until Phase 6 made it real,
    // so this names something no phase is going to implement.
    expect(jobLabel('backup.reticulate')).toBe('backup.reticulate');
  });
});
