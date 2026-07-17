# Galaxy View — Trade Flows Redesign

- **Date:** 2026-07-16
- **Status:** Approved design (brainstorm complete)
- **Origin:** Admiral request — convert the Visualizer's Trade Flows page into a true Galaxy View: real geometry, all arb trade routes, active trade ships animated in real time along their legs with direction, and expected-vs-realized profit per tour.
- **Related:** `docs/superpowers/specs/2026-07-10-trade-flows-tab-design.md` (current page), `docs/superpowers/specs/2026-07-16-longer-trade-tours-and-placement-engine-design.md` (epic sp-fguo: 5-6-system tours, RATE objective, open/closed modes, placement engine), `docs/superpowers/specs/2026-07-05-visualizer-cinematic-observatory-design.md` (nebula/noir).

## 1. Goal

Upgrade `/trade-flows` from an abstract force-layout graph into a real-coordinate galaxy scene where:

1. Every active trade program (tour / closed tour / trade-route / arb / relocation) draws its full planned route.
2. Every active trade ship is animated in real time — positioned along the system→system edge proportionally to elapsed vs planned time, with clear direction.
3. Each tour shows **expected profit** (solver projection) and **actual profit realized so far** (transaction ledger), as an at-a-glance ring on the hull and exact numbers in a roster panel / hover card.
4. Realized-profit history lanes (1h/6h/24h) glow under the live traffic.

This is an **evolve-in-place** redesign of the existing page — polling hooks, flow store, drilldown, and feed-lost degradation all survive.

## 2. Current state (what we build on)

- Page: `visualizer/web/src/pages/TradeFlowsView.tsx` → `components/flows/FlowGalaxyScene.tsx` (Konva). System nodes use a **synthesized force layout** (`server/utils/galaxyLayout.ts`) — no real galaxy coordinates in PG.
- Data: `GET /api/flows/live` (5s poll; daemon flowfeed proxy + PG `ships` join), `GET /api/flows/lanes?window=` (30s; PG `tour_leg_telemetry` + `arbitrage_execution_logs`), `GET /api/flows/topology` (once; PG `gate_edges` + force layout, 5-min cache). Client: `web/src/services/api/flows.ts`, `web/src/hooks/useFlowsPolling.ts`, `web/src/store/flowStore.ts`, types in `web/src/types/flows.ts`.
- Daemon feed: `gobot/internal/adapters/flowfeed/registry.go` — `Flow{ContainerID, Program, Ship, TourID, CurrentLeg, Cargo, RemainingHops, Projected{Profit, RatePerHour}, PlannedAt}`. Built in `flow_publish.go`. Forward plans are **in-memory only** (durable intent is Phase 2 of the 2026-07-10 design; unchanged here).
- Ground truth: PG `ships` has `nav_status, system_symbol, location_symbol, location_x/y, arrival_time` **plus migration-040 transit columns `origin_symbol, origin_x, origin_y, departure_time`** that `/api/flows/live` does not yet select.
- Realized attribution: `transactions` rows carry `related_entity_type='container'`, `related_entity_id` = container/tour id, `operation_type` (`tour`/`arbitrage`/…), signed `amount`; `metadata->>'ship_symbol'`, `->>'good_symbol'`. `tour_leg_telemetry` holds planned-vs-realized per leg.
- Travel model: **cross-system jumps are instant** (gate → jump → cooldown). Ships are never physically between systems; warp (time-consuming cross-system transit) is a rare special case. The **complete tour route is known beforehand**, including per-leg planned travel seconds (`TourLeg.TravelSecondsFromPrev`, `gobot/internal/domain/routing/tour.go`).

## 3. Decisions (brainstorm outcomes)

