import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

// This config does not set `test.globals`, so @testing-library/react cannot
// auto-register its afterEach cleanup. Unmount between tests explicitly, or
// mounted trees leak across tests (and, with a shared Zustand store, produce
// duplicate-element matches).
afterEach(() => {
  cleanup();
});
