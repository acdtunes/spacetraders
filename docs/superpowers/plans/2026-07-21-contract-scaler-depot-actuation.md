# Contract Scaler — Depot Actuation (C2d) Implementation Plan

> **CORRECTION (as-built, authoritative — supersedes Task 2 below).** `supported_goods` is NOT resolved by a far-source-exports lookup and NOT by the on-demand miner. It is a **fixed constant** — `contractscaler.FarSourceGoods = {COPPER_ORE, IRON_ORE, ALUMINUM_ORE, GOLD, SILVER, DIAMONDS, PRECIOUS_STONES, DRUGS}`, `DepotUnitsPerGood = 140` — pinned once at arm via `launchDepotWarehousePinned` (bypasses `depotColocatedWarehouseTargets`; no recompute). Per economy-analyst ruling st-wisp-2h6r5. The role lookup resolves geometry only. As-built SHA `cc479268`.

> **For agentic workers:** implement task-by-task, RED-first (write failing test → run → implement → run → commit `--no-verify`). Steps use `- [ ]`.

**Goal:** Make the contract auto-scaler drive the existing contract depot — one hull at a time, next role from its fixed plan — reusing `launchDepotWarehouse/Stocker` + the depot registry, reusing idle hulls before buying, and owning the far-source `supported_goods` the reconciler deletion orphaned.

**Architecture:** The scaler's `reconcileOnce` becomes role-aware. Delivery is unchanged (buy+dedicate `"contract"` + spread-home). Warehouse/Stocker units grow the **existing** depot via `depotstore.Store.AddElement` + `launchDepotWarehouse/Stocker`. Per-role `current` = delivery from `ContractHullCount("contract")`, warehouse/stocker from `LoadRegistry` element counts — so the ramp *reconciles* the depot (add-only-the-short) instead of buying duplicates. Reuse-before-buy (`FindReclaimable`) applies to every role.

**Tech Stack:** Go; hexagonal ports; the scaler domain (`internal/domain/contractscaler`), coordinator (`internal/application/contractscaler/commands`), and adapters (`internal/adapters/grpc/contract_scaler_ports.go`); the depot store (`internal/application/contract/depotstore`) + domain (`internal/domain/contract/depot`) + launch verbs (`internal/adapters/grpc/container_ops_depot_launch.go`).

## Global Constraints (verbatim from spec)
- **RULINGS #4/#6:** every BUY stays gated by the 200 000 cushion (`treasury-price >= 200000`); RECLAIM stays free but never buys. No guard weakened. **#5** 50k floor untouched.
- **RULINGS #2/#3:** restart-safe via the persistent depot registry; all fleet writes via the single writer (`AssignFleet` / the store).
- **RULINGS #7:** reclaim only `DedicatedFleet==""`, idle, cargo-capable hulls; never poach.
- **RULINGS #14:** far-source goods resolved within the home system only.
- **#19 default-off:** at ceiling=2 the ramp reaches no warehouse plan index → zero depot calls → byte-identical. "Arming" = raising `contract_fleet_max_hulls`.
- Reuse (do not rebuild): depot store/registry/boot-reload, `launchDepotWarehouse/Stocker`, `RunWarehouseHandler`/`RunStockerCoordinatorHandler`, the coordinator's depot exclusion, `FindReclaimable`, the delivery buy+spread-home path. Protected paths untouched.

---

### Task 1: Per-role plan targets (domain)

**Files:** Modify `internal/domain/contractscaler/plan.go` · Test `internal/domain/contractscaler/plan_test.go`

**Produces:** `RoleTargets(plan []PlanUnit) (delivery, warehouse, stocker int)` — counts of each role in the fixed plan, so the ramp can fill by role in order rather than by flat index.

- [ ] **Step 1:** Failing test `TestRoleTargets`: `BuildPlan` for 7 parks + a hub → `RoleTargets` returns `(7, 3, 1)`; for 0 parks → `(0,0,0)`; for 3 parks → `(3,3,1)` (delivery capped at parks, warehouse/stocker only when a hub exists).
- [ ] **Step 2:** Run → FAIL (undefined `RoleTargets`).
- [ ] **Step 3:** Implement `RoleTargets` counting `Role==DeliveryHauler/Warehouse/Stocker` in the plan slice.
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `--no-verify`: "feat(contractscaler): RoleTargets — per-role counts of the fixed plan (sp-urpxy)".

### Task 2: Far-source goods lookup (fix the orphan)

