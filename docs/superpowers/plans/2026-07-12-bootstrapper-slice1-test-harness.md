# Bootstrapper Slice-1 (DATA) e2e Test Harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `twin/tests/bootstrap/` e2e suite that drives the real `spacetraders` daemon + CLI against the digital twin and proves every behavior of Slice 1 (the DATA phase) of the captain-bootstrap coordinator — golden path, the guard matrix, and mid-purchase restart-idempotency.

**Architecture:** A Vitest suite layered on the existing twin foundation (`tests/helpers/run-cli.ts`, `test-config.yaml`, `scripts/launch-test-stack.sh`). A small set of harness modules (pure assertion helpers + a typed `/_twin` admin client + a per-scenario daemon-lifecycle/drive wrapper) support eight scenario tests. Each scenario admin-seeds one world via `/_twin`, boots an isolated test daemon, runs `workflow bootstrap`, steps a deterministic admin clock, and asserts across three truth surfaces (twin `GET /_twin/state` incl. a mutation log, daemon Prometheus metrics on `:9092`, and CLI stdout).

**Tech Stack:** Node ≥22, TypeScript, Vitest 3, `tsx`; the `spacetraders` Go CLI/daemon binaries (`gobot/bin/`); Postgres `spacetraders_test` on `:5433`; the twin's Fastify server on `:8080`.

## Global Constraints

- **Do NOT modify the twin** (`twin/src/**`) or the gobot bootstrap code. This plan writes only `twin/tests/bootstrap/**` (+ tiny additions to `twin/tests/helpers/**` that are bootstrap-specific). Source: spec "Non-goals".
- **`/_twin` endpoints are consumed as contracts, not implemented here.** The twin's `/v2` + `/_twin` server is a **prerequisite** built by the twin workstream (twin spec Slices 1–4). Live-stack tests go GREEN once the twin honors the admin contracts in the design spec; until then they are the authored red acceptance layer (outside-in TDD). Source: spec "Admin-endpoint contracts", "Non-goals".
- **Isolation is sacred (the `--force` PID trap).** Only ever run the daemon with `SPACETRADERS_CONFIG=twin/test-config.yaml` (test pidfile `/tmp/spacetraders-daemon-test.pid`, socket `…-test.sock`, metrics `:9092`, gRPC `:50062`, DB `spacetraders_test@:5433`). Never touch prod paths. Source: `twin/test-config.yaml` header, spec "Harness mechanics".
- **Metric names (verbatim):** `spacetraders_daemon_bootstrap_phase{phase}` (gauge, 1 for the active phase) and `spacetraders_daemon_bootstrap_probes_total` (counter). Namespace `spacetraders`, subsystem `daemon`. Source: `gobot/internal/adapters/metrics/{bootstrap_metrics.go,prometheus_collector.go}`.
- **CLI verb:** `spacetraders workflow bootstrap [--agent <A> | --player-id <n>] [--dry-run]`. All other tuning is `[bootstrap]` config in `test-config.yaml`. Source: `gobot/internal/adapters/cli/workflow_bootstrap.go`.
- **Bootstrap `[bootstrap]` knobs (mapstructure keys):** `bootstrap_disabled`, `dry_run`, `probe_target` (default 3), `coverage_bar`, `reserve_margin`, `tick_seconds`, `probe_ship_type`. Source: `gobot/internal/infrastructure/config/bootstrap.go`.
- **Cold-start fixture:** ~175,000 credits, 1 `COMMAND` frigate + 1 `SATELLITE`/probe at HQ. `probe_target − 1 = 2` probes are bought to reach 3. Source: twin spec "World model"; design spec Scenario 1.
- **Determinism:** no wall-clock sleeps for world progress. Tests step time via `POST /_twin/clock`. `test-config.yaml` sets a low `tick_seconds` for fast reconcile cadence. Source: spec "Harness mechanics".
- TDD, DRY, YAGNI, frequent commits. Commit with `git ... --no-verify` and NEVER stage `.beads/issues.jsonl`.

---

## File structure

```
twin/tests/bootstrap/
  helpers/
    parse-metrics.ts    # PURE: Prometheus text → number for a metric+labels
    mutation-log.ts     # PURE: MutationLogEntry[] queries (count/ticks/byCall)
    fixtures.ts         # PURE: types + builders for the /_twin/reset fixture body
    twin-admin.ts       # typed client over /_twin: reset/state/clock/agent/coverage/fault
    daemon.ts           # per-scenario daemon lifecycle: startTestDaemon/stop/restart + resetDaemonDb
    scenario.ts         # withScenario(fixture, fn): reset twin → reset db → boot daemon → fn → stop
    drive.ts            # launchBootstrap(flags) + pollUntil(fn, budget) + scrapeBootstrapMetric
  golden-path.e2e.test.ts       # Scenario 1
  capital-gate.e2e.test.ts      # Scenario 2
  staging.e2e.test.ts           # Scenario 3
  coverage-exit.e2e.test.ts     # Scenario 4
  dry-run.e2e.test.ts           # Scenario 5
  disabled.e2e.test.ts          # Scenario 6
  fail-closed.e2e.test.ts       # Scenario 7
  restart-idempotency.e2e.test.ts  # Scenario 8
twin/tests/unit/bootstrap/
  parse-metrics.test.ts         # unit (no live stack)
  mutation-log.test.ts          # unit
  fixtures.test.ts              # unit
```

