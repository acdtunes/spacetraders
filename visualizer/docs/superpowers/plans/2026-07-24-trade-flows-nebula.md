# Trade Flows "Living Nebula" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Trade Flows Konva scene + separate drilldown with one PixiJS v8 "Living Nebula" continuum (galaxy → region → system semantic zoom), landing on a fit-to-galaxy view.

**Architecture:** React page keeps all DOM chrome; a new `NebulaScene` component owns a PixiJS v8 Application. Pure modules (`lod`, `camera`, `clusters`, `sceneData`) carry the logic and are fully unit-tested; pixi layers consume them and are verified by on-screen headless-Chrome acceptance. Existing pure math (`flowGeometry`, `flowMotion`, `freshness`, `drilldownGeometry`) is reused, not rewritten.

**Tech Stack:** React 18, TypeScript, Vite, vitest (jsdom), PixiJS v8 (`pixi.js@^8`), existing Express server untouched.

**Spec:** `docs/superpowers/specs/2026-07-24-trade-flows-nebula-design.md` (all decisions there are binding).

## Global Constraints

- Working directory for ALL tasks: `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/visualizer` (called `$VIZ` below). Frontend code lives in `$VIZ/web`.
- The visualizer is untracked by the parent spacetraders repo. Task 1 creates a STANDALONE git repo at `$VIZ`. NEVER `git add` visualizer files from the parent repo root; always commit inside `$VIZ`.
- Do not modify anything under `$VIZ/server/` (v1 is frontend-only) except nothing — zero server changes.
- Do not remove the `konva`/`react-konva` dependencies (other pages use them).
- New dependency allowed: `pixi.js@^8` only. No other new deps.
- Palette (exact): bg `#070312`, cyan `#22d3ee`, violet `#a78bfa`, magenta `#f0abfc`, gold `#e8d9a0`, ember `#ef4444`, dim slate `#475569`, label `#8b9cc0`.
- LOD thresholds (normalized `z = scale / fitScale`): REGION enters at `z ≥ 2.2`, exits back to GALAXY at `z < 1.8`; SYSTEM enters at `z ≥ 9.0`, exits back to REGION at `z < 7.5`.
- Camera clamps: `minScale = fitScale × 0.5`, `maxScale = fitScale × 40`.
- Performance budget: 60fps @ 500 systems / 150 ships / 5,000 particles; `devicePixelRatio` capped at 2.
- Tests: vitest, run from `$VIZ/web` with `npx vitest run <file>`. The dev server (`:5173` web, `:4000` server) is ALREADY RUNNING with HMR — do not restart it; screenshots hit the live page.
- Screenshot acceptance command (used by several tasks):
  `"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless --disable-gpu --screenshot=<out.png> --window-size=1920,1080 --virtual-time-budget=15000 "http://localhost:5173/trade-flows"`
  then READ the png and confirm the named content is actually visible (project scar tissue: a prior feature shipped invisible).

---

### Task 1: Standalone git repo + pixi.js dependency

**Files:**
- Create: `$VIZ/.gitignore`
- Modify: `$VIZ/web/package.json` (dependency add via npm)

**Interfaces:**
- Produces: a git repo at `$VIZ` (all later tasks commit into it); `pixi.js@^8` importable from `$VIZ/web`.

- [ ] **Step 1: Init the repo + baseline commit**

```bash
cd $VIZ
git init
printf 'node_modules/\ndist/\nbuild/\n.superpowers/\n*.tar.gz\n.DS_Store\n' > .gitignore
git add -A
git commit -m "chore: baseline — visualizer as standalone repo (pre-Nebula)"
```

Expected: `git log --oneline` shows exactly 1 commit; `git status --short` is empty.

- [ ] **Step 2: Install pixi**

```bash
cd $VIZ/web && npm install pixi.js@^8
```

- [ ] **Step 3: Write the import smoke test** — Create `$VIZ/web/src/nebula/__tests__/pixi.smoke.test.ts`:

```ts
import { describe, it, expect } from 'vitest';
import * as PIXI from 'pixi.js';

describe('pixi.js v8 availability', () => {
  it('exports Application and Container (v8 API)', () => {
    expect(typeof PIXI.Application).toBe('function');
    expect(typeof PIXI.Container).toBe('function');
    // v8 signature: init() is an instance method (v7 had constructor options only)
    expect(typeof PIXI.Application.prototype.init).toBe('function');
  });
});
```

- [ ] **Step 4: Run it** — `cd $VIZ/web && npx vitest run src/nebula/__tests__/pixi.smoke.test.ts` → PASS (3 assertions).

- [ ] **Step 5: Commit**

```bash
cd $VIZ && git add -A && git commit -m "feat: add pixi.js v8 + smoke test"
```

---

### Task 2: LOD bands with hysteresis (`lod.ts`)

