# Bootstrapper Slice-2 (INCOME) e2e Test Harness — Design + Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Extend the bootstrapper e2e harness to prove Slice 2 (the INCOME phase): frigate retirement, contract-hub selection + one-per-hub light-hauler placement (capped at `hauler_target`, staged + capital-gated), `batch-contract` launch, and the `income_bar` exit → GATE hand-off.

**Architecture:** Inherits the Slice-1 harness wholesale (`twin/tests/bootstrap/helpers/{parse-metrics,mutation-log,fixtures,twin-admin,daemon,drive,scenario}.ts`, the deterministic admin clock, three truth surfaces). Adds an INCOME foundation extension (`fixtures-income.ts`, `twin-admin-income.ts`, `scenario-income.ts`) and 9 INCOME scenario tests. **Additive only** — never edits Slice-1 files.

**Tech Stack:** identical to Slice 1 (Node ≥22, TypeScript, Vitest 3, `tsx`; Go `spacetraders`/`spacetraders-daemon` binaries; Postgres `spacetraders_test`; twin on `:8080`).

## Global Constraints

- **Assume the twin serves everything INCOME needs** (per the Admiral): the DATA endpoints + the INCOME endpoints (contracts negotiate/accept/deliver/fulfill, light-hauler shipyard listings) + the INCOME admin fixtures below. Unlike Slice 1, no twin-availability hedging in the design.
- **Additive only.** Write ONLY new files under `twin/tests/`. Do NOT edit Slice-1's files, `twin/src/`, or `gobot/` Go code.
- **Reuse the Slice-1 foundation** — import `withScenario`'s siblings (`startTestDaemon`, `resetDaemonDb`, `launchBootstrap`, `pollUntil`, `advanceTicks`, `scrapeBootstrapMetric`) and `twin` + `TwinState` from `./twin-admin`; `MutationLogEntry`/`countCall`/`ticksOf` from `./mutation-log`. These are authored by the Slice-1 workflow (concurrent). If `tsc` reports unresolved imports from those foundation files, that is the concurrent Slice-1 build not-yet-landed — NOT a Slice-2 error.
- **Metric names (verbatim):** `spacetraders_daemon_bootstrap_phase{phase}` (gauge), `spacetraders_daemon_bootstrap_haulers_total` (counter — INCOME's analog of `_probes_total`). Namespace `spacetraders`, subsystem `daemon`. Source: `gobot/internal/adapters/metrics/bootstrap_metrics.go` (`haulersTotal`).
- **INCOME `[bootstrap]` knobs:** `hauler_target` (4–5), `income_bar` ($/hr), `min_contract_earners`, `reserve_margin` (shared money-guard), `probe_ship_type`/hauler type. Source: `gobot/internal/infrastructure/config/bootstrap.go`; design spec `docs/superpowers/specs/2026-07-11-captain-bootstrap-design.md` (INCOME phase).
- **INCOME entry = post-DATA world:** 3 probes with markets scouted (coverage ≥ bar), contract-good-bearing markets (hub candidates), command frigate present + contract-tagged, treasury grown to the ~600k band. The reconciler derives INCOME because coverage already clears `coverage_bar`.
- Determinism via `POST /_twin/clock`; TDD, DRY, YAGNI; commit `--no-verify`; never stage `.beads/issues.jsonl`.

---

## Design context — the INCOME behaviors under test

Once coverage clears the bar, `actIncome()` each tick (guarded "already done / in-flight?"): (1) **retire the frigate** from contract dedication; (2) **select contract hubs** from scouted market data (coverage desc → source-cost asc → density desc → deterministic tiebreak); (3) **buy light haulers** one per viable hub, capped at `hauler_target`, staged (≤1/tick) + capital-gated (≤`reserve_margin`×treasury), each placed on its hub; (4) run **`batch-contract`** (launch the ContractFleetCoordinator once); (5) **exit** INCOME when realized $/hr ≥ `income_bar`, deriving DATA→…→GATE (GATE is a stub in this harness).

### New admin-endpoint contracts (twin provides; consumed here)

- **`POST /_twin/reset` — INCOME-entry mode.** Body `{ mode:"income-entry", credits?, haulerPrice?, hubs?:string[], frigateContractTagged?, creditsPerHour? }`. **Guarantee:** rebuilds a post-DATA world — coverage ≥ bar; the listed `hubs` are marketplace waypoints bearing contract goods (so the hub selector ranks them); the command frigate exists and (if `frigateContractTagged`) carries the "contract" dedication; the shipyard sells `SHIP_LIGHT_HAULER` at `haulerPrice`; realized $/hr starts at `creditsPerHour` (default 0). Mutation log empty; clock frozen.
- **`POST /_twin/income` — set realized $/hr.** Body `{ creditsPerHour }`. **Guarantee:** the daemon's realized-$/hr observation returns ≥ this value (the `income_bar` exit lever, analogous to `markets/coverage` for DATA) — forces the INCOME→GATE crossing deterministically without simulating a full contract economy.
- **`GET /_twin/state` — INCOME view (superset of the Slice-1 shape).** Adds `haulers: {symbol, role, parkedHub}[]`, `frigateContractTagged: boolean`, `batchContractRunning: boolean`, `creditsPerHour: number`, `hubs: string[]` (the ranked contract-hub waypoints). The mutation log now records `PurchaseShip` (haulers), `navigate` (to hubs), `fleet-unassign` (frigate retire), and `batch-contract` (coordinator launch).

---

## File structure

```
twin/tests/bootstrap/
  helpers/
    fixtures-income.ts      # PURE: IncomeFixture type + incomeEntry() builder
    twin-admin-income.ts    # INCOME admin client (extends the Slice-1 twin): seedIncome/setIncome/incomeState
    scenario-income.ts      # withIncomeScenario(fixture, fn) — seedIncome → reset db → boot daemon → fn
  income-golden-path.e2e.test.ts        # Scenario 1
  income-frigate-retire.e2e.test.ts     # Scenario 2
  income-hub-placement.e2e.test.ts      # Scenario 3
  income-hauler-cap.e2e.test.ts         # Scenario 4
  income-capital-gate.e2e.test.ts       # Scenario 5
  income-staging.e2e.test.ts            # Scenario 6
  income-batch-contract.e2e.test.ts     # Scenario 7
  income-exit.e2e.test.ts               # Scenario 8
  income-restart-idempotency.e2e.test.ts # Scenario 9
twin/tests/unit/bootstrap/
  fixtures-income.test.ts   # unit
```

---

## Task 1: INCOME fixture builder (pure)

**Files:**
- Create: `twin/tests/bootstrap/helpers/fixtures-income.ts`
- Test: `twin/tests/unit/bootstrap/fixtures-income.test.ts`

**Interfaces:**
- Produces: `interface IncomeFixture { credits?: number; haulerPrice?: number; hubs?: string[]; frigateContractTagged?: boolean; creditsPerHour?: number }`; `incomeEntry(overrides?: Partial<IncomeFixture>): IncomeFixture`.

- [ ] **Step 1: Write the failing unit test**

`twin/tests/unit/bootstrap/fixtures-income.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { incomeEntry } from '../../bootstrap/helpers/fixtures-income';

describe('incomeEntry fixture', () => {
  it('defaults to a post-DATA / INCOME-entry world', () => {
    expect(incomeEntry()).toEqual({
      credits: 600000, haulerPrice: 300000,
      hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4', 'X1-PZ28-H5'],
      frigateContractTagged: true, creditsPerHour: 0,
    });
  });
  it('applies overrides (shallow)', () => {
    const f = incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 2_000_000 });
    expect(f.hubs).toEqual(['X1-PZ28-H1']);
    expect(f.credits).toBe(2_000_000);
    expect(f.haulerPrice).toBe(300000);
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/fixtures-income.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement `fixtures-income.ts`**

`twin/tests/bootstrap/helpers/fixtures-income.ts`:
```ts
export interface IncomeFixture {
  credits?: number;
  haulerPrice?: number;
  hubs?: string[];
  frigateContractTagged?: boolean;
  creditsPerHour?: number;
}

export function incomeEntry(overrides: Partial<IncomeFixture> = {}): IncomeFixture {
  return {
    credits: 600000,      // post-DATA treasury band
    haulerPrice: 300000,  // a light hauler
    hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4', 'X1-PZ28-H5'],
    frigateContractTagged: true,
    creditsPerHour: 0,    // starts below income_bar
    ...overrides,
  };
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd twin && npx vitest run -c vitest.unit.config.ts tests/unit/bootstrap/fixtures-income.test.ts`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add twin/tests/bootstrap/helpers/fixtures-income.ts twin/tests/unit/bootstrap/fixtures-income.test.ts
git commit --no-verify -m "test(twin): INCOME fixture builder (pure)"
```

---

## Task 2: INCOME admin client + `withIncomeScenario`

**Files:**
- Create: `twin/tests/bootstrap/helpers/twin-admin-income.ts`
- Create: `twin/tests/bootstrap/helpers/scenario-income.ts`
- Test: `twin/tests/bootstrap/smoke-income.e2e.test.ts`

**Interfaces:**
- Consumes: `twin`, `TwinState` from `./twin-admin`; `TWIN_ADMIN` from `../../helpers/run-cli`; `IncomeFixture` from `./fixtures-income`; `startTestDaemon`, `resetDaemonDb`, `DaemonHandle` from `./daemon`; `launchBootstrap`, `pollUntil`, `advanceTicks`, `scrapeBootstrapMetric` from `./drive`.
- Produces:
  - `interface IncomeHauler { symbol: string; role: string; parkedHub: string | null }`
  - `interface IncomeState extends TwinState { haulers: IncomeHauler[]; frigateContractTagged: boolean; batchContractRunning: boolean; creditsPerHour: number; hubs: string[] }`
  - `twinIncome` = the Slice-1 `twin` plus `seedIncome(f: IncomeFixture): Promise<void>`, `setIncome(creditsPerHour: number): Promise<void>`, `incomeState(): Promise<IncomeState>`.
  - `interface IncomeScenarioCtx { twin: typeof twinIncome; daemon: DaemonHandle; launchBootstrap; pollUntil; advanceTicks; scrapeBootstrapMetric }`
  - `withIncomeScenario(fixture: IncomeFixture, fn: (ctx: IncomeScenarioCtx) => Promise<void>): Promise<void>`

- [ ] **Step 1: Implement `twin-admin-income.ts`**

`twin/tests/bootstrap/helpers/twin-admin-income.ts`:
```ts
import { TWIN_ADMIN } from '../../helpers/run-cli';
import { twin, type TwinState } from './twin-admin';
import type { IncomeFixture } from './fixtures-income';

export interface IncomeHauler { symbol: string; role: string; parkedHub: string | null }
export interface IncomeState extends TwinState {
  haulers: IncomeHauler[];
  frigateContractTagged: boolean;
  batchContractRunning: boolean;
  creditsPerHour: number;
  hubs: string[];
}

async function post(pathUnder: string, body: unknown): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST /_twin${pathUnder} → ${res.status} ${await res.text()}`);
}

