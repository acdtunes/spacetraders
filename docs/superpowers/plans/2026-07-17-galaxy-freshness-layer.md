# Galaxy Freshness Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Per-system market-freshness halos (continuous 0–100% solver-visibility ramp) + scout-post markers on the Galaxy View, and thinner route lines.

**Architecture:** One new viz-server endpoint (`/api/flows/freshness`, pure-util shaping + supertest), web wire (types/api/store/polling/mocks), one new Konva layer + a fourth layer toggle + one drilldown line, and two stroke-width tweaks. No gobot changes.

**Tech Stack:** Express + pg + vitest/supertest · React 18 + react-konva + zustand + vitest.

**Spec:** `docs/superpowers/specs/2026-07-17-galaxy-freshness-layer-design.md` (approved). Repo root: `/Users/andres.dandrea/IdeaProjects/cities/spacetraders`.

## Global Constraints

- Prefix shell commands with `rtk`. **Known rtk traps:** `rtk npx X` gets rewritten to `npm run X` (Missing-script error) — always use `rtk proxy npx vitest run` / `rtk proxy npx tsc --noEmit` (or `./node_modules/.bin/...`). `rtk`-compacted `git log` hides merge commits — use `rtk proxy git log` when verifying merges.
- If a package's `node_modules` is missing in the worktree, run `rtk proxy npm ci` first (gitignored, not a tracked change).
- Task tracking via `bd` only. A bead is created by the orchestrator before Task 1; commits reference it as `(sp-XXXX)`.
- All web colors from `NOIR` tokens (`web/src/theme/noir.ts`); interpolation helpers may compute intermediate rgb() strings from those tokens but never introduce new hex anchors.
- The freshness constant is **75 minutes**, declared once server-side as `STALE_AFTER_MINUTES = 75` with a provenance comment pointing at gobot `maxListingAge` (`gobot/internal/application/trading/commands/run_trade_route_coordinator_travel.go:711`); clients read it from the response (`staleAfterMinutes`), never hardcode it.
- Demo mode is the existing `VITE_USE_MOCK_API` mock client (`web/src/services/api/mockClient.ts` dispatches `/flows/*` paths) — extend it, don't invent a mode.
- Every commit message ends with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

## File Structure

- **Task 1 (server):** Create `visualizer/server/utils/freshness.ts` + test `visualizer/server/routes/__tests__/freshness.test.ts`; Modify `visualizer/server/routes/flows.ts` (new route) + Create `visualizer/server/routes/__tests__/flows.freshness.test.ts`.
- **Task 2 (web wire):** Modify `web/src/types/flows.ts`, `web/src/services/api/flows.ts`, `web/src/hooks/useFlowsPolling.ts`, `web/src/store/flowStore.ts` (+ its test), `web/src/mocks/mockFlows.ts`, `web/src/services/api/mockClient.ts`.
- **Task 3 (visuals):** Create `web/src/components/flows/freshness.ts` (+ test) and `web/src/components/flows/FreshnessLayer.tsx`; Modify `web/src/components/flows/FlowGalaxyScene.tsx`, `web/src/pages/TradeFlowsView.tsx`, `web/src/components/flows/SystemDrilldown.tsx`, `web/src/store/flowStore.ts` (toggle key).
- **Task 4 (thinner routes):** Modify `web/src/components/flows/flowGeometry.ts` (+ test), `web/src/components/flows/FlowPlanPath.tsx`.
- **Task 5:** on-screen verification (may touch any of the above + mocks).

---

### Task 1: server — `/api/flows/freshness`

**Files:**
- Create: `visualizer/server/utils/freshness.ts`
- Create: `visualizer/server/routes/__tests__/freshness.test.ts`, `visualizer/server/routes/__tests__/flows.freshness.test.ts`
- Modify: `visualizer/server/routes/flows.ts` (append route before `export default`)

