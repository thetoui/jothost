import { useCallback, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  ArrowUp,
  ChevronRight,
  Copy,
  Download,
  FileArchive,
  FilePlus2,
  FolderPlus,
  FolderTree,
  Pencil,
  RefreshCw,
  Search,
  Shield,
  Trash2,
  Upload,
} from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, ProgressBar, SkeletonRows } from '@/components/ui/Loading';
import { TextField } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useProfile } from '@/features/auth/hooks';
import { hasPermission } from '@/features/auth/permissions';
import type { FileAction } from '@/features/files/components/FileActionMenu';
import { FileDialogsHost } from '@/features/files/components/FileDialogsHost';
import { FileTable } from '@/features/files/components/FileTable';
import { useDirectory, useDownload, useFileMutations, useFileSearch } from '@/features/files/hooks';
import { breadcrumbs, joinPath, parentOf } from '@/features/files/format';
import { ApiError } from '@/services/apiClient';
import type { FileEntry } from '@/types/api';

/**
 * ROOT is where the file manager starts.
 *
 * It matches the Agent's own allowed root. The Agent refuses anything outside
 * it regardless of what this value says — this only decides where the browser
 * opens and how far back the breadcrumb goes.
 */
const ROOT = '/var/www';

const PAGE_SIZE = 200;

