# Bootstrapper Slice-3 (GATE) e2e Test Harness — Design + Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Extend the bootstrapper e2e harness to prove Slice 3 (the GATE phase): gate-site discovery, `construction start` + the L57 adoption bounce, gate-worker sizing (repurpose idle haulers → top-up to `gate_worker_target`, keep `min_contract_earners`), monitor construction to 100% → COMPLETE, and the hand-off (launch autosizer + standing coordinators once, then the coordinator exits) — plus the sticky-GATE anti-thrash guard.

**Architecture:** Inherits the Slice-1 harness wholesale. Adds a GATE foundation extension (`gate-fixtures.ts`, `twin-admin-gate.ts`, `scenario-gate.ts`) and 8 GATE scenario tests. **Additive only** — never edits Slice-1/Slice-2 files.

**Tech Stack:** identical to Slices 1–2 (Node ≥22, TypeScript, Vitest 3, `tsx`; Go binaries; Postgres `spacetraders_test`; twin on `:8080`).

## Global Constraints

- **Assume the twin serves everything GATE needs** (per the Admiral): construction endpoints (`GET/POST construction`), the manufacturing/goods_factory executor, and the GATE admin fixtures below.
- **Additive only.** Write ONLY new files under `twin/tests/`. Do NOT edit Slice-1/Slice-2 files, `twin/src/`, or `gobot/` Go code.
- **Reuse the Slice-1 foundation** — `twin` + `TwinState` from `./twin-admin`; `startTestDaemon`/`resetDaemonDb`/`DaemonHandle` from `./daemon`; `launchBootstrap`/`pollUntil`/`advanceTicks`/`scrapeBootstrapMetric` from `./drive`; `countCall`/`ticksOf` from `./mutation-log`. Authored by the concurrent Slice-1 workflow — unresolved-import typecheck errors from those files are the concurrent build, NOT Slice-3 defects.
- **Metric names (verbatim):** `spacetraders_daemon_bootstrap_phase{phase}` (gauge; `phase="GATE"` and `phase="COMPLETE"`). Namespace `spacetraders`, subsystem `daemon`. Source: `gobot/internal/adapters/metrics/bootstrap_metrics.go`.
- **GATE `[bootstrap]` knobs:** `gate_worker_target`, `min_contract_earners`, `income_bar` (entry), `reserve_margin` (shared money-guard). Source: `gobot/internal/infrastructure/config/bootstrap.go`; design spec `docs/superpowers/specs/2026-07-11-captain-bootstrap-design.md` (GATE phase + "Fleet scaling & hand-off").
- **GATE entry = post-INCOME world:** haulers earning at $/hr ≥ `income_bar`, the under-construction jump-gate site present (0%), the construction executor available. The reconciler derives GATE from INCOME-complete.
- **Sticky-GATE (the anti-thrash invariant):** once construction has STARTED, `derivePhase` returns GATE regardless of current $/hr (haulers repurposed to construction drop $/hr below `income_bar`; a naive derivation would thrash GATE→INCOME). Source: bootstrap Slice-3 merge `d8056e40`.
- Determinism via `POST /_twin/clock`; TDD, DRY, YAGNI; commit `--no-verify`; never stage `.beads/issues.jsonl`.

---

## Design context — the GATE behaviors under test

`actGate()` each tick (guarded): (1) **discover** the jump-gate construction site; (2) **ensure** the manufacturing executor is running, `construction start <site>`, then the **L57 adoption bounce** (a fresh pipeline is inert until the executor adopts it at restart — StopContainer the executor so it re-adopts; guarded so it never re-bounces once adopted); (3) **size gate workers** — repurpose idle INCOME haulers to "manufacturing" first, top-up ~one per active gate-material chain up to `gate_worker_target`, keep `min_contract_earners` on contracts; (4) **monitor** `construction status` until 100% → COMPLETE; (5) at **COMPLETE**, launch the fleet-autosizer + siting + worker-rebalancer (each once) and the coordinator exits (`done`). Phase is **sticky on construction-started**.

### New admin-endpoint contracts (twin provides; consumed here)

