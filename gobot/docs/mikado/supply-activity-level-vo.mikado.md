# Mikado: supply-activity-level-vo (L4)

**Goal (business value):** Single source of truth for the supply/activity level
enums. Extract `SupplyLevel` and `ActivityLevel` as string-backed value objects in
the leaf package `internal/domain/shared` (owning `validSupplyValues` + `Order()`),
repoint `domain/manufacturing.SupplyLevel` and `domain/market.ActivityLevel` onto
the shared type, and remove the odd `trading -> manufacturing` import in
`manufacturing_opportunity.go` (whose sole use of manufacturing is
`SupplyLevel.Order()`). End-state: no domain package except `shared` DEFINES a
supply/activity level type, and the `trading -> manufacturing` import is gone.
This kills the cross-package enum drift that produced the sp-9mkf supply-semantics
bug class.

**Status:** achieved
**START (baseline SHA on refactor/sp-1z4q-rpp):** ddaff98d7f0fe981af7c2eada3b19c95391b41cb
**Affected packages (touched):** `internal/domain/shared`, `internal/domain/manufacturing`, `internal/domain/market`, `internal/domain/trading`

## Baseline facts (pre-exploration)

- `manufacturing/supply_level.go` defines `type SupplyLevel string` + 5 consts +
  `purchaseMultipliers` map + `DefaultPurchaseMultiplier` const + 8 methods
  (`PurchaseMultiplier`, `IsFavorableForCollection`, `IsSaturated`, `AllowsPurchase`,
  `Order`, `ParseSupplyLevel`, `String`, `CalculateSupplyAwareLimit`). No imports (pure VO).
- `market/activity_level.go` defines `type ActivityLevel string` + 4 consts + 3
  methods (`BuyerActivityScore`, `SellerActivityScore`, `String`). No imports (pure VO).
- `market/trade_good.go` defines `var validSupplyValues` (used in `trade_good.go` and
  `market_price_history.go`) alongside `validActivityValues` (out of goal scope; kept in market).
- `shared` is a clean LEAF: imports no sibling domain package -> moving pure VOs in
  introduces zero cycle risk. `market` already imports `shared`; `manufacturing` does not yet.
- `trading/manufacturing_opportunity.go` is the ONLY trading file importing
  `manufacturing`; its sole use is line 177 `manufacturing.SupplyLevel(o.supply).Order()`.
  It already imports `shared` (for `Waypoint`).
- No domain `_test.go` file references either type directly (Mandate 2 holds -- tested
  indirectly), so consolidation touches no domain unit test.

## Blast-radius discovery (why type-alias, not full removal)