**Interfaces:**
- Consumes: `currentEraId` (`../utils/systemCoords.js`), the route file's existing `pool` idioms.
- Produces (Task 2 consumes): response `{ systems: SystemFreshnessRecord[], staleAfterMinutes: 75, generatedAt }` with `SystemFreshnessRecord = { system, totalListings, freshListings, freshnessPct, freshestAt, scoutPost: null | { status: 'manned'|'relay'|'unmanned', hull: string|null, kind: string } }`.

- [ ] **Step 1: Write the failing util tests** (`freshness.test.ts`)

```ts
import { describe, it, expect } from 'vitest';
import { deriveScoutStatus, shapeFreshnessResponse } from '../../utils/freshness.js';

describe('deriveScoutStatus', () => {
  it('manned when assigned_hull is set', () => {
    expect(deriveScoutStatus({ assigned_hull: 'TORWIND-9', reposition_container_id: null })).toBe('manned');
  });
  it('relay when unmanned but a reposition is airborne', () => {
    expect(deriveScoutStatus({ assigned_hull: null, reposition_container_id: 'jump-1' })).toBe('relay');
    expect(deriveScoutStatus({ assigned_hull: '', reposition_container_id: 'jump-1' })).toBe('relay');
  });
  it('unmanned when both are empty', () => {
    expect(deriveScoutStatus({ assigned_hull: null, reposition_container_id: null })).toBe('unmanned');
    expect(deriveScoutStatus({ assigned_hull: '', reposition_container_id: '' })).toBe('unmanned');
  });
});

describe('shapeFreshnessResponse', () => {
  const marketRows = [
    { system: 'X1-AA', total: '60', fresh: '41', freshest_at: '2026-07-17T12:03:11Z' },
    { system: 'X1-BB', total: '10', fresh: '0', freshest_at: '2026-07-17T08:00:00Z' },
  ];
  const scoutRows = [
    { system_symbol: 'X1-AA', assigned_hull: 'TORWIND-9', reposition_container_id: null, kind: 'standing' },
    { system_symbol: 'X1-ZZ', assigned_hull: null, reposition_container_id: null, kind: 'standing' },
  ];

  it('merges market aggregates with scout posts, computing pct', () => {
    const systems = shapeFreshnessResponse(marketRows, scoutRows);
    const aa = systems.find((s) => s.system === 'X1-AA')!;
    expect(aa).toMatchObject({ totalListings: 60, freshListings: 41, freshnessPct: 68 });
    expect(aa.freshestAt).toBe(new Date('2026-07-17T12:03:11Z').toISOString());
    expect(aa.scoutPost).toEqual({ status: 'manned', hull: 'TORWIND-9', kind: 'standing' });
    const bb = systems.find((s) => s.system === 'X1-BB')!;
    expect(bb.freshnessPct).toBe(0);
    expect(bb.scoutPost).toBeNull();
  });

  it('emits a zero-listing record for a posted system with no market rows (post visible on unsensed system)', () => {
    const systems = shapeFreshnessResponse(marketRows, scoutRows);
    const zz = systems.find((s) => s.system === 'X1-ZZ')!;
    expect(zz).toMatchObject({ totalListings: 0, freshListings: 0, freshnessPct: 0, freshestAt: null });
    expect(zz.scoutPost).toEqual({ status: 'unmanned', hull: null, kind: 'standing' });
  });

  it('skips malformed market rows', () => {
    const systems = shapeFreshnessResponse([{ system: null, total: 'x', fresh: 'y', freshest_at: null } as any], []);
    expect(systems).toEqual([]);
  });
});
```

- [ ] **Step 2: Run to verify failure** — `cd visualizer/server && rtk proxy npx vitest run routes/__tests__/freshness.test.ts` → FAIL (module not found).

- [ ] **Step 3: Implement `utils/freshness.ts`**