**Files:** Modify `internal/domain/contractscaler/roles.go` (+ `EraRoles`/resolver as needed) · `internal/adapters/grpc/contract_scaler_ports.go` (RoleResolver impl) · Tests alongside.

**Produces:** the resolved far-source good symbols (the goods the hub's `FarSources` waypoints EXPORT — ores/precious/drugs) reachable by the ramp, e.g. add `FarSourceGoods []string` to `EraRoles` (populated in `ResolveRoles` from the far-band exporters' `Exports`), and surface it through the coordinator's armed plan so warehouse growth can pass it as `supportedGoods`.

- [ ] **Step 1:** Failing test in `roles_test.go`: `ResolveRoles` over markets where far-band exporters export `{IRON_ORE, SILVER_ORE}` → `EraRoles.FarSourceGoods == [IRON_ORE, SILVER_ORE]` (sorted, deduped); central importers contribute none.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement: in the far-band branch of `ResolveRoles`, collect `m.Exports` into a set; sort+dedupe into `FarSourceGoods`.
- [ ] **Step 4:** Run → PASS. Add a ports test that `contractScalerRoleResolver.ResolveRoles` carries the goods through.
- [ ] **Step 5:** Commit `--no-verify`: "feat(contractscaler): resolve far-source goods (home-system exporter lookup) — owns the depot supported_goods the reconciler orphaned (sp-urpxy, RULINGS #14)".

### Task 3: Depot-aware per-role counter (port)

**Files:** Modify `internal/application/contractscaler/commands/run_contract_scaler.go` (new port interface) · `internal/adapters/grpc/contract_scaler_ports.go` (impl over `depotstore.Store.LoadRegistry`) · Tests with a spy.

**Produces:** `DepotElementCounter` port: `WarehouseCount(ctx, playerID) (int, error)`, `StockerCount(ctx, playerID) (int, error)` — reads the contract depot's element counts (`len(depot.Warehouses())`/`Stockers()`), the ramp's warehouse/stocker `current`. Read error → fail-closed (hold the ramp). Nil port → 0 (byte-identical: the ramp then only ever fills delivery, and delivery is capped by parks ≤ ceiling before any warehouse index).

- [ ] **Step 1:** Failing test: spy registry with a depot of 2 warehouses + 1 stocker → counter returns `(2, 1)`; empty registry → `(0,0)`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement the impl over `LoadRegistry` (sum across depots — there is one contract depot; summing is correct + future-proof).
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `--no-verify`: "feat(contractscaler): depot-aware warehouse/stocker counter — depot registry is the source of truth for actuated depot units (sp-urpxy)".

### Task 4: Depot growth port (reuse launch verbs + AddElement)

**Files:** Modify `run_contract_scaler.go` (port) · `contract_scaler_ports.go` (impl) · Tests with spies.

**Produces:** `DepotGrower` port: `GrowWarehouse(ctx, order DepotGrowOrder) error`, `GrowStocker(ctx, order DepotGrowOrder) error` where `DepotGrowOrder{PlayerID, ShipSymbol, Hub string, SupportedGoods []string}`. Impl: ensure a contract depot exists (`LoadRegistry`; if none, `AddDepot(NewContractDepot(id, [first-warehouse Element], …))`), then `store.AddElement(depotID, depot.RoleWarehouse/RoleStocker, Element{Waypoint: Hub, ShipSymbol})` (persist), then `launchDepotWarehouse(ship, Hub, coLocated, playerID)` / `launchDepotStocker(ship, warehouseWaypoint, playerID)` (run). SupportedGoods is threaded to the warehouse launch so the depot stocks the resolved far-source goods (fixes the orphan) — verify the exact launch/StartWarehouse seam and add a `supportedGoods` pass-through if `launchDepotWarehouse` doesn't already accept it.

