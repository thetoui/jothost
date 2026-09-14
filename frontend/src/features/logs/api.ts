import { API_BASE_URL, request, requestBlob } from '@/services/apiClient';
import type { LogSource, LogSourceList, LogTail } from '@/types/api';

/** What a tail request asks for. */
export interface TailParams {
  /** How many matching lines to return. */
  limit?: number;
  search?: string;
  level?: string;
  /** A byte offset from a previous read: only what arrived since is returned,
   *  which is what makes following a log cheap enough to poll. */
  after?: number;
}

function query(params: TailParams): string {
  const search = new URLSearchParams();
  if (params.limit !== undefined) search.set('limit', String(params.limit));
  if (params.search) search.set('search', params.search);
  if (params.level) search.set('level', params.level);
  if (params.after !== undefined && params.after > 0) search.set('after', String(params.after));

  const rendered = search.toString();
  return rendered === '' ? '' : `?${rendered}`;
}

/** Host log calls. Every one names a catalogue key, never a path. */
export const logsApi = {
  sources: (signal?: AbortSignal) =>
    request<LogSourceList>('/logs', signal ? { signal } : {}),

  tail: (key: string, params: TailParams = {}, signal?: AbortSignal) =>
    request<LogTail>(
      `/logs/${encodeURIComponent(key)}${query(params)}`,
      signal ? { signal } : {},
    ),

  download: (key: string, signal?: AbortSignal) =>
    requestBlob(`/logs/${encodeURIComponent(key)}/download`, signal),

  /** The URL a download came from, for the filename the browser saves under. */
  downloadUrl: (key: string) => `${API_BASE_URL}/logs/${encodeURIComponent(key)}/download`,
};

/**
 * A website's own logs.
 *
 * Separate endpoints, and deliberately so: /logs needs server.view, which is
 * the permission for reading the whole machine — every site's traffic, the
 * mail queue, the authentication log. Somebody who looks after one website can
 * read that website's logs without being handed all of that.
 */
export const websiteLogsApi = {
  list: (websiteId: string, signal?: AbortSignal) =>
    request<{ domain: string; logs: LogSource[] }>(
      `/websites/${encodeURIComponent(websiteId)}/logs`,
      signal ? { signal } : {},
    ),

  tail: (websiteId: string, kind: string, params: TailParams = {}, signal?: AbortSignal) =>
    request<LogTail>(
      `/websites/${encodeURIComponent(websiteId)}/logs/${encodeURIComponent(kind)}${query(params)}`,
      signal ? { signal } : {},
    ),

  downloadUrl: (websiteId: string, kind: string) =>
    `${API_BASE_URL}/websites/${encodeURIComponent(websiteId)}/logs/${encodeURIComponent(kind)}/download`,
};