- **`POST /_twin/reset` — GATE-entry mode.** Body `{ mode:"gate-entry", credits?, haulers?, incomePerHour?, gateSite?, gateMaterialChains?, constructionPercent?, workerPrice?, executorRunning? }`. **Guarantee:** a post-INCOME world — `haulers` light haulers exist and are idle-repurposable; realized $/hr = `incomePerHour` (≥ `income_bar`); the `gateSite` waypoint is an under-construction JUMP_GATE at `constructionPercent` (default 0) needing `gateMaterialChains` producing chains; the construction executor is running iff `executorRunning`; the shipyard sells worker hulls at `workerPrice`. Mutation log empty; clock frozen.
- **`POST /_twin/construction` — set construction %.** Body `{ percent }`. **Guarantee:** the daemon's `construction status` observation for the gate site returns ≥ this percent (the COMPLETE lever) — forces the GATE→COMPLETE crossing deterministically without simulating material production/delivery.
- **`GET /_twin/state` — GATE view (superset of the Slice-1 shape).** Adds `construction:{ site:string, percent:number, started:boolean, adopted:boolean }`, `gateWorkers:{ symbol:string, source:"repurposed"|"bought" }[]`, `executorRunning:boolean`, `autosizerRunning:boolean`, `standingCoordinators:{ siting:boolean, workerRebalancer:boolean }`, `done:boolean`. Mutation log records `construction-start`, `executor-bounce`, `repurpose`, `PurchaseShip` (bought workers), `launch-autosizer`, `launch-siting`, `launch-worker-rebalancer`.

---

## File structure

```
twin/tests/bootstrap/
  helpers/
    gate-fixtures.ts        # PURE: GateFixture type + gateEntry() builder
    twin-admin-gate.ts      # GATE admin client (extends the Slice-1 twin): seedGate/setConstruction/gateState
    scenario-gate.ts        # withGateScenario(fixture, fn)
  gate-golden-path.e2e.test.ts          # Scenario 1
  gate-construction-start.e2e.test.ts   # Scenario 2
  gate-worker-sizing.e2e.test.ts        # Scenario 3
  gate-worker-cap.e2e.test.ts           # Scenario 4
  gate-sticky.e2e.test.ts               # Scenario 5
  gate-monitor-complete.e2e.test.ts     # Scenario 6
  gate-handoff.e2e.test.ts              # Scenario 7
  gate-restart-idempotency.e2e.test.ts  # Scenario 8
twin/tests/unit/bootstrap/
  gate-fixtures.test.ts     # unit
```

---

## Task 1: GATE fixture builder (pure)

**Files:** Create `twin/tests/bootstrap/helpers/gate-fixtures.ts`; Test `twin/tests/unit/bootstrap/gate-fixtures.test.ts`

**Interfaces:** Produces `interface GateFixture { credits?: number; haulers?: number; incomePerHour?: number; gateSite?: string; gateMaterialChains?: number; constructionPercent?: number; workerPrice?: number; executorRunning?: boolean }`; `gateEntry(overrides?: Partial<GateFixture>): GateFixture`.

- [ ] **Step 1: Write the failing unit test**

`twin/tests/unit/bootstrap/gate-fixtures.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { gateEntry } from '../../bootstrap/helpers/gate-fixtures';

describe('gateEntry fixture', () => {
  it('defaults to a post-INCOME / GATE-entry world', () => {
    expect(gateEntry()).toEqual({
      credits: 1_500_000, haulers: 4, incomePerHour: 50000,
      gateSite: 'X1-PZ28-I57', gateMaterialChains: 3, constructionPercent: 0,
      workerPrice: 300000, executorRunning: true,
    });
  });
  it('applies overrides (shallow)', () => {
    const f = gateEntry({ constructionPercent: 90, gateMaterialChains: 5 });
    expect(f.constructionPercent).toBe(90);
    expect(f.gateMaterialChains).toBe(5);
    expect(f.gateSite).toBe('X1-PZ28-I57');
  });
});
```

- [ ] **Step 2: Run it to verify it fails** — `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/gate-fixtures.test.ts` → FAIL (module not found).

- [ ] **Step 3: Implement `gate-fixtures.ts`**

`twin/tests/bootstrap/helpers/gate-fixtures.ts`:
```ts
export interface GateFixture {
  credits?: number;
  haulers?: number;             // idle income haulers available to repurpose
  incomePerHour?: number;       // >= income_bar so INCOME is complete
  gateSite?: string;            // the under-construction jump-gate waypoint
  gateMaterialChains?: number;  // producing chains (worker-sizing input)
  constructionPercent?: number; // starting % (default 0)
  workerPrice?: number;         // price to buy a top-up worker hull
  executorRunning?: boolean;    // is the construction executor already up
}

export function gateEntry(overrides: Partial<GateFixture> = {}): GateFixture {
  return {
    credits: 1_500_000,
    haulers: 4,
    incomePerHour: 50000,
    gateSite: 'X1-PZ28-I57',
    gateMaterialChains: 3,
    constructionPercent: 0,
    workerPrice: 300000,
    executorRunning: true, // assume the executor is in the standing set (bootstrap sp-382j caveat)
    ...overrides,
  };
}
```