**Files:**
- Create: `$VIZ/web/src/nebula/lod.ts`
- Test: `$VIZ/web/src/nebula/__tests__/lod.test.ts`

**Interfaces:**
- Produces: `type Band = 'GALAXY' | 'REGION' | 'SYSTEM'`; `bandFor(scale: number, fitScale: number, prev: Band | null): Band`; exported consts `REGION_ENTER=2.2, REGION_EXIT=1.8, SYSTEM_ENTER=9.0, SYSTEM_EXIT=7.5`.

- [ ] **Step 1: Write the failing test** — `lod.test.ts`:

```ts
import { describe, it, expect } from 'vitest';
import { bandFor } from '../lod';

describe('bandFor', () => {
  const fit = 0.1;
  it('maps a zoom sweep to GALAXY → REGION → SYSTEM', () => {
    expect(bandFor(0.1, fit, null)).toBe('GALAXY');      // z=1
    expect(bandFor(0.25, fit, null)).toBe('REGION');     // z=2.5
    expect(bandFor(1.0, fit, null)).toBe('SYSTEM');      // z=10
  });
  it('is sticky inside the hysteresis gap (no flicker)', () => {
    // z=2.0 sits between REGION_EXIT(1.8) and REGION_ENTER(2.2)
    expect(bandFor(0.2, fit, 'GALAXY')).toBe('GALAXY');  // was out → stays out
    expect(bandFor(0.2, fit, 'REGION')).toBe('REGION');  // was in  → stays in
    // z=8.0 sits between SYSTEM_EXIT(7.5) and SYSTEM_ENTER(9.0)
    expect(bandFor(0.8, fit, 'REGION')).toBe('REGION');
    expect(bandFor(0.8, fit, 'SYSTEM')).toBe('SYSTEM');
  });
  it('crosses thresholds decisively', () => {
    expect(bandFor(0.23, fit, 'GALAXY')).toBe('REGION'); // z=2.3 ≥ 2.2
    expect(bandFor(0.17, fit, 'REGION')).toBe('GALAXY'); // z=1.7 < 1.8
    expect(bandFor(0.95, fit, 'REGION')).toBe('SYSTEM'); // z=9.5 ≥ 9.0
    expect(bandFor(0.74, fit, 'SYSTEM')).toBe('REGION'); // z=7.4 < 7.5
  });
  it('guards degenerate fitScale', () => {
    expect(bandFor(1, 0, null)).toBe('GALAXY');
  });
});
```

- [ ] **Step 2: Run to verify FAIL** — `npx vitest run src/nebula/__tests__/lod.test.ts` → FAIL ("Cannot find module '../lod'").

- [ ] **Step 3: Implement** — `lod.ts`:

```ts
export type Band = 'GALAXY' | 'REGION' | 'SYSTEM';
export const REGION_ENTER = 2.2;
export const REGION_EXIT = 1.8;
export const SYSTEM_ENTER = 9.0;
export const SYSTEM_EXIT = 7.5;

// Hysteresis: entering a deeper band needs z ≥ *_ENTER; leaving it needs z < *_EXIT.
// Inside the gap the previous band wins, so a wheel hovering a boundary never flickers.
export function bandFor(scale: number, fitScale: number, prev: Band | null): Band {
  if (!isFinite(fitScale) || fitScale <= 0) return 'GALAXY';
  const z = scale / fitScale;
  if (prev === 'SYSTEM') return z < SYSTEM_EXIT ? (z < REGION_EXIT ? 'GALAXY' : 'REGION') : 'SYSTEM';
  if (prev === 'REGION') {
    if (z >= SYSTEM_ENTER) return 'SYSTEM';
    return z < REGION_EXIT ? 'GALAXY' : 'REGION';
  }
  // prev GALAXY or null → enter thresholds
  if (z >= SYSTEM_ENTER) return 'SYSTEM';
  if (z >= REGION_ENTER) return 'REGION';
  return 'GALAXY';
}
```

- [ ] **Step 4: Run to verify PASS.**
- [ ] **Step 5: Commit** — `cd $VIZ && git add -A && git commit -m "feat(nebula): LOD bands with hysteresis"`

---

### Task 3: Camera math (`camera.ts`)

**Files:**
- Create: `$VIZ/web/src/nebula/camera.ts`
- Test: `$VIZ/web/src/nebula/__tests__/camera.test.ts`

**Interfaces:**
- Produces: `interface CamXform { x: number; y: number; scale: number }`;
  `worldBounds(points: {x:number;y:number}[], pad?: number): {minX,minY,maxX,maxY}`;
  `fitTransform(bounds, viewportW: number, viewportH: number): CamXform` (centers bounds, scale = min(vw/w, vh/h));
  `clampScale(scale: number, fitScale: number): number` (min fit×0.5, max fit×40);
  `anchoredZoom(cam: CamXform, pointerX, pointerY, factor, fitScale): CamXform`;
  `lerpCam(a: CamXform, b: CamXform, t: number): CamXform` with `easeInOutCubic(t)`.