```ts
export type ScoutStatus = 'manned' | 'relay' | 'unmanned';

export interface SystemFreshnessRecord {
  system: string;
  totalListings: number;
  freshListings: number;
  freshnessPct: number; // round(100 * fresh / total); 0 when total is 0
  freshestAt: string | null;
  scoutPost: { status: ScoutStatus; hull: string | null; kind: string } | null;
}

// ScoutPostModel semantics: assigned_hull set => manned; else an airborne
// reposition relay => relay; else unmanned.
export function deriveScoutStatus(row: { assigned_hull?: string | null; reposition_container_id?: string | null }): ScoutStatus {
  if (row.assigned_hull) return 'manned';
  if (row.reposition_container_id) return 'relay';
  return 'unmanned';
}

// Merge the grouped market aggregation with scout_posts rows. Systems with
// zero listings are omitted (dark = unsensed) UNLESS they carry a scout post —
// the actuator marker must render even before its first scan lands.
export function shapeFreshnessResponse(
  marketRows: { system: unknown; total: unknown; fresh: unknown; freshest_at: unknown }[],
  scoutRows: { system_symbol: string; assigned_hull?: string | null; reposition_container_id?: string | null; kind?: string | null }[],
): SystemFreshnessRecord[] {
  const bySystem = new Map<string, SystemFreshnessRecord>();
  for (const r of marketRows) {
    const system = typeof r.system === 'string' ? r.system : '';
    const total = Number(r.total);
    const fresh = Number(r.fresh);
    if (!system || !Number.isFinite(total) || !Number.isFinite(fresh) || total <= 0) continue;
    const freshestMs = r.freshest_at ? Date.parse(String(r.freshest_at)) : NaN;
    bySystem.set(system, {
      system,
      totalListings: total,
      freshListings: fresh,
      freshnessPct: Math.round((100 * fresh) / total),
      freshestAt: Number.isNaN(freshestMs) ? null : new Date(freshestMs).toISOString(),
      scoutPost: null,
    });
  }
  for (const s of scoutRows) {
    if (!s.system_symbol) continue;
    const post = { status: deriveScoutStatus(s), hull: s.assigned_hull || null, kind: s.kind ?? '' };
    const rec = bySystem.get(s.system_symbol);
    if (rec) rec.scoutPost = post;
    else
      bySystem.set(s.system_symbol, {
        system: s.system_symbol,
        totalListings: 0,
        freshListings: 0,
        freshnessPct: 0,
        freshestAt: null,
        scoutPost: post,
      });
  }
  return [...bySystem.values()].sort((a, b) => a.system.localeCompare(b.system));
}
```

- [ ] **Step 4: Util tests green**, then **write the failing endpoint test** (`flows.freshness.test.ts`, same pg/client mock idiom as `flows.topology.test.ts` — copy its header):

```ts
// (imports + pg/client mocks + makeApp identical to flows.topology.test.ts)
describe('GET /api/flows/freshness', () => {
  it('aggregates era-scoped market freshness and merges scout posts', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 3 }] }) // eras
      .mockResolvedValueOnce({ rows: [ // grouped market aggregation
        { system: 'X1-AA', total: '60', fresh: '41', freshest_at: '2026-07-17T12:03:11Z' },
      ] })
      .mockResolvedValueOnce({ rows: [ // scout_posts
        { system_symbol: 'X1-AA', assigned_hull: 'TORWIND-9', reposition_container_id: null, kind: 'standing' },
      ] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');

    expect(res.status).toBe(200);
    expect(res.body.staleAfterMinutes).toBe(75);
    expect(res.body.systems[0]).toMatchObject({ system: 'X1-AA', freshnessPct: 68, scoutPost: { status: 'manned' } });
    const marketSql = query.mock.calls[1][0] as string;
    expect(marketSql).toMatch(/JOIN waypoints/i);
    expect(marketSql).toMatch(/era_id = \$1 OR era_id IS NULL/);
    expect(marketSql).toMatch(/GROUP BY/i);
    // cutoff param is a Date/ISO ~75min before now
    const cutoff = new Date(query.mock.calls[1][1][1]).getTime();
    expect(Date.now() - cutoff).toBeGreaterThan(74 * 60 * 1000);
    expect(Date.now() - cutoff).toBeLessThan(76 * 60 * 1000);
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});
```

