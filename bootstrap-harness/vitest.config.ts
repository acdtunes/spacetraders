import { defineConfig } from 'vitest/config';
import { MAX_WORKERS } from './tests/helpers/config';

// Live-stack config: the e2e scenario tests under tests/{data,income,gate}/ drive the real
// spacetraders daemon+CLI against an API base URL (default http://127.0.0.1:8080/v2, override
// via HARNESS_API_BASE_URL). They require that URL to answer + a test daemon.
//
// ── TWO MODES, one config (st-drm.22) ────────────────────────────────────────────────────────
// Default (HARNESS_PARALLEL unset): SERIAL against a single externally-launched stack on :8080 —
//   byte-for-byte the historical behaviour, so manual `vitest run` and the concurrent GATE stack
//   are untouched.
// HARNESS_PARALLEL=1: each spec file runs in its own fork driving its OWN isolated stack (twin +
//   daemon + Postgres DB) on disjoint per-worker resources. globalSetup provisions the per-worker
//   DBs/configs; the per-fork setup file boots each worker's twin. Fork count is HARNESS_MAX_WORKERS
//   (default min(cpus−2, 6), hard-capped by the Postgres connection budget).
const PARALLEL = process.env.HARNESS_PARALLEL === '1';

export default defineConfig({
  test: {
    include: ['tests/data/**/*.e2e.test.ts', 'tests/income/**/*.e2e.test.ts', 'tests/gate/**/*.e2e.test.ts'],
    environment: 'node',
    globals: false,
    retry: 2, // e2e timing-flaky (real-travel arrivals + daemon CAS retries); independent retries
    testTimeout: 300_000,
    hookTimeout: 120_000,
    ...(PARALLEL
      ? {
          fileParallelism: true,
          pool: 'forks' as const,
          // isolate:false keeps each fork alive across the files it runs, so the per-fork twin
          // (a module singleton) is booted once and reused — see tests/helpers/worker-stack.ts.
          poolOptions: { forks: { maxForks: MAX_WORKERS, minForks: MAX_WORKERS, isolate: false } },
          globalSetup: ['./tests/setup/global.ts'],
          setupFiles: ['./tests/setup/worker-setup.ts'],
        }
      : {
          // Serialized: the scenarios share one API world + one isolated daemon/DB/port.
          fileParallelism: false,
        }),
  },
});