- [ ] **Step 1: Failing test** — `camera.test.ts`:

```ts
import { describe, it, expect } from 'vitest';
import { worldBounds, fitTransform, clampScale, anchoredZoom, lerpCam, easeInOutCubic } from '../camera';

describe('camera', () => {
  const pts = [{ x: -100, y: 0 }, { x: 300, y: 200 }];
  it('worldBounds pads the extent', () => {
    expect(worldBounds(pts, 50)).toEqual({ minX: -150, minY: -50, maxX: 350, maxY: 250 });
  });
  it('fitTransform fits and centers the bounds in the viewport', () => {
    const cam = fitTransform(worldBounds(pts, 0), 800, 400);
    // world w=400,h=200 → scale = min(800/400, 400/200) = 2; center (100,100) → screen center (400,200)
    expect(cam.scale).toBe(2);
    expect(cam.x).toBe(400 - 100 * 2);
    expect(cam.y).toBe(200 - 100 * 2);
  });
  it('clampScale bounds to [fit×0.5, fit×40]', () => {
    expect(clampScale(0.01, 0.1)).toBe(0.05);
    expect(clampScale(10, 0.1)).toBe(4);
    expect(clampScale(1, 0.1)).toBe(1);
  });
  it('anchoredZoom keeps the world point under the pointer fixed', () => {
    const cam = { x: 0, y: 0, scale: 1 };
    const out = anchoredZoom(cam, 100, 50, 2, 1); // zoom in ×2 at (100,50)
    // world point under pointer was (100,50); after: screen = world*2 + t ⇒ t = 100-200=-100, 50-100=-50
    expect(out).toEqual({ x: -100, y: -50, scale: 2 });
  });
  it('lerpCam eases between transforms', () => {
    const a = { x: 0, y: 0, scale: 1 }, b = { x: 100, y: 100, scale: 3 };
    expect(lerpCam(a, b, 0)).toEqual(a);
    expect(lerpCam(a, b, 1)).toEqual(b);
    const mid = lerpCam(a, b, 0.5);
    expect(mid.x).toBeCloseTo(50); expect(mid.scale).toBeCloseTo(2);
  });
  it('easeInOutCubic endpoints and midpoint', () => {
    expect(easeInOutCubic(0)).toBe(0); expect(easeInOutCubic(1)).toBe(1);
    expect(easeInOutCubic(0.5)).toBeCloseTo(0.5);
  });
});
```

- [ ] **Step 2: Run → FAIL** (module missing).
- [ ] **Step 3: Implement** — `camera.ts`:

```ts
export interface CamXform { x: number; y: number; scale: number }
export interface Bounds { minX: number; minY: number; maxX: number; maxY: number }

export function worldBounds(points: { x: number; y: number }[], pad = 0): Bounds {
  if (points.length === 0) return { minX: -1, minY: -1, maxX: 1, maxY: 1 };
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const p of points) {
    if (p.x < minX) minX = p.x; if (p.y < minY) minY = p.y;
    if (p.x > maxX) maxX = p.x; if (p.y > maxY) maxY = p.y;
  }
  return { minX: minX - pad, minY: minY - pad, maxX: maxX + pad, maxY: maxY + pad };
}

export function fitTransform(b: Bounds, viewportW: number, viewportH: number): CamXform {
  const w = Math.max(1e-6, b.maxX - b.minX), h = Math.max(1e-6, b.maxY - b.minY);
  const scale = Math.min(viewportW / w, viewportH / h);
  const cx = (b.minX + b.maxX) / 2, cy = (b.minY + b.maxY) / 2;
  return { x: viewportW / 2 - cx * scale, y: viewportH / 2 - cy * scale, scale };
}

export const MIN_FIT_RATIO = 0.5;
export const MAX_FIT_RATIO = 40;
export function clampScale(scale: number, fitScale: number): number {
  return Math.max(fitScale * MIN_FIT_RATIO, Math.min(fitScale * MAX_FIT_RATIO, scale));
}

export function anchoredZoom(cam: CamXform, px: number, py: number, factor: number, fitScale: number): CamXform {
  const wx = (px - cam.x) / cam.scale, wy = (py - cam.y) / cam.scale;
  const scale = clampScale(cam.scale * factor, fitScale);
  return { x: px - wx * scale, y: py - wy * scale, scale };
}

export function easeInOutCubic(t: number): number {
  return t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;
}

export function lerpCam(a: CamXform, b: CamXform, t: number): CamXform {
  const e = easeInOutCubic(Math.max(0, Math.min(1, t)));
  return { x: a.x + (b.x - a.x) * e, y: a.y + (b.y - a.y) * e, scale: a.scale + (b.scale - a.scale) * e };
}
```

- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): pure camera math (fit/clamp/anchored zoom/flight lerp)"`

---