- [ ] **Step 5: Implement the route** (append to `flows.ts` before `export default router;`):

```ts
// ---- GET /api/flows/freshness ------------------------------------------------
// Per-system solver visibility: share of market listings inside the tour/sink
// staleness gate, plus scout-post actuator state. STALE_AFTER_MINUTES mirrors
// gobot maxListingAge (run_trade_route_coordinator_travel.go:711) — the solver's
// number; clients read it from the response, never hardcode it.
const STALE_AFTER_MINUTES = 75;

router.get('/freshness', async (_req, res) => {
  let client;
  try {
    client = await pool.connect();
    const eraId = await currentEraId(client);
    const cutoffIso = new Date(Date.now() - STALE_AFTER_MINUTES * 60 * 1000).toISOString();
    const marketResult = await client.query(
      `SELECT w.system_symbol AS system,
              COUNT(*) AS total,
              COUNT(*) FILTER (WHERE md.last_updated >= $2) AS fresh,
              MAX(md.last_updated) AS freshest_at
       FROM market_data md
       JOIN waypoints w ON w.waypoint_symbol = md.waypoint_symbol
        AND (w.era_id = $1 OR w.era_id IS NULL)
       GROUP BY w.system_symbol`,
      [eraId, cutoffIso],
    );
    const scoutResult = await client.query(
      `SELECT system_symbol, assigned_hull, reposition_container_id, kind FROM scout_posts`,
    );
    res.json({
      systems: shapeFreshnessResponse(marketResult.rows, scoutResult.rows),
      staleAfterMinutes: STALE_AFTER_MINUTES,
      generatedAt: new Date().toISOString(),
    });
  } catch (error: any) {
    console.error('Failed to build flows freshness:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    if (client) client.release();
  }
});
```

with `import { shapeFreshnessResponse } from '../utils/freshness.js';` added to the imports. Note: `scout_posts` may not exist on old DBs — that lands in the catch → 503; acceptable (same as other tables).

- [ ] **Step 6: Full server suite green** — `rtk proxy npx vitest run` (all files). Commit:

```bash
rtk git add utils/freshness.ts routes/flows.ts routes/__tests__/freshness.test.ts routes/__tests__/flows.freshness.test.ts
rtk git commit -m "feat(viz-server): /api/flows/freshness — era-scoped solver-visibility aggregation + scout posts (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: web wire — types, api, polling, store, mocks

**Files:** Modify `web/src/types/flows.ts`, `web/src/services/api/flows.ts`, `web/src/hooks/useFlowsPolling.ts`, `web/src/store/flowStore.ts` + `web/src/store/__tests__/flowStore.test.ts`, `web/src/mocks/mockFlows.ts`, `web/src/services/api/mockClient.ts`.

**Interfaces:**
- Produces (Task 3 consumes): types `ScoutPostStatus`, `SystemFreshnessRecord`, `FreshnessResponse`; store fields `freshness: FreshnessResponse | null`, `freshnessMissedPolls: number`, actions `setFreshness(f)`, `freshnessPollFailed()`; derived rule: degraded when `freshnessMissedPolls >= 5`; `getFlowFreshness()`; `mockFreshness(): FreshnessResponse`.

- [ ] **Step 1: Types** (append to `types/flows.ts`):

```ts
export type ScoutPostStatus = 'manned' | 'relay' | 'unmanned';

export interface SystemFreshnessRecord {
  system: string;
  totalListings: number;
  freshListings: number;
  freshnessPct: number;          // 0..100 solver visibility
  freshestAt: string | null;     // ISO of the newest listing scan
  scoutPost: { status: ScoutPostStatus; hull: string | null; kind: string } | null;
}

