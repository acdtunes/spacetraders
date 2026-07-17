# Galaxy View (Trade Flows Redesign) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade `/trade-flows` into a real-coordinate galaxy scene: schedule-driven ship glides with direction, closed-tour loops, deadhead styling, and expected-vs-realized profit per tour (ring + roster + card).

**Architecture:** Evolve in place. gobot adds two additive pieces (flowfeed hop metadata, a `system_coords` table via AutoMigrate). The viz-server upgrades its three `/api/flows/*` endpoints (real coords with lazy live-API fill, transit columns + realized sums, system-level lane rollups). The web rebuilds the Konva scene's motion + visuals on top of the existing polling/store/degradation skeleton.

**Tech Stack:** Go (GORM/AutoMigrate, stdlib tests) · Express + pg + vitest/supertest · React 18 + react-konva + zustand + vitest.

**Spec:** `docs/superpowers/specs/2026-07-16-galaxy-view-trade-flows-design.md` (approved). Repo root for all paths below: `/Users/andres.dandrea/IdeaProjects/cities/spacetraders`.

## Global Constraints

- Prefix every shell command with `rtk` (repo rule; passthrough is safe for unlisted commands).
- Task tracking via `bd` only — no TodoWrite/TaskCreate. Before Task 1: from the repo root run `rtk bd create --title="Galaxy View: Trade Flows redesign (spec 2026-07-16)" --type=feature --priority=2` and reference the returned id in commit messages as `(sp-XXXX)`.
- Execute in an isolated worktree (superpowers:using-git-worktrees) branched from `main`.
- The flowfeed JSON contract is **additive-only**; struct field order in `registry.go` IS the JSON field order — never reorder existing fields.
- No new runtime dependencies anywhere (no WebGL/three.js; Konva + existing libs only).
- All web colors come from `NOIR` tokens (`web/src/theme/noir.ts`) — no raw hex in components.
- Demo/fixtures mode is the existing `VITE_USE_MOCK_API=true` mock client — extend `mocks/mockFlows.ts`, do not invent a new mode.
- `system_coords` is era-scoped: PK `(era_id, symbol)`; era resolved as `SELECT era_id FROM eras ORDER BY era_id DESC LIMIT 1` (0 when empty).
- Realized profit = signed `SUM(amount)` over `transactions` rows with `related_entity_type='container'` (amount is "positive for income, negative for expenses" — models.go:360; `idx_related` covers the lookup).
- Every commit message ends with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Go tests: run package-scoped (`rtk go test ./internal/...`). If a phantom "package X is not in std" error appears after parallel runs, `rtk go clean -cache` and retry once before suspecting the diff.

## File Structure

**gobot (Tasks 1-2)**
- Modify `gobot/internal/adapters/flowfeed/registry.go` — `Hop` gains `System`, `TravelSeconds`; `Flow` gains `Closed`.
- Modify `gobot/internal/application/trading/commands/flow_publish.go` — map the new fields; local `waypointSystem` helper.
- Modify `gobot/internal/application/trading/commands/flow_publish_test.go`.
- Modify `gobot/internal/adapters/persistence/models.go` — `SystemCoordModel` + `AllModels()` registration.
- Create `gobot/internal/infrastructure/database/system_coords_migrate_test.go`.

**viz-server (Tasks 3-6)**
- Create `visualizer/server/utils/systemCoords.ts` (+ `__tests__/systemCoords.test.ts` under `visualizer/server/routes/__tests__/` — that dir holds all server tests today).
- Modify `visualizer/server/utils/galaxyLayout.ts` — `layoutWithAnchors` anchored fallback.
- Modify `visualizer/server/utils/laneAggregation.ts` — `systemOfWaypoint`, `rollupSystemLanes`, `rollupSystemActivity`.
- Modify `visualizer/server/routes/flows.ts` — all three handlers.
- Modify `visualizer/server/routes/__tests__/flows.topology.test.ts` (full rewrite — query sequence changed), `galaxyLayout.test.ts`, `laneAggregation.test.ts`; Create `flows.live.realized.test.ts`.

**web (Tasks 7-13)**
- Modify `visualizer/web/src/types/flows.ts`, `visualizer/web/src/mocks/mockFlows.ts`.
- Create `visualizer/web/src/components/flows/flowMotion.ts` (+ `__tests__/flowMotion.test.ts`) — adjacency/BFS, stops, per-edge glide solver, plan polylines.
- Create `visualizer/web/src/components/flows/profitRing.ts` (+ `__tests__/profitRing.test.ts`).
- Modify `visualizer/web/src/components/flows/FlowShipLayer.tsx` (wedge glyph, trail, ring), `FlowPlanPath.tsx` (gate-path polylines, gradient, deadhead, anchor ring), `FlowGalaxyScene.tsx` (backdrop, activity nodes, systemLanes, hover/focus), `FlowDetailPanel.tsx` (realized + ring + hover).
- Create `visualizer/web/src/components/flows/TourRoster.tsx` (+ test).
- Modify `visualizer/web/src/store/flowStore.ts` (+ test), `visualizer/web/src/pages/TradeFlowsView.tsx`.

Each task ends green + committed. Web/server test runner: `rtk npx vitest run <file>` from the respective package dir.

---

### Task 1: flowfeed wire — `Hop.system/travelSeconds`, `Flow.closed`

**Files:**
- Modify: `gobot/internal/adapters/flowfeed/registry.go:33-37` (Hop), `:62-72` (Flow)
- Modify: `gobot/internal/application/trading/commands/flow_publish.go`
- Test: `gobot/internal/application/trading/commands/flow_publish_test.go`

**Interfaces:**
- Consumes: `routing.TourLeg{System string, TravelSecondsFromPrev int}` (`gobot/internal/domain/routing/tour.go:138-144`), `RunTourCoordinatorCommand.ClosedTours bool` (`run_tour_coordinator.go:163`).
- Produces (JSON, consumed by Tasks 5/7): hop `{waypoint, system, travelSeconds, tranches}`; flow gains `"closed": bool` after `tourId`.

- [ ] **Step 1: Write the failing tests**

In `flow_publish_test.go`, update `tourPlanFixture()` legs — add `System: "X1-AA"` to all three legs and `TravelSecondsFromPrev: 420` to leg 2, `TravelSecondsFromPrev: 300` to leg 3 (leg 1 stays 0). Leave every existing waypoint/assertion untouched. Then append:

```go
func crossSystemTourPlanFixture() *routing.TourPlan {
	return &routing.TourPlan{
		Legs: []routing.TourLeg{
			{Waypoint: "X1-AA-A1", System: "X1-AA", Trades: []routing.TourTrade{{Good: "IRON", Units: 50, ExpectedUnitPrice: 30, IsBuy: true}}},
			{Waypoint: "X1-BB-D4", System: "X1-BB", TravelSecondsFromPrev: 900, Trades: []routing.TourTrade{{Good: "IRON", Units: 50, ExpectedUnitPrice: 42, IsBuy: false}}},
		},
		ProjectedProfit:         600,
		ProjectedCreditsPerHour: 5400,
	}
}

func TestBuildTourFlow_HopsCarrySystemAndTravelSeconds(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9"}
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)

	f := buildTourFlow(cmd, crossSystemTourPlanFixture(), -1, time.Time{}, nil, now)

	if len(f.RemainingHops) != 2 {
		t.Fatalf("want 2 hops, got %d", len(f.RemainingHops))
	}
	h0, h1 := f.RemainingHops[0], f.RemainingHops[1]
	if h0.System != "X1-AA" || h0.TravelSeconds != 0 {
		t.Errorf("hop0 = %q/%d, want X1-AA/0", h0.System, h0.TravelSeconds)
	}
	if h1.System != "X1-BB" || h1.TravelSeconds != 900 {
		t.Errorf("hop1 = %q/%d, want X1-BB/900", h1.System, h1.TravelSeconds)
	}
	if f.Closed {
		t.Errorf("open command must publish closed=false")
	}
}

func TestBuildTourFlow_ClosedFlagFollowsCommand(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ContainerID: "tour-run-SHIP-9-xyz", ShipSymbol: "SHIP-9", ClosedTours: true}
	f := buildTourFlow(cmd, tourPlanFixture(), -1, time.Time{}, nil, time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC))
	if !f.Closed {
		t.Errorf("ClosedTours command must publish closed=true")
	}
}

func TestBuildArbAndTradeRouteFlows_DeriveHopSystem(t *testing.T) {
	arbCmd := &RunArbCoordinatorCommand{ContainerID: "arb-1", ShipSymbol: "SHIP-1", Good: "IRON_ORE", SellAt: "X1-AA-B2", MaxUnits: 10, QuotedDestBid: 5}
	af := buildArbFlow(arbCmd, nil, time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC))
	if af.RemainingHops[0].System != "X1-AA" || af.RemainingHops[0].TravelSeconds != 0 {
		t.Errorf("arb hop = %q/%d, want X1-AA/0", af.RemainingHops[0].System, af.RemainingHops[0].TravelSeconds)
	}

	trCmd := &RunTradeRouteCoordinatorCommand{ContainerID: "tr-1", ShipSymbol: "SHIP-2"}
	lane := trading.ArbitrageLane{SourceWaypoint: "X1-CC-A1", DestWaypoint: "X1-DD-B2", Good: "FUEL", VolumeCap: 40, DestBid: 80, CappedSpread: 1200}
	tf := buildTradeRouteFlow(trCmd, lane, 3600, nil, time.Time{}, time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC))
	if tf.RemainingHops[0].System != "X1-DD" || tf.RemainingHops[0].TravelSeconds != 0 {
		t.Errorf("trade-route hop = %q/%d, want X1-DD/0", tf.RemainingHops[0].System, tf.RemainingHops[0].TravelSeconds)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /path/to/worktree/gobot && rtk go test ./internal/application/trading/commands/ -run 'TestBuildTourFlow_HopsCarry|TestBuildTourFlow_ClosedFlag|TestBuildArbAndTradeRoute' -v
```
Expected: compile errors — `unknown field System in struct literal` / `f.Closed undefined`.

- [ ] **Step 3: Implement**

`registry.go` — replace the `Hop` struct (keep the comment style):

```go
// Hop is a planned future stop with its intended tranches. System tags the
// stop's system so the galaxy view can chain cross-system glides;
// TravelSeconds is the planner's projected travel time from the previous stop
// (0 = no plan-time estimate — viewers fall back to nav-truth interpolation).
type Hop struct {
	Waypoint      string    `json:"waypoint"`
	System        string    `json:"system"`
	TravelSeconds int       `json:"travelSeconds"`
	Tranches      []Tranche `json:"tranches"`
}
```

In `Flow`, insert ONE line directly after the `TourID` field (additive; do not touch any other line):

```go
	Closed        bool        `json:"closed"` // closed-tour mode: the plan returns to its anchor (sp-im74)
```

`flow_publish.go` — add `"strings"` to imports, add the helper, and extend the three builders:

```go
// waypointSystem derives "X1-AA" from "X1-AA-B2" (SECTOR-SYSTEM-WAYPOINT).
// Non-conforming symbols pass through unchanged.
func waypointSystem(wp string) string {
	parts := strings.Split(wp, "-")
	if len(parts) < 2 {
		return wp
	}
	return parts[0] + "-" + parts[1]
}
```

In `buildTourFlow`'s hop loop, replace `hops = append(hops, flowfeed.Hop{Waypoint: leg.Waypoint, Tranches: tranches})` with:

```go
		hops = append(hops, flowfeed.Hop{
			Waypoint:      leg.Waypoint,
			System:        leg.System,
			TravelSeconds: leg.TravelSecondsFromPrev,
			Tranches:      tranches,
		})
```

In `buildTourFlow`'s returned `flowfeed.Flow` literal, add `Closed: cmd.ClosedTours,` after `TourID`. In `buildArbFlow`'s hop literal add `System: waypointSystem(cmd.SellAt),` (TravelSeconds stays zero-value). In `buildTradeRouteFlow`'s hop literal add `System: waypointSystem(lane.DestWaypoint),`.

- [ ] **Step 4: Run the package tests**

```bash
rtk go test ./internal/application/trading/commands/ -run 'TestBuild' -v && rtk go test ./internal/adapters/flowfeed/...
```
Expected: PASS. If any flowfeed/handler test asserts an exact JSON payload, extend its expected hops with `"system":"…","travelSeconds":0` and the flow with `"closed":false` — the Go values above are the source of truth.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/flowfeed/registry.go internal/application/trading/commands/flow_publish.go internal/application/trading/commands/flow_publish_test.go
rtk git commit -m "feat(flowfeed): hops carry system + planned travelSeconds; flows carry closed (galaxy view, sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `system_coords` table (AutoMigrate)

**Files:**
- Modify: `gobot/internal/adapters/persistence/models.go` (new model near `GateEdgeModel:551`; register in `AllModels():863`)
- Test: `gobot/internal/infrastructure/database/system_coords_migrate_test.go` (create)

**Interfaces:**
- Produces: PG table `system_coords(era_id int, symbol text, x float8, y float8, fetched_at text)` PK `(era_id, symbol)` — written by the viz-server (Task 3), never by gobot.

- [ ] **Step 1: Write the failing test**

```go
// gobot/internal/infrastructure/database/system_coords_migrate_test.go
package database

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

func TestAutoMigrate_CreatesSystemCoordsTable(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if !db.Migrator().HasTable("system_coords") {
		t.Fatal("system_coords table not created")
	}
	for _, col := range []string{"era_id", "symbol", "x", "y", "fetched_at"} {
		if !db.Migrator().HasColumn(&persistence.SystemCoordModel{}, col) {
			t.Errorf("system_coords missing column %s", col)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
rtk go test ./internal/infrastructure/database/ -run TestAutoMigrate_CreatesSystemCoords -v
```
Expected: compile error `undefined: persistence.SystemCoordModel`.

- [ ] **Step 3: Implement**

In `models.go`, directly above `TourLegTelemetryModel` (i.e. after `GateEdgeModel.TableName`), add:

```go
// SystemCoordModel is one galaxy-level system coordinate snapshot row. The
// daemon owns only the DDL (AutoMigrate); rows are written LAZILY by the
// visualizer server from the live GET /systems/{symbol} API while building
// /api/flows/topology, so the galaxy view draws REAL positions instead of a
// synthesized force layout. Era-scoped like GateEdgeModel: a universe reset
// regenerates symbols, and the (era_id, symbol) key keeps a dead era's
// coordinates from colliding with a recurring symbol.
type SystemCoordModel struct {
	EraID     int     `gorm:"column:era_id;primaryKey"`
	Symbol    string  `gorm:"column:symbol;primaryKey;size:32"`
	X         float64 `gorm:"column:x;not null"`
	Y         float64 `gorm:"column:y;not null"`
	FetchedAt string  `gorm:"column:fetched_at"` // RFC3339, mirrors GateEdgeModel.SyncedAt
}

func (SystemCoordModel) TableName() string {
	return "system_coords"
}
```