### Task 4: Deterministic region clustering (`clusters.ts`)

**Files:**
- Create: `$VIZ/web/src/nebula/clusters.ts`
- Test: `$VIZ/web/src/nebula/__tests__/clusters.test.ts`

**Interfaces:**
- Consumes: topology shape `{ systems: { symbol: string; x: number; y: number }[]; edges: { from: string; to: string }[]; homeSystem: string | null }` (matches `web/src/types/flows.ts` — VERIFY the exact field names there and adapt imports, not semantics).
- Produces: `interface Cluster { id: string; members: string[]; cx: number; cy: number; isHome: boolean }`; `clustersFor(topology): Cluster[]` — deterministic, each cluster ≤ 8 members, `id` = lexicographically smallest member, `cx/cy` = member centroid.

- [ ] **Step 1: Failing test** — `clusters.test.ts`:

```ts
import { describe, it, expect } from 'vitest';
import { clustersFor } from '../clusters';

const topo = {
  homeSystem: 'S3',
  systems: [
    { symbol: 'S1', x: 0, y: 0 }, { symbol: 'S2', x: 10, y: 0 }, { symbol: 'S3', x: 20, y: 0 },
    { symbol: 'S4', x: 30, y: 0 }, { symbol: 'S5', x: 400, y: 400 }, // isolated
  ],
  edges: [
    { from: 'S1', to: 'S2' }, { from: 'S2', to: 'S3' }, { from: 'S3', to: 'S4' },
  ],
};

describe('clustersFor', () => {
  it('groups connected systems and isolates the unconnected', () => {
    const cs = clustersFor(topo as any);
    const byId = new Map(cs.map(c => [c.id, c]));
    expect(byId.get('S1')!.members).toEqual(['S1', 'S2', 'S3', 'S4']);
    expect(byId.get('S5')!.members).toEqual(['S5']);
  });
  it('is deterministic (same input → identical output)', () => {
    expect(clustersFor(topo as any)).toEqual(clustersFor(topo as any));
  });
  it('caps clusters at 8 members', () => {
    const big = {
      homeSystem: null,
      systems: Array.from({ length: 12 }, (_, i) => ({ symbol: `T${String(i).padStart(2, '0')}`, x: i, y: 0 })),
      edges: Array.from({ length: 11 }, (_, i) => ({ from: `T${String(i).padStart(2, '0')}`, to: `T${String(i + 1).padStart(2, '0')}` })),
    };
    const cs = clustersFor(big as any);
    expect(Math.max(...cs.map(c => c.members.length))).toBeLessThanOrEqual(8);
    expect(cs.reduce((n, c) => n + c.members.length, 0)).toBe(12);
  });
  it('marks the home cluster and computes centroids', () => {
    const cs = clustersFor(topo as any);
    const home = cs.find(c => c.isHome)!;
    expect(home.members).toContain('S3');
    expect(home.cx).toBeCloseTo((0 + 10 + 20 + 30) / 4);
  });
});
```

- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — `clusters.ts`:

```ts
export interface Cluster { id: string; members: string[]; cx: number; cy: number; isHome: boolean }
const MAX_CLUSTER = 8;

interface TopoLike {
  systems: { symbol: string; x: number; y: number }[];
  edges: { from: string; to: string }[];
  homeSystem: string | null;
}

// Deterministic greedy BFS over the gate graph: iterate symbols sorted, seed a
// cluster from each unassigned symbol, absorb sorted-neighbor frontier up to
// MAX_CLUSTER. Sorted iteration everywhere ⇒ same topology → same clusters.
export function clustersFor(topo: TopoLike): Cluster[] {
  const pos = new Map(topo.systems.map(s => [s.symbol, s]));
  const adj = new Map<string, string[]>();
  for (const s of topo.systems) adj.set(s.symbol, []);
  for (const e of topo.edges) {
    if (e.from === e.to || !adj.has(e.from) || !adj.has(e.to)) continue;
    adj.get(e.from)!.push(e.to); adj.get(e.to)!.push(e.from);
  }
  for (const [, ns] of adj) ns.sort();

  const assigned = new Set<string>();
  const clusters: Cluster[] = [];
  for (const seed of [...pos.keys()].sort()) {
    if (assigned.has(seed)) continue;
    const members: string[] = [];
    const queue = [seed];
    while (queue.length && members.length < MAX_CLUSTER) {
      const cur = queue.shift()!;
      if (assigned.has(cur)) continue;
      assigned.add(cur); members.push(cur);
      for (const n of adj.get(cur) ?? []) if (!assigned.has(n)) queue.push(n);
    }
    members.sort();
    const cx = members.reduce((s, m) => s + pos.get(m)!.x, 0) / members.length;
    const cy = members.reduce((s, m) => s + pos.get(m)!.y, 0) / members.length;
    clusters.push({ id: members[0], members, cx, cy, isHome: topo.homeSystem != null && members.includes(topo.homeSystem) });
  }
  return clusters;
}
```