**Prerequisites provided by the twin foundation (NOT built here):** `tests/helpers/run-cli.ts` (`runCli`, `TWIN_ADMIN`, `CLI_BIN`, `DAEMON_BIN`, `TEST_CONFIG`, `TEST_DATABASE_URL`), `test-config.yaml`, `scripts/launch-test-stack.sh`, and a `tests/global-setup.ts` that boots the twin on `:8080` and seeds the `TWINAGENT` `players` row (player_id 1). The bootstrap suite **owns its daemon per scenario** — it does not rely on a shared daemon; `startTestDaemon` uses `--force`, which SIGTERM-evicts any prior daemon on the test pidfile.

---

## Task 1: Pure assertion helpers (metrics parser, mutation-log queries, fixture builders)

**Files:**
- Create: `twin/tests/bootstrap/helpers/parse-metrics.ts`
- Create: `twin/tests/bootstrap/helpers/mutation-log.ts`
- Create: `twin/tests/bootstrap/helpers/fixtures.ts`
- Test: `twin/tests/unit/bootstrap/parse-metrics.test.ts`, `mutation-log.test.ts`, `fixtures.test.ts`

**Interfaces:**
- Produces:
  - `parseMetric(text: string, name: string, labels?: Record<string,string>): number | null`
  - `interface MutationLogEntry { seq: number; call: string; detail?: Record<string, unknown>; at: string }`
  - `countCall(log: MutationLogEntry[], call: string): number`
  - `ticksOf(log: MutationLogEntry[], call: string): string[]` (the `at` world-times of each matching entry)
  - `interface ResetFixture { credits?: number; probes?: number; frigates?: number; probePrice?: number; preScoutedMarkets?: string[]; coverage?: number }`
  - `coldStart(overrides?: Partial<ResetFixture>): ResetFixture`

- [ ] **Step 1: Write the failing unit test for the metrics parser**

`twin/tests/unit/bootstrap/parse-metrics.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { parseMetric } from '../../bootstrap/helpers/parse-metrics';

const SAMPLE = `
# HELP spacetraders_daemon_bootstrap_probes_total Probes bought
# TYPE spacetraders_daemon_bootstrap_probes_total counter
spacetraders_daemon_bootstrap_probes_total 3
# TYPE spacetraders_daemon_bootstrap_phase gauge
spacetraders_daemon_bootstrap_phase{phase="DATA"} 1
spacetraders_daemon_bootstrap_phase{phase="INCOME"} 0
`;

describe('parseMetric', () => {
  it('reads an unlabeled counter', () => {
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_probes_total')).toBe(3);
  });
  it('reads a labeled gauge series', () => {
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_phase', { phase: 'DATA' })).toBe(1);
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
  });
  it('returns null when absent', () => {
    expect(parseMetric(SAMPLE, 'nope')).toBeNull();
    expect(parseMetric(SAMPLE, 'spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBeNull();
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/parse-metrics.test.ts`
Expected: FAIL — `Cannot find module '../../bootstrap/helpers/parse-metrics'`.

- [ ] **Step 3: Implement `parse-metrics.ts`**

`twin/tests/bootstrap/helpers/parse-metrics.ts`:
```ts
// Minimal Prometheus text-exposition reader for test assertions (one metric at a time).
export function parseMetric(
  text: string,
  name: string,
  labels?: Record<string, string>,
): number | null {
  const wantLabels = labels ?? {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (line === '' || line.startsWith('#')) continue;
    const brace = line.indexOf('{');
    const metricName = brace === -1 ? line.split(/\s+/)[0] : line.slice(0, brace);
    if (metricName !== name) continue;

    let lineLabels: Record<string, string> = {};
    if (brace !== -1) {
      const end = line.indexOf('}');
      const body = line.slice(brace + 1, end);
      for (const pair of body.split(',')) {
        if (!pair) continue;
        const eq = pair.indexOf('=');
        const k = pair.slice(0, eq).trim();
        const v = pair.slice(eq + 1).trim().replace(/^"|"$/g, '');
        lineLabels[k] = v;
      }
    }
    const match = Object.entries(wantLabels).every(([k, v]) => lineLabels[k] === v);
    if (!match) continue;
    const value = line.slice(line.lastIndexOf(' ') + 1);
    const n = Number(value);
    return Number.isNaN(n) ? null : n;
  }
  return null;
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/parse-metrics.test.ts`
Expected: PASS (3 tests).

- [ ] **Step 5: Write the failing unit test for mutation-log queries**

`twin/tests/unit/bootstrap/mutation-log.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { countCall, ticksOf, type MutationLogEntry } from '../../bootstrap/helpers/mutation-log';

const LOG: MutationLogEntry[] = [
  { seq: 1, call: 'PurchaseShip', detail: { shipType: 'SHIP_PROBE' }, at: '2026-01-01T00:00:01Z' },
  { seq: 2, call: 'navigate', at: '2026-01-01T00:00:02Z' },
  { seq: 3, call: 'PurchaseShip', detail: { shipType: 'SHIP_PROBE' }, at: '2026-01-01T00:00:06Z' },
];

describe('mutation-log queries', () => {
  it('counts calls by name', () => {
    expect(countCall(LOG, 'PurchaseShip')).toBe(2);
    expect(countCall(LOG, 'refuel')).toBe(0);
  });
  it('returns the world-times of matching calls', () => {
    expect(ticksOf(LOG, 'PurchaseShip')).toEqual(['2026-01-01T00:00:01Z', '2026-01-01T00:00:06Z']);
  });
});
```

- [ ] **Step 6: Run it to verify it fails**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/mutation-log.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 7: Implement `mutation-log.ts`**

