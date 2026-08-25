/**
 * Token storage.
 *
 * CLAUDE.md section 10 forbids putting secrets in localStorage without an
 * explicit justification, so here is the reasoning and the split:
 *
 * - The **access token stays in memory only**. It is the credential presented
 *   on every request, it is short lived (15 minutes), and keeping it out of
 *   any persistent store means a reload cannot resurrect it.
 *
 * - The **refresh token is persisted** in localStorage. Without persistence a
 *   page reload would log the user out, which pushes people toward weaker
 *   passwords and longer sessions. The residual XSS risk is mitigated
 *   server-side: refresh tokens rotate on every use and the API detects reuse,
 *   so a stolen token is single-use and its use *destroys the session* and
 *   raises an audit event (see docs/PHASE1.md).
 *
 * The stronger option is an httpOnly, SameSite=Strict cookie, which JavaScript
 * cannot read at all. That requires the API to set cookies rather than return
 * tokens in the body — a change to API_SPEC.md section 2 — and is recorded as
 * recommended hardening for Phase 24.
 */

const REFRESH_TOKEN_KEY = 'jothost.refresh_token';

/** In-memory access token. Deliberately not persisted anywhere. */
let accessToken: string | null = null;

export function getAccessToken(): string | null {
  return accessToken;
}

export function setAccessToken(token: string | null): void {
  accessToken = token;
}

export function getRefreshToken(): string | null {
  try {
    return globalThis.localStorage?.getItem(REFRESH_TOKEN_KEY) ?? null;
  } catch {
    // Private browsing modes can throw on storage access; treat it as
    // "no stored session" rather than crashing the app.
    return null;
  }
}

export function setRefreshToken(token: string | null): void {
  try {
    if (token === null) {
      globalThis.localStorage?.removeItem(REFRESH_TOKEN_KEY);
      return;
    }
    globalThis.localStorage?.setItem(REFRESH_TOKEN_KEY, token);
  } catch {
    // Storage unavailable: the session simply will not survive a reload.
  }
}

/** Clears every stored credential. Called on logout and on refresh failure. */
export function clearTokens(): void {
  setAccessToken(null);
  setRefreshToken(null);
}