/** message pulls a readable error out of whatever a mutation threw. */
function message(error: unknown): string | null {
  if (error === null || error === undefined) {
    return null;
  }
  if (error instanceof ApiError) {
    return error.message;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return 'Something went wrong';
}

export function FilesPage() {
  const [path, setPath] = useState(ROOT);
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [searchTerm, setSearchTerm] = useState('');
  const [activeSearch, setActiveSearch] = useState('');
  const [uploadProgress, setUploadProgress] = useState<number | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  // Which row's action menu is open. One at a time: two menus on screen makes
  // it ambiguous which file an action would apply to.
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const [rowDialog, setRowDialog] = useState<{ action: FileAction; entry: FileEntry } | null>(null);

  const navigate = useNavigate();

  const uploadInput = useRef<HTMLInputElement>(null);

  const { data: profile } = useProfile();
  const canWrite = hasPermission(profile, Permission.FileWrite);

  const listing = useDirectory(path, offset, PAGE_SIZE);
  const search = useFileSearch({ path, query: activeSearch }, activeSearch !== '');
  const mutations = useFileMutations();
  const download = useDownload();

  const entries = useMemo(() => listing.data?.entries ?? [], [listing.data]);
  const crumbs = useMemo(() => breadcrumbs(path, ROOT), [path]);

  const selectedEntries = useMemo(
    () => entries.filter((entry) => selected.has(entry.path)),
    [entries, selected],
  );

  const goTo = useCallback((next: string) => {
    setPath(next);
    setOffset(0);
    setSelected(new Set());
    setActiveSearch('');
    setSearchTerm('');
    setActionError(null);
    setMenuFor(null);
  }, []);

  const toggle = useCallback((target: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(target)) {
        next.delete(target);
      } else {
        next.add(target);
      }
      return next;
    });
  }, []);

  const toggleAll = useCallback(() => {
    setSelected((current) =>
      current.size === entries.length ? new Set() : new Set(entries.map((entry) => entry.path)),
    );
  }, [entries]);

  const open = useCallback(
    (entry: FileEntry) => {
      if (entry.type === 'directory') {
        goTo(entry.path);
        return;
      }
      // Clicking a file used to download it outright. Download is one of five
      // things someone might mean, and the only one that cannot be undone by
      // closing a dialog, so the click asks instead.
      setMenuFor((current) => (current === entry.path ? null : entry.path));
    },
    [goTo],
  );

  const runAction = useCallback(
    (action: FileAction, entry: FileEntry) => {
      setMenuFor(null);
      setActionError(null);

      switch (action) {
        case 'editor':
          // The editor is reached through the file it is going to open, so the
          // path travels with the navigation rather than being picked again.
          navigate(`/editor?path=${encodeURIComponent(entry.path)}`);
          return;
        case 'download':
          download.mutate({ path: entry.path, name: entry.name }, { onError: (thrown) => setActionError(message(thrown)) });
          return;
        default:
          setRowDialog({ action, entry });
      }
    },
    [navigate, download],
  );

  const runUpload = useCallback(
    (file: File) => {
      setActionError(null);
      setUploadProgress(0);
      mutations.upload.mutate(
        {
          directory: path,
          file,
          onProgress: ({ loaded, total }) =>
            setUploadProgress(total > 0 ? Math.round((loaded / total) * 100) : 0),
        },
        {
          onSettled: () => setUploadProgress(null),
          onError: (error) => setActionError(message(error)),
        },
      );
    },
    [mutations.upload, path],
  );

  const listingError = message(listing.error);
  const busy = listing.isFetching || mutations.upload.isPending;

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Files</h1>
          <p className="text-sm text-slate-500">
            Browse and manage the files under {ROOT}.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            icon={<RefreshCw aria-hidden="true" className="h-4 w-4" />}
            onClick={() => void listing.refetch()}
            loading={listing.isFetching}
          >
            Refresh
          </Button>

          <RequirePermission permission={Permission.FileWrite}>
            <Button
              icon={<Upload aria-hidden="true" className="h-4 w-4" />}
              onClick={() => uploadInput.current?.click()}
              disabled={uploadProgress !== null}
            >
              Upload
            </Button>
          </RequirePermission>
        </div>
      </header>

      <input
        ref={uploadInput}
        type="file"
        className="hidden"
        aria-hidden="true"
        tabIndex={-1}
        onChange={(event) => {
          const file = event.target.files?.[0];
          if (file) {
            runUpload(file);
          }
          // Cleared so selecting the same file twice in a row still fires.
          event.target.value = '';
        }}
      />

      <Card label="File browser">
        <CardHeader
          icon={<FolderTree aria-hidden="true" className="h-4 w-4" />}
          title={
            <nav aria-label="Breadcrumb" className="flex flex-wrap items-center gap-1 text-sm">
              {crumbs.map((crumb, index) => (
                <span key={crumb.path} className="flex items-center gap-1">
                  {index > 0 && (
                    <ChevronRight aria-hidden="true" className="h-3.5 w-3.5 text-slate-300" />
                  )}
                  <button
                    type="button"
                    onClick={() => goTo(crumb.path)}
                    aria-current={index === crumbs.length - 1 ? 'page' : undefined}
                    className={
                      index === crumbs.length - 1
                        ? 'font-medium text-slate-900'
                        : 'text-slate-500 hover:text-brand-700 hover:underline'
                    }
                  >
                    {crumb.name}
                  </button>
                </span>
              ))}
            </nav>
          }
          action={
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                icon={<ArrowUp aria-hidden="true" className="h-4 w-4" />}
                onClick={() => goTo(parentOf(path))}
                disabled={path === ROOT || parentOf(path) === ''}
              >
                Up
              </Button>
            </div>
          }
        />

        <CardBody className="space-y-3">
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              setActiveSearch(searchTerm.trim());
            }}
          >
            <div className="min-w-[16rem] flex-1">
              <TextField
                id="file-search"
                label="Search this folder"
                value={searchTerm}
                onChange={(event) => setSearchTerm(event.target.value)}
                placeholder="Part of a file name"
                adornment={<Search aria-hidden="true" className="h-4 w-4 text-slate-400" />}
              />
            </div>
            <Button type="submit" disabled={searchTerm.trim() === ''}>
              Search
            </Button>
            {activeSearch !== '' && (
              <Button
                variant="ghost"
                onClick={() => {
                  setActiveSearch('');
                  setSearchTerm('');
                }}
              >
                Clear
              </Button>
            )}
          </form>

          {uploadProgress !== null && (
            <ProgressBar value={uploadProgress} label={`Uploading… ${uploadProgress}%`} />
          )}

          {actionError && <Alert tone="danger">{actionError}</Alert>}
          {listingError && <Alert tone="danger">{listingError}</Alert>}

          {canWrite && (
            <FileToolbar
              path={path}
              selected={selectedEntries}
              onError={setActionError}
              onDone={() => setSelected(new Set())}
            />
          )}

          {activeSearch !== '' ? (
            <SearchResults
              loading={search.isLoading}
              error={message(search.error)}
              result={search.data}
              onOpen={open}
            />
          ) : listing.isLoading ? (
            <SkeletonRows rows={6} />
          ) : entries.length === 0 ? (
            <EmptyState
              icon={<FolderTree aria-hidden="true" className="h-6 w-6" />}
              title="This folder is empty"
              description="Upload a file or create a folder to get started."
            />
          ) : (
            <>
              <FileTable
                entries={entries}
                selected={selected}
                onToggle={toggle}
                onToggleAll={toggleAll}
                onOpen={open}
                menuFor={menuFor}
                onMenuAction={runAction}
                onMenuClose={() => setMenuFor(null)}
                canWrite={canWrite}
              />

              {listing.data && listing.data.total > PAGE_SIZE && (
                <div className="flex items-center justify-between border-t border-surface-border pt-3 text-sm text-slate-600">
                  <span>
                    {offset + 1}–{offset + entries.length} of {listing.data.total}
                  </span>
                  <div className="flex gap-2">
                    <Button
                      size="sm"
                      disabled={offset === 0 || busy}
                      onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                    >
                      Previous
                    </Button>
                    <Button
                      size="sm"
                      disabled={!listing.data.truncated || busy}
                      onClick={() => setOffset(offset + PAGE_SIZE)}
                    >
                      Next
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </CardBody>
      </Card>

      {/* The row menu drives the same dialogs as the toolbar, on one entry
          rather than on the selection. Sharing them keeps "rename" meaning
          exactly one thing however it was reached. */}
      <FileDialogsHost
        dialog={
          rowDialog && rowDialog.action !== 'delete'
            ? (rowDialog.action as 'rename' | 'permissions')
            : null
        }
        path={path}
        selected={rowDialog ? [rowDialog.entry] : []}
        onClose={() => setRowDialog(null)}
        onSettled={(thrown) => {
          if (thrown) {
            setActionError(message(thrown));
            return;
          }
          setActionError(null);
          setRowDialog(null);
        }}
      />

      <RowDeleteDialog
        entry={rowDialog?.action === 'delete' ? rowDialog.entry : null}
        onClose={() => setRowDialog(null)}
        onError={setActionError}
      />
    </div>
  );
}

interface RowDeleteDialogProps {
  entry: FileEntry | null;
  onClose: () => void;
  onError: (message: string | null) => void;
}

/** RowDeleteDialog confirms deleting the one file the menu was opened on. */
function RowDeleteDialog({ entry, onClose, onError }: RowDeleteDialogProps) {
  const mutations = useFileMutations();

  return (
    <ConfirmDialog
      open={entry !== null}
      onClose={onClose}
      destructive
      loading={mutations.remove.isPending}
      title={`Delete ${entry?.name}?`}
      description="This cannot be undone. There is no trash and no backup."
      confirmLabel="Delete"
      onConfirm={() => {
        if (!entry) {
          return;
        }
        mutations.remove.mutate(
          { path: entry.path, recursive: entry.type === 'directory' },
          {
            onSuccess: () => {
              onError(null);
              onClose();
            },
            onError: (thrown) => onError(message(thrown)),
          },
        );
      }}
    >
      <p className="mt-2 font-mono text-xs text-slate-600">{entry?.path}</p>
    </ConfirmDialog>
  );
}

interface FileToolbarProps {
  path: string;
  selected: FileEntry[];
  onError: (message: string | null) => void;
  onDone: () => void;
}

/** FileToolbar holds every action that changes something. */
function FileToolbar({ path, selected, onError, onDone }: FileToolbarProps) {
  const [dialog, setDialog] = useState<
    'folder' | 'file' | 'rename' | 'permissions' | 'delete' | 'zip' | null
  >(null);

  const mutations = useFileMutations();
  const download = useDownload();
  const one = selected.length === 1 ? selected[0] : null;

  const close = () => setDialog(null);
  const settle = (error: unknown) => {
    if (error) {
      onError(message(error));
      return;
    }
    onError(null);
    close();
    onDone();
  };

  return (
    <>
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-surface-border bg-surface-sunken px-3 py-2">
        <Button
          size="sm"
          icon={<FolderPlus aria-hidden="true" className="h-4 w-4" />}
          onClick={() => setDialog('folder')}
        >
          New folder
        </Button>
        <Button
          size="sm"
          icon={<FilePlus2 aria-hidden="true" className="h-4 w-4" />}
          onClick={() => setDialog('file')}
        >
          New file
        </Button>

        <span className="mx-1 h-5 w-px bg-surface-border" aria-hidden="true" />

        <Button
          size="sm"
          icon={<Download aria-hidden="true" className="h-4 w-4" />}
          disabled={!one || one.type !== 'file'}
          loading={download.isPending}
          onClick={() => {
            if (one) {
              download.mutate({ path: one.path, name: one.name });
            }
          }}
        >
          Download
        </Button>
        <Button
          size="sm"
          icon={<Pencil aria-hidden="true" className="h-4 w-4" />}
          disabled={!one}
          onClick={() => setDialog('rename')}
        >
          Rename
        </Button>
        <Button
          size="sm"
          icon={<Copy aria-hidden="true" className="h-4 w-4" />}
          disabled={!one}
          onClick={() => {
            if (!one) {
              return;
            }
            // A copy beside the original, named so it cannot collide with it.
            mutations.copy.mutate(
              { source: one.path, destination: joinPath(path, `copy-of-${one.name}`) },
              { onError: settle, onSuccess: () => settle(null) },
            );
          }}
        >
          Copy
        </Button>
        <Button
          size="sm"
          icon={<Shield aria-hidden="true" className="h-4 w-4" />}
          disabled={!one}
          onClick={() => setDialog('permissions')}
        >
          Permissions
        </Button>

        <span className="mx-1 h-5 w-px bg-surface-border" aria-hidden="true" />

        <Button
          size="sm"
          icon={<FileArchive aria-hidden="true" className="h-4 w-4" />}
          disabled={selected.length === 0}
          loading={mutations.zip.isPending || mutations.unzip.isPending}
          onClick={() => {
            // One .zip selected is an unzip; anything else is a zip. It is the
            // only reading of the selection that is never ambiguous.
            if (one && one.name.toLowerCase().endsWith('.zip')) {
              mutations.unzip.mutate(
                { path: one.path, destination: joinPath(path, one.name.replace(/\.zip$/i, '')) },
                { onError: settle, onSuccess: () => settle(null) },
              );
              return;
            }
            setDialog('zip');
          }}
        >
          {one && one.name.toLowerCase().endsWith('.zip') ? 'Extract' : 'Archive'}
        </Button>

        <Button
          size="sm"
          variant="danger"
          icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
          disabled={selected.length === 0}
          onClick={() => setDialog('delete')}
        >
          Delete
        </Button>

        {selected.length > 0 && (
          <span className="ml-auto text-xs text-slate-500">{selected.length} selected</span>
        )}
      </div>

      <FileDialogsHost
        dialog={dialog}
        path={path}
        selected={selected}
        onClose={close}
        onSettled={settle}
      />

      <ConfirmDialog
        open={dialog === 'delete'}
        onClose={close}
        destructive
        loading={mutations.remove.isPending}
        title={selected.length === 1 ? `Delete ${selected[0]?.name}?` : `Delete ${selected.length} items?`}
        description="This cannot be undone. There is no trash and no backup."
        confirmLabel="Delete"
        onConfirm={() => {
          // Sequential rather than parallel: a partial failure should stop
          // rather than leave a half-deleted tree with no way to tell which
          // half went.
          const run = async () => {
            for (const entry of selected) {
              await mutations.remove.mutateAsync({
                path: entry.path,
                recursive: entry.type === 'directory',
              });
            }
          };
          run().then(
            () => settle(null),
            (error: unknown) => settle(error),
          );
        }}
      >
        <ul className="mt-2 max-h-40 space-y-1 overflow-y-auto text-sm text-slate-600">
          {selected.map((entry) => (
            <li key={entry.path} className="font-mono text-xs">
              {entry.path}
            </li>
          ))}
        </ul>
      </ConfirmDialog>
    </>
  );
}

interface SearchResultsProps {
  loading: boolean;
  error: string | null;
  result: import('@/types/api').FileSearchResult | undefined;
  onOpen: (entry: FileEntry) => void;
}

function SearchResults({ loading, error, result, onOpen }: SearchResultsProps) {
  if (loading) {
    return <SkeletonRows rows={4} />;
  }
  if (error) {
    return <Alert tone="danger">{error}</Alert>;
  }
  if (!result || result.matches.length === 0) {
    return (
      <EmptyState
        icon={<Search aria-hidden="true" className="h-6 w-6" />}
        title="Nothing matched"
        description="Try part of a file or folder name."
      />
    );
  }

  return (
    <div className="space-y-2">
      {result.truncated && (
        <Alert tone="warning">
          Showing the first {result.matches.length} matches. Narrow the search to see the rest.
        </Alert>
      )}
      <ul className="divide-y divide-surface-border">
        {result.matches.map((match) => (
          <li key={match.entry.path} className="py-2">
            <button
              type="button"
              onClick={() => onOpen(match.entry)}
              className="text-left text-sm font-medium text-slate-800 hover:text-brand-700 hover:underline"
            >
              {match.entry.path}
            </button>
            {match.line && (
              <p className="mt-0.5 truncate font-mono text-xs text-slate-500">
                {match.line_number}: {match.line}
              </p>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
