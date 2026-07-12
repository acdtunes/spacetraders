import { defineConfig } from 'vitest/config';

// Default config for the twin: CLI-driven acceptance tests run under the live-stack
// globalSetup (boots twin + isolated test daemon + seeds TWINAGENT). Serialized so the
// single shared daemon + shared reset are safe.
export default defineConfig({
  test: {
    include: ['tests/**/*.test.ts'],
    exclude: ['tests/unit/**', 'tests/world/**', 'tests/skeleton/**', 'tests/harness/**'],
    environment: 'node',
    globals: false,
    globalSetup: ['tests/global-setup.ts'],
    fileParallelism: false,
    testTimeout: 30_000,
    hookTimeout: 120_000,
  },
});
