# Visualizer: Trade Flows Tab — Design

**Date:** 2026-07-10
**Status:** Approved (Admiral, via brainstorm)
**Scope:** `visualizer/` (new tab + server routes) and one read-only `gobot/` surface (daemon flow feed)

## Purpose

A new visualizer tab for **seeing the fleet's trade routes live**: the trading trio
(heavy multi-hop tours, trade-route circuits, arb runs) rendered spatially on a
galaxy-scale map — where each hull is on its leg, what it intends to do next, and
where the money is flowing. The spatial story Grafana cannot tell.

Decided via brainstorm:

| Decision | Choice |
|---|---|
| Primary job | **Live flow map** — active trade circulation now (not analytics, not history) |
| Spatial scope | **Galaxy + drill-down** — gate-network view default, click a system for waypoint detail |
| Flow scope (phase 1) | **Trading trio only**: tours, trade-route circuits, arb runs |
| Interaction model | **Interactive first** — pan/zoom/click investigation; wall-mode is not a phase-1 goal |
| Data truth | **Daemon-backed feed** — the optimizer's in-memory plans (future hops + tranches), not just PG residue |

## Experience

Third entry in the top `Navigation` → route `/trade-flows`, page `TradeFlowsView`.

- **Galaxy scene** (Deep Space Noir tokens): known systems as nodes, `gate_edges`
  as hairline lanes (220 edges today, 42 systems priced).
- **Active hulls** glide along their current leg — position interpolated per frame
  from nav route departure/arrival timestamps (same math as the observatory's
  `useShipInterpolation`).
- **Intent ahead**: each hull's remaining planned hops draw as a dimming dashed
  path with per-hop markers — this is what the daemon feed uniquely provides.
- **Residue behind**: realized legs from the last hour fade behind each hull.
- **Lane encoding**: brightness/thickness = realized profit volume through that
  lane in the selected window (1h / 6h / 24h switch).