export interface FreshnessResponse {
  systems: SystemFreshnessRecord[];
  staleAfterMinutes: number;     // mirrors gobot maxListingAge; never hardcode
  generatedAt: string;
}
```

- [ ] **Step 2: API + polling.** `services/api/flows.ts` gains `export async function getFlowFreshness(): Promise<FreshnessResponse> { return fetchApi<FreshnessResponse>('/flows/freshness'); }`. `useFlowsPolling.ts` gains a fourth effect (constant `FRESHNESS_INTERVAL_MS = 60000`), mirroring the live effect but calling `setFreshness` on success and `freshnessPollFailed()` on catch (freshness failures do NOT go through `setError` — the layer dims instead, spec §5).

- [ ] **Step 3: Failing store tests** (append to `flowStore.test.ts`, inside its reset idiom):

```ts
describe('freshness state', () => {
  it('stores responses and resets the missed-poll counter', () => {
    const s = useFlowStore.getState();
    s.freshnessPollFailed();
    s.freshnessPollFailed();
    expect(useFlowStore.getState().freshnessMissedPolls).toBe(2);
    s.setFreshness({ systems: [], staleAfterMinutes: 75, generatedAt: 'x' });
    expect(useFlowStore.getState().freshnessMissedPolls).toBe(0);
    expect(useFlowStore.getState().freshness?.staleAfterMinutes).toBe(75);
  });
});
```

Run → FAIL (unknown actions). Implement in `flowStore.ts`: fields `freshness: null`, `freshnessMissedPolls: 0`; actions `setFreshness: (freshness) => set({ freshness, freshnessMissedPolls: 0 })`, `freshnessPollFailed: () => set((s) => ({ freshnessMissedPolls: s.freshnessMissedPolls + 1 }))`. Green.

- [ ] **Step 4: Mocks.** `mockFlows.ts` gains (spec §6 ramp-spanning scenario):

```ts
export function mockFreshness(): FreshnessResponse {
  return {
    systems: [
      { system: 'X1-NK36', totalListings: 42, freshListings: 40, freshnessPct: 95, freshestAt: new Date(Date.now() - 4 * 60_000).toISOString(), scoutPost: { status: 'manned', hull: 'TORWIND-9', kind: 'standing' } },
      { system: 'X1-KA42', totalListings: 60, freshListings: 30, freshnessPct: 50, freshestAt: new Date(Date.now() - 38 * 60_000).toISOString(), scoutPost: { status: 'relay', hull: null, kind: 'standing' } },
      { system: 'X1-ZC66', totalListings: 31, freshListings: 3, freshnessPct: 10, freshestAt: new Date(Date.now() - 71 * 60_000).toISOString(), scoutPost: { status: 'unmanned', hull: null, kind: 'standing' } },
      // X1-UU57 deliberately absent: unsensed — no halo, no marker.
    ],
    staleAfterMinutes: 75,
    generatedAt: new Date().toISOString(),
  };
}
```

`mockClient.ts` `/flows/*` dispatch gains `if (path === '/flows/freshness') return mockFreshness() as T;` (import alongside the existing mockFlows imports).

- [ ] **Step 5: Verify + commit** — `rtk proxy npx tsc --noEmit && rtk proxy npx vitest run` all green.

```bash
rtk git add src/types/flows.ts src/services/api/flows.ts src/hooks/useFlowsPolling.ts src/store/flowStore.ts src/store/__tests__/flowStore.test.ts src/mocks/mockFlows.ts src/services/api/mockClient.ts
rtk git commit -m "feat(viz-web): freshness wire — types, 60s poll, missed-poll counter, ramp-spanning demo mock (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: visuals — ramp helper, FreshnessLayer, scene/toggle/drilldown

**Files:** Create `web/src/components/flows/freshness.ts` + `__tests__/freshness.test.ts`, `web/src/components/flows/FreshnessLayer.tsx`; Modify `web/src/store/flowStore.ts` (+test), `web/src/pages/TradeFlowsView.tsx`, `web/src/components/flows/FlowGalaxyScene.tsx`, `web/src/components/flows/SystemDrilldown.tsx`.

**Interfaces:**
- Consumes: Task 2 store fields/types; `noirRgb`/`NOIR` from theme; scene's `systemPos`, `scale`, `nowMs`, `activityBySystem` idioms.
- Produces: `freshnessColor(pct): string` (rgb() string), `haloAlpha(pct): number`, `<FreshnessLayer records systemPos scale nowMs degraded />`.

- [ ] **Step 1: Failing ramp tests** (`__tests__/freshness.test.ts`):

```ts
import { describe, it, expect } from 'vitest';
import { freshnessColor, haloAlpha } from '../freshness';
import { NOIR, noirRgb } from '../../../theme/noir';

const rgb = (hex: string) => { const { r, g, b } = noirRgb(hex); return `rgb(${r}, ${g}, ${b})`; };

describe('freshnessColor', () => {
  it('hits the NOIR anchors at 0 / 50 / 100', () => {
    expect(freshnessColor(0)).toBe(rgb(NOIR.bad));
    expect(freshnessColor(50)).toBe(rgb(NOIR.warn));
    expect(freshnessColor(100)).toBe(rgb(NOIR.good));
  });
  it('interpolates piecewise and clamps out-of-range', () => {
    expect(freshnessColor(-10)).toBe(rgb(NOIR.bad));
    expect(freshnessColor(200)).toBe(rgb(NOIR.good));
    expect(freshnessColor(25)).not.toBe(rgb(NOIR.bad));
    expect(freshnessColor(25)).not.toBe(rgb(NOIR.warn));
  });
});

describe('haloAlpha', () => {
  it('is monotonic from smolder to glow', () => {
    expect(haloAlpha(0)).toBeCloseTo(0.18, 2);
    expect(haloAlpha(100)).toBeCloseTo(0.45, 2);
    expect(haloAlpha(50)).toBeGreaterThan(haloAlpha(0));
    expect(haloAlpha(50)).toBeLessThan(haloAlpha(100));
  });
});
```

- [ ] **Step 2: Implement `freshness.ts`**:

```ts
import { NOIR, noirRgb } from '../../theme/noir';

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

function lerpRgb(a: string, b: string, t: number): string {
  const ca = noirRgb(a);
  const cb = noirRgb(b);
  const r = Math.round(ca.r + (cb.r - ca.r) * t);
  const g = Math.round(ca.g + (cb.g - ca.g) * t);
  const bl = Math.round(ca.b + (cb.b - ca.b) * t);
  return `rgb(${r}, ${g}, ${bl})`;
}

// Continuous solver-visibility ramp over the existing NOIR tokens:
// 0% bad (dead red) -> 50% warn (amber) -> 100% good (green).
export function freshnessColor(pct: number): string {
  const p = clamp(pct, 0, 100);
  return p <= 50 ? lerpRgb(NOIR.bad, NOIR.warn, p / 50) : lerpRgb(NOIR.warn, NOIR.good, (p - 50) / 50);
}

// Center alpha of the halo: green coverage glows brighter than red decay.
export function haloAlpha(pct: number): number {
  return 0.18 + 0.27 * (clamp(pct, 0, 100) / 100);
}
```

- [ ] **Step 3: `FreshnessLayer.tsx`**:

```tsx
import { memo } from 'react';
import { Group, Circle, Rect } from 'react-konva';
import type { SystemFreshnessRecord } from '../../types/flows';
import type { Point } from './flowGeometry';
import { freshnessColor, haloAlpha } from './freshness';
import { NOIR } from '../../theme/noir';

interface Props {
  records: SystemFreshnessRecord[];
  systemPos: Map<string, Point>;
  scale: number;
  nowMs: number;
  degraded: boolean; // >=5 missed polls: honest 50% dim
}

// The sensor picture: a radial halo per sensed system (color+alpha ramp =
// solver visibility) and a diamond scout-post tick (solid manned / hollow
// unmanned / pulsing hollow relay). Systems absent from records stay dark.
export const FreshnessLayer = memo(function FreshnessLayer({ records, systemPos, scale, nowMs, degraded }: Props) {
  const u = 1 / Math.max(scale, 1e-6);
  const dim = degraded ? 0.5 : 1;
  return (
    <Group listening={false}>
      {records.map((r) => {
        const p = systemPos.get(r.system);
        if (!p) return null;
        const color = freshnessColor(r.freshnessPct);
        const radius = 26 * u;
        const markerSize = 3.2 * u;
        const relayPulse = 0.45 + 0.35 * Math.sin((nowMs / 1200) * Math.PI * 2);
        const post = r.scoutPost;
        return (
          <Group key={`fresh-${r.system}`} x={p.x} y={p.y} listening={false}>
            {r.totalListings > 0 && (
              <Circle
                radius={radius}
                fillRadialGradientStartPoint={{ x: 0, y: 0 }}
                fillRadialGradientEndPoint={{ x: 0, y: 0 }}
                fillRadialGradientStartRadius={0}
                fillRadialGradientEndRadius={radius}
                fillRadialGradientColorStops={[0, color, 1, 'rgba(0,0,0,0)']}
                opacity={haloAlpha(r.freshnessPct) * dim}
                listening={false}
              />
            )}
            {post && (
              <Rect
                x={9 * u}
                y={-9 * u}
                width={markerSize}
                height={markerSize}
                rotation={45}
                fill={post.status === 'manned' ? NOIR.accent : undefined}
                stroke={post.status === 'unmanned' ? NOIR.dim : NOIR.accent}
                strokeWidth={0.7 * u}
                opacity={(post.status === 'relay' ? relayPulse : 0.95) * dim}
                listening={false}
              />
            )}
          </Group>
        );
      })}
    </Group>
  );
});
```

- [ ] **Step 4: Wire in.**
  - `flowStore.ts`: `layerToggles` type + default gain `freshness: boolean` / `freshness: true`; `toggleLayer` key union gains `'freshness'`. **Update the existing toggle test** asserting `{ lanes: true, paths: true, ships: true }` → include `freshness: true`.
  - `TradeFlowsView.tsx`: toggle row array `(['lanes', 'paths', 'ships'] as const)` → `(['lanes', 'paths', 'ships', 'freshness'] as const)` (row is at `left-44`; four buttons fit).
  - `FlowGalaxyScene.tsx`: read `freshness` + `freshnessMissedPolls` from the store; render `{layerToggles.freshness && freshness && (<FreshnessLayer records={freshness.systems} systemPos={systemPos} scale={scale} nowMs={nowMs} degraded={freshnessMissedPolls >= 5} />)}` as the FIRST child inside the topology fragment (below `FlowLaneLayer` — halos under everything).
  - `SystemDrilldown.tsx`: new optional prop `freshness?: SystemFreshnessRecord | null`; when set, render under the header title: `Sensor: {freshnessPct}% fresh ({freshListings}/{totalListings} listings{freshestAt ? `, freshest ${Math.round((Date.now()-Date.parse(freshestAt))/60000)}m ago` : ''}){scoutPost ? ` · post: ${scoutPost.status}${scoutPost.hull ? ` (${scoutPost.hull})` : ''}` : ''}` styled `NOIR.muted` text-xs, pct value colored `freshnessColor(pct)`. `TradeFlowsView.tsx` passes `freshness={freshness?.systems.find((s) => s.system === drilldownSystem) ?? null}`.

- [ ] **Step 5: Verify + commit** — `rtk proxy npx tsc --noEmit && rtk proxy npx vitest run` (expect the drilldown/layout suites to still pass; fix any assertion your header line directly breaks, e.g. text multi-matches, minimally).

```bash
rtk git add src/components/flows/freshness.ts src/components/flows/__tests__/freshness.test.ts src/components/flows/FreshnessLayer.tsx src/components/flows/FlowGalaxyScene.tsx src/components/flows/SystemDrilldown.tsx src/pages/TradeFlowsView.tsx src/store/flowStore.ts src/store/__tests__/flowStore.test.ts
rtk git commit -m "feat(viz-web): freshness halos + scout-post markers + toggle + drilldown sensor line (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: thinner route lines

**Files:** Modify `web/src/components/flows/flowGeometry.ts` (`laneWidth`), `web/src/components/flows/FlowPlanPath.tsx`; check `__tests__/flowGeometry.test.ts` (`laneProfitColor / laneWidth` describe block, ~line 74).

- [ ] **Step 1:** `laneWidth`: `Math.min(6, ...)` → `Math.min(3, ...)`; `Math.max(0.5, mag / scale)` → `Math.max(0.35, mag / scale)`; inner floor `Math.max(0.5, ...)` inside the mag clamp stays. Update any test assertions on the old cap/floor to the new values (run the file to see which).
- [ ] **Step 2:** `FlowPlanPath.tsx`: gradient stroke `Math.max(0.5, 1.4 * u)` → `Math.max(0.4, 0.9 * u)` (line ~85); cool/deadhead stroke `Math.max(0.5, 1.6 * u)` → `Math.max(0.4, 1.0 * u)` (line ~70). Anchor ring + hop markers unchanged.
- [ ] **Step 3:** `rtk proxy npx vitest run src/components/flows/__tests__/flowGeometry.test.ts` + full suite green. Commit:

```bash
rtk git add src/components/flows/flowGeometry.ts src/components/flows/FlowPlanPath.tsx src/components/flows/__tests__/flowGeometry.test.ts
rtk git commit -m "style(viz-web): halve route-line weight — lane cap 6->3, plan strokes 1.4->0.9 / 1.6->1.0 (sp-XXXX)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: on-screen verification

Build + preview the demo (`VITE_USE_MOCK_API=true rtk proxy npx vite build && rtk proxy npx vite preview --port 4173 &`), screenshot with headless Chrome (same command as the sp-gl06 plan, output `/private/tmp/galaxy-freshness-check.png`), **Read the PNG** and verify:

1. Three halos, visually distinct along the ramp: X1-NK36 green-bright, X1-KA42 amber-mid, X1-ZC66 red-faint.
2. X1-UU57: no halo, no marker (dark/unsensed).
3. Scout markers distinguishable: solid diamond (NK36), hollow pulsing (KA42 — compare two screenshots for the pulse), hollow dim (ZC66).
4. Route lines visibly thinner than `/private/tmp/galaxy-view-check.png` (the sp-gl06 before-shot, if still present; else judge absolutely — lanes must read as accents, not arteries).
5. Toggle row shows four buttons; halos don't drown lanes/ships.
6. Drilldown (via a dev-store screenshot or skipped if impractical headlessly — the drilldown line is covered by component render in the suite).

Fix-and-rescreenshot until 1-5 pass. Kill servers. Run full gates: server `rtk proxy npx vitest run`; web `rtk proxy npx tsc --noEmit && rtk proxy npx vitest run`. Commit any fixes as `fix(viz-web): freshness on-screen verification fixes (sp-XXXX)`.

---

## Final validation & close-out

- Full suites: server + web (no gobot changes — skip Go gates).
- Orchestrator: close the bead, merge per finishing-a-development-branch, session-close protocol (`git pull --rebase && bd dolt push && git push`).

## Deferred by design

- Per-waypoint freshness in the drilldown map; freshness alerts/automation; historical trends (spec §7).