In `AllModels()`, append `&SystemCoordModel{},` at the end of the slice (before the closing brace).

- [ ] **Step 4: Run tests + build**

```bash
rtk go test ./internal/infrastructure/database/ -run TestAutoMigrate -v && rtk go build ./...
```
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapters/persistence/models.go internal/infrastructure/database/system_coords_migrate_test.go
rtk git commit -m "feat(persistence): era-scoped system_coords table for galaxy coordinates (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: server coord resolution — `systemCoords.ts` + anchored fallback layout

**Files:**
- Create: `visualizer/server/utils/systemCoords.ts`
- Modify: `visualizer/server/utils/galaxyLayout.ts` (export `layoutWithAnchors`; `hashSymbol` is already in this file)
- Test: `visualizer/server/routes/__tests__/systemCoords.test.ts` (create), `visualizer/server/routes/__tests__/galaxyLayout.test.ts` (append)

**Interfaces:**
- Consumes: pg client (`query(sql, params)`), a `fetchSystemXY(symbol) => Promise<{x,y}|null>` injected by Task 4.
- Produces: `currentEraId(client): Promise<number>`, `resolveSystemCoords(client, fetchSystemXY, systems, eraId): Promise<Map<string,{x,y}>>`, `layoutWithAnchors(real, systems, edges): (LayoutNode & {layout:'real'|'force'})[]`.

- [ ] **Step 1: Write the failing tests**

`visualizer/server/routes/__tests__/systemCoords.test.ts`:

```ts
import { describe, it, expect, vi } from 'vitest';
import { currentEraId, resolveSystemCoords } from '../../utils/systemCoords.js';

const client = (impl: (sql: string, params?: unknown[]) => Promise<{ rows: any[] }>) => ({ query: vi.fn(impl) });

describe('currentEraId', () => {
  it('returns the latest era id', async () => {
    const c = client(async () => ({ rows: [{ era_id: 7 }] }));
    expect(await currentEraId(c)).toBe(7);
  });
  it('returns 0 when eras is empty or malformed', async () => {
    expect(await currentEraId(client(async () => ({ rows: [] })))).toBe(0);
    expect(await currentEraId(client(async () => ({ rows: [{ nope: 1 }] })))).toBe(0);
  });
});

describe('resolveSystemCoords', () => {
  it('returns known rows and skips the live API for them', async () => {
    const c = client(async (sql) => {
      if (/FROM system_coords/.test(sql)) return { rows: [{ symbol: 'X1-AA', x: '10', y: '-20' }] };
      return { rows: [] };
    });
    const fetcher = vi.fn();
    const real = await resolveSystemCoords(c, fetcher, ['X1-AA'], 7);
    expect(real.get('X1-AA')).toEqual({ x: 10, y: -20 });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('lazily fetches + upserts missing systems', async () => {
    const calls: { sql: string; params?: unknown[] }[] = [];
    const c = {
      query: vi.fn(async (sql: string, params?: unknown[]) => {
        calls.push({ sql, params });
        if (/FROM system_coords/.test(sql)) return { rows: [] };
        return { rows: [] }; // INSERT
      }),
    };
    const fetcher = vi.fn(async () => ({ x: 33, y: 44 }));
    const real = await resolveSystemCoords(c, fetcher, ['X1-BB'], 7);
    expect(real.get('X1-BB')).toEqual({ x: 33, y: 44 });
    const insert = calls.find((c2) => /INSERT INTO system_coords/.test(c2.sql));
    expect(insert).toBeTruthy();
    expect(insert!.params!.slice(0, 4)).toEqual([7, 'X1-BB', 33, 44]);
    expect(insert!.sql).toMatch(/ON CONFLICT \(era_id, symbol\)/);
  });

  it('leaves a system absent when the live API returns null or throws', async () => {
    const c = client(async (sql) => (/FROM system_coords/.test(sql) ? { rows: [] } : { rows: [] }));
    const fetcher = vi.fn(async (sym: string) => {
      if (sym === 'X1-CC') return null;
      throw new Error('api down');
    });
    const real = await resolveSystemCoords(c, fetcher, ['X1-CC', 'X1-DD'], 7);
    expect(real.size).toBe(0);
  });

  it('ignores malformed snapshot rows (garbage never poisons the map)', async () => {
    const c = client(async (sql) =>
      /FROM system_coords/.test(sql) ? { rows: [{ symbol: undefined, x: 'nope', y: null }] } : { rows: [] },
    );
    const real = await resolveSystemCoords(c, vi.fn(async () => null), ['X1-EE'], 7);
    expect(real.size).toBe(0);
  });
});
```

Append to `galaxyLayout.test.ts`:

```ts
import { layoutWithAnchors } from '../../utils/galaxyLayout.js';

describe('layoutWithAnchors', () => {
  const edges = [
    { from: 'X1-AA', to: 'X1-BB' },
    { from: 'X1-BB', to: 'X1-CC' },
  ];

  it('passes real coordinates through verbatim with layout=real', () => {
    const real = new Map([
      ['X1-AA', { x: 100, y: 200 }],
      ['X1-BB', { x: -50, y: 80 }],
      ['X1-CC', { x: 0, y: -300 }],
    ]);
    const nodes = layoutWithAnchors(real, ['X1-AA', 'X1-BB', 'X1-CC'], edges);
    const aa = nodes.find((n) => n.symbol === 'X1-AA')!;
    expect(aa).toMatchObject({ x: 100, y: 200, layout: 'real' });
  });

  it('anchors an unknown near its real neighbours, flagged force', () => {
    const real = new Map([
      ['X1-AA', { x: 0, y: 0 }],
      ['X1-CC', { x: 400, y: 0 }],
    ]);
    const nodes = layoutWithAnchors(real, ['X1-AA', 'X1-BB', 'X1-CC'], edges);
    const bb = nodes.find((n) => n.symbol === 'X1-BB')!;
    expect(bb.layout).toBe('force');
    // Neighbour centroid is (200, 0); jitter is bounded by spread*0.06 + 100.
    expect(Math.hypot(bb.x - 200, bb.y - 0)).toBeLessThanOrEqual(400 * 0.06 + 100 + 1);
  });

  it('places a neighbourless unknown on a ring outside the real spread', () => {
    const real = new Map([['X1-AA', { x: 0, y: 0 }]]);
    const nodes = layoutWithAnchors(real, ['X1-AA', 'X1-ZZ'], []);
    const zz = nodes.find((n) => n.symbol === 'X1-ZZ')!;
    expect(zz.layout).toBe('force');
    expect(Math.hypot(zz.x, zz.y)).toBeGreaterThan(0);
  });

  it('degenerates to the classic force layout when nothing is real, deterministically', () => {
    const a = layoutWithAnchors(new Map(), ['X1-AA', 'X1-BB'], edges);
    const b = layoutWithAnchors(new Map(), ['X1-AA', 'X1-BB'], edges);
    expect(a).toEqual(b);
    expect(a.every((n) => n.layout === 'force')).toBe(true);
  });
});
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd visualizer/server && rtk npx vitest run routes/__tests__/systemCoords.test.ts routes/__tests__/galaxyLayout.test.ts
```
Expected: FAIL — module `../../utils/systemCoords.js` not found; `layoutWithAnchors` not exported.

- [ ] **Step 3: Implement**

Create `visualizer/server/utils/systemCoords.ts`:

```ts
export interface PgClientLike {
  query: (sql: string, params?: unknown[]) => Promise<{ rows: any[] }>;
}

export type FetchSystemXY = (symbol: string) => Promise<{ x: number; y: number } | null>;

// Latest era id, or 0 when the eras table is empty/malformed.
export async function currentEraId(client: PgClientLike): Promise<number> {
  const r = await client.query(`SELECT era_id FROM eras ORDER BY era_id DESC LIMIT 1`);
  return Number(r.rows[0]?.era_id) || 0;
}

// Real galaxy coordinates for `systems`: read the era-scoped snapshot, then
// lazily fetch + upsert any missing system from the live API (one GET per
// unknown; this only runs on topology cache misses, ~once per 5 minutes).
// A system the API cannot supply stays absent — the caller force-places it.
// Fetch/upsert failures are per-system and non-fatal.
export async function resolveSystemCoords(
  client: PgClientLike,
  fetchSystemXY: FetchSystemXY,
  systems: string[],
  eraId: number,
): Promise<Map<string, { x: number; y: number }>> {
  const real = new Map<string, { x: number; y: number }>();
  if (systems.length === 0) return real;

  const known = await client.query(
    `SELECT symbol, x, y FROM system_coords WHERE era_id = $1 AND symbol = ANY($2)`,
    [eraId, systems],
  );
  for (const row of known.rows) {
    const x = Number(row?.x);
    const y = Number(row?.y);
    if (typeof row?.symbol === 'string' && Number.isFinite(x) && Number.isFinite(y)) {
      real.set(row.symbol, { x, y });
    }
  }

  for (const symbol of systems.filter((s) => !real.has(s))) {
    try {
      const xy = await fetchSystemXY(symbol);
      if (!xy) continue;
      await client.query(
        `INSERT INTO system_coords (era_id, symbol, x, y, fetched_at)
         VALUES ($1, $2, $3, $4, $5)
         ON CONFLICT (era_id, symbol)
         DO UPDATE SET x = EXCLUDED.x, y = EXCLUDED.y, fetched_at = EXCLUDED.fetched_at`,
        [eraId, symbol, xy.x, xy.y, new Date().toISOString()],
      );
      real.set(symbol, xy);
    } catch {
      // best effort per system; anchored force placement covers it this cycle
    }
  }
  return real;
}
```

Append to `visualizer/server/utils/galaxyLayout.ts`:

```ts
export type AnchoredNode = LayoutNode & { layout: 'real' | 'force' };

// Place systems with real coordinates verbatim; anchor each unknown at the
// centroid of its gate neighbours' REAL positions (deterministic hash jitter
// so siblings sharing a neighbour set don't stack), or on a hash ring outside
// the real spread when no neighbour is known. All-unknown degenerates to the
// classic force layout.
export function layoutWithAnchors(
  real: Map<string, { x: number; y: number }>,
  systems: string[],
  edges: LayoutEdge[],
): AnchoredNode[] {
  const sorted = [...new Set(systems)].sort();
  if (real.size === 0) {
    return computeGalaxyLayout(sorted, edges).map((n) => ({ ...n, layout: 'force' as const }));
  }

  const neighbours = new Map<string, string[]>();
  const push = (k: string, v: string) => {
    const arr = neighbours.get(k);
    if (arr) arr.push(v);
    else neighbours.set(k, [v]);
  };
  for (const e of edges) {
    if (e.from === e.to) continue;
    push(e.from, e.to);
    push(e.to, e.from);
  }

  const reals = [...real.values()];
  const cx = reals.reduce((s, p) => s + p.x, 0) / reals.length;
  const cy = reals.reduce((s, p) => s + p.y, 0) / reals.length;
  let spread = 0;
  for (const p of reals) spread = Math.max(spread, Math.hypot(p.x - cx, p.y - cy));
  if (spread === 0) spread = 1000;

  return sorted.map((sym) => {
    const r = real.get(sym);
    if (r) return { symbol: sym, x: Math.round(r.x), y: Math.round(r.y), layout: 'real' as const };
    const h = hashSymbol(sym);
    const angle = ((h % 360) / 360) * Math.PI * 2;
    const anchored = (neighbours.get(sym) ?? [])
      .map((n) => real.get(n))
      .filter((p): p is { x: number; y: number } => Boolean(p));
    if (anchored.length > 0) {
      const ax = anchored.reduce((s, p) => s + p.x, 0) / anchored.length;
      const ay = anchored.reduce((s, p) => s + p.y, 0) / anchored.length;
      const jr = spread * 0.06 + ((h >>> 9) % 100);
      return { symbol: sym, x: Math.round(ax + Math.cos(angle) * jr), y: Math.round(ay + Math.sin(angle) * jr), layout: 'force' as const };
    }
    const ring = spread * 1.15 + ((h >>> 9) % 200);
    return { symbol: sym, x: Math.round(cx + Math.cos(angle) * ring), y: Math.round(cy + Math.sin(angle) * ring), layout: 'force' as const };
  });
}
```

- [ ] **Step 4: Run tests**