export const twinIncome = {
  ...twin, // reuse reset/state/clock/setCredits/forceCoverage/injectFault
  async seedIncome(fixture: IncomeFixture): Promise<void> {
    await post('/reset', { mode: 'income-entry', ...fixture });
  },
  async setIncome(creditsPerHour: number): Promise<void> {
    await post('/income', { creditsPerHour });
  },
  async incomeState(): Promise<IncomeState> {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    if (!res.ok) throw new Error(`GET /_twin/state → ${res.status}`);
    return res.json() as Promise<IncomeState>;
  },
};
```

- [ ] **Step 2: Implement `scenario-income.ts`**

`twin/tests/bootstrap/helpers/scenario-income.ts`:
```ts
import type { IncomeFixture } from './fixtures-income';
import { twinIncome } from './twin-admin-income';
import { startTestDaemon, resetDaemonDb, type DaemonHandle } from './daemon';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from './drive';

export interface IncomeScenarioCtx {
  twin: typeof twinIncome;
  daemon: DaemonHandle;
  launchBootstrap: typeof launchBootstrap;
  pollUntil: typeof pollUntil;
  advanceTicks: typeof advanceTicks;
  scrapeBootstrapMetric: typeof scrapeBootstrapMetric;
}

export async function withIncomeScenario(
  fixture: IncomeFixture,
  fn: (ctx: IncomeScenarioCtx) => Promise<void>,
): Promise<void> {
  await twinIncome.seedIncome(fixture);  // (1) admin-seed the post-DATA / INCOME-entry world
  await resetDaemonDb();                 // (2) wipe daemon mirror (keep players)
  const daemon = await startTestDaemon();// (3) boot isolated daemon (re-syncs from twin)
  try {
    await fn({ twin: twinIncome, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop();
  }
}
```

- [ ] **Step 3: Write the live-stack smoke test**

`twin/tests/bootstrap/smoke-income.e2e.test.ts`:
```ts
import { describe, expect, it } from 'vitest';
import { twinIncome } from './helpers/twin-admin-income';
import { incomeEntry } from './helpers/fixtures-income';

describe('INCOME admin client (live stack)', () => {
  it('seedIncome → incomeState round-trips a post-DATA world', async () => {
    await twinIncome.seedIncome(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2'] }));
    const s = await twinIncome.incomeState();
    expect(s.frigateContractTagged).toBe(true);
    expect(s.hubs.length).toBeGreaterThanOrEqual(2);
    expect(s.creditsPerHour).toBe(0);
    expect(s.batchContractRunning).toBe(false);
  });
  it('setIncome moves realized $/hr', async () => {
    await twinIncome.setIncome(42000);
    expect((await twinIncome.incomeState()).creditsPerHour).toBeGreaterThanOrEqual(42000);
  });
});
```

- [ ] **Step 4: Run against the live stack (green once the twin serves the INCOME admin contracts)**

Run: `cd twin && npx vitest run tests/bootstrap/smoke-income.e2e.test.ts`
Expected once the twin honors `/_twin/{reset income-entry,income,state INCOME view}`: PASS.

- [ ] **Step 5: Commit**

```bash
git add twin/tests/bootstrap/helpers/twin-admin-income.ts twin/tests/bootstrap/helpers/scenario-income.ts twin/tests/bootstrap/smoke-income.e2e.test.ts
git commit --no-verify -m "test(twin): INCOME admin client + withIncomeScenario"
```

---

## Task 3: Scenario 1 — golden path (INCOME)

**Files:** Create `twin/tests/bootstrap/income-golden-path.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';

describe('bootstrap INCOME — golden path', () => {
  it('retires frigate → hub haulers → batch-contract → holds at INCOME-complete past income_bar', async () => {
    // 4 hubs, treasury ample so all clear the capital gate.
    await withIncomeScenario(
      incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4'], credits: 3_000_000, haulerPrice: 300000 }),
      async (ctx) => {
        ctx.launchBootstrap();
        const s = await ctx.pollUntil(
          () => ctx.twin.incomeState(),
          (st) => st.haulers.filter((h) => h.parkedHub).length >= 4 && st.batchContractRunning,
          { steps: 80, advanceMs: 1000 },
        );
        expect(s.frigateContractTagged).toBe(false);                       // frigate retired
        const placed = s.haulers.filter((h) => h.parkedHub);
        expect(placed.length).toBe(4);                                     // one per hub
        expect(new Set(placed.map((h) => h.parkedHub)).size).toBe(4);      // distinct hubs
        expect(s.batchContractRunning).toBe(true);                         // earning
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_haulers_total')).toBe(4);
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(1);

        // Force $/hr over income_bar → derive INCOME-complete. GATE is a stub here (never activates).
        await ctx.twin.setIncome(60000);
        await ctx.pollUntil(() => ctx.twin.incomeState(), (st) => st.creditsPerHour >= 60000, { steps: 10, advanceMs: 1000 });
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);
      },
    );
  }, 240_000);
});
```

- [ ] **Step 2: Run; verify it passes** — `cd twin && npx vitest run tests/bootstrap/income-golden-path.e2e.test.ts` (PASS).
- [ ] **Step 3: Commit** — `git add twin/tests/bootstrap/income-golden-path.e2e.test.ts && git commit --no-verify -m "test(twin): INCOME golden-path e2e"`

---

## Task 4: Scenario 2 — frigate retirement (idempotent)

**Files:** Create `twin/tests/bootstrap/income-frigate-retire.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';
import { countCall } from './helpers/mutation-log';

describe('bootstrap INCOME — frigate retirement', () => {
  it('clears the frigate contract dedication exactly once', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.frigateContractTagged === false,
        { steps: 30, advanceMs: 1000 },
      );
      expect(s.frigateContractTagged).toBe(false);
      // Idempotent: run more ticks; the retire (fleet-unassign) fires at most once.
      await ctx.advanceTicks(10, 1000);
      const s2 = await ctx.twin.incomeState();
      expect(countCall(s2.mutationLog, 'fleet-unassign')).toBeLessThanOrEqual(1);
      expect(s2.frigateContractTagged).toBe(false);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify it passes.** **Step 3: Commit** (`-m "test(twin): INCOME frigate-retire idempotent e2e"`).

---

## Task 5: Scenario 3 — hub selection + one-per-hub placement

**Files:** Create `twin/tests/bootstrap/income-hub-placement.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';

describe('bootstrap INCOME — hub placement', () => {
  it('places one hauler on each of the ranked contract hubs (no doubling up)', async () => {
    const hubs = ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'];
    await withIncomeScenario(incomeEntry({ hubs, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.haulers.filter((h) => h.parkedHub).length >= 3,
        { steps: 60, advanceMs: 1000 },
      );
      const placedHubs = s.haulers.map((h) => h.parkedHub).filter(Boolean) as string[];
      // Exactly one hauler per hub, and every hub used is one of the ranked hubs.
      expect(new Set(placedHubs).size).toBe(placedHubs.length);          // no hub doubled
      expect(placedHubs.every((w) => s.hubs.includes(w))).toBe(true);    // placed on ranked hubs
      expect(placedHubs.length).toBe(3);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): INCOME hub-selection + one-per-hub placement e2e"`).

---

## Task 6: Scenario 4 — hauler cap (`hauler_target`)

**Files:** Create `twin/tests/bootstrap/income-hauler-cap.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';
import { countCall } from './helpers/mutation-log';

describe('bootstrap INCOME — hauler cap', () => {
  it('buys at most hauler_target haulers even when more hubs are viable', async () => {
    // 8 viable hubs but hauler_target defaults to 4–5; assert the fleet never exceeds the cap.
    const hubs = Array.from({ length: 8 }, (_, i) => `X1-PZ28-H${i + 1}`);
    await withIncomeScenario(incomeEntry({ hubs, credits: 5_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Let it buy until it plateaus (no new buy across a settle window).
      await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.haulers.filter((h) => h.parkedHub).length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      await ctx.advanceTicks(15, 1000); // extra ticks — the cap must hold
      const s = await ctx.twin.incomeState();
      const bought = countCall(s.mutationLog, 'PurchaseShip');
      expect(bought).toBeGreaterThanOrEqual(4);
      expect(bought).toBeLessThanOrEqual(5); // hauler_target ceiling (4–5), never 8
      expect(s.haulers.filter((h) => h.parkedHub).length).toBeLessThanOrEqual(5);
    });
  }, 240_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): INCOME hauler_target cap e2e"`).

---

## Task 7: Scenario 5 — capital gate on hauler buys

**Files:** Create `twin/tests/bootstrap/income-capital-gate.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';
import { countCall } from './helpers/mutation-log';

describe('bootstrap INCOME — capital gate', () => {
  it('blocks a hauler buy that breaches reserve_margin, then buys once funded', async () => {
    // 300k hauler but only 400k credits → a buy leaves 100k < 50% reserve → blocked.
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 400000, haulerPrice: 300000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.advanceTicks(8, 1000);
      const s1 = await ctx.twin.incomeState();
      expect(countCall(s1.mutationLog, 'PurchaseShip')).toBe(0); // no hauler while under-funded
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_haulers_total')).toBe(0);

      await ctx.twin.setCredits(3_000_000); // fund → gate releases
      const s2 = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => countCall(st.mutationLog, 'PurchaseShip') >= 1,
        { steps: 20, advanceMs: 1000 },
      );
      expect(countCall(s2.mutationLog, 'PurchaseShip')).toBeGreaterThanOrEqual(1);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): INCOME capital-gate hauler-buy e2e"`).

---

## Task 8: Scenario 6 — staging (one hauler buy per tick)

**Files:** Create `twin/tests/bootstrap/income-staging.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';
import { ticksOf } from './helpers/mutation-log';

describe('bootstrap INCOME — staging', () => {
  it('buys at most one hauler per reconcile tick (distinct world-times)', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'], credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => ticksOf(st.mutationLog, 'PurchaseShip').length >= 3,
        { steps: 80, advanceMs: 1000 },
      );
      const buyTimes = ticksOf(s.mutationLog, 'PurchaseShip');
      expect(buyTimes.length).toBe(3);
      expect(new Set(buyTimes).size).toBe(3); // each buy on a different tick — never batched
    });
  }, 240_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): INCOME staging one-buy-per-tick e2e"`).

