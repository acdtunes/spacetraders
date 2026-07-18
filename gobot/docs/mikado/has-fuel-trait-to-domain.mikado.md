# Mikado: has-fuel-trait-to-domain (L4)

**Goal (business value):** Single source of truth for which waypoint traits grant
on-site fuel. Introduce a trait->fuel predicate in `internal/domain/shared` that
owns the `MARKETPLACE | FUEL_STATION` vocabulary, and repoint the two api-adapter
sites to call it. End-state: the fuel-rule string literals live only under
`internal/domain/shared`, with zero copies in `internal/adapters/api`.

**Status:** achieved
**START (baseline SHA on refactor/sp-1z4q-rpp):** c3cf12eb8c3d658ec0bc2c4061dcc33d725c1c1f
**Affected packages:** `internal/adapters/api`, `internal/domain/shared`

## Baseline facts (pre-exploration)

- Predicate lives once in the adapter: `waypoint_converter.go` defines
  `traitGrantsFuel(trait string) bool` plus consts `traitMarketplace="MARKETPLACE"`,
  `traitFuelStation="FUEL_STATION"`.
- 3 call sites, ALL inside package `api`:
  - `waypoint_converter.go` `extractHasFuel` strategy 2 (`[]string`)
  - `waypoint_converter.go` `extractHasFuel` strategy 3 (`[]interface{}`)
  - `graph_builder.go` has_fuel loop
- Both files already import `internal/domain/shared`.
- No test file references `traitGrantsFuel` / the consts; the api adapter has no
  existing fuel unit test. The domain `shared` package already unit-tests pure
  functions directly (fuel_test.go, flight_mode_selector_test.go), so a direct
  characterization test for the extracted predicate matches package convention.
- The `MARKETPLACE` literal also appears in `shared/waypoint.go` `IsMarketplace()`
  (a DIFFERENT rule, already in the domain) and in many other packages for
  market/scouting rules (out of scope: not the fuel rule).

## Tree

- [x] GOAL: fuel-rule vocabulary owned only by `internal/domain/shared`; both api sites call it
    - [x] Leaf 1: Add `shared.TraitGrantsFuel(string) bool` + `shared.TraitsGrantFuel([]string) bool` to `domain/shared/waypoint.go` (owns MARKETPLACE|FUEL_STATION), with a parametrized characterization test
    - [x] Leaf 2: Repoint `graph_builder.go` has_fuel loop -> `shared.TraitsGrantFuel(traits)`
    - [x] Leaf 3: Repoint `waypoint_converter.go` `extractHasFuel` (strategy 2 -> `shared.TraitsGrantFuel`, strategy 3 -> `shared.TraitGrantsFuel`) AND delete the adapter-local `traitGrantsFuel` + `traitMarketplace`/`traitFuelStation` consts

## Completion

Goal ACHIEVED. End-state verified:
- `grep '"MARKETPLACE"\|"FUEL_STATION"' internal/adapters/api/` -> zero hits (bare
  words too, including comments: none remain in the api package).
- Fuel-rule literals now live only in `internal/domain/shared/waypoint.go`.
- FULL gate green: `go test -race -count=1 ./...` -> exit 0 (all packages ok / no-test-files).

Behavior preservation: the boolean at every site is unchanged. `graph_builder` and
`extractHasFuel` strategy 2 use `shared.TraitsGrantFuel` (any-match over the same
two literals); strategy 3 keeps its per-element `[]interface{}` type assertion and
calls `shared.TraitGrantsFuel`. No proto/SQL/CLI/config/log-string changes.

## Exploration log

**EXPLORE (naive full attempt, then revert):** Applied all three changes at once
(add domain predicate + repoint both api sites + delete adapter-local predicate),
then ran the targeted gate:

- `go build ./...` -> exit 0
- `go vet ./...` -> exit 0
- `go test -race -count=1 -p 4 ./internal/adapters/api/... ./internal/domain/shared/...` -> exit 0 (both packages `ok`)

**Result: NO hidden prerequisites discovered.** The dependency graph is exactly the
three leaves above, all trivially safe:
- `shared` has no pre-existing `traitMarketplace`/`traitFuelStation`/`TraitGrantsFuel`
  identifiers, so adding them introduces no collision.
- All 3 call sites are inside package `api`; deleting the adapter-local
  `traitGrantsFuel` after both repoints leaves no dangling reference.
- Both api files already import `internal/domain/shared`; no import churn.
- No existing test references the adapter-local predicate or its consts.

Reverted the code (kept this doc), then executed the leaves bottom-up as gated
commits below. Boolean output is byte-identical at every site (same OR over the
same two literals; slice-any-match preserved).
