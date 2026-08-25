import '@testing-library/jest-dom/vitest';
import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  // Tokens are module-level state shared across tests; leaking one would make
  // an unrelated test appear authenticated.
  globalThis.localStorage?.clear();
});
