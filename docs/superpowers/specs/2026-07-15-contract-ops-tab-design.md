# Contract Ops tab — live depot view (sp-c6pm)

**Date:** 2026-07-15 · **Approved:** Admiral (in-session) · **Bead:** sp-c6pm · **Companion engine bead:** sp-vp9k

A new visualizer tab at `/contract-ops`: a full-bleed NOIR Konva scene of the contract
operation's home system showing the depot machinery — source hubs, warehouses, stockers,
delivery hulls, workers — live from the daemon's Postgres, with four **accreting passes**
(iterative deepening, same mechanic as the architecture deck).

## Explicitly rejected

- **Deadline countdown / urgency ramp** — useless: the coordinator churns contracts
  continuously; deadlines are never in play. Deadline appears only as a static fact in
  expanded details.

## The four passes (stepper, bottom-left; deep-linkable; layers accrete)

- **Pass 0 · Contract** — contract card: good, destination, UnitsFulfilled/UnitsRequired
  progress, **lifecycle phase strip** NEGOTIATE→ACCEPT→SOURCE→DELIVER→FULFILL (current lit),
  **cycle stats** (fulfilled/hr, avg cycle time from historical `contracts` rows), payment vs
  cost-so-far margin, **worker liveness** (container heartbeat freshness; stale = red).
  Map: destination beacon only.
- **Pass 1 · Topology** — + warehouse hulls with per-good fill rings (event-sourced:
  Σ`warehouse_stockings` − Σ`warehouse_withdrawals`), source hubs, delivery-hull parking
  hubs, cluster territories (dashed service radius per delivery hull — clusters are emergent
  nearest-hull-wins in `depot/delivery.go`, so the view draws exactly that).
- **Pass 2 · Fleet** — + every role ship as a glyph (role = `ships.dedicated_fleet` +
  container join): nav status, cargo fill bar, transit animation; click → manifest card
  (cargo JSONB, container status, heartbeat).
- **Pass 3 · Flow** — + event ticker (stockings, withdrawals, contract transactions) and a
  small throughput chart (contracts fulfilled/hr).

## Data findings that shaped the design (live DB, 2026-07-15)

- **Two depots** for the live player (`central`: warehouse TORWIND-A@A1, stocker, 8 delivery
  hulls across waypoints, 5 uncrewed source-hub slots; `j58`: warehouses TORWIND-11/12,
  stockers 13/14, delivery hull 15). The scene renders **all** of the live player's depots,
  labeled by depot id.
- **Player selection**: era re-registration leaves multiple players with the same agent
  symbol; `players.last_active` alone picks the wrong one. Live player = owner of RUNNING
  containers (max heartbeat), fallback `players.last_active`.
- Container identities confirmed: `CONTRACT_FLEET_COORDINATOR`, `CONTRACT_WORKFLOW` (worker,
  `parent_container_id` → coordinator), `WAREHOUSE`, `TRADING`+`command_type='stocker'`.
- `contracts.deliveries_json` keys are **PascalCase** (no json tags on the domain struct).

## Ship animation (Admiral-approved option 3)

DB persists only destination + `arrival_time` for IN_TRANSIT (origin/departure deliberately
dropped in `ship_dto.go`). Approach: **client-side origin memory** — the store remembers each
ship's last stationary waypoint and the poll timestamp at which it flipped to IN_TRANSIT
(≈departure ±5s) → true-path interpolation for every flight observed from its start;
ships already mid-flight at page load render as "inbound → X · ETA" near the destination.
Companion bead **sp-vp9k** adds origin/departure persistence to gobot for exact tweens later.

## Architecture (follows the flows-tab pattern verbatim)

**Server** — `server/routes/contract-ops.ts`, mounted at `/api/contract-ops` in `index.ts`;
lazy pg Pool (`DATABASE_URL` default as flows.ts), `pool.on('error')`, 503 `db_unavailable`
degrade; pure logic in `server/utils/contractOps.ts` (vitest, tests first):

- `GET /topology` (60s in-memory cache): live-player id, `contract_depots` rows (4 parsed
  element arrays), waypoints of every involved system (backdrop + coords).
- `GET /live` (browser polls 5s): active contract (+parsed deliveries), recent contracts
  (cycle stats), RUNNING contract containers (+heartbeats), role ships
  (`dedicated_fleet IN ('contract','stocker','warehouse') OR container_id ∈ contract
  containers`) with nav/cargo, warehouse levels (stockings−withdrawals reduced per
  waypoint×good), last ~20 events, contract P/L from `transactions`
  (`related_entity_type='contract'` / `operation_type='contract'`).

**Web** — `pages/ContractOps.tsx`; `store/contractOpsStore.ts` (zustand: topology, live,
pass depth 0–3, selection, origin-memory map, error); `hooks/useContractOpsPolling.ts`
(topology once, live 5s); `services/api/contractOps.ts` (+barrel export);
`utils/transitMemory.ts` (pure interpolation + memory update, vitest tests first);
`components/contract/` (Scene, ContractCard, PassStepper, EventTicker, ShipCard).
Route in `App.tsx`, link in `Navigation.tsx`. NOIR tokens throughout; `useRafClock`
interpolation; scale-compensated strokes; center-once guard; designed empty states
(no depot / no active contract / 503).

## Gates

Server & web vitest suites green; `tsc` builds green both sides; endpoints verified against
the live DB; commit only files of this feature; push per session protocol; close sp-c6pm.
