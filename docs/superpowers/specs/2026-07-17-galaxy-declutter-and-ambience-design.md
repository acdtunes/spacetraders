# Galaxy View — Declutter at Fleet Scale + Ambient Detail

- **Date:** 2026-07-17
- **Status:** Approved design (brainstorm complete)
- **Origin:** Admiral request with a real-galaxy screenshot (a lane hairball): "make the trade routes thinner and add an option to hide them. Make this better" — followed by an enhancements round. Direction set by the Admiral: **no ops-room machinery** (the engine is automated); the view is for *watching* the fleet, beautifully.
- **Related:** `2026-07-16-galaxy-view-trade-flows-design.md` (sp-gl06), `2026-07-17-galaxy-freshness-layer-design.md` (sp-kiaf). A Lanes hide toggle already exists (layer row, bottom-left) — discovered during brainstorm; the deeper problem is defaults, not the toggle.

## 1. Diagnosis (from the screenshot)

At ~134 systems with a busy fleet, the hairball is **count × treatment**, not stroke width: every profitable system-pair in the window renders a full-ceremony lane (marching dash + arrowhead, one per direction), and the log-width ramp saturates so the #1 corridor and the #40 look identical. Plan paths add a second dashed family on top. Fix: **visual weight must follow information rank.**

## 2. Decisions (brainstorm outcomes)