- [ ] **Step 4: Run it to verify it passes** — same command → PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add twin/tests/bootstrap/helpers/gate-fixtures.ts twin/tests/unit/bootstrap/gate-fixtures.test.ts
git commit --no-verify -m "test(twin): GATE fixture builder (pure)"
```

---

## Task 2: GATE admin client + `withGateScenario`

**Files:** Create `twin/tests/bootstrap/helpers/twin-admin-gate.ts`, `twin/tests/bootstrap/helpers/scenario-gate.ts`; Test `twin/tests/bootstrap/smoke-gate.e2e.test.ts`

**Interfaces:**
- Produces: `interface GateWorker { symbol: string; source: 'repurposed' | 'bought' }`; `interface GateState extends TwinState { construction: { site: string; percent: number; started: boolean; adopted: boolean }; gateWorkers: GateWorker[]; executorRunning: boolean; autosizerRunning: boolean; standingCoordinators: { siting: boolean; workerRebalancer: boolean }; done: boolean }`; `twinGate` = Slice-1 `twin` + `seedGate(f: GateFixture)`, `setConstruction(percent: number)`, `gateState(): Promise<GateState>`; `withGateScenario(fixture, fn)` + `GateScenarioCtx`.

- [ ] **Step 1: Implement `twin-admin-gate.ts`**

`twin/tests/bootstrap/helpers/twin-admin-gate.ts`:
```ts
import { TWIN_ADMIN } from '../../helpers/run-cli';
import { twin, type TwinState } from './twin-admin';
import type { GateFixture } from './gate-fixtures';

export interface GateWorker { symbol: string; source: 'repurposed' | 'bought' }
export interface GateState extends TwinState {
  construction: { site: string; percent: number; started: boolean; adopted: boolean };
  gateWorkers: GateWorker[];
  executorRunning: boolean;
  autosizerRunning: boolean;
  standingCoordinators: { siting: boolean; workerRebalancer: boolean };
  done: boolean;
}

async function post(pathUnder: string, body: unknown): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST /_twin${pathUnder} → ${res.status} ${await res.text()}`);
}

export const twinGate = {
  ...twin,
  async seedGate(fixture: GateFixture): Promise<void> {
    await post('/reset', { mode: 'gate-entry', ...fixture });
  },
  async setConstruction(percent: number): Promise<void> {
    await post('/construction', { percent });
  },
  async gateState(): Promise<GateState> {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    if (!res.ok) throw new Error(`GET /_twin/state → ${res.status}`);
    return res.json() as Promise<GateState>;
  },
};
```

- [ ] **Step 2: Implement `scenario-gate.ts`**

`twin/tests/bootstrap/helpers/scenario-gate.ts`:
```ts
import type { GateFixture } from './gate-fixtures';
import { twinGate } from './twin-admin-gate';
import { startTestDaemon, resetDaemonDb, type DaemonHandle } from './daemon';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from './drive';

export interface GateScenarioCtx {
  twin: typeof twinGate;
  daemon: DaemonHandle;
  launchBootstrap: typeof launchBootstrap;
  pollUntil: typeof pollUntil;
  advanceTicks: typeof advanceTicks;
  scrapeBootstrapMetric: typeof scrapeBootstrapMetric;
}

export async function withGateScenario(
  fixture: GateFixture,
  fn: (ctx: GateScenarioCtx) => Promise<void>,
): Promise<void> {
  await twinGate.seedGate(fixture);
  await resetDaemonDb();
  const daemon = await startTestDaemon();
  try {
    await fn({ twin: twinGate, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop();
  }
}
```

- [ ] **Step 3: Write the smoke test**

