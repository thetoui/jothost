import { useCallback, useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';

import { logsApi, websiteLogsApi } from '@/features/logs/api';
import type { TailParams } from '@/features/logs/api';
import { ApiError } from '@/services/apiClient';
import type { LogLine, LogTail } from '@/types/api';

export const logKeys = {
  all: ['logs'] as const,
  sources: () => [...logKeys.all, 'sources'] as const,
};

/**
 * useLogSources lists the logs this host has.
 *
 * Refetched on an interval because the list itself changes: a log file appears
 * the first time its daemon writes one, and the sizes shown next to each are
 * how an operator decides which is worth opening.
 */
export function useLogSources() {
  return useQuery({
    queryKey: logKeys.sources(),
    queryFn: ({ signal }) => logsApi.sources(signal),
    refetchInterval: 30_000,
    placeholderData: (previous) => previous,
  });
}

/** How often a followed log is polled. */
const FOLLOW_INTERVAL_MS = 3_000;

/**
 * How many lines are kept on screen while following.
 *
 * A log that is being written to quickly would otherwise grow the page without
 * limit until the tab runs out of memory — which is precisely the log an
 * operator leaves open while they wait for a problem to happen again.
 */
const MAX_BUFFERED_LINES = 5_000;

export interface TailFilters {
  key: string;
  search: string;
  level: string;
  limit: number;
  /** Follow the log: poll for what is appended and add it to what is shown. */
  live: boolean;
}

export interface TailState {
  lines: LogLine[];
  /** Where the next follow poll continues from. */
  offset: number;
  size: number;
  /** The file was replaced or truncated while we were reading it. */
  rotated: boolean;
  /** There is more in the file than one read could reach. */
  partial: boolean;
  /** How many lines the filters removed from the last read. */
  filtered: number;
  path: string;
  loading: boolean;
  error: string | null;
  /** True once a follow poll has been made, so the page can say it is live. */
  following: boolean;
}

const emptyState: TailState = {
  lines: [],
  offset: 0,
  size: 0,
  rotated: false,
  partial: false,
  filtered: 0,
  path: '',
  loading: true,
  error: null,
  following: false,
};

/**
 * useLogTail reads one log, and follows it when asked to.
 *
 * Following is polling rather than a stream, and that is a decision rather than
 * a shortcut. A server-sent stream cannot carry an Authorization header, so the
 * alternatives were putting the access token in a URL — where it would be
 * written to every proxy log between here and the panel, including the very
 * access log this page displays — or a second authentication path built only
 * for logs. Polling a byte offset costs one small request every few seconds and
 * needs neither.
 *
 * The offset is what makes it cheap: after the first read, each poll asks only
 * for what was appended since, so a quiet log transfers nothing at all.
 */
export function useLogTail(filters: TailFilters): TailState & { refresh: () => void } {
  const [state, setState] = useState<TailState>(emptyState);

  // The offset lives in a ref as well as in state: the polling effect reads it
  // on every tick, and depending on the state value would restart the timer on
  // each poll — turning a steady 3-second interval into a rescheduling loop.
  const offsetRef = useRef(0);
  const { key, search, level, limit, live } = filters;

  const apply = useCallback((result: LogTail, append: boolean) => {
    offsetRef.current = result.offset;

    setState((previous) => {
      // A rotated file's offsets refer to a file that no longer exists, so what
      // is on screen is replaced rather than added to. Appending would splice
      // two different files together and present them as one.
      const lines =
        append && !result.rotated ? [...previous.lines, ...result.lines] : result.lines;

      return {
        lines: lines.length > MAX_BUFFERED_LINES ? lines.slice(-MAX_BUFFERED_LINES) : lines,
        offset: result.offset,
        size: result.size,
        rotated: result.rotated,
        partial: result.partial,
        filtered: result.filtered,
        path: result.path,
        loading: false,
        error: null,
        following: append,
      };
    });
  }, []);

  const [reloads, setReloads] = useState(0);
  const refresh = useCallback(() => setReloads((n) => n + 1), []);

  // The first read, and every read after the source or the filters change.
  useEffect(() => {
    if (key === '') {
      setState({ ...emptyState, loading: false });
      return;
    }

    const controller = new AbortController();
    offsetRef.current = 0;
    setState((previous) => ({ ...previous, loading: true, error: null, following: false }));

    logsApi
      .tail(key, { limit, search, level }, controller.signal)
      .then((result) => apply(result, false))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({ ...emptyState, loading: false, error: describe(error) });
      });

    return () => controller.abort();
  }, [key, search, level, limit, reloads, apply]);

  // Following: what has been appended since the last read.
  useEffect(() => {
    if (!live || key === '') return;

    const controller = new AbortController();
    const timer = setInterval(() => {
      logsApi
        .tail(key, { limit, search, level, after: offsetRef.current }, controller.signal)
        .then((result) => {
          // Nothing new is the common case and must not disturb the page: a
          // poll that returned no lines should not clear the ones on screen or
          // scroll them.
          if (result.lines.length === 0 && !result.rotated) return;
          apply(result, true);
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted) return;
          // A failed poll stops the follow. The timer is cleared rather than
          // just flagged: a log that was deleted or rotated away answers 404
          // for as long as the tab is open, and a follower that only *said* it
          // had stopped would go on asking every three seconds forever.
          clearInterval(timer);
          setState((previous) => ({ ...previous, error: describe(error), following: false }));
        });
    }, FOLLOW_INTERVAL_MS);

    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [live, key, limit, search, level, apply]);

  return { ...state, refresh };
}

function describe(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return 'The log could not be read.';
}

/**
 * useWebsiteLogs lists one site's own logs.
 */
export function useWebsiteLogs(websiteId: string, enabled = true) {
  return useQuery({
    queryKey: ['website-logs', websiteId],
    queryFn: ({ signal }) => websiteLogsApi.list(websiteId, signal),
    enabled: enabled && websiteId !== '',
  });
}

/**
 * useWebsiteLogTail follows the end of one of a site's logs.
 *
 * Refetched on an interval rather than held open: a log viewer that streams
 * needs a connection per viewer, and this is a page somebody leaves open on a
 * second monitor.
 */
export function useWebsiteLogTail(
  websiteId: string,
  kind: string,
  params: TailParams,
  enabled = true,
) {
  return useQuery({
    queryKey: ['website-log-tail', websiteId, kind, params],
    queryFn: ({ signal }) => websiteLogsApi.tail(websiteId, kind, params, signal),
    enabled: enabled && websiteId !== '' && kind !== '',
    refetchInterval: 5000,
  });
}