```bash
rtk npx vitest run routes/__tests__/systemCoords.test.ts routes/__tests__/galaxyLayout.test.ts
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add utils/systemCoords.ts utils/galaxyLayout.ts routes/__tests__/systemCoords.test.ts routes/__tests__/galaxyLayout.test.ts
rtk git commit -m "feat(viz-server): era-scoped coord resolution with lazy live-API fill + anchored force fallback (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: topology endpoint — real coordinates + `layout` flag

**Files:**
- Modify: `visualizer/server/routes/flows.ts:47-97` (topology handler + new `fetchSystemXY`)
- Test: `visualizer/server/routes/__tests__/flows.topology.test.ts` (FULL REWRITE — the PG query sequence changed)

**Interfaces:**
- Consumes: Task 3 exports; `SpaceTradersClient.get('/systems/{symbol}')` → `{ data: { x, y, … } }`.
- Produces: topology `systems: [{ symbol, x, y, layout: 'real'|'force' }]` (consumed by Task 7 types and the scene). Query order per cache miss: (1) gate_edges, (2) eras, (3) system_coords select, (4…) one INSERT per lazily-filled system, (last) players token.

- [ ] **Step 1: Rewrite the test file**

Replace the entire contents of `flows.topology.test.ts` with:

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest';
import express from 'express';
import request from 'supertest';

const connect = vi.fn();
vi.mock('pg', () => ({
  default: { Pool: class { on() {} connect() { return connect(); } } },
}));

// One mocked SpaceTradersClient serves BOTH concerns: /systems/{sym} coord
// fetches (lazy fill) and /my/agent (home system).
const stGet = vi.fn();
vi.mock('../../src/client.js', () => ({
  SpaceTradersClient: class {
    get(path: string) { return stGet(path); }
  },
}));

async function makeApp() {
  const { default: flowsRouter } = await import('../flows.js');
  const app = express();
  app.use(express.json());
  app.use('/api/flows', flowsRouter);
  return app;
}

beforeEach(() => {
  connect.mockReset();
  stGet.mockReset();
  vi.resetModules();
});

const GATE_ROWS = {
  rows: [
    { system_symbol: 'X1-NK36', connected_system: 'X1-KA42', gate_waypoint: 'X1-KA42-I52', under_construction: false },
    { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-ZC66-I52', under_construction: true },
  ],
};
const ERA_ROW = { rows: [{ era_id: 3 }] };

describe('GET /api/flows/topology', () => {
  it('serves real snapshot coordinates with layout=real', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(GATE_ROWS) // gate_edges
      .mockResolvedValueOnce(ERA_ROW)   // eras
      .mockResolvedValueOnce({          // system_coords: all known
        rows: [
          { symbol: 'X1-NK36', x: -100, y: 0 },
          { symbol: 'X1-KA42', x: 250, y: 40 },
          { symbol: 'X1-ZC66', x: 120, y: 380 },
        ],
      })
      .mockResolvedValueOnce({ rows: [] }); // players token (none)
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const nk = res.body.systems.find((s: any) => s.symbol === 'X1-NK36');
    expect(nk).toMatchObject({ x: -100, y: 0, layout: 'real' });
    expect(res.body.systems).toHaveLength(3);
    expect(res.body.edges).toHaveLength(2);
    expect(stGet).not.toHaveBeenCalledWith(expect.stringMatching(/^\/systems\//));
  });

  it('lazily fetches a missing system from the live API and upserts it', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-NK36', x: -100, y: 0 },
        { symbol: 'X1-KA42', x: 250, y: 40 },
      ] })
      .mockResolvedValueOnce({ rows: [] })  // INSERT for X1-ZC66
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockImplementation(async (path: string) =>
      path === '/systems/X1-ZC66' ? { data: { symbol: 'X1-ZC66', x: 9, y: -4 } } : { data: {} },
    );

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const zc = res.body.systems.find((s: any) => s.symbol === 'X1-ZC66');
    expect(zc).toMatchObject({ x: 9, y: -4, layout: 'real' });
    const insert = query.mock.calls.find((c) => /INSERT INTO system_coords/.test(c[0]));
    expect(insert![1].slice(0, 4)).toEqual([3, 'X1-ZC66', 9, -4]);
  });

  it('force-places a system the live API cannot supply (still 200, finite coords)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-NK36', x: -100, y: 0 },
        { symbol: 'X1-KA42', x: 250, y: 40 },
      ] })
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockResolvedValue({ data: {} }); // no x/y -> null

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const zc = res.body.systems.find((s: any) => s.symbol === 'X1-ZC66');
    expect(zc.layout).toBe('force');
    expect(Number.isFinite(zc.x) && Number.isFinite(zc.y)).toBe(true);
  });

  it('degrades to an all-force layout when system_coords is unavailable (pre-AutoMigrate deploy order)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce(ERA_ROW)
      .mockRejectedValueOnce(new Error('relation "system_coords" does not exist'))
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.systems).toHaveLength(3);
    for (const s of res.body.systems) {
      expect(s.layout).toBe('force');
      expect(Number.isFinite(s.x) && Number.isFinite(s.y)).toBe(true);
    }
  });

  it('stamps homeSystem from players.token -> GET /my/agent headquarters', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [GATE_ROWS.rows[0]] })
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-NK36', x: 0, y: 0 },
        { symbol: 'X1-KA42', x: 100, y: 0 },
      ] })
      .mockResolvedValueOnce({ rows: [{ token: 'agent-jwt' }] });
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockImplementation(async (path: string) =>
      path === '/my/agent' ? { data: { symbol: 'TORWIND', headquarters: 'X1-KA42-A1' } } : { data: {} },
    );

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.homeSystem).toBe('X1-KA42');
    expect(stGet).toHaveBeenCalledWith('/my/agent');
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});
```

- [ ] **Step 2: Run to verify the new expectations fail**

```bash
rtk npx vitest run routes/__tests__/flows.topology.test.ts
```
Expected: FAIL — current handler returns no `layout` field, makes no eras/system_coords queries.

- [ ] **Step 3: Implement**

In `flows.ts`: extend the import from `../utils/galaxyLayout.js` to `{ computeGalaxyLayout, layoutWithAnchors, type AnchoredNode }`, add `import { currentEraId, resolveSystemCoords } from '../utils/systemCoords.js';`, then add below the `API_BASE_URL` constant:

```ts
// Coord fetch for the lazy system_coords fill (public endpoint, no token).
async function fetchSystemXY(symbol: string): Promise<{ x: number; y: number } | null> {
  try {
    const stClient = new SpaceTradersClient(API_BASE_URL);
    const resp = await stClient.get(`/systems/${symbol}`);
    const x = Number(resp?.data?.x);
    const y = Number(resp?.data?.y);
    return Number.isFinite(x) && Number.isFinite(y) ? { x, y } : null;
  } catch {
    return null;
  }
}
```

In the topology handler, replace the single `const layout = computeGalaxyLayout(...)` line with:

```ts
    // Real coordinates, era-scoped, lazily filled from the live API. ANY
    // failure in this block (e.g. system_coords not yet AutoMigrated by the
    // daemon) degrades to the classic all-force layout — never a 503.
    let systems: AnchoredNode[];
    try {
      const eraId = await currentEraId(client);
      const real = await resolveSystemCoords(client, fetchSystemXY, [...systemSet], eraId);
      systems = layoutWithAnchors(real, [...systemSet], edges.map((e) => ({ from: e.from, to: e.to })));
    } catch (coordError: any) {
      console.error('system_coords unavailable, using force layout:', coordError?.message ?? coordError);
      systems = computeGalaxyLayout([...systemSet], edges.map((e) => ({ from: e.from, to: e.to })))
        .map((n) => ({ ...n, layout: 'force' as const }));
    }
```

and change the payload line `systems: layout,` to `systems,`.

- [ ] **Step 4: Run the server suite**

```bash
rtk npx vitest run
```
Expected: all server tests PASS (topology rewrite + untouched lanes/live/homeSystem suites).

- [ ] **Step 5: Commit**

```bash
rtk git add routes/flows.ts routes/__tests__/flows.topology.test.ts
rtk git commit -m "feat(viz-server): topology serves real galaxy coordinates with lazy fill + layout flag (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: live endpoint — transit columns + realized-per-flow

**Files:**
- Modify: `visualizer/server/routes/flows.ts:172-253` (`RawDaemonFlow`, `/live` handler)
- Test: `visualizer/server/routes/__tests__/flows.live.realized.test.ts` (create; the existing `flows.live.test.ts` stays untouched — its expectations remain valid because all changes are additive)

**Interfaces:**
- Consumes: Task 1 feed fields (passed through verbatim by the `...f` spread); PG `ships` migration-040 columns; `transactions` signed ledger.
- Produces (consumed by Task 7 types): `shipNav` gains `originSymbol: string|null, originX: number|null, originY: number|null, departureTime: string|null`; each flow gains `realized: { net: number, lastEventAt: string|null }`. Query order: (1) ships join, (2) realized sums.

- [ ] **Step 1: Write the failing test**

Create `flows.live.realized.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import express from 'express';
import request from 'supertest';

const connect = vi.fn();
vi.mock('pg', () => ({
  default: { Pool: class { on() {} connect() { return connect(); } } },
}));
vi.mock('../../src/client.js', () => ({
  SpaceTradersClient: class { get() { return Promise.resolve({ data: {} }); } },
}));

const realFetch = global.fetch;

async function makeApp() {
  const { default: flowsRouter } = await import('../flows.js');
  const app = express();
  app.use('/api/flows', flowsRouter);
  return app;
}

beforeEach(() => { connect.mockReset(); vi.resetModules(); });
afterEach(() => { global.fetch = realFetch; });

function stubDaemon(flows: any[]) {
  global.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ flows }) }) as any;
}

const baseFlow = (containerId: string, ship: string) => ({
  containerId, program: 'tour', ship, tourId: containerId, closed: false,
  currentLeg: null, cargo: [], remainingHops: [], projected: { profit: 100000, ratePerHour: 40000 },
  plannedAt: '2026-07-17T09:00:00Z',
});

const shipRow = (ship_symbol: string) => ({
  ship_symbol, nav_status: 'IN_TRANSIT', system_symbol: 'X1-NK36', location_symbol: 'X1-NK36-I52',
  location_x: '12', location_y: '-7', arrival_time: '2026-07-17T10:05:00Z',
  origin_symbol: 'X1-NK36-A1', origin_x: '3', origin_y: '4', departure_time: '2026-07-17T10:00:00Z',
});

