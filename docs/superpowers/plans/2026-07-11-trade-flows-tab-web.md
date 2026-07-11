# Trade Flows Tab (Web) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Part 1 of the Trade Flows tab — a fixture-first `/trade-flows` visualizer route that renders the trading trio (tours, trade-route circuits, arb runs) spatially on a galaxy-scale gate map, backed by three new `/api/flows/*` server endpoints, and degrading gracefully (FEED LOST chip, no fabricated intent) until the Part-2 daemon feed deploys.

**Architecture:** The browser talks only to the visualizer Express server (single origin); the server owns the three `/api/flows/*` endpoints — `topology` (PG `gate_edges` + a deterministic server-computed galaxy layout, cached), `lanes` (PG realized-leg aggregation, polled 30s), and `live` (proxy of the daemon feed at `http://localhost:9090/api/flows` joined to PG `ships` nav, polled 5s). The web layer follows the existing Konva + Zustand + polling-hook idioms, keeps all non-trivial math in pure helpers beside the Konva components (the `routeVectorsUtils.ts`/`RouteVectors.test.ts` pattern), and drives a full fleet-stopped demo through the existing `mockClient` dispatch.

**Tech Stack:**
- Web: React 18, react-konva 18 (Konva 10), Zustand 4.5, react-router-dom 7, Tailwind 3, Vite 5, Vitest 3 + @testing-library/react (jsdom env, `vitest.setup.ts`).
- Server: Node 22, Express 4 (ESM, `.js` import specifiers), `pg` 8 Pool; new dev deps `vitest` + `supertest` for the flows router.
- PostgreSQL (`postgresql://spacetraders:dev_password@localhost:5432/spacetraders`): `gate_edges`, `tour_leg_telemetry`, `arbitrage_execution_logs`, `ships`, `containers`.

## Global Constraints

- **Single origin.** The browser NEVER calls the daemon (`:9090`) directly. Only `server/routes/flows.ts` proxies it; the web layer calls `/api/flows/*` through the existing `fetchApi` client.
- **Poll cadences.** `/api/flows/live` every 5s; `/api/flows/lanes` every 30s; `/api/flows/topology` once per page mount, then cached (server caches too).
- **Degradation (never fabricate intent).**
  - Daemon feed unreachable → `/api/flows/live` returns `{ flows: [], feedLost: true, lastPlanAt: null, generatedAt }`; the tab stays functional on PG (realized lanes + last-known ship positions) and shows a `FEED LOST · last plan mm:ss ago` chip (mirrors the observatory SIGNAL LOST doctrine). Dashed intent paths disappear — they are never synthesized from PG.
  - Hull trading without a published plan (`currentLeg`/`remainingHops` absent) → position-only glyph, no dashed path.
  - PG unavailable → `topology`/`lanes` return HTTP 503 `{ error: 'db_unavailable' }` (the existing Operational-Pulse degrade contract), and the tab shows a standard error state — NOT a 404 and NOT a process crash.
- **Deep Space Noir tokens.** All new UI (Konva fills + Tailwind panels) draws from `theme/noir.ts` (`NOIR`, `noirAlpha`) — no ad-hoc hex outside the profit-scale helper, which is centralized and tested.
- **On-screen acceptance is mandatory (S1 nebula lesson).** The final task requires BOTH rendered-layout assertions (element bounding boxes vs viewport) AND a screenshot read of the live tab in demo mode, fleet-stopped. Backing-store / prop-shape checks alone do NOT count as "visible".
- **Separable lanes.** This plan builds ONLY the web tab + server endpoints against a fixture of the daemon payload. The daemon `GET /api/flows` surface is Part 2; every field name this plan consumes is pinned to the agreed contract so the two lanes meet without rework.

## Tasks

### Task 1 — Flow types + demo fixtures

Everything downstream (server response typing on the web side, store, hooks, scene helpers, panels, demo dispatch) consumes these types and fixtures, so they land first.

**Files:**
- Create: `visualizer/web/src/types/flows.ts`
- Create: `visualizer/web/src/mocks/mockFlows.ts`
- Create: `visualizer/web/src/mocks/__tests__/mockFlows.test.ts`

**Interfaces:**
- Produces (types): `FlowProgram`, `FlowTranche`, `FlowHop`, `FlowCargoItem`, `FlowLeg`, `FlowProjection`, `DaemonFlow`, `FlowShipNav`, `LiveFlow`, `LiveFlowsResponse`, `FlowWindow`, `LaneRecord`, `LanesResponse`, `TopologySystem`, `TopologyEdge`, `TopologyResponse`.
- Produces (fixtures): `mockTopology: TopologyResponse`, `mockLanes(window: FlowWindow): LanesResponse`, `mockLiveFlows(nowMs: number): LiveFlowsResponse`, `mockFeedLostResponse(nowMs: number): LiveFlowsResponse`.
- Consumes: nothing (leaf module). The wire JSON shape produced here IS the contract the server (Tasks 2-4) and daemon (Part 2) must match.

**Steps:**

- [ ] Write the failing test `visualizer/web/src/mocks/__tests__/mockFlows.test.ts`:
  ```ts
  import { describe, it, expect } from 'vitest';
  import {
    mockTopology,
    mockLanes,
    mockLiveFlows,
    mockFeedLostResponse,
  } from '../mockFlows';

  describe('mockFlows fixtures', () => {
    it('topology has systems and only real (non-backoff) edges', () => {
      expect(mockTopology.systems.length).toBeGreaterThan(1);
      expect(mockTopology.edges.length).toBeGreaterThan(0);
      // Every edge endpoint resolves to a system node.
      const symbols = new Set(mockTopology.systems.map((s) => s.symbol));
      for (const e of mockTopology.edges) {
        expect(symbols.has(e.from)).toBe(true);
        expect(symbols.has(e.to)).toBe(true);
        expect(e.to).not.toBe(''); // no backoff markers
      }
    });

    it('live fixture covers all three programs, each with a current leg', () => {
      const res = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
      expect(res.feedLost).toBe(false);
      const programs = res.flows.map((f) => f.program).sort();
      expect(programs).toEqual(['arb', 'tour', 'trade-route']);
      for (const f of res.flows) {
        expect(f.currentLeg).not.toBeNull();
        expect(typeof f.plannedAt).toBe('string');
      }
    });

    it('the tour flow carries remaining hops with priced tranches', () => {
      const res = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
      const tour = res.flows.find((f) => f.program === 'tour')!;
      expect(tour.remainingHops.length).toBeGreaterThan(0);
      const tranche = tour.remainingHops[0].tranches[0];
      expect(tranche.expectedUnitPrice).toBeGreaterThan(0);
      expect(typeof tranche.isBuy).toBe('boolean');
    });

    it('feed-loss fixture is empty flows + feedLost true + null lastPlanAt', () => {
      const res = mockFeedLostResponse(Date.parse('2026-07-11T00:00:00Z'));
      expect(res.feedLost).toBe(true);
      expect(res.flows).toEqual([]);
      expect(res.lastPlanAt).toBeNull();
    });

    it('lanes fixture is sorted by realized profit descending', () => {
      const lanes = mockLanes('6h').lanes;
      expect(lanes.length).toBeGreaterThan(0);
      for (let i = 1; i < lanes.length; i++) {
        expect(lanes[i - 1].realizedProfit).toBeGreaterThanOrEqual(lanes[i].realizedProfit);
      }
    });
  });
  ```
- [ ] Run it to see it fail (module missing):
  ```bash
  cd visualizer/web && npx vitest run src/mocks/__tests__/mockFlows.test.ts
  ```
  Expected failure: `Failed to resolve import "../mockFlows"` / cannot find module.
- [ ] Create `visualizer/web/src/types/flows.ts` (complete):
  ```ts
  // Wire contract for the Trade Flows tab. The daemon (Part 2) serializes
  // DaemonFlow / LiveFlowsResponse verbatim; the visualizer server adds the
  // shipNav enrichment + feedLost/lastPlanAt envelope fields.

  export type FlowProgram = 'tour' | 'trade-route' | 'arb';

  export interface FlowTranche {
    good: string;
    isBuy: boolean;
    units: number;
    expectedUnitPrice: number;
  }

  export interface FlowHop {
    waypoint: string;
    tranches: FlowTranche[];
  }

  export interface FlowCargoItem {
    good: string;
    units: number;
  }

  export interface FlowLeg {
    from: string;         // waypoint symbol
    to: string;           // waypoint symbol
    departedAt: string;   // ISO
    arrivesAt: string;    // ISO
  }

  export interface FlowProjection {
    profit: number;
    ratePerHour: number;
  }

  // One active flow as the daemon publishes it.
  export interface DaemonFlow {
    containerId: string;
    program: FlowProgram;
    ship: string;
    tourId: string | null;
    currentLeg: FlowLeg | null;
    cargo: FlowCargoItem[];
    remainingHops: FlowHop[];
    projected: FlowProjection | null;
    plannedAt: string;    // ISO
  }

  // Server enrichment: last-known PG nav for the flow's ship (position truth).
  export interface FlowShipNav {
    status: string;          // ships.nav_status
    systemSymbol: string;    // ships.system_symbol
    waypointSymbol: string;  // ships.location_symbol
    x: number;               // ships.location_x
    y: number;               // ships.location_y
    arrivalTime: string | null; // ships.arrival_time
  }

  export interface LiveFlow extends DaemonFlow {
    shipNav: FlowShipNav | null;
  }

  export interface LiveFlowsResponse {
    flows: LiveFlow[];
    generatedAt: string;   // ISO
    feedLost: boolean;
    lastPlanAt: string | null; // max plannedAt from the daemon; null when feedLost
  }

  export type FlowWindow = '1h' | '6h' | '24h';

  export interface LaneRecord {
    from: string;          // waypoint symbol
    to: string;            // waypoint symbol
    realizedUnits: number;
    realizedProfit: number;
    legCount: number;
  }

  export interface LanesResponse {
    lanes: LaneRecord[];
    window: FlowWindow;
    generatedAt: string;
  }

  export interface TopologySystem {
    symbol: string;
    x: number;             // galaxy-scale layout coordinate (server-computed)
    y: number;
  }

  export interface TopologyEdge {
    from: string;          // gate_edges.system_symbol
    to: string;            // gate_edges.connected_system
    gateWaypoint: string;  // gate_edges.gate_waypoint
    underConstruction: boolean;
  }

  export interface TopologyResponse {
    systems: TopologySystem[];
    edges: TopologyEdge[];
    generatedAt: string;
  }
  ```
- [ ] Create `visualizer/web/src/mocks/mockFlows.ts` (complete):
  ```ts
  import type {
    TopologyResponse,
    LanesResponse,
    LiveFlowsResponse,
    LiveFlow,
    FlowWindow,
  } from '../types/flows';

  // A compact synthetic gate network (two systems + a couple of neighbours) with
  // hand-placed galaxy coordinates so the demo tab is self-contained (the live
  // server computes real coordinates from the gate graph — see galaxyLayout.ts).
  export const mockTopology: TopologyResponse = {
    systems: [
      { symbol: 'X1-NK36', x: -400, y: -120 },
      { symbol: 'X1-KA42', x: 260, y: 40 },
      { symbol: 'X1-ZC66', x: 120, y: 380 },
      { symbol: 'X1-UU57', x: -260, y: 280 },
    ],
    edges: [
      { from: 'X1-NK36', to: 'X1-KA42', gateWaypoint: 'X1-NK36-I52', underConstruction: false },
      { from: 'X1-KA42', to: 'X1-ZC66', gateWaypoint: 'X1-KA42-I52', underConstruction: false },
      { from: 'X1-ZC66', to: 'X1-UU57', gateWaypoint: 'X1-ZC66-I52', underConstruction: false },
      { from: 'X1-UU57', to: 'X1-NK36', gateWaypoint: 'X1-UU57-I52', underConstruction: true },
    ],
    generatedAt: new Date(0).toISOString(),
  };

  // Realized lanes, pre-sorted by profit desc (as the live endpoint returns them).
  export function mockLanes(window: FlowWindow): LanesResponse {
    const base = [
      { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', realizedUnits: 480, realizedProfit: 312000, legCount: 6 },
      { from: 'X1-KA42-D39', to: 'X1-ZC66-C39A', realizedUnits: 300, realizedProfit: 141000, legCount: 4 },
      { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', realizedUnits: 120, realizedProfit: -8000, legCount: 2 },
    ];
    // The window only scales the volume in the fixture — enough to see the switch work.
    const scale = window === '1h' ? 0.25 : window === '6h' ? 1 : 3.5;
    return {
      lanes: base.map((l) => ({
        ...l,
        realizedUnits: Math.round(l.realizedUnits * scale),
        realizedProfit: Math.round(l.realizedProfit * scale),
      })),
      window,
      generatedAt: new Date(0).toISOString(),
    };
  }

  // Three live flows — one per program — with current legs anchored to `nowMs`
  // so a fleet-stopped demo still interpolates a stable mid-leg position.
  export function mockLiveFlows(nowMs: number): LiveFlowsResponse {
    const iso = (ms: number) => new Date(ms).toISOString();
    const flows: LiveFlow[] = [
      {
        containerId: 'tour-run-TORWIND-19-086680f9',
        program: 'tour',
        ship: 'TORWIND-19',
        tourId: 'tour-run-TORWIND-19-086680f9',
        currentLeg: { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', departedAt: iso(nowMs - 90_000), arrivesAt: iso(nowMs + 90_000) },
        cargo: [{ good: 'FABRICS', units: 120 }, { good: 'SHIP_PARTS', units: 12 }],
        remainingHops: [
          { waypoint: 'X1-KA42-D39', tranches: [{ good: 'SHIP_PARTS', isBuy: false, units: 12, expectedUnitPrice: 3959 }] },
          { waypoint: 'X1-ZC66-C39A', tranches: [{ good: 'ADVANCED_CIRCUITRY', isBuy: true, units: 100, expectedUnitPrice: 4100 }] },
        ],
        projected: { profit: 312000, ratePerHour: 445000 },
        plannedAt: iso(nowMs - 120_000),
        shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-NK36', waypointSymbol: 'X1-NK36-FE8A', x: 12, y: -8, arrivalTime: iso(nowMs + 90_000) },
      },
      {
        containerId: 'trade-route-TORWIND-2B-28a64d19',
        program: 'trade-route',
        ship: 'TORWIND-2B',
        tourId: null,
        currentLeg: { from: 'X1-KA42-D39', to: 'X1-ZC66-C39A', departedAt: iso(nowMs - 30_000), arrivesAt: iso(nowMs + 150_000) },
        cargo: [{ good: 'ELECTRONICS', units: 60 }],
        remainingHops: [
          { waypoint: 'X1-ZC66-C39A', tranches: [{ good: 'ELECTRONICS', isBuy: false, units: 60, expectedUnitPrice: 2200 }] },
        ],
        projected: { profit: 88000, ratePerHour: 190000 },
        plannedAt: iso(nowMs - 45_000),
        shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-KA42', waypointSymbol: 'X1-KA42-D39', x: -20, y: 15, arrivalTime: iso(nowMs + 150_000) },
      },
      {
        containerId: 'arb-run-TORWIND-54-beba64e7',
        program: 'arb',
        ship: 'TORWIND-54',
        tourId: null,
        currentLeg: { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', departedAt: iso(nowMs - 150_000), arrivesAt: iso(nowMs + 30_000) },
        cargo: [{ good: 'EQUIPMENT', units: 200 }],
        remainingHops: [
          { waypoint: 'X1-UU57-E21Z', tranches: [{ good: 'EQUIPMENT', isBuy: false, units: 200, expectedUnitPrice: 1500 }] },
        ],
        projected: { profit: 41000, ratePerHour: 96000 },
        plannedAt: iso(nowMs - 160_000),
        shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-C39A', x: 4, y: 22, arrivalTime: iso(nowMs + 30_000) },
      },
    ];
    const lastPlanAt = flows.reduce<string | null>((max, f) => (max === null || f.plannedAt > max ? f.plannedAt : max), null);
    return { flows, generatedAt: iso(nowMs), feedLost: false, lastPlanAt };
  }

  // The feed-loss scenario: no flows, no intent, flagged.
  export function mockFeedLostResponse(nowMs: number): LiveFlowsResponse {
    return { flows: [], generatedAt: new Date(nowMs).toISOString(), feedLost: true, lastPlanAt: null };
  }
  ```
