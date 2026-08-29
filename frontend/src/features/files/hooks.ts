import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import {
  filesApi,
  type CreateInput,
  type PatchInput,
  type SearchInput,
  type TransferInput,
} from '@/features/files/api';
import type { UploadProgress } from '@/services/apiClient';

/** Query keys are centralised so cache invalidation stays predictable. */
export const fileKeys = {
  all: ['files'] as const,
  listing: (path: string, offset: number) => [...fileKeys.all, 'listing', path, offset] as const,
  search: (path: string, query: string, content: boolean) =>
    [...fileKeys.all, 'search', path, query, content] as const,
};

/** useDirectory reads one page of a directory. */
export function useDirectory(path: string, offset = 0, limit = 0) {
  return useQuery({
    queryKey: fileKeys.listing(path, offset),
    queryFn: ({ signal }) => filesApi.list({ path, offset, limit }, signal),
    enabled: path !== '',
    // Keeping the previous page on screen while the next loads stops the
    // browser jumping to the top of an empty list on every navigation.
    placeholderData: (previous) => previous,
  });
}

/**
 * useFileSearch runs a search only once there is something to search for.
 *
 * An empty query is not sent: the API refuses it, and asking anyway would put a
 * guaranteed error behind every keystroke that clears the box.
 */
export function useFileSearch(input: SearchInput, enabled: boolean) {
  return useQuery({
    queryKey: fileKeys.search(input.path, input.query, input.content ?? false),
    queryFn: ({ signal }) => filesApi.search(input, signal),
    enabled: enabled && input.query.trim() !== '' && input.path !== '',
  });
}

/**
 * useFileMutations bundles every change to the filesystem.
 *
 * They share one invalidation rule — any change re-reads the directory listing —
 * because a file manager that shows stale contents after an action is worse
 * than one that refetches more than it strictly needs to.
 */
export function useFileMutations() {
  const client = useQueryClient();
  const invalidate = () => {
    void client.invalidateQueries({ queryKey: fileKeys.all });
  };

  const createFolder = useMutation({
    mutationFn: (input: CreateInput) => filesApi.createFolder(input),
    onSuccess: invalidate,
  });

  const createFile = useMutation({
    mutationFn: (input: CreateInput) => filesApi.createFile(input),
    onSuccess: invalidate,
  });

  const copy = useMutation({
    mutationFn: (input: TransferInput) => filesApi.copy(input),
    onSuccess: invalidate,
  });

  const move = useMutation({
    mutationFn: (input: TransferInput) => filesApi.move(input),
    onSuccess: invalidate,
  });

  const patch = useMutation({
    mutationFn: (input: PatchInput) => filesApi.patch(input),
    onSuccess: invalidate,
  });

  const remove = useMutation({
    mutationFn: ({ path, recursive }: { path: string; recursive: boolean }) =>
      filesApi.remove(path, recursive),
    onSuccess: invalidate,
  });

  const zip = useMutation({
    mutationFn: ({ sources, destination }: { sources: string[]; destination: string }) =>
      filesApi.zip(sources, destination),
    onSuccess: invalidate,
  });

  const unzip = useMutation({
    mutationFn: ({ path, destination }: { path: string; destination: string }) =>
      filesApi.unzip(path, destination),
    onSuccess: invalidate,
  });

  const upload = useMutation({
    mutationFn: ({
      directory,
      file,
      onProgress,
    }: {
      directory: string;
      file: File;
      onProgress?: (progress: UploadProgress) => void;
    }) => filesApi.upload(directory, file, onProgress),
    onSuccess: invalidate,
  });

  return { createFolder, createFile, copy, move, patch, remove, zip, unzip, upload };
}

/**
 * useDownload saves a file through the browser.
 *
 * The blob is fetched with the session token in a header and handed to an
 * object URL, rather than pointing a link at the API: a token in a download URL
 * ends up in browser history and in the web server's access log.
 */
export function useDownload() {
  return useMutation({
    mutationFn: async ({ path, name }: { path: string; name: string }) => {
      const blob = await filesApi.download(path);
      const url = URL.createObjectURL(blob);
      try {
        const link = document.createElement('a');
        link.href = url;
        link.download = name;
        document.body.appendChild(link);
        link.click();
        link.remove();
      } finally {
        // Revoking releases the blob. Without it the whole file stays in
        // memory for the life of the tab, which for a file manager means the
        // tab grows by every file anyone downloads.
        URL.revokeObjectURL(url);
      }
      return { path, name };
    },
  });
}
