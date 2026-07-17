# Galaxy Declutter + Ambience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fleet-scale declutter (top-N artery lanes, quiet paths), light/heavy hauler silhouettes with thruster flares, live fill ticker, shared hover tooltip (system + lane cards), and schedule-drift ticks.

**Architecture:** One tiny additive gobot field (`Leg.travelSeconds` — powers drift). Server: `/api/flows/fills` + per-lane goods rollup (`topGoods`). Web: wire (types/api/store/mocks incl. a dense hairball fixture), lane partition + path quieting, new hull glyphs + drift, tooltip + ticker widgets.

**Tech Stack:** Go (stdlib tests) · Express + pg + vitest/supertest · React 18 + react-konva + zustand + vitest.

**Spec:** `docs/superpowers/specs/2026-07-17-galaxy-declutter-and-ambience-design.md`. Repo root: `/Users/andres.dandrea/IdeaProjects/cities/spacetraders`.

**Spec deviation (approved at plan stage):** §6.5's "sum of planned travelSeconds for completed hops" is not computable from the wire — the feed carries only *remaining* hops and `plannedAt` refreshes at each leg publish. Drift is therefore measured on the **current leg**: `drift = arrivesAt − (plannedAt + currentLeg.travelSeconds)`, where `travelSeconds` is a new additive field on `flowfeed.Leg` (Task 1). Same amber/red semantics; flows without the field never show a glyph.

## Global Constraints

- Prefix shell commands with `rtk`. **rtk traps:** `rtk npx X` → rewritten to `npm run X` (Missing-script) — always `rtk proxy npx vitest run` / `rtk proxy npx tsc --noEmit`; compacted `git log` hides merge commits — use `rtk proxy git log` when checking merges; missing `node_modules` → `rtk proxy npm ci` first (gitignored).
- Task tracking via `bd` only; the orchestrator creates the bead — commits reference it as `(sp-XXXX)`.
- Web colors only from `NOIR` tokens (`web/src/theme/noir.ts`); computed rgb()/rgba() from those tokens is fine (see `noirAlpha`, `freshnessColor`).
- The flowfeed JSON contract is **additive-only**; struct field order = JSON order — never reorder existing fields.
- Files have drifted across three merged features: **Read before editing**; the plan gives exact code for new units and precise directives for edits to evolved files (`FlowGalaxyScene.tsx`, `FlowShipLayer.tsx`, `FlowPlanPath.tsx`, `flowStore.ts`, `useFlowsPolling.ts`, `routes/flows.ts`, `laneAggregation.ts`).
- Named constants for every tunable: `LANE_EMPHASIS_N = 12`, `LANE_FLOOR_PCT = 0.02`, `HEAVY_HAULER_MIN_CAPACITY = 80`, `DRIFT_AMBER_SECONDS = 300`, `DRIFT_RED_SECONDS = 900`, `FILLS_INTERVAL_MS = 15000`.
- Every commit message ends with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

## File Structure

- **T1 (gobot):** Modify `gobot/internal/adapters/flowfeed/registry.go` (Leg), `gobot/internal/application/trading/commands/flow_publish.go` (+test).
- **T2 (server):** Modify `visualizer/server/routes/flows.ts` (fills route; lanes SELECTs gain goods), `visualizer/server/utils/laneAggregation.ts` (goods accumulation + topGoods); Create `visualizer/server/utils/fills.ts` + tests `routes/__tests__/fills.test.ts`, `routes/__tests__/flows.fills.test.ts`; extend `routes/__tests__/laneAggregation.test.ts`.
- **T3 (web wire):** Modify `web/src/types/flows.ts`, `web/src/services/api/flows.ts`, `web/src/hooks/useFlowsPolling.ts`, `web/src/store/flowStore.ts` (+test), `web/src/mocks/mockFlows.ts`, `web/src/services/api/mockClient.ts`.
- **T4 (declutter):** Modify `web/src/components/flows/flowGeometry.ts` (+test: `partitionLanes`), `web/src/components/flows/FlowLaneLayer.tsx`, `web/src/components/flows/FlowPlanPath.tsx`, `web/src/components/flows/FlowGalaxyScene.tsx` (paths quiet/bright + lane hover wiring).
- **T5 (hulls+drift):** Modify `web/src/components/flows/flowMotion.ts` (+test: drift), `web/src/components/flows/FlowShipLayer.tsx` (silhouettes + drift tick), `web/src/components/flows/TourRoster.tsx` (+test: drift suffix). Create `web/src/components/flows/__tests__/hullClass.test.ts` is NOT needed — classification lives in flowMotion tests.
- **T6 (widgets):** Create `web/src/components/flows/FlowTooltip.tsx` (+test), `web/src/components/flows/FillTicker.tsx` (+test); Modify `web/src/pages/TradeFlowsView.tsx`, `web/src/components/flows/FlowGalaxyScene.tsx` (node hover → tooltip).
- **T7:** on-screen verification (both scenarios).

Web paths are rooted at `visualizer/web/` in the repo.

---

### Task 1: gobot — `Leg.travelSeconds` (drift anchor)

**Files:** Modify `gobot/internal/adapters/flowfeed/registry.go:52-57`, `gobot/internal/application/trading/commands/flow_publish.go` (buildTourFlow), `flow_publish_test.go`; extend the flowfeed golden-payload test if it asserts exact JSON.