describe('GET /api/flows/live — transit columns + realized', () => {
  it('joins origin/departure transit columns into shipNav', async () => {
    stubDaemon([baseFlow('tour-1', 'SHIP-1')]);
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [shipRow('SHIP-1')] }) // ships join
      .mockResolvedValueOnce({ rows: [] });                  // realized sums
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    expect(res.status).toBe(200);
    const nav = res.body.flows[0].shipNav;
    expect(nav).toMatchObject({
      originSymbol: 'X1-NK36-A1', originX: 3, originY: 4,
      departureTime: '2026-07-17T10:00:00.000Z',
    });
    const shipSql = query.mock.calls[0][0] as string;
    expect(shipSql).toMatch(/origin_symbol/);
    expect(shipSql).toMatch(/departure_time/);
  });

  it('folds signed transaction sums into realized per flow (0/null when no rows)', async () => {
    stubDaemon([baseFlow('tour-1', 'SHIP-1'), baseFlow('tour-2', 'SHIP-2')]);
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [shipRow('SHIP-1'), shipRow('SHIP-2')] })
      .mockResolvedValueOnce({ rows: [{ cid: 'tour-1', net: '-42000', last_event_at: '2026-07-17T10:00:00Z' }] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    const f1 = res.body.flows.find((f: any) => f.containerId === 'tour-1');
    expect(f1.realized).toEqual({ net: -42000, lastEventAt: '2026-07-17T10:00:00.000Z' });
    const f2 = res.body.flows.find((f: any) => f.containerId === 'tour-2');
    expect(f2.realized).toEqual({ net: 0, lastEventAt: null });
    const realizedSql = query.mock.calls[1][0] as string;
    expect(realizedSql).toMatch(/related_entity_type\s*=\s*'container'/);
    expect(realizedSql).toMatch(/SUM\(amount\)/i);
  });

  it('feed lost: no PG round-trips at all', async () => {
    global.fetch = vi.fn().mockRejectedValue(new Error('daemon down')) as any;
    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');
    expect(res.status).toBe(200);
    expect(res.body.feedLost).toBe(true);
    expect(connect).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run to verify it fails**

```bash
rtk npx vitest run routes/__tests__/flows.live.realized.test.ts
```
Expected: FAIL — `originSymbol` undefined on shipNav; no `realized` field.

- [ ] **Step 3: Implement**

In `RawDaemonFlow` (flows.ts:172-182): add `closed: boolean;` after `tourId`, and change the `remainingHops` element type to `{ waypoint: string; system: string; travelSeconds: number; tranches: { good: string; isBuy: boolean; units: number; expectedUnitPrice: number }[] }`. (Passthrough is automatic via the spread; this documents the contract.)

In the `/live` handler: extend the ships SELECT to

```ts
        SELECT ship_symbol, nav_status, system_symbol, location_symbol,
               location_x, location_y, arrival_time,
               origin_symbol, origin_x, origin_y, departure_time
        FROM ships
        WHERE ship_symbol = ANY($1)
```

After the `navByShip` loop and before `const flows = feed.flows.map(...)`, add:

```ts
    // Realized-so-far per flow: one grouped signed sum over the container-
    // attributed ledger (purchases negative, sells positive, refuels negative
    // => net realized). transactions.idx_related covers the lookup.
    const realizedByContainer = new Map<string, { net: number; lastEventAt: string | null }>();
    const containerIds = feed.flows.map((f) => f.containerId);
    if (containerIds.length > 0) {
      const realizedResult = await client.query(`
        SELECT related_entity_id AS cid,
               COALESCE(SUM(amount), 0) AS net,
               MAX(timestamp) AS last_event_at
        FROM transactions
        WHERE related_entity_type = 'container' AND related_entity_id = ANY($1)
        GROUP BY related_entity_id
      `, [containerIds]);
      for (const row of realizedResult.rows) {
        realizedByContainer.set(row.cid, {
          net: Number(row.net) || 0,
          lastEventAt: row.last_event_at ? new Date(row.last_event_at).toISOString() : null,
        });
      }
    }
```

In the flow mapper, extend the returned object (after `...f,`):

```ts
        realized: realizedByContainer.get(f.containerId) ?? { net: 0, lastEventAt: null },
```

and extend the `shipNav` object with:

```ts
              originSymbol: nav.origin_symbol ?? null,
              originX: nav.origin_x !== null && nav.origin_x !== undefined ? Number(nav.origin_x) : null,
              originY: nav.origin_y !== null && nav.origin_y !== undefined ? Number(nav.origin_y) : null,
              departureTime: nav.departure_time ? new Date(nav.departure_time).toISOString() : null,
```

- [ ] **Step 4: Run the full server suite**

```bash
rtk npx vitest run
```
Expected: PASS, including the pre-existing `flows.live.test.ts` (its ships-join mock rows simply lack the new columns → they map to nulls; its realized query gets the next mocked value — if any of its tests use strictly sequenced `mockResolvedValueOnce` chains, append one `.mockResolvedValueOnce({ rows: [] })` for the realized query where the run reveals it).

- [ ] **Step 5: Commit**

```bash
rtk git add routes/flows.ts routes/__tests__/flows.live.realized.test.ts routes/__tests__/flows.live.test.ts
rtk git commit -m "feat(viz-server): live feed carries exact transit columns + realized-per-flow signed sums (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: lanes endpoint — system-level rollups + per-system activity

**Files:**
- Modify: `visualizer/server/utils/laneAggregation.ts`, `visualizer/server/routes/flows.ts:154-155`
- Test: `visualizer/server/routes/__tests__/laneAggregation.test.ts` (append)

**Interfaces:**
- Produces (consumed by Task 7/11): lanes response gains `systemLanes: LaneRecord[]` (from/to are SYSTEM symbols, intra-system excluded) and `systemActivity: { system, realizedProfit, legCount }[]` (credited to the lane destination's system; intra lanes credit their own system).

- [ ] **Step 1: Write the failing tests**

Append to `laneAggregation.test.ts`:

```ts
import { rollupSystemLanes, rollupSystemActivity, systemOfWaypoint } from '../../utils/laneAggregation.js';

describe('system rollups', () => {
  const lanes = [
    { from: 'X1-AA-P1', to: 'X1-BB-Q2', realizedUnits: 100, realizedProfit: 50000, legCount: 2 },
    { from: 'X1-AA-P9', to: 'X1-BB-Q2', realizedUnits: 40, realizedProfit: 10000, legCount: 1 },
    { from: 'X1-AA-P1', to: 'X1-AA-P2', realizedUnits: 60, realizedProfit: 7000, legCount: 3 },
    { from: 'X1-BB-Q2', to: 'X1-AA-P1', realizedUnits: 10, realizedProfit: -500, legCount: 1 },
  ];

  it('systemOfWaypoint truncates to SECTOR-SYSTEM', () => {
    expect(systemOfWaypoint('X1-AA-P1')).toBe('X1-AA');
    expect(systemOfWaypoint('WEIRD')).toBe('WEIRD');
  });

  it('rolls waypoint lanes up to directed system lanes, dropping intra-system', () => {
    const sys = rollupSystemLanes(lanes);
    expect(sys).toEqual([
      { from: 'X1-AA', to: 'X1-BB', realizedUnits: 140, realizedProfit: 60000, legCount: 3 },
      { from: 'X1-BB', to: 'X1-AA', realizedUnits: 10, realizedProfit: -500, legCount: 1 },
    ]);
  });

  it('credits activity to the destination system (intra credits its own)', () => {
    const act = rollupSystemActivity(lanes);
    expect(act).toEqual([
      { system: 'X1-BB', realizedProfit: 60000, legCount: 3 },
      { system: 'X1-AA', realizedProfit: 6500, legCount: 4 },
    ]);
  });
});
```

- [ ] **Step 2: Run to verify it fails**

```bash
rtk npx vitest run routes/__tests__/laneAggregation.test.ts
```
Expected: FAIL — the three functions are not exported.

- [ ] **Step 3: Implement**

Append to `laneAggregation.ts`:

```ts
export interface SystemActivityRecord {
  system: string;
  realizedProfit: number;
  legCount: number;
}

// "X1-AA-P1" -> "X1-AA" (SECTOR-SYSTEM-WAYPOINT); non-conforming pass through.
export function systemOfWaypoint(wp: string): string {
  const parts = wp.split('-');
  return parts.length >= 2 ? `${parts[0]}-${parts[1]}` : wp;
}

// Galaxy-level rollup: directed system→system lanes. Intra-system realizations
// are excluded — they light the node (see rollupSystemActivity), not an edge.
export function rollupSystemLanes(lanes: LaneRecord[]): LaneRecord[] {
  const out = new Map<string, LaneRecord>();
  for (const l of lanes) {
    const from = systemOfWaypoint(l.from);
    const to = systemOfWaypoint(l.to);
    if (from === to) continue;
    const k = key(from, to);
    const rec = out.get(k) ?? { from, to, realizedUnits: 0, realizedProfit: 0, legCount: 0 };
    rec.realizedUnits += l.realizedUnits;
    rec.realizedProfit += l.realizedProfit;
    rec.legCount += l.legCount;
    out.set(k, rec);
  }
  return [...out.values()].sort((a, b) => b.realizedProfit - a.realizedProfit);
}

// Per-system realized activity in the window, credited to the system where the
// value realized (the lane destination; intra-system lanes credit their own).
export function rollupSystemActivity(lanes: LaneRecord[]): SystemActivityRecord[] {
  const out = new Map<string, SystemActivityRecord>();
  for (const l of lanes) {
    const system = systemOfWaypoint(l.to);
    const rec = out.get(system) ?? { system, realizedProfit: 0, legCount: 0 };
    rec.realizedProfit += l.realizedProfit;
    rec.legCount += l.legCount;
    out.set(system, rec);
  }
  return [...out.values()].sort((a, b) => b.realizedProfit - a.realizedProfit);
}
```

In `flows.ts`, extend the lanes-route import to `{ aggregateLanes, rollupSystemLanes, rollupSystemActivity }` and change the response line to:

```ts
    const lanes = aggregateLanes(telemetry, arb, windowStartMs, windowEndMs);
    res.json({
      lanes,
      systemLanes: rollupSystemLanes(lanes),
      systemActivity: rollupSystemActivity(lanes),
      window,
      generatedAt: new Date().toISOString(),
    });
```

- [ ] **Step 4: Run the full server suite**

```bash
rtk npx vitest run
```
Expected: PASS (`flows.lanes.test.ts` asserts on `lanes` and ignores the additive fields; if it deep-equals the whole body, extend its expectation with the two new arrays).

- [ ] **Step 5: Commit**

```bash
rtk git add utils/laneAggregation.ts routes/flows.ts routes/__tests__/laneAggregation.test.ts
rtk git commit -m "feat(viz-server): system-level lane rollups + per-system realized activity (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: web wire types + mock fixtures

**Files:**
- Modify: `visualizer/web/src/types/flows.ts`, `visualizer/web/src/mocks/mockFlows.ts`
- Test: existing suites must stay green after a typecheck-driven fixture sweep

**Interfaces:**
- Produces (consumed by every later task): the extended types below. Mock fixtures gain a 4-flow scenario — cross-system glide, closed loop, deadhead relocation, and three ring states (negative / partial / overshoot).

- [ ] **Step 1: Extend the types**

In `types/flows.ts` apply exactly:

```ts
export interface FlowHop {
  waypoint: string;
  system: string;          // hop's system (daemon-tagged; galaxy glide chaining)
  travelSeconds: number;   // planned travel from previous stop; 0 = no estimate
  tranches: FlowTranche[];
}
```

`DaemonFlow` gains `closed: boolean;` after `tourId`. Add:

```ts
// Signed realized-so-far from the container-attributed transaction ledger
// (purchases negative, sells positive, refuels negative).
export interface FlowRealized {
  net: number;
  lastEventAt: string | null;
}
```

`LiveFlow` becomes:

```ts
export interface LiveFlow extends DaemonFlow {
  shipNav: FlowShipNav | null;
  realized: FlowRealized;
}
```

`FlowShipNav` gains:

```ts
  originSymbol: string | null;   // ships.origin_symbol (migration 040)
  originX: number | null;
  originY: number | null;
  departureTime: string | null;  // ships.departure_time
```

`TopologySystem` gains `layout: 'real' | 'force';`. `LanesResponse` gains:

```ts
  systemLanes: LaneRecord[];              // directed system→system (galaxy layer)
  systemActivity: SystemActivityRecord[]; // node sizing/brightness
```

with:

```ts
export interface SystemActivityRecord {
  system: string;
  realizedProfit: number;
  legCount: number;
}
```

- [ ] **Step 2: Typecheck to enumerate every broken fixture**

```bash
cd visualizer/web && rtk npx tsc --noEmit
```
Expected: errors in `mocks/mockFlows.ts` and any test files constructing `LiveFlow`/`FlowHop`/`FlowShipNav`/`TopologySystem` literals (`flowGeometry.test.ts`, `FlowDetailPanel.test.tsx`, `TradeFlowsView.layout.test.tsx`, `mocks/__tests__/*`, …).

- [ ] **Step 3: Sweep the fixtures**

Mechanical additions at every site tsc flags (values per fixture intent):
- `FlowHop` literals: add `system: <first two segments of the waypoint>, travelSeconds: 0` (use a real number where the scenario wants a glide, see below).
- `DaemonFlow`/`LiveFlow` literals: add `closed: false` and `realized: { net: 0, lastEventAt: null }` unless the scenario below says otherwise.
- `FlowShipNav` literals: add `originSymbol: null, originX: null, originY: null, departureTime: null`.
- `TopologySystem` literals: add `layout: 'real'`.

Then upgrade `mocks/mockFlows.ts` into the demo scenario (keep existing exports/signatures):
1. `mockTopology`: every system gains `layout: 'real'`, and every edge's `gateWaypoint` is corrected to the CONNECTED (to-side) system's gate — matching real `gate_edges` semantics, which `buildSystemGates` depends on: `NK36→KA42: 'X1-KA42-I52'`, `KA42→ZC66: 'X1-ZC66-I52'`, `ZC66→UU57: 'X1-UU57-I52'`, `UU57→NK36: 'X1-NK36-I52'`.
2. `mockLanes(window)`: response gains hand-written `systemLanes` (roll the existing 5 base lanes up by hand: `X1-NK36→X1-KA42`, `X1-KA42→X1-ZC66`, `X1-ZC66→X1-UU57` with summed profits, window-scaled like `lanes`) and `systemActivity` (destination-credited, including intra `X1-NK36` activity).
3. `mockLiveFlows(nowMs)` — four flows:
   - **Tour A (cross-system glide, partial ring):** ship `TORWIND-3`, mid-leg toward `X1-NK36`'s gate with `currentLeg` anchored `departedAt: nowMs-90s`, `arrivesAt: nowMs+90s`; `shipNav` `IN_TRANSIT` in `X1-NK36` with `originSymbol/originX/originY/departureTime` matching, `waypointSymbol: 'X1-NK36-I52'` (the gate); hops in `X1-KA42` with `travelSeconds: 420`; `projected {profit: 250000}`, `realized {net: 96000}` (~0.38 ring).
   - **Tour B (closed loop, overshoot ring):** `closed: true`, hops circling `X1-KA42 → X1-ZC66 → X1-UU57 → X1-NK36` (last hop = anchor, `tranches: []` — the honest no-trade return leg); `shipNav` dwelling (`IN_ORBIT`) in `X1-KA42`; `projected {profit: 180000}`, `realized {net: 205000}` (overshoot glow).
   - **Arb C (early tour, negative ring):** existing arb flow shape; `projected {profit: 60000}`, `realized {net: -42000, lastEventAt: <nowMs-120s ISO>}` (capital-committed red under-glow).
   - **Relocation D (pure deadhead):** program `'tour'`, every hop `tranches: []` (`flowIsRelocation` styling), one hop in `X1-UU57` with `travelSeconds: 600`; `shipNav` `IN_ORBIT` in `X1-ZC66`; `projected: null`, `realized {net: 0}`.

- [ ] **Step 4: Typecheck + full web suite**

```bash
rtk npx tsc --noEmit && rtk npx vitest run
```
Expected: clean typecheck; all suites PASS (mock-shape tests updated alongside).

- [ ] **Step 5: Commit**

```bash
rtk git add src/types/flows.ts src/mocks/mockFlows.ts $(rtk git diff --name-only | rtk grep -E 'test|mocks')
rtk git commit -m "feat(viz-web): galaxy wire types + 4-flow demo scenario (glide/loop/deadhead/rings) (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: `flowMotion.ts` — adjacency, gate paths, glide solver, plan polylines

**Files:**
- Create: `visualizer/web/src/components/flows/flowMotion.ts`
- Test: `visualizer/web/src/components/flows/__tests__/flowMotion.test.ts`

**Interfaces:**
- Consumes: Task 7 types; `systemOf`/`Point` from `./flowGeometry`.
- Produces (consumed by Tasks 9-11): `buildAdjacency(topology)`, `buildSystemGates(topology)`, `gatePath(adj, from, to)`, `buildStops(flow)`, `flowIsRelocation(flow)`, `projectFlowMotion(flow, adj, systemGates, systemPos, nowMs, scale): MotionState | null`, `planRoutePolylines(flow, adj, systemPos): { points: number[]; deadhead: boolean }[]`.

**Motion semantics** (mirrors spec §6; edge `X→Y` is a progress bar for the whole crossing): `[0, 0.5]` = travel inside X to its gate, `0.5` = the instant jump, `[0.5, 1]` = travel inside Y from its gate to the stop. Within each half, position interpolates the REAL nav leg (`departureTime→arrivalTime`), so motion is exact where it can be and conventional only at the halves split. Holds: `0.47` parked at own gate pre-jump, `0.53` parked at own gate post-jump (cooldown), `0.04` parked pre-departure. Jump discontinuities are by design ("snaps through the midpoint at the jump instant"). Dwell (objective is intra-system) = slow orbit around the node.

- [ ] **Step 1: Write the failing tests**

```ts
// visualizer/web/src/components/flows/__tests__/flowMotion.test.ts
import { describe, it, expect } from 'vitest';
import {
  buildAdjacency, buildSystemGates, gatePath, buildStops, flowIsRelocation, projectFlowMotion,
  planRoutePolylines,
} from '../flowMotion';
import type { LiveFlow, TopologyResponse } from '../../../types/flows';

const topology: TopologyResponse = {
  systems: [
    { symbol: 'X1-AA', x: 0, y: 0, layout: 'real' },
    { symbol: 'X1-BB', x: 1000, y: 0, layout: 'real' },
    { symbol: 'X1-CC', x: 2000, y: 0, layout: 'real' },
  ],
  edges: [
    // gate_waypoint carries the CONNECTED (to-side) system's gate.
    { from: 'X1-AA', to: 'X1-BB', gateWaypoint: 'X1-BB-G1', underConstruction: false },
    { from: 'X1-BB', to: 'X1-AA', gateWaypoint: 'X1-AA-G1', underConstruction: false },
    { from: 'X1-BB', to: 'X1-CC', gateWaypoint: 'X1-CC-G1', underConstruction: false },
    { from: 'X1-CC', to: 'X1-BB', gateWaypoint: 'X1-BB-G1', underConstruction: false },
  ],
  generatedAt: 'x',
};
const adj = buildAdjacency(topology);
const gates = buildSystemGates(topology);
const pos = new Map(topology.systems.map((s) => [s.symbol, { x: s.x, y: s.y }]));
const NOW = Date.parse('2026-07-17T12:00:00Z');
const iso = (deltaSec: number) => new Date(NOW + deltaSec * 1000).toISOString();

const nav = (over: Partial<NonNullable<LiveFlow['shipNav']>>): NonNullable<LiveFlow['shipNav']> => ({
  status: 'IN_ORBIT', systemSymbol: 'X1-AA', waypointSymbol: 'X1-AA-M1', x: 0, y: 0,
  arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, ...over,
});
const flow = (over: Partial<LiveFlow>): LiveFlow => ({
  containerId: 'tour-1', program: 'tour', ship: 'SHIP-1', tourId: 'tour-1', closed: false,
  currentLeg: null, cargo: [], remainingHops: [], projected: null,
  plannedAt: iso(-600), shipNav: nav({}), realized: { net: 0, lastEventAt: null }, ...over,
});
const hop = (waypoint: string, system: string, travelSeconds = 0, tranches: any[] = [{ good: 'IRON', isBuy: false, units: 1, expectedUnitPrice: 1 }]) =>
  ({ waypoint, system, travelSeconds, tranches });

describe('graph helpers', () => {
  it('BFS gate path, both trivial and multi-hop', () => {
    expect(gatePath(adj, 'X1-AA', 'X1-AA')).toEqual(['X1-AA']);
    expect(gatePath(adj, 'X1-AA', 'X1-CC')).toEqual(['X1-AA', 'X1-BB', 'X1-CC']);
    expect(gatePath(adj, 'X1-AA', 'X1-ZZ')).toBeNull();
  });
  it('systemGates maps each system to its own gate waypoint', () => {
    expect(gates.get('X1-AA')).toBe('X1-AA-G1');
    expect(gates.get('X1-BB')).toBe('X1-BB-G1');
  });
  it('flowIsRelocation is true only when every hop is trade-less', () => {
    expect(flowIsRelocation(flow({ remainingHops: [hop('X1-BB-M1', 'X1-BB', 0, [])] }))).toBe(true);
    expect(flowIsRelocation(flow({ remainingHops: [hop('X1-BB-M1', 'X1-BB')] }))).toBe(false);
    expect(flowIsRelocation(flow({ remainingHops: [] }))).toBe(false);
  });
});

describe('projectFlowMotion', () => {
  it('dwells in orbit when the objective is intra-system', () => {
    const f = flow({ remainingHops: [hop('X1-AA-M2', 'X1-AA')] });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('dwell');
    const r = Math.hypot(m.x - 0, m.y - 0);
    expect(r).toBeGreaterThan(1);
    expect(r).toBeLessThan(20);
  });

  it('outbound half: in-transit toward own gate maps to [0, 0.5]', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-60), arrivesAt: iso(60) },
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 420)],
      shipNav: nav({ status: 'IN_TRANSIT', waypointSymbol: 'X1-AA-G1', departureTime: iso(-60), arrivalTime: iso(60) }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('glide');
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.25, 5); // t=0.5 → s=0.25
    expect(m.x).toBeCloseTo(250, 0);
    expect(m.bearingRad).toBeCloseTo(0, 5); // due +x
  });

  it('holds at 0.47 parked at own gate pre-jump', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-120), arrivesAt: iso(-10) },
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 420)],
      shipNav: nav({ status: 'IN_ORBIT', waypointSymbol: 'X1-AA-G1' }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.phase).toBeCloseTo(0.47, 5);
  });

  it('arrival half: in-transit FROM own gate completes the incoming edge [0.5, 1]', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-300), arrivesAt: iso(100) },
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 420)],
      shipNav: nav({
        status: 'IN_TRANSIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-M1',
        originSymbol: 'X1-BB-G1', departureTime: iso(-60), arrivalTime: iso(40),
      }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.5 + 0.5 * 0.6, 5);
    expect(m.x).toBeCloseTo(800, 0);
  });

  it('holds at 0.53 on the incoming edge during post-jump cooldown', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-CC-M1', departedAt: iso(-300), arrivesAt: iso(600) },
      remainingHops: [hop('X1-CC-M1', 'X1-CC', 900)],
      shipNav: nav({ status: 'IN_ORBIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-G1' }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    // Pass-through at X1-BB (came from AA, heading CC): held just past the AA→BB midpoint.
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.53, 5);
  });

  it('true warp renders directly on the origin→destination edge', () => {
    const f = flow({
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 0)],
      shipNav: nav({
        status: 'IN_TRANSIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-M1',
        originSymbol: 'X1-AA-M1', departureTime: iso(-50), arrivalTime: iso(50),
      }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB', mode: 'glide' });
    expect(m.phase).toBeCloseTo(0.5, 5);
    expect(m.x).toBeCloseTo(500, 0);
  });

  it('falls back to currentLeg timestamp lerp when shipNav is missing', () => {
    const f = flow({
      shipNav: null,
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-50), arrivesAt: iso(50) },
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('glide');
    expect(m.x).toBeCloseTo(500, 0);
  });

  it('returns null when the hull system has no known position', () => {
    const f = flow({ shipNav: nav({ systemSymbol: 'X1-ZZ' }) });
    expect(projectFlowMotion(f, adj, gates, pos, NOW, 1)).toBeNull();
  });
});