- [ ] **Step 1:** Failing test: `GrowWarehouse` on a registry with an existing depot calls `AddElement(depotID, RoleWarehouse, {hub, ship})` then the warehouse launch spy with the ship+hub+goods; on an EMPTY registry it first creates the depot (`AddDepot`).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `GrowWarehouse`/`GrowStocker` composing store + launch. (Read `container_ops_depot_launch.go` launch signatures + `container_ops_depot_lifecycle.go` startDepot for the persist+launch idiom; reuse, don't duplicate.)
- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `--no-verify`: "feat(contractscaler): depot growth port — AddElement + launchDepot* grows the existing depot (sp-urpxy)".

### Task 5: Role-aware ramp (the core)

**Files:** Modify `internal/application/contractscaler/commands/run_contract_scaler.go` (`reconcileOnce`, `tryReclaim`) · Tests in `run_contract_scaler_test.go`.

**Consumes:** `RoleTargets` (T1), `EraRoles.FarSourceGoods` (T2), `DepotElementCounter` (T3), `DepotGrower` (T4).

**Interfaces:** `reconcileOnce` computes `haveDelivery = ContractHullCount`, `haveWh = counter.WarehouseCount`, `haveStk = counter.StockerCount`; `(dT, wT, sT) = RoleTargets(plan)`; `total = haveDelivery+haveWh+haveStk`; loop while `total < min(planSize, ceiling)`: pick the next role in fill order — delivery if `haveDelivery<dT`, else warehouse if `haveWh<wT`, else stocker if `haveStk<sT`; **reuse-before-buy** for that role (T-reclaim); actuate (delivery = existing buy+home; warehouse/stocker = `grower.GrowWarehouse/Stocker` with hub=`plan hub`/goods=`FarSourceGoods`); increment the role's have + total. Buy stays cushion-gated; reclaim free. Nil counter/grower ⇒ warehouse/stocker unreachable ⇒ delivery-only (byte-identical).

- [ ] **Step 1:** Failing tests: (a) ceiling=10, 7 parks, depot already has 2 warehouses+1 stocker, plenty treasury → ramp adds 0 delivery beyond 7, exactly **1** warehouse (to reach W=3) + 0 stocker (S=1 already met) → NO duplicate warehouses; (b) ceiling=2 → zero counter/grower/AddElement calls, delivery-only, byte-identical; (c) reuse-before-buy: an idle undedicated cargo hull is reclaimed for a warehouse slot (grower called with the reclaimed ship, buyer NOT called); (d) cushion: warehouse buy held when `treasury-price<200000`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement the role-aware loop + role reclaim/actuate branch. Keep the delivery path byte-identical (same buy+home calls).
- [ ] **Step 4:** Run → PASS (+ `-race`).
- [ ] **Step 5:** Commit `--no-verify`: "feat(contractscaler): role-aware ramp — fill delivery→warehouse→stocker, reconcile the existing depot, reuse-before-buy per role (sp-urpxy)".

### Task 6: Daemon wiring

**Files:** Modify `internal/adapters/grpc/contract_scaler_ports.go` (`WireContractScaler`/setters) + wherever the scaler ports are injected at boot (`cmd/spacetraders-daemon/main.go` scaler registration).

**Interfaces:** construct the `DepotElementCounter` + `DepotGrower` over the same `depotstore.Store` (`depotStoreFor(playerID)` — see `container_ops_depot.go:51`) + the `DaemonServer` launch verbs + the ship repo, and call `SetDepotElementCounter`/`SetDepotGrower` on the handler. Nil-safe (unset ⇒ delivery-only).

- [ ] **Step 1:** Failing wiring test (or a boot smoke test) asserting the handler has non-nil counter+grower after wiring.
- [ ] **Step 2–4:** Wire; run boot/build/tests green.
- [ ] **Step 5:** Commit `--no-verify`: "feat(contractscaler): wire depot counter + grower at boot (sp-urpxy)".

---

## Verification (end-to-end)
- `cd gobot && go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race ./...` — all green in the worktree.
- Land via captain-gate → main; verify merge SHA + `git diff --numstat`.
- Deploy (`make restart-daemon`); raise `contract_fleet_max_hulls` past the delivery count and confirm the scaler adds only the plan-short warehouse/stocker (no duplicate of TORWIND-15/11), re-solves far-source goods, reuses idle hulls before buying, 200k cushion holds; ceiling=2 first confirms byte-identical.

## Self-review notes
- Spec coverage: role-aware ramp (T5), reuse-before-buy per role (T5), depot-as-truth count/no-dup (T3+T5), supported_goods ownership (T2+T4), default-off (T5b), guards (Global + T5d). ✓
- Type consistency: `RoleTargets`→(d,w,s); `WarehouseCount/StockerCount`; `GrowWarehouse/GrowStocker(DepotGrowOrder)`; `EraRoles.FarSourceGoods`. Used consistently T1→T6.
- Open build-time item (flagged, not a placeholder): confirm the `supportedGoods` seam into `launchDepotWarehouse` (Task 4 Step 3) — thread it through or add the param; if the launch path can't accept it cleanly, set it on the persisted warehouse config via the store and escalate.