| Topic | Decision |
|---|---|
| Lanes default | **Top-N emphasis**: top 12 by \|realizedProfit\| get full treatment; the rest are hair-thin faint solid capillaries; below a floor (2% of #1) hidden entirely. |
| Paths default | **Quiet-all + bright-selected**: every path at whisper weight; hovered/selected flow's path gets the full gradient treatment. |
| HUD / direction-merge | **Not in scope** (Admiral chose C — layer changes only; toggle row untouched). |
| Enhancements | **In**: live fill ticker, system hover card, lane hover detail, schedule-drift glyphs, beautiful hulls. **Out** (rejected): replay scrubber, jump-to-search, minimap, follow mode. |
| Ship glyphs | Distinguish **light vs heavy haulers** only. Two silhouettes; program color demoted to accent; animated thruster flare while gliding. |

## 3. Lane declutter (client-only)

In `FlowLaneLayer` (system-lane records, already sorted by profit desc from the server):

- **Arteries** — the top `LANE_EMPHASIS_N = 12` lanes by `|realizedProfit|`: current full treatment (profit color, log width, marching dash, mid-lane arrowhead, direction-pair offset).
- **Capillaries** — every other lane at or above the floor: a single **solid** stroke (no dash animation, no arrowhead) at width `max(0.3, 0.5/scale)` and ~0.25 alpha of its profit color. Direction-pair offset retained (cheap, avoids z-fighting).
- **Floor** — lanes with `|realizedProfit| < 0.02 × |top lane's profit|` are not rendered.
- Loss-making lanes rank by magnitude (a big loss is information); they keep the dim loss color.
- Partitioning lives in a pure helper `partitionLanes(records, n, floorPct)` in `flowGeometry.ts` (unit-tested); the layer maps the two tiers to their treatments.

## 4. Path quieting (client-only)

In the scene's path rendering:

- **Quiet (default, all flows)**: stroke ~`0.6/scale`, alpha ~0.25, **static** dash (no gradient, no animation); deadhead legs fainter still (~0.15) and dashed shorter.
- **Bright (hovered or selected flow)**: today's full treatment — directional gradient, current widths, anchor ring, hop markers. Only the focused flow's hop markers render; quiet paths draw no hop markers (marker soup was part of the hairball).
- Selection source: existing `hoveredFlowId ?? selectedFlowId` from the store.

## 5. Beautiful hulls: light vs heavy haulers

- **Data**: the `/api/flows/live` ships join adds `cargo_capacity` (PG `ships`). `FlowShipNav` gains `cargoCapacity: number | null`. Client classifies `heavy = cargoCapacity >= 80`, else light (null ⇒ light). Threshold is a named constant with a comment (light haulers run ~40, heavy freighters 80+; verify the fleet's actual capacities at plan time and adjust the constant if the split disagrees).
- **Silhouettes** (Konva vector groups, scale-normalized, nose = +x, drawn in the rotated body group that already exists):
  - **Light hauler** — slender dart: tapered fuselage, small canopy glint near the nose, single engine block aft.
  - **Heavy hauler** — broad freighter: wide hull, visible segmented cargo spine (2–3 body segments), twin engine nacelles aft.
- **Color language**: hull body in neutral ink tones (`NOIR.ink`/`dim` mixes); the **program color** (tour/trade-route/arb) becomes an accent stripe along the fuselage plus the engine-glow tint — no more solid program-colored blobs.
- **Thruster flare**: while `mode === 'glide'`, an aft flame polygon flickers (length/alpha jitter off the raf clock, deterministic per ship via hash phase); while dwelling, engines dark and the whole glyph drops to ~0.85 alpha.
- Progress ring, selection ring, comet trail, hover/click behavior, and label all unchanged.

## 6. Ambient detail widgets

### 6.1 Live fill ticker

- **Endpoint**: `GET /api/flows/fills?limit=30` — recent realized trades, merged desc by time from:
  - `tour_leg_telemetry` realized rows (`realized_at IS NOT NULL`): ship, good, `is_buy`, units, unit price, waypoint, at.
  - `arbitrage_execution_logs` (`success = true`): ship, good (**add `good_symbol` to the SELECT**), units_sold, net profit, sell_market, at — rendered as sells.
  - Response rows: `{ at, ship, good, isBuy, units, credits, waypoint }` where `credits` is signed (sell +units×price, buy −units×price; arb uses `actual_net_profit`). `limit` capped at 100.
- **Client**: polled every **15s** (`useFlowsPolling` effect → `flowStore.fills`). Renders as a bottom-edge ambient stream (above the toggle row, full width, pointer-events none): newest entry slides in from the right, ~6 visible, older entries fade with age; sells in `NOIR.good`, buys in dim `NOIR.warn`. Format: `TORWIND-3 sold 40 ELECTRONICS +82,000 @ X1-KA42-D39`. Deduplication by `(source table, row id)` — the endpoint includes a stable `id` per row.
- Failure: ticker simply stops advancing (no error surface); resumes on next successful poll.

### 6.2 Shared hover tooltip infra

One DOM tooltip component (`FlowTooltip`): absolutely-positioned styled div over the canvas (the panel/glass styling of the existing cards), driven by a store slice `tooltip: { kind: 'system' | 'lane'; key: string; x: number; y: number } | null` set from Konva pointer events (stage-relative → client coords). One component, two content renderers. Hide on pointer-leave, layer toggle-off, and drilldown open.

### 6.3 System hover card

Node hover → tooltip: system symbol (+ HOME/anchor badges), freshness line (`68% fresh · 41/60 listings`, pct ramp-colored, omitted when unsensed), realized credits in the window, resident hull count (flows whose current system matches), scout post status. Data: all already client-side (freshness, systemActivity, flows).

### 6.4 Lane hover detail

**Arteries only** get hit targets (a wider invisible stroke over the visible one); capillaries stay non-interactive. Hover → tooltip: `X1-AA → X1-BB`, credits in window, trip count, **top 3 goods** with per-good credits.
- **Server**: the lanes system-rollup gains `topGoods: { good: string; credits: number }[]` (top 3 by |credits|). Telemetry rows carry `good`; the arb query adds `good_symbol`. Waypoint-level lanes unchanged.

### 6.5 Schedule-drift glyphs

Client-only, from data already shipped (per-hop planned `travelSeconds`, `plannedAt`, nav-truth leg timestamps):

- `scheduleDriftSeconds(flow, nowMs)` (pure, in `flowMotion.ts`): planned elapsed-to-current-position (sum of planned `travelSeconds` for completed hops, anchored at `plannedAt`) vs actual elapsed; positive = behind. Flows lacking planned seconds → `null` (no glyph ever).
- Rendering: a small tick on the progress ring's edge — **amber** when drift > 5 min, **red** when > 15 min, **nothing** when on/ahead of schedule or unknown (silence = nominal; no ops-room noise). Thresholds are named constants.
- Roster rows show the same drift state as a small `+7m` suffix on the ETA line when behind.

## 7. Degradation

- `/fills` failure → ticker freezes silently; recovery is automatic. PG down → 503, same as siblings.
- Tooltips derive from already-polled state; no new failure modes. Feed-lost: tooltips over stale flows show stale data consistent with the frozen scene (acceptable; the feed-lost chip already flags it).
- All new rendering respects existing layer toggles: ticker is independent; lane tooltip dies with the Lanes layer; hulls/drift die with Ships.

## 8. Testing & verification

- **Units (web)**: `partitionLanes` (tiers + floor + loss-magnitude ranking), hauler classification threshold, `scheduleDriftSeconds` (on-time / behind / unknown, fixed clocks), fills dedup/ordering in the store, tooltip store slice.
- **Units (server)**: fills merge query shaping (both sources, signed credits, stable ids, limit cap), lanes `topGoods` rollup.
- **Endpoint tests**: `/fills` happy + 503; lanes response with `topGoods`.
- **Demo mocks**: fills stream fixture (mixed buys/sells, advancing timestamps), one deliberately-late tour (amber) and one very late (red), one heavy + one light hauler, dense-lane fixture.
- **On-screen (assert pixels)**: two scenarios — (a) the standard 4-system demo for widgets/glyphs/ticker; (b) a **dense synthetic hairball** (~40 systems, ~30 lanes via a generated mock topology) proving the declutter reads: exactly 12 arteries with arrows, capillaries as faint texture, quiet paths, and the before/after against the Admiral's screenshot aesthetic. Screenshot checklist per feature, headless Chrome, iterate until pass.

## 9. Out of scope

- Replay scrubber, jump-to-search, minimap, camera follow (rejected: no ops-room).
- Toggle-row polish and capillary direction-merge (Admiral chose layer-changes-only).
- Role-based silhouettes beyond the light/heavy split; cargo-fill indicators.
- Configurable N / thresholds in the UI (constants in code).