- [ ] **Step 4: Run → PASS.** (If `types/flows.ts` field names differ — e.g. `gateEdges` — adapt the `TopoLike` mapping at the CALL SITE in Task 5, never the algorithm.)
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): deterministic region clustering"`

---

### Task 5: Scene snapshot adapter (`sceneData.ts`)

**Files:**
- Create: `$VIZ/web/src/nebula/sceneData.ts`
- Test: `$VIZ/web/src/nebula/__tests__/sceneData.test.ts` (fixtures from `$VIZ/web/src/mocks/mockFlows.ts`)

**Interfaces:**
- Consumes: the page's existing poll results — READ `web/src/pages/TradeFlowsView.tsx` (or wherever `/trade-flows` fetches live: topology + live flows + lanes; types in `web/src/types/flows.ts`) and `clustersFor` (Task 4).
- Produces:

```ts
export interface SceneSystem { symbol: string; x: number; y: number; activity: number; isHome: boolean; underConstruction: boolean }
export interface SceneLane { from: string; to: string; profitPerHr: number; volume: number; realized: number; projected: number }
export interface SceneShip { id: string; flowId: string; x: number; y: number; headingRad: number; system: string | null }
export interface SceneData {
  systems: SceneSystem[]; lanes: SceneLane[]; ships: SceneShip[];
  clusters: Cluster[]; homeSystem: string | null; fitPoints: { x: number; y: number }[];
}
export function buildSceneData(topology, lanes, live, nowMs: number): SceneData
```

  Ship positions come from the EXISTING `projectFlowMotion` in `web/src/components/flows/flowMotion.ts` — reuse it verbatim (it already computes x/y along a flow at a timestamp); do not reimplement interpolation.

- [ ] **Step 1: Failing test** — assert against `mockFlows.ts` fixtures: systems carry activity summed from lane realized profit; home flagged; `fitPoints` includes every system; ships resolve to finite coords; empty inputs → empty SceneData (no throw). Write the concrete test by importing the mock module and asserting counts/fields (exact numbers come from the fixture — read it first, then pin them in the test).
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — a pure mapping: index systems, sum per-system realized profit into `activity`, map lanes 1:1, call `projectFlowMotion(flow, adj, systemGates, systemPos, nowMs, 1)` per live flow for ship dots, `clustersFor(topology)` for clusters. No pixi imports here.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): SceneData snapshot adapter (reuses flowMotion)"`

---

### Task 6: NebulaScene mount shell + layer scaffold

**Files:**
- Create: `$VIZ/web/src/nebula/NebulaScene.tsx`, `$VIZ/web/src/nebula/layers/registry.ts`
- Test: `$VIZ/web/src/nebula/__tests__/NebulaScene.mount.test.tsx`

**Interfaces:**
- Produces: `<NebulaScene data={SceneData|null} onSelectSystem={(sym)=>void} onHover={(t: HoverTarget|null)=>void} apiRef={MutableRefObject<NebulaApi|null>} />` where `interface NebulaApi { fitGalaxy(): void; focusSystem(symbol: string): void; focusTour(flowId: string): void; band(): Band }`.
- Layer registry: `createLayers(stage: PIXI.Container): Layers` returning named containers in z-order: `backdrop, auras, currents, lanes, orbs, ships, labels, fx` — later tasks attach content to these EXACT names.

- [ ] **Step 1: Failing mount test** — mock `pixi.js` (vitest `vi.mock`) with a stub `Application` capturing `init/destroy` calls and a stub `Container`; assert: render mounts a canvas host div, `init` called once with `{ background: 0x070312, antialias: true, resolution: Math.min(devicePixelRatio, 2) }`-compatible options, all 8 layers registered in order, unmount calls `destroy(true)`, `apiRef.current` exposes the 4 NebulaApi methods.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — `NebulaScene.tsx`: `useEffect` → `const app = new Application(); await app.init({...}); hostDiv.appendChild(app.canvas); const layers = createLayers(app.stage);` store cam state `{x,y,scale}` in a ref; a `ticker` callback applies cam to a root world container each frame and advances flight tweens (`lerpCam` over 600ms). ResizeObserver refits viewport vars. `fitGalaxy()` = tween to `fitTransform(worldBounds(data.fitPoints, 60), w, h)`. On `data` change, stash snapshot in a ref for layer tasks. Guard all pixi calls behind `app.renderer != null` (WebGL-absent fallback: set a `failed` state → render `<div className="nebula-fallback">WebGL unavailable — panels remain live.</div>`).
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): scene mount shell, layer registry, camera loop, WebGL fallback"`

---

### Task 7: Backdrop layer — starfield + nebula fog (first visible pixels)

**Files:**
- Create: `$VIZ/web/src/nebula/layers/backdrop.ts`, `$VIZ/web/src/nebula/glowTexture.ts`
- Test: `$VIZ/web/src/nebula/__tests__/glowTexture.test.ts` + screenshot acceptance