describe('planRoutePolylines', () => {
  it('expands stop pairs through the gate graph with deadhead flags', () => {
    const f = flow({
      shipNav: nav({ systemSymbol: 'X1-AA' }),
      remainingHops: [hop('X1-CC-M1', 'X1-CC', 900), hop('X1-AA-M9', 'X1-AA', 900, [])],
    });
    const segs = planRoutePolylines(f, adj, pos);
    expect(segs).toHaveLength(2);
    // AA→CC runs through BB: 3 points = 6 numbers.
    expect(segs[0].points).toEqual([0, 0, 1000, 0, 2000, 0]);
    expect(segs[0].deadhead).toBe(false);
    // CC→AA return leg is trade-less → deadhead.
    expect(segs[1].deadhead).toBe(true);
  });
});
```

- [ ] **Step 2: Run to verify it fails**

```bash
rtk npx vitest run src/components/flows/__tests__/flowMotion.test.ts
```
Expected: FAIL — module `../flowMotion` does not exist.

- [ ] **Step 3: Implement**

```ts
// visualizer/web/src/components/flows/flowMotion.ts
import type { LiveFlow, TopologyResponse } from '../../types/flows';
import { systemOf, type Point } from './flowGeometry';

export interface MotionState {
  x: number;
  y: number;
  bearingRad: number;          // travel bearing (orbit tangent when dwelling)
  mode: 'dwell' | 'glide';
  fromSystem: string;          // glide: rendered edge endpoints (= fromSystem when dwelling)
  toSystem: string;
  phase: number;               // 0..1 along the rendered edge; 0 when dwelling
}

export type Adjacency = Map<string, string[]>;

// Edge X→Y is a progress bar for the whole crossing: [0,0.5] inside X to its
// gate, 0.5 the instant jump, [0.5,1] inside Y from its gate to the stop.
const PRE_JUMP_HOLD = 0.47;    // parked at own gate, jump pending
const POST_JUMP_HOLD = 0.53;   // parked at own gate, just arrived (cooldown)
const PRE_DEPARTURE = 0.04;    // parked away from the gate, departure pending
const ORBIT_RADIUS_PX = 9;     // dwell orbit, screen-stable (÷ scale)
const ORBIT_RAD_PER_SEC = 0.35;

export function buildAdjacency(topology: TopologyResponse): Adjacency {
  const adj: Adjacency = new Map();
  const push = (a: string, b: string) => {
    const arr = adj.get(a);
    if (arr) { if (!arr.includes(b)) arr.push(b); } else adj.set(a, [b]);
  };
  for (const e of topology.edges) {
    if (e.from === e.to) continue;
    push(e.from, e.to);
    push(e.to, e.from);
  }
  return adj;
}

// Each system's own jump-gate waypoint. gate_edges.gate_waypoint carries the
// CONNECTED (to-side) system's gate, and every system has one gate, so any
// edge INTO a system names that system's gate.
export function buildSystemGates(topology: TopologyResponse): Map<string, string> {
  const gates = new Map<string, string>();
  for (const e of topology.edges) {
    if (e.gateWaypoint && !gates.has(e.to)) gates.set(e.to, e.gateWaypoint);
  }
  return gates;
}

// BFS shortest path over the gate graph (systems inclusive of both endpoints).
export function gatePath(adj: Adjacency, from: string, to: string): string[] | null {
  if (from === to) return [from];
  const prev = new Map<string, string>([[from, from]]);
  const queue = [from];
  while (queue.length > 0) {
    const cur = queue.shift()!;
    for (const nxt of adj.get(cur) ?? []) {
      if (prev.has(nxt)) continue;
      prev.set(nxt, cur);
      if (nxt === to) {
        const path = [to];
        let p = to;
        while (p !== from) { p = prev.get(p)!; path.unshift(p); }
        return path;
      }
      queue.push(nxt);
    }
  }
  return null;
}

export interface Stop {
  waypoint: string;
  system: string;
  travelSeconds: number;
  deadhead: boolean; // no tranches at this stop (pure repositioning / return leg)
}

// The flow's remaining stop sequence: the current leg's destination first
// (unknown tranches → not deadhead), then every remaining hop.
export function buildStops(flow: LiveFlow): Stop[] {
  const stops: Stop[] = [];
  if (flow.currentLeg) {
    stops.push({ waypoint: flow.currentLeg.to, system: systemOf(flow.currentLeg.to), travelSeconds: 0, deadhead: false });
  }
  for (const h of flow.remainingHops) {
    stops.push({
      waypoint: h.waypoint,
      system: h.system || systemOf(h.waypoint),
      travelSeconds: h.travelSeconds || 0,
      deadhead: h.tranches.length === 0,
    });
  }
  return stops;
}

// A flow that only repositions: it has hops and none of them trade.
export function flowIsRelocation(flow: LiveFlow): boolean {
  return flow.remainingHops.length > 0 && flow.remainingHops.every((h) => h.tranches.length === 0);
}

const clamp01 = (v: number) => Math.max(0, Math.min(1, v));

function navProgress(departureIso: string, arrivalIso: string, nowMs: number): number {
  const dep = Date.parse(departureIso);
  const arr = Date.parse(arrivalIso);
  if (Number.isNaN(dep) || Number.isNaN(arr)) return 0;
  return clamp01((nowMs - dep) / Math.max(arr - dep, 1));
}

function hashShip(sym: string): number {
  let h = 2166136261;
  for (let i = 0; i < sym.length; i++) {
    h ^= sym.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

function glide(a: Point, b: Point, s: number, fromSystem: string, toSystem: string): MotionState {
  return {
    x: a.x + (b.x - a.x) * s,
    y: a.y + (b.y - a.y) * s,
    bearingRad: Math.atan2(b.y - a.y, b.x - a.x),
    mode: 'glide',
    fromSystem,
    toSystem,
    phase: s,
  };
}

function dwell(system: string, p: Point, ship: string, nowMs: number, scale: number): MotionState {
  const ang = (nowMs / 1000) * ORBIT_RAD_PER_SEC + (hashShip(ship) % 628) / 100;
  const r = ORBIT_RADIUS_PX / Math.max(scale, 1e-6);
  return {
    x: p.x + Math.cos(ang) * r,
    y: p.y + Math.sin(ang) * r,
    bearingRad: ang + Math.PI / 2, // orbit tangent
    mode: 'dwell',
    fromSystem: system,
    toSystem: system,
    phase: 0,
  };
}

// Galaxy-space kinematics for one flow. Nav truth (PG ships) grounds every
// branch; planned data only shapes the route. Null = nothing renderable.
export function projectFlowMotion(
  flow: LiveFlow,
  adj: Adjacency,
  systemGates: Map<string, string>,
  systemPos: Map<string, Point>,
  nowMs: number,
  scale: number,
): MotionState | null {
  const nav = flow.shipNav;
  const stops = buildStops(flow);

  // Legacy fallback: no nav row — lerp the current leg between its endpoint
  // systems on the daemon's best-effort timestamps (old projectFlowShip).
  if (!nav) {
    const leg = flow.currentLeg;
    if (!leg) return null;
    const fromSys = systemOf(leg.from);
    const toSys = systemOf(leg.to);
    const a = systemPos.get(fromSys);
    const b = systemPos.get(toSys);
    if (!a || !b) return null;
    if (fromSys === toSys) return dwell(fromSys, a, flow.ship, nowMs, scale);
    return glide(a, b, navProgress(leg.departedAt, leg.arrivesAt, nowMs), fromSys, toSys);
  }

  const hullSystem = nav.systemSymbol || (flow.currentLeg ? systemOf(flow.currentLeg.from) : '');
  const hullPos = hullSystem ? systemPos.get(hullSystem) : undefined;
  if (!hullSystem || !hullPos) return null;

  const inTransit = nav.status === 'IN_TRANSIT' && Boolean(nav.departureTime) && Boolean(nav.arrivalTime);

  // True warp: a single nav leg that itself crosses systems.
  if (inTransit && nav.originSymbol) {
    const originSys = systemOf(nav.originSymbol);
    const destSys = nav.waypointSymbol ? systemOf(nav.waypointSymbol) : hullSystem;
    if (originSys !== destSys) {
      const wa = systemPos.get(originSys);
      const wb = systemPos.get(destSys);
      if (wa && wb) return glide(wa, wb, navProgress(nav.departureTime!, nav.arrivalTime!, nowMs), originSys, destSys);
    }
  }

  const target = stops.length > 0 ? stops[0].system : hullSystem;
  if (target === hullSystem) return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);

  // The crossing runs from the previous stop's system to the target.
  const crossingStart = flow.currentLeg ? systemOf(flow.currentLeg.from) : hullSystem;
  let path = gatePath(adj, crossingStart, target);
  let i = path ? path.indexOf(hullSystem) : -1;
  if (!path || i === -1) {
    path = gatePath(adj, hullSystem, target);
    i = 0;
  }
  if (!path || path.length < 2) return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);

  const ownGate = systemGates.get(hullSystem);
  const edgeOf = (fromIdx: number): { a: Point; b: Point; from: string; to: string } | null => {
    const from = path![fromIdx];
    const to = path![fromIdx + 1];
    const a = systemPos.get(from);
    const b = systemPos.get(to);
    return a && b ? { a, b, from, to } : null;
  };

  if (inTransit) {
    const t = navProgress(nav.departureTime!, nav.arrivalTime!, nowMs);
    if (nav.originSymbol === ownGate && i > 0) {
      const e = edgeOf(i - 1); // arrival half: completing the incoming edge
      if (e) return glide(e.a, e.b, 0.5 + 0.5 * t, e.from, e.to);
    }
    if (i < path.length - 1) {
      const e = edgeOf(i); // outbound half (gate-bound, or any detour leg)
      if (e) return glide(e.a, e.b, 0.5 * t, e.from, e.to);
    }
    return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);
  }

  // Parked while a crossing is pending.
  if (i >= path.length - 1) return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);
  if (nav.waypointSymbol === ownGate) {
    if (i > 0) {
      const e = edgeOf(i - 1); // cooldown: just arrived through our gate
      if (e) return glide(e.a, e.b, POST_JUMP_HOLD, e.from, e.to);
    }
    const e = edgeOf(i); // first hop of the journey: jump pending
    return e ? glide(e.a, e.b, PRE_JUMP_HOLD, e.from, e.to) : null;
  }
  const e = edgeOf(i);
  return e ? glide(e.a, e.b, PRE_DEPARTURE, e.from, e.to) : null;
}

