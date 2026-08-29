import { useState } from 'react';

import { NameDialog, PermissionsDialog } from '@/features/files/components/FileDialogs';
import { useFileMutations } from '@/features/files/hooks';
import { joinPath } from '@/features/files/format';
import { ApiError } from '@/services/apiClient';
import type { FileEntry } from '@/types/api';

export type FileDialog = 'folder' | 'file' | 'rename' | 'permissions' | 'delete' | 'zip' | null;

interface FileDialogsHostProps {
  dialog: FileDialog;
  path: string;
  selected: FileEntry[];
  onClose: () => void;
  onSettled: (error: unknown) => void;
}

function message(error: unknown): string | null {
  if (error instanceof ApiError || error instanceof Error) {
    return error.message;
  }
  return error ? 'Something went wrong' : null;
}

/**
 * FileDialogsHost renders whichever dialog the toolbar asked for.
 *
 * The dialogs live together because they share one mutation set and one error
 * surface: a failure has to stay on the dialog that caused it rather than
 * appearing behind it on the page.
 */
export function FileDialogsHost({
  dialog,
  path,
  selected,
  onClose,
  onSettled,
}: FileDialogsHostProps) {
  const mutations = useFileMutations();
  const [error, setError] = useState<string | null>(null);

  // `?? null` because noUncheckedIndexedAccess types selected[0] as possibly
  // undefined even inside the length check.
  const one = selected.length === 1 ? (selected[0] ?? null) : null;

  const handle = (promise: Promise<unknown>) => {
    setError(null);
    promise.then(
      () => {
        onSettled(null);
      },
      (failure: unknown) => {
        // Kept on the dialog rather than passed up: the person is looking at
        // the form that failed, and that is where the reason belongs.
        setError(message(failure));
      },
    );
  };

  return (
    <>
      <NameDialog
        open={dialog === 'folder'}
        title="New folder"
        label="Folder name"
        confirmLabel="Create"
        loading={mutations.createFolder.isPending}
        error={error}
        onClose={() => {
          setError(null);
          onClose();
        }}
        onSubmit={(name) => handle(mutations.createFolder.mutateAsync({ path, name }))}
      />

      <NameDialog
        open={dialog === 'file'}
        title="New file"
        label="File name"
        confirmLabel="Create"
        loading={mutations.createFile.isPending}
        error={error}
        onClose={() => {
          setError(null);
          onClose();
        }}
        onSubmit={(name) => handle(mutations.createFile.mutateAsync({ path, name }))}
      />

      <NameDialog
        open={dialog === 'rename'}
        title="Rename"
        label="New name"
        initial={one?.name ?? ''}
        confirmLabel="Rename"
        loading={mutations.patch.isPending}
        error={error}
        onClose={() => {
          setError(null);
          onClose();
        }}
        onSubmit={(name) => {
          if (one) {
            handle(mutations.patch.mutateAsync({ path: one.path, name }));
          }
        }}
      />

      <NameDialog
        open={dialog === 'zip'}
        title="Create archive"
        label="Archive name"
        initial={selected.length === 1 ? `${selected[0]?.name ?? 'archive'}.zip` : 'archive.zip'}
        confirmLabel="Create"
        loading={mutations.zip.isPending}
        error={error}
        onClose={() => {
          setError(null);
          onClose();
        }}
        onSubmit={(name) =>
          handle(
            mutations.zip.mutateAsync({
              sources: selected.map((entry) => entry.path),
              destination: joinPath(path, name.endsWith('.zip') ? name : `${name}.zip`),
            }),
          )
        }
      />

      <PermissionsDialog
        open={dialog === 'permissions'}
        entry={one}
        loading={mutations.patch.isPending}
        error={error}
        onClose={() => {
          setError(null);
          onClose();
        }}
        onSubmit={(mode, recursive) => {
          if (one) {
            handle(mutations.patch.mutateAsync({ path: one.path, mode, recursive }));
          }
        }}
      />
    </>
  );
}