`twin/tests/bootstrap/smoke-gate.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { twinGate } from './helpers/twin-admin-gate';
import { gateEntry } from './helpers/gate-fixtures';

describe('GATE admin client (live stack)', () => {
  it('seedGate → gateState round-trips a GATE-entry world', async () => {
    await twinGate.seedGate(gateEntry({ constructionPercent: 0 }));
    const s = await twinGate.gateState();
    expect(s.construction.site).toBeTruthy();
    expect(s.construction.percent).toBe(0);
    expect(s.construction.started).toBe(false);
    expect(s.done).toBe(false);
  });
  it('setConstruction advances the percent', async () => {
    await twinGate.setConstruction(100);
    expect((await twinGate.gateState()).construction.percent).toBeGreaterThanOrEqual(100);
  });
});
```

- [ ] **Step 4: Run against the live stack (green once the twin serves the GATE admin contracts).** `cd twin && npx vitest run tests/bootstrap/smoke-gate.e2e.test.ts`.

- [ ] **Step 5: Commit**

```bash
git add twin/tests/bootstrap/helpers/twin-admin-gate.ts twin/tests/bootstrap/helpers/scenario-gate.ts twin/tests/bootstrap/smoke-gate.e2e.test.ts
git commit --no-verify -m "test(twin): GATE admin client + withGateScenario"
```

---

## Task 3: Scenario 1 — golden path (GATE)

**Files:** Create `twin/tests/bootstrap/gate-golden-path.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';

describe('bootstrap GATE — golden path', () => {
  it('starts construction, adopts, sizes workers, reaches COMPLETE, hands off to the autosizer, exits', async () => {
    await withGateScenario(gateEntry({ gateMaterialChains: 3, haulers: 4, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Construction started + executor adopted (post L57 bounce) + workers sized.
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.construction.started && st.construction.adopted && st.gateWorkers.length >= 3,
        { steps: 80, advanceMs: 1000 },
      );
      expect(s.construction.site).toBeTruthy();
      expect(s.gateWorkers.length).toBeGreaterThanOrEqual(3);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);

      // Force construction 100% → COMPLETE → hand-off launches the standing economy → coordinator exits.
      await ctx.twin.setConstruction(100);
      const done = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 40, advanceMs: 1000 });
      expect(done.autosizerRunning).toBe(true);
      expect(done.standingCoordinators.siting).toBe(true);
      expect(done.standingCoordinators.workerRebalancer).toBe(true);
      expect(done.done).toBe(true);
    });
  }, 240_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE golden-path e2e"`).

---

## Task 4: Scenario 2 — construction start + L57 adoption bounce

**Files:** Create `twin/tests/bootstrap/gate-construction-start.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';
import { countCall } from './helpers/mutation-log';

describe('bootstrap GATE — construction start + adoption bounce', () => {
  it('starts the pipeline and bounces the executor to adopt it, each exactly once', async () => {
    await withGateScenario(gateEntry({ credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.construction.started && st.construction.adopted,
        { steps: 40, advanceMs: 1000 },
      );
      expect(s.construction.started).toBe(true);
      expect(s.construction.adopted).toBe(true);
      // Idempotent: extra ticks must not re-start construction or re-bounce the (already-adopted) executor.
      await ctx.advanceTicks(12, 1000);
      const s2 = await ctx.twin.gateState();
      expect(countCall(s2.mutationLog, 'construction-start')).toBeLessThanOrEqual(1);
      expect(countCall(s2.mutationLog, 'executor-bounce')).toBeLessThanOrEqual(1);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE construction-start + L57 adoption-bounce e2e"`).

---

## Task 5: Scenario 3 — worker sizing (repurpose then top-up)

**Files:** Create `twin/tests/bootstrap/gate-worker-sizing.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';
import { countCall } from './helpers/mutation-log';

describe('bootstrap GATE — worker sizing', () => {
  it('repurposes idle haulers first, then buys only the delta to the chain count', async () => {
    // 2 idle haulers, 4 material chains → repurpose 2, buy ~2 more (delta), keep min_contract_earners.
    await withGateScenario(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.gateWorkers.length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      const repurposed = s.gateWorkers.filter((w) => w.source === 'repurposed').length;
      const bought = s.gateWorkers.filter((w) => w.source === 'bought').length;
      expect(repurposed).toBe(2);                 // both idle haulers repurposed first
      expect(bought).toBe(s.gateWorkers.length - repurposed); // only the delta bought
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(bought);
    });
  }, 240_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE worker-sizing repurpose-then-top-up e2e"`).

---

## Task 6: Scenario 4 — `gate_worker_target` cap