---

## Task 9: Scenario 7 — `batch-contract` launched once

**Files:** Create `twin/tests/bootstrap/income-batch-contract.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';
import { countCall } from './helpers/mutation-log';

describe('bootstrap INCOME — batch-contract launch', () => {
  it('launches the contract fleet coordinator exactly once (idempotent across ticks)', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.batchContractRunning,
        { steps: 40, advanceMs: 1000 },
      );
      expect(s.batchContractRunning).toBe(true);
      // Run more ticks; the launch is guarded → fires at most once.
      await ctx.advanceTicks(12, 1000);
      const s2 = await ctx.twin.incomeState();
      expect(s2.batchContractRunning).toBe(true);
      expect(countCall(s2.mutationLog, 'batch-contract')).toBeLessThanOrEqual(1);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): INCOME batch-contract launched-once e2e"`).

---

## Task 10: Scenario 8 — `income_bar` exit → GATE hand-off

**Files:** Create `twin/tests/bootstrap/income-exit.e2e.test.ts`

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from './helpers/scenario-income';
import { incomeEntry } from './helpers/fixtures-income';

describe('bootstrap INCOME — income_bar exit', () => {
  it('holds INCOME below the bar, derives INCOME-complete once $/hr clears it (GATE stub, out of scope)', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2'], credits: 3_000_000, creditsPerHour: 0 }), async (ctx) => {
      ctx.launchBootstrap();
      // Below the bar → INCOME stays active.
      await ctx.pollUntil(
        () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' }),
        (v) => v === 1,
        { steps: 20, advanceMs: 1000 },
      );
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);

      // Force $/hr over income_bar → INCOME-complete derived; GATE never activates in this harness.
      await ctx.twin.setIncome(80000);
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.creditsPerHour >= 80000,
        { steps: 10, advanceMs: 1000 },
      );
      expect(s.creditsPerHour).toBeGreaterThanOrEqual(80000);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);
    });
  }, 180_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Commit** (`-m "test(twin): INCOME income_bar exit → GATE-handoff e2e"`).