**Interfaces:**
- Produces: `makeGlowTexture(renderer, radius: number, stops: [number,string][]): PIXI.Texture` (radial gradient canvas → texture; cached by key) — REUSED by Tasks 8/9/11 for every glow; `buildBackdrop(layers.backdrop, data: SceneData, renderer): void` — seeded starfield (FNV hash of system symbols, ~600 point sprites across the fitted bounds ×1.5) + one fog sprite per cluster (`makeGlowTexture` tinted violet/cyan alternating by cluster index, alpha 0.10–0.22 by activity share).
- Pure helper (unit-tested): `starSeeds(symbols: string[], count: number, bounds): {x,y,r,alpha}[]` — deterministic.

- [ ] **Step 1: Failing test for `starSeeds`** (deterministic, in-bounds, count respected) — concrete asserts on first 3 seeds for a fixed input.
- [ ] **Step 2: Run → FAIL.**  **Step 3: Implement** `starSeeds` (FNV-1a like `galaxyLayout.ts`) + the pixi builders. **Step 4: Run → PASS.**
- [ ] **Step 5: Wire into NebulaScene** (rebuild backdrop when SceneData identity changes).
- [ ] **Step 6: Screenshot acceptance** — take the Global-Constraints screenshot; READ it: the page must show a starfield + colored fog (NOT uniform black). If black → debug before proceeding (check init awaited, canvas appended, layer added to stage).
- [ ] **Step 7: Commit** — `git commit -m "feat(nebula): backdrop starfield + cluster fog (first pixels on screen)"`

---

### Task 8: GALAXY band — region auras, aggregate currents, labels

**Files:**
- Create: `$VIZ/web/src/nebula/layers/galaxyBand.ts`, `$VIZ/web/src/nebula/aggregate.ts`
- Test: `$VIZ/web/src/nebula/__tests__/aggregate.test.ts` + screenshot

**Interfaces:**
- Consumes: `SceneData.clusters`, `SceneData.lanes`, `makeGlowTexture`.
- Produces: `aggregateCurrents(clusters: Cluster[], lanes: SceneLane[]): { fromCluster: string; toCluster: string; profitPerHr: number; volume: number }[]` (pure — sums lanes whose endpoints land in different clusters; intra-cluster lanes excluded; symmetric pairs merged keyed `min|max`); `buildGalaxyBand(layers, data, renderer)` — aura sprite per cluster (radius ∝ √memberCount, alpha ∝ profit share, gold ring texture if `isHome`), current ribbons (`PIXI.Graphics` quadratic curve, width 2–14px by |profitPerHr| share, cyan→violet gradient approximated with two overlaid strokes, ember `0xef4444` when negative), drifting particles along each current (8–24 sprites advancing `t += dt·speed`, wrapped), top-3 currents labeled `±X.XM/hr` (`PIXI.Text`, `#8b9cc0`, monospace 11px).
- Visibility contract: containers built here are shown ONLY when `band() === 'GALAXY'` (cross-fade alpha over 250ms handled in the scene ticker; bind visibility to the band value, never to raw zoom).

- [ ] **Step 1: Failing `aggregateCurrents` test** — 2 clusters, 3 lanes (one intra, two inter incl. a reversed duplicate) → exactly 1 merged current with summed profit/volume; negative-profit lane keeps sign; deterministic order.
- [ ] **Step 2: FAIL → Step 3: implement pure fn + pixi builder → Step 4: PASS.**
- [ ] **Step 5: Screenshot at L0** (default landing = fit = GALAXY band): READ it — region blobs + at least one labeled current visible.
- [ ] **Step 6: Commit** — `git commit -m "feat(nebula): GALAXY band — auras, money currents, labels"`

---

### Task 9: REGION band — system orbs + dormant threads

**Files:**
- Create: `$VIZ/web/src/nebula/layers/orbs.ts`
- Test: `$VIZ/web/src/nebula/__tests__/orbSizing.test.ts` + screenshot

**Interfaces:**
- Consumes: `SceneData.systems`, `makeGlowTexture`.
- Produces: pure `orbRadius(activity: number, maxActivity: number): number` (4px floor, 18px cap, sqrt scaling — unit-tested with exact values: `orbRadius(0, 100)=4`, `orbRadius(100,100)=18`, `orbRadius(25,100)=Math.round((4+14*0.5)*10)/10=11`); `buildOrbs(layers.orbs, data, renderer)` — per system: glow sprite (white core + tinted halo; gold ring sprite when `isHome`; dim ember tint `0x475569` when activity=0), monospace label ≥ top-20 by activity (others hidden until SYSTEM band), dormant topology edges as 1px `0x475569` alpha-0.3 lines under the orbs, dashed (4px on/4 off) when `underConstruction`.
- Visibility: shown when band ∈ {REGION, SYSTEM} (SYSTEM dims non-focused to 25% alpha — the dimming itself lands in Task 11).