| Question | Decision |
|---|---|
| System positions | **Real galaxy coordinates**, persisted snapshot in PG; force-layout fallback only for systems not yet snapshotted. |
| Route layers | **Active programs + realized-history lanes** (tier B). No opportunity web now — seam left for placement-engine scores (sp-z7ng) to become an opportunity heat layer later. |
| Profit display | **Hybrid**: progress ring on each hull + roster side panel + hover/selection card. |
| Architecture | **Evolve in place** (no rebase onto MapView's GalaxyView, no WebGL). Konva stays. |

## 4. Data plane

Five changes, all small:

### 4.1 `system_coords` table (gobot migration), lazily filled by viz-server

- Schema: `system_coords(era_id, symbol, x, y, fetched_at)`, PK `(era_id, symbol)`. Era-scoped so a galaxy reset naturally refills.
- gobot owns the DDL (numbered migration). **viz-server owns population**: while building `/api/flows/topology`, any gate-connected system missing coords triggers a single live-API `GET /systems/{symbol}` and an upsert. No bulk universe pagination. New frontier systems acquire real coords within one topology refresh (5-min cache).
- `/api/flows/topology` response: each system gains `x, y, layout: "real" | "force"`. Systems still missing coords are force-placed **relaxed against their gate-neighbors' real positions** (anchored force pass), so mixed regimes don't fight.

### 4.2 Exact transit columns on the live feed

`/api/flows/live` ships join adds `origin_symbol, origin_x, origin_y, departure_time` (already in PG via migration 040). `FlowShipNav` (`web/src/types/flows.ts`) gains the same fields. Client interpolates within-leg position purely from clock vs `departure_time/arrival_time`.

### 4.3 Realized-so-far folded into the live response

No extra poll. For the active flows in a `/api/flows/live` response, viz-server runs one grouped query:

```sql
SELECT related_entity_id, SUM(amount) AS net, MAX(timestamp) AS last_event_at
FROM transactions
WHERE related_entity_type = 'container' AND related_entity_id = ANY($1)
GROUP BY related_entity_id
```

Signed sum ⇒ purchases, sells, and refuels net to true realized profit. Each flow gains `realized: { net, lastEventAt }`. (Plan-stage check: index on `transactions(related_entity_id)`; add if missing.)

### 4.4 System-level lane rollups

`/api/flows/lanes` additionally returns lanes grouped **system→system** (directed), keeping waypoint→waypoint detail for the drilldown. Window switch (1h/6h/24h) unchanged. Rollup lives in `server/utils/laneAggregation.ts`.

### 4.5 Flow feed serializes per-hop planned travel seconds

`flowfeed.Hop` gains `travelSeconds` (from `TourLeg.TravelSecondsFromPrev` / fueled-tour response) and each hop's `system` tag. Mapped in `flow_publish.go`. This gives the client the full route timeline up front. Hops lacking planned seconds (e.g., legacy arb flows) fall back to the equal-halves convention (§6).

## 5. Scene composition (bottom → top)

1. **Backdrop** — existing `AmbientBackdrop` (nebula + parallax starfield, Deep Space Noir tokens), reused as-is.
2. **Gate web** — hairline edges for every gate connection; dashed while under construction. Dim, structural.
3. **History lanes** — system→system realized-profit rollups for the selected window; warm glow strokes, brightness/width ∝ realized credits. Under live traffic.
4. **Tour paths** — per active flow, the full planned route polyline: hops ahead lit with a directional gradient (bright toward next stop), completed hops fading. **Closed tours render as loops** back to the anchor system (anchor gets a ring marker). **Relocation/deadhead transits use a distinct cool/dashed style** vs warm profitable legs.
5. **Ships** — oriented glyph (nose = travel bearing), short comet trail, **progress ring** (§7). Working inside a system ⇒ slow orbit around the node (dwell animation); cross-system hop ⇒ edge glide (§6).
6. **System nodes** — sized/brightened by realized credits touching the system in the selected window; home system keeps its star ring; anchor systems marked.
7. **HUD** — roster side panel (§7), hover/selection card, window switch, per-layer toggles (lanes / paths / ships), existing feed-lost chip.

Interactions: pan/zoom and click-system→`SystemDrilldown` carry over. Clicking a ship (map or roster row) eases the camera to it and pins its detail card.

## 6. Motion model

**Schedule first, observation second.** Per flow, the client builds a planned timeline from the feed (per-hop `travelSeconds` + trade stops). A `requestAnimationFrame` loop renders position purely from the clock against that timeline; the 5s poll only **re-anchors**.

Kinematic states:

1. **Working inside a system** (trading, or waypoint-hopping within one system): ship stays at the node with a slow orbit; sub-waypoint travel is the drilldown's job, not the galaxy's.
2. **Cross-system hop A→B**: one glide along the A→B edge spanning *departure from last A-waypoint → gate leg → (instant jump) → cooldown at B's gate → arrival at first B-waypoint*. Position = elapsed ÷ total planned seconds for the sequence. Pure gate pass-throughs (arrive at gate, cooldown, jump onward) are a short node-dwell between two edge glides; multi-hop chains A→B→C compose naturally. A true warp leg (real cross-system transit) renders as direct interpolation on the edge from its own `departure/arrival` timestamps.
3. **Resync**: when a poll lands, planned position blends toward observed truth — never a visible teleport. Ahead of plan ⇒ ease faster; behind ⇒ decelerate; when the actual B-leg starts, its real timestamps re-anchor the remaining glide so the hull lands on B's node at the true arrival moment. If actual stalls past plan, hold at the segment boundary (no overshoot).

Direction is carried three ways, no arrow clutter: glyph rotation, comet trail, and the path polyline's directional gradient.

**Lifecycle**: new flow ⇒ path fades in; completed/aborted ⇒ path + glyph fade out (~2s); feed lost ⇒ ships freeze at last schedule position, feed-lost chip shows, paths dim to stale-intent styling. No fake motion on stale intent.

**Fallback**: hops without planned seconds map the edge as equal halves per system traversed, with exact within-leg interpolation from nav timestamps on whichever half is observable.

## 7. Profit UX

**Ring semantics.** Realized net is a signed sum, so early tours run **negative** (capital committed, nothing sold). Rendering: net < 0 ⇒ empty ring with faint red under-glow; net crosses 0 ⇒ amber fill; approaching projection ⇒ green; past projection ⇒ soft overshoot glow. Fill = `clamp(realizedNet / projectedProfit, 0, 1)`. Exact numbers are one hover away.

**Roster panel** (right side, collapsible): one row per active flow — ship + program badge (tour / closed loop / arb / trade-route / relocation), route summary, projected profit & $/hr, realized-so-far bar (ring semantics), current leg + ETA. Sorted by projected $/hr desc. Header: fleet totals — Σ projected, Σ realized-so-far, realized $/hr for the selected window.

**Hover/selection card**: large ring, projected vs realized side by side, current action ("gate leg → X1-QK42" / "selling FUEL"), cargo manifest from the feed, next 3 stops. Credits compact (1.2M); deltas colored.

## 8. Degradation

- **Daemon feed down** ⇒ feed-lost chip, ships freeze at last schedule position, paths dim to stale-intent; history lanes keep working from PG. (Inherited behavior, preserved.)
- **Live API down** (coord fetch fails) ⇒ only unknown systems fall back to anchored force placement; snapshotted systems unaffected. Server logs the miss; no user-facing error.
- **PG down** ⇒ page degraded as today (out of scope).

## 9. Testing & verification

- **Vitest units**: schedule builder (timeline from hops), glide math (edge fraction incl. cooldown dwell; resync blending; equal-halves fallback), lane system-rollup grouping, realized-sum response shaping.
- **Endpoint tests** (viz-server): lazy coord fill (mocked live API; upsert + `layout` flag), realized aggregation over seeded `transactions`, lanes rollup.
- **gobot test**: flowfeed serialization of `travelSeconds` + hop `system` tags.
- **On-screen verification** (the nebula lesson — assert visible pixels, not backing stores): a `?demo=1` fixtures mode injects a canned galaxy snapshot (systems, flows mid-glide, rings at 3 fill states, a closed loop, a relocation transit); screenshot via headless Chrome and eyeball/compare. Deterministic fixtures double as design-iteration harness.
- **Perf sanity**: ~134 systems / ~503 edges / dozens of hulls at 60fps is comfortably in Konva range; ships on their own layer, hit detection only on hover targets.

## 10. Out of scope / seams

- **Opportunity heat layer** — when the placement engine (sp-z7ng) persists per-system `score(x)=E_x−β·D_x`, it plugs in as an additional toggleable layer under the lanes. The layer-toggle mechanism is built now; the data source comes later.
- **Ahead/behind-schedule display** — the full planned timeline makes schedule drift computable client-side; not surfaced in v1.
- **Durable tour persistence** — forward plans remain in-memory in the daemon (Phase 2 of the 2026-07-10 design); feed-lost degradation is the mitigation, unchanged.
- **Waypoint-level animation** — stays in `SystemDrilldown`.

## 11. Open details for plan stage

1. Verify `ProjectedProfit` semantics (does the solver projection net fuel the way the signed transaction sum does?) — affects only ring-ratio comparability; numbers are shown raw regardless.
2. Confirm/add index on `transactions(related_entity_id)`.
3. Confirm `flowfeed.Hop` current fields before extending (registry.go) and whether trade-route/arb programs can supply planned seconds cheaply or take the fallback.
4. Jump cooldown duration source (constant vs per-ship) for the planned-sequence total; if unavailable, fold cooldown into the fallback dwell at the node.