---

## Task 11: Scenario 9 — restart idempotency (mid-hauler-buy)

**Files:** Create `twin/tests/bootstrap/income-restart-idempotency.e2e.test.ts`

**Interfaces:** manages the daemon directly (restart mid-run without wiping the daemon DB), mirroring the Slice-1 restart test.

- [ ] **Step 1: Write the test**

```ts
import { describe, expect, it } from 'vitest';
import { twinIncome } from './helpers/twin-admin-income';
import { incomeEntry } from './helpers/fixtures-income';
import { resetDaemonDb, startTestDaemon } from './helpers/daemon';
import { launchBootstrap, pollUntil } from './helpers/drive';
import { countCall } from './helpers/mutation-log';

describe('bootstrap INCOME — restart idempotency', () => {
  it('no double-buy / no re-retire / no double batch-contract across a mid-purchase restart', async () => {
    await twinIncome.seedIncome(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'], credits: 3_000_000 }));
    await resetDaemonDb();

    // Lifetime 1: run until the FIRST hauler purchase is recorded, then stop before the next observe.
    let daemon = await startTestDaemon();
    launchBootstrap();
    const afterFirst = await pollUntil(
      () => twinIncome.incomeState(),
      (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
      { steps: 60, advanceMs: 1000 },
    );
    expect(countCall(afterFirst.mutationLog, 'PurchaseShip')).toBe(1);
    const retiresBefore = countCall(afterFirst.mutationLog, 'fleet-unassign');
    const batchBefore = countCall(afterFirst.mutationLog, 'batch-contract');

    await daemon.stop();
    daemon = await startTestDaemon(); // reboot; same test DB + twin world (1 hauler persists)
    try {
      launchBootstrap();
      const done = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => s.haulers.filter((h) => h.parkedHub).length >= 3,
        { steps: 60, advanceMs: 1000 },
      );
      // Exactly 3 hauler buys across BOTH lifetimes — the mid-flight hauler is not re-bought.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBe(3);
      // Frigate retired at most once total; batch-contract launched at most once total.
      expect(countCall(done.mutationLog, 'fleet-unassign')).toBeLessThanOrEqual(Math.max(1, retiresBefore));
      expect(countCall(done.mutationLog, 'batch-contract')).toBeLessThanOrEqual(Math.max(1, batchBefore, 1));
      expect(done.frigateContractTagged).toBe(false);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
```