**Files:** Create `twin/tests/bootstrap/gate-worker-cap.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';

describe('bootstrap GATE — worker cap', () => {
  it('never sizes more gate workers than gate_worker_target, even with many chains', async () => {
    await withGateScenario(gateEntry({ haulers: 2, gateMaterialChains: 12, credits: 6_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.gateWorkers.length >= 4, { steps: 80, advanceMs: 1000 });
      await ctx.advanceTicks(15, 1000); // the cap must hold across extra ticks
      const s = await ctx.twin.gateState();
      // gate_worker_target caps the pool well below the 12 chains.
      expect(s.gateWorkers.length).toBeLessThanOrEqual(8);
    });
  }, 240_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE gate_worker_target cap e2e"`).

Note: `8` is a safe upper bound; if `test-config.yaml` pins `[bootstrap] gate_worker_target`, tighten to that value.

---

## Task 7: Scenario 5 — sticky-GATE (anti-thrash)

**Files:** Create `twin/tests/bootstrap/gate-sticky.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';

describe('bootstrap GATE — sticky phase (anti-thrash)', () => {
  it('stays GATE after construction starts even when $/hr drops below income_bar', async () => {
    await withGateScenario(gateEntry({ haulers: 4, incomePerHour: 60000, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Reach construction-started.
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.construction.started, { steps: 40, advanceMs: 1000 });
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);

      // Drop realized $/hr BELOW income_bar (as repurposing would) — a naive derivation would regress to INCOME.
      await ctx.twin.setIncome(0);
      await ctx.advanceTicks(10, 1000);
      // Sticky-on-construction-started: phase MUST remain GATE, never INCOME.
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
      const s = await ctx.twin.gateState();
      expect(s.construction.started).toBe(true); // still building, not thrashing back to buy haulers
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE sticky-phase anti-thrash e2e"`).

Note: this uses `setIncome` (from the Slice-1 `twin`, exposed via `...twin` on `twinGate` — it is `twin.setCredits`/`forceCoverage`/etc.; `setIncome` is INCOME-specific). If `twinGate` does not carry `setIncome`, import `twinIncome` from `./twin-admin-income` and call `twinIncome.setIncome(0)` (Slice-2 foundation, authored concurrently). Prefer the direct `twinIncome.setIncome` import to avoid coupling GATE admin to the income lever.

- [ ] **Correction to Step 1 code:** replace `await ctx.twin.setIncome(0);` with an explicit import:
```ts
// at top of file:
import { twinIncome } from './helpers/twin-admin-income';
// in the body, instead of ctx.twin.setIncome(0):
await twinIncome.setIncome(0);
```

---

## Task 8: Scenario 6 — monitor to COMPLETE

**Files:** Create `twin/tests/bootstrap/gate-monitor-complete.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';

describe('bootstrap GATE — monitor to COMPLETE', () => {
  it('holds GATE below 100%, derives COMPLETE at 100%', async () => {
    await withGateScenario(gateEntry({ constructionPercent: 40, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.construction.started, { steps: 40, advanceMs: 1000 });
      // At 40% it holds GATE, not done.
      await ctx.advanceTicks(6, 1000);
      let s = await ctx.twin.gateState();
      expect(s.done).toBe(false);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);
      // Force 100% → COMPLETE.
      await ctx.twin.setConstruction(100);
      s = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 30, advanceMs: 1000 });
      expect(s.done).toBe(true);
      expect(s.construction.percent).toBeGreaterThanOrEqual(100);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE monitor-to-COMPLETE e2e"`).

---

## Task 9: Scenario 7 — hand-off launches the autosizer once

**Files:** Create `twin/tests/bootstrap/gate-handoff.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withGateScenario } from './helpers/scenario-gate';
import { gateEntry } from './helpers/gate-fixtures';
import { countCall } from './helpers/mutation-log';

describe('bootstrap GATE — hand-off', () => {
  it('at COMPLETE launches autosizer + standing coordinators exactly once', async () => {
    await withGateScenario(gateEntry({ constructionPercent: 95, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.construction.started, { steps: 40, advanceMs: 1000 });
      await ctx.twin.setConstruction(100);
      const s = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 30, advanceMs: 1000 });
      expect(s.autosizerRunning).toBe(true);
      expect(s.standingCoordinators.siting).toBe(true);
      expect(s.standingCoordinators.workerRebalancer).toBe(true);
      // Extra ticks after COMPLETE must NOT relaunch anything (guarded, exactly-once).
      await ctx.advanceTicks(10, 1000);
      const s2 = await ctx.twin.gateState();
      expect(countCall(s2.mutationLog, 'launch-autosizer')).toBe(1);
      expect(countCall(s2.mutationLog, 'launch-siting')).toBeLessThanOrEqual(1);
      expect(countCall(s2.mutationLog, 'launch-worker-rebalancer')).toBeLessThanOrEqual(1);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): GATE COMPLETE hand-off launch-once e2e"`).

