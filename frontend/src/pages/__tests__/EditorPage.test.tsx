import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { clearTokens, setAccessToken, setRefreshToken } from '@/features/auth/tokenStorage';
import { EditorPage } from '@/pages/EditorPage';
import { useAuthStore } from '@/stores/authStore';
import { useEditorStore } from '@/stores/editorStore';
import { envelopeResponse, errorResponse, renderWithProviders } from '@/test/utils';
import type { FileContent, FileEntry, FileListing, UserProfile } from '@/types/api';

// Monaco is a large editor that needs a real DOM measurement pass and web
// workers, neither of which jsdom provides. It is replaced with a textarea so
// these tests cover what this page is responsible for — opening, tab state,
// saving, permissions, conflicts — rather than re-testing Monaco.
vi.mock('@monaco-editor/react', () => ({
  default: ({
    value,
    onChange,
    options,
  }: {
    value: string;
    onChange: (value: string | undefined) => void;
    options?: { readOnly?: boolean };
  }) => (
    <textarea
      aria-label="Editor"
      value={value}
      readOnly={options?.readOnly ?? false}
      onChange={(event) => onChange(event.target.value)}
    />
  ),
}));

vi.mock('@/features/editor/monaco', () => ({ configureMonaco: () => undefined }));

function entry(overrides: Partial<FileEntry> = {}): FileEntry {
  return {
    name: 'index.php',
    path: '/var/www/index.php',
    type: 'file',
    size: 120,
    mode: '0644',
    modified: '2026-08-30T10:00:00Z',
    owner: 'web_site',
    group: 'nginx',
    uid: 1001,
    gid: 101,
    editable: true,
    ...overrides,
  };
}

function listing(entries: FileEntry[]): FileListing {
  return { path: '/var/www', parent: '', entries, total: entries.length, offset: 0, limit: 500, truncated: false };
}

function content(overrides: Partial<FileContent> = {}): FileContent {
  return {
    path: '/var/www/index.php',
    content: '<?php echo 1;',
    size: 13,
    mode: '0644',
    modified: '2026-08-30T10:00:00Z',
    owner: 'web_site:nginx',
    checksum: 'sum-one',
    language: 'php',
    end_of_line: 'lf',
    ...overrides,
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
    recovery_codes_remaining: 0,
    last_login_at: null,
    created_at: '2026-01-01T00:00:00Z',
  };
}

interface Handlers {
  profile: string[];
  content?: () => Response;
  save?: () => Response;
}

function mockFetch({ profile: permissions, content: onContent, save }: Handlers) {
  return vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/auth/me')) {
      return Promise.resolve(envelopeResponse(profile(permissions)));
    }
    if (url.includes('/files/content')) {
      if (init?.method === 'PUT') {
        return Promise.resolve(save ? save() : envelopeResponse({ ...content(), checksum: 'sum-two' }));
      }
      return Promise.resolve(onContent ? onContent() : envelopeResponse(content()));
    }
    if (url.includes('/files?')) {
      return Promise.resolve(envelopeResponse(listing([entry()])));
    }
    return Promise.resolve(errorResponse('NOT_FOUND', 'No handler', 404));
  }) as unknown as typeof fetch;
}

describe('EditorPage', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    clearTokens();
    setRefreshToken('refresh-test');
    setAccessToken('access-test');
    useAuthStore.setState({ status: 'authenticated' });
    useEditorStore.setState({ tabs: [], activePath: null, autoSave: false });
  });

  it('starts with nothing open and says how to search', async () => {
    globalThis.fetch = mockFetch({ profile: ['file.read', 'file.write'] });
    renderWithProviders(<EditorPage />);

    expect(await screen.findByText('No file open')).toBeInTheDocument();
    // Search and replace are Monaco's own; the page has to make them findable.
    expect(screen.getByText(/Ctrl\+F to search/i)).toBeInTheDocument();
    expect(screen.getByText(/Ctrl\+H to replace/i)).toBeInTheDocument();
  });

  it('opens a file from the tree into a tab', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({ profile: ['file.read', 'file.write'] });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));

    expect(await screen.findByRole('textbox', { name: 'Editor' })).toHaveValue('<?php echo 1;');
    expect(screen.getByRole('tablist', { name: 'Open files' })).toBeInTheDocument();
  });

  it('marks a tab unsaved once it is edited, and saves it', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({ profile: ['file.read', 'file.write'] });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));
    const box = await screen.findByRole('textbox', { name: 'Editor' });

    await user.type(box, '!');
    expect(await screen.findByText('Unsaved')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /^save$/i }));
    await waitFor(() => expect(screen.getByText('Saved')).toBeInTheDocument());
  });

  // A viewer can read the file and must not be able to change it.
  it('is read-only without file.write', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({ profile: ['file.read'] });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));

    expect(await screen.findByRole('textbox', { name: 'Editor' })).toHaveAttribute('readonly');
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();
    expect(screen.getByText(/You can read these files but not change them/i)).toBeInTheDocument();
    // Auto-save is not even offered to someone who cannot save.
    expect(screen.queryByLabelText(/auto-save/i)).not.toBeInTheDocument();
  });

  // The whole point of the checksum: a second editor must not silently
  // overwrite the first one's work.
  it('asks what to do when the file changed underneath', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({
      profile: ['file.read', 'file.write'],
      save: () =>
        errorResponse('CONFLICT', 'This file changed since you opened it.', 409),
    });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));
    await user.type(await screen.findByRole('textbox', { name: 'Editor' }), '!');
    await user.click(screen.getByRole('button', { name: /^save$/i }));

    expect(await screen.findByText('This file changed since you opened it')).toBeInTheDocument();
    // Both directions lose something, so both are offered and neither is default.
    expect(screen.getByRole('button', { name: /overwrite with my version/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /reload from disk/i })).toBeInTheDocument();
  });

  it('warns before closing a tab with unsaved changes', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({ profile: ['file.read', 'file.write'] });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));
    await user.type(await screen.findByRole('textbox', { name: 'Editor' }), '!');
    await user.click(screen.getByRole('button', { name: /close index\.php \(unsaved changes\)/i }));

    expect(await screen.findByText(/Close index\.php without saving\?/)).toBeInTheDocument();
  });

  it('reports a file that is too large to edit', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({
      profile: ['file.read', 'file.write'],
      content: () =>
        errorResponse(
          'VALIDATION_FAILED',
          'This file is too large to edit in the browser. Download it instead.',
          422,
        ),
    });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));

    expect(
      await screen.findByText(/too large to edit in the browser/i),
    ).toBeInTheDocument();
  });

  it('reports a file that is not text', async () => {
    const user = userEvent.setup();
    globalThis.fetch = mockFetch({
      profile: ['file.read', 'file.write'],
      content: () =>
        errorResponse(
          'VALIDATION_FAILED',
          'This file is not text, so editing it would corrupt it.',
          422,
        ),
    });
    renderWithProviders(<EditorPage />);

    await user.click(await screen.findByRole('button', { name: /index\.php/ }));

    expect(await screen.findByText(/not text, so editing it would corrupt it/i)).toBeInTheDocument();
  });
});