// Planned route as gate-graph polylines, one entry per stop transition that
// changes system (consecutive same-system stops collapse). deadhead mirrors
// the DESTINATION stop's flag.
export function planRoutePolylines(
  flow: LiveFlow,
  adj: Adjacency,
  systemPos: Map<string, Point>,
): { points: number[]; deadhead: boolean }[] {
  const stops = buildStops(flow);
  const startSystem = flow.shipNav?.systemSymbol || (flow.currentLeg ? systemOf(flow.currentLeg.from) : stops[0]?.system);
  if (!startSystem) return [];

  const out: { points: number[]; deadhead: boolean }[] = [];
  let prev = startSystem;
  for (const stop of stops) {
    if (stop.system === prev) continue;
    const path = gatePath(adj, prev, stop.system);
    prev = stop.system;
    if (!path) continue;
    const points: number[] = [];
    for (const sys of path) {
      const p = systemPos.get(sys);
      if (!p) { points.length = 0; break; }
      points.push(p.x, p.y);
    }
    if (points.length >= 4) out.push({ points, deadhead: stop.deadhead });
  }
  return out;
}
```

- [ ] **Step 4: Run the tests**

```bash
rtk npx vitest run src/components/flows/__tests__/flowMotion.test.ts
```
Expected: PASS (all 12).

- [ ] **Step 5: Commit**

```bash
rtk git add src/components/flows/flowMotion.ts src/components/flows/__tests__/flowMotion.test.ts
rtk git commit -m "feat(viz-web): nav-grounded galaxy motion model — gate-path glides, holds, warp, plan polylines (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: profit rings + oriented ship glyphs

**Files:**
- Create: `visualizer/web/src/components/flows/profitRing.ts`
- Test: `visualizer/web/src/components/flows/__tests__/profitRing.test.ts`
- Modify: `visualizer/web/src/components/flows/FlowShipLayer.tsx` (full rewrite below)

**Interfaces:**
- Consumes: `projectFlowMotion` etc. from Task 8; `LiveFlow.realized`/`projected` from Task 7.
- Produces: `ringSpec(realizedNet, projectedProfit): { fill, color, underGlow, overshoot }`; `FlowShipLayer` props change to `{ flows, adj, systemGates, systemPos, nowMs, scale, selectedFlowId, onSelect, onHover }` (Task 11 passes them).

Ring semantics (spec §7): projection unknown/≤0 → empty dim track; net < 0 → empty ring + red under-glow (capital committed); 0 ≤ ratio < 0.6 → amber fill; 0.6 ≤ ratio < 1 → green fill; ratio ≥ 1 → full green + star overshoot pulse. No Konva component test — visual truth is Task 13's screenshot gate; logic lives in the pure helpers.

- [ ] **Step 1: Write the failing ring tests**

```ts
// visualizer/web/src/components/flows/__tests__/profitRing.test.ts
import { describe, it, expect } from 'vitest';
import { ringSpec } from '../profitRing';
import { NOIR } from '../../../theme/noir';

describe('ringSpec', () => {
  it('unknown or non-positive projection: empty dim track', () => {
    expect(ringSpec(1000, null)).toEqual({ fill: 0, color: NOIR.dim, underGlow: null, overshoot: false });
    expect(ringSpec(1000, 0)).toEqual({ fill: 0, color: NOIR.dim, underGlow: null, overshoot: false });
  });
  it('negative net: capital committed — empty ring, red under-glow', () => {
    expect(ringSpec(-42000, 100000)).toEqual({ fill: 0, color: NOIR.warn, underGlow: NOIR.bad, overshoot: false });
  });
  it('partial fill: amber below 0.6, green from 0.6', () => {
    expect(ringSpec(30000, 100000)).toMatchObject({ fill: 0.3, color: NOIR.warn });
    expect(ringSpec(70000, 100000)).toMatchObject({ fill: 0.7, color: NOIR.good });
  });
  it('overshoot: clamped full, flagged', () => {
    expect(ringSpec(120000, 100000)).toEqual({ fill: 1, color: NOIR.good, underGlow: null, overshoot: true });
  });
});
```

- [ ] **Step 2: Run to verify it fails**

```bash
rtk npx vitest run src/components/flows/__tests__/profitRing.test.ts
```
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the helper**

```ts
// visualizer/web/src/components/flows/profitRing.ts
import { NOIR } from '../../theme/noir';

export interface RingSpec {
  fill: number;              // 0..1 arc sweep
  color: string;
  underGlow: string | null;  // red while capital is committed (net < 0)
  overshoot: boolean;        // realized beat the projection
}

// Realized-vs-projected as a glanceable ring. Realized is a SIGNED sum, so a
// young tour is negative (cargo bought, nothing sold): that renders as an
// empty ring with a red under-glow, not as fake progress.
export function ringSpec(realizedNet: number | null | undefined, projectedProfit: number | null | undefined): RingSpec {
  const projected = projectedProfit ?? 0;
  if (projected <= 0) return { fill: 0, color: NOIR.dim, underGlow: null, overshoot: false };
  const net = realizedNet ?? 0;
  if (net < 0) return { fill: 0, color: NOIR.warn, underGlow: NOIR.bad, overshoot: false };
  const ratio = net / projected;
  if (ratio >= 1) return { fill: 1, color: NOIR.good, underGlow: null, overshoot: true };
  return { fill: ratio, color: ratio < 0.6 ? NOIR.warn : NOIR.good, underGlow: null, overshoot: false };
}
```

- [ ] **Step 4: Rewrite `FlowShipLayer.tsx`**

Replace the file's contents with:

```tsx
import { memo } from 'react';
import { Group, Line, Arc, Ring, Circle, Text } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import type { Point } from './flowGeometry';
import { projectFlowMotion, type Adjacency } from './flowMotion';
import { ringSpec } from './profitRing';
import { NOIR, noirAlpha } from '../../theme/noir';

interface Props {
  flows: LiveFlow[];
  adj: Adjacency;
  systemGates: Map<string, string>;
  systemPos: Map<string, Point>;
  nowMs: number;
  scale: number;
  selectedFlowId: string | null;
  onSelect: (containerId: string) => void;
  onHover: (containerId: string | null) => void;
}

const PROGRAM_COLOR: Record<LiveFlow['program'], string> = {
  tour: NOIR.star,
  'trade-route': NOIR.accent,
  arb: NOIR.good,
};

// Oriented hull glyphs: a wedge nosing along the travel bearing with a fading
// comet trail, wrapped in an unrotated progress ring (realized ÷ projected).
// Position/bearing come from the nav-grounded motion model; the raf clock
// makes glides continuous between the 5s polls.
export const FlowShipLayer = memo(function FlowShipLayer({
  flows, adj, systemGates, systemPos, nowMs, scale, selectedFlowId, onSelect, onHover,
}: Props) {
  return (
    <Group>
      {flows.map((flow) => {
        const m = projectFlowMotion(flow, adj, systemGates, systemPos, nowMs, scale);
        if (!m) return null;
        const color = PROGRAM_COLOR[flow.program];
        const selected = flow.containerId === selectedFlowId;
        const u = 1 / Math.max(scale, 1e-6); // 1 on-screen px in stage units
        const r = Math.max(2, 4 * u);
        const ring = ringSpec(flow.realized?.net, flow.projected?.profit ?? null);
        const rotationDeg = (m.bearingRad * 180) / Math.PI;
        const pulse = 0.4 + 0.25 * Math.sin(nowMs / 300);
        return (
          <Group key={`ship-${flow.containerId}`} x={m.x} y={m.y}>
            {/* rotated body: wedge + trail */}
            <Group rotation={rotationDeg} listening={false}>
              <Line points={[-14 * u, 0, -5 * u, 0]} stroke={noirAlpha(color, 0.35)} strokeWidth={1.6 * u} lineCap="round" listening={false} />
              <Line points={[-22 * u, 0, -14 * u, 0]} stroke={noirAlpha(color, 0.15)} strokeWidth={1 * u} lineCap="round" listening={false} />
              <Line
                points={[6 * u, 0, -4 * u, 3.5 * u, -4 * u, -3.5 * u]}
                closed
                fill={color}
                stroke={noirAlpha(NOIR.ink, 0.5)}
                strokeWidth={0.4 * u}
                listening={false}
              />
            </Group>

            {/* unrotated dress: ring, under-glow, selection, overshoot pulse */}
            <Ring innerRadius={r + 2.5 * u} outerRadius={r + 4 * u} fill={noirAlpha(NOIR.ink, 0.14)} listening={false} />
            {ring.underGlow && (
              <Ring innerRadius={r + 2.5 * u} outerRadius={r + 4 * u} fill={noirAlpha(ring.underGlow, 0.4)} listening={false} />
            )}
            {ring.fill > 0 && (
              <Arc
                innerRadius={r + 2.5 * u}
                outerRadius={r + 4 * u}
                angle={ring.fill * 360}
                rotation={-90}
                fill={ring.color}
                listening={false}
              />
            )}
            {ring.overshoot && (
              <Ring innerRadius={r + 5 * u} outerRadius={r + 6 * u} fill={noirAlpha(NOIR.star, pulse)} listening={false} />
            )}
            {selected && <Ring innerRadius={r + 7 * u} outerRadius={r + 7.8 * u} fill={noirAlpha(NOIR.ink, 0.9)} listening={false} />}

            {/* hit target */}
            <Circle
              radius={r + 6 * u}
              opacity={0}
              onMouseEnter={(e) => {
                const c = e.target.getStage()?.container();
                if (c) c.style.cursor = 'pointer';
                onHover(flow.containerId);
              }}
              onMouseLeave={(e) => {
                const c = e.target.getStage()?.container();
                if (c) c.style.cursor = 'default';
                onHover(null);
              }}
              onClick={() => onSelect(flow.containerId)}
              onTouchStart={() => onSelect(flow.containerId)}
            />
            {scale > 0.4 && (
              <Text text={flow.ship} fontSize={Math.max(6, 9 * u)} fill={NOIR.muted} x={r + 6 * u} y={-r} listening={false} />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
```

- [ ] **Step 5: Run ring tests + typecheck**

```bash
rtk npx vitest run src/components/flows/__tests__/profitRing.test.ts && rtk npx tsc --noEmit
```
Expected: ring tests PASS; tsc reports ONLY the call-site mismatch in `FlowGalaxyScene.tsx` (old props) — that call site is rewritten in Task 11, so for now the scene passes the new props with locally-built `useMemo` values, or Task 9 and 11 land in the same working session. If executing strictly task-by-task: add the two `useMemo` lines from Task 11 Step 3 (adjacency + gates) and the `onHover={() => {}}` stub to `FlowGalaxyScene.tsx` now, and let Task 11 replace them.

- [ ] **Step 6: Commit**

```bash
rtk git add src/components/flows/profitRing.ts src/components/flows/__tests__/profitRing.test.ts src/components/flows/FlowShipLayer.tsx src/components/flows/FlowGalaxyScene.tsx
rtk git commit -m "feat(viz-web): oriented wedge glyphs with comet trails + realized/projected progress rings (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: plan paths — gate-graph polylines, gradients, deadhead, anchor

**Files:**
- Modify: `visualizer/web/src/components/flows/FlowPlanPath.tsx` (full rewrite below)

**Interfaces:**
- Consumes: `planRoutePolylines`, `buildStops`, `flowIsRelocation` (Task 8); `flow.closed` (Task 7).
- Produces: `FlowPlanPath` props become `{ flow, adj, systemPos, scale }`.

- [ ] **Step 1: Rewrite the component**

```tsx
import { memo } from 'react';
import { Group, Line, Circle } from 'react-konva';
import type { LiveFlow } from '../../types/flows';
import type { Point } from './flowGeometry';
import { planRoutePolylines, buildStops, flowIsRelocation, type Adjacency } from './flowMotion';
import { NOIR, noirAlpha } from '../../theme/noir';

interface Props {
  flow: LiveFlow;
  adj: Adjacency;
  systemPos: Map<string, Point>;
  scale: number;
}

// Planned intent over the gate graph. Profitable transitions carry a
// directional gradient (bright toward the next stop); trade-less transitions
// (closed-tour return legs, placement relocations) render cool and dashed.
// A closed tour's final stop is its anchor — ringed so the loop reads.
export const FlowPlanPath = memo(function FlowPlanPath({ flow, adj, systemPos, scale }: Props) {
  const segments = planRoutePolylines(flow, adj, systemPos);
  if (segments.length === 0) return null;
  const u = 1 / Math.max(scale, 1e-6);
  const relocation = flowIsRelocation(flow);
  const stops = buildStops(flow);
  const anchorSystem = flow.closed && stops.length > 0 ? stops[stops.length - 1].system : null;
  const anchorPos = anchorSystem ? systemPos.get(anchorSystem) : undefined;

  return (
    <Group listening={false}>
      {segments.map((seg, i) => {
        const cool = seg.deadhead || relocation;
        const last = seg.points.length;
        if (cool) {
          return (
            <Line
              key={`plan-${flow.containerId}-${i}`}
              points={seg.points}
              stroke={noirAlpha(NOIR.dim, 0.6)}
              strokeWidth={Math.max(0.5, 1.1 * u)}
              dash={[3 * u, 5 * u]}
              lineCap="round"
              opacity={Math.max(0.25, 0.55 - i * 0.08)}
              listening={false}
            />
          );
        }
        return (
          <Line
            key={`plan-${flow.containerId}-${i}`}
            points={seg.points}
            strokeLinearGradientStartPoint={{ x: seg.points[0], y: seg.points[1] }}
            strokeLinearGradientEndPoint={{ x: seg.points[last - 2], y: seg.points[last - 1] }}
            strokeLinearGradientColorStops={[0, noirAlpha(NOIR.accentSoft, 0.12), 1, noirAlpha(NOIR.accentSoft, 0.75)]}
            strokeWidth={Math.max(0.5, 1.4 * u)}
            lineCap="round"
            opacity={Math.max(0.25, 0.7 - i * 0.08)}
            listening={false}
          />
        );
      })}

      {flow.remainingHops.map((hop, i) => {
        const p = systemPos.get(hop.system);
        if (!p) return null;
        const dead = hop.tranches.length === 0;
        return (
          <Circle
            key={`hop-${flow.containerId}-${i}`}
            x={p.x}
            y={p.y}
            radius={Math.max(1.5, 3 * u)}
            fill={noirAlpha(dead ? NOIR.dim : NOIR.accentSoft, dead ? 0.35 : 0.5)}
            listening={false}
          />
        );
      })}

      {anchorPos && (
        <Circle
          x={anchorPos.x}
          y={anchorPos.y}
          radius={10 * u}
          stroke={NOIR.warn}
          strokeWidth={1 * u}
          dash={[2.5 * u, 2.5 * u]}
          opacity={0.85}
          listening={false}
        />
      )}
    </Group>
  );
});
```

- [ ] **Step 2: Fix the call site + verify**

In `FlowGalaxyScene.tsx`, the plan-path map becomes `<FlowPlanPath key={…} flow={f} adj={adj} systemPos={systemPos} scale={scale} />` (the `adj` memo exists from Task 9's stub or Task 11 proper).

```bash
rtk npx tsc --noEmit && rtk npx vitest run
```
Expected: clean; suites PASS (flowMotion tests already cover `planRoutePolylines`).

- [ ] **Step 3: Commit**

```bash
rtk git add src/components/flows/FlowPlanPath.tsx src/components/flows/FlowGalaxyScene.tsx
rtk git commit -m "feat(viz-web): gate-graph plan paths — directional gradients, deadhead styling, closed-tour anchor ring (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: scene integration — backdrop, activity nodes, system lanes, hover/focus, layer toggles