**Interfaces — Produces:** `Leg` JSON gains `"travelSeconds": int` after `arrivesAt` (0 = unknown; arb/trade-route legs stay 0). `buildTourFlow` maps `plan.Legs[currentLegIdx].TravelSecondsFromPrev` into it.

- [ ] **Step 1 — failing test** (append to `flow_publish_test.go`; the fixture's legs already carry `TravelSecondsFromPrev` 0/420/300 from sp-gl06):

```go
func TestBuildTourFlow_CurrentLegCarriesPlannedTravelSeconds(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9"}
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	arrives := now.Add(7 * time.Minute)

	f := buildTourFlow(cmd, tourPlanFixture(), 1, arrives, nil, now)

	if f.CurrentLeg == nil {
		t.Fatal("leg 1 in progress must yield a currentLeg")
	}
	if f.CurrentLeg.TravelSeconds != 420 {
		t.Errorf("currentLeg.travelSeconds = %d, want 420 (leg 1's TravelSecondsFromPrev)", f.CurrentLeg.TravelSeconds)
	}
}
```

- [ ] **Step 2** — run `rtk go test ./internal/application/trading/commands/ -run TestBuildTourFlow_CurrentLegCarries -v` → compile FAIL (`TravelSeconds` undefined).
- [ ] **Step 3 — implement.** `registry.go` Leg gains one line after `ArrivesAt` (additive, do not touch other fields):

```go
	// TravelSeconds is the planner's projected duration for THIS leg (0 = no
	// plan-time estimate). With PlannedAt (stamped at leg-start publish) it
	// anchors the galaxy view's schedule-drift glyph.
	TravelSeconds int `json:"travelSeconds"`
```

In `buildTourFlow`, the `currentLeg` literal gains `TravelSeconds: plan.Legs[currentLegIdx].TravelSecondsFromPrev,`.
- [ ] **Step 4** — `rtk go test ./internal/application/trading/commands/ -run 'TestBuild' && rtk go test ./internal/adapters/flowfeed/...` → PASS (extend the golden-payload test's expected leg JSON with `"travelSeconds":0`/actual value if it asserts bytes). `rtk go build ./...` clean.
- [ ] **Step 5 — commit** `feat(flowfeed): currentLeg carries planned travelSeconds — drift anchor (sp-XXXX)`.

---

### Task 2: server — `/api/flows/fills` + lanes `topGoods`

**Files:** Create `visualizer/server/utils/fills.ts`, tests `routes/__tests__/fills.test.ts` + `routes/__tests__/flows.fills.test.ts`; Modify `visualizer/server/utils/laneAggregation.ts` (+its test), `visualizer/server/routes/flows.ts` (fills route; lanes SELECTs gain `good`/`good_symbol`).

**Interfaces — Produces:**
- `GET /api/flows/fills?limit=30` → `{ fills: FillRecord[], generatedAt }`, `FillRecord = { id, at, ship, good, isBuy, units, credits, waypoint }`, desc by `at`, limit capped 100. Telemetry rows: `id = "t-"+row id`, credits = `(isBuy ? -1 : +1) × units × unitPrice`. Arb rows: `id = "a-"+row id`, `isBuy: false`, credits = `actual_net_profit`, waypoint = `sell_market`.
- `LaneRecord` gains `goods: Record<string, number>` (per-good signed credits, internal accumulation, additive on the wire); system-lane records additionally gain `topGoods: { good: string; credits: number }[]` (top 3 by |credits|).

- [ ] **Step 1 — failing util tests.** `fills.test.ts` — `mergeFills(telemetryRows, arbRows, limit)`:

```ts
import { describe, it, expect } from 'vitest';
import { mergeFills } from '../../utils/fills.js';

describe('mergeFills', () => {
  const tele = [
    { id: 11, ship_symbol: 'SHIP-1', good: 'IRON', is_buy: true, realized_units: 40, realized_unit_price: 30, waypoint: 'X1-AA-P1', realized_at: '2026-07-17T12:00:00Z' },
    { id: 12, ship_symbol: 'SHIP-1', good: 'IRON', is_buy: false, realized_units: 40, realized_unit_price: 55, waypoint: 'X1-BB-Q2', realized_at: '2026-07-17T12:10:00Z' },
  ];
  const arb = [
    { id: 7, ship_symbol: 'SHIP-2', good_symbol: 'FUEL', units_sold: 20, actual_net_profit: 900, sell_market: 'X1-CC-R3', executed_at: '2026-07-17T12:05:00Z' },
  ];

  it('merges both sources desc by time with stable ids and signed credits', () => {
    const fills = mergeFills(tele, arb, 30);
    expect(fills.map((f) => f.id)).toEqual(['t-12', 'a-7', 't-11']);
    expect(fills[0]).toMatchObject({ ship: 'SHIP-1', good: 'IRON', isBuy: false, units: 40, credits: 2200, waypoint: 'X1-BB-Q2' });
    expect(fills[1]).toMatchObject({ ship: 'SHIP-2', good: 'FUEL', isBuy: false, credits: 900, waypoint: 'X1-CC-R3' });
    expect(fills[2].credits).toBe(-1200); // buy: negative
  });

  it('applies the limit after merging and skips malformed rows', () => {
    expect(mergeFills(tele, arb, 2)).toHaveLength(2);
    expect(mergeFills([{ id: 1 } as any], [], 10)).toEqual([]);
  });
});
```

Append to `laneAggregation.test.ts` (existing suites untouched):

```ts
describe('goods accumulation + topGoods', () => {
  it('waypoint lanes accumulate per-good signed credits', () => {
    const tele = [
      { tourId: 'T', shipSymbol: 'S', legIndex: 0, waypoint: 'X1-AA-P1', isBuy: true, realizedUnits: 10, realizedUnitPrice: 100, realizedAt: '2026-07-17T12:00:00Z', good: 'IRON' },
      { tourId: 'T', shipSymbol: 'S', legIndex: 1, waypoint: 'X1-BB-Q2', isBuy: false, realizedUnits: 10, realizedUnitPrice: 180, realizedAt: '2026-07-17T12:05:00Z', good: 'IRON' },
    ];
    const lanes = aggregateLanes(tele as any, [], Date.parse('2026-07-17T11:00:00Z'), Date.parse('2026-07-17T13:00:00Z'));
    expect(lanes[0].goods).toEqual({ IRON: 1800 });
  });

  it('system rollup merges goods and emits top-3 topGoods by |credits|', () => {
    const lanes = [
      { from: 'X1-AA-P1', to: 'X1-BB-Q2', realizedUnits: 1, realizedProfit: 100, legCount: 1, goods: { IRON: 900, GOLD: -50 } },
      { from: 'X1-AA-P9', to: 'X1-BB-Q3', realizedUnits: 1, realizedProfit: 100, legCount: 1, goods: { IRON: 100, FUEL: 300, ALUM: 10, COPPER: 5 } },
    ];
    const sys = rollupSystemLanes(lanes as any);
    expect(sys[0].topGoods).toEqual([
      { good: 'IRON', credits: 1000 },
      { good: 'FUEL', credits: 300 },
      { good: 'GOLD', credits: -50 },
    ]);
  });
});
```

- [ ] **Step 2** — run both files → FAIL (no `mergeFills`; no `goods`).
- [ ] **Step 3 — implement.**

`utils/fills.ts`:

```ts
export interface FillRecord {
  id: string;
  at: string; // ISO
  ship: string;
  good: string;
  isBuy: boolean;
  units: number;
  credits: number; // signed: sells +, buys −; arb rows carry net profit
  waypoint: string;
}

// Merge realized tour-leg trades with arb executions into one desc-by-time
// fills stream. Malformed rows are skipped; ids are stable per source row so
// the client can dedupe across polls.
export function mergeFills(
  telemetryRows: any[],
  arbRows: any[],
  limit: number,
): FillRecord[] {
  const out: FillRecord[] = [];
  for (const r of telemetryRows) {
    const at = r?.realized_at ? Date.parse(String(r.realized_at)) : NaN;
    const units = Number(r?.realized_units);
    const price = Number(r?.realized_unit_price);
    if (!r?.ship_symbol || !r?.good || Number.isNaN(at) || !Number.isFinite(units) || !Number.isFinite(price)) continue;
    const isBuy = Boolean(r.is_buy);
    out.push({
      id: `t-${r.id}`,
      at: new Date(at).toISOString(),
      ship: r.ship_symbol,
      good: r.good,
      isBuy,
      units,
      credits: (isBuy ? -1 : 1) * units * price,
      waypoint: r.waypoint ?? '',
    });
  }
  for (const r of arbRows) {
    const at = r?.executed_at ? Date.parse(String(r.executed_at)) : NaN;
    if (!r?.ship_symbol || !r?.good_symbol || Number.isNaN(at)) continue;
    out.push({
      id: `a-${r.id}`,
      at: new Date(at).toISOString(),
      ship: r.ship_symbol,
      good: r.good_symbol,
      isBuy: false,
      units: Number(r.units_sold) || 0,
      credits: Number(r.actual_net_profit) || 0,
      waypoint: r.sell_market ?? '',
    });
  }
  out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
  return out.slice(0, limit);
}
```

`laneAggregation.ts` directives: `TelemetryRow` gains `good: string`; `ArbRow` gains `goodSymbol: string`; `LaneRecord` gains `goods: Record<string, number>`; `bump(...)` gains a `goodsDelta: Record<string, number>` param merged into `rec.goods`; the telemetry per-leg fold tracks `goodsByLeg` (per leg\_index, per good signed value) and passes the destination leg's map to `bump`; the arb fold passes `{ [a.goodSymbol]: a.actualNetProfit }`. `rollupSystemLanes` merges `goods` across folded lanes and sets `topGoods` = top 3 entries by `|credits|` (`SystemLaneRecord = LaneRecord & { topGoods: ... }` type exported). `rollupSystemActivity` unchanged.

`routes/flows.ts` directives: lanes handler's telemetry SELECT adds `good`; arb SELECT adds `good_symbol`; the row mappers add `good: r.good` / `goodSymbol: r.good_symbol`. **Also in the `/live` handler** (hauler split, spec §5): the ships SELECT adds `cargo_capacity` (PG `ships.cargo_capacity`, models.go:117) and the `shipNav` mapper adds `cargoCapacity: nav.cargo_capacity !== null && nav.cargo_capacity !== undefined ? Number(nav.cargo_capacity) : null,` — cover it with one assertion added to `flows.live.realized.test.ts`'s transit-columns test (ship row gains `cargo_capacity: '120'`, expect `shipNav.cargoCapacity === 120`). Append the fills route before `export default`:

```ts
// ---- GET /api/flows/fills?limit=30 -------------------------------------------
// Recent realized trades (tour-leg fills + arb executions), newest first — the
// galaxy view's ambient ticker. Read-only, tiny LIMIT, no window param.
router.get('/fills', async (req, res) => {
  const limit = Math.min(100, Math.max(1, Number(req.query.limit) || 30));
  let client;
  try {
    client = await pool.connect();
    const tele = await client.query(`
      SELECT id, ship_symbol, good, is_buy, realized_units, realized_unit_price, waypoint, realized_at
      FROM tour_leg_telemetry
      WHERE realized_at IS NOT NULL
      ORDER BY realized_at DESC
      LIMIT $1
    `, [limit]);
    const arb = await client.query(`
      SELECT id, ship_symbol, good_symbol, units_sold, actual_net_profit, sell_market, executed_at
      FROM arbitrage_execution_logs
      WHERE success = true
      ORDER BY executed_at DESC
      LIMIT $1
    `, [limit]);
    res.json({ fills: mergeFills(tele.rows, arb.rows, limit), generatedAt: new Date().toISOString() });
  } catch (error: any) {
    console.error('Failed to build flows fills:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    if (client) client.release();
  }
});
```

(import `mergeFills` at top). `flows.fills.test.ts`: same pg-mock idiom as siblings — happy path (two mocked queries → merged body, `LIMIT $1` params) + 503 on connect failure.

- [ ] **Step 4** — full server suite green (`rtk proxy npx vitest run`); existing lanes tests must still pass (goods field is additive — if a deep-equal breaks, extend its expectation).
- [ ] **Step 5 — commit** `feat(viz-server): /api/flows/fills ticker feed + per-lane goods rollup with topGoods (sp-XXXX)`.

---

### Task 3: web wire — types, api, polling, store, mocks (incl. dense fixture)

**Files:** Modify `web/src/types/flows.ts`, `web/src/services/api/flows.ts`, `web/src/hooks/useFlowsPolling.ts`, `web/src/store/flowStore.ts` (+`__tests__/flowStore.test.ts`), `web/src/mocks/mockFlows.ts`, `web/src/services/api/mockClient.ts`.

**Interfaces — Produces (T4/5/6 consume):**
- Types: `FlowLeg` gains `travelSeconds: number`; `FlowShipNav` gains `cargoCapacity: number | null`; `LaneRecord` gains `goods?: Record<string, number>`; new `SystemLaneRecord = LaneRecord & { topGoods: { good: string; credits: number }[] }` and `LanesResponse.systemLanes: SystemLaneRecord[]`; new `FillRecord`/`FillsResponse` mirroring the server; new `TooltipState = { kind: 'system' | 'lane'; key: string; x: number; y: number } | null`.
- Store: `fills: FillsResponse | null`, `setFills(f)`; `tooltip: TooltipState`, `setTooltip(t: TooltipState)`; polling adds a 15s fills effect (failure = silent skip, no setError, no counter).
- Mocks: `mockFills(nowMs)`; drift-carrying legs on flows A (amber ≈ 6.8m) and C (red ≈ 20m); capacities A=120/D=80 (heavy), B=40/C=40 (light); `VITE_MOCK_DENSE=1` serves a dense generated galaxy.

- [ ] **Step 1 — types + api + polling** (mechanical, exact fields above; `getFlowFills()` mirrors siblings; `FILLS_INTERVAL_MS = 15000` effect calls `setFills` on success, silent catch).
- [ ] **Step 2 — failing store test** (append):

```ts
describe('fills + tooltip state', () => {
  it('stores fills and tooltip round-trips', () => {
    const s = useFlowStore.getState();
    s.setFills({ fills: [{ id: 't-1', at: 'x', ship: 'S', good: 'IRON', isBuy: false, units: 1, credits: 5, waypoint: 'W' }], generatedAt: 'x' });
    expect(useFlowStore.getState().fills?.fills[0].id).toBe('t-1');
    s.setTooltip({ kind: 'system', key: 'X1-AA', x: 10, y: 20 });
    expect(useFlowStore.getState().tooltip?.key).toBe('X1-AA');
    s.setTooltip(null);
    expect(useFlowStore.getState().tooltip).toBeNull();
  });
});
```

Run → FAIL; implement (`fills: null`, `tooltip: null`, `setFills: (fills) => set({ fills })`, `setTooltip: (tooltip) => set({ tooltip })`); green.
- [ ] **Step 3 — mocks.**
  - `mockLiveFlows`: Tour A's `currentLeg` gains `travelSeconds: 180` and `plannedAt` moved to `nowMs − 500_000` (drift = (nowMs+90s) − (plannedAt+180s) ≈ 410s → amber). Arb C's leg gains `travelSeconds: 200` with `plannedAt = nowMs − 1_300_000` (drift ≈ 1200s → red). B and D legs get `travelSeconds: 0` (no glyph). `shipNav.cargoCapacity`: A 120, B 40, C 40, D 80.
  - All other `FlowLeg`/`FlowShipNav` literals across mocks/tests: sweep via `rtk proxy npx tsc --noEmit` and add `travelSeconds: 0` / `cargoCapacity: null`.
  - `mockLanes`: each base lane gains a small hand-written `goods` map; `systemLanes` entries gain `topGoods` (2-3 entries each, consistent with the goods maps).
  - New `mockFills(nowMs)`: 8 entries alternating sells/buys across the 4 demo ships, timestamps descending from `nowMs − 30s` in ~45s steps, ids `t-1…`/`a-…`.
  - New `mockDenseGalaxy()`: deterministic (mulberry-style hash, NO Math.random) ~40 systems on a jittered ring + 2 inner clusters, chain + chord gate edges, ~30 system lanes with profits log-spread 5M → 20k (so exactly 12 clear the artery cut and several fall under the 2% floor), plus ~10 live flows with legs/paths. Export `{ topology, lanes, live }`.
  - `mockClient.ts`: when `import.meta.env.VITE_MOCK_DENSE === '1'`, `/flows/topology`, `/flows/lanes`, `/flows/live` serve the dense variants; `/flows/fills` returns `mockFills(Date.now())` in both modes; freshness unchanged (dense mode may return empty systems).
- [ ] **Step 4** — `rtk proxy npx tsc --noEmit && rtk proxy npx vitest run` green (update mock-shape suites the new fields break, minimally).
- [ ] **Step 5 — commit** `feat(viz-web): wire — fills/tooltip state, drift+capacity mocks, dense hairball fixture (sp-XXXX)`.

---

### Task 4: declutter — lane partition + path quieting + artery hover

**Files:** Modify `web/src/components/flows/flowGeometry.ts` (+`__tests__/flowGeometry.test.ts`), `FlowLaneLayer.tsx`, `FlowPlanPath.tsx`, `FlowGalaxyScene.tsx`.

**Interfaces:** Consumes `SystemLaneRecord` (T3), store `setTooltip`, `hoveredFlowId`/`selectedFlowId`. Produces `partitionLanes(records, n, floorPct): { arteries: SystemLaneRecord[]; capillaries: SystemLaneRecord[] }`; `FlowPlanPath` gains prop `bright: boolean`; `FlowLaneLayer` gains prop `onLaneHover(key: string | null, x: number, y: number)` (key = `"from→to"`).

- [ ] **Step 1 — failing partition tests** (append to `flowGeometry.test.ts`):

```ts
import { partitionLanes, LANE_EMPHASIS_N, LANE_FLOOR_PCT } from '../flowGeometry';

describe('partitionLanes', () => {
  const lane = (from: string, profit: number) => ({ from, to: 'X1-ZZ', realizedUnits: 1, realizedProfit: profit, legCount: 1, topGoods: [] });

  it('splits top-N arteries, floor-passing capillaries, and drops sub-floor lanes', () => {
    const records = [
      ...Array.from({ length: 15 }, (_, i) => lane(`A${i}`, 1_000_000 - i * 50_000)),
      lane('TINY', 1_000_000 * LANE_FLOOR_PCT * 0.5), // below floor -> dropped
      lane('LOSS', -400_000),                          // big loss ranks by magnitude
    ];
    const { arteries, capillaries } = partitionLanes(records, LANE_EMPHASIS_N, LANE_FLOOR_PCT);
    expect(arteries).toHaveLength(LANE_EMPHASIS_N);
    expect(arteries.some((l) => l.from === 'LOSS')).toBe(true);
    expect(capillaries.some((l) => l.from === 'TINY')).toBe(false);
    expect(arteries.length + capillaries.length).toBe(16); // 17 minus the dropped TINY
  });

  it('small sets are all arteries; empty is empty', () => {
    const { arteries, capillaries } = partitionLanes([lane('A', 100)], LANE_EMPHASIS_N, LANE_FLOOR_PCT);
    expect(arteries).toHaveLength(1);
    expect(capillaries).toHaveLength(0);
    expect(partitionLanes([], LANE_EMPHASIS_N, LANE_FLOOR_PCT)).toEqual({ arteries: [], capillaries: [] });
  });
});
```

- [ ] **Step 2 — implement `partitionLanes`** in `flowGeometry.ts` (with the two exported constants): sort a copy by `|realizedProfit|` desc; floor = `|records[0].realizedProfit| × floorPct` (after sorting); drop below-floor; first `n` are arteries, rest capillaries. Green.
- [ ] **Step 3 — `FlowLaneLayer`**: partition once per render; arteries keep the existing full treatment PLUS an invisible wide hit `Line` (strokeWidth `10/scale`, `opacity 0`, `listening`) whose `onMouseEnter/MouseMove` calls `onLaneHover("from→to", pointerX, pointerY)` (stage pointer position → client coords via `stage.container().getBoundingClientRect()`) and `onMouseLeave` calls `onLaneHover(null, 0, 0)`; capillaries render as one solid `Line` each — `strokeWidth Math.max(0.3, 0.5/scale)`, `noirAlpha(profitColor, 0.25)`, no dash, no Arrow, `listening={false}`.
- [ ] **Step 4 — path quieting.** `FlowPlanPath` gains `bright: boolean`: when false, every segment renders thin static-dashed (`strokeWidth Math.max(0.3, 0.6*u)`, alpha 0.25 — deadheads 0.15, dash `[2u, 4u]`, no gradient) and hop markers + anchor ring are skipped; when true, exactly today's rendering. In `FlowGalaxyScene`, the presence wrapper passes `bright={p.flow.containerId === hoveredFlowId || p.flow.containerId === selectedFlowId}` (read both from the store) — keep the presence/stale opacity multiplication as-is.
- [ ] **Step 5 — scene wiring**: `FlowLaneLayer` gets `onLaneHover={(key, x, y) => setTooltip(key ? { kind: 'lane', key, x, y } : null)}`.
- [ ] **Step 6** — `rtk proxy npx tsc --noEmit && rtk proxy npx vitest run` green. **Commit** `feat(viz-web): fleet-scale declutter — top-N artery lanes with hover targets, whisper paths (sp-XXXX)`.

---

### Task 5: hulls + drift

**Files:** Modify `web/src/components/flows/flowMotion.ts` (+`__tests__/flowMotion.test.ts`), `FlowShipLayer.tsx`, `TourRoster.tsx` (+`__tests__/TourRoster.test.tsx`).

**Interfaces:** Produces `isHeavyHauler(nav): boolean` (`HEAVY_HAULER_MIN_CAPACITY = 80`; null capacity ⇒ false) and `scheduleDriftSeconds(flow, nowMs): number | null` in `flowMotion.ts`; `DRIFT_AMBER_SECONDS = 300`, `DRIFT_RED_SECONDS = 900` exported.

- [ ] **Step 1 — failing tests** (append to `flowMotion.test.ts`, reusing its `flow`/`nav`/`iso` helpers):

```ts
describe('isHeavyHauler', () => {
  it('classifies by cargo capacity with 80 as the heavy floor', () => {
    expect(isHeavyHauler(nav({ cargoCapacity: 120 }))).toBe(true);
    expect(isHeavyHauler(nav({ cargoCapacity: 80 }))).toBe(true);
    expect(isHeavyHauler(nav({ cargoCapacity: 40 }))).toBe(false);
    expect(isHeavyHauler(nav({ cargoCapacity: null }))).toBe(false);
    expect(isHeavyHauler(null)).toBe(false);
  });
});

describe('scheduleDriftSeconds', () => {
  it('positive drift when the leg arrives later than planned', () => {
    const f = flow({
      plannedAt: iso(-500),
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-500), arrivesAt: iso(90), travelSeconds: 180 },
    });
    expect(scheduleDriftSeconds(f, NOW)).toBeCloseTo(410, 0);
  });
  it('clamps ahead-of-schedule to 0 and returns null without a plan anchor', () => {
    const onTime = flow({
      plannedAt: iso(-100),
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-100), arrivesAt: iso(50), travelSeconds: 300 },
    });
    expect(scheduleDriftSeconds(onTime, NOW)).toBe(0);
    expect(scheduleDriftSeconds(flow({ currentLeg: null }), NOW)).toBeNull();
    const noEstimate = flow({
      currentLeg: { from: 'A', to: 'B', departedAt: iso(-10), arrivesAt: iso(10), travelSeconds: 0 },
    });
    expect(scheduleDriftSeconds(noEstimate, NOW)).toBeNull();
  });
});
```

(The `flow` fixture's `FlowLeg` literals need `travelSeconds` added — part of the T3 sweep, but verify.)
- [ ] **Step 2 — implement** in `flowMotion.ts`:

```ts
export const HEAVY_HAULER_MIN_CAPACITY = 80;
export const DRIFT_AMBER_SECONDS = 300;
export const DRIFT_RED_SECONDS = 900;

// Heavy = big freighter hull (silhouette split). Unknown capacity reads light.
export function isHeavyHauler(nav: LiveFlow['shipNav']): boolean {
  return Boolean(nav && nav.cargoCapacity !== null && nav.cargoCapacity >= HEAVY_HAULER_MIN_CAPACITY);
}

// Current-leg schedule drift: actual arrival vs (plannedAt + planned leg
// seconds). Positive = behind plan; ahead clamps to 0; null = no estimate
// (no glyph — silence is nominal). plannedAt is stamped at leg-start publish,
// so this measures exactly the leg the hull is flying.
export function scheduleDriftSeconds(flow: LiveFlow, nowMs: number): number | null {
  const leg = flow.currentLeg;
  if (!leg || !leg.travelSeconds) return null;
  const planned = Date.parse(flow.plannedAt);
  const arrives = Date.parse(leg.arrivesAt);
  if (Number.isNaN(planned) || Number.isNaN(arrives)) return null;
  return Math.max(0, (arrives - (planned + leg.travelSeconds * 1000)) / 1000);
}
```

(`nowMs` kept in the signature for future in-flight drift refinement; unused today — name it `_nowMs` if the linter objects.) Green.
- [ ] **Step 3 — silhouettes.** In `FlowShipLayer.tsx`, replace ONLY the wedge `Line` inside the rotated body group (trail lines stay) with a hauler-split glyph; program color (existing `color`) becomes stripe + engine tint; add the thruster flare, gated on glide (the layer already receives the motion state `m`):

```tsx
{/* hull silhouette: heavy = broad twin-nacelle freighter, light = slender dart */}
{heavy ? (
  <>
    <Line points={[7 * u, 0, 2 * u, 4.5 * u, -6 * u, 4.5 * u, -8 * u, 2 * u, -8 * u, -2 * u, -6 * u, -4.5 * u, 2 * u, -4.5 * u]} closed fill={noirAlpha(NOIR.ink, 0.92)} stroke={noirAlpha(NOIR.dim, 0.8)} strokeWidth={0.4 * u} listening={false} />
    <Line points={[-1 * u, -3.2 * u, -1 * u, 3.2 * u]} stroke={noirAlpha(NOIR.dim, 0.9)} strokeWidth={0.5 * u} listening={false} />
    <Line points={[-4.5 * u, -3.2 * u, -4.5 * u, 3.2 * u]} stroke={noirAlpha(NOIR.dim, 0.9)} strokeWidth={0.5 * u} listening={false} />
    <Line points={[5.5 * u, -1.2 * u, 1 * u, -1.2 * u]} stroke={color} strokeWidth={0.8 * u} lineCap="round" listening={false} />
    <Rect x={-9.5 * u} y={-3.4 * u} width={2 * u} height={2 * u} fill={noirAlpha(color, engineGlow)} cornerRadius={0.5 * u} listening={false} />
    <Rect x={-9.5 * u} y={1.4 * u} width={2 * u} height={2 * u} fill={noirAlpha(color, engineGlow)} cornerRadius={0.5 * u} listening={false} />
  </>
) : (
  <>
    <Line points={[8 * u, 0, 1 * u, 2.6 * u, -5 * u, 1.8 * u, -5 * u, -1.8 * u, 1 * u, -2.6 * u]} closed fill={noirAlpha(NOIR.ink, 0.92)} stroke={noirAlpha(NOIR.dim, 0.8)} strokeWidth={0.4 * u} listening={false} />
    <Circle x={4.2 * u} y={0} radius={0.9 * u} fill={noirAlpha(NOIR.accentSoft, 0.9)} listening={false} />
    <Line points={[3 * u, 0, -3.5 * u, 0]} stroke={color} strokeWidth={0.7 * u} lineCap="round" listening={false} />
    <Rect x={-6.5 * u} y={-1.1 * u} width={1.6 * u} height={2.2 * u} fill={noirAlpha(color, engineGlow)} cornerRadius={0.4 * u} listening={false} />
  </>
)}
{gliding && (
  <Line
    points={[heavy ? -9.5 * u : -6.5 * u, 0, (heavy ? -9.5 : -6.5) * u - flameLen, 0]}
    stroke={noirAlpha(NOIR.warn, 0.5 + 0.3 * flicker)}
    strokeWidth={(heavy ? 2.4 : 1.6) * u}
    lineCap="round"
    listening={false}
  />
)}
```

with per-ship locals computed above the return: `const heavy = isHeavyHauler(flow.shipNav);`, `const gliding = m.mode === 'glide';`, `const flicker = 0.5 + 0.5 * Math.sin(nowMs / 90 + (hash of containerId % 7));` (reuse/hoist a hash — `flowMotion`'s `hashShip` may need exporting), `const flameLen = (heavy ? 7 : 5) * u * (0.7 + 0.3 * flicker);`, `const engineGlow = gliding ? 0.9 : 0.25;`, and the whole rotated group gets `opacity={gliding ? 1 : 0.85}`. Imports gain `Rect` from react-konva, `isHeavyHauler` + drift symbols from `./flowMotion`.
- [ ] **Step 4 — drift tick.** In the unrotated dress section: `const drift = scheduleDriftSeconds(flow, nowMs);` then

```tsx
{drift !== null && drift > DRIFT_AMBER_SECONDS && (
  <Arc innerRadius={r + 4.6 * u} outerRadius={r + 6 * u} angle={26} rotation={-103} fill={drift > DRIFT_RED_SECONDS ? NOIR.bad : NOIR.warn} listening={false} />
)}
```

- [ ] **Step 5 — roster suffix.** In `TourRoster.tsx`, on the ETA line: append `{drift > amber ? \` · +${Math.round(drift/60)}m\` : ''}` colored amber/red (compute via the same helper; import from flowMotion). Test: assert the late mock tour's row shows `+7m` (exact value per the T3 fixture) and an on-time row shows none.
- [ ] **Step 6** — full web suite + tsc green. **Commit** `feat(viz-web): hauler silhouettes with thruster flares + schedule-drift ticks (sp-XXXX)`.

---

### Task 6: widgets — tooltip + ticker + page wiring

**Files:** Create `web/src/components/flows/FlowTooltip.tsx` (+`__tests__/FlowTooltip.test.tsx`), `web/src/components/flows/FillTicker.tsx` (+`__tests__/FillTicker.test.tsx`); Modify `web/src/pages/TradeFlowsView.tsx`, `web/src/components/flows/FlowGalaxyScene.tsx` (node hover → tooltip).

**Interfaces:** Consumes store `tooltip`/`setTooltip`, `fills`, `freshness`, `lanes` (systemLanes topGoods), `live` flows; `freshnessColor` from `./freshness`.

- [ ] **Step 1 — failing widget tests.** `FlowTooltip.test.tsx` (render with a store-populated state via the exposed `useFlowStore`): system tooltip shows symbol, ramp-colored pct, realized credits, hull count, post status; lane tooltip (`key: 'X1-NK36→X1-KA42'`) shows corridor, credits, trips, top-goods lines; renders nothing when `tooltip` is null. `FillTicker.test.tsx`: renders newest-first entries (`sold`/`bought` verbs, signed compact credits, `@ waypoint`), sell rows styled `NOIR.good` / buys `NOIR.warn`, at most 6 rendered, empty fills → null.
- [ ] **Step 2 — implement `FlowTooltip.tsx`.** Pure-presentational: reads the store; positioned `style={{ left: tooltip.x + 14, top: tooltip.y + 14 }}` in a `pointer-events-none absolute z-20` glass panel (same NOIR panel styling as the cards). Content branch per `kind`: **system** — resolve from `freshness.systems`, `lanes.systemActivity`, flows (`shipNav?.systemSymbol === key` count), topology `homeSystem`; omit missing sections gracefully. **lane** — split the key on `'→'`, find the `systemLanes` record, list `topGoods` with `money()` compact formatting.
- [ ] **Step 3 — implement `FillTicker.tsx`.** Reads `fills`; renders bottom strip (`absolute bottom-12 left-4 right-4 pointer-events-none flex flex-col-reverse gap-0.5 text-xs font-mono`): newest 6, per-row opacity `1 − ageRank × 0.15`, entry animation via a keyed CSS class (new id mounts with a short translate/fade-in — plain CSS in the component, no lib). Line: `{ship} {isBuy ? 'bought' : 'sold'} {units} {good} {credits signed compact} @ {waypoint}`.
- [ ] **Step 4 — wiring.** `TradeFlowsView` mounts `<FillTicker />` and `<FlowTooltip />` (tooltip last, above everything). `FlowGalaxyScene`: system-node `Circle` gains `onMouseMove` → `setTooltip({ kind: 'system', key: s.symbol, x, y })` (client coords from the stage container rect + pointer position, same recipe as T4) and `onMouseLeave` → `setTooltip(null)`; drilldown open also clears it (add to `openDrilldown` call site). Layer-toggle interaction: hide the lane tooltip when Lanes toggles off (clear `tooltip` in `toggleLayer` when the key matches the tooltip kind — one-line store tweak, keep it minimal).
- [ ] **Step 5** — full suite + tsc green. **Commit** `feat(viz-web): shared hover tooltip (system+lane cards) + ambient fill ticker (sp-XXXX)`.

---

### Task 7: on-screen verification (two scenarios)

Standard demo build (`VITE_USE_MOCK_API=true rtk proxy npx vite build && rtk proxy npx vite preview --port 4173 &`), headless Chrome screenshots (`--force-device-scale-factor=2` recommended), **Read the PNGs**:

**Scenario A (4-system demo, `/private/tmp/galaxy-ambience-check.png`):**
1. Hull silhouettes: A/D read as broad twin-nacelle freighters, B/C as slender darts; program color visible as stripe + engine glow, not body fill; gliding hulls show flickering aft flames (compare two shots), dwelling hulls dark-engined.
2. Drift: gliding Tour A shows an amber tick on its ring; Arb C a red tick; B/D no tick. Roster shows `+7m` (amber) and `+20m` (red) suffixes.
3. Ticker: bottom stream visible, ≤6 rows, sells green / buys amber, newest at bottom edge sliding in.
4. Paths: only hovered/selected flow bright (screenshot default state: all whisper-thin); rings/trails intact.
5. Hover checks (drive via `window.__flowStore.getState().setTooltip(...)` on a dev build, or verify by component tests + one manual screenshot with the tooltip forced): system card + lane card render with content.

**Scenario B (dense fixture, `VITE_MOCK_DENSE=1` build, `/private/tmp/galaxy-dense-check.png`):**
6. Exactly 12 artery lanes carry dash+arrow; the rest are faint solid capillaries; sub-floor lanes absent (count visually against the fixture's known profit spread).
7. The hairball reads: arteries pop, capillaries are texture, quiet paths don't compete — compare side-by-side against the Admiral's screenshot aesthetic (`the original complaint`).
8. 60fps sanity: no obvious animation hitching with ~40 systems/10 flows (eyeball two consecutive screenshots' timestamps; deep profiling out of scope).

Fix-and-rescreenshot until all pass. Kill servers. Full gates: gobot targeted (`rtk go build ./... && rtk go test ./internal/adapters/flowfeed/... ./internal/application/trading/commands/ -run 'TestBuild'`), server `rtk proxy npx vitest run`, web `rtk proxy npx tsc --noEmit && rtk proxy npx vitest run`. Commit fixes as `fix(viz-web): declutter/ambience on-screen verification fixes (sp-XXXX)`.

---

## Final validation & close-out

- Full suites (gobot targeted, server, web) + the orchestrator's close-out: close the bead, merge per finishing-a-development-branch, session-close protocol (`git pull --rebase && bd dolt push && git push`).

## Deferred by design

- Replay scrubber, search, minimap, follow mode (no ops-room — Admiral).
- Toggle-row polish, capillary direction-merge, role silhouettes beyond light/heavy, UI-configurable thresholds (spec §9).