- [ ] Run the test to green:
  ```bash
  cd visualizer/web && npx vitest run src/mocks/__tests__/mockFlows.test.ts
  ```
  Expected: 5 passing.
- [ ] Commit:
  ```bash
  cd visualizer/web && git add src/types/flows.ts src/mocks/mockFlows.ts src/mocks/__tests__/mockFlows.test.ts && git commit --no-verify -m "feat(flows-web): flow wire types + demo fixtures (Task 1)"
  ```

### Task 2 — Server: galaxy layout helper + `/api/flows/topology` (+ server test harness)

> **Resolved ambiguity (galaxy coordinates).** The spec asserts PG holds "systems coordinates", but the DB was verified to have NO galaxy-level system coordinates — only intra-system `waypoints.x/y` (range ±786). Resolution: the topology endpoint derives a **deterministic, seeded force-directed layout** from the `gate_edges` graph, computed server-side in a pure helper. This keeps topology PG-only (no public-API call), stable across restarts (seeded by symbol hash — no `Math.random`), and unit-testable. Real edges only: rows with `connected_system = ''` are backoff markers and are excluded (verified: 220 real vs 6 backoff, 47 systems).

This is the first server task, so it also stands up the flows router file, mounts it, and adds the minimal Vitest + Supertest harness (the server currently has no test framework).

**Files:**
- Create: `visualizer/server/utils/galaxyLayout.ts`
- Create: `visualizer/server/routes/flows.ts`
- Modify: `visualizer/server/index.ts` (mount the router)
- Modify: `visualizer/server/package.json` (add `vitest`, `supertest`, `@types/supertest` dev deps + a `test` script)
- Create: `visualizer/server/vitest.config.ts`
- Create: `visualizer/server/routes/__tests__/galaxyLayout.test.ts`
- Create: `visualizer/server/routes/__tests__/flows.topology.test.ts`

**Interfaces:**
- Produces: `computeGalaxyLayout(systems: string[], edges: LayoutEdge[], opts?): LayoutNode[]` where `LayoutNode = { symbol: string; x: number; y: number }`, `LayoutEdge = { from: string; to: string }`.
- Produces: Express router mounted at `/api/flows` exposing `GET /topology` → `TopologyResponse` (wire shape from Task 1) or HTTP 503 `{ error: 'db_unavailable' }`.
- Consumes: `pg` Pool (`pool.connect()` inside `try`, `pool.on('error')` swallow — the Operational-Pulse degrade contract from `routes/bot.ts`).

**Steps:**

- [ ] Add the harness to `visualizer/server/package.json`: under `scripts` add `"test": "vitest run"`, and under `devDependencies` add `"vitest": "^3.2.4"`, `"supertest": "^7.0.0"`, `"@types/supertest": "^6.0.2"`. Then install:
  ```bash
  cd visualizer/server && npm install
  ```
- [ ] Create `visualizer/server/vitest.config.ts`:
  ```ts
  import { defineConfig } from 'vitest/config';

  export default defineConfig({
    test: {
      environment: 'node',
      include: ['routes/**/*.test.ts', 'utils/**/*.test.ts'],
    },
  });
  ```
- [ ] Write the failing layout test `visualizer/server/routes/__tests__/galaxyLayout.test.ts`:
  ```ts
  import { describe, it, expect } from 'vitest';
  import { computeGalaxyLayout } from '../../utils/galaxyLayout.js';

  describe('computeGalaxyLayout', () => {
    it('returns one node per distinct system', () => {
      const nodes = computeGalaxyLayout(
        ['X1-A', 'X1-B', 'X1-C', 'X1-A'],
        [{ from: 'X1-A', to: 'X1-B' }],
      );
      expect(nodes.map((n) => n.symbol).sort()).toEqual(['X1-A', 'X1-B', 'X1-C']);
    });

    it('is deterministic — identical input yields identical coordinates', () => {
      const args = [['X1-A', 'X1-B', 'X1-C'], [{ from: 'X1-A', to: 'X1-B' }]] as const;
      const a = computeGalaxyLayout([...args[0]], [...args[1]]);
      const b = computeGalaxyLayout([...args[0]], [...args[1]]);
      expect(a).toEqual(b);
    });

    it('produces finite, integer coordinates', () => {
      const nodes = computeGalaxyLayout(['X1-A', 'X1-B'], [{ from: 'X1-A', to: 'X1-B' }]);
      for (const n of nodes) {
        expect(Number.isFinite(n.x)).toBe(true);
        expect(Number.isInteger(n.x)).toBe(true);
        expect(Number.isInteger(n.y)).toBe(true);
      }
    });

    it('empty input yields empty layout', () => {
      expect(computeGalaxyLayout([], [])).toEqual([]);
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/galaxyLayout.test.ts
  ```
  Expected failure: cannot resolve `../../utils/galaxyLayout.js`.
- [ ] Create `visualizer/server/utils/galaxyLayout.ts` (complete):
  ```ts
  export interface LayoutNode {
    symbol: string;
    x: number;
    y: number;
  }

  export interface LayoutEdge {
    from: string;
    to: string;
  }

  // FNV-1a — a stable, dependency-free hash so initial placement is seeded by the
  // symbol (deterministic across processes; no Math.random anywhere in here).
  function hashSymbol(symbol: string): number {
    let h = 2166136261;
    for (let i = 0; i < symbol.length; i++) {
      h ^= symbol.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    return h >>> 0;
  }

  // Deterministic Fruchterman-Reingold-style layout of the gate graph. Nodes start
  // on a hash-jittered ring and relax under edge attraction + all-pairs repulsion.
  export function computeGalaxyLayout(
    systems: string[],
    edges: LayoutEdge[],
    opts: { radius?: number; iterations?: number } = {},
  ): LayoutNode[] {
    const radius = opts.radius ?? 1000;
    const iterations = opts.iterations ?? 200;
    const sorted = [...new Set(systems)].sort();
    const n = sorted.length;
    if (n === 0) return [];

    const pos = new Map<string, { x: number; y: number }>();
    sorted.forEach((sym, i) => {
      const base = (i / n) * Math.PI * 2;
      const h = hashSymbol(sym);
      const jitter = ((h % 1000) / 1000 - 0.5) * (Math.PI / n);
      const r = radius * (0.6 + ((h >>> 10) % 1000) / 1000 * 0.4);
      pos.set(sym, { x: Math.cos(base + jitter) * r, y: Math.sin(base + jitter) * r });
    });

    const known = new Set(sorted);
    const realEdges = edges.filter((e) => e.from !== e.to && known.has(e.from) && known.has(e.to));
    const k = radius / Math.sqrt(n); // ideal spacing

    for (let iter = 0; iter < iterations; iter++) {
      const disp = new Map<string, { x: number; y: number }>();
      sorted.forEach((s) => disp.set(s, { x: 0, y: 0 }));

      for (let i = 0; i < n; i++) {
        for (let j = i + 1; j < n; j++) {
          const a = pos.get(sorted[i])!;
          const b = pos.get(sorted[j])!;
          const dx = a.x - b.x;
          const dy = a.y - b.y;
          const dist = Math.hypot(dx, dy) || 0.01;
          const force = (k * k) / dist;
          const fx = (dx / dist) * force;
          const fy = (dy / dist) * force;
          const da = disp.get(sorted[i])!;
          const db = disp.get(sorted[j])!;
          da.x += fx; da.y += fy;
          db.x -= fx; db.y -= fy;
        }
      }

      for (const e of realEdges) {
        const a = pos.get(e.from)!;
        const b = pos.get(e.to)!;
        const dx = a.x - b.x;
        const dy = a.y - b.y;
        const dist = Math.hypot(dx, dy) || 0.01;
        const force = (dist * dist) / k;
        const fx = (dx / dist) * force;
        const fy = (dy / dist) * force;
        disp.get(e.from)!.x -= fx; disp.get(e.from)!.y -= fy;
        disp.get(e.to)!.x += fx; disp.get(e.to)!.y += fy;
      }

      const temp = radius * (1 - iter / iterations) * 0.1;
      for (const s of sorted) {
        const d = disp.get(s)!;
        const p = pos.get(s)!;
        const dl = Math.hypot(d.x, d.y) || 0.01;
        p.x += (d.x / dl) * Math.min(dl, temp);
        p.y += (d.y / dl) * Math.min(dl, temp);
      }
    }

    return sorted.map((s) => ({ symbol: s, x: Math.round(pos.get(s)!.x), y: Math.round(pos.get(s)!.y) }));
  }
  ```