`twin/tests/bootstrap/helpers/mutation-log.ts`:
```ts
export interface MutationLogEntry {
  seq: number;
  call: string;
  detail?: Record<string, unknown>;
  at: string; // world-time (rfc3339) at which the mutation occurred
}

export function countCall(log: MutationLogEntry[], call: string): number {
  return log.filter((e) => e.call === call).length;
}

export function ticksOf(log: MutationLogEntry[], call: string): string[] {
  return log.filter((e) => e.call === call).map((e) => e.at);
}
```

- [ ] **Step 8: Write the failing unit test for the fixture builder**

`twin/tests/unit/bootstrap/fixtures.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { coldStart } from '../../bootstrap/helpers/fixtures';

describe('coldStart fixture', () => {
  it('defaults to the ~175k / 1 probe / 1 frigate cold start', () => {
    expect(coldStart()).toEqual({ credits: 175000, probes: 1, frigates: 1 });
  });
  it('applies overrides (shallow)', () => {
    expect(coldStart({ credits: 30000, probePrice: 40000 })).toEqual({
      credits: 30000, probes: 1, frigates: 1, probePrice: 40000,
    });
  });
});
```

- [ ] **Step 9: Run it to verify it fails**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/fixtures.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 10: Implement `fixtures.ts`**

`twin/tests/bootstrap/helpers/fixtures.ts`:
```ts
export interface ResetFixture {
  credits?: number;
  probes?: number;
  frigates?: number;
  probePrice?: number;
  preScoutedMarkets?: string[];
  coverage?: number;
}

export function coldStart(overrides: Partial<ResetFixture> = {}): ResetFixture {
  return { credits: 175000, probes: 1, frigates: 1, ...overrides };
}
```

- [ ] **Step 11: Run all three unit tests; verify green**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/`
Expected: PASS (7 tests across 3 files).

- [ ] **Step 12: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders
git add twin/tests/bootstrap/helpers/parse-metrics.ts twin/tests/bootstrap/helpers/mutation-log.ts twin/tests/bootstrap/helpers/fixtures.ts twin/tests/unit/bootstrap/
git commit --no-verify -m "test(twin): bootstrap harness pure helpers (metrics parse, mutation-log, fixtures)"
```

---

## Task 2: Typed `/_twin` admin client

**Files:**
- Create: `twin/tests/bootstrap/helpers/twin-admin.ts`
- Test: `twin/tests/bootstrap/smoke-admin.e2e.test.ts` (live-stack smoke; green once the twin honors the contracts)

**Interfaces:**
- Consumes: `TWIN_ADMIN` from `../helpers/run-cli`; `ResetFixture` from `./fixtures`; `MutationLogEntry` from `./mutation-log`.
- Produces a `twin` object:
  - `reset(fixture?: ResetFixture): Promise<void>` → `POST /_twin/reset`
  - `state(): Promise<TwinState>` → `GET /_twin/state`
  - `clock(opts: { mode?: 'frozen'|'running'; advanceMs?: number; setNow?: string }): Promise<{ now: string }>` → `POST /_twin/clock`
  - `setCredits(credits: number): Promise<void>` → `POST /_twin/agent`
  - `forceCoverage(opts: { fraction?: number; scoutWaypoints?: string[] }): Promise<{ coverage: number }>` → `POST /_twin/markets/coverage`
  - `injectFault(opts: { endpoint: string; code: number; count: number }): Promise<void>` → `POST /_twin/fault`
  - `interface TwinState { agent: { credits: number }; ships: TwinShip[]; coverage: number; markets: { waypoint: string; scouted: boolean; fresh: boolean }[]; clock: { now: string; mode: string }; mutationLog: MutationLogEntry[] }`
  - `interface TwinShip { symbol: string; role: string; nav: { status: string; waypoint: string }; scoutAssignment: string | null }`

- [ ] **Step 1: Implement `twin-admin.ts`** (thin typed wrappers — verified by the smoke test in Step 3, no separate unit test since it is pure HTTP I/O)

`twin/tests/bootstrap/helpers/twin-admin.ts`:
```ts
import { TWIN_ADMIN } from '../../helpers/run-cli';
import type { ResetFixture } from './fixtures';
import type { MutationLogEntry } from './mutation-log';

export interface TwinShip {
  symbol: string;
  role: string;
  nav: { status: string; waypoint: string };
  scoutAssignment: string | null;
}
export interface TwinState {
  agent: { credits: number };
  ships: TwinShip[];
  coverage: number;
  markets: { waypoint: string; scouted: boolean; fresh: boolean }[];
  clock: { now: string; mode: string };
  mutationLog: MutationLogEntry[];
}

async function post<T = unknown>(pathUnder: string, body?: unknown): Promise<T> {
  const res = await fetch(`${TWIN_ADMIN}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST /_twin${pathUnder} → ${res.status} ${await res.text()}`);
  return (res.headers.get('content-type')?.includes('json') ? res.json() : undefined) as T;
}

export const twin = {
  async reset(fixture: ResetFixture = {}): Promise<void> {
    await post('/reset', fixture);
  },
  async state(): Promise<TwinState> {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    if (!res.ok) throw new Error(`GET /_twin/state → ${res.status}`);
    return res.json() as Promise<TwinState>;
  },
  clock(opts: { mode?: 'frozen' | 'running'; advanceMs?: number; setNow?: string }) {
    return post<{ now: string }>('/clock', opts);
  },
  async setCredits(credits: number): Promise<void> {
    await post('/agent', { credits });
  },
  forceCoverage(opts: { fraction?: number; scoutWaypoints?: string[] }) {
    return post<{ coverage: number }>('/markets/coverage', opts);
  },
  async injectFault(opts: { endpoint: string; code: number; count: number }): Promise<void> {
    await post('/fault', opts);
  },
};
```