`manufacturing.SupplyLevel*` (type/consts/`ParseSupplyLevel`) is referenced in ~10
files OUTSIDE the 6 domain packages; `market.ActivityLevel*` in ~3 more
(`adapters/metrics`, `adapters/cli`, `adapters/persistence`,
`application/manufacturing/services`). Deleting the origin declarations outright
would force a repo-wide `manufacturing.` -> `shared.` / `market.` -> `shared.`
sweep across ~13 non-domain files (>12 nodes, well beyond the assessed "6 domain
packages" bound). The behavior-preserving, bounded move is the standard Go
consolidation pattern: DEFINE the type once in `shared`, and leave transparent
`type X = shared.X` ALIASES + re-exported consts in the origin packages. External
callers compile and behave identically (an alias is the same type); only `shared`
DEFINES the type, so drift is impossible -- the goal's end-state.

## Tree

- [x] GOAL: no domain pkg except `shared` DEFINES a supply/activity type; `trading -> manufacturing` import gone
    - [x] Leaf 1: Move the `SupplyLevel` VO (type + consts + `purchaseMultipliers` + `DefaultPurchaseMultiplier` + all methods + `ParseSupplyLevel`) into new `shared/supply_level.go`; replace `manufacturing/supply_level.go` with `type SupplyLevel = shared.SupplyLevel` alias + re-exported consts/`ParseSupplyLevel`/`DefaultPurchaseMultiplier`. (factory_state.go unqualified `SupplyLevel(...)` + all ~10 external callers ride the alias unchanged.)
    - [x] Leaf 2: Move the `ActivityLevel` VO (type + consts + 3 methods) into new `shared/activity_level.go`; replace `market/activity_level.go` with `type ActivityLevel = shared.ActivityLevel` alias + re-exported consts. (~3 external callers ride the alias unchanged.)
    - [x] Leaf 3: Move supply-value validation into `shared` (`shared.IsValidSupply(string) bool` over a `validSupplyValues` set); repoint market's two `validSupplyValues[*supply]` checks (`trade_good.go`, `market_price_history.go`) and delete market's local `validSupplyValues`. (`validActivityValues` stays in market -- out of goal scope.)
    - [x] Leaf 4 (GOAL node): Repoint `trading/manufacturing_opportunity.go` line 177 to `shared.SupplyLevel(o.supply).Order()` and drop the now-unused `manufacturing` import. (Behavior-identical: `manufacturing.SupplyLevel` was already a transparent alias of `shared.SupplyLevel` — same conversion, same `.Order()`. `manufacturing.` had exactly one use in the whole `trading` package and no `trading` test referenced it.)

## PARKED sub-goal: struct-field type repointing (out of the 6-package bound)

The goal's setup sentence also mentions repointing the raw `string` fields
(`goods.SupplyChainNode.SupplyLevel/.MarketActivity`,
`shipyard.ShipTypeAvailability.Supply`,
`trading.ArbitrageLane.SourceSupply/.DestActivity` +
`trading.GoodListing.Supply/.Activity`) onto the VO type. These are EXPORTED struct
fields written/read as `string` OUTSIDE the 6 domain packages -- e.g.
`application/.../supply_chain_resolver.go` (`node.SupplyLevel = marketData.Supply`),
`adapters/cli/tree_formatter.go` (appends `node.SupplyLevel` into a `[]string`),
`adapters/persistence/shipyard_inventory_repository.go` + `application/ship/shipyard_scanner.go`
(build `ShipTypeAvailability{Supply: ...}`), plus ~15 test files. Changing the field
types forces conversions at every one of those sites -> blast radius across
`adapters/cli`, `adapters/persistence`, `application/manufacturing`,
`application/ship`, `application/scouting`, `application/shipyard`, far beyond the
assessed 6-domain-package bound. Not required by the end-state criteria (the fields
are `string`, not a declared level TYPE), so PARKED as a follow-up. Empirically
confirmed: see Exploration log.

## Completion

GOAL achieved. All four tree leaves ticked. End-state criteria both hold:

1. **`trading -> manufacturing` import gone.** `manufacturing_opportunity.go` no longer
   imports `internal/domain/manufacturing`; its lone use (`SupplyLevel(o.supply).Order()`
   on the scoring line) now goes through `shared.SupplyLevel`. `manufacturing_opportunity.go`
   was the ONLY file in the `trading` package importing `manufacturing`, and that import
   had exactly one call site, so the odd domain->domain dependency is fully removed.
2. **Only `shared` DEFINES a supply/activity level type.** `manufacturing/supply_level.go`
   and `market/activity_level.go` hold transparent `type X = shared.X` aliases (+ re-exported
   consts) — identical types, drift impossible. `shared` is the single source of truth for the
   SCARCE..ABUNDANT / WEAK..RESTRICTED vocabularies, their scoring/ordering rules, and
   `validSupplyValues` (via `shared.IsValidSupply`). This kills the sp-9mkf cross-package
   enum-drift bug class.

**Gates:** leaf gate (`go build ./...` + `go vet` + `-race` tests over the 6 domain pkgs) green;
FULL gate `go test -race -count=1 ./...` -> exit 0, 0 failures, 107 packages. Behavior-preserving:
the one changed line swapped an alias for its definition target (byte-for-byte equivalent types),
no logic, no strings, no field types, no wire/DB/CLI surface touched.

## Exploration log

**Field-repointing experiment (re-verified this session, expanded permissions).** The goal's
setup prose also mentions repointing the raw `string` fields onto the VO type. I re-measured the
blast radius rather than trusting the earlier estimate: repointing
`goods.SupplyChainNode.SupplyLevel/.MarketActivity`, `shipyard.ShipTypeAvailability.Supply`,
`trading.ArbitrageLane.SourceSupply/.DestActivity` (+ `.Supply/.Activity`) from `string` to
`shared.SupplyLevel/.ActivityLevel` forces a `string(...)`/VO conversion at every read/write site.
Grep of production `.go` outside the 6 domain packages: **23 external files**, concentrated in
`application/manufacturing/services` (9), `adapters/cli` (3), `adapters/metrics` (2),
`application/gas/queries` (1), `adapters/persistence` (1) — plus test files. Fully eliminating the
`manufacturing`/`market` aliases (rather than keeping them) is a separate but equally wide sweep:
**57** `manufacturing.SupplyLevel`/`market.ActivityLevel` references live outside the 6 domain pkgs.
Both are far beyond the ~12-node Mikado bound and the assessed "6 domain packages" risk envelope, so
both stay PARKED. The fields are `string`, not a declared level TYPE, so neither is required by the
end-state criteria above — they are a genuine follow-up (candidate its own Mikado goal), not a gap in
this one. NOTE: these fields carry no gorm/protobuf tags (verified pure in-memory VOs), so the
repointing IS behavior-preserving in principle — the block is blast radius, not semantics.
