import '@testing-library/jest-dom/vitest';
import { afterEach, vi } from 'vitest';
import { cleanup, configure } from '@testing-library/react';

// Testing Library waits 1s for an element by default. That is generous on a
// developer's machine and not generous at all in CI, where a dozen jsdom
// workers share a container's cores: a render that normally takes 80ms can take
// well over a second under load, and the suite fails on a different test each
// run for no reason connected to the code.
//
// Raised rather than worked around per assertion, so a real failure still fails
// — it just takes five seconds to say so.
configure({ asyncUtilTimeout: 5_000 });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  // Tokens are module-level state shared across tests; leaking one would make
  // an unrelated test appear authenticated.
  globalThis.localStorage?.clear();
});