- [ ] Run the layout test to green:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/galaxyLayout.test.ts
  ```
  Expected: 4 passing.
- [ ] Write the failing router test `visualizer/server/routes/__tests__/flows.topology.test.ts` (mocks `pg` so no real DB is needed):
  ```ts
  import { describe, it, expect, vi, beforeEach } from 'vitest';
  import express from 'express';
  import request from 'supertest';

  const connect = vi.fn();
  vi.mock('pg', () => ({
    default: { Pool: class { on() {} connect() { return connect(); } } },
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
    vi.resetModules();
  });

  describe('GET /api/flows/topology', () => {
    it('returns systems (with coordinates) and only real edges', async () => {
      const query = vi.fn().mockResolvedValue({
        rows: [
          { system_symbol: 'X1-NK36', connected_system: 'X1-KA42', gate_waypoint: 'X1-NK36-I52', under_construction: false },
          { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-KA42-I52', under_construction: true },
        ],
      });
      connect.mockResolvedValue({ query, release: vi.fn() });

      const app = await makeApp();
      const res = await request(app).get('/api/flows/topology');

      expect(res.status).toBe(200);
      const symbols = res.body.systems.map((s: any) => s.symbol).sort();
      expect(symbols).toEqual(['X1-KA42', 'X1-NK36', 'X1-ZC66']);
      for (const s of res.body.systems) {
        expect(Number.isFinite(s.x)).toBe(true);
        expect(Number.isFinite(s.y)).toBe(true);
      }
      expect(res.body.edges).toHaveLength(2);
      // The SQL must exclude backoff markers (connected_system = '').
      const sql = query.mock.calls[0][0] as string;
      expect(sql).toMatch(/connected_system\s*<>\s*''/);
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
- [ ] Run to see it fail:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/flows.topology.test.ts
  ```
  Expected failure: cannot resolve `../flows.js`.
- [ ] Create `visualizer/server/routes/flows.ts` (complete for Task 2 — Tasks 3 and 4 append the `/lanes` and `/live` handlers to this same file):
  ```ts
  import { Router } from 'express';
  import pkg from 'pg';
  const { Pool } = pkg;
  import { computeGalaxyLayout } from '../utils/galaxyLayout.js';

  const router = Router();

  // Lazy pg pool — construction does NOT connect (mirrors routes/bot.ts). The
  // idle-client 'error' listener prevents a DB restart from crashing the process.
  const pool = new Pool({
    connectionString:
      process.env.DATABASE_URL || 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders',
  });
  pool.on('error', (err) => {
    console.error('pg pool idle-client error (DB likely restarting):', err.message);
  });

  // ---- GET /api/flows/topology -------------------------------------------------
  // PG gate_edges (real edges only) + a deterministic server-computed galaxy
  // layout. Cached in-memory: the gate graph changes on the order of eras, and
  // the browser polls this once per mount. Any DB failure degrades to 503.
  let topologyCache: { payload: unknown; builtAtMs: number } | null = null;
  const TOPOLOGY_TTL_MS = 5 * 60 * 1000;

  router.get('/topology', async (_req, res) => {
    if (topologyCache && Date.now() - topologyCache.builtAtMs < TOPOLOGY_TTL_MS) {
      return res.json(topologyCache.payload);
    }
    let client;
    try {
      client = await pool.connect();
      const result = await client.query(`
        SELECT system_symbol, connected_system, gate_waypoint, under_construction
        FROM gate_edges
        WHERE connected_system <> ''
      `);

      const edges = result.rows.map((r: any) => ({
        from: r.system_symbol as string,
        to: r.connected_system as string,
        gateWaypoint: r.gate_waypoint as string,
        underConstruction: Boolean(r.under_construction),
      }));

      const systemSet = new Set<string>();
      for (const e of edges) {
        systemSet.add(e.from);
        systemSet.add(e.to);
      }
      const layout = computeGalaxyLayout([...systemSet], edges.map((e) => ({ from: e.from, to: e.to })));

      const payload = { systems: layout, edges, generatedAt: new Date().toISOString() };
      topologyCache = { payload, builtAtMs: Date.now() };
      res.json(payload);
    } catch (error: any) {
      console.error('Failed to build flows topology:', error?.message ?? error);
      res.status(503).json({ error: 'db_unavailable' });
    } finally {
      if (client) client.release();
    }
  });

  export default router;
  ```
- [ ] Run the topology test to green:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/flows.topology.test.ts
  ```
  Expected: 2 passing.
- [ ] Mount the router in `visualizer/server/index.ts`. Add the import beside the other static routers (`import flowsRouter from './routes/flows.js';`) and the mount beside `app.use('/api/systems', systemsRouter);`:
  ```ts
  app.use('/api/flows', flowsRouter);
  ```
  (Static mount like agents/systems — NOT the lazy bot try/catch. The Pool constructs without connecting, and every endpoint degrades to 503 on DB failure, so a down DB surfaces as 503 rather than a missing route.)
- [ ] Build-check the server compiles:
  ```bash
  cd visualizer/server && npx tsc --noEmit
  ```
  Expected: no errors.
- [ ] Commit:
  ```bash
  cd visualizer/server && git add utils/galaxyLayout.ts routes/flows.ts index.ts package.json package-lock.json vitest.config.ts routes/__tests__/galaxyLayout.test.ts routes/__tests__/flows.topology.test.ts && git commit --no-verify -m "feat(flows-server): galaxy layout + /api/flows/topology + test harness (Task 2)"
  ```

### Task 3 — Server: lane aggregation helper + `/api/flows/lanes?window=`

> **Resolved ambiguity (lane aggregation sources).** The spec lists `tour_leg_telemetry + transactions + arbitrage_execution_logs`. Verified: `tour_leg_telemetry` rows are written at realization, sequential by `leg_index` (0..5), multiple tranche rows per leg; `arbitrage_execution_logs` has clean per-execution `buy_market`/`sell_market`/`actual_net_profit`/`units_sold`/`executed_at` (currently 0 rows, so fixture-tested); `transactions` has realized credit amounts but NO from/to waypoint geocoding, so it cannot be attributed to a directed lane. Resolution: Part-1 lanes come from telemetry (consecutive-leg pairing) + arb logs (one-hop per execution). `transactions` per-lane attribution is deferred (documented, not silently dropped). The aggregation math lives in a pure helper so window-edge and profit-sign cases are unit-tested (codebase idiom: pure helper beside the route).

**Files:**
- Create: `visualizer/server/utils/laneAggregation.ts`
- Modify: `visualizer/server/routes/flows.ts` (append the `/lanes` handler)
- Create: `visualizer/server/routes/__tests__/laneAggregation.test.ts`
- Create: `visualizer/server/routes/__tests__/flows.lanes.test.ts`

**Interfaces:**
- Produces: `aggregateLanes(telemetry: TelemetryRow[], arb: ArbRow[], windowStartMs: number, windowEndMs: number): LaneRecord[]` where `TelemetryRow = { tourId; shipSymbol; legIndex; waypoint; isBuy; realizedUnits; realizedUnitPrice; realizedAt }`, `ArbRow = { buyMarket; sellMarket; unitsSold; actualNetProfit; executedAt }`, `LaneRecord = { from; to; realizedUnits; realizedProfit; legCount }`.
- Produces: `GET /api/flows/lanes?window=1h|6h|24h` → `LanesResponse` (Task 1 wire shape) or 503.
- Consumes: same `pool` from Task 2.

**Steps:**

- [ ] Write the failing helper test `visualizer/server/routes/__tests__/laneAggregation.test.ts`:
  ```ts
  import { describe, it, expect } from 'vitest';
  import { aggregateLanes } from '../../utils/laneAggregation.js';

  const t = (leg: number, wp: string, isBuy: boolean, units: number, price: number, atIso: string) => ({
    tourId: 'tour-1', shipSymbol: 'SHIP-1', legIndex: leg, waypoint: wp,
    isBuy, realizedUnits: units, realizedUnitPrice: price, realizedAt: atIso,
  });

  const W_START = Date.parse('2026-07-10T00:00:00Z');
  const W_END = Date.parse('2026-07-10T06:00:00Z');
  const inWin = '2026-07-10T03:00:00Z';

  describe('aggregateLanes', () => {
    it('pairs consecutive legs into directed lanes with signed profit (sell +, buy -)', () => {
      const lanes = aggregateLanes([
        t(0, 'X1-A-1', true, 100, 50, inWin),   // buy 5000 at leg 0
        t(1, 'X1-A-2', false, 100, 80, inWin),  // sell 8000 at leg 1
      ], [], W_START, W_END);
      // Lane A-1 -> A-2 realizes the leg-1 (destination) value: +8000.
      expect(lanes).toHaveLength(1);
      expect(lanes[0]).toMatchObject({ from: 'X1-A-1', to: 'X1-A-2', realizedProfit: 8000, realizedUnits: 100 });
    });

    it('excludes rows outside the window (both edges)', () => {
      const before = '2026-07-09T23:59:59Z';
      const after = '2026-07-10T06:00:01Z';
      const lanes = aggregateLanes([
        t(0, 'X1-A-1', false, 10, 10, before),
        t(1, 'X1-A-2', false, 10, 10, after),
      ], [], W_START, W_END);
      expect(lanes).toEqual([]);
    });

    it('folds arb executions into one-hop lanes and can yield a net loss', () => {
      const lanes = aggregateLanes([], [
        { buyMarket: 'X1-B-1', sellMarket: 'X1-B-2', unitsSold: 40, actualNetProfit: -1200, executedAt: inWin },
      ], W_START, W_END);
      expect(lanes).toHaveLength(1);
      expect(lanes[0]).toMatchObject({ from: 'X1-B-1', to: 'X1-B-2', realizedProfit: -1200 });
    });

    it('sorts lanes by realized profit descending', () => {
      const lanes = aggregateLanes([], [
        { buyMarket: 'X1-A', sellMarket: 'X1-B', unitsSold: 1, actualNetProfit: 100, executedAt: inWin },
        { buyMarket: 'X1-C', sellMarket: 'X1-D', unitsSold: 1, actualNetProfit: 900, executedAt: inWin },
      ], W_START, W_END);
      expect(lanes.map((l) => l.realizedProfit)).toEqual([900, 100]);
    });

    it('ignores self-loops (same waypoint / same market)', () => {
      const lanes = aggregateLanes(
        [t(0, 'X1-A-1', false, 5, 5, inWin), t(1, 'X1-A-1', false, 5, 5, inWin)],
        [{ buyMarket: 'X1-Z', sellMarket: 'X1-Z', unitsSold: 5, actualNetProfit: 10, executedAt: inWin }],
        W_START, W_END,
      );
      expect(lanes).toEqual([]);
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/laneAggregation.test.ts
  ```
  Expected failure: cannot resolve `../../utils/laneAggregation.js`.
- [ ] Create `visualizer/server/utils/laneAggregation.ts` (complete):
  ```ts
  export interface TelemetryRow {
    tourId: string;
    shipSymbol: string;
    legIndex: number;
    waypoint: string;
    isBuy: boolean;
    realizedUnits: number;
    realizedUnitPrice: number;
    realizedAt: string; // ISO
  }

  export interface ArbRow {
    buyMarket: string;
    sellMarket: string;
    unitsSold: number;
    actualNetProfit: number;
    executedAt: string; // ISO
  }

  export interface LaneRecord {
    from: string;
    to: string;
    realizedUnits: number;
    realizedProfit: number;
    legCount: number;
  }

  const key = (from: string, to: string) => `${from} ${to}`;

  // Fold telemetry legs + arb executions into directed waypoint lanes within the
  // window. Telemetry: per (tour, ship), rows collapse to one representative
  // waypoint + signed value per leg_index (sell +units*price, buy -units*price);
  // consecutive legs form a directed lane and the DESTINATION leg's realized value
  // is what the lane carries. Arb: one lane per successful execution.
  export function aggregateLanes(
    telemetry: TelemetryRow[],
    arb: ArbRow[],
    windowStartMs: number,
    windowEndMs: number,
  ): LaneRecord[] {
    const lanes = new Map<string, LaneRecord>();
    const bump = (from: string, to: string, units: number, profit: number) => {
      const k = key(from, to);
      const rec = lanes.get(k) ?? { from, to, realizedUnits: 0, realizedProfit: 0, legCount: 0 };
      rec.realizedUnits += units;
      rec.realizedProfit += profit;
      rec.legCount += 1;
      lanes.set(k, rec);
    };

    const groups = new Map<string, TelemetryRow[]>();
    for (const r of telemetry) {
      const at = Date.parse(r.realizedAt);
      if (Number.isNaN(at) || at < windowStartMs || at > windowEndMs) continue;
      const gk = key(r.tourId, r.shipSymbol);
      const arr = groups.get(gk);
      if (arr) arr.push(r);
      else groups.set(gk, [r]);
    }

    for (const rows of groups.values()) {
      const byLeg = new Map<number, { waypoint: string; value: number; units: number; firstAt: number }>();
      for (const r of rows) {
        const signed = (r.isBuy ? -1 : 1) * r.realizedUnits * r.realizedUnitPrice;
        const at = Date.parse(r.realizedAt);
        const cur = byLeg.get(r.legIndex);
        if (!cur) {
          byLeg.set(r.legIndex, { waypoint: r.waypoint, value: signed, units: r.realizedUnits, firstAt: at });
        } else {
          cur.value += signed;
          cur.units += r.realizedUnits;
          if (at < cur.firstAt) {
            cur.firstAt = at;
            cur.waypoint = r.waypoint;
          }
        }
      }
      const legs = [...byLeg.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v);
      for (let i = 1; i < legs.length; i++) {
        const from = legs[i - 1].waypoint;
        const to = legs[i].waypoint;
        if (from === to) continue;
        bump(from, to, legs[i].units, legs[i].value);
      }
    }

    for (const a of arb) {
      const at = Date.parse(a.executedAt);
      if (Number.isNaN(at) || at < windowStartMs || at > windowEndMs) continue;
      if (!a.buyMarket || !a.sellMarket || a.buyMarket === a.sellMarket) continue;
      bump(a.buyMarket, a.sellMarket, a.unitsSold, a.actualNetProfit);
    }

    return [...lanes.values()].sort((a, b) => b.realizedProfit - a.realizedProfit);
  }
  ```
- [ ] Run the helper test to green:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/laneAggregation.test.ts
  ```
  Expected: 5 passing.
- [ ] Write the failing route test `visualizer/server/routes/__tests__/flows.lanes.test.ts`:
  ```ts
  import { describe, it, expect, vi, beforeEach } from 'vitest';
  import express from 'express';
  import request from 'supertest';

  const connect = vi.fn();
  vi.mock('pg', () => ({
    default: { Pool: class { on() {} connect() { return connect(); } } },
  }));

  async function makeApp() {
    const { default: flowsRouter } = await import('../flows.js');
    const app = express();
    app.use('/api/flows', flowsRouter);
    return app;
  }

  beforeEach(() => {
    connect.mockReset();
    vi.resetModules();
  });

  describe('GET /api/flows/lanes', () => {
    it('defaults window to 6h and returns aggregated lanes', async () => {
      const query = vi.fn()
        .mockResolvedValueOnce({ rows: [
          { tour_id: 't1', ship_symbol: 'S1', leg_index: 0, waypoint: 'X1-A-1', is_buy: true, realized_units: 100, realized_unit_price: 50, realized_at: new Date().toISOString() },
          { tour_id: 't1', ship_symbol: 'S1', leg_index: 1, waypoint: 'X1-A-2', is_buy: false, realized_units: 100, realized_unit_price: 80, realized_at: new Date().toISOString() },
        ] })                       // telemetry query
        .mockResolvedValueOnce({ rows: [] }); // arb query
      connect.mockResolvedValue({ query, release: vi.fn() });

      const app = await makeApp();
      const res = await request(app).get('/api/flows/lanes');
      expect(res.status).toBe(200);
      expect(res.body.window).toBe('6h');
      expect(res.body.lanes[0]).toMatchObject({ from: 'X1-A-1', to: 'X1-A-2', realizedProfit: 8000 });
    });

    it('rejects an invalid window with 400', async () => {
      const app = await makeApp();
      const res = await request(app).get('/api/flows/lanes?window=99h');
      expect(res.status).toBe(400);
    });

    it('degrades to 503 when the DB is down', async () => {
      connect.mockRejectedValue(new Error('ECONNREFUSED'));
      const app = await makeApp();
      const res = await request(app).get('/api/flows/lanes?window=1h');
      expect(res.status).toBe(503);
      expect(res.body).toEqual({ error: 'db_unavailable' });
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/flows.lanes.test.ts
  ```
  Expected failure: `/lanes` returns 404 (handler not yet added).
- [ ] Append the `/lanes` handler to `visualizer/server/routes/flows.ts`, immediately before `export default router;`, and add the import at the top (`import { aggregateLanes } from '../utils/laneAggregation.js';`):
  ```ts
  // ---- GET /api/flows/lanes?window=1h|6h|24h -----------------------------------
  // Realized directed-lane volume/profit over the window, from tour_leg_telemetry
  // (multi-hop tours + trade-route circuits) + arbitrage_execution_logs (arb).
  const WINDOW_MS: Record<string, number> = {
    '1h': 60 * 60 * 1000,
    '6h': 6 * 60 * 60 * 1000,
    '24h': 24 * 60 * 60 * 1000,
  };

  router.get('/lanes', async (req, res) => {
    const window = (req.query.window as string) || '6h';
    const span = WINDOW_MS[window];
    if (!span) {
      return res.status(400).json({ error: 'invalid_window' });
    }
    const windowEndMs = Date.now();
    const windowStartMs = windowEndMs - span;
    const sinceIso = new Date(windowStartMs).toISOString();

    let client;
    try {
      client = await pool.connect();

      const telemetryResult = await client.query(`
        SELECT tour_id, ship_symbol, leg_index, waypoint, is_buy,
               realized_units, realized_unit_price, realized_at
        FROM tour_leg_telemetry
        WHERE realized_at IS NOT NULL AND realized_at >= $1
        ORDER BY tour_id, ship_symbol, leg_index, realized_at
      `, [sinceIso]);

      const arbResult = await client.query(`
        SELECT buy_market, sell_market, units_sold, actual_net_profit, executed_at
        FROM arbitrage_execution_logs
        WHERE success = true AND executed_at >= $1
      `, [sinceIso]);

      const telemetry = telemetryResult.rows.map((r: any) => ({
        tourId: r.tour_id,
        shipSymbol: r.ship_symbol,
        legIndex: Number(r.leg_index),
        waypoint: r.waypoint,
        isBuy: Boolean(r.is_buy),
        realizedUnits: Number(r.realized_units) || 0,
        realizedUnitPrice: Number(r.realized_unit_price) || 0,
        realizedAt: new Date(r.realized_at).toISOString(),
      }));
      const arb = arbResult.rows.map((r: any) => ({
        buyMarket: r.buy_market,
        sellMarket: r.sell_market,
        unitsSold: Number(r.units_sold) || 0,
        actualNetProfit: Number(r.actual_net_profit) || 0,
        executedAt: new Date(r.executed_at).toISOString(),
      }));

      const lanes = aggregateLanes(telemetry, arb, windowStartMs, windowEndMs);
      res.json({ lanes, window, generatedAt: new Date().toISOString() });
    } catch (error: any) {
      console.error('Failed to build flows lanes:', error?.message ?? error);
      res.status(503).json({ error: 'db_unavailable' });
    } finally {
      if (client) client.release();
    }
  });
  ```
- [ ] Run the route test to green:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/flows.lanes.test.ts
  ```
  Expected: 3 passing.
- [ ] Commit:
  ```bash
  cd visualizer/server && git add utils/laneAggregation.ts routes/flows.ts routes/__tests__/laneAggregation.test.ts routes/__tests__/flows.lanes.test.ts && git commit --no-verify -m "feat(flows-server): realized-lane aggregation + /api/flows/lanes (Task 3)"
  ```

### Task 4 — Server: `/api/flows/live` (daemon proxy + PG nav join + feed-loss)

> **Resolved ambiguity (nav-join source + daemon URL).** The spec says live "joins ship nav (position truth)" but does not name the source; verified the PG `ships` table carries `nav_status, system_symbol, location_symbol, location_x, location_y, arrival_time` — the join source (no SpaceTraders API call, which would be rate-limited on a 5s poll). Daemon feed URL default is `http://localhost:9090/api/flows` (gobot metrics port default 9090, verified in `gobot/internal/infrastructure/config/defaults.go`), overridable via `DAEMON_FLOWS_URL`. Distinguish the two failure modes: **daemon unreachable → `feedLost: true`, `flows: []`** (NOT an error — the tab keeps working on PG); **PG down during the nav join → HTTP 503** (standard error state). A hung daemon must not stall the 5s poll, so the fetch has a 2s AbortController timeout.

**Files:**
- Modify: `visualizer/server/routes/flows.ts` (append the `/live` handler)
- Create: `visualizer/server/routes/__tests__/flows.live.test.ts`

**Interfaces:**
- Produces: `GET /api/flows/live` → `LiveFlowsResponse` (Task 1 wire shape). Feed lost → `{ flows: [], feedLost: true, lastPlanAt: null, generatedAt }` with HTTP 200. PG down → 503 `{ error: 'db_unavailable' }`.
- Consumes: global `fetch` (Node 22) against `DAEMON_FLOWS_URL`; PG `ships` table via the Task-2 pool.

**Steps:**

- [ ] Write the failing route test `visualizer/server/routes/__tests__/flows.live.test.ts`:
  ```ts
  import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
  import express from 'express';
  import request from 'supertest';

  const connect = vi.fn();
  vi.mock('pg', () => ({
    default: { Pool: class { on() {} connect() { return connect(); } } },
  }));

  async function makeApp() {
    const { default: flowsRouter } = await import('../flows.js');
    const app = express();
    app.use('/api/flows', flowsRouter);
    return app;
  }

  const daemonFlow = {
    containerId: 'tour-run-S1-abc',
    program: 'tour',
    ship: 'S1',
    tourId: 'tour-run-S1-abc',
    currentLeg: { from: 'X1-A-1', to: 'X1-A-2', departedAt: '2026-07-11T00:00:00Z', arrivesAt: '2026-07-11T00:05:00Z' },
    cargo: [{ good: 'FABRICS', units: 120 }],
    remainingHops: [{ waypoint: 'X1-A-2', tranches: [{ good: 'FABRICS', isBuy: false, units: 120, expectedUnitPrice: 1600 }] }],
    projected: { profit: 5000, ratePerHour: 9000 },
    plannedAt: '2026-07-11T00:00:00Z',
  };

  beforeEach(() => {
    connect.mockReset();
    vi.resetModules();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe('GET /api/flows/live', () => {
    it('proxies the daemon feed and enriches each flow with PG ship nav', async () => {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ flows: [daemonFlow], generatedAt: '2026-07-11T00:01:00Z' }),
      }));
      const query = vi.fn().mockResolvedValue({ rows: [
        { ship_symbol: 'S1', nav_status: 'IN_TRANSIT', system_symbol: 'X1-A', location_symbol: 'X1-A-1', location_x: 10, location_y: -4, arrival_time: '2026-07-11T00:05:00Z' },
      ] });
      connect.mockResolvedValue({ query, release: vi.fn() });

      const app = await makeApp();
      const res = await request(app).get('/api/flows/live');

      expect(res.status).toBe(200);
      expect(res.body.feedLost).toBe(false);
      expect(res.body.flows).toHaveLength(1);
      expect(res.body.flows[0].shipNav).toMatchObject({ status: 'IN_TRANSIT', systemSymbol: 'X1-A', x: 10, y: -4 });
      expect(res.body.lastPlanAt).toBe('2026-07-11T00:00:00Z');
    });

    it('reports feedLost with empty flows when the daemon is unreachable', async () => {
      vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('ECONNREFUSED')));
      // PG is healthy but there are no flows to join.
      connect.mockResolvedValue({ query: vi.fn().mockResolvedValue({ rows: [] }), release: vi.fn() });

      const app = await makeApp();
      const res = await request(app).get('/api/flows/live');

      expect(res.status).toBe(200);
      expect(res.body).toMatchObject({ flows: [], feedLost: true, lastPlanAt: null });
    });

    it('degrades to 503 when the daemon is up but PG is down', async () => {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ flows: [daemonFlow], generatedAt: '2026-07-11T00:01:00Z' }),
      }));
      connect.mockRejectedValue(new Error('ECONNREFUSED'));

      const app = await makeApp();
      const res = await request(app).get('/api/flows/live');

      expect(res.status).toBe(503);
      expect(res.body).toEqual({ error: 'db_unavailable' });
    });

    it('treats a non-200 daemon response as feed loss', async () => {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 502, json: async () => ({}) }));
      connect.mockResolvedValue({ query: vi.fn().mockResolvedValue({ rows: [] }), release: vi.fn() });

      const app = await makeApp();
      const res = await request(app).get('/api/flows/live');
      expect(res.status).toBe(200);
      expect(res.body.feedLost).toBe(true);
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/flows.live.test.ts
  ```
  Expected failure: `/live` returns 404 (handler not yet added).
- [ ] Append the `/live` handler to `visualizer/server/routes/flows.ts`, immediately before `export default router;`:
  ```ts
  // ---- GET /api/flows/live -----------------------------------------------------
  // Proxy the daemon in-memory plan feed, join PG ships for position truth. The
  // browser never talks to the daemon directly — this is the only hop. Daemon
  // unreachable/slow/non-200 => feedLost (tab keeps working on PG); PG down during
  // the nav join => 503.
  const DAEMON_FLOWS_URL = process.env.DAEMON_FLOWS_URL || 'http://localhost:9090/api/flows';
  const DAEMON_TIMEOUT_MS = 2000;

  interface RawDaemonFlow {
    containerId: string;
    program: 'tour' | 'trade-route' | 'arb';
    ship: string;
    tourId: string | null;
    currentLeg: { from: string; to: string; departedAt: string; arrivesAt: string } | null;
    cargo: { good: string; units: number }[];
    remainingHops: { waypoint: string; tranches: { good: string; isBuy: boolean; units: number; expectedUnitPrice: number }[] }[];
    projected: { profit: number; ratePerHour: number } | null;
    plannedAt: string;
  }

  async function fetchDaemonFlows(): Promise<{ flows: RawDaemonFlow[] } | null> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), DAEMON_TIMEOUT_MS);
    try {
      const resp = await fetch(DAEMON_FLOWS_URL, { signal: controller.signal });
      if (!resp.ok) return null;
      const body = await resp.json();
      return { flows: Array.isArray(body?.flows) ? body.flows : [] };
    } catch {
      return null; // unreachable / timeout / bad JSON => feed lost
    } finally {
      clearTimeout(timer);
    }
  }

  router.get('/live', async (_req, res) => {
    const feed = await fetchDaemonFlows();

    if (feed === null) {
      // Feed lost: the tab still works on PG residue; never fabricate intent.
      return res.json({ flows: [], generatedAt: new Date().toISOString(), feedLost: true, lastPlanAt: null });
    }

    // Feed up: join PG ships for last-known position truth. PG failure here is a
    // real error state (503), distinct from feed loss.
    let client;
    try {
      client = await pool.connect();
      const shipSymbols = [...new Set(feed.flows.map((f) => f.ship))];
      const navByShip = new Map<string, any>();
      if (shipSymbols.length > 0) {
        const result = await client.query(`
          SELECT ship_symbol, nav_status, system_symbol, location_symbol,
                 location_x, location_y, arrival_time
          FROM ships
          WHERE ship_symbol = ANY($1)
        `, [shipSymbols]);
        for (const r of result.rows) navByShip.set(r.ship_symbol, r);
      }

      const flows = feed.flows.map((f) => {
        const nav = navByShip.get(f.ship);
        return {
          ...f,
          shipNav: nav
            ? {
                status: nav.nav_status,
                systemSymbol: nav.system_symbol,
                waypointSymbol: nav.location_symbol,
                x: Number(nav.location_x) || 0,
                y: Number(nav.location_y) || 0,
                arrivalTime: nav.arrival_time ? new Date(nav.arrival_time).toISOString() : null,
              }
            : null,
        };
      });

      const lastPlanAt = flows.reduce<string | null>(
        (max, f) => (max === null || f.plannedAt > max ? f.plannedAt : max),
        null,
      );

      res.json({ flows, generatedAt: new Date().toISOString(), feedLost: false, lastPlanAt });
    } catch (error: any) {
      console.error('Failed to join ship nav for flows/live:', error?.message ?? error);
      res.status(503).json({ error: 'db_unavailable' });
    } finally {
      if (client) client.release();
    }
  });
  ```
- [ ] Run the route test to green:
  ```bash
  cd visualizer/server && npx vitest run routes/__tests__/flows.live.test.ts
  ```
  Expected: 4 passing.
- [ ] Run the whole server suite + typecheck:
  ```bash
  cd visualizer/server && npx vitest run && npx tsc --noEmit
  ```
  Expected: all flows tests pass, no TS errors.
- [ ] Commit:
  ```bash
  cd visualizer/server && git add routes/flows.ts routes/__tests__/flows.live.test.ts && git commit --no-verify -m "feat(flows-server): /api/flows/live daemon proxy + PG nav join + feed-loss (Task 4)"
  ```

### Task 5 — Web: flows API client + `flowStore` + polling hook

> **Resolved ambiguity (`stores/` vs `store/`).** The spec writes `stores/flowStore.ts`, but the repo convention is the singular `store/` directory (`store/useStore.ts`). Resolution: `store/flowStore.ts`, a SEPARATE Zustand store via `create()` (keeps the 548-line `useStore` untouched and flow state isolated). The `lastPlanAt` is kept **sticky** in the store — the server nulls it on feed loss, so the store retains the last non-null value for the chip's "last plan mm:ss ago".

**Files:**
- Create: `visualizer/web/src/services/api/flows.ts`
- Create: `visualizer/web/src/store/flowStore.ts`
- Create: `visualizer/web/src/hooks/useFlowsPolling.ts`
- Create: `visualizer/web/src/store/__tests__/flowStore.test.ts`

**Interfaces:**
- Produces (client): `getFlowsLive(): Promise<LiveFlowsResponse>`, `getFlowLanes(window: FlowWindow): Promise<LanesResponse>`, `getFlowTopology(): Promise<TopologyResponse>`.
- Produces (store): `useFlowStore` with state `{ topology, lanes, live, window, lastPlanAt, selectedFlowId, drilldownSystem, error }` and actions `setTopology`, `setLanes`, `setLive`, `setWindow`, `selectFlow`, `openDrilldown`, `closeDrilldown`, `setError`.
- Produces (hook): `useFlowsPolling()` — polls live 5s, lanes 30s, topology once.
- Consumes: `fetchApi` from `services/api/client`; Task-1 types.

**Steps:**

- [ ] Write the failing store test `visualizer/web/src/store/__tests__/flowStore.test.ts`:
  ```ts
  import { describe, it, expect, beforeEach } from 'vitest';
  import { useFlowStore } from '../flowStore';
  import { mockLiveFlows, mockFeedLostResponse } from '../../mocks/mockFlows';

  describe('flowStore', () => {
    beforeEach(() => {
      useFlowStore.setState(useFlowStore.getInitialState());
    });

    it('setLive stores flows and updates sticky lastPlanAt on a live feed', () => {
      const res = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
      useFlowStore.getState().setLive(res);
      expect(useFlowStore.getState().live?.flows).toHaveLength(3);
      expect(useFlowStore.getState().lastPlanAt).toBe(res.lastPlanAt);
    });

    it('keeps the previous lastPlanAt sticky when a feed-loss response arrives', () => {
      const live = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
      useFlowStore.getState().setLive(live);
      const before = useFlowStore.getState().lastPlanAt;
      useFlowStore.getState().setLive(mockFeedLostResponse(Date.parse('2026-07-11T00:10:00Z')));
      expect(useFlowStore.getState().live?.feedLost).toBe(true);
      expect(useFlowStore.getState().lastPlanAt).toBe(before); // unchanged, not null
    });

    it('setWindow switches the active lane window', () => {
      useFlowStore.getState().setWindow('24h');
      expect(useFlowStore.getState().window).toBe('24h');
    });

    it('selectFlow and drilldown toggles round-trip', () => {
      const s = useFlowStore.getState();
      s.selectFlow('tour-run-1');
      expect(useFlowStore.getState().selectedFlowId).toBe('tour-run-1');
      s.openDrilldown('X1-KA42');
      expect(useFlowStore.getState().drilldownSystem).toBe('X1-KA42');
      s.closeDrilldown();
      expect(useFlowStore.getState().drilldownSystem).toBeNull();
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/web && npx vitest run src/store/__tests__/flowStore.test.ts
  ```
  Expected failure: cannot resolve `../flowStore`.
- [ ] Create `visualizer/web/src/services/api/flows.ts` (complete):
  ```ts
  import { fetchApi } from './client';
  import type { LiveFlowsResponse, LanesResponse, TopologyResponse, FlowWindow } from '../../types/flows';

  export async function getFlowsLive(): Promise<LiveFlowsResponse> {
    return fetchApi<LiveFlowsResponse>('/flows/live');
  }

  export async function getFlowLanes(window: FlowWindow): Promise<LanesResponse> {
    return fetchApi<LanesResponse>(`/flows/lanes?window=${window}`);
  }

  export async function getFlowTopology(): Promise<TopologyResponse> {
    return fetchApi<TopologyResponse>('/flows/topology');
  }
  ```
- [ ] Create `visualizer/web/src/store/flowStore.ts` (complete):
  ```ts
  import { create } from 'zustand';
  import type { LiveFlowsResponse, LanesResponse, TopologyResponse, FlowWindow } from '../types/flows';

  export interface FlowState {
    topology: TopologyResponse | null;
    lanes: LanesResponse | null;
    live: LiveFlowsResponse | null;
    window: FlowWindow;
    lastPlanAt: string | null;   // sticky across feed loss
    selectedFlowId: string | null;
    drilldownSystem: string | null;
    error: string | null;

    setTopology: (t: TopologyResponse) => void;
    setLanes: (l: LanesResponse) => void;
    setLive: (l: LiveFlowsResponse) => void;
    setWindow: (w: FlowWindow) => void;
    selectFlow: (containerId: string | null) => void;
    openDrilldown: (systemSymbol: string) => void;
    closeDrilldown: () => void;
    setError: (message: string | null) => void;
  }

  export const useFlowStore = create<FlowState>((set) => ({
    topology: null,
    lanes: null,
    live: null,
    window: '6h',
    lastPlanAt: null,
    selectedFlowId: null,
    drilldownSystem: null,
    error: null,

    setTopology: (topology) => set({ topology, error: null }),
    setLanes: (lanes) => set({ lanes, error: null }),
    // lastPlanAt is sticky: only advance it when the server reports a real plan.
    setLive: (live) =>
      set((state) => ({
        live,
        error: null,
        lastPlanAt: live.lastPlanAt ?? state.lastPlanAt,
      })),
    setWindow: (window) => set({ window }),
    selectFlow: (selectedFlowId) => set({ selectedFlowId }),
    openDrilldown: (drilldownSystem) => set({ drilldownSystem }),
    closeDrilldown: () => set({ drilldownSystem: null }),
    setError: (error) => set({ error }),
  }));
  ```
- [ ] Create `visualizer/web/src/hooks/useFlowsPolling.ts` (complete). Mirrors the `useFinancialPolling` interval idiom; topology fetched once, lanes on window change + every 30s, live every 5s:
  ```ts
  import { useEffect, useRef } from 'react';
  import { useFlowStore } from '../store/flowStore';
  import { getFlowsLive, getFlowLanes, getFlowTopology } from '../services/api/flows';

  const LIVE_INTERVAL_MS = 5000;
  const LANES_INTERVAL_MS = 30000;

  export function useFlowsPolling() {
    const window = useFlowStore((s) => s.window);
    const setTopology = useFlowStore((s) => s.setTopology);
    const setLanes = useFlowStore((s) => s.setLanes);
    const setLive = useFlowStore((s) => s.setLive);
    const setError = useFlowStore((s) => s.setError);

    // Topology once per mount.
    useEffect(() => {
      let cancelled = false;
      getFlowTopology()
        .then((t) => { if (!cancelled) setTopology(t); })
        .catch((e) => { if (!cancelled) setError(e?.message ?? 'topology failed'); });
      return () => { cancelled = true; };
    }, [setTopology, setError]);

    // Live feed every 5s.
    useEffect(() => {
      let cancelled = false;
      const tick = () => {
        getFlowsLive()
          .then((l) => { if (!cancelled) setLive(l); })
          .catch((e) => { if (!cancelled) setError(e?.message ?? 'live feed failed'); });
      };
      tick();
      const id = setInterval(tick, LIVE_INTERVAL_MS);
      return () => { cancelled = true; clearInterval(id); };
    }, [setLive, setError]);

    // Lanes every 30s and immediately on window change.
    useEffect(() => {
      let cancelled = false;
      const tick = () => {
        getFlowLanes(window)
          .then((l) => { if (!cancelled) setLanes(l); })
          .catch((e) => { if (!cancelled) setError(e?.message ?? 'lanes failed'); });
      };
      tick();
      const id = setInterval(tick, LANES_INTERVAL_MS);
      return () => { cancelled = true; clearInterval(id); };
    }, [window, setLanes, setError]);
  }
  ```
- [ ] Run the store test to green:
  ```bash
  cd visualizer/web && npx vitest run src/store/__tests__/flowStore.test.ts
  ```
  Expected: 4 passing.
- [ ] Commit:
  ```bash
  cd visualizer/web && git add src/services/api/flows.ts src/store/flowStore.ts src/hooks/useFlowsPolling.ts src/store/__tests__/flowStore.test.ts && git commit --no-verify -m "feat(flows-web): flows API client + flowStore + polling hook (Task 5)"
  ```

### Task 6 — Web: flow scene geometry helpers (pure, tested)

The Konva scene components (Task 7) render canvas, which jsdom cannot exercise — so, following the `routeVectorsUtils.ts` + `RouteVectors.test.ts` idiom (and `domain/ship.ts`'s interpolation), all non-trivial math lives here in pure helpers with unit tests. The scene components stay thin.

**Files:**
- Create: `visualizer/web/src/components/flows/flowGeometry.ts`
- Create: `visualizer/web/src/components/flows/__tests__/flowGeometry.test.ts`

**Interfaces:**
- Produces: `systemOf(waypoint: string): string`; `buildSystemIndex(topology: TopologyResponse): Map<string, Point>`; `projectFlowShip(flow: LiveFlow, systemPos: Map<string, Point>, nowMs: number): Point | null`; `laneEndpoints(lane: LaneRecord, systemPos: Map<string, Point>): { from: Point; to: Point } | null`; `laneProfitColor(profit: number): string`; `laneWidth(profit: number, scale: number): number`; `planPathPoints(flow: LiveFlow, systemPos: Map<string, Point>): number[][]`. `Point = { x: number; y: number }`.
- Consumes: Task-1 types; `NOIR` from `theme/noir.ts`.

**Steps:**

- [ ] Write the failing test `visualizer/web/src/components/flows/__tests__/flowGeometry.test.ts`:
  ```ts
  import { describe, it, expect } from 'vitest';
  import {
    systemOf,
    buildSystemIndex,
    projectFlowShip,
    laneProfitColor,
    laneWidth,
    planPathPoints,
  } from '../flowGeometry';
  import { mockTopology, mockLiveFlows } from '../../../mocks/mockFlows';
  import type { LiveFlow } from '../../../types/flows';

  const idx = buildSystemIndex(mockTopology);

  describe('systemOf', () => {
    it('extracts the system prefix from a waypoint symbol', () => {
      expect(systemOf('X1-NK36-FE8A')).toBe('X1-NK36');
      expect(systemOf('X1-KA42-D39')).toBe('X1-KA42');
    });
  });

  describe('projectFlowShip', () => {
    const baseFlow = (overrides: Partial<LiveFlow>): LiveFlow => ({
      containerId: 'c', program: 'tour', ship: 'S', tourId: null,
      currentLeg: { from: 'X1-NK36-A', to: 'X1-KA42-B', departedAt: '2026-07-11T00:00:00Z', arrivesAt: '2026-07-11T00:10:00Z' },
      cargo: [], remainingHops: [], projected: null, plannedAt: '2026-07-11T00:00:00Z', shipNav: null,
      ...overrides,
    });

    it('clamps to the origin system before departure', () => {
      const p = projectFlowShip(baseFlow({}), idx, Date.parse('2026-07-10T23:00:00Z'));
      expect(p).toEqual(idx.get('X1-NK36'));
    });

    it('clamps to the destination system after arrival', () => {
      const p = projectFlowShip(baseFlow({}), idx, Date.parse('2026-07-11T01:00:00Z'));
      expect(p).toEqual(idx.get('X1-KA42'));
    });

    it('interpolates halfway at the leg midpoint', () => {
      const from = idx.get('X1-NK36')!;
      const to = idx.get('X1-KA42')!;
      const p = projectFlowShip(baseFlow({}), idx, Date.parse('2026-07-11T00:05:00Z'))!;
      expect(p.x).toBeCloseTo((from.x + to.x) / 2, 3);
      expect(p.y).toBeCloseTo((from.y + to.y) / 2, 3);
    });

    it('returns the origin position for an intra-system leg', () => {
      const p = projectFlowShip(
        baseFlow({ currentLeg: { from: 'X1-NK36-A', to: 'X1-NK36-B', departedAt: '2026-07-11T00:00:00Z', arrivesAt: '2026-07-11T00:10:00Z' } }),
        idx, Date.parse('2026-07-11T00:05:00Z'),
      );
      expect(p).toEqual(idx.get('X1-NK36'));
    });

    it('falls back to last-known shipNav system when no current leg', () => {
      const p = projectFlowShip(
        baseFlow({ currentLeg: null, shipNav: { status: 'DOCKED', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-C', x: 0, y: 0, arrivalTime: null } }),
        idx, Date.now(),
      );
      expect(p).toEqual(idx.get('X1-ZC66'));
    });

    it('returns null when neither leg nor known nav resolves', () => {
      const p = projectFlowShip(baseFlow({ currentLeg: null, shipNav: null }), idx, Date.now());
      expect(p).toBeNull();
    });
  });

  describe('laneProfitColor / laneWidth', () => {
    it('maps loss to the dim token and large profit to the star token', () => {
      expect(laneProfitColor(-5000)).toBe('#5A6478');
      expect(laneProfitColor(500000)).toBe('#F5E9C8');
    });
    it('width grows with magnitude and shrinks with scale', () => {
      expect(laneWidth(500000, 1)).toBeGreaterThan(laneWidth(100, 1));
      expect(laneWidth(500000, 4)).toBeLessThan(laneWidth(500000, 1));
    });
  });

  describe('planPathPoints', () => {
    it('emits one polyline segment per remaining hop, in system space', () => {
      const tour = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')).flows.find((f) => f.program === 'tour')!;
      const segments = planPathPoints(tour, idx);
      expect(segments.length).toBe(tour.remainingHops.length);
      for (const seg of segments) expect(seg).toHaveLength(4); // [x1,y1,x2,y2]
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/web && npx vitest run src/components/flows/__tests__/flowGeometry.test.ts
  ```
  Expected failure: cannot resolve `../flowGeometry`.
- [ ] Create `visualizer/web/src/components/flows/flowGeometry.ts` (complete):
  ```ts
  import { NOIR } from '../../theme/noir';
  import type { LiveFlow, LaneRecord, TopologyResponse } from '../../types/flows';

  export interface Point {
    x: number;
    y: number;
  }

  // "X1-NK36-FE8A" -> "X1-NK36"
  export function systemOf(waypoint: string): string {
    const parts = waypoint.split('-');
    return parts.length >= 2 ? `${parts[0]}-${parts[1]}` : waypoint;
  }

  export function buildSystemIndex(topology: TopologyResponse): Map<string, Point> {
    return new Map(topology.systems.map((s) => [s.symbol, { x: s.x, y: s.y }]));
  }

  // Galaxy-space position of a flow's hull. Interpolates the current leg between
  // its endpoint SYSTEMS using departedAt/arrivesAt with the same clamp math as
  // domain/ship.ts (before departure -> origin, after arrival -> destination).
  // Intra-system legs collapse to the origin; with no leg, fall back to last-known
  // PG nav system; otherwise null (position-only glyphs are dropped upstream).
  export function projectFlowShip(
    flow: LiveFlow,
    systemPos: Map<string, Point>,
    nowMs: number,
  ): Point | null {
    const leg = flow.currentLeg;
    if (leg) {
      const fromSys = systemOf(leg.from);
      const toSys = systemOf(leg.to);
      const from = systemPos.get(fromSys);
      const to = systemPos.get(toSys);
      const dep = Date.parse(leg.departedAt);
      const arr = Date.parse(leg.arrivesAt);
      if (from && to && !Number.isNaN(dep) && !Number.isNaN(arr)) {
        if (fromSys === toSys) return { x: from.x, y: from.y };
        const progress = (nowMs - dep) / Math.max(arr - dep, 1);
        const tClamped = Math.max(0, Math.min(1, progress));
        return { x: from.x + (to.x - from.x) * tClamped, y: from.y + (to.y - from.y) * tClamped };
      }
    }
    const nav = flow.shipNav;
    if (nav) {
      const p = systemPos.get(nav.systemSymbol);
      if (p) return { x: p.x, y: p.y };
    }
    return null;
  }

  export function laneEndpoints(
    lane: LaneRecord,
    systemPos: Map<string, Point>,
  ): { from: Point; to: Point } | null {
    const from = systemPos.get(systemOf(lane.from));
    const to = systemPos.get(systemOf(lane.to));
    if (!from || !to) return null;
    return { from, to };
  }

  // Profit -> Noir color ramp: loss dim, then good-green, accent-blue, star-gold.
  export function laneProfitColor(profit: number): string {
    if (profit <= 0) return NOIR.dim;        // #5A6478
    if (profit < 50_000) return NOIR.good;   // #3DD68C
    if (profit < 250_000) return NOIR.accent; // #7DB1FF
    return NOIR.star;                          // #F5E9C8
  }

  // Log-scaled stroke width, divided by the current stage scale so lanes hold a
  // roughly constant on-screen weight while zooming (the TradeRouteLayer idiom).
  export function laneWidth(profit: number, scale: number): number {
    const mag = Math.min(6, Math.max(0.5, Math.log10(Math.abs(profit) + 10) - 1));
    return Math.max(0.5, mag / scale);
  }

  // Remaining planned hops as polyline segments in system space. Consecutive hop
  // waypoints (and the current leg's destination as the first anchor) are paired.
  export function planPathPoints(flow: LiveFlow, systemPos: Map<string, Point>): number[][] {
    const anchors: string[] = [];
    if (flow.currentLeg) anchors.push(flow.currentLeg.to);
    for (const hop of flow.remainingHops) anchors.push(hop.waypoint);

    const segments: number[][] = [];
    for (let i = 1; i < anchors.length; i++) {
      const a = systemPos.get(systemOf(anchors[i - 1]));
      const b = systemPos.get(systemOf(anchors[i]));
      if (!a || !b) continue;
      segments.push([a.x, a.y, b.x, b.y]);
    }
    // When every hop resolves, the segment count equals remainingHops.length.
    return segments;
  }
  ```
- [ ] Run the test to green:
  ```bash
  cd visualizer/web && npx vitest run src/components/flows/__tests__/flowGeometry.test.ts
  ```
  Expected: all passing.
- [ ] Commit:
  ```bash
  cd visualizer/web && git add src/components/flows/flowGeometry.ts src/components/flows/__tests__/flowGeometry.test.ts && git commit --no-verify -m "feat(flows-web): pure scene geometry helpers (Task 6)"
  ```

### Task 7 — Web: Konva scene components (galaxy stage, lanes, ships, plan paths)

Thin Konva components over the Task-6 helpers, following `GalaxyView.tsx` (Stage + zoom-to-cursor + scale state via `CANVAS_CONSTANTS`) and `TradeRouteLayer.tsx` (arrow/label/dash idiom, `listening={false}`, scale-aware sizing). No jsdom render tests here (canvas is unavailable in jsdom — the math is already covered by Task 6; visual correctness is Task 10). A 1s `requestAnimationFrame`-free clock tick (`setInterval`) advances ship interpolation and the lane dash, matching the observatory's per-frame reposition without a game loop.

**Files:**
- Create: `visualizer/web/src/components/flows/FlowLaneLayer.tsx`
- Create: `visualizer/web/src/components/flows/FlowShipLayer.tsx`
- Create: `visualizer/web/src/components/flows/FlowPlanPath.tsx`
- Create: `visualizer/web/src/components/flows/FlowGalaxyScene.tsx`

**Interfaces:**
- Consumes: `useFlowStore` (topology, lanes, live, selectedFlowId), Task-6 helpers, `NOIR`/`noirAlpha`, `CANVAS_CONSTANTS`.
- Produces: `FlowGalaxyScene` (default export) rendering `<Stage>` with a `Layer` of lanes → system nodes → plan paths → ship glyphs; calls `useFlowStore.selectFlow` / `openDrilldown` on click.

**Steps:**

- [ ] Create `visualizer/web/src/components/flows/FlowLaneLayer.tsx` (complete):
  ```tsx
  import { memo } from 'react';
  import { Group, Line } from 'react-konva';
  import type { LanesResponse } from '../../types/flows';
  import { laneEndpoints, laneProfitColor, laneWidth, type Point } from './flowGeometry';

  interface Props {
    lanes: LanesResponse | null;
    systemPos: Map<string, Point>;
    scale: number;
    dashOffset: number;
  }

  // Gate/realized lanes: profit-colored, thickness by log(profit), animated dash
  // for a subtle flow direction cue (mirrors TradeRouteLayer).
  export const FlowLaneLayer = memo(function FlowLaneLayer({ lanes, systemPos, scale, dashOffset }: Props) {
    if (!lanes) return null;
    return (
      <Group listening={false}>
        {lanes.lanes.map((lane, i) => {
          const ep = laneEndpoints(lane, systemPos);
          if (!ep) return null;
          const color = laneProfitColor(lane.realizedProfit);
          const width = laneWidth(lane.realizedProfit, scale);
          const dash = 10 / scale;
          const gap = 6 / scale;
          return (
            <Line
              key={`lane-${i}-${lane.from}-${lane.to}`}
              points={[ep.from.x, ep.from.y, ep.to.x, ep.to.y]}
              stroke={color}
              strokeWidth={width}
              opacity={0.75}
              dash={[dash, gap]}
              dashOffset={dashOffset / scale}
              lineCap="round"
              listening={false}
            />
          );
        })}
      </Group>
    );
  });
  ```
- [ ] Create `visualizer/web/src/components/flows/FlowPlanPath.tsx` (complete):
  ```tsx
  import { memo } from 'react';
  import { Group, Line, Circle } from 'react-konva';
  import type { LiveFlow } from '../../types/flows';
  import { planPathPoints, systemOf, type Point } from './flowGeometry';
  import { NOIR, noirAlpha } from '../../theme/noir';

  interface Props {
    flow: LiveFlow;
    systemPos: Map<string, Point>;
    scale: number;
  }

  // The uniquely-daemon-provided intent: remaining planned hops as a dimming dashed
  // path with a marker per hop. Rendered only when the flow HAS a plan (never
  // synthesized) — the caller passes only flows with remainingHops.
  export const FlowPlanPath = memo(function FlowPlanPath({ flow, systemPos, scale }: Props) {
    const segments = planPathPoints(flow, systemPos);
    if (segments.length === 0) return null;
    const dash = 6 / scale;
    return (
      <Group listening={false}>
        {segments.map((seg, i) => (
          <Line
            key={`plan-${flow.containerId}-${i}`}
            points={seg}
            stroke={NOIR.accentSoft}
            strokeWidth={Math.max(0.5, 1.4 / scale)}
            opacity={0.55 - i * 0.08}
            dash={[dash, dash]}
            lineCap="round"
            listening={false}
          />
        ))}
        {flow.remainingHops.map((hop, i) => {
          const p = systemPos.get(systemOf(hop.waypoint));
          if (!p) return null;
          return (
            <Circle
              key={`hop-${flow.containerId}-${i}`}
              x={p.x}
              y={p.y}
              radius={Math.max(1.5, 3 / scale)}
              fill={noirAlpha(NOIR.accentSoft, 0.5)}
              listening={false}
            />
          );
        })}
      </Group>
    );
  });
  ```
- [ ] Create `visualizer/web/src/components/flows/FlowShipLayer.tsx` (complete):
  ```tsx
  import { memo } from 'react';
  import { Group, Circle, Text } from 'react-konva';
  import type { LiveFlow } from '../../types/flows';
  import { projectFlowShip, type Point } from './flowGeometry';
  import { NOIR } from '../../theme/noir';

  interface Props {
    flows: LiveFlow[];
    systemPos: Map<string, Point>;
    nowMs: number;
    scale: number;
    selectedFlowId: string | null;
    onSelect: (containerId: string) => void;
  }

  const PROGRAM_COLOR: Record<LiveFlow['program'], string> = {
    tour: NOIR.star,
    'trade-route': NOIR.accent,
    arb: NOIR.good,
  };

  // Hull glyphs glide along their current leg (interpolated per tick). Clicking a
  // hull selects its flow for the detail panel.
  export const FlowShipLayer = memo(function FlowShipLayer({
    flows, systemPos, nowMs, scale, selectedFlowId, onSelect,
  }: Props) {
    return (
      <Group>
        {flows.map((flow) => {
          const pos = projectFlowShip(flow, systemPos, nowMs);
          if (!pos) return null;
          const color = PROGRAM_COLOR[flow.program];
          const selected = flow.containerId === selectedFlowId;
          const r = Math.max(2, 4 / scale);
          return (
            <Group key={`ship-${flow.containerId}`} x={pos.x} y={pos.y}>
              {selected && (
                <Circle radius={r + 3 / scale} stroke={NOIR.ink} strokeWidth={1 / scale} opacity={0.9} />
              )}
              <Circle
                radius={r}
                fill={color}
                onMouseEnter={(e) => { const c = e.target.getStage()?.container(); if (c) c.style.cursor = 'pointer'; }}
                onMouseLeave={(e) => { const c = e.target.getStage()?.container(); if (c) c.style.cursor = 'default'; }}
                onClick={() => onSelect(flow.containerId)}
                onTouchStart={() => onSelect(flow.containerId)}
              />
              {scale > 0.4 && (
                <Text
                  text={flow.ship}
                  fontSize={Math.max(6, 9 / scale)}
                  fill={NOIR.muted}
                  x={r + 2 / scale}
                  y={-r}
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
- [ ] Create `visualizer/web/src/components/flows/FlowGalaxyScene.tsx` (complete):
  ```tsx
  import { useEffect, useRef, useState } from 'react';
  import { Stage, Layer, Circle, Text, Group, Line } from 'react-konva';
  import type Konva from 'konva';
  import { useFlowStore } from '../../store/flowStore';
  import { CANVAS_CONSTANTS } from '../../constants/canvas';
  import { NOIR, noirAlpha } from '../../theme/noir';
  import { buildSystemIndex, systemOf, type Point } from './flowGeometry';
  import { FlowLaneLayer } from './FlowLaneLayer';
  import { FlowShipLayer } from './FlowShipLayer';
  import { FlowPlanPath } from './FlowPlanPath';

  export default function FlowGalaxyScene() {
    const topology = useFlowStore((s) => s.topology);
    const lanes = useFlowStore((s) => s.lanes);
    const live = useFlowStore((s) => s.live);
    const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
    const selectFlow = useFlowStore((s) => s.selectFlow);
    const openDrilldown = useFlowStore((s) => s.openDrilldown);

    const stageRef = useRef<Konva.Stage>(null);
    const [scale, setScale] = useState(0.5);
    const [nowMs, setNowMs] = useState(() => Date.now());
    const [dashOffset, setDashOffset] = useState(0);
    const centeredRef = useRef<string | null>(null);

    const width = window.innerWidth;
    const height = window.innerHeight - 64; // minus nav bar

    // Advance the interpolation clock + lane dash once a second.
    useEffect(() => {
      const id = setInterval(() => {
        setNowMs(Date.now());
        setDashOffset((d) => (d + 1) % 1000);
      }, 1000);
      return () => clearInterval(id);
    }, []);

    // Center once per topology (mirrors GalaxyView's centeredKeyRef guard).
    useEffect(() => {
      if (!stageRef.current || !topology || topology.systems.length === 0) return;
      const key = topology.systems.map((s) => s.symbol).sort().join(',');
      if (centeredRef.current === key) return;
      centeredRef.current = key;
      const avgX = topology.systems.reduce((sum, s) => sum + s.x, 0) / topology.systems.length;
      const avgY = topology.systems.reduce((sum, s) => sum + s.y, 0) / topology.systems.length;
      const initial = 0.4;
      setScale(initial);
      stageRef.current.scale({ x: initial, y: initial });
      stageRef.current.position({ x: width / 2 - avgX * initial, y: height / 2 - avgY * initial });
    }, [topology, width, height]);

    const handleWheel = (e: Konva.KonvaEventObject<WheelEvent>) => {
      e.evt.preventDefault();
      const stage = e.target.getStage();
      if (!stage) return;
      const oldScale = stage.scaleX();
      const pointer = stage.getPointerPosition();
      if (!pointer) return;
      const mousePointTo = { x: (pointer.x - stage.x()) / oldScale, y: (pointer.y - stage.y()) / oldScale };
      const delta = e.evt.deltaY > 0 ? CANVAS_CONSTANTS.ZOOM_OUT_FACTOR : CANVAS_CONSTANTS.ZOOM_IN_FACTOR;
      const newScale = Math.max(
        CANVAS_CONSTANTS.MIN_ZOOM_GALAXY,
        Math.min(CANVAS_CONSTANTS.MAX_ZOOM_GALAXY, oldScale * delta),
      );
      stage.scale({ x: newScale, y: newScale });
      stage.position({ x: pointer.x - mousePointTo.x * newScale, y: pointer.y - mousePointTo.y * newScale });
      setScale(newScale);
    };

    if (!topology) {
      return (
        <div className="w-full h-full flex items-center justify-center" style={{ background: NOIR.bg0, color: NOIR.muted }}>
          Loading gate network…
        </div>
      );
    }

    const systemPos: Map<string, Point> = buildSystemIndex(topology);
    const flows = live?.flows ?? [];

    return (
      <div className="relative w-full h-full" style={{ background: NOIR.bg0 }}>
        <Stage ref={stageRef} width={width} height={height} draggable onWheel={handleWheel}>
          <Layer>
            <FlowLaneLayer lanes={lanes} systemPos={systemPos} scale={scale} dashOffset={dashOffset} />

            {/* Gate edges as hairlines (dashed when under construction) */}
            <Group listening={false}>
              {topology.edges.map((e, i) => {
                const a = systemPos.get(e.from);
                const b = systemPos.get(e.to);
                if (!a || !b) return null;
                return (
                  <Line
                    key={`edge-${i}-${e.from}-${e.to}`}
                    points={[a.x, a.y, b.x, b.y]}
                    stroke={noirAlpha(NOIR.nebula, 0.6)}
                    strokeWidth={Math.max(0.25, 0.5 / scale)}
                    dash={e.underConstruction ? [4 / scale, 4 / scale] : undefined}
                    listening={false}
                  />
                );
              })}
            </Group>

            {/* System nodes */}
            <Group>
              {topology.systems.map((s) => (
                <Group key={s.symbol} x={s.x} y={s.y}>
                  <Circle
                    radius={Math.max(2, 3 / scale)}
                    fill={noirAlpha(NOIR.nebulaCore, 0.9)}
                    stroke={NOIR.accent}
                    strokeWidth={0.5 / scale}
                    onMouseEnter={(ev) => { const c = ev.target.getStage()?.container(); if (c) c.style.cursor = 'pointer'; }}
                    onMouseLeave={(ev) => { const c = ev.target.getStage()?.container(); if (c) c.style.cursor = 'default'; }}
                    onClick={() => openDrilldown(s.symbol)}
                  />
                  {scale > 0.3 && (
                    <Text text={s.symbol} fontSize={Math.max(5, 8 / scale)} fill={NOIR.dim} x={4 / scale} y={-4 / scale} listening={false} />
                  )}
                </Group>
              ))}
            </Group>

            {/* Plan paths for flows that actually published intent */}
            <Group listening={false}>
              {flows.filter((f) => f.remainingHops.length > 0).map((f) => (
                <FlowPlanPath key={`pp-${f.containerId}`} flow={f} systemPos={systemPos} scale={scale} />
              ))}
            </Group>

            <FlowShipLayer
              flows={flows}
              systemPos={systemPos}
              nowMs={nowMs}
              scale={scale}
              selectedFlowId={selectedFlowId}
              onSelect={selectFlow}
            />
          </Layer>
        </Stage>
      </div>
    );
  }
  ```
- [ ] Typecheck the web app (Konva components have no unit test; tsc is the gate here):
  ```bash
  cd visualizer/web && npx tsc --noEmit
  ```
  Expected: no errors.
- [ ] Commit:
  ```bash
  cd visualizer/web && git add src/components/flows/FlowLaneLayer.tsx src/components/flows/FlowShipLayer.tsx src/components/flows/FlowPlanPath.tsx src/components/flows/FlowGalaxyScene.tsx && git commit --no-verify -m "feat(flows-web): Konva galaxy scene, lanes, ships, plan paths (Task 7)"
  ```

### Task 8 — Web: detail panel, system drilldown, FEED LOST chip (HTML/Tailwind, render-tested)

These are plain HTML/Tailwind (Noir tokens), so — unlike the Konva scene — they CAN and DO get `@testing-library/react` render tests (jsdom). These are the first `.test.tsx` files in the repo; that is expected (the dep is installed, `vitest.setup.ts` loads jest-dom).

**Files:**
- Create: `visualizer/web/src/components/flows/FlowDetailPanel.tsx`
- Create: `visualizer/web/src/components/flows/FeedLostChip.tsx`
- Create: `visualizer/web/src/components/flows/SystemDrilldown.tsx`
- Create: `visualizer/web/src/components/flows/feedLostChip.ts` (pure elapsed formatter)
- Create: `visualizer/web/src/components/flows/__tests__/FlowDetailPanel.test.tsx`
- Create: `visualizer/web/src/components/flows/__tests__/FeedLostChip.test.tsx`

**Interfaces:**
- Produces: `FlowDetailPanel({ flow }: { flow: LiveFlow | null })`; `FeedLostChip({ feedLost, lastPlanAt, nowMs }: {...})`; `SystemDrilldown({ systemSymbol, lanes, flows, onClose }: {...})`; `formatElapsed(fromIso: string | null, nowMs: number): string`.
- Consumes: Task-1 types, Task-6 `systemOf`, `NOIR`.

**Steps:**

- [ ] Write the failing chip formatter + render test `visualizer/web/src/components/flows/__tests__/FeedLostChip.test.tsx`:
  ```tsx
  import { describe, it, expect } from 'vitest';
  import { render, screen } from '@testing-library/react';
  import { FeedLostChip } from '../FeedLostChip';
  import { formatElapsed } from '../feedLostChip';

  describe('formatElapsed', () => {
    it('formats mm:ss since the given timestamp', () => {
      const base = Date.parse('2026-07-11T00:00:00Z');
      expect(formatElapsed('2026-07-11T00:00:00Z', base + 125_000)).toBe('02:05');
    });
    it('returns a dash when no timestamp is known', () => {
      expect(formatElapsed(null, Date.now())).toBe('—');
    });
  });

  describe('FeedLostChip', () => {
    it('renders nothing while the feed is healthy', () => {
      const { container } = render(<FeedLostChip feedLost={false} lastPlanAt={null} nowMs={Date.now()} />);
      expect(container).toBeEmptyDOMElement();
    });
    it('shows FEED LOST with the elapsed-since-last-plan when the feed is down', () => {
      const base = Date.parse('2026-07-11T00:00:00Z');
      render(<FeedLostChip feedLost lastPlanAt="2026-07-11T00:00:00Z" nowMs={base + 65_000} />);
      expect(screen.getByText(/FEED LOST/)).toBeInTheDocument();
      expect(screen.getByText(/01:05/)).toBeInTheDocument();
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/web && npx vitest run src/components/flows/__tests__/FeedLostChip.test.tsx
  ```
  Expected failure: cannot resolve `../FeedLostChip` / `../feedLostChip`.
- [ ] Create `visualizer/web/src/components/flows/feedLostChip.ts` (complete):
  ```ts
  // Elapsed mm:ss since an ISO timestamp; "—" when unknown.
  export function formatElapsed(fromIso: string | null, nowMs: number): string {
    if (!fromIso) return '—';
    const then = Date.parse(fromIso);
    if (Number.isNaN(then)) return '—';
    const secs = Math.max(0, Math.floor((nowMs - then) / 1000));
    const mm = String(Math.floor(secs / 60)).padStart(2, '0');
    const ss = String(secs % 60).padStart(2, '0');
    return `${mm}:${ss}`;
  }
  ```
- [ ] Create `visualizer/web/src/components/flows/FeedLostChip.tsx` (complete):
  ```tsx
  import { NOIR } from '../../theme/noir';
  import { formatElapsed } from './feedLostChip';

  interface Props {
    feedLost: boolean;
    lastPlanAt: string | null;
    nowMs: number;
  }

  // Mirrors the observatory SIGNAL LOST doctrine: when the daemon feed is dark the
  // tab stays on PG residue and shows how stale the last known plan is.
  export function FeedLostChip({ feedLost, lastPlanAt, nowMs }: Props) {
    if (!feedLost) return null;
    return (
      <div
        className="absolute top-4 right-4 px-3 py-1.5 rounded text-xs font-mono flex items-center gap-2"
        style={{ background: NOIR.panel, color: NOIR.bad, border: `1px solid ${NOIR.bad}` }}
        role="status"
      >
        <span className="inline-block w-2 h-2 rounded-full" style={{ background: NOIR.bad }} />
        FEED LOST · last plan {formatElapsed(lastPlanAt, nowMs)} ago
      </div>
    );
  }
  ```
- [ ] Write the failing detail-panel render test `visualizer/web/src/components/flows/__tests__/FlowDetailPanel.test.tsx`:
  ```tsx
  import { describe, it, expect } from 'vitest';
  import { render, screen } from '@testing-library/react';
  import { FlowDetailPanel } from '../FlowDetailPanel';
  import { mockLiveFlows } from '../../../mocks/mockFlows';

  const tour = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')).flows.find((f) => f.program === 'tour')!;

  describe('FlowDetailPanel', () => {
    it('renders nothing when no flow is selected', () => {
      const { container } = render(<FlowDetailPanel flow={null} />);
      expect(container).toBeEmptyDOMElement();
    });

    it('renders program, ship, tour id, current leg, cargo, hops+tranches and P&L', () => {
      render(<FlowDetailPanel flow={tour} />);
      expect(screen.getByText(/tour/i)).toBeInTheDocument();
      expect(screen.getByText('TORWIND-19')).toBeInTheDocument();
      expect(screen.getByText(/X1-NK36-FE8A/)).toBeInTheDocument(); // current leg from
      expect(screen.getByText(/X1-KA42-D39/)).toBeInTheDocument();  // current leg to / hop
      expect(screen.getByText(/FABRICS/)).toBeInTheDocument();       // cargo good
      expect(screen.getByText(/ADVANCED_CIRCUITRY/)).toBeInTheDocument(); // hop tranche good
      expect(screen.getByText(/312,?000/)).toBeInTheDocument();      // projected profit
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/web && npx vitest run src/components/flows/__tests__/FlowDetailPanel.test.tsx
  ```
  Expected failure: cannot resolve `../FlowDetailPanel`.
- [ ] Create `visualizer/web/src/components/flows/FlowDetailPanel.tsx` (complete):
  ```tsx
  import type { LiveFlow } from '../../types/flows';
  import { NOIR } from '../../theme/noir';

  const money = (n: number) => n.toLocaleString('en-US');
  const eta = (iso: string) => {
    const ms = Date.parse(iso) - Date.now();
    if (Number.isNaN(ms)) return '—';
    if (ms <= 0) return 'arrived';
    const secs = Math.floor(ms / 1000);
    return `${String(Math.floor(secs / 60)).padStart(2, '0')}:${String(secs % 60).padStart(2, '0')}`;
  };

  interface Props {
    flow: LiveFlow | null;
  }

  // Glass side panel: program, tour id, current leg + ETA, cargo aboard, remaining
  // hops with tranches, projected P&L. Renders only from what the flow actually
  // carries (no intent invented for feed-lost hulls — those are never selected).
  export function FlowDetailPanel({ flow }: Props) {
    if (!flow) return null;
    return (
      <div
        className="absolute top-4 left-4 w-80 max-h-[80vh] overflow-auto rounded-lg p-4 text-sm backdrop-blur"
        style={{ background: `${NOIR.panel}E6`, color: NOIR.ink, border: `1px solid ${NOIR.nebulaCore}` }}
      >
        <div className="flex items-center justify-between mb-2">
          <span className="uppercase tracking-wide text-xs" style={{ color: NOIR.accent }}>{flow.program}</span>
          <span className="font-mono" style={{ color: NOIR.ink }}>{flow.ship}</span>
        </div>
        {flow.tourId && (
          <div className="text-xs mb-2 font-mono truncate" style={{ color: NOIR.dim }}>{flow.tourId}</div>
        )}

        {flow.currentLeg && (
          <div className="mb-3">
            <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Current leg</div>
            <div className="font-mono text-xs">
              {flow.currentLeg.from} → {flow.currentLeg.to}
            </div>
            <div className="text-xs" style={{ color: NOIR.warn }}>ETA {eta(flow.currentLeg.arrivesAt)}</div>
          </div>
        )}

        {flow.cargo.length > 0 && (
          <div className="mb-3">
            <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Cargo</div>
            {flow.cargo.map((c, i) => (
              <div key={i} className="flex justify-between text-xs font-mono">
                <span>{c.good}</span>
                <span style={{ color: NOIR.dim }}>{c.units}</span>
              </div>
            ))}
          </div>
        )}

        {flow.remainingHops.length > 0 && (
          <div className="mb-3">
            <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Remaining hops</div>
            {flow.remainingHops.map((hop, i) => (
              <div key={i} className="mb-1">
                <div className="font-mono text-xs" style={{ color: NOIR.accentSoft }}>{hop.waypoint}</div>
                {hop.tranches.map((tr, j) => (
                  <div key={j} className="flex justify-between text-xs font-mono pl-2">
                    <span>{tr.isBuy ? 'buy' : 'sell'} {tr.good}</span>
                    <span style={{ color: NOIR.dim }}>{tr.units} @ {money(tr.expectedUnitPrice)}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        )}

        {flow.projected && (
          <div className="pt-2 border-t" style={{ borderColor: NOIR.nebulaCore }}>
            <div className="flex justify-between text-xs">
              <span style={{ color: NOIR.muted }}>Projected profit</span>
              <span style={{ color: flow.projected.profit >= 0 ? NOIR.good : NOIR.bad }}>{money(flow.projected.profit)}</span>
            </div>
            <div className="flex justify-between text-xs">
              <span style={{ color: NOIR.muted }}>Rate / hr</span>
              <span style={{ color: NOIR.dim }}>{money(flow.projected.ratePerHour)}</span>
            </div>
          </div>
        )}
      </div>
    );
  }
  ```
- [ ] Create `visualizer/web/src/components/flows/SystemDrilldown.tsx` (complete — waypoint-scale view of one system's realized lanes + resident flows, reusing the same visual grammar in a panel form):
  ```tsx
  import type { LaneRecord, LiveFlow } from '../../types/flows';
  import { NOIR } from '../../theme/noir';
  import { systemOf } from './flowGeometry';

  interface Props {
    systemSymbol: string;
    lanes: LaneRecord[];
    flows: LiveFlow[];
    onClose: () => void;
  }

  const money = (n: number) => n.toLocaleString('en-US');

  // Drill-down: this system's local realized lanes (either endpoint in-system) and
  // the flows currently resident/inbound, same grammar as the galaxy detail panel.
  export function SystemDrilldown({ systemSymbol, lanes, flows, onClose }: Props) {
    const localLanes = lanes.filter((l) => systemOf(l.from) === systemSymbol || systemOf(l.to) === systemSymbol);
    const localFlows = flows.filter(
      (f) => f.shipNav?.systemSymbol === systemSymbol ||
        (f.currentLeg && (systemOf(f.currentLeg.from) === systemSymbol || systemOf(f.currentLeg.to) === systemSymbol)),
    );
    return (
      <div
        className="absolute inset-y-4 right-4 w-96 overflow-auto rounded-lg p-4 text-sm backdrop-blur"
        style={{ background: `${NOIR.panel}E6`, color: NOIR.ink, border: `1px solid ${NOIR.nebulaCore}` }}
      >
        <div className="flex items-center justify-between mb-3">
          <span className="font-mono" style={{ color: NOIR.accent }}>{systemSymbol}</span>
          <button onClick={onClose} className="text-xs px-2 py-1 rounded" style={{ color: NOIR.muted, border: `1px solid ${NOIR.dim}` }}>
            close
          </button>
        </div>

        <div className="text-xs mb-1" style={{ color: NOIR.muted }}>Local realized lanes</div>
        {localLanes.length === 0 && <div className="text-xs" style={{ color: NOIR.dim }}>none in window</div>}
        {localLanes.map((l, i) => (
          <div key={i} className="flex justify-between text-xs font-mono mb-0.5">
            <span>{l.from} → {l.to}</span>
            <span style={{ color: l.realizedProfit >= 0 ? NOIR.good : NOIR.bad }}>{money(l.realizedProfit)}</span>
          </div>
        ))}

        <div className="text-xs mt-3 mb-1" style={{ color: NOIR.muted }}>Flows here</div>
        {localFlows.length === 0 && <div className="text-xs" style={{ color: NOIR.dim }}>none</div>}
        {localFlows.map((f) => (
          <div key={f.containerId} className="flex justify-between text-xs font-mono mb-0.5">
            <span>{f.ship}</span>
            <span style={{ color: NOIR.dim }}>{f.program}</span>
          </div>
        ))}
      </div>
    );
  }
  ```
- [ ] Run both render tests to green:
  ```bash
  cd visualizer/web && npx vitest run src/components/flows/__tests__/FeedLostChip.test.tsx src/components/flows/__tests__/FlowDetailPanel.test.tsx
  ```
  Expected: all passing.
- [ ] Commit:
  ```bash
  cd visualizer/web && git add src/components/flows/FeedLostChip.tsx src/components/flows/feedLostChip.ts src/components/flows/FlowDetailPanel.tsx src/components/flows/SystemDrilldown.tsx src/components/flows/__tests__/FeedLostChip.test.tsx src/components/flows/__tests__/FlowDetailPanel.test.tsx && git commit --no-verify -m "feat(flows-web): detail panel, drilldown, FEED LOST chip (Task 8)"
  ```

### Task 9 — Web: page + route + Navigation link + demo-mode dispatch

Wire the tab together: the `TradeFlowsView` page composes the scene + panels + chip + window switch and starts polling; `App.tsx` adds the route; `Navigation.tsx` adds the third link; and `mockClient.ts` gains a `/flows/*` branch so demo mode (`VITE_USE_MOCK_API=true`) drives the whole tab fleet-stopped — including the recurring feed-loss drill (reusing `isSignalLossWindow`).

**Files:**
- Create: `visualizer/web/src/pages/TradeFlowsView.tsx`
- Modify: `visualizer/web/src/App.tsx`
- Modify: `visualizer/web/src/components/Navigation.tsx`
- Modify: `visualizer/web/src/services/api/mockClient.ts`
- Create: `visualizer/web/src/services/api/__tests__/mockFlowsDispatch.test.ts`

**Interfaces:**
- Produces: `TradeFlowsView` (route element at `/trade-flows`); a `demoFlowsRequest(path, searchParams, nowMs)` branch in `mockClient` returning Task-1 wire shapes from `mockFlows`.
- Consumes: `useFlowsPolling`, `useFlowStore`, `FlowGalaxyScene`, `FlowDetailPanel`, `SystemDrilldown`, `FeedLostChip`; `mockFlows` fixtures + `isSignalLossWindow` from `mocks/demoEvents`.

**Steps:**

- [ ] Write the failing demo-dispatch test `visualizer/web/src/services/api/__tests__/mockFlowsDispatch.test.ts`:
  ```ts
  import { describe, it, expect } from 'vitest';
  import { mockRequest } from '../mockClient';
  import type { LiveFlowsResponse, TopologyResponse, LanesResponse } from '../../../types/flows';

  describe('mockClient /flows dispatch', () => {
    it('serves topology with systems + edges', async () => {
      const res = await mockRequest<TopologyResponse>('/flows/topology');
      expect(res.systems.length).toBeGreaterThan(0);
      expect(res.edges.length).toBeGreaterThan(0);
    });

    it('serves lanes for the requested window', async () => {
      const res = await mockRequest<LanesResponse>('/flows/lanes?window=24h');
      expect(res.window).toBe('24h');
      expect(res.lanes.length).toBeGreaterThan(0);
    });

    it('serves live flows OR a feed-loss envelope (never throws)', async () => {
      const res = await mockRequest<LiveFlowsResponse>('/flows/live');
      expect(typeof res.feedLost).toBe('boolean');
      if (res.feedLost) expect(res.flows).toEqual([]);
      else expect(res.flows.length).toBe(3);
    });
  });
  ```
- [ ] Run to see it fail:
  ```bash
  cd visualizer/web && npx vitest run src/services/api/__tests__/mockFlowsDispatch.test.ts
  ```
  Expected failure: `Mock API does not handle GET /flows/topology`.
- [ ] Modify `visualizer/web/src/services/api/mockClient.ts`: add the import and a `/flows/*` branch. Near the top imports add:
  ```ts
  import { mockTopology, mockLanes, mockLiveFlows, mockFeedLostResponse } from '../../mocks/mockFlows';
  import type { FlowWindow } from '../../types/flows';
  ```
  Add this helper above `mockRequest` (beside `demoBotRequest`):
  ```ts
  // Demo-mode /flows/* namespace (Trade Flows tab). Deterministic from the caller
  // clock so a fleet-stopped demo still interpolates. During the recurring
  // signal-loss drill the LIVE feed goes dark (feedLost envelope) while topology
  // and lanes — which are PG-backed in production and survive daemon loss — keep
  // serving, so the tab degrades exactly as designed.
  function demoFlowsRequest<T>(path: string, searchParams: URLSearchParams, nowMs: number): T {
    if (path === '/flows/topology') return mockTopology as T;
    if (path === '/flows/lanes') {
      const w = (searchParams.get('window') as FlowWindow) || '6h';
      const window: FlowWindow = w === '1h' || w === '6h' || w === '24h' ? w : '6h';
      return mockLanes(window) as T;
    }
    if (path === '/flows/live') {
      return (isSignalLossWindow(nowMs) ? mockFeedLostResponse(nowMs) : mockLiveFlows(nowMs)) as T;
    }
    return {} as T;
  }
  ```
  Then, in `mockRequest`, add this branch immediately before the `/bot/` branch (`if (path.startsWith('/bot/'))`):
  ```ts
  // Trade Flows namespace (demo — see demoFlowsRequest).
  if (path.startsWith('/flows/')) {
    return demoFlowsRequest<T>(path, url.searchParams, Date.now());
  }
  ```
- [ ] Run the dispatch test to green:
  ```bash
  cd visualizer/web && npx vitest run src/services/api/__tests__/mockFlowsDispatch.test.ts
  ```
  Expected: 3 passing.
- [ ] Create `visualizer/web/src/pages/TradeFlowsView.tsx` (complete):
  ```tsx
  import { useEffect, useState } from 'react';
  import { useFlowStore } from '../store/flowStore';
  import { useFlowsPolling } from '../hooks/useFlowsPolling';
  import FlowGalaxyScene from '../components/flows/FlowGalaxyScene';
  import { FlowDetailPanel } from '../components/flows/FlowDetailPanel';
  import { SystemDrilldown } from '../components/flows/SystemDrilldown';
  import { FeedLostChip } from '../components/flows/FeedLostChip';
  import { NOIR } from '../theme/noir';
  import type { FlowWindow } from '../types/flows';

  const WINDOWS: FlowWindow[] = ['1h', '6h', '24h'];

  export function TradeFlowsView() {
    useFlowsPolling();
    const window = useFlowStore((s) => s.window);
    const setWindow = useFlowStore((s) => s.setWindow);
    const live = useFlowStore((s) => s.live);
    const lanes = useFlowStore((s) => s.lanes);
    const lastPlanAt = useFlowStore((s) => s.lastPlanAt);
    const selectedFlowId = useFlowStore((s) => s.selectedFlowId);
    const drilldownSystem = useFlowStore((s) => s.drilldownSystem);
    const closeDrilldown = useFlowStore((s) => s.closeDrilldown);
    const error = useFlowStore((s) => s.error);

    const [nowMs, setNowMs] = useState(() => Date.now());
    useEffect(() => {
      const id = setInterval(() => setNowMs(Date.now()), 1000);
      return () => clearInterval(id);
    }, []);

    const flows = live?.flows ?? [];
    const selectedFlow = flows.find((f) => f.containerId === selectedFlowId) ?? null;

    return (
      <div className="relative w-full h-full" style={{ background: NOIR.bg0 }}>
        <FlowGalaxyScene />

        {/* Window switch */}
        <div className="absolute bottom-4 left-4 flex gap-1 rounded p-1" style={{ background: NOIR.panel }}>
          {WINDOWS.map((w) => (
            <button
              key={w}
              onClick={() => setWindow(w)}
              className="px-3 py-1 text-xs rounded"
              style={{
                background: window === w ? NOIR.accent : 'transparent',
                color: window === w ? NOIR.bg0 : NOIR.muted,
              }}
            >
              {w}
            </button>
          ))}
        </div>

        <FlowDetailPanel flow={selectedFlow} />
        {drilldownSystem && (
          <SystemDrilldown
            systemSymbol={drilldownSystem}
            lanes={lanes?.lanes ?? []}
            flows={flows}
            onClose={closeDrilldown}
          />
        )}
        <FeedLostChip feedLost={live?.feedLost ?? false} lastPlanAt={lastPlanAt} nowMs={nowMs} />

        {error && (
          <div className="absolute bottom-4 right-4 px-3 py-1.5 rounded text-xs" style={{ background: NOIR.panel, color: NOIR.bad }}>
            {error}
          </div>
        )}
      </div>
    );
  }
  ```
- [ ] Modify `visualizer/web/src/App.tsx`: import the page and add the route.
  ```tsx
  import { TradeFlowsView } from './pages/TradeFlowsView';
  // ...
  <Route path="/trade-flows" element={<TradeFlowsView />} />
  ```
- [ ] Modify `visualizer/web/src/components/Navigation.tsx`: add a third `Link` after the `/financial` link, following the exact active-class idiom:
  ```tsx
  <Link
    to="/trade-flows"
    className={`px-4 py-2 rounded transition-colors ${
      location.pathname === '/trade-flows'
        ? 'bg-blue-600 text-white'
        : 'text-gray-300 hover:bg-gray-700'
    }`}
  >
    Trade Flows
  </Link>
  ```
- [ ] Full web suite + typecheck (nothing regressed, new tests green):
  ```bash
  cd visualizer/web && npx vitest run && npx tsc --noEmit
  ```
  Expected: all tests pass, no TS errors.
- [ ] Commit:
  ```bash
  cd visualizer/web && git add src/pages/TradeFlowsView.tsx src/App.tsx src/components/Navigation.tsx src/services/api/mockClient.ts src/services/api/__tests__/mockFlowsDispatch.test.ts && git commit --no-verify -m "feat(flows-web): TradeFlowsView page + route + nav + demo dispatch (Task 9)"
  ```

### Task 10 — On-screen acceptance (S1 nebula lesson: backing-store checks are not enough)

> **The S1 nebula lesson.** A previous visualizer feature passed every prop/backing-store assertion yet rendered invisibly on screen. This task therefore requires BOTH (a) rendered-layout assertions against the real viewport AND (b) a human-legible screenshot read of the running tab in demo mode, fleet-stopped. The task is NOT complete until a screenshot shows system nodes, gate lanes, and hull glyphs actually painted, plus the detail panel and (during a drill window) the FEED LOST chip.

**Files:**
- Create: `visualizer/web/src/pages/__tests__/TradeFlowsView.layout.test.tsx`
- (No source changes — this task verifies Tasks 1-9. Any failure here sends you back to the offending task, not to a patch here.)

**Interfaces:**
- Consumes: the full tab in demo mode (`VITE_USE_MOCK_API=true`), the `/api/flows/*` fixtures, and a browser driver (claude-in-chrome or the repo's `run` skill) for the screenshot read.

**Steps:**

- [ ] Write the rendered-layout test `visualizer/web/src/pages/__tests__/TradeFlowsView.layout.test.tsx`. It renders the real page (demo dispatch supplies data) and asserts the DOM chrome occupies the viewport and the panels mount from live fixture data. (Konva canvas content is verified by the screenshot step below — jsdom has no canvas — so this test covers the HTML overlay layer + that a flow is selectable.)
  ```tsx
  import { describe, it, expect, beforeEach, vi } from 'vitest';
  import { render, screen, waitFor, act } from '@testing-library/react';
  import { MemoryRouter } from 'react-router-dom';
  import { TradeFlowsView } from '../TradeFlowsView';
  import { useFlowStore } from '../../store/flowStore';
  import { mockTopology, mockLanes, mockLiveFlows } from '../../mocks/mockFlows';

  // Seed the store directly (bypass the network/poll) so layout is deterministic.
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
    vi.spyOn(useFlowStore.getState(), 'setError');
  });

  function seed() {
    const s = useFlowStore.getState();
    s.setTopology(mockTopology);
    s.setLanes(mockLanes('6h'));
    s.setLive(mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')));
  }

  describe('TradeFlowsView layout (demo, fleet-stopped)', () => {
    it('renders the window switch with the three windows', async () => {
      render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
      act(() => seed());
      for (const w of ['1h', '6h', '24h']) {
        expect(screen.getByRole('button', { name: w })).toBeInTheDocument();
      }
    });

    it('shows the detail panel when a flow is selected', async () => {
      render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
      act(() => {
        seed();
        useFlowStore.getState().selectFlow('tour-run-TORWIND-19-086680f9');
      });
      await waitFor(() => expect(screen.getByText('TORWIND-19')).toBeInTheDocument());
      expect(screen.getByText(/ADVANCED_CIRCUITRY/)).toBeInTheDocument();
    });

    it('shows the FEED LOST chip when the live feed reports feedLost', async () => {
      render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
      act(() => {
        useFlowStore.getState().setTopology(mockTopology);
        useFlowStore.getState().setLive({ flows: [], generatedAt: new Date().toISOString(), feedLost: true, lastPlanAt: '2026-07-11T00:00:00Z' });
      });
      await waitFor(() => expect(screen.getByText(/FEED LOST/)).toBeInTheDocument());
    });
  });
  ```
- [ ] Run the layout test to green:
  ```bash
  cd visualizer/web && npx vitest run src/pages/__tests__/TradeFlowsView.layout.test.tsx
  ```
  Expected: 3 passing. (If the detail panel or chip fails to appear, the defect is in Task 8/9 — fix there.)
- [ ] Start the demo stack (fleet-stopped — demo mode has no real ships moving):
  ```bash
  cd visualizer/web && VITE_USE_MOCK_API=true npm run dev
  ```
  Note the dev URL (default `http://localhost:5173`).
- [ ] Drive a browser to `http://localhost:5173/trade-flows` (claude-in-chrome or the repo `run` skill). Perform the **on-screen** checks:
  - [ ] Assert the `Navigation` bar shows the active "Trade Flows" tab, and the Konva `<canvas>` element's bounding box fills the viewport width and (viewport height − nav). Use `read_page`/DOM measurement for the canvas + overlay bounding boxes (this is the "rendered-layout vs viewport" half).
  - [ ] Take a screenshot and READ it: confirm the galaxy shows discrete system nodes, hairline gate lanes between them (one dashed = under-construction edge), profit-colored realized lanes, and three hull glyphs (star/blue/green = tour/trade-route/arb) sitting mid-leg. This is the half that backing-store checks cannot substitute for (S1 nebula lesson).
  - [ ] Click a hull glyph; screenshot-confirm the glass `FlowDetailPanel` appears with program, ship, current leg + ETA, cargo, remaining hops with tranches, and projected P&L.
  - [ ] Click a system node; screenshot-confirm the `SystemDrilldown` panel opens with that system's local lanes/flows, then close it.
  - [ ] Wait for (or, to avoid a ~3-min wait, temporarily set `DEMO_SIGNAL_LOSS`-driven `isSignalLossWindow` true) the feed-loss drill; screenshot-confirm the `FEED LOST · last plan mm:ss ago` chip appears AND the dashed intent paths disappear while system nodes + realized lanes remain (graceful degradation, no fabricated intent).
  - [ ] Save the two key screenshots (fleet map + feed-lost state) to the job scratch dir and reference them in the completion report.
- [ ] Stop the dev server. Final full-suite gate across both packages:
  ```bash
  cd visualizer/web && npx vitest run && npx tsc --noEmit
  cd visualizer/server && npx vitest run && npx tsc --noEmit
  ```
  Expected: all green, no TS errors.
- [ ] Commit the acceptance test:
  ```bash
  cd visualizer/web && git add src/pages/__tests__/TradeFlowsView.layout.test.tsx && git commit --no-verify -m "test(flows-web): on-screen acceptance — layout assertions + screenshot read (Task 10)"
  ```

## Spec coverage map

Every design-doc requirement maps to a task; use this to self-audit before calling the build done.

| Spec §  | Requirement | Task(s) |
|---|---|---|
| Experience: route `/trade-flows`, `TradeFlowsView`, third nav entry | Page + route + nav link | 9 |
| Experience: galaxy scene, gate lanes as nodes/edges, Noir tokens | Topology endpoint + `FlowGalaxyScene` + gate-edge lines | 2, 7 |
| Experience: active hulls interpolated from leg timestamps (observatory math) | `projectFlowShip` clamp + `FlowShipLayer` | 6, 7 |
| Experience: intent-ahead dashed dimming path + per-hop markers (daemon-only) | `planPathPoints` + `FlowPlanPath` | 6, 7 |
| Experience: residue-behind realized legs; lane brightness/thickness by realized profit; 1h/6h/24h switch | Lanes endpoint + `FlowLaneLayer` + window switch | 3, 7, 9 |
| Experience: drill-down to system waypoint view | `SystemDrilldown` + node click | 7, 8 |
| Experience: detail panel (program/tour/leg+ETA/cargo/hops+tranches/P&L) | `FlowDetailPanel` | 8 |
| §2 server: `/api/flows/live` proxy+nav join (5s), `/lanes` (30s), `/topology` (cached) | Three endpoints + poll cadences | 2, 3, 4, 5 |
| §2: browser never talks to daemon (single origin) | Server-only proxy; web calls `/api/flows/*` | 4, 5 |
| §3 web: store + polling hooks mirroring existing idiom | `flowStore` + `useFlowsPolling` | 5 |
| §3: demo mode drives full tab fleet-stopped incl. feed-loss sim | `mockFlows` + `demoFlowsRequest` + `isSignalLossWindow` | 1, 9 |
| Degradation: FEED LOST chip; intent never fabricated; PG-down error state | Feed-loss envelope + chip + 503 contract | 4, 8 |
| Testing: lane math (window edges, profit signs), interpolation clamps, panel render, chip behavior, demo dispatch | Pure-helper + render tests | 3, 6, 8, 9 |
| Testing: on-screen acceptance (layout + screenshot, S1 nebula) | Acceptance task | 10 |
| Delivery: separable from daemon; builds against a fixture of the feed | Fixture-first throughout; pinned wire contract | 1, 4 |

## Notes for the implementer

- **Wire-contract fidelity (Part 2 handshake).** The daemon `GET /api/flows` payload MUST serialize `DaemonFlow` exactly as typed in Task 1 (`containerId`, `program` ∈ `tour|trade-route|arb`, `ship`, `tourId`, `currentLeg{from,to,departedAt,arrivesAt}`, `cargo[]`, `remainingHops[].tranches[]`, `projected{profit,ratePerHour}`, `plannedAt`). Program strings map from `containers.command_type` (`tour_run`→`tour`, `trade_route`→`trade-route`, `arb_run`→`arb`) — do not invent new strings.
- **Deferred (documented, not dropped):** `transactions`-based per-lane attribution (no from/to geocoding) — Part 2 or a later pass. Restart-durable intent (plan-time telemetry inserts) is Phase 2 per the spec.
- **Two separate TS projects.** `server/` and `web/` do not share a package; the `LaneRecord`/flow interfaces are defined independently on each side. The JSON wire shape is the contract, and the tests on both sides pin it.
- **Server static mount rationale.** `routes/flows.ts` is mounted like `agents`/`systems` (not the lazy `bot` try/catch): the pg `Pool` constructs without connecting, and every endpoint degrades to 503 on DB failure, so a down DB is a 503 (spec contract) rather than a missing route.
