import { defineConfig } from 'vitest/config';

// Live-stack config: the e2e scenario tests under tests/{data,income,gate}/ drive the real
// spacetraders daemon+CLI against an API base URL (default http://127.0.0.1:8080/v2, override
// via HARNESS_API_BASE_URL). They require that URL to answer + a test daemon; serialized because
// the scenarios share one API world + one isolated daemon/DB/port and reset between runs.
export default defineConfig({
  test: {
    include: ['tests/data/**/*.e2e.test.ts', 'tests/income/**/*.e2e.test.ts', 'tests/gate/**/*.e2e.test.ts'],
    environment: 'node',
    globals: false,
    fileParallelism: false,
    testTimeout: 300_000,
    hookTimeout: 120_000,
  },
});