- [ ] Steps 1–4: TDD `orbRadius` (test above, FAIL → implement → PASS).
- [ ] **Step 5: Screenshot at REGION band** — zoom the live page via CDP is overkill: instead TEMPORARILY set the initial camera to `fit × 3` behind a `?band=region` URL param (add a dev-only query-param override in NebulaScene: `new URLSearchParams(location.search).get('band')` maps region→z 3, system→z 12 — KEEP this param; Tasks 11/14 reuse it), screenshot with `"http://localhost:5173/trade-flows?band=region"`, READ: orbs + labels + threads visible.
- [ ] **Step 6: Commit** — `git commit -m "feat(nebula): REGION band — bloom orbs, labels, dormant threads (+dev band param)"`

---

### Task 10: Lane particle streams + ship motes

**Files:**
- Create: `$VIZ/web/src/nebula/layers/streams.ts`
- Test: `$VIZ/web/src/nebula/__tests__/streamBudget.test.ts` + screenshot

**Interfaces:**
- Consumes: `SceneData.lanes`, `SceneData.ships`, existing `flowGeometry` polyline helpers.
- Produces: pure `particleBudget(lanes: SceneLane[], cap: number): Map<laneKey, number>` (proportional to volume, total ≤ cap=5000, every active lane ≥ 4 — exact test: two lanes volume 10/30 with cap 100 → 25/75); `buildStreams(layers.lanes, layers.ships, data)` — one `PIXI.ParticleContainer` for all lane particles (2px sprites, tint cyan `0x22d3ee` → violet by lane-progress, ember when lane profit < 0; advance along the lane polyline each tick, speed ∝ profit rate, wrap); ships as 3px motes with a 6px heading streak (`SceneShip.headingRad`), updated per SceneData poll and dead-reckoned between polls via the same velocity.
- Visibility: REGION + SYSTEM bands.

- [ ] Steps 1–4: TDD `particleBudget` (FAIL → implement → PASS).
- [ ] **Step 5: Screenshot** at `?band=region`: READ — visible particle chains along at least one lane and ship motes distinct from orbs.
- [ ] **Step 6: Commit** — `git commit -m "feat(nebula): lane particle streams + ship motes"`

---

### Task 11: SYSTEM band — the drilldown-in-place

**Files:**
- Create: `$VIZ/web/src/nebula/layers/systemBand.ts`
- Modify: `$VIZ/web/src/nebula/NebulaScene.tsx` (focus state + edge dimming)
- Test: reuse `drilldownGeometry` suites (must stay green) + screenshot

**Interfaces:**
- Consumes: `drilldownGeometry.ts` (orbit ring + waypoint placement — reuse its exported functions verbatim; read its test file for the exact API), `freshness.ts` (staleness → ring style), server `systems` route data already fetched by the old `SystemDrilldown` (find its fetch in `SystemDrilldown.tsx` and lift the SAME call into a `useSystemDetail(symbol)` hook — move, don't rewrite).
- Produces: `buildSystemBand(layers.fx, detail, focusSymbol)` — orbit rings (1px `0x1e293b`), waypoints as small orbs, market freshness ring: solid cyan ≤ fresh threshold, amber dashed when stale (thresholds from `freshness.ts`), in-system ship legs with 1px particle trails, gate-site badge (`PIXI.Text` `gate NN% · GOOD X/Y` gold on dark chip) fed from the construction fields the old drilldown displayed; non-focused galaxy dimmed to 25% via a scene-level `dimmer` alpha on all other layers.
- `focusSystem(symbol)`: camera flight (600ms) to the system at `z = 12 × fitScale`, sets focus state, triggers detail fetch; wheel-out past SYSTEM_EXIT clears focus and restores alpha. `onSelectSystem` fires so the page can filter `TourRoster`.

- [ ] **Step 1: Verify ported suites still pass** — `npx vitest run src/components/flows/__tests__/drilldownGeometry.test.ts` → PASS untouched.
- [ ] **Step 2: Implement** hook + layer + focus/dim logic.
- [ ] **Step 3: Screenshot** `"?band=system&focus=<home>"` (extend the dev param: `focus` picks the system; default home): READ — orbit rings, waypoints, at least one freshness ring, gate badge text visible.
- [ ] **Step 4: Commit** — `git commit -m "feat(nebula): SYSTEM band — in-place drilldown (orbits, freshness, gate badge)"`

---

### Task 12: Interactions — hover, select, follow, keys, wheel/drag

**Files:**
- Modify: `$VIZ/web/src/nebula/NebulaScene.tsx`, `$VIZ/web/src/nebula/layers/orbs.ts` (eventMode)
- Test: `$VIZ/web/src/nebula/__tests__/interactions.test.tsx` (mocked pixi) 

**Interfaces:**
- Consumes: NebulaApi; DOM chrome components (`FlowTooltip`, `FlowDetailPanel`) stay as-is.
- Produces: orbs/lanes/currents get `eventMode: 'static'` + pointer handlers → `onHover({kind:'system'|'lane'|'current', key, clientX, clientY})` / `onSelectSystem(symbol)`; wheel → `anchoredZoom` (factors 1.1 / 0.9); pointer drag pans (threshold 3px so clicks survive); keys: `F` and `Escape` → `fitGalaxy()` (Escape first clears focus if set); roster click → `focusTour(flowId)` = camera follows that ship's mote each tick until user wheels/drags (break-follow-on-input).

- [ ] **Step 1: Failing tests** (mocked pixi): `F` keydown calls the fit tween; drag > 3px suppresses the click select; wheel calls anchoredZoom with 1.1/0.9; focusTour then a wheel event stops following.
- [ ] **Step 2: FAIL → Step 3: implement → Step 4: PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): interactions — hover/select/follow, F/Esc, anchored wheel, drag pan"`