**Files:**
- Modify: `visualizer/web/src/store/flowStore.ts`, `visualizer/web/src/store/__tests__/flowStore.test.ts` (append)
- Modify: `visualizer/web/src/components/flows/FlowLaneLayer.tsx` (prop change), `visualizer/web/src/components/flows/FlowGalaxyScene.tsx`

**Interfaces:**
- Consumes: Tasks 6-10 outputs; `AmbientBackdrop({ pan })`; `useRafClock`.
- Produces: store gains `hoveredFlowId`, `focusFlowId`, `layerToggles {lanes,paths,ships}` + actions `hoverFlow(id|null)`, `requestFocus(id)`, `clearFocus()`, `toggleLayer(key)`. `FlowLaneLayer` prop `lanes: LanesResponse | null` becomes `records: LaneRecord[] | null`.

- [ ] **Step 1: Write the failing store tests**

Append to `store/__tests__/flowStore.test.ts`:

```ts
describe('galaxy view state', () => {
  it('hover, focus, and layer toggles round-trip', () => {
    const s = useFlowStore.getState();
    s.hoverFlow('tour-1');
    expect(useFlowStore.getState().hoveredFlowId).toBe('tour-1');
    s.hoverFlow(null);
    expect(useFlowStore.getState().hoveredFlowId).toBeNull();

    s.requestFocus('tour-2');
    expect(useFlowStore.getState().focusFlowId).toBe('tour-2');
    s.clearFocus();
    expect(useFlowStore.getState().focusFlowId).toBeNull();

    expect(useFlowStore.getState().layerToggles).toEqual({ lanes: true, paths: true, ships: true });
    s.toggleLayer('lanes');
    expect(useFlowStore.getState().layerToggles.lanes).toBe(false);
    s.toggleLayer('lanes');
    expect(useFlowStore.getState().layerToggles.lanes).toBe(true);
  });
});
```

Also append the feed-loss freeze test (spec §8: ships freeze, never vanish):

```ts
  it('freezes the last live flows across feed loss and clears on recovery', () => {
    const s = useFlowStore.getState();
    const mk = (feedLost: boolean, flows: any[]) => ({ flows, generatedAt: new Date().toISOString(), feedLost, lastPlanAt: null });
    s.setLive(mk(false, [{ containerId: 'a' }]) as any);
    s.setLive(mk(true, []) as any);
    expect(useFlowStore.getState().staleFlows?.[0]?.containerId).toBe('a');
    expect(useFlowStore.getState().freezeAtMs).not.toBeNull();
    s.setLive(mk(true, []) as any); // repeated loss keeps the ORIGINAL snapshot
    expect(useFlowStore.getState().staleFlows?.[0]?.containerId).toBe('a');
    s.setLive(mk(false, []) as any);
    expect(useFlowStore.getState().staleFlows).toBeNull();
    expect(useFlowStore.getState().freezeAtMs).toBeNull();
  });
```

Run `rtk npx vitest run src/store/__tests__/flowStore.test.ts` — expect FAIL (unknown actions/fields).

- [ ] **Step 2: Extend the store**

In `flowStore.ts`, add to `FlowState`:

```ts
  hoveredFlowId: string | null;
  focusFlowId: string | null;    // one-shot camera request; scene clears it
  layerToggles: { lanes: boolean; paths: boolean; ships: boolean };
  staleFlows: LiveFlow[] | null; // last live flows, frozen while feedLost
  freezeAtMs: number | null;     // clock value the frozen render pins to

  hoverFlow: (containerId: string | null) => void;
  requestFocus: (containerId: string) => void;
  clearFocus: () => void;
  toggleLayer: (key: 'lanes' | 'paths' | 'ships') => void;
```

(import `LiveFlow` alongside the existing type imports) and change the creator — `setLive` gains the freeze logic, plus the new fields/actions:

```ts
  hoveredFlowId: null,
  focusFlowId: null,
  layerToggles: { lanes: true, paths: true, ships: true },
  staleFlows: null,
  freezeAtMs: null,

  // lastPlanAt is sticky; staleFlows freezes the last real snapshot the moment
  // the feed drops (never fabricate motion on stale intent — spec §8).
  setLive: (live) =>
    set((state) => ({
      live,
      error: null,
      lastPlanAt: live.lastPlanAt ?? state.lastPlanAt,
      ...(live.feedLost
        ? state.staleFlows
          ? {}
          : {
              staleFlows: state.live && !state.live.feedLost && state.live.flows.length > 0 ? state.live.flows : null,
              freezeAtMs: Date.now(),
            }
        : { staleFlows: null, freezeAtMs: null }),
    })),

  hoverFlow: (hoveredFlowId) => set({ hoveredFlowId }),
  requestFocus: (focusFlowId) => set({ focusFlowId }),
  clearFocus: () => set({ focusFlowId: null }),
  toggleLayer: (key) => set((state) => ({ layerToggles: { ...state.layerToggles, [key]: !state.layerToggles[key] } })),
```

Store tests now PASS.

- [ ] **Step 3: `FlowLaneLayer` prop change**

Replace the `Props` interface and destructure: `lanes: LanesResponse | null` → `records: LaneRecord[] | null` (import `LaneRecord` instead of `LanesResponse`); `if (!lanes) return null;` → `if (!records) return null;`; `lanes.lanes.map(...)` → `records.map(...)`. Nothing else changes — `laneEndpoints`/`systemOf` are identity on system symbols, so system-lane records render on nodes directly.

- [ ] **Step 4: Rewrite `FlowGalaxyScene.tsx`**

Keep the existing centering effect, wheel handler, and node/edge structure; apply these changes (resulting file):

1. Imports add: `useMemo`, `AmbientBackdrop` (`../AmbientBackdrop`), `buildAdjacency, buildSystemGates, projectFlowMotion` from `./flowMotion`.
2. Store reads add: `hoverFlow`, `focusFlowId`, `clearFocus`, `layerToggles`.
3. Memos + pan state after `nowMs`:

```tsx
  const adj = useMemo(() => (topology ? buildAdjacency(topology) : new Map<string, string[]>()), [topology]);
  const systemGates = useMemo(() => (topology ? buildSystemGates(topology) : new Map<string, string>()), [topology]);
  const activityBySystem = useMemo(() => {
    const m = new Map<string, number>();
    for (const a of lanes?.systemActivity ?? []) m.set(a.system, a.realizedProfit);
    return m;
  }, [lanes]);
  const [pan, setPan] = useState({ x: 0, y: 0 });
```

4. The centering effect additionally calls `setPan({ x: width / 2 - avgX * initial, y: height / 2 - avgY * initial });`; the wheel handler ends with `setPan({ x: stage.x(), y: stage.y() });`.
5. Camera focus effect (after the centering effect):

```tsx
  // One-shot camera ease to a focused flow (roster/card click), then release.
  useEffect(() => {
    if (!focusFlowId || !stageRef.current || !topology) return;
    const flow = flows.find((f) => f.containerId === focusFlowId);
    if (flow) {
      const m = projectFlowMotion(flow, adj, systemGates, systemPos, Date.now(), scale);
      if (m) {
        const stage = stageRef.current;
        stage.to({
          x: width / 2 - m.x * scale,
          y: height / 2 - m.y * scale,
          duration: 0.6,
          onFinish: () => setPan({ x: stage.x(), y: stage.y() }),
        });
      }
    }
    clearFocus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusFlowId]);
```

6. Render: backdrop behind the Stage; Stage gains `onDragMove`; layers honor toggles; nodes size by activity:

```tsx
    <div className="relative w-full h-full overflow-hidden" style={{ background: NOIR.bg0 }}>
      <AmbientBackdrop pan={pan} />
      <Stage
        ref={stageRef}
        width={width}
        height={height}
        draggable
        onWheel={handleWheel}
        onDragMove={(e) => {
          const stage = e.target.getStage();
          if (stage && e.target === stage) setPan({ x: stage.x(), y: stage.y() });
        }}
      >
```

`FlowLaneLayer` call becomes `{layerToggles.lanes && <FlowLaneLayer records={lanes ? lanes.systemLanes : null} systemPos={systemPos} scale={scale} nowMs={nowMs} />}`. The plan-path group is wrapped in `{layerToggles.paths && (…)}` with the Task 10 props. `FlowShipLayer` is wrapped in `{layerToggles.ships && (…)}` and passes `adj={adj} systemGates={systemGates} onHover={hoverFlow}`.

Node sizing replaces the fixed `nodeR`:

```tsx
              const activity = activityBySystem.get(s.symbol) ?? 0;
              const bump = activity !== 0 ? Math.min(5, Math.max(0, Math.log10(Math.abs(activity) + 10) - 3)) : 0;
              const nodeR = Math.max(2, (3 + bump) / scale);
```

and the non-home node fill becomes `noirAlpha(NOIR.nebulaCore, Math.min(1, 0.55 + 0.09 * bump))`.

- [ ] **Step 4b: feed-loss freeze + enter/exit fades (spec §6 lifecycle, §8 degradation)**

Scene reads add `staleFlows`, `freezeAtMs`, and derives the render inputs:

```tsx
  const feedLost = live?.feedLost ?? false;
  const renderFlows = feedLost && staleFlows ? staleFlows : flows;
  const clockMs = feedLost && freezeAtMs ? freezeAtMs : nowMs;   // frozen clock = frozen glides
  const staleOpacity = feedLost ? 0.45 : 1;
  const presence = useFlowPresence(renderFlows, clockMs);
```

Add the presence hook at the bottom of `FlowGalaxyScene.tsx` (module-local; `useRef` joins the react imports):

```tsx
// Enter/exit presence: new flows fade in over 2s; departed flows linger 2s
// fading out, rendered from their last snapshot.
function useFlowPresence(
  flows: LiveFlow[],
  nowMs: number,
): { flow: LiveFlow; opacity: number }[] {
  const ref = useRef(new Map<string, { flow: LiveFlow; enterAt: number; exitAt: number | null }>());
  const seen = new Set<string>();
  for (const f of flows) {
    seen.add(f.containerId);
    const cur = ref.current.get(f.containerId);
    if (cur) { cur.flow = f; cur.exitAt = null; }
    else ref.current.set(f.containerId, { flow: f, enterAt: nowMs, exitAt: null });
  }
  for (const [id, rec] of ref.current) {
    if (!seen.has(id) && rec.exitAt === null) rec.exitAt = nowMs;
    if (rec.exitAt !== null && nowMs - rec.exitAt > 2000) ref.current.delete(id);
  }
  const out: { flow: LiveFlow; opacity: number }[] = [];
  for (const rec of ref.current.values()) {
    const enter = Math.min(1, (nowMs - rec.enterAt) / 2000);
    const exit = rec.exitAt === null ? 1 : Math.max(0, 1 - (nowMs - rec.exitAt) / 2000);
    out.push({ flow: rec.flow, opacity: enter * exit });
  }
  return out;
}
```

(import `LiveFlow` in the scene's type imports). Wire it in:
- Plan paths: `{layerToggles.paths && presence.filter((p) => p.flow.remainingHops.length > 0).map((p) => (<Group key={`pp-${p.flow.containerId}`} opacity={p.opacity * staleOpacity} listening={false}><FlowPlanPath flow={p.flow} adj={adj} systemPos={systemPos} scale={scale} /></Group>))}`
- Ships: `FlowShipLayer` gains an optional prop `opacityById?: Map<string, number>` (default: everything 1) applied as `opacity={opacityById?.get(flow.containerId) ?? 1}` on each ship's outer `Group`; the scene passes `flows={presence.map((p) => p.flow)}`, `nowMs={clockMs}`, and `opacityById={new Map(presence.map((p) => [p.flow.containerId, p.opacity * staleOpacity]))}`.

- [ ] **Step 5: Verify**

```bash
rtk npx tsc --noEmit && rtk npx vitest run
```
Expected: clean typecheck, all suites PASS (`TradeFlowsView.layout.test.tsx` may need the new store defaults in its fixture store setup — mechanical).

- [ ] **Step 6: Commit**

```bash
rtk git add src/store/flowStore.ts src/store/__tests__/flowStore.test.ts src/components/flows/FlowLaneLayer.tsx src/components/flows/FlowGalaxyScene.tsx
rtk git commit -m "feat(viz-web): galaxy scene — nebula backdrop, activity nodes, system lanes, hover/focus, layer toggles (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: roster panel + detail card + page assembly

**Files:**
- Create: `visualizer/web/src/components/flows/TourRoster.tsx`
- Test: `visualizer/web/src/components/flows/__tests__/TourRoster.test.tsx` (create), `__tests__/FlowDetailPanel.test.tsx` (append)
- Modify: `visualizer/web/src/components/flows/FlowDetailPanel.tsx`, `visualizer/web/src/pages/TradeFlowsView.tsx`

**Interfaces:**
- Consumes: store actions from Task 11; `ringSpec` (Task 9); `flowIsRelocation` (Task 8).
- Produces: `<TourRoster flows={LiveFlow[]} lanes={LanesResponse|null} selectedFlowId onRowClick={(id)=>void} />`; `FlowDetailPanel` unchanged signature (`flow: LiveFlow | null`) but renders realized + ring.

- [ ] **Step 1: Write the failing tests**

`__tests__/TourRoster.test.tsx` (testing-library is already a web devDep — mirror `FlowDetailPanel.test.tsx`'s render helper):

```tsx
import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { TourRoster } from '../TourRoster';
import { mockLiveFlows, mockLanes } from '../../../mocks/mockFlows';

const NOW = Date.parse('2026-07-17T12:00:00Z');

describe('TourRoster', () => {
  const flows = mockLiveFlows(NOW).flows;
  const lanes = mockLanes('6h');

  it('renders one row per flow with projected and realized credits', () => {
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={() => {}} />);
    expect(screen.getByText('TORWIND-3')).toBeTruthy();
    expect(screen.getByText(/250,000/)).toBeTruthy();  // tour A projected
    expect(screen.getByText(/96,000/)).toBeTruthy();   // tour A realized
  });

  it('shows fleet totals in the header', () => {
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={() => {}} />);
    expect(screen.getByText(/Σ projected/i)).toBeTruthy();
    expect(screen.getByText(/Σ realized/i)).toBeTruthy();
  });

  it('badges closed loops and relocations', () => {
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={() => {}} />);
    expect(screen.getByText(/loop/i)).toBeTruthy();
    expect(screen.getByText(/relocation/i)).toBeTruthy();
  });

  it('row click reports the container id', () => {
    const onRowClick = vi.fn();
    render(<TourRoster flows={flows} lanes={lanes} selectedFlowId={null} onRowClick={onRowClick} />);
    fireEvent.click(screen.getByText('TORWIND-3'));
    expect(onRowClick).toHaveBeenCalledWith(flows[0].containerId);
  });
});
```

Append to `FlowDetailPanel.test.tsx`:

```tsx
  it('renders realized-so-far next to the projection', () => {
    const flow = mockLiveFlows(Date.now()).flows[0];
    render(<FlowDetailPanel flow={flow} />);
    expect(screen.getByText(/Realized so far/i)).toBeTruthy();
    expect(screen.getByText(/96,000/)).toBeTruthy();
  });
