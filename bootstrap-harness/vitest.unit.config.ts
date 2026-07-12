import { defineConfig } from 'vitest/config';

// Pure/unit config: the helper unit tests under tests/unit/ are self-contained (no daemon, no
// API server, no DB). They run anywhere, immediately — this is the harness's always-green gate.
export default defineConfig({
  test: {
    include: ['tests/unit/**/*.test.ts'],
    environment: 'node',
    globals: false,
    testTimeout: 15_000,
  },
});
