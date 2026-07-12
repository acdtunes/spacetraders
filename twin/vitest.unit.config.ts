import { defineConfig } from 'vitest/config';

// Live-stack-free config for PURE / in-process (Fastify inject) tests: no globalSetup,
// so they run even before the register route / daemon exist.
export default defineConfig({
  test: {
    include: ['tests/unit/**/*.test.ts', 'tests/world/**/*.test.ts', 'tests/skeleton/**/*.test.ts'],
    environment: 'node',
    globals: false,
    testTimeout: 15_000,
  },
});
