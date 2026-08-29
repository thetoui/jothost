import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { FilesPage } from '@/pages/FilesPage';
import { useAuthStore } from '@/stores/authStore';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import type { FileEntry, FileListing, UserProfile } from '@/types/api';

function entry(overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    name: 'index.php',
    path: '/var/www/site/index.php',
    type: 'file',
    size: 2048,
    mode: '0644',
    modified: '2026-08-29T10:00:00Z',
    owner: 'web_site',
    group: 'nginx',
    uid: 1001,
    gid: 101,
    editable: true,
    ...overrides,
  };
}

function listing(entries: FileEntry[]): FileListing {
  return {
    path: '/var/www',
    parent: '',
    entries,
    total: entries.length,
    offset: 0,
    limit: 200,
    truncated: false,
  };
}

function profile(permissions: string[]): UserProfile {
  return {
    id: '11111111-2222-3333-4444-555555555555',
    username: 'admin',
    email: 'admin@example.test',
    status: 'active',
    roles: ['admin'],
    permissions,
    two_factor_enabled: false,
    last_login_at: null,
    created_at: '2026-01-01T00:00:00Z',
  };
}

/** route answers the calls the page makes, keyed by path fragment. */
function route(handlers: Record<string, () => Response>) {
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    for (const [fragment, handler] of Object.entries(handlers)) {
      if (url.includes(fragment)) {
        return Promise.resolve(handler());
      }
    }
    return Promise.resolve(errorResponse('NOT_FOUND', 'No handler', 404));
  });
}

describe('FilesPage', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    clearTokens();
    // useProfile only runs when a refresh token is stored, so the permission
    // gates below would otherwise all read "no permissions" and pass for the
    // wrong reason.
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
  });

  it('lists a directory with its permissions and owner', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read', 'file.write'])),
      '/files?': () =>
        envelopeResponse(
          listing([
            entry({ name: 'public', path: '/var/www/public', type: 'directory', mode: '0750' }),
            entry(),
          ]),
        ),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    expect(await screen.findByRole('button', { name: 'public' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'index.php' })).toBeInTheDocument();
    expect(screen.getByText(/0644/)).toBeInTheDocument();
    expect(screen.getAllByText('web_site:nginx').length).toBeGreaterThan(0);
  });

  // The whole point of the file.read / file.write split: a viewer can look at a
  // site's files without being able to change one.
  it('hides every write control from a user with only file.read', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read'])),
      '/files?': () => envelopeResponse(listing([entry()])),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    await screen.findByRole('button', { name: 'index.php' });

    expect(screen.queryByRole('button', { name: /upload/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /new folder/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /new file/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^delete$/i })).not.toBeInTheDocument();
  });

  it('offers the write controls to a user with file.write', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read', 'file.write'])),
      '/files?': () => envelopeResponse(listing([entry()])),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    await screen.findByRole('button', { name: 'index.php' });
    expect(screen.getByRole('button', { name: /upload/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /new folder/i })).toBeInTheDocument();
  });

  // A link out of the root is shown so it can be seen and deleted, but it is
  // labelled rather than presented as though it were its target.
  it('marks a symlink that points outside the root', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read'])),
      '/files?': () =>
        envelopeResponse(
          listing([
            entry({
              name: 'escape',
              path: '/var/www/escape',
              type: 'symlink',
              target: '/etc/passwd',
              target_inside_root: false,
            }),
          ]),
        ),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    expect(await screen.findByText('link (outside)')).toBeInTheDocument();
    // It is not a link a person can follow.
    expect(screen.queryByRole('button', { name: 'escape' })).not.toBeInTheDocument();
  });

  it('reports why a directory could not be read', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read'])),
      '/files?': () => errorResponse('NOT_FOUND', 'No such file or directory', 404),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    expect(await screen.findByText('No such file or directory')).toBeInTheDocument();
  });

  it('shows an empty folder as empty rather than as a failure', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read'])),
      '/files?': () => envelopeResponse(listing([])),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    expect(await screen.findByText('This folder is empty')).toBeInTheDocument();
  });

  it('navigates into a folder through the breadcrumb', async () => {
    const user = userEvent.setup();
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read'])),
      '/files?': () =>
        envelopeResponse(
          listing([entry({ name: 'site', path: '/var/www/site', type: 'directory' })]),
        ),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    await user.click(await screen.findByRole('button', { name: 'site' }));

    await waitFor(() => {
      expect(screen.getByRole('navigation', { name: 'Breadcrumb' })).toHaveTextContent('site');
    });
  });

  it('names the folder it is browsing in an accessible region', async () => {
    globalThis.fetch = route({
      '/auth/me': () => envelopeResponse(profile(['file.read'])),
      '/files?': () => envelopeResponse(listing([entry()])),
    }) as unknown as typeof fetch;

    renderWithProviders(<FilesPage />);

    expect(await screen.findByRole('region', { name: 'File browser' })).toBeInTheDocument();
  });
});