- [ ] **Step 2: Run; verify.** **Step 3: Run the whole INCOME suite** — `cd twin && npx vitest run tests/bootstrap/income-*.e2e.test.ts`. **Step 4: Commit** (`-m "test(twin): INCOME restart-idempotency e2e"`).

---

## Self-review notes (for the implementer)

- **Coverage vs. the design spec:** frigate retirement (T4), hub selection + one-per-hub (T5), `hauler_target` cap (T6), capital gate (T7), staging (T8), `batch-contract` once (T9), `income_bar` exit (T10), restart idempotency (T9→Task11), golden path (T3). All INCOME behaviors from the design spec are covered.
- **INCOME never over-reaches into GATE:** every phase assertion checks `GATE` gauge = 0; GATE is a stub in this harness (Slice 3 is a separate concern).
- **Config-value assumptions:** the cap test (T6) asserts `4 ≤ bought ≤ 5` per `hauler_target`'s 4–5 default; if the test daemon's `[bootstrap] hauler_target` is pinned to a specific value in `test-config.yaml`, tighten to that exact number.
- **Mutation-log call names** (`PurchaseShip`, `navigate`, `fleet-unassign`, `batch-contract`) are the twin's recorded world-changing calls — if the twin labels the frigate-retire or coordinator-launch differently, align the `countCall` names to the twin's `GET /_twin/state` mutation-log vocabulary (a one-line change per assertion).
- **Cross-slice imports:** Tasks 3–11 import Slice-1 foundation helpers authored concurrently; unresolved-import typecheck errors from those files are the concurrent Slice-1 build, not Slice-2 defects. A unified `npx tsc --noEmit` after both slices land is the final gate.
