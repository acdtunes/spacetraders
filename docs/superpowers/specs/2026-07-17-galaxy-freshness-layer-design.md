# Galaxy View — Market Freshness Layer (+ thinner route lines)

- **Date:** 2026-07-17
- **Status:** Approved design (brainstorm complete)
- **Origin:** Admiral request — show market-data freshness in the Galaxy View as a continuous 0–100% color scale, plus make the route lines thinner.
- **Related:** `docs/superpowers/specs/2026-07-16-galaxy-view-trade-flows-design.md` (the Galaxy View this extends, merged as sp-gl06), gobot `maxListingAge = 75 * time.Minute` (`gobot/internal/application/trading/commands/run_trade_route_coordinator_travel.go:711`), scout posts (`ScoutPostModel`, `gobot/internal/adapters/persistence/models.go:667`).

## 1. Goal

Overlay the fleet's **sensor picture** on the galaxy: per system, how much of its market data the trade solvers can actually use *right now*, on a continuous red→amber→green ramp — plus the actuator state (scout posts) that explains why. Secondary: halve the visual weight of route lines.

**Why it's load-bearing:** the tour snapshot and the cross-system sink scan both refuse listings older than 75 minutes (`maxListingAge`). A system whose listings have all aged out is invisible to the money engine regardless of how profitable it was an hour ago. The halo makes that invisibility visible.

## 2. Decisions (brainstorm outcomes)

| Question | Decision |
|---|---|
| Freshness metric | **Solver visibility**: `freshListings / totalListings` per system, where fresh = `last_updated >= now − 75min`. Continuous 0–100%, not buckets. Exact counts + freshest-scan age as drilldown detail. |
| Visual encoding | **Halo/aura** behind each system node, color-ramped; node core keeps encoding realized activity. No halo when the system has no listings at all (dark = unsensed). |
| Color ramp | Continuous interpolation over existing NOIR tokens: `bad` (0%) → `warn` (50%) → `good` (100%). No new palette. |
| Scout posts | **Yes, minimal marker**: small diamond tick per posted system — manned / relay-in-flight / unmanned. |
| Data plane | **New dedicated endpoint** `GET /api/flows/freshness`, polled every 60s. Not folded into lanes (window-parameterized; semantic mismatch) nor topology (5-min cache; wrong cadence). |

## 3. Data plane

### 3.1 Endpoint

`GET /api/flows/freshness` (viz-server, `server/routes/flows.ts`):

```jsonc
{
  "systems": [
    {
      "system": "X1-KA42",
      "totalListings": 60,        // market_data rows joined to this system's waypoints
      "freshListings": 41,        // rows with last_updated >= now - 75min
      "freshnessPct": 68,         // round(100 * fresh / total); total=0 rows are omitted entirely
      "freshestAt": "2026-07-17T12:03:11Z",  // MAX(last_updated) in the system
      "scoutPost": {              // null when the system has no scout post row
        "status": "manned",      // "manned" | "relay" | "unmanned"
        "hull": "TORWIND-9",     // assigned_hull; null when not manned
        "kind": "standing"       // scout_posts.kind
      }
    }
  ],
  "staleAfterMinutes": 75,
  "generatedAt": "..."
}
```

- **Query shape:** `market_data` joined to `waypoints` on `waypoint_symbol` for `system_symbol`, era-scoped by joining only current-era waypoints (reuse `currentEraId` from `server/utils/systemCoords.ts`); grouped by system with `COUNT(*)`, `COUNT(*) FILTER (WHERE last_updated >= $cutoff)`, `MAX(last_updated)`. A second query reads `scout_posts` (all rows; it is small) and is merged in TS. Systems with zero listings are **not returned** — absence is the "unsensed" signal.
- **The 75-minute constant** is declared once in the route file as `STALE_AFTER_MINUTES = 75` with a provenance comment pointing at gobot's `maxListingAge` (`run_trade_route_coordinator_travel.go:711`). The response carries it (`staleAfterMinutes`) so the client never hardcodes it.
- **Scout status derivation** (mirrors ScoutPostModel semantics): `assigned_hull` non-null/non-empty → `manned`; else `reposition_container_id` non-null/non-empty → `relay`; else `unmanned`.
- No player filter (consistent with the lanes/realized queries — single-player deployment in practice).

### 3.2 Client

