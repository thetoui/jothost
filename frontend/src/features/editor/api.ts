import { request } from '@/services/apiClient';
import type { FileContent, FileContentSaved } from '@/types/api';

export interface SaveInput {
  path: string;
  content: string;
  /** What was loaded. Omitted only when creating a file. */
  checksum?: string;
  /** Save anyway, after the panel has told the user what changed underneath. */
  force?: boolean;
}

/** Code editor API calls. */
export const editorApi = {
  open: (path: string, signal?: AbortSignal) =>
    request<FileContent>(
      `/files/content?path=${encodeURIComponent(path)}`,
      signal ? { signal } : {},
    ),

  save: (body: SaveInput) =>
    request<FileContentSaved>('/files/content', { method: 'PUT', body }),
};