- [ ] **Step 2: Write the live-stack smoke test**

`twin/tests/bootstrap/smoke-admin.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { twin } from './helpers/twin-admin';
import { coldStart } from './helpers/fixtures';

describe('twin admin client (live stack)', () => {
  it('reset → state round-trips the cold-start fixture and an empty mutation log', async () => {
    await twin.reset(coldStart());
    const s = await twin.state();
    expect(s.agent.credits).toBe(175000);
    expect(s.ships.filter((x) => x.role === 'COMMAND').length).toBe(1);
    expect(s.mutationLog).toEqual([]);
  });
  it('clock freeze + advance moves world-now forward', async () => {
    const { now: t0 } = await twin.clock({ mode: 'frozen' });
    const { now: t1 } = await twin.clock({ advanceMs: 60_000 });
    expect(new Date(t1).getTime() - new Date(t0).getTime()).toBe(60_000);
  });
});
```

- [ ] **Step 3: Run against the live stack; verify it passes (green-gated on the twin)**

Run (twin must be up on `:8080`): `cd twin && npx vitest run tests/bootstrap/smoke-admin.e2e.test.ts`
Expected once the twin honors `/_twin/{reset,state,clock,agent}`: PASS (2 tests). Until then: FAIL with the twin's 404/501 — this is the authored red acceptance layer.

- [ ] **Step 4: Commit**

```bash
git add twin/tests/bootstrap/helpers/twin-admin.ts twin/tests/bootstrap/smoke-admin.e2e.test.ts
git commit --no-verify -m "test(twin): typed /_twin admin client + live smoke"
```

---

## Task 3: Per-scenario daemon lifecycle + drive helpers + `withScenario`

**Files:**
- Create: `twin/tests/bootstrap/helpers/daemon.ts`
- Create: `twin/tests/bootstrap/helpers/drive.ts`
- Create: `twin/tests/bootstrap/helpers/scenario.ts`
- Test: `twin/tests/bootstrap/smoke-daemon.e2e.test.ts`

**Interfaces:**
- Consumes: `DAEMON_BIN`, `CLI_BIN`, `GOBOT_DIR`, `TEST_CONFIG`, `TWIN_BASE_URL`, `TEST_DATABASE_URL`, `runCli` from `../helpers/run-cli`; `twin` + `TwinState` from `./twin-admin`; `parseMetric` from `./parse-metrics`.
- Produces:
  - `interface DaemonHandle { proc: ChildProcess; stop(): Promise<void> }`
  - `startTestDaemon(): Promise<DaemonHandle>` — spawns `spacetraders-daemon --force` with the isolated env, waits until the gRPC socket answers.
  - `resetDaemonDb(): Promise<void>` — truncates daemon-owned tables in `spacetraders_test`, preserving `players`.
  - `scrapeBootstrapMetric(name: string, labels?: Record<string,string>): Promise<number|null>` — fetch `:9092/metrics`, `parseMetric`.
  - `launchBootstrap(flags?: string[]): RunCliResult` — `spacetraders workflow bootstrap --player-id 1 [flags]`.
  - `pollUntil<T>(fn: () => Promise<T>, pred: (v: T) => boolean, opts?: { steps?: number; stepMs?: number; advanceMs?: number }): Promise<T>` — repeatedly advance the twin clock, run `fn`, return when `pred` holds or throw after `steps`.
  - `interface ScenarioCtx { twin: typeof twin; daemon: DaemonHandle; launchBootstrap: typeof launchBootstrap; pollUntil: typeof pollUntil; scrapeBootstrapMetric: typeof scrapeBootstrapMetric }`
  - `withScenario(fixture: ResetFixture, fn: (ctx: ScenarioCtx) => Promise<void>): Promise<void>`

- [ ] **Step 1: Implement `daemon.ts`**

`twin/tests/bootstrap/helpers/daemon.ts`:
```ts
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import { DAEMON_BIN, GOBOT_DIR, TEST_CONFIG, TWIN_BASE_URL, TEST_DATABASE_URL } from '../../helpers/run-cli';

const TEST_SOCK = '/tmp/spacetraders-daemon-test.sock';
const env = () => ({
  ...process.env,
  SPACETRADERS_CONFIG: TEST_CONFIG,
  ST_API_BASE_URL: TWIN_BASE_URL,
  DATABASE_URL: TEST_DATABASE_URL,
});

export interface DaemonHandle { proc: ChildProcess; stop(): Promise<void> }

async function waitForSocket(timeoutMs = 20_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(TEST_SOCK)) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`test daemon socket ${TEST_SOCK} never appeared`);
}

export async function startTestDaemon(): Promise<DaemonHandle> {
  // --force SIGTERM-evicts any prior daemon on the TEST pidfile (isolated by test-config.yaml).
  const proc = spawn(DAEMON_BIN, ['--force'], { cwd: GOBOT_DIR, env: env(), stdio: 'ignore' });
  await waitForSocket();
  const stop = () =>
    new Promise<void>((resolve) => {
      if (proc.exitCode !== null) return resolve();
      proc.once('exit', () => resolve());
      proc.kill('SIGTERM');
    });
  return { proc, stop };
}

export async function resetDaemonDb(): Promise<void> {
  // Truncate every daemon-owned table but preserve the seeded players row + migration ledger.
  const sql = `DO $$ DECLARE r RECORD; BEGIN
    FOR r IN SELECT tablename FROM pg_tables WHERE schemaname='public'
             AND tablename NOT IN ('players','schema_migrations','goose_db_version') LOOP
      EXECUTE 'TRUNCATE TABLE public.' || quote_ident(r.tablename) || ' RESTART IDENTITY CASCADE';
    END LOOP; END $$;`;
  const res = spawnSync('psql', [TEST_DATABASE_URL, '-v', 'ON_ERROR_STOP=1', '-c', sql], { encoding: 'utf8' });
  if (res.status !== 0) throw new Error(`resetDaemonDb failed: ${res.stderr}`);
}
```