- `getFlowFreshness()` in `web/src/services/api/flows.ts`; polled every **60s** in `useFlowsPolling` (own effect, like lanes).
- Store: `flowStore.freshness: FreshnessResponse | null` + `freshnessMissedPolls: number` (a failed poll increments; a successful one resets — drives degraded rendering, §5).
- Types in `web/src/types/flows.ts`: `SystemFreshnessRecord`, `ScoutPostStatus`, `FreshnessResponse`.

## 4. Visuals

### 4.1 Freshness halo

- One Konva `Circle` per freshness record, rendered in a `FreshnessLayer` group **below** the gate-web/lanes layers, at the system's node position: radius ≈ 4× node radius (scale-normalized like everything else), `fillRadialGradient` from `freshnessColor(pct)` at center-alpha to transparent at the rim.
- `freshnessColor(pct)`: piecewise-linear RGB interpolation `NOIR.bad → NOIR.warn` over 0–50 and `NOIR.warn → NOIR.good` over 50–100 (helper in `web/src/components/flows/freshness.ts`, pure + unit-tested).
- Center alpha scales with pct too (0% ≈ 0.18 smolder, 100% ≈ 0.45 glow) so green coverage reads brighter than red decay without overpowering lanes.
- Systems absent from the response render **no halo** — unsensed space stays dark.

### 4.2 Scout-post marker

- Small diamond (rotated Konva `Rect`) at the node's upper-right (offset ~nodeR + 6px, scale-normalized): **manned** = solid `NOIR.accent`; **unmanned** = hollow (stroke only) `NOIR.dim`; **relay** = hollow `NOIR.accent` with a slow opacity pulse (raf clock, ~1.2s period).

### 4.3 Layer toggle + drilldown detail

- `freshness` joins `layerToggles` (default **on**); a fourth button in the toggle row. Toggle hides halos *and* scout markers.
- `SystemDrilldown` header gains one line when a record exists: `Sensor: 68% fresh (41/60 listings, freshest 12m ago) · post: manned (TORWIND-9)` — post segment omitted when `scoutPost` is null. No new tooltip infrastructure.

### 4.4 Thinner route lines (the direct ask)

- `laneWidth` (`flowGeometry.ts`): magnitude cap `6 → 3`, floor `0.5 → 0.35`.
- `FlowPlanPath`: gradient stroke `1.4u → 0.9u`; deadhead/cool stroke `1.6u → 1.0u`.
- Gate-web hairlines and ship comet trails unchanged (already hairline / deliberately brightened by the sp-gl06 verification pass).

## 5. Degradation

- **Freshness poll fails:** keep rendering the last response; after **5 consecutive** missed polls, halos and markers drop to 50% of their normal alpha (stale sensor picture, honestly dimmed). Counter and threshold live in the store; the layer reads a derived `degraded` boolean.
- **PG down:** endpoint 503s (same `db_unavailable` shape as siblings); client counts it as a missed poll. Layer simply doesn't render before the first successful poll.
- **Empty galaxy / no market rows:** `systems: []` is valid — no halos, page unaffected.

## 6. Testing & verification

- **Unit (server):** freshness SQL shaping against seeded rows with a pinned clock — fresh/total counts, era scoping (dead-era waypoint rows excluded), zero-listing systems omitted, scout-status derivation for all three states, `staleAfterMinutes` echoed.
- **Unit (web):** `freshnessColor` endpoints and 50% midpoint hit the NOIR tokens exactly; alpha mapping monotonic; store missed-poll counter (increment/reset/degraded threshold).
- **Endpoint test:** supertest with mocked pg, verifying response shape + 503 path.
- **On-screen (the standing rule — assert pixels):** extend `mockFlows.ts` with a `mockFreshness()` spanning the ramp — X1-NK36 ≈ 95% (manned post), X1-KA42 ≈ 50% (relay post), X1-ZC66 ≈ 10% (unmanned post), X1-UU57 absent (no halo, no marker). Headless-Chrome screenshot checklist: four distinct halo states visible, three marker states distinguishable, freshness toggle removes both, route lines visibly thinner than the pre-change screenshot (`/private/tmp/galaxy-view-check.png` kept as the before).

## 7. Out of scope

- Per-waypoint freshness inside the drilldown map (the drilldown's own future concern).
- Freshness-driven alerts/automation — display only; the scout-post coordinator already owns the control loop.
- Historical freshness trends.
