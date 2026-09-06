import { useCallback, useRef, useState } from 'react';

import { useConsoleSession } from '@/features/databases/hooks';
import { ApiError } from '@/services/apiClient';
import type { DatabaseConsoleSession } from '@/types/api';

/**
 * The window name the console opens into.
 *
 * Named rather than "_blank" so a second click reuses the tab instead of
 * leaving a row of half-identical phpMyAdmin tabs behind. It is also the reason
 * this works at all: the tab is opened synchronously inside the click, before
 * the request that fetches the credentials, because a window opened after an
 * await is a popup as far as the browser is concerned and gets blocked.
 */
const consoleWindow = 'jothost-database-console';

/**
 * loadEntryPage fetches phpMyAdmin's entry page as a parsed document.
 *
 * Parsed rather than pattern-matched, so a value containing a quote or an
 * attribute order phpMyAdmin changes between releases cannot silently produce
 * the wrong string.
 */
async function loadEntryPage(base: string): Promise<Document> {
  // Not the panel's API, so not the API client: this is phpMyAdmin, a
  // different application that answers in HTML and needs its own cookie sent.
  // Routing it through the service layer would mean teaching that layer about
  // a non-JSON, non-panel endpoint.
  // eslint-disable-next-line no-restricted-globals
  const response = await fetch(`${base}/index.php?route=/`, {
    credentials: 'include',
    headers: { Accept: 'text/html' },
  });
  if (!response.ok) {
    throw new Error(`phpMyAdmin answered ${response.status} at ${base}`);
  }
  return new DOMParser().parseFromString(await response.text(), 'text/html');
}

/**
 * SignInFields is what phpMyAdmin needs alongside the credentials.
 */
interface SignInFields {
  /** The CSRF token, bound to the session cookie the page was served with. */
  token: string;
  /**
   * Present only on the login form. phpMyAdmin does not require it — measured
   * both ways — but it is what the real form posts, so it is sent when it is
   * there and omitted when it is not.
   */
  setSession: string;
}

/**
 * signInFields reads what phpMyAdmin needs to accept a sign-in, from whatever
 * page it serves this browser.
 *
 * phpMyAdmin will not sign anybody in without a CSRF token bound to the session
 * cookie the page was served with. That was measured rather than inferred:
 * credentials alone, credentials with the cookie, and credentials with a token
 * from a different session each failed; only the pair worked. It is also why
 * phpMyAdmin is proxied onto the panel's own origin, since reading a page for
 * its token is exactly what the same-origin policy exists to prevent.
 *
 * The token is taken from any page, not only the login form. That is the whole
 * fix for the second database: phpMyAdmin keeps one signed-in account per
 * browser, so once an operator had opened one database, the entry page answered
 * with the signed-in interface instead of a login form. Looking for the token
 * only inside #login_form found nothing there, and every database after the
 * first refused to open with "phpMyAdmin did not return a login form" — true,
 * and no help at all.
 *
 * Posting credentials with the signed-in page's token switches the account
 * outright; no sign-out is needed. That was confirmed by asking the database
 * itself, not by reading phpMyAdmin's markup: after the post, SELECT
 * CURRENT_USER() returns the new account.
 */
function signInFields(page: Document): SignInFields | null {
  const token =
    page.querySelector<HTMLInputElement>('#login_form input[name="token"]')?.value ??
    page.querySelector<HTMLInputElement>('input[name="token"]')?.value ??
    '';
  if (!token) {
    return null;
  }
  const setSession =
    page.querySelector<HTMLInputElement>('#login_form input[name="set_session"]')?.value ?? '';
  return { token, setSession };
}

/**
 * submit posts the credentials into phpMyAdmin's login form.
 *
 * A POST, deliberately. phpMyAdmin will accept credentials in a query string
 * and the password would then be in its access log, in the Referer header of
 * every link on the page that follows, and in the operator's browser history.
 * A form body is in none of those.
 *
 * The form is removed again immediately. It exists for one submit; leaving it
 * in the document would leave the password in the DOM for anything else on the
 * page to read.
 */
function submit(
  session: DatabaseConsoleSession,
  fields: SignInFields,
  target: Window | null,
) {
  const base = session.url.replace(/\/+$/, '');

  const form = document.createElement('form');
  form.method = 'POST';
  form.action = `${base}/index.php?route=/`;
  form.target = consoleWindow;
  form.style.display = 'none';

  const values: Record<string, string> = {
    pma_username: session.username,
    pma_password: session.password,
    server: '1',
    token: fields.token,
    db: session.database,
    // Where phpMyAdmin lands once it has signed in. Without it the operator
    // arrives at the server overview and has to find their database in a list
    // of everybody else's.
    target: `index.php?route=/database/structure&db=${encodeURIComponent(session.database)}`,
  };

  // Only when phpMyAdmin offered one. The signed-in page has none, and posting
  // an empty one is not the same as leaving it out.
  if (fields.setSession) {
    values['set_session'] = fields.setSession;
  }

  for (const [name, value] of Object.entries(values)) {
    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = name;
    input.value = value;
    form.appendChild(input);
  }

  document.body.appendChild(form);
  form.submit();
  form.remove();

  // The tab was opened blank and is now loading phpMyAdmin. Bringing it
  // forward is the last step rather than the first, so a failure leaves the
  // operator on the panel reading an error rather than staring at a blank tab.
  target?.focus();
}

/**
 * useOpenConsole opens phpMyAdmin on one database, signed in.
 *
 * The panel does not proxy phpMyAdmin or hold a session for it. It hands the
 * browser the database's own credentials — the ones it already stores, and
 * already reveals to this same permission — and lets phpMyAdmin authenticate
 * them itself. Nothing here grants access that asking for the password directly
 * would not.
 */
export function useOpenConsole() {
  const session = useConsoleSession();
  const opened = useRef<Window | null>(null);
  const [failed, setFailed] = useState<string | null>(null);

  const open = useCallback(
    (databaseId: string) => {
      setFailed(null);

      // Synchronous, inside the gesture. See consoleWindow above.
      const target = window.open('', consoleWindow);
      opened.current = target;

      session.mutate(databaseId, {
        onSuccess: (result) => {
          const base = result.url.replace(/\/+$/, '');
          loadEntryPage(base)
            .then((page) => {
              const fields = signInFields(page);
              if (!fields) {
                // Neither a login form nor any page phpMyAdmin served. The
                // likeliest cause is the proxy answering with something else
                // entirely — the panel's own application, when the
                // /phpmyadmin/ location is missing.
                throw new Error('phpMyAdmin did not return a page it can sign in from.');
              }
              submit(result, fields, opened.current);
            })
            .catch((error: unknown) => {
              opened.current?.close();
              setFailed(
                error instanceof Error
                  ? error.message
                  : 'phpMyAdmin could not be opened.',
              );
            });
        },
        onError: (error) => {
          // The blank tab is the operator's problem otherwise: it says nothing
          // and never will.
          opened.current?.close();
          setFailed(
            error instanceof ApiError
              ? error.message
              : 'The console session could not be opened.',
          );
        },
      });
    },
    [session],
  );

  return { open, isPending: session.isPending, error: failed };
}