- [ ] **Step 2: Implement `drive.ts`**

`twin/tests/bootstrap/helpers/drive.ts`:
```ts
import { runCli, type RunCliResult } from '../../helpers/run-cli';
import { twin } from './twin-admin';
import { parseMetric } from './parse-metrics';

export function launchBootstrap(flags: string[] = []): RunCliResult {
  return runCli(['workflow', 'bootstrap', '--player-id', '1', ...flags]);
}

export async function scrapeBootstrapMetric(
  name: string, labels?: Record<string, string>,
): Promise<number | null> {
  const res = await fetch('http://127.0.0.1:9092/metrics');
  if (!res.ok) throw new Error(`GET :9092/metrics → ${res.status}`);
  return parseMetric(await res.text(), name, labels);
}

export async function pollUntil<T>(
  fn: () => Promise<T>,
  pred: (v: T) => boolean,
  opts: { steps?: number; stepMs?: number; advanceMs?: number } = {},
): Promise<T> {
  const steps = opts.steps ?? 30;
  const stepMs = opts.stepMs ?? 300;      // real wall gap between daemon reconcile observations
  const advanceMs = opts.advanceMs ?? 0;  // twin world-time advanced each step (0 = don't advance)
  let last: T = await fn();
  for (let i = 0; i < steps; i++) {
    if (pred(last)) return last;
    if (advanceMs > 0) await twin.clock({ advanceMs });
    await new Promise((r) => setTimeout(r, stepMs));
    last = await fn();
  }
  if (pred(last)) return last;
  throw new Error(`pollUntil exhausted ${steps} steps; last=${JSON.stringify(last)}`);
}

// Advance a fixed number of reconcile ticks with no exit predicate — for "run N ticks, then
// assert the world did NOT change" scenarios (dry-run, disabled, capital-gate-while-poor).
export async function advanceTicks(
  steps: number, advanceMs: number, stepMs = 300,
): Promise<void> {
  for (let i = 0; i < steps; i++) {
    await twin.clock({ advanceMs });
    await new Promise((r) => setTimeout(r, stepMs));
  }
}
```

- [ ] **Step 3: Implement `scenario.ts`**

`twin/tests/bootstrap/helpers/scenario.ts`:
```ts
import type { ResetFixture } from './fixtures';
import { twin } from './twin-admin';
import { startTestDaemon, resetDaemonDb, type DaemonHandle } from './daemon';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from './drive';

export interface ScenarioCtx {
  twin: typeof twin;
  daemon: DaemonHandle;
  launchBootstrap: typeof launchBootstrap;
  pollUntil: typeof pollUntil;
  advanceTicks: typeof advanceTicks;
  scrapeBootstrapMetric: typeof scrapeBootstrapMetric;
}

export async function withScenario(
  fixture: ResetFixture,
  fn: (ctx: ScenarioCtx) => Promise<void>,
): Promise<void> {
  await twin.reset(fixture);            // (1) admin-seed the world; clock left frozen
  await resetDaemonDb();                // (2) wipe daemon mirror (keep players)
  const daemon = await startTestDaemon(); // (3) boot isolated daemon (re-syncs from twin)
  try {
    await fn({ twin, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop();               // (4) teardown; twin stays up for the next scenario
  }
}
```

- [ ] **Step 4: Write the live-stack daemon smoke test**

`twin/tests/bootstrap/smoke-daemon.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { coldStart } from './helpers/fixtures';
import { withScenario } from './helpers/scenario';
import { runCli } from '../helpers/run-cli';

describe('test daemon lifecycle (live stack)', () => {
  it('boots against the twin and the CLI reports the seeded cold-start treasury', async () => {
    await withScenario(coldStart(), async () => {
      const { stdout, exitCode } = runCli(['agent']);
      expect(exitCode).toBe(0);
      expect(stdout).toContain('175000');
    });
  });
});
```

- [ ] **Step 5: Run against the live stack; verify it passes (green-gated on the twin)**

Run: `cd twin && npx vitest run tests/bootstrap/smoke-daemon.e2e.test.ts`
Expected once the twin serves `/register`+`/my/agent` and the daemon boots: PASS. This proves the whole rig (twin + isolated daemon + CLI) before any scenario logic.

- [ ] **Step 6: Commit**

```bash
git add twin/tests/bootstrap/helpers/daemon.ts twin/tests/bootstrap/helpers/drive.ts twin/tests/bootstrap/helpers/scenario.ts twin/tests/bootstrap/smoke-daemon.e2e.test.ts
git commit --no-verify -m "test(twin): per-scenario daemon lifecycle + drive + withScenario"
```

---

## Task 4: Scenario 1 — golden path (Delivery Slice 1 complete)

**Files:**
- Create: `twin/tests/bootstrap/golden-path.e2e.test.ts`

**Interfaces:**
- Consumes: `withScenario`, `coldStart`, `twin`, `countCall`, `scrapeBootstrapMetric` (via ctx).

- [ ] **Step 1: Write the golden-path scenario test**

