# Trade Flows "Living Nebula" Revamp — Design Spec

**Date:** 2026-07-24 · **Status:** Approved (brainstorm complete, all sections user-approved)
**Scope:** the `/trade-flows` page of the SpaceTraders visualizer (`visualizer/web` + no server changes)

## 1. Problem

The current Trade Flows page renders a near-empty canvas: the fleet runs 15+ tours across a
dozen-plus systems, but the default view (hardcoded `scale = 0.4`, centered on the topology
centroid — `FlowGalaxyScene.tsx`) shows ~4 faint dots on black. All information lives in the
side panels; the map itself communicates almost nothing, and there is no "see the whole
galaxy" action even though the wheel clamp (`MIN_ZOOM_GALAXY = 0.05`) mechanically allows it.

## 2. Goals (user-approved decisions)

| Decision | Choice |
|---|---|
| Page purpose | **Ambient mission control + drill-in** — a living map you leave open; every element is a doorway to detail |
| Scale target | **Design for hundreds** — 100–500 systems, 150 ships, thousands of fills/hr |
| Visual direction | **B · Living Nebula** — bloom-lit orbs, cargo as particle streams, nebula region fog, motion as the language |
| Revamp scope | **Full continuum** — galaxy → region → system is ONE scene; the separate drilldown screen is retired and becomes the deepest zoom band |
| Renderer | **PixiJS v8** (WebGL) mounted in the React page; React keeps all DOM chrome |

Non-goals: other pages (Map, Contract-Ops, Financial, Leaderboards) stay on their current
stack; no server/API changes in v1; no 3D camera.

## 3. Architecture

### 3.1 What is retired / ported / new

- **Retired:** `FlowGalaxyScene.tsx` (Konva), `SystemDrilldown.tsx` + `DrilldownScene.tsx`
  as separate screens, and their konva-specific layers (`FlowLaneLayer`, `FlowShipLayer`,
  `FreshnessLayer` as konva components).
- **Ported unchanged (pure, tested):** `flowGeometry.ts`, `flowMotion.ts`, `freshness.ts`,
  `profitRing.ts`, `drilldownGeometry.ts` (feeds the SYSTEM band), `feedLostElapsed.ts`,
  server-side `galaxyLayout.ts`. Their vitest suites keep passing untouched.
- **Kept as DOM (React):** `TourRoster`, `FlowDetailPanel`, `FlowTooltip`, `FillTicker`,
  `FeedLostChip`, `DrilldownHeader` (repurposed as the SYSTEM-band header), the
  time-range/layer-toggle bar, `Navigation`.
- **New:** `NebulaScene.tsx` (React component owning a PixiJS `Application`),
  `nebula/camera.ts`, `nebula/lod.ts`, `nebula/clusters.ts`, `nebula/layers/*` (pixi
  containers), `nebula/sceneData.ts` (snapshot adapter).

### 3.2 Scene structure

```
TradeFlowsView (React)
├── NebulaScene (div hosting Pixi Application)
│     Camera: world transform {x,y,scale}; tween-based flights
│     LOD:    zoom → GALAXY | REGION | SYSTEM (named bands, hysteresis)
│     Layers (pixi containers, band-gated, back→front):
│       starfield/nebula backdrop → region auras → lanes+particle streams
│       → system orbs → ships → labels → fx (fill ripples)
└── DOM chrome: TourRoster · FlowDetailPanel · FillTicker · Tooltip · toggles
```

**Boundary rule:** Pixi never touches React state. The scene exposes
`onHover(entity)`, `onSelect(entity)`, `focusTour(id)`, `fitGalaxy()` and consumes an
immutable `SceneData` snapshot per poll. Selection syncs two-way with the roster.

### 3.3 Camera & the zoom-out fix

- **Landing state = fit-to-galaxy.** On topology load, the camera computes the bounding
  box of all systems (+padding) and fits it. No hardcoded scale, ever.
