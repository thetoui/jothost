import {
  request,
  requestBlob,
  uploadFile,
  type UploadProgress,
} from '@/services/apiClient';
import type {
  FileArchiveResult,
  FileEntry,
  FileListing,
  FileSearchResult,
} from '@/types/api';

export interface ListInput {
  path: string;
  offset?: number;
  limit?: number;
}

export interface CreateInput {
  path: string;
  name: string;
  parents?: boolean;
}

export interface TransferInput {
  source: string;
  destination: string;
  overwrite?: boolean;
}

export interface PatchInput {
  path: string;
  name?: string;
  mode?: string;
  recursive?: boolean;
}

export interface SearchInput {
  path: string;
  query: string;
  content?: boolean;
  limit?: number;
}

/** query builds a query string, leaving out anything not set. */
function query(params: Record<string, string | number | boolean | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      search.set(key, String(value));
    }
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : '';
}

/** File manager API calls. */
export const filesApi = {
  list: ({ path, offset, limit }: ListInput, signal?: AbortSignal) =>
    request<FileListing>(
      `/files${query({ path, offset, limit })}`,
      signal ? { signal } : {},
    ),

  stat: (path: string, signal?: AbortSignal) =>
    request<{ entry: FileEntry }>(
      `/files/stat${query({ path })}`,
      signal ? { signal } : {},
    ),

  search: ({ path, query: term, content, limit }: SearchInput, signal?: AbortSignal) =>
    request<FileSearchResult>(
      `/files/search${query({ path, query: term, content, limit })}`,
      signal ? { signal } : {},
    ),

  /** Downloads a file as a blob. The token travels in a header, not the URL. */
  download: (path: string, signal?: AbortSignal) =>
    requestBlob(`/files/download${query({ path })}`, signal),

  upload: (
    directory: string,
    file: File,
    onProgress?: (progress: UploadProgress) => void,
  ) =>
    uploadFile<{ entry: FileEntry }>(
      `/files/upload${query({ path: directory })}`,
      file,
      onProgress,
    ),

  createFolder: (body: CreateInput) =>
    request<{ entry: FileEntry }>('/files/folder', { method: 'POST', body }),

  createFile: (body: CreateInput) =>
    request<{ entry: FileEntry }>('/files/file', { method: 'POST', body }),

  copy: (body: TransferInput) =>
    request<{ entry: FileEntry }>('/files/copy', { method: 'POST', body }),

  move: (body: TransferInput) =>
    request<{ entry: FileEntry }>('/files/move', { method: 'POST', body }),

  patch: (body: PatchInput) =>
    request<{ entry: FileEntry }>('/files', { method: 'PATCH', body }),

  remove: (path: string, recursive: boolean) =>
    request<{ path: string; deleted: boolean }>(
      `/files${query({ path, recursive })}`,
      { method: 'DELETE' },
    ),

  zip: (sources: string[], destination: string) =>
    request<FileArchiveResult>('/files/zip', {
      method: 'POST',
      body: { sources, destination },
    }),

  unzip: (path: string, destination: string) =>
    request<FileArchiveResult>('/files/unzip', {
      method: 'POST',
      body: { path, destination },
    }),
};