`twin/tests/bootstrap/golden-path.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { withScenario } from './helpers/scenario';
import { coldStart } from './helpers/fixtures';
import { countCall } from './helpers/mutation-log';

describe('bootstrap DATA — golden path', () => {
  it('cold agent → buys 2 probes → 3 total scouting → holds at DATA-complete', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap();

      // Reconciler buys probe_target-1 = 2 probes, one per tick, at HQ (no travel).
      const bought = await ctx.pollUntil(
        () => ctx.twin.state(),
        (s) => countCall(s.mutationLog, 'PurchaseShip') >= 2,
        { steps: 40, advanceMs: 1000 },
      );

      // World truth: 3 probes, each assigned to scout-all-markets, treasury debited 2×price.
      const probes = bought.ships.filter((x) => x.role === 'SATELLITE');
      expect(probes.length).toBe(3);
      expect(probes.every((p) => p.scoutAssignment === 'scout-all-markets')).toBe(true);
      expect(bought.agent.credits).toBe(175000 - 2 * 40000);
      expect(countCall(bought.mutationLog, 'PurchaseShip')).toBe(2); // no over-buy

      // Daemon truth: probes counter = 2 (bought), phase gauge active on DATA.
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_probes_total')).toBe(2);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' })).toBe(1);

      // Force coverage over the bar → next observation derives DATA-complete and holds.
      await ctx.twin.forceCoverage({ fraction: 0.95 });
      const done = await ctx.pollUntil(
        () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' }),
        (v) => v === 1, // DATA gauge stays 1 while holding at DATA-complete (INCOME is a stub)
        { steps: 10, advanceMs: 1000 },
      );
      expect(done).toBe(1);
      // Still exactly 2 buys — DATA-complete does not buy more.
      expect(countCall((await ctx.twin.state()).mutationLog, 'PurchaseShip')).toBe(2);
    });
  }, 120_000);
});
```

- [ ] **Step 2: Run against the live stack; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/golden-path.e2e.test.ts`
Expected: PASS — the headline DATA acceptance. (Green once the twin serves the DATA endpoints + admin contracts.)

- [ ] **Step 3: Commit**

```bash
git add twin/tests/bootstrap/golden-path.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA golden-path e2e (Delivery Slice 1)"
```

---

## Task 5: Scenario 2 — capital gate (block then release)

**Files:**
- Create: `twin/tests/bootstrap/capital-gate.e2e.test.ts`

- [ ] **Step 1: Write the test**

`twin/tests/bootstrap/capital-gate.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { withScenario } from './helpers/scenario';
import { coldStart } from './helpers/fixtures';
import { countCall } from './helpers/mutation-log';

