# Refactoring Lane Report: domain-economy (sp-1z4q)

Scope: `internal/domain/{trading, market, goods, ledger, contract, manufacturing, shipyard, outfitting, gas}` (~10.5k LOC).
Pass: RPP L1-L3, behavior-preserving only. Gate per commit: `go build ./... && go vet ./... && go test -race -count=1 <lane pkgs>` — green on every commit.

## Overall assessment

This lane is in unusually good shape. Recent code (trading tour/lane/impact model, depot, outfitting, good-gating overrides) is exemplary: small pure functions, guard clauses, heavy WHY/bead documentation. Smells clustered in the older aggregate files (manufacturing task, ledger transaction, gas operation, market entities): dead exported symbols, redundant WHAT-comments, one god-file.

Pristine packages (no or trivial findings, left untouched): **outfitting**, **shipyard**, **contract/depot** (only the role-switch dedup), **ledger** (beyond transaction.go), trading files `trade_circuit.go`, `price_impact.go`, `lane_cooldown_ledger.go`, `tour_telemetry.go`.

## L1 — Readability (commit `refactor(L1)`)

Smells found: Dead Code (7 exported symbols, all whole-repo grep-verified zero references), redundant WHAT-comments, one shadowed builtin, one factually wrong comment line.

Applied:
- Deleted `manufacturing/task_readiness_spec.go` — `TaskReadinessSpecification` was an empty struct + constructor, never referenced (Lazy Class).
- Deleted `ledger.Transaction.GetCategory()` — exact duplicate of `Category()`, zero call sites.
- Deleted `goods.ValidateSupplyChain` (unused) and `goods.UnknownGoodError` (its only user), `goods.ErrProductionTimeout`, `goods.ErrMarketNotFound` (never constructed or matched).
- Deleted `market.ErrMarketNotFound`, `market.ErrStaleMarketData`, `market.ErrInvalidTradeGood` vars (unused; the other 7 market error vars are used and kept).
- Renamed shadowed-builtin local `copy` -> `metadataCopy` in `ledger.Transaction.Metadata()`.
- Removed redundant WHAT-comments that literally restated the next line in `ledger/transaction.go`, `market/market_price_history.go`, `market/trade_good.go`, `contract/contract_profitability_service.go`, `gas/gas_operation.go`. All WHY/bead comments preserved verbatim.
- `trading/manufacturing_opportunity.go`: removed the false comment line "Check both: no children AND root is BUY method" — the code checks only root `AcquisitionMethod == AcquisitionBuy`; WHY lines kept.

Deliberately NOT extracted: the scoring weights in `calculateScore` (0.40/0.30/0.20/0.10) — each appears once with adjacent business documentation; constant extraction would add indirection without clarity.

## L2 — Complexity (commit `refactor(L2)`)

Smells found: Duplicate Code (2 clusters), one 3-level nested scan.

Applied:
- `trading/tour_rate.go`: extracted `groupLegs(rows, key)` (fold loop previously copy-pasted 3x across `ComputeFleetTourRate`, `tourRateDeclining`, `MedianTourRate`) and `computableRates(groups)` (rate-collection loop, 2x). Mean/min/median are order-insensitive so map-iteration neutrality is unchanged.
- `contract/depot/mutation.go`: extracted `roleTarget(role, ...)` — the identical role->slice switch appeared in `WithElementAdded/Removed/Placed`; error text unchanged.
- `contract/fleet_assigner.go`: extracted `nearestTargetWithCapacity` from `AssignShipsToTargetsWithHint`'s nested loop (3 -> 2 nesting levels).

Considered and skipped: `gas.Status()`/`FromData` status switches (inverse mappings, not duplicates); `goods_factory` transition methods (similar shape, distinct guards — a template would obscure).

## L3 — Responsibilities (commit `refactor(L3)`)

Smells found: one Large Class/god-file.

Applied:
- Split `manufacturing/task.go` (808 lines) within-package into `task.go` (520: types, priority/aging constants, constructors, accessors, routing/query helpers, `ReconstituteTask`) and `task_transitions.go` (296: the full state machine `MarkReady`..`Cancel`, re-sourcing `UpdateSourceMarket`/`ClearSourceForResupply`/`ParkForResupply`, phase tracking). Pure move via line-range extraction; every bead comment (sp-hs2j, sp-izh8, sp-r900) byte-identical.

Considered and skipped: `market/ports.go` (interfaces + DTOs cohabit, 166 lines — acceptable); `trading/arbitrage_lane.go` (ranking + hold-fit are documented as one concern); `manufacturing/pipeline.go` (534 lines but cohesive, already uses predicate helpers).

## Counts

| Level | Files touched | Net LOC |
|---|---|---|
| L1 | 10 | -110 |
| L2 | 3 | -10 |
| L3 | 2 (1 new) | +8 (split overhead) |

3 commits, 15 unique `.go` files, all gates green.

## Suspected bugs (report only — NOT fixed)

1. **`goods.ErrInsufficientCargo` can never match.** `internal/application/manufacturing/commands/run_factory_coordinator.go:1414` declares `var cargoErr *goods.ErrInsufficientCargo` as an `errors.As` target, but nothing anywhere in gobot constructs that type — the recovery branch behind it is unreachable. Either wire the error into whatever should raise it or drop the branch.
2. **`goods.ValidateSupplyChain` had an unreachable error branch** (pre-deletion, for the record): `IsRawMaterial(good)` is defined as "not in ExportToImportMap", so after its early-return the map lookup could never miss — the function always returned nil, never `UnknownGoodError`. It was also fully unused; deleted as dead code. If validation is ever wanted, it must check against a known-goods list, not the same map.
3. **Minor**: `contract.FleetAssigner.IsRebalancingNeeded` and `AssignShipsToTargets*` return an `error` that is always nil (API slack, callers must still handle it).

## L4-L6 candidates (architectural, not touched)

1. **Shared Supply/Activity value objects (L4)** — `manufacturing.SupplyLevel` (with `Order()`/parse), `market.validSupplyValues`/`ActivityLevel`, and raw supply/activity strings in `trading.GoodListing`, `goods.SupplyChainNode`, `shipyard.ShipTypeAvailability` are parallel encodings of the same domain concept. `trading/manufacturing_opportunity.go` even imports `domain/manufacturing` solely for `SupplyLevel.Order()`. Extract shared value objects into a leaf package (e.g. `domain/shared`) to kill cross-package drift and the odd domain->domain dependency.
2. **Shared TradeType (L4)** — `trading.tradeTypeExport` + `GoodListing.TradeType string` deliberately duplicate `market.TradeType` to keep ranking pure (documented in-file, sp-9mkf). A leaf trade-type package would let both share one type without coupling the ranker to the market aggregate.
3. **Lifecycle status projection (L5)** — `gas.Operation` and `goods.GoodsFactory` both wrap `shared.LifecycleStateMachine` yet hand-roll their own status enums and mapping switches; `goods` keeps a parallel `status` field that can drift from the lifecycle machine. A shared status projection (or generic mapping) on the lifecycle machine removes the parallel-enum drift risk in two money-bearing aggregates.
