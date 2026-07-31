import { configDefaults, defineConfig } from 'vitest/config';

// Live-stack-free config for PURE / in-process (Fastify inject) tests: no globalSetup,
// so they run even before the register route / daemon exist. The in-process acceptance
// specs (construction, contracts-lifecycle) run here too; the LIVE-STACK acceptance specs
// (*.e2e.test.ts — ship-actions, cargo-trade) are excluded — they are the orchestrator's.
export default defineConfig({
  test: {
    include: ['tests/unit/**/*.test.ts', 'tests/world/**/*.test.ts', 'tests/skeleton/**/*.test.ts', 'tests/harness/**/*.test.ts', 'tests/openapi/**/*.test.ts', 'tests/acceptance/**/*.test.ts'],
    exclude: [...configDefaults.exclude, '**/*.e2e.test.ts'],
    environment: 'node',
    globals: false,
    testTimeout: 15_000,
  },
});