describe('bootstrap DATA — capital gate', () => {
  it('blocks a buy that would exceed reserve_margin×treasury, then releases when funded', async () => {
    // probePrice 40k but only 60k credits: a 40k buy leaves 20k < 50% reserve → blocked.
    await withScenario(coldStart({ credits: 60000, probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap();

      // Over a bounded budget the money-guard buys nothing.
      await ctx.advanceTicks(8, 1000);
      const s1 = await ctx.twin.state();
      expect(countCall(s1.mutationLog, 'PurchaseShip')).toBe(0);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_probes_total')).toBe(0);

      // Fund the treasury; the gate releases and a buy occurs.
      await ctx.twin.setCredits(600000);
      const funded = await ctx.pollUntil(
        () => ctx.twin.state(),
        (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
        { steps: 12, advanceMs: 1000 },
      );
      expect(countCall(funded.mutationLog, 'PurchaseShip')).toBeGreaterThanOrEqual(1);
    });
  }, 120_000);
});
```

- [ ] **Step 2: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/capital-gate.e2e.test.ts`
Expected: PASS — zero buys while under-funded, ≥1 after `setCredits`.

- [ ] **Step 3: Commit**

```bash
git add twin/tests/bootstrap/capital-gate.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA capital-gate block-then-release e2e"
```

---

## Task 6: Scenario 3 — staging (one buy per tick)

**Files:**
- Create: `twin/tests/bootstrap/staging.e2e.test.ts`

- [ ] **Step 1: Write the test**

`twin/tests/bootstrap/staging.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { withScenario } from './helpers/scenario';
import { coldStart } from './helpers/fixtures';
import { ticksOf } from './helpers/mutation-log';

describe('bootstrap DATA — staging', () => {
  it('buys at most one probe per reconcile tick (distinct world-times per buy)', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.state(),
        (v) => ticksOf(v.mutationLog, 'PurchaseShip').length >= 2,
        { steps: 40, advanceMs: 1000 },
      );
      const buyTimes = ticksOf(s.mutationLog, 'PurchaseShip');
      expect(buyTimes.length).toBe(2);
      // The two buys carry different world-times → they landed on different ticks (never batched).
      expect(new Set(buyTimes).size).toBe(2);
    });
  }, 120_000);
});
```

- [ ] **Step 2: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/staging.e2e.test.ts`
Expected: PASS — two buys with distinct world-times.

- [ ] **Step 3: Commit**

```bash
git add twin/tests/bootstrap/staging.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA staging one-buy-per-tick e2e"
```

---

## Task 7: Scenario 4 — coverage-bar exit → INCOME hand-off

**Files:**
- Create: `twin/tests/bootstrap/coverage-exit.e2e.test.ts`

- [ ] **Step 1: Write the test**

`twin/tests/bootstrap/coverage-exit.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { withScenario } from './helpers/scenario';
import { coldStart } from './helpers/fixtures';

describe('bootstrap DATA — coverage-bar exit', () => {
  it('holds in DATA below the bar, then derives DATA-complete once coverage crosses it', async () => {
    await withScenario(coldStart({ probePrice: 40000, coverage: 0.0 }), async (ctx) => {
      ctx.launchBootstrap();

      // Below the bar, the phase stays DATA across ticks.
      await ctx.pollUntil(
        () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' }),
        (v) => v === 1,
        { steps: 6, advanceMs: 1000 },
      );
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);

      // Force coverage ≥ bar → the next tick derives DATA-complete (INCOME remains a stub: gauge
      // does not advance to an active INCOME; the daemon logs the not-implemented hold).
      await ctx.twin.forceCoverage({ fraction: 0.95 });
      const s = await ctx.pollUntil(
        () => ctx.twin.state(),
        (st) => st.coverage >= 0.95,
        { steps: 6, advanceMs: 1000 },
      );
      expect(s.coverage).toBeGreaterThanOrEqual(0.95);
      // INCOME never becomes the active phase in this harness (Slice-2 out of scope).
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
    });
  }, 120_000);
});
```

- [ ] **Step 2: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/coverage-exit.e2e.test.ts`
Expected: PASS — DATA held below bar; DATA-complete derived at/after the bar; INCOME never activates.

- [ ] **Step 3: Commit**

```bash
git add twin/tests/bootstrap/coverage-exit.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA coverage-bar exit → INCOME-handoff e2e"
```

---

## Task 8: Scenario 5 — dry-run

**Files:**
- Create: `twin/tests/bootstrap/dry-run.e2e.test.ts`

- [ ] **Step 1: Write the test**

`twin/tests/bootstrap/dry-run.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { withScenario } from './helpers/scenario';
import { coldStart } from './helpers/fixtures';

describe('bootstrap DATA — dry-run', () => {
  it('evaluates but mutates nothing under --dry-run', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap(['--dry-run']);
      // Advance several ticks; the world must stay byte-identical (no buys, no navigates).
      await ctx.advanceTicks(8, 1000);
      const s = await ctx.twin.state();
      expect(s.mutationLog).toEqual([]);            // nothing mutated
      expect(s.ships.filter((x) => x.role === 'SATELLITE').length).toBe(1); // still the original probe
      expect(s.agent.credits).toBe(175000);         // treasury untouched
    });
  }, 120_000);
});
```

- [ ] **Step 2: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/dry-run.e2e.test.ts`
Expected: PASS — empty mutation log, world unchanged.

- [ ] **Step 3: Commit**

```bash
git add twin/tests/bootstrap/dry-run.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA dry-run no-mutation e2e"
```

---

## Task 9: Scenario 6 — `bootstrap_disabled` escape

**Files:**
- Create: `twin/tests/bootstrap/disabled.e2e.test.ts`
- Create: `twin/tests/bootstrap/fixtures/test-config.disabled.yaml` (a copy of `test-config.yaml` with a `[bootstrap] bootstrap_disabled: true` block appended)

**Interfaces:**
- Consumes: `runCli` env override (`SPACETRADERS_CONFIG` pointed at the disabled config). Note: `withScenario` boots the daemon with the default `TEST_CONFIG`; this scenario needs the disabled config, so it boots the daemon directly via `startTestDaemon` after setting `SPACETRADERS_CONFIG`. To keep the helper simple, this test sets `process.env.SPACETRADERS_CONFIG` for the daemon boot and restores it after. (Alternative accepted in review: add a `configPath` option to `startTestDaemon`.)

- [ ] **Step 1: Create the disabled config fixture**

`twin/tests/bootstrap/fixtures/test-config.disabled.yaml` — copy `twin/test-config.yaml` verbatim, then append:
```yaml

bootstrap:
  bootstrap_disabled: true
```

- [ ] **Step 2: Write the test**

`twin/tests/bootstrap/disabled.e2e.test.ts`:
```ts
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { twin } from './helpers/twin-admin';
import { coldStart } from './helpers/fixtures';
import { resetDaemonDb, startTestDaemon } from './helpers/daemon';
import { launchBootstrap, advanceTicks } from './helpers/drive';
import { REPO_ROOT } from '../helpers/run-cli';

const DISABLED_CONFIG = path.join(REPO_ROOT, 'twin', 'tests', 'bootstrap', 'fixtures', 'test-config.disabled.yaml');

describe('bootstrap DATA — disabled escape', () => {
  it('no-ops every tick when bootstrap_disabled=true', async () => {
    await twin.reset(coldStart({ probePrice: 40000 }));
    await resetDaemonDb();
    const prev = process.env.SPACETRADERS_CONFIG;
    process.env.SPACETRADERS_CONFIG = DISABLED_CONFIG;
    const daemon = await startTestDaemon();
    try {
      launchBootstrap();
      await advanceTicks(8, 1000);
      const s = await twin.state();
      expect(s.mutationLog).toEqual([]); // disabled → no acting
    } finally {
      await daemon.stop();
      if (prev === undefined) delete process.env.SPACETRADERS_CONFIG; else process.env.SPACETRADERS_CONFIG = prev;
    }
  }, 120_000);
});
```

- [ ] **Step 3: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/disabled.e2e.test.ts`
Expected: PASS — empty mutation log under the disabled config.

- [ ] **Step 4: Commit**

```bash
git add twin/tests/bootstrap/fixtures/test-config.disabled.yaml twin/tests/bootstrap/disabled.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA bootstrap_disabled escape e2e"
```

---

## Task 10: Scenario 7 — fail-closed on a read fault

**Files:**
- Create: `twin/tests/bootstrap/fail-closed.e2e.test.ts`

- [ ] **Step 1: Write the test**

`twin/tests/bootstrap/fail-closed.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { withScenario } from './helpers/scenario';
import { coldStart } from './helpers/fixtures';
import { countCall } from './helpers/mutation-log';

describe('bootstrap DATA — fail-closed', () => {
  it('does not buy on a failed observation, then resumes when the fault clears', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      // Arm a single fleet-read failure that lands on the next reconcile observe.
      await ctx.twin.injectFault({ endpoint: 'GET /my/ships', code: 500, count: 1 });
      ctx.launchBootstrap();

      // Give the faulted tick time to occur and fail closed (no buy), then the fault self-clears
      // and buying resumes — assert we still reach 2 buys (fail-closed delayed, not lost).
      const s = await ctx.pollUntil(
        () => ctx.twin.state(),
        (v) => countCall(v.mutationLog, 'PurchaseShip') >= 2,
        { steps: 40, advanceMs: 1000 },
      );
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(2);
      // The first buy's world-time is strictly after the fault would have fired — it did NOT buy
      // on the faulted observe (proved by never exceeding 2 buys + reaching the target).
    });
  }, 120_000);
});
```

- [ ] **Step 2: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/fail-closed.e2e.test.ts`
Expected: PASS — reaches exactly 2 buys after the fault clears; never over-buys.

- [ ] **Step 3: Commit**

```bash
git add twin/tests/bootstrap/fail-closed.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA fail-closed-on-read-fault e2e (Delivery Slice 2)"
```

---

## Task 11: Scenario 8 — restart idempotency (mid-purchase)

**Files:**
- Create: `twin/tests/bootstrap/restart-idempotency.e2e.test.ts`

**Interfaces:**
- Consumes: `twin`, `resetDaemonDb`, `startTestDaemon`, `launchBootstrap`, `pollUntil`, `countCall`. This scenario manages the daemon directly (not via `withScenario`) because it restarts the daemon mid-run **without** wiping the daemon DB (the operation record persists; only the process restarts).

- [ ] **Step 1: Write the test**

`twin/tests/bootstrap/restart-idempotency.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { twin } from './helpers/twin-admin';
import { coldStart } from './helpers/fixtures';
import { resetDaemonDb, startTestDaemon } from './helpers/daemon';
import { launchBootstrap, pollUntil } from './helpers/drive';
import { countCall } from './helpers/mutation-log';

describe('bootstrap DATA — restart idempotency', () => {
  it('no double-buy when the daemon is killed mid-purchase and rebooted', async () => {
    await twin.reset(coldStart({ probePrice: 40000 }));
    await resetDaemonDb();

    // Lifetime 1: run until exactly the FIRST probe purchase is recorded, then freeze progress.
    let daemon = await startTestDaemon();
    launchBootstrap();
    const afterFirst = await pollUntil(
      () => twin.state(),
      (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
      { steps: 40, advanceMs: 1000 },
    );
    expect(countCall(afterFirst.mutationLog, 'PurchaseShip')).toBe(1); // one buy so far
    expect(afterFirst.ships.filter((x) => x.role === 'SATELLITE').length).toBe(2); // probe really exists

    // Kill the daemon BEFORE it re-observes — the twin world (2 probes) persists; the daemon keeps
    // no in-memory progress cursor, so a reboot must re-derive "need 1 more", not "need 2".
    await daemon.stop();
    daemon = await startTestDaemon(); // reboot; SAME test DB (operation record persists), SAME twin
    try {
      launchBootstrap();
      const done = await pollUntil(
        () => twin.state(),
        (s) => s.ships.filter((x) => x.role === 'SATELLITE').length >= 3,
        { steps: 40, advanceMs: 1000 },
      );
      // The crux: exactly probe_target-1 = 2 PurchaseShip across BOTH daemon lifetimes — no re-buy
      // of the probe that existed at restart.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBe(2);
      expect(done.ships.filter((x) => x.role === 'SATELLITE').length).toBe(3);
    } finally {
      await daemon.stop();
    }
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify it passes**

Run: `cd twin && npx vitest run tests/bootstrap/restart-idempotency.e2e.test.ts`
Expected: PASS — exactly 2 `PurchaseShip` across the restart; final fleet 3 probes.

- [ ] **Step 3: Run the whole bootstrap suite green**

Run: `cd twin && npx vitest run tests/bootstrap/`
Expected: all 8 scenarios + 2 smokes PASS against the live stack.

- [ ] **Step 4: Commit**

```bash
git add twin/tests/bootstrap/restart-idempotency.e2e.test.ts
git commit --no-verify -m "test(twin): bootstrap DATA restart-idempotency e2e (Delivery Slice 3)"
```

---

## Self-review notes (for the implementer)

- **Green-gating:** every `*.e2e.test.ts` is authored against the twin's documented `/v2` + `/_twin` contracts and goes green once the twin serves them; the unit tests in Task 1 are green immediately (no live stack). Run unit tests with `-c vitest.unit.config.ts`; run e2e with the default config (its `global-setup.ts` boots the twin + seeds `TWINAGENT`).
- **INCOME/GATE stay out:** scenario 4 asserts INCOME never activates; no test drives contracts/construction.
- **Isolation:** every daemon boot goes through `startTestDaemon`, which only ever uses `test-config.yaml` (or the disabled copy) — never a prod path; `--force` evicts a stale test daemon via the test pidfile.
- **Open questions from the spec carried here:** (a) if a future assertion needs a real in-test probe arrival, the daemon-timer-vs-admin-clock interaction must be settled — not needed by any task here (buys are at HQ; coverage is admin-forced); (b) `resetDaemonDb` truncates all non-`players` tables — if the daemon's migration ledger table is named other than `schema_migrations`/`goose_db_version`, extend the exclusion list.