- `F` key / Esc / "Home" button → animated fit-to-galaxy from anywhere.
- Wheel zoom is anchored at the pointer (as today); clamps derive from the fitted extent
  (min = fit-scale × 0.5, max = SYSTEM-band deep zoom), not fixed constants.
- Click system → animated camera flight into its SYSTEM band (the drilldown moment).
- Click roster tour → camera follows that ship (ports `focusFlowId` behavior).

## 4. The three zoom bands (user-approved content)

Bands cross-fade over a zoom range (hysteresis so a boundary never flickers).

### L0 · GALAXY (landing view)
- **Shown:** region clusters as glowing nebula blobs (size = system count, brightness =
  realized $/hr), broad aggregate "currents" between regions (width = money rate, particle
  drift = direction), home region ringed gold, top-3 current labels.
- **Hidden:** individual ships, individual lanes, waypoints.
- **Answers:** "is the empire alive; where is the money weather."

### L1 · REGION
- **Shown:** every system as a bloom orb (size/brightness = activity; gold ring = home;
  dark ember = dormant), real lanes with flowing cargo particles (density = volume, hue =
  margin health: cyan→violet gradient healthy, ember red = negative), ships as moving
  motes with heading, dormant topology edges as faint threads, hover cards on everything.
- **Hidden:** waypoints, per-market detail.
- **Answers:** "which systems and lanes carry the economy; where are my ships."

### L2 · SYSTEM (replaces the drilldown screen)
- **Shown:** orbital map of the focused system — waypoints on orbit rings
  (`drilldownGeometry`), market freshness as ring glow (stale = amber dashed), in-system
  ship legs with particle trails, gate site with build % badge, fills pulse at their
  market. Tour roster auto-filters to the focused system. Rest of galaxy dimmed at edges.
- **Answers:** everything the old drilldown did, without leaving the scene.

## 5. Data flow

- Same endpoints as today: topology, `/flows` live poll, lane aggregation. Poll cadence
  unchanged. Zero backend changes for v1.
- `sceneData.ts` converts poll results into an immutable snapshot the scene diffs against.
- L0 region clusters are computed **client-side** in `clusters.ts` from the gate graph —
  deterministic (seeded like `galaxyLayout`'s FNV approach), stable across polls; a
  cluster is a connected neighborhood of the gate graph sized for legibility (~4–10
  systems). No new API.

## 6. Performance budget (acceptance numbers)

- 60 fps at 500 systems / 150 ships / 5,000 lane particles on Apple Silicon.
- Band gating + viewport culling on every layer; `ParticleContainer` for streams;
  labels culled to top-N by importance per band; `devicePixelRatio` capped at 2.
- Dev-mode FPS overlay for measurement.

## 7. Failure modes

- **WebGL unavailable:** static fallback card in the canvas area; DOM panels (roster,
  ticker, detail) remain fully functional.
- **Feed lost:** existing `FeedLostChip` behavior unchanged.
- **Empty topology:** fit-galaxy no-ops gracefully; scene shows backdrop only.

## 8. Testing

- Ported pure modules keep their existing vitest suites.
- New pure tests: LOD band mapping + hysteresis; cluster determinism (same topology →
  same clusters); camera fit math (bounds → transform); particle budget allocation.
- Scene: mount smoke test (Pixi app boots, layers registered, unmount leak-free).
- **On-screen acceptance (project scar tissue — the invisible-nebula incident):**
  headless-Chrome screenshots at each band against the live dev server, verified for
  actual visible content (non-black pixel budget + expected label text), not just
  store state.

## 9. Delivery notes

- The visualizer directory is untracked by the spacetraders repo (no own git); work
  happens in place against the running Vite dev server (`:5173`) — verify on screen.
- Konva remains a dependency (other pages); PixiJS v8 is added to `web/package.json`.
- Implementation is decomposed and delegated to subagents per the standing crew model;
  the pure-math ports and the scene build are separable lanes.
