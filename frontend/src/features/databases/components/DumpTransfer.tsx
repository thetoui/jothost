import { useCallback, useRef, useState } from 'react';

import { getAccessToken } from '@/features/auth/tokenStorage';
import type { Database } from '@/types/api';

/**
 * Exporting and importing a database as a .sql file.
 *
 * These two tiles used to be links to the Backups page, which is a different
 * thing wearing the same word: a backup is an archive of the host, taken on a
 * schedule and restored whole. An operator who wants to send somebody a dump
 * of one database, or who has been sent one, wants a file — and had no way to
 * get one out or put one in.
 *
 * Both go through fetch rather than a plain link or a form. A download needs an
 * Authorization header, which an <a href> cannot carry; an upload needs to
 * stream, which a form post of a large file does badly.
 */

/** What the panel will offer to send through a browser. Matches the API's cap. */
const maxImportBytes = 2 * 1024 * 1024 * 1024;

export interface TransferState {
  busy: boolean;
  error: string | null;
  /** Set briefly after a successful import, so the panel can say so. */
  done: string | null;
}

const idle: TransferState = { busy: false, error: null, done: null };

/**
 * useDumpTransfer exports and imports one database.
 */
export function useDumpTransfer(database: Database) {
  const [state, setState] = useState<TransferState>(idle);
  const input = useRef<HTMLInputElement | null>(null);

  const exportDump = useCallback(async () => {
    setState({ busy: true, error: null, done: null });
    try {
      // The token goes in a header, which is why this cannot be an anchor: a
      // link would arrive unauthenticated and the panel would answer 401 to a
      // click that looked perfectly ordinary.
      //
      // Not the API client: that layer parses JSON and this response is a
      // file. Teaching it to sometimes return a Blob would make every caller
      // handle a case only this one has.
      // eslint-disable-next-line no-restricted-globals
      const response = await fetch(`/api/v1/databases/${encodeURIComponent(database.id)}/export`, {
        headers: { Authorization: `Bearer ${getAccessToken() ?? ''}` },
      });
      if (!response.ok) {
        throw new Error(await describe(response));
      }

      const blob = await response.blob();
      // The name the server chose, not one built here: it comes from the
      // database's own validated name and the two should not drift.
      saveBlob(blob, filenameFrom(response) ?? `${database.name}.sql`);
      setState(idle);
    } catch (error) {
      setState({
        busy: false,
        done: null,
        error: error instanceof Error ? error.message : 'The export failed.',
      });
    }
  }, [database.id, database.name]);

  const importDump = useCallback(
    async (file: File) => {
      if (file.size > maxImportBytes) {
        setState({
          busy: false,
          done: null,
          error: 'That file is larger than this panel accepts. Restore a backup instead.',
        });
        return;
      }

      setState({ busy: true, error: null, done: null });
      try {
        // Not the API client, for the same reason as the export: the body here
        // is the file itself, streamed, rather than JSON.
        // eslint-disable-next-line no-restricted-globals
        const response = await fetch(
          `/api/v1/databases/${encodeURIComponent(database.id)}/import?filename=${encodeURIComponent(file.name)}`,
          {
            method: 'POST',
            headers: {
              Authorization: `Bearer ${getAccessToken() ?? ''}`,
              'Content-Type': 'application/sql',
            },
            body: file,
          },
        );
        if (!response.ok) {
          throw new Error(await describe(response));
        }
        setState({ busy: false, error: null, done: `Loaded ${file.name}` });
      } catch (error) {
        setState({
          busy: false,
          done: null,
          error: error instanceof Error ? error.message : 'The import failed.',
        });
      }
    },
    [database.id],
  );

  /** Opens the file picker. The input itself is rendered by the caller. */
  const chooseFile = useCallback(() => {
    input.current?.click();
  }, []);

  const onFileChosen = useCallback(
    (event: React.ChangeEvent<HTMLInputElement>) => {
      const file = event.target.files?.[0];
      // Cleared so choosing the same file twice fires again — an operator who
      // has just fixed their dump and picked it a second time expects
      // something to happen.
      event.target.value = '';
      if (file) {
        void importDump(file);
      }
    },
    [importDump],
  );

  return { state, exportDump, chooseFile, onFileChosen, input, clear: () => setState(idle) };
}

/** describe turns a failed response into something worth reading. */
async function describe(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { error?: { message?: string } };
    if (body.error?.message) {
      return body.error.message;
    }
  } catch {
    // Not JSON. The status is all there is.
  }
  return `The server answered ${response.status}.`;
}

/** filenameFrom reads the name the server asked the browser to save under. */
function filenameFrom(response: Response): string | null {
  const header = response.headers.get('Content-Disposition');
  if (!header) {
    return null;
  }
  const match = /filename="([^"]+)"/.exec(header);
  return match?.[1] ?? null;
}

/**
 * saveBlob hands the file to the browser.
 *
 * The object URL is revoked afterwards. Without it the whole dump stays in
 * memory for as long as the tab is open, which for a database dump is not a
 * small amount.
 */
function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