- **Drill-down**: click a system → waypoint-scale view of that system's buys,
  sells, and local legs, same visual grammar (reuses the existing system-map
  component patterns, e.g. `TradeRouteLayer`'s arrow/label idiom).
- **Detail panel**: click a hull or lane → glass side panel: program type,
  tour id, current leg + ETA, cargo aboard, remaining hops with tranches
  (good / units / expected price), projected vs realized P&L for the run.

## Architecture

### 1. Daemon flow feed (the one Go surface — read-only)

- **In-process plan registry**: a small registry (keyed by container id) that
  trading executors publish plan snapshots to — at plan adoption and at each leg
  boundary. Tour runs publish hops + tranches + projections; trade-route circuits
  publish their circuit legs; arb runs publish their one-shot leg. Publishing is
  fire-and-forget state exposure; **no trading logic reads the registry, no guard
  interacts with it** (RULINGS #4 untouched — this surface can never gate or relax
  a buy).
- **HTTP handler**: `GET /api/flows` registered on the existing metrics HTTP mux
  (`internal/adapters/grpc/daemon_server.go`, beside the `/metrics` handler,
  localhost-bound via the existing `metrics:` config). Serializes the registry to
  JSON. Read-only; no auth change (same trust boundary as `/metrics`).
- **Payload shape** (per active flow):

```json
{
  "flows": [{
    "containerId": "tour-run-TORWIND-54-beba64e7",
    "program": "tour",
    "ship": "TORWIND-54",
    "tourId": "tour-run-TORWIND-54-beba64e7",
    "currentLeg": {"from": "X1-UU57-E21Z", "to": "X1-ZC66-C39A",
                    "departedAt": "…", "arrivesAt": "…"},
    "cargo": [{"good": "EQUIPMENT", "units": 200}],
    "remainingHops": [{"waypoint": "X1-ZC66-F12F",
                        "tranches": [{"good": "ADVANCED_CIRCUITRY", "isBuy": false,
                                       "units": 100, "expectedUnitPrice": 4100}]}],
    "projected": {"profit": 312000, "ratePerHour": 445000},
    "plannedAt": "…"
  }],
  "generatedAt": "…"
}
```

- Restart behavior: the registry is in-memory; after a daemon restart it
  repopulates as executors adopt plans. Durable intent is phase 2.

### 2. Visualizer server (`server/routes/flows.ts`)

| Endpoint | Source | Poll |
|---|---|---|
| `GET /api/flows/live` | proxies daemon `GET /api/flows`, joins ship nav (position truth) | 5s |
| `GET /api/flows/lanes?window=1h\|6h\|24h` | PG aggregation: `tour_leg_telemetry` + `transactions` + `arbitrage_execution_logs` → per-lane realized units/profit | 30s |
| `GET /api/flows/topology` | PG: `gate_edges` + server-derived deterministic galaxy layout (erratum: PG stores no galaxy-level system coordinates — see plan Task 2) | on load, cached |

The browser never talks to the daemon directly — single origin, one client.

PG facts this rests on (verified): `tour_leg_telemetry(tour_id, ship_symbol,
leg_index, waypoint, good, is_buy, planned_units, realized_units,
planned_unit_price, realized_unit_price, planned_at, realized_at)` — rows are
written **at realization** (plan-vs-actual per flown leg; zero unflown rows),
which is exactly why live intent requires the daemon feed. `containers` carries
program type/status/config for cross-checking active flows.

### 3. Web (`web/src`)

- `pages/TradeFlowsView.tsx` — route shell, window switch, layout.
- `components/flows/FlowGalaxyScene.tsx` — Konva stage; zoom/scale machinery
  following `GalaxyView.tsx` patterns.
- `components/flows/FlowLaneLayer.tsx` — gate lanes + realized-flow encoding.
- `components/flows/FlowShipLayer.tsx` — hull glyphs, interpolation, heading.
- `components/flows/FlowPlanPath.tsx` — dashed intent path + hop markers.
- `components/flows/FlowDetailPanel.tsx` — glass panel (Noir/Tailwind tokens).
- `components/flows/SystemDrilldown.tsx` — waypoint-scale view.
- `stores/flowStore.ts` + polling hooks mirroring the existing Zustand +
  polling-hook idiom.
- **Demo mode**: a mock scenario (existing `mocks/` pattern) drives the full tab
  fleet-stopped — synthetic flows, legs completing, a feed-loss simulation.

## Degradation

- **Daemon feed unreachable** → tab stays functional on PG alone: realized
  trails, last-known positions, and a "FEED LOST · last plan mm:ss ago" chip
  (mirrors the observatory's SIGNAL LOST doctrine). Intent paths disappear —
  never fabricated.
- **Hull trading without a published plan** (e.g. mid-adoption) → position-only
  glyph, no dashed path.
- **PG unavailable** → standard server error state for the tab.

## Testing

- **Go**: registry unit tests (publish/replace/remove per container lifecycle);
  handler tests (payload shape, empty fleet → `{"flows":[]}`, read-only — no
  state mutation on GET). House pattern: export-proof test à la the metrics
  Gather() tests.
- **TS**: lane aggregation math (window edges, profit signs), interpolation
  clamps (before departure / after arrival), detail-panel rendering from fixture
  flows, feed-loss chip behavior, demo-mode component tests. Full suite + tsc
  clean.
- **On-screen acceptance** (the S1 nebula lesson): rendered-layout assertions
  (element bounding boxes vs viewport) **and** a screenshot read of the live tab
  before the tab is called visible; verified against demo mode fleet-stopped and
  against live data with at least one active tour.

## Phase 2 (explicitly out of this build)

1. **Plan-time telemetry inserts** (`realized_at` NULL until flown — schema
   already supports it) → restart-durable intent + plan-vs-execution history.
2. **Plan vs. execution overlay mode** (projected path vs what actually happened).
3. **Historical range mode** (date-ranged lane analytics).
4. Wall-display / attract mode; additional cargo programs (manufacturing,
   contracts, ferries).

## Delivery notes

- The gobot feed and the visualizer tab are **separable lanes**: the tab builds
  against a fixture of the feed payload (demo mode) and degrades gracefully until
  the feed deploys; the feed rides a normal batched daemon deploy boundary.
- No config keys required; the feed serves on the existing metrics listener.