---

### Task 13: Page integration + retirement of the Konva scene

**Files:**
- Modify: the `/trade-flows` page component (find it: `grep -rn "FlowGalaxyScene" $VIZ/web/src --include='*.tsx'`) — swap in `NebulaScene`, wire `SceneData` build per poll (`buildSceneData`), tooltip/detail-panel/roster sync, layer toggles (Lanes/Paths/Ships/Freshness map to layer visibility flags passed as props), roster auto-filter on focus.
- Delete: `FlowGalaxyScene.tsx`, `SystemDrilldown.tsx`, `DrilldownScene.tsx`, `FlowLaneLayer.tsx`, `FlowShipLayer.tsx`, `FreshnessLayer.tsx`, `FlowPlanPath.tsx` and THEIR component tests (keep every pure-module file + test: `flowGeometry`, `flowMotion`, `freshness`, `profitRing`, `drilldownGeometry`, `feedLostElapsed`, `FillTicker`, `TourRoster`, `FlowDetailPanel`, `FlowTooltip`, `FeedLostChip`, `DrilldownHeader`).
- Test: full suite + screenshots.

- [ ] **Step 1: Swap + wire** (page renders NebulaScene; poll → `buildSceneData` → prop).
- [ ] **Step 2: Delete retired files**; fix imports until `npx tsc --noEmit` is clean.
- [ ] **Step 3: Full suite** — `cd $VIZ/web && npx vitest run` → ALL PASS (ported suites green, deleted component tests gone).
- [ ] **Step 4: Screenshots ×3** (default L0, `?band=region`, `?band=system`) — READ each; the L0 shot must show the ENTIRE galaxy fitted (multiple regions visible — this is the original bug's acceptance).
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): page integration; retire Konva scene + drilldown screens"`

---

### Task 14: Acceptance hardening — perf overlay, WebGL fallback, final sweep

**Files:**
- Create: `$VIZ/web/src/nebula/__tests__/fallback.test.tsx`; dev FPS overlay in `NebulaScene.tsx`
- Test: screenshots + suite + manual fps read

**Interfaces:**
- Produces: `?fps=1` URL param renders a DOM overlay (top-left, monospace) with rolling-average FPS from the pixi ticker; init-failure fallback card (Task 6's branch) proven by test.

- [ ] **Step 1: Failing fallback test** — mock pixi `init` to reject; assert the fallback div renders and NO crash propagates; DOM panels still mount.
- [ ] **Step 2: FAIL → implement overlay + verify fallback branch → PASS.**
- [ ] **Step 3: Perf read** — screenshot `"?fps=1&band=region"`; READ the FPS number; must be ≥ 55 with live data. If below: check ParticleContainer usage, label count, `resolution` cap.
- [ ] **Step 4: Full suite green; `npx tsc --noEmit` clean; `git status` clean after commit.**
- [ ] **Step 5: Commit** — `git commit -m "feat(nebula): fps overlay, WebGL fallback proof — acceptance complete"`

---

## Self-review notes (done at write time)

- **Spec coverage:** §3.1 retire/port/keep → Tasks 13/5/11; §3.3 camera+fix → Tasks 3/6/12/13(L0 acceptance); §4 bands → Tasks 8/9/10/11 + LOD Task 2; §5 data → Tasks 4/5; §6 perf → Global constraints + Task 14; §7 failure → Tasks 6/14 (FeedLostChip untouched by design); §8 testing → every task + screenshots. No gaps found.
- **Type consistency:** `Band`/`CamXform`/`SceneData`/`Cluster`/`NebulaApi` defined once (Tasks 2/3/5/4/6) and consumed by name everywhere later; layer names fixed in Task 6's registry.
- **Placeholders:** none; Task 5's test intentionally instructs reading the mock fixture to pin exact numbers (the fixture is the source of truth, not a guess).