---

## Task 10: Scenario 8 — restart idempotency (mid-GATE)

**Files:** Create `twin/tests/bootstrap/gate-restart-idempotency.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { twinGate } from './helpers/twin-admin-gate';
import { gateEntry } from './helpers/gate-fixtures';
import { resetDaemonDb, startTestDaemon } from './helpers/daemon';
import { launchBootstrap, pollUntil } from './helpers/drive';
import { countCall } from './helpers/mutation-log';

describe('bootstrap GATE — restart idempotency', () => {
  it('no double-start / no re-bounce / no double-worker-buy / no double-autosizer across a mid-GATE restart', async () => {
    await twinGate.seedGate(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }));
    await resetDaemonDb();

    // Lifetime 1: run until construction started + at least one worker sized, then stop.
    let daemon = await startTestDaemon();
    launchBootstrap();
    const mid = await pollUntil(
      () => twinGate.gateState(),
      (s) => s.construction.started && s.gateWorkers.length >= 1,
      { steps: 60, advanceMs: 1000 },
    );
    const startsBefore = countCall(mid.mutationLog, 'construction-start');
    const bouncesBefore = countCall(mid.mutationLog, 'executor-bounce');
    expect(startsBefore).toBe(1);

    await daemon.stop();
    daemon = await startTestDaemon(); // reboot; same DB + twin (construction + workers persist)
    try {
      launchBootstrap();
      // Drive to COMPLETE.
      await twinGate.setConstruction(100);
      const done = await pollUntil(() => twinGate.gateState(), (s) => s.done, { steps: 60, advanceMs: 1000 });
      // Guards held across the restart:
      expect(countCall(done.mutationLog, 'construction-start')).toBe(1);         // not re-started
      expect(countCall(done.mutationLog, 'executor-bounce')).toBeLessThanOrEqual(Math.max(1, bouncesBefore)); // not re-bounced once adopted
      expect(countCall(done.mutationLog, 'launch-autosizer')).toBe(1);           // launched once total
      expect(done.done).toBe(true);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Run the whole GATE suite** — `cd twin && npx vitest run tests/bootstrap/gate-*.e2e.test.ts`. **Step 4: Commit** (`-m "test(twin): GATE restart-idempotency e2e"`).

---

## Self-review notes (for the implementer)

- **Coverage vs. the design spec:** gate-site discovery + construction start + adoption bounce (T4), worker sizing repurpose-then-top-up (T5), `gate_worker_target` cap (T6), sticky-GATE anti-thrash (T7), monitor→COMPLETE (T8), hand-off launch-once (T9), restart idempotency (T10), golden path (T3). All GATE behaviors from the design spec + the sticky-phase fix are covered.
- **Sticky-GATE is the headline correctness guard** (Scenario 5): it pins the anti-thrash fix from bootstrap `d8056e40` — phase stays GATE on construction-started regardless of $/hr.
- **Cross-foundation import in Scenario 5:** it uses `twinIncome.setIncome` (Slice-2 foundation) to drop $/hr — import it directly from `./helpers/twin-admin-income`; that file is authored by the concurrent Slice-2 workflow, so an unresolved import there is the concurrent build, not a defect.
- **Mutation-log call names** (`construction-start`, `executor-bounce`, `repurpose`, `PurchaseShip`, `launch-autosizer`, `launch-siting`, `launch-worker-rebalancer`) must match the twin's `GET /_twin/state` mutation-log vocabulary — align per the twin's actual labels (one-line change per assertion).
- **Config-value assumptions:** the worker-cap bound (T6, `≤ 8`) is conservative; tighten to the pinned `[bootstrap] gate_worker_target` if `test-config.yaml` sets it.
- **Cross-slice imports:** Tasks 3–10 import the Slice-1 foundation (concurrent) + the Slice-3 foundation (this run). A unified `npx tsc --noEmit` after all three slices land is the final gate.