```

Run both files — expect FAIL (no TourRoster module; no realized section).

- [ ] **Step 2: Implement `TourRoster.tsx`**

```tsx
import { useState } from 'react';
import type { LiveFlow, LanesResponse } from '../../types/flows';
import { flowIsRelocation } from './flowMotion';
import { ringSpec } from './profitRing';
import { NOIR, noirAlpha } from '../../theme/noir';

const money = (n: number) => Math.round(n).toLocaleString('en-US');
const WINDOW_HOURS: Record<string, number> = { '1h': 1, '6h': 6, '24h': 24 };
const legEta = (iso: string) => {
  const ms = Date.parse(iso) - Date.now();
  if (Number.isNaN(ms)) return '—';
  if (ms <= 0) return 'arrived';
  const secs = Math.floor(ms / 1000);
  return `${String(Math.floor(secs / 60)).padStart(2, '0')}:${String(secs % 60).padStart(2, '0')}`;
};

interface Props {
  flows: LiveFlow[];
  lanes: LanesResponse | null;
  selectedFlowId: string | null;
  onRowClick: (containerId: string) => void;
}

function routeSummary(flow: LiveFlow): string {
  const systems: string[] = [];
  const push = (s: string) => { if (systems[systems.length - 1] !== s) systems.push(s); };
  if (flow.shipNav?.systemSymbol) push(flow.shipNav.systemSymbol);
  for (const h of flow.remainingHops) push(h.system);
  if (systems.length <= 1) return systems[0] ?? '—';
  if (flow.closed) return `${systems[0]} ⟳ (${systems.length} sys)`;
  return `${systems[0]} → ${systems[systems.length - 1]} (${systems.length} sys)`;
}

function badge(flow: LiveFlow): string {
  if (flowIsRelocation(flow)) return 'relocation';
  if (flow.program === 'tour') return flow.closed ? 'closed loop' : 'tour';
  return flow.program;
}

// Right-side roster: every active flow with projected vs realized, sorted by
// projected $/hr. Header carries fleet totals + the window's realized $/hr.
export function TourRoster({ flows, lanes, selectedFlowId, onRowClick }: Props) {
  const [collapsed, setCollapsed] = useState(false);
  const sorted = [...flows].sort((a, b) => (b.projected?.ratePerHour ?? 0) - (a.projected?.ratePerHour ?? 0));
  const totalProjected = flows.reduce((s, f) => s + (f.projected?.profit ?? 0), 0);
  const totalRealized = flows.reduce((s, f) => s + (f.realized?.net ?? 0), 0);
  const windowProfit = (lanes?.lanes ?? []).reduce((s, l) => s + l.realizedProfit, 0);
  const windowRate = lanes ? windowProfit / (WINDOW_HOURS[lanes.window] ?? 6) : 0;

  return (
    <div
      className="absolute top-4 right-4 w-72 max-h-[82vh] overflow-auto rounded-lg text-xs backdrop-blur"
      style={{ background: `${NOIR.panel}E6`, color: NOIR.ink, border: `1px solid ${NOIR.nebulaCore}` }}
    >
      <button
        className="w-full flex items-center justify-between px-3 py-2"
        style={{ color: NOIR.accent }}
        onClick={() => setCollapsed((c) => !c)}
      >
        <span className="uppercase tracking-wide">Active tours ({flows.length})</span>
        <span>{collapsed ? '▸' : '▾'}</span>
      </button>

      {!collapsed && (
        <>
          <div className="px-3 pb-2 grid grid-cols-2 gap-x-2" style={{ color: NOIR.muted }}>
            <span>Σ projected</span><span className="text-right" style={{ color: NOIR.ink }}>{money(totalProjected)}</span>
            <span>Σ realized</span><span className="text-right" style={{ color: totalRealized >= 0 ? NOIR.good : NOIR.bad }}>{money(totalRealized)}</span>
            <span>window $/hr</span><span className="text-right" style={{ color: NOIR.dim }}>{money(windowRate)}</span>
          </div>

          {sorted.map((f) => {
            const ring = ringSpec(f.realized?.net, f.projected?.profit ?? null);
            const fillPct = Math.round(ring.fill * 100);
            const selected = f.containerId === selectedFlowId;
            return (
              <div
                key={f.containerId}
                className="px-3 py-2 cursor-pointer border-t"
                style={{ borderColor: noirAlpha(NOIR.nebulaCore, 0.5), background: selected ? noirAlpha(NOIR.nebulaCore, 0.35) : 'transparent' }}
                onClick={() => onRowClick(f.containerId)}
              >
                <div className="flex justify-between">
                  <span className="font-mono" style={{ color: NOIR.ink }}>{f.ship}</span>
                  <span style={{ color: NOIR.accentSoft }}>{badge(f)}</span>
                </div>
                <div className="font-mono truncate" style={{ color: NOIR.dim }}>{routeSummary(f)}</div>
                {f.currentLeg && (
                  <div style={{ color: NOIR.warn }}>leg → {f.currentLeg.to} · ETA {legEta(f.currentLeg.arrivesAt)}</div>
                )}
                <div className="flex justify-between mt-1">
                  <span style={{ color: NOIR.muted }}>proj {f.projected ? money(f.projected.profit) : '—'}</span>
                  <span style={{ color: (f.realized?.net ?? 0) >= 0 ? NOIR.good : NOIR.bad }}>real {money(f.realized?.net ?? 0)}</span>
                </div>
                <div className="h-1 mt-1 rounded" style={{ background: noirAlpha(NOIR.ink, 0.12) }}>
                  <div className="h-1 rounded" style={{ width: `${fillPct}%`, background: ring.underGlow ?? ring.color }} />
                </div>
                {f.projected && (
                  <div className="text-right" style={{ color: NOIR.dim }}>{money(f.projected.ratePerHour)}/hr</div>
                )}
              </div>
            );
          })}
        </>
      )}
    </div>
  );
}
```

- [ ] **Step 3: Extend `FlowDetailPanel.tsx`**

Inside the `flow.projected` block (or standalone when projection is null), insert before the projected rows:

```tsx
      <div className="pt-2 border-t" style={{ borderColor: NOIR.nebulaCore }}>
        <div className="flex justify-between text-xs">
          <span style={{ color: NOIR.muted }}>Realized so far (net, incl. fuel)</span>
          <span style={{ color: flow.realized.net >= 0 ? NOIR.good : NOIR.bad }}>{money(flow.realized.net)}</span>
        </div>
        {flow.realized.lastEventAt && (
          <div className="text-xs text-right" style={{ color: NOIR.dim }}>last fill {eta(flow.realized.lastEventAt) === 'arrived' ? 'just now' : new Date(flow.realized.lastEventAt).toLocaleTimeString()}</div>
        )}
      </div>
```

No SVG ring in the panel — the roster bar and the on-map ring carry the visual semantics; the panel carries exact numbers. (The realized label says "incl. fuel" because the signed transaction sum nets refuels — true regardless of whether the solver projection does.)

- [ ] **Step 4: Assemble `TradeFlowsView.tsx`**

Add store reads `hoveredFlowId`, `requestFocus`, `layerToggles`, `toggleLayer`; mount the roster and toggles; hover wins over selection for the card:

```tsx
  const hoveredFlow = flows.find((f) => f.containerId === hoveredFlowId) ?? null;
  // …
  <FlowDetailPanel flow={hoveredFlow ?? selectedFlow} />
  <TourRoster
    flows={flows}
    lanes={lanes}
    selectedFlowId={selectedFlowId}
    onRowClick={(id) => { selectFlow(id); requestFocus(id); }}
  />
```

Next to the window switch, the layer toggles:

```tsx
      <div className="absolute bottom-4 left-28 flex gap-1 rounded p-1" style={{ background: NOIR.panel }}>
        {(['lanes', 'paths', 'ships'] as const).map((k) => (
          <button
            key={k}
            onClick={() => toggleLayer(k)}
            className="px-3 py-1 text-xs rounded capitalize"
            style={{
              background: layerToggles[k] ? NOIR.accent : 'transparent',
              color: layerToggles[k] ? NOIR.bg0 : NOIR.muted,
            }}
          >
            {k}
          </button>
        ))}
      </div>
```

Also update the map ship-click path: `FlowShipLayer`'s `onSelect` already routes through the store's `selectFlow` in the scene — clicking a hull additionally pins the card (existing behavior preserved).

- [ ] **Step 5: Verify + commit**

```bash
rtk npx tsc --noEmit && rtk npx vitest run
```
Expected: PASS.

```bash
rtk git add src/components/flows/TourRoster.tsx src/components/flows/__tests__/TourRoster.test.tsx src/components/flows/FlowDetailPanel.tsx src/components/flows/__tests__/FlowDetailPanel.test.tsx src/pages/TradeFlowsView.tsx
rtk git commit -m "feat(viz-web): tour roster with fleet totals, realized-aware detail card, hover/focus assembly (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13: on-screen verification (demo scenario, headless screenshots)

**Files:**
- Modify (only if the checklist fails): any Task 9-12 visual file; `visualizer/web/src/mocks/mockFlows.ts` tweaks for readability.

Visual features need ON-SCREEN verification, not just backing-store measurement (the invisible-nebula lesson). The Chrome extension is unavailable in this environment — use the headless Chrome CLI.

- [ ] **Step 1: Build + serve the demo scenario**

```bash
cd visualizer/web && VITE_USE_MOCK_API=true rtk npx vite build && rtk npx vite preview --port 4173 &
sleep 2
```

- [ ] **Step 2: Screenshot the galaxy**

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless --disable-gpu \
  --screenshot=/private/tmp/galaxy-view-check.png --window-size=1600,1000 \
  --virtual-time-budget=9000 --hide-scrollbars "http://localhost:4173/trade-flows"
```

Read `/private/tmp/galaxy-view-check.png` and verify EVERY item:

1. Nebula + starfield backdrop visible behind the graph (not flat `#04060D`).
2. Four system nodes at the mock's hand-placed coordinates; home system ringed + labeled.
3. System-level lanes glowing between nodes (not waypoint spaghetti); marching dashes.
4. Tour A: wedge glyph mid-glide on the NK36→KA42 edge, nose pointing along the edge, comet trail behind, amber partial ring.
5. Tour B: loop path through KA42→ZC66→UU57→NK36 with the dashed return leg (deadhead cool style) and the anchor ring on the final system; overshoot pulse on its dwelling hull.
6. Arb C: hull with empty ring + red under-glow.
7. Relocation D: fully dashed cool path; roster badges it "relocation".
8. Roster panel right side: 4 rows, fleet totals header, per-row bars matching ring colors.
9. Window switch + the three layer toggle buttons bottom-left.

- [ ] **Step 3: Second screenshot — motion advanced**

Re-run the Step 2 command with `--screenshot=/private/tmp/galaxy-view-check-2.png` ~30 s later and confirm Tour A's glyph has advanced along its edge (compare the two images). Toggle/hover/focus behavior is covered by the store unit tests and needs no headless interaction pass.

- [ ] **Step 4: Fix-and-repeat**

Any failed checklist item: fix the component, re-run Step 1-2, re-verify. Do not mark this task complete until all nine items pass on a fresh screenshot. Kill the background servers when done (`kill %1 %2` or TaskStop equivalents).

- [ ] **Step 5: Full-stack quality gates + commit**

```bash
cd ../../gobot && rtk go build ./... && rtk go test ./internal/adapters/flowfeed/... ./internal/application/trading/commands/ -run 'TestBuild' && rtk go test ./internal/infrastructure/database/ -run TestAutoMigrate
cd ../visualizer/server && rtk npx vitest run
cd ../web && rtk npx tsc --noEmit && rtk npx vitest run
rtk git add -A && rtk git commit -m "chore(viz): galaxy view on-screen verification fixes (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```
(Skip the commit if Step 4 needed no fixes.)

---

## Final validation & close-out

- [ ] Full suites one last time: gobot (`rtk go test ./...` — long; at minimum the three packages touched), server (`rtk npx vitest run`), web (`rtk npx tsc --noEmit && rtk npx vitest run`).
- [ ] `rtk bd close <sp-XXXX>` (the bead created before Task 1), plus `rtk bd create` follow-ups for anything discovered-but-deferred (e.g. the placement-engine opportunity layer seam).
- [ ] Merge/PR per superpowers:finishing-a-development-branch. Session-close protocol (repo rule): `rtk git pull --rebase && bd dolt push && rtk git push && rtk git status` must end "up to date with origin".

## Deferred by design (do NOT build)

- Opportunity heat layer (waits on sp-z7ng placement scores; toggle mechanism ships now).
- Ahead/behind-schedule display (data present via `travelSeconds`; no UI in v1).
- Durable tour persistence (Phase 2 of the 2026-07-10 design; feed-lost degradation is the mitigation).
- Waypoint-level animation (drilldown's job, unchanged).






