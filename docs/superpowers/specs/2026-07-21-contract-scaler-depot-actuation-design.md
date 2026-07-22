# Contract Scaler — Depot Actuation (C2d) Design

**Bead:** sp-urpxy (epic sp-9le3x) · **Date:** 2026-07-21 · **Author:** shipwright (Admiral-directed)

> **FINAL CORRECTION (post-analyst, authoritative — supersedes the "orphaned goods" premise below and §4's exporter lookup).** The depot's `supported_goods` is NOT orphaned: `launchDepotWarehouse` self-solves it unconditionally via the on-demand demand miner (`depotColocatedWarehouseTargets`, commit f861cd7c). This feature deliberately **replaces that on-demand self-solve, on scaler-grown warehouses, with a FIXED whitelist pinned once at arm** — the 8 universe-invariant far-source symbols `{COPPER_ORE, IRON_ORE, ALUMINUM_ORE, GOLD, SILVER, DIAMONDS, PRECIOUS_STONES, DRUGS}` at `target_units = 140/good`, pinned via `launchDepotWarehousePinned` (bypasses the miner; no recompute, ever). The per-era role lookup resolves only geometry (export waypoints + hub); the good symbols are the constant. Authority: Admiral in-chat ruling + economy-analyst reply **st-wisp-2h6r5**. As-built: SHA `cc479268`.

## Context

The dedicated contract auto-scaler (epic sp-9le3x) ramps a fixed, exclusive contract fleet
one hull at a time behind a 200 000-credit cushion. Its domain plan already sequences three
roles — delivery haulers, then a central far-source warehouse, then a stocker — but only the
delivery role is actuated. When the ramp reaches a `Warehouse`/`Stocker` plan unit, the
actuator ignores `unit.Role` and buys a plain "contract"-tagged, spread-homed delivery hull
(`contract_scaler_ports.go:284`). So the depot half of the plan is inert. This was banked as
"C2d" at C2b landing (commit `1a5a4bbd`).

Verified against the live durable store + `daemon.log`, the real state is **not** what the
banked note implied:

- There is **no general-economy warehouse system.** The two warehouses (TORWIND-15/11) and the
  stocker (TORWIND-18) at hub `X1-UM5-I56` **are the one contract depot** — reconciler-built
  2026-07-21 13:22 (`"Launched reuse-staffed depot for hub X1-UM5-I56 (2 warehouse, 1 stocker,
  1 delivery)"`), persisted in the durable depot store, and **boot-reloaded every restart**
  (`"Reloaded 1 contract depot(s) … from durable store (restart-safe registry)"`). It is the
  warehouse a delivery hull sourced from at zero cost (30k saved) earlier today.
- The depot is **orphaned.** The log is explicit: `"depot buffer re-solve deferred to armed
  reconciler … warehouse TORWIND-15 … left to the reconciler's supported_goods"` — and C3
  (`f861cd7c`) deleted the reconciler. Nothing now owns what the depot stocks or how deep.

So the task is **not** to build warehouse/stocker behaviour — that machinery survived C3 intact
and is running. It is to make the scaler the depot's **fixed-plan driver**, the role the deleted
reconciler played, reusing the surviving machinery and fixing the orphan.

## Goal

The scaler scales one hull at a time, chooses the next optimal role from its fixed plan, and
places it — reusing the existing depot for warehouse/stocker roles, preferring reuse over buy for
every role, and owning the far-source goods the reconciler deletion orphaned.

## Design (reuse the wheel; wire, don't rebuild)

### 1. One role-aware ramp
Each tick the ramp fills the fixed plan in order — delivery hulls first to the ~7-park knee, then
warehouse, then stocker — one unit at a time, capped by the live ceiling
(`contract_fleet_max_hulls`, total budget) and the 200 000-credit cushion (the sole money guard,
RULINGS #6 amendment). `reconcileOnce` branches on `unit.Role`:

- **DeliveryHauler** → unchanged: buy+dedicate "contract" + demand-ranked spread-home
  (`homeContractHull`). Byte-identical to today.
- **Warehouse** → grow the existing depot: place an idle hull via the existing
  `launchDepotWarehouse(ship, hubWaypoint, coLocatedWarehouseShips, playerID)` with the resolved
  far-source `supportedGoods`.
- **Stocker** → grow the existing depot: `launchDepotStocker(ship, warehouseWaypoint, playerID)`.

### 2. Reuse before buy — for every role
The scaler already reclaims before it buys a delivery hull (`tryReclaim` → `FindReclaimable`:
idle, undedicated, cargo-capable, never-poach RULINGS #7). Extend the **same** reuse tier to
warehouse/stocker: reclaim an idle hull and launch it into the depot as a warehouse/stocker;
buy only when no reclaimable hull is free. `FindReclaimable` is reused verbatim; only the
placement (`launchDepotWarehouse/Stocker` vs delivery home) branches by role. Reclaim stays free
of the cushion (it strictly reduces spend); buys stay cushion-gated.

### 3. Depot registry = source of truth for actuated warehouse/stocker units
Today the ramp's `current` counts only "contract"-tagged (delivery) hulls, so raising the ceiling
would make it **buy new warehouses, duplicating TORWIND-15/11.** Fix: the ramp's per-role current
reads the **depot registry** for warehouse/stocker element counts (delivery stays "contract"-tag
counted). The ramp therefore **reconciles the existing depot** (2 warehouses + 1 stocker already
present) up to the plan's targets — adding only what's short — and it is restart-safe because the
depot registry persists + boot-reloads (RULINGS #2), and the "contract" delivery count is live.

### 4. The scaler owns `supported_goods` (fix the orphan)
The scaler resolves the depot's far-source goods — a **lookup, not a solver** (RULINGS #14
home-system scope): the goods the hub's far-source waypoints EXPORT (ores / precious metals+stones
/ drugs), the same role classification the existing `RoleResolver` already performs. These feed
`launchDepotWarehouse`'s `supportedGoods` for the warehouses the scaler adds, and re-solve the
orphaned ones the reconciler left. This is the one genuinely new piece of logic.

## Reused wholesale (unchanged)
Depot registry + durable store + boot-reload (`reloadDepotRegistryAtBoot`); `launchDepotWarehouse`
/ `launchDepotStocker` and the `depotCoordinatorSink` port; `RunWarehouseHandler` (passive hold +
StorageCoordinator) and `RunStockerCoordinatorHandler` (source-legs); the contract coordinator's
depot routing/exclusion (warehouse/stocker/depot-delivery tags already fall out of the delivery
pool); the scaler's `FindReclaimable` reuse tier; the delivery buy+spread-home path.

## New / changed
- `internal/application/contractscaler/commands/run_contract_scaler.go` — role-aware `reconcileOnce`
  (per-role current + fill order + role branch); reuse tier branches placement by role.
- `internal/adapters/grpc/contract_scaler_ports.go` — a depot-element counter (warehouse/stocker
  current), a depot-growth port wrapping `launchDepotWarehouse/Stocker`, and the far-source-goods
  lookup.
- `internal/domain/contractscaler/plan.go` — expose per-role targets (delivery D / warehouse W /
  stocker S) so the ramp fills by role in order (the sequence already encodes them).

## Money & safety (RULINGS)
- **#4 / #6:** every buy stays gated by the 200 000 cushion; reuse stays free but never buys.
  No guard weakened. **#5** 50k floor untouched.
- **#2 / #3:** restart-safety from the persistent depot registry + single-writer AssignFleet.
- **#14:** far-source goods resolved within the home system only.
- **#19 default-off / byte-identical:** at ceiling=2 the ramp never reaches a warehouse plan index
  → no depot growth, no goods re-solve → byte-identical to today. The feature activates only when
  the operator raises the ceiling past the delivery-hull count. "Arming" = raising the ceiling
  (the existing single lever); no new flag.

## Verification
- RED-first unit tests: per-role current from a spy depot registry; ramp fills delivery→warehouse
  →stocker in order and stops at ceiling; warehouse/stocker units call `launchDepotWarehouse/Stocker`
  (not the delivery home); reuse-before-buy for warehouse/stocker; the ramp does **not** duplicate
  an existing depot warehouse (reconcile, add-only-the-short); cushion holds each role; far-source
  goods lookup returns the hub's exporter goods; default-ceiling(2) byte-identical (zero depot calls).
- `go build ./... && go vet ./... && gofmt -l . (empty) && go test -race ./...` green in the worktree.
- Land via captain-gate → main; verify merge SHA + numstat.
- Deploy (`make restart-daemon`); raise `contract_fleet_max_hulls` past the delivery count and
  confirm the scaler grows the depot (adds the plan-short warehouse/stocker) and re-solves goods —
  reusing idle hulls before buying — with the 200k cushion holding.
