# Gate Delivery Fleet (Phase 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the gate a delivery fleet: hulls tagged by ROLE, a supply-anchored buy/pause policy with live-tunable hysteresis, a greedy max-cargo mixed fill, and a buy pinned to the terminal factory that phase 1's `TerminalFactory` resolves — with both policies recording every decision, so a paused fleet and an idle one never look the same again.

**Architecture:** A new pure-domain package `internal/domain/manufacturing/gate` (the parallel of `internal/domain/contract/depot`) holds the three policy objects — `Role` (the fleet-tag vocabulary and the D/F/F/D purchase order), `BuyPolicy` (hysteresis + the `Decision` record), and `PlanFill` (the greedy mixed fill + per-material skip reasons). None of them does I/O, so all three are exhaustively testable without a fixture. The construction drain (`RunConstructionCoordinatorHandler`) gains one optional collaborator, `SetGateDelivery`, wiring phase 1's `GateTopology` and a new pinned-source buy on `ProductionExecutor`. Delivery hulls are discovered across every gate fleet tag and claimed **under the hull's own tag**, because `ClaimShip` authorizes a dedicated hull only when `tag == operation`. The two floors persist on the construction pipeline row (migration `051`) and are re-read **every leg**, which is what makes them live.

**Tech Stack:** Go 1.x, standard `testing` + `testify/require` where the surrounding package already uses it. GORM/Postgres for the pipeline row. protobuf/gRPC for the new RPC. No new dependencies.

## Global Constraints

Copied verbatim from `docs/superpowers/specs/2026-08-03-gate-construction-two-fleet-design.md` and `docs/superpowers/plans/2026-08-03-gate-delivery-fleet-phase2-DESIGN-NOTES.md`:

- **No feature flag, no default-off, no arm seam. Ships ARMED.** The buy-floor/resume-floor knobs are **tunables, not feature flags** — "They ship armed with the defaults above; the knob adjusts a value in a path that always runs. Nothing here is default-off." Defaults: **buy floor MODERATE, resume at HIGH**.
- **Money guards untouched.** "The 50k `common.ImmutableReserveFloor` applies to **both** fleets, unchanged. RULINGS #4: money guards fail closed and are never weakened. This is a simplification of *control flow*, not of *spend safety*. No new floor, no new knob, no config seam (RULINGS #5)." Do not read, move, or weaken it.
- **ZERO waypoint-symbol literals in production code.** "No system or waypoint symbol may be hardcoded. Every location is resolved at runtime by role, from market import/export data." Goods names ARE invariant and fine (FAB_MATS, ADVANCED_CIRCUITRY, and recipe inputs may be named directly). Test fixtures may use synthetic waypoints.
- **Both policies must record their decisions.** "Two policies that decide silently rebuild exactly the problem this design exists to solve — a paused delivery fleet and an idle one still look identical." Delivery pause records: factory, good, observed supply, resume condition. Fill outcome records: per trip — capacity, what was loaded, what was skipped and why. **Observability ships INSIDE each task, never as a final task.**
- **Supply ordering is SCARCE < LIMITED < MODERATE < HIGH < ABUNDANT** (`domain/shared/supply_level.go`). Buy while supply is **at or above** the buy floor; pause **below** it; resume only once supply recovers to the resume floor.
- **`AssignFleet` is the single write path** for `dedicated_fleet` (`adapters/api/ship_repository_claims.go:432`). "All role assignment and reassignment goes through it — never through a general save."
- **Purchase order is D / F / F / D.** "The interleaving matters at every partial-purchase state... First hull is delivery so any already-accumulated stock starts moving immediately; stopping after two leaves one of each rather than two of the same."
- **Delivery is "paused" only when EVERY gate material is paused**, never when any one is.
- **`gateMaxTranchesPerStop` is 4.** There is no stock field on the market — an independent DB probe confirmed the market exposes a supply LEVEL and a per-transaction `trade_volume` and **no stock count**. Trip availability is `trade_volume × gateMaxTranchesPerStop` so one stop cannot monopolise a mixed trip. There is no `available_supply` field; do not reference one.
- **Skip-reason precedence is `hold_full → bill_satisfied → paused → no_supply`** (RULINGS, adjudication #1). A met bill is never reported as "paused".
- **Pause state is in-memory per process, not persisted** (adjudication #2). A restart re-derives: an unpaused start re-pauses on the first low read, costing one tick and never a spend.
- **Do NOT use `GateTopology.IsRaw` or `Inputs`** — bead `sp-4irrr` (P1): the recipe map is cyclic and `!hasRecipe` returns false for every real raw material. Phase 2 buys terminal outputs; it does not walk the recipe graph.
- **Legacy `manufacturing`-tagged hulls carry no role** (adjudication #4); leave them on the existing path. All three tags must still increment `GateWorkers` so the ramp still stops at `gateWorkerTarget` (4).
- Do not touch protected paths: `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.

**Test output must be filtered.** ~4550 tests across ~107 packages; a raw `go test ./...` will exhaust an agent's context. Always use the filtered forms given in each task:
`go test -race ./<pkg>/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40`
This shell is **zsh** — `PIPESTATUS` does not work; capture `$?` directly without piping when you need an exit code.

**Commits use `git commit --no-verify -- <pathspec>`** (a hook auto-stages `.beads/issues.jsonl`).

**Commit BEFORE every mutation probe.** `git checkout --` restores to HEAD and erases uncommitted work; a lane in a prior session lost a fix that way.

## File Structure

| File | Responsibility |
|---|---|
| `gobot/internal/domain/manufacturing/gate/role.go` (create) | `Role` vocabulary, gate fleet tags, `IsGateFleetTag`, the pure D/F/F/D `NextRole` |
| `gobot/internal/domain/manufacturing/gate/role_test.go` (create) | Role + purchase-order tests, incl. the mix at every partial N |
| `gobot/internal/domain/manufacturing/gate/buy_policy.go` (create) | `BuyPolicy` hysteresis, the `Decision` observability record, `FleetPaused` |
| `gobot/internal/domain/manufacturing/gate/buy_policy_test.go` (create) | Hysteresis / chatter / EVERY-vs-ANY tests |
| `gobot/internal/domain/manufacturing/gate/fill.go` (create) | `PlanFill` greedy mixed fill, `trade_volume` tranching, skip reasons, trip log line |
| `gobot/internal/domain/manufacturing/gate/fill_test.go` (create) | Fill tests + the era-invariance source guard over the whole package |
| `gobot/internal/domain/manufacturing/pipeline.go` (modify) | Two new private fields for the delivery floors |
| `gobot/internal/domain/manufacturing/pipeline_construction.go` (modify) | `DeliveryBuyFloor` / `DeliveryResumeFloor` / `SetDeliveryFloors` |
| `gobot/internal/adapters/persistence/models_manufacturing.go` (modify) | `delivery_buy_floor` / `delivery_resume_floor` columns |
| `gobot/internal/adapters/persistence/manufacturing_pipeline_repository.go` (modify) | Map both floors in `pipelineToModel` / `modelToPipeline` |
| `gobot/migrations/051_add_construction_delivery_floors.up.sql` (create) | The two columns; **apply to production BEFORE the daemon deploys** |
| `gobot/migrations/051_add_construction_delivery_floors.down.sql` (create) | Rollback |
| `gobot/internal/adapters/persistence/manufacturing_pipeline_delivery_floors_test.go` (create) | Restart-survival round trip |
| `gobot/pkg/proto/daemon/daemon.proto` (modify) | `ConstructionDeliveryFloors` RPC + request/response messages |
| `gobot/internal/adapters/grpc/container_ops_construction.go` (modify) | `MutateConstructionDeliveryFloors` — the daemon single writer |
| `gobot/internal/adapters/grpc/daemon_service_construction.go` (modify) | RPC handler |
| `gobot/internal/adapters/cli/daemon_client_construction.go` (modify) | Client wrapper |
| `gobot/internal/adapters/cli/construction_override.go` (modify) | `--buy-floor` / `--resume-floor` flags, validation, two-RPC dispatch |
| `gobot/internal/application/manufacturing/services/production_executor.go` (modify) | Extract the tranche loop into `fillFromSource` so both buys share it |
| `gobot/internal/application/manufacturing/services/production_executor_gate_buy.go` (create) | `BuyAtTerminalFactory` — pinned-source buy, every money guard unchanged |
| `gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go` (create) | Pinned-source + guard-still-called tests |
| `gobot/internal/adapters/grpc/bootstrap_ports_gate.go` (modify) | Role-tag the hull at purchase; widen the surplus/trade re-tag guards |
| `gobot/internal/adapters/grpc/bootstrap_ports_observe.go` (modify) | Count role-tagged hulls as `GateWorkers` |
| `gobot/internal/adapters/grpc/bootstrap_ports_gate_role_test.go` (create) | Purchase-order + widened-guard + observer tests |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go` (create) | The delivery leg: decide → record → fill → buy → deliver |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go` (modify) | Discover every gate tag; carry the per-hull claim identity on the lot |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator.go` (modify) | `SetGateDelivery`; claim under the hull's OWN tag |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go` (create) | Claim-identity, leg, and per-leg floor-read tests |
| `gobot/cmd/spacetraders-daemon/main.go` (modify) | Wire `SetGateDelivery` alongside the existing setters |

---

### Task 1: `gate.Role` vocabulary, fleet tags, and the D/F/F/D purchase order

Pure functions over counts — no I/O — so the purchase order the spec pins at every partial N is testable exhaustively before any hull is bought. `IsGateFleetTag` deliberately covers the LEGACY `manufacturing` tag too: adjudication #4 leaves legacy hulls role-less on the existing path, but all three tags must still increment `GateWorkers` or the ramp would run past `gateWorkerTarget` (4).

**Files:**
- Create: `gobot/internal/domain/manufacturing/gate/role.go`
- Test: `gobot/internal/domain/manufacturing/gate/role_test.go`

**Interfaces:**
- Consumes: nothing. Pure.
- Produces: `type Role int`; `RoleDelivery`, `RoleFactory`; `(Role) String() string`; `(Role) FleetTag() string`; `ParseFleetTag(tag string) (Role, bool)`; `IsGateFleetTag(tag string) bool`; `RoleFleetTags() []string`; `NextRole(delivery, factory int) Role`; consts `LegacyFleetTag`, `DeliveryFleetTag`, `FactoryFleetTag`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/domain/manufacturing/gate/role_test.go`:

```go
package gate

import "testing"

// The GATE phase buys 4 hulls tagged D/F/F/D. The interleaving matters at every
// PARTIAL-purchase state, which is the state that actually occurs when treasury is
// tight: stopping after two must leave one of each, not two of the same.
func TestNextRole_PurchaseOrderIsDeliveryFactoryFactoryDelivery(t *testing.T) {
	want := []Role{RoleDelivery, RoleFactory, RoleFactory, RoleDelivery}

	delivery, factory := 0, 0
	for i, expected := range want {
		got := NextRole(delivery, factory)
		if got != expected {
			t.Fatalf("hull %d: NextRole(%d, %d) = %v, want %v", i+1, delivery, factory, got, expected)
		}
		if got == RoleDelivery {
			delivery++
		} else {
			factory++
		}
	}
	if delivery != 2 || factory != 2 {
		t.Fatalf("after 4 hulls the mix is %dD/%dF, want 2D/2F", delivery, factory)
	}
}

// The spec pins the mix at every N, not just at 4: 1->1D, 2->1D/1F, 3->1D/2F, 4->2D/2F.
func TestNextRole_MixIsCorrectAtEveryPartialPurchase(t *testing.T) {
	cases := []struct{ n, wantDelivery, wantFactory int }{
		{1, 1, 0},
		{2, 1, 1},
		{3, 1, 2},
		{4, 2, 2},
	}
	for _, tc := range cases {
		delivery, factory := 0, 0
		for i := 0; i < tc.n; i++ {
			if NextRole(delivery, factory) == RoleDelivery {
				delivery++
			} else {
				factory++
			}
		}
		if delivery != tc.wantDelivery || factory != tc.wantFactory {
			t.Fatalf("after %d hull(s) the mix is %dD/%dF, want %dD/%dF",
				tc.n, delivery, factory, tc.wantDelivery, tc.wantFactory)
		}
	}
}

// NextRole must be TOTAL: a count past the 4-hull target (a miscount, a manual
// re-tag) must still answer, and must keep the 2:2 balance rather than degenerating.
func TestNextRole_IsTotalPastTheFourHullTarget(t *testing.T) {
	delivery, factory := 2, 2
	for i := 0; i < 4; i++ {
		if NextRole(delivery, factory) == RoleDelivery {
			delivery++
		} else {
			factory++
		}
	}
	if delivery != 4 || factory != 4 {
		t.Fatalf("after 8 hulls the mix is %dD/%dF, want 4D/4F — the order must keep cycling in balance", delivery, factory)
	}
}

// The tag is the dedicated_fleet column value AND the ClaimShip operation string.
// They must be the same value, so round-tripping cannot drift.
func TestFleetTag_RoundTripsThroughParseFleetTag(t *testing.T) {
	for _, role := range []Role{RoleDelivery, RoleFactory} {
		tag := role.FleetTag()
		if tag == "" {
			t.Fatalf("%v has an empty fleet tag", role)
		}
		got, ok := ParseFleetTag(tag)
		if !ok || got != role {
			t.Fatalf("ParseFleetTag(%q) = (%v, %v), want (%v, true)", tag, got, ok, role)
		}
	}
	if RoleDelivery.FleetTag() == RoleFactory.FleetTag() {
		t.Fatal("the two roles share one fleet tag — ClaimShip would authorize either hull for either drain")
	}
}

// The LEGACY "manufacturing" tag carries NO role (phase 3 re-roles live hulls), but it
// IS a gate fleet tag: the observer counts all three, which is what keeps the ramp
// stopping at gateWorkerTarget instead of over-buying.
func TestIsGateFleetTag_CoversAllThreeTagsButOnlyTwoParseToARole(t *testing.T) {
	for _, tag := range []string{LegacyFleetTag, DeliveryFleetTag, FactoryFleetTag} {
		if !IsGateFleetTag(tag) {
			t.Fatalf("IsGateFleetTag(%q) = false, want true — the observer must count it as a gate worker", tag)
		}
	}
	if _, ok := ParseFleetTag(LegacyFleetTag); ok {
		t.Fatalf("ParseFleetTag(%q) reported a role; a legacy hull carries none until phase 3 re-roles it", LegacyFleetTag)
	}
	for _, tag := range []string{"", "contract", "trade", "purchasing", "warehouse"} {
		if IsGateFleetTag(tag) {
			t.Fatalf("IsGateFleetTag(%q) = true — a foreign or undedicated hull must never read as a gate hull", tag)
		}
	}
}

// RoleFleetTags is what discovery iterates. It must list exactly the ROLE tags and
// must not include the legacy tag, which the drain already discovers by default.
func TestRoleFleetTags_ListsExactlyTheTwoRoleTags(t *testing.T) {
	tags := RoleFleetTags()
	if len(tags) != 2 {
		t.Fatalf("RoleFleetTags() = %v, want exactly the two role tags", tags)
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag == LegacyFleetTag {
			t.Fatalf("RoleFleetTags() includes the legacy tag %q, which the drain already discovers by default", tag)
		}
		if seen[tag] {
			t.Fatalf("RoleFleetTags() = %v contains a duplicate; discovery would claim-check the same pool twice", tags)
		}
		seen[tag] = true
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/domain/manufacturing/gate/... 2>&1 | tail -20`
Expected: FAIL — the package does not exist yet (`no Go files in .../gate`, or once the file is stubbed, `undefined: NextRole`).

- [ ] **Step 3: Write minimal implementation**

Create `gobot/internal/domain/manufacturing/gate/role.go`:

```go
// Package gate holds the gate-construction delivery fleet's POLICY objects: the role
// vocabulary, the supply-anchored buy/pause rule, and the greedy mixed fill.
//
// Everything here is pure. No I/O, no clock, no repository — a policy that cannot be
// exercised without a fixture is a policy that gets tested by running the fleet, which
// is exactly the opacity this design exists to remove. It is the gate-side parallel of
// internal/domain/contract/depot: one container, several roles.
package gate

// Role names one of the two gate-construction worker roles. One container type, two
// roles, assigned in real time via the ship's dedicated_fleet tag — the depot.Role
// precedent, not a second container type.
type Role int

const (
	// RoleDelivery buys terminal-factory OUTPUT and hauls it to the gate. It never fabricates.
	RoleDelivery Role = iota
	// RoleFactory feeds the production chain INTO the terminal factories. It never touches
	// their output. (Phase 3 gives this role its behaviour; phase 2 only tags for it.)
	RoleFactory
)

const (
	// LegacyFleetTag is the pre-role gate tag. Hulls carrying it have NO role and stay on
	// the existing path until phase 3's reallocation re-roles them. It is still a GATE tag:
	// the observer counts all three, which is what keeps the worker ramp stopping at its
	// target instead of ramping past a total that undercounts live hulls.
	LegacyFleetTag = "manufacturing"
	// DeliveryFleetTag and FactoryFleetTag are BOTH the dedicated_fleet column value AND the
	// ClaimShip operation string for their role. They must be one value each, because
	// ClaimShip authorizes a new claim only when the hull's tag EQUALS the operation: a hull
	// discovered by tag but claimed under a different identity is rejected at the DB and
	// silently never works.
	DeliveryFleetTag = "gate-delivery"
	FactoryFleetTag  = "gate-factory"
)

// roleTags is the single source of truth for the Role <-> tag mapping, so String,
// FleetTag and ParseFleetTag cannot drift apart.
var roleTags = map[Role]string{
	RoleDelivery: DeliveryFleetTag,
	RoleFactory:  FactoryFleetTag,
}

// purchaseOrder is the GATE phase's hull-buying order: delivery, factory, factory, delivery.
//
// The interleaving is the point, and it is load-bearing at every PARTIAL state — which is
// the state that actually occurs when treasury is tight. First hull is delivery so any
// already-accumulated factory stock starts moving immediately; stopping after two leaves
// one of each rather than two of the same.
var purchaseOrder = [...]Role{RoleDelivery, RoleFactory, RoleFactory, RoleDelivery}

// String is the stable, operator-facing name of the role (log lines, CLI output).
func (r Role) String() string {
	switch r {
	case RoleDelivery:
		return "delivery"
	case RoleFactory:
		return "factory"
	default:
		return "unknown"
	}
}

// FleetTag is the dedicated_fleet tag — and therefore the ClaimShip operation — for the role.
func (r Role) FleetTag() string { return roleTags[r] }

// ParseFleetTag maps a dedicated_fleet tag back to its Role. ok is false for the legacy
// tag, for a foreign fleet, and for the undedicated empty tag: only a ROLE-tagged hull
// has a role, and a caller must not infer one for anything else.
func ParseFleetTag(tag string) (Role, bool) {
	for role, t := range roleTags {
		if t == tag {
			return role, true
		}
	}
	return 0, false
}

// IsGateFleetTag reports whether tag belongs to the gate workforce at all — the two role
// tags PLUS the legacy one. This is the observer's count predicate and the re-tag guards'
// membership test. Deliberately broader than ParseFleetTag: a legacy hull has no role but
// is unmistakably a gate worker, and treating it as anything else would let the ramp
// over-buy against a total that ignores it.
func IsGateFleetTag(tag string) bool {
	if tag == LegacyFleetTag {
		return true
	}
	_, ok := ParseFleetTag(tag)
	return ok
}

// RoleFleetTags lists the ROLE tags, for discovery that must look up each pool separately.
// The legacy tag is deliberately absent: the drain already discovers it as its default
// identity, and listing it here would make discovery scan that pool twice.
func RoleFleetTags() []string {
	return []string{DeliveryFleetTag, FactoryFleetTag}
}

// NextRole is the role the NEXT gate hull should carry, derived purely from how many of
// each the fleet already holds. Deriving it from LIVE counts rather than from a stored
// cursor is what makes the order correct after a restart, a manual re-tag, or a hull loss:
// there is no cursor to get out of step with the fleet.
//
// TOTAL by construction — it cycles the 4-slot order — so a count past the target (a
// miscount, an operator re-tag) still answers, and still answers in balance.
func NextRole(delivery, factory int) Role {
	if delivery < 0 {
		delivery = 0
	}
	if factory < 0 {
		factory = 0
	}
	return purchaseOrder[(delivery+factory)%len(purchaseOrder)]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test -race ./internal/domain/manufacturing/gate/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40`
Expected: `ok` for the package, no `FAIL`.

- [ ] **Step 5: Commit (BEFORE the probe — `git checkout --` erases uncommitted work)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(gate): Role vocabulary, fleet tags and the D/F/F/D purchase order (phase 2)

Two roles in one container, assigned via dedicated_fleet -- the depot.Role
precedent. Each role tag is BOTH the column value and the ClaimShip operation,
because ClaimShip authorizes a new claim only when tag == operation.

NextRole derives the order from LIVE counts rather than a stored cursor, so a
restart or a manual re-tag cannot put the order out of step with the fleet. The
interleaving is load-bearing at every partial-purchase state: 1->1D, 2->1D/1F,
3->1D/2F, 4->2D/2F.

IsGateFleetTag deliberately covers the legacy 'manufacturing' tag too. Those
hulls carry no role until phase 3 re-roles them, but they are gate workers, and
a count that ignored them would let the ramp buy past its target." -- gobot/internal/domain/manufacturing/gate/role.go gobot/internal/domain/manufacturing/gate/role_test.go
```

- [ ] **Step 6: Mutation-probe the legacy-tag membership**

`IsGateFleetTag` covering the legacy tag is the guard that keeps the ramp from over-buying. Prove it is load-bearing. Run as ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && perl -0pi -e 's/\tif tag == LegacyFleetTag \{\n\t\treturn true\n\t\}\n//' internal/domain/manufacturing/gate/role.go && ! grep -q 'tag == LegacyFleetTag' internal/domain/manufacturing/gate/role.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestIsGateFleetTag' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/domain/manufacturing/gate/role.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestIsGateFleetTag_CoversAllThreeTagsButOnlyTwoParseToARole`, then `RESTORED`. A run that names **zero** tests means the guard is untested — stop and fix the test before continuing.

---

### Task 2: `gate.BuyPolicy` — supply-anchored hysteresis, the `Decision` record, and `FleetPaused`

Two thresholds, not one: "A single threshold chatters at the boundary: pause, one unit regenerates, resume, immediately deplete." The `Decision` record is the observability half and ships **in this task** — a policy that decides silently rebuilds the exact opacity this design exists to remove.

Pause state is in-memory per process (adjudication #2), following the worker-registry precedent. A restart re-derives it: an unpaused start re-pauses on the first low read, costing one tick and never a spend.

`shared.SupplyLevel` is the single definition of the SCARCE..ABUNDANT vocabulary; `manufacturing.SupplyLevel` is a type alias for it, so there is no enum to drift.

**Files:**
- Create: `gobot/internal/domain/manufacturing/gate/buy_policy.go`
- Test: `gobot/internal/domain/manufacturing/gate/buy_policy_test.go`

**Interfaces:**
- Consumes: `shared.SupplyLevel` and its `Order() int` from `internal/domain/shared/supply_level.go`.
- Produces: `DefaultBuyFloor`, `DefaultResumeFloor`; `type Decision struct{...}`; `(Decision) LogLine() string`; `NewBuyPolicy(buyFloor, resumeFloor shared.SupplyLevel) *BuyPolicy`; `(*BuyPolicy) Decide(good, factory string, supply shared.SupplyLevel) Decision`; `(*BuyPolicy) FleetPaused(goods []string) bool`; `(*BuyPolicy) Floors() (buy, resume shared.SupplyLevel)`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/domain/manufacturing/gate/buy_policy_test.go`:

```go
package gate

import (
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Buy while supply is AT OR ABOVE the buy floor; pause BELOW it.
func TestBuyPolicy_BuysAtOrAboveTheFloorAndPausesBelow(t *testing.T) {
	cases := []struct {
		supply  shared.SupplyLevel
		wantBuy bool
	}{
		{shared.SupplyLevelAbundant, true},
		{shared.SupplyLevelHigh, true},
		{shared.SupplyLevelModerate, true}, // AT the floor buys — "at or above"
		{shared.SupplyLevelLimited, false},
		{shared.SupplyLevelScarce, false},
	}
	for _, tc := range cases {
		p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
		got := p.Decide("FAB_MATS", "WP-FAB", tc.supply)
		if got.Buy != tc.wantBuy {
			t.Fatalf("supply %s: Buy = %v, want %v (buy floor MODERATE)", tc.supply, got.Buy, tc.wantBuy)
		}
		if got.Paused == got.Buy {
			t.Fatalf("supply %s: Buy=%v and Paused=%v — a decision must be exactly one of the two", tc.supply, got.Buy, got.Paused)
		}
	}
}

// HYSTERESIS. Once paused, recovering back to the BUY floor is NOT enough: supply
// must reach the RESUME floor. A single threshold chatters at the boundary — pause,
// one unit regenerates, resume, immediately deplete.
func TestBuyPolicy_ResumeRequiresOneLevelAboveTheBuyFloor(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)

	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelLimited); !d.Paused {
		t.Fatal("LIMITED is below the MODERATE buy floor and must pause")
	}
	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelModerate); d.Buy {
		t.Fatal("recovering only to the BUY floor must NOT resume — that is the chatter the resume floor exists to stop")
	}
	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelHigh); !d.Buy {
		t.Fatal("reaching the RESUME floor must resume buying")
	}
	if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelModerate); !d.Buy {
		t.Fatal("after resuming, MODERATE is at the buy floor and must keep buying — the hysteresis is one-directional")
	}
}

// Supply oscillating AT the boundary must not produce buy/pause chatter.
func TestBuyPolicy_OscillatingAtTheBoundaryDoesNotChatter(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelLimited) // enter the pause

	for i := 0; i < 10; i++ {
		if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelModerate); d.Buy {
			t.Fatalf("oscillation %d: resumed at the buy floor while paused — chatter", i)
		}
		if d := p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelLimited); d.Buy {
			t.Fatalf("oscillation %d: bought below the buy floor", i)
		}
	}
}

// Pause state is PER GOOD. One material pausing must not pause the other, which is
// what lets a mixed load depart with whatever is still eligible.
func TestBuyPolicy_PauseStateIsPerGood(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)

	p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelScarce)
	if d := p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelHigh); !d.Buy {
		t.Fatal("pausing FAB_MATS also paused ADVANCED_CIRCUITRY — a hull could then idle with an eligible material available")
	}
}

// The fleet is paused only when EVERY gate material is paused. Because a hull fills
// greedily from any eligible material, delivery still has useful work while even one
// material is buyable.
func TestBuyPolicy_FleetPausedRequiresEveryMaterialPausedNotAny(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	goods := []string{"FAB_MATS", "ADVANCED_CIRCUITRY"}

	p.Decide("FAB_MATS", "WP-FAB", shared.SupplyLevelScarce)
	p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelHigh)
	if p.FleetPaused(goods) {
		t.Fatal("FleetPaused reported true with ONE material paused — that would starve delivery of capacity it can still use")
	}

	p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelScarce)
	if !p.FleetPaused(goods) {
		t.Fatal("FleetPaused reported false with EVERY material paused")
	}
}

// An empty material list is NOT a paused fleet. Nothing was observed, so claiming
// "paused" would send an operator to tune a knob that changes nothing.
func TestBuyPolicy_FleetPausedIsFalseWhenNothingWasObserved(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	if p.FleetPaused(nil) {
		t.Fatal("FleetPaused(nil) = true; an unobserved fleet is not a paused one")
	}
	if p.FleetPaused([]string{}) {
		t.Fatal("FleetPaused([]) = true; an unobserved fleet is not a paused one")
	}
	// A good never decided on has no pause state and must not read as paused.
	if p.FleetPaused([]string{"FAB_MATS"}) {
		t.Fatal("a good with no recorded decision read as paused")
	}
}

// OBSERVABILITY. A pause must record factory, good, observed supply and the resume
// condition — in the MESSAGE, because the container log renderer drops metadata maps.
func TestDecision_LogLineNamesFactoryGoodSupplyAndResumeCondition(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	line := p.Decide("FAB_MATS", "WP-FABMILL", shared.SupplyLevelLimited).LogLine()

	for _, want := range []string{"FAB_MATS", "WP-FABMILL", "LIMITED", "MODERATE", "HIGH"} {
		if !strings.Contains(line, want) {
			t.Fatalf("pause log line %q does not name %q — the pause must be diagnosable from the log alone", line, want)
		}
	}

	buyLine := p.Decide("ADVANCED_CIRCUITRY", "WP-CIRC", shared.SupplyLevelHigh).LogLine()
	if buyLine == "" {
		t.Fatal("a BUY decision produced no log line; a buying fleet and an idle one must not look identical either")
	}
	if !strings.Contains(buyLine, "ADVANCED_CIRCUITRY") || !strings.Contains(buyLine, "WP-CIRC") {
		t.Fatalf("buy log line %q does not name the good and factory", buyLine)
	}
}

// Unset floors resolve to the ARMED defaults: MODERATE buy, HIGH resume. There is no
// off state — an unset knob is a default, never a disabled policy.
func TestNewBuyPolicy_UnsetFloorsResolveToTheArmedDefaults(t *testing.T) {
	p := NewBuyPolicy("", "")
	buy, resume := p.Floors()
	if buy != DefaultBuyFloor || resume != DefaultResumeFloor {
		t.Fatalf("Floors() = (%s, %s), want the armed defaults (%s, %s)", buy, resume, DefaultBuyFloor, DefaultResumeFloor)
	}
	if DefaultBuyFloor.Order() >= DefaultResumeFloor.Order() {
		t.Fatalf("default resume floor %s is not above the default buy floor %s — the hysteresis gap would be zero", DefaultResumeFloor, DefaultBuyFloor)
	}
}

// A resume floor at or below the buy floor is a zero-or-negative gap: the policy would
// chatter exactly as a single threshold does. Raise it to the buy floor's next level.
func TestNewBuyPolicy_ResumeFloorIsRaisedAboveTheBuyFloor(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelHigh, shared.SupplyLevelLimited)
	buy, resume := p.Floors()
	if resume.Order() <= buy.Order() {
		t.Fatalf("Floors() = (%s, %s); a resume floor at or below the buy floor collapses the hysteresis to a single threshold", buy, resume)
	}
}

// The drain decides concurrently across supply workers; -race must find no data race.
func TestBuyPolicy_DecideIsSafeUnderConcurrentUse(t *testing.T) {
	p := NewBuyPolicy(shared.SupplyLevelModerate, shared.SupplyLevelHigh)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			supply := shared.SupplyLevelHigh
			if i%2 == 0 {
				supply = shared.SupplyLevelScarce
			}
			p.Decide("FAB_MATS", "WP-FAB", supply)
			p.FleetPaused([]string{"FAB_MATS"})
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/domain/manufacturing/gate/ -run 'TestBuyPolicy|TestDecision|TestNewBuyPolicy' 2>&1 | tail -20`
Expected: FAIL — `undefined: NewBuyPolicy`.

- [ ] **Step 3: Write minimal implementation**

Create `gobot/internal/domain/manufacturing/gate/buy_policy.go`:

```go
package gate

import (
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// DefaultBuyFloor and DefaultResumeFloor are the ARMED defaults. These are tunables,
	// not feature flags: the knob adjusts a value in a path that always runs, and an unset
	// knob resolves here rather than disabling the policy. There is no off state.
	DefaultBuyFloor    = shared.SupplyLevelModerate
	DefaultResumeFloor = shared.SupplyLevelHigh
)

// supplyLadder is the SCARCE..ABUNDANT ordering, used to raise a mis-set resume floor to
// the level above the buy floor. It mirrors shared.SupplyLevel.Order() and is asserted
// against it by the package tests, so the two orderings cannot drift.
var supplyLadder = []shared.SupplyLevel{
	shared.SupplyLevelScarce,
	shared.SupplyLevelLimited,
	shared.SupplyLevelModerate,
	shared.SupplyLevelHigh,
	shared.SupplyLevelAbundant,
}

// Decision is one buy/pause ruling, materialized. It exists because the failure this
// design corrects is that decisions lived only as control flow — an if that returned
// nil — so a declined operation and an idle one were indistinguishable. Every field an
// operator needs to act is here: what, where, what was observed, and what would change it.
type Decision struct {
	Good        string
	Factory     string
	Supply      shared.SupplyLevel
	Buy         bool
	Paused      bool
	BuyFloor    shared.SupplyLevel
	ResumeFloor shared.SupplyLevel
}

// LogLine renders the decision for the container log. Everything is in the MESSAGE, not
// in a metadata map: the container log renderer drops the map, so a decision that
// reported itself only in metadata would be exactly as invisible as one that said nothing.
func (d Decision) LogLine() string {
	if d.Paused {
		return fmt.Sprintf("Gate delivery PAUSED on %s at %s: supply %s is below the %s buy floor — resumes at %s",
			d.Good, d.Factory, d.Supply, d.BuyFloor, d.ResumeFloor)
	}
	return fmt.Sprintf("Gate delivery BUYING %s at %s: supply %s is at or above the %s buy floor",
		d.Good, d.Factory, d.Supply, d.BuyFloor)
}

// BuyPolicy is the supply-anchored buy/pause rule with hysteresis.
//
// Pause state is IN-MEMORY and per-process, following the worker-registry precedent. A
// restart re-derives it: an unpaused start re-pauses on its first low read, costing one
// tick and never a spend. Persisting it would add a write to the hot path and buy nothing.
//
// Price is deliberately NOT a gate here. The gate is a finite, high-ROI investment, and
// supply-anchoring already paces against our own market impact — sustained buying depletes
// supply, which trips the pause before the ask can ladder far.
type BuyPolicy struct {
	mu          sync.Mutex
	buyFloor    shared.SupplyLevel
	resumeFloor shared.SupplyLevel
	// paused is good -> paused. Absent means "never observed", which is NOT paused: an
	// unobserved fleet must never read as a paused one.
	paused map[string]bool
}

// NewBuyPolicy builds the policy from the live floors. Unset floors resolve to the armed
// defaults, and a resume floor that is not strictly above the buy floor is RAISED to the
// next level up — a zero-or-negative gap collapses the hysteresis back to the single
// threshold that chatters, which is the whole defect the second floor exists to prevent.
func NewBuyPolicy(buyFloor, resumeFloor shared.SupplyLevel) *BuyPolicy {
	if buyFloor.Order() == 0 {
		buyFloor = DefaultBuyFloor
	}
	if resumeFloor.Order() == 0 {
		resumeFloor = DefaultResumeFloor
	}
	if resumeFloor.Order() <= buyFloor.Order() {
		resumeFloor = nextLevelAbove(buyFloor)
	}
	return &BuyPolicy{buyFloor: buyFloor, resumeFloor: resumeFloor, paused: make(map[string]bool)}
}

// nextLevelAbove is the supply level one step above level, or level itself when it is
// already the top of the ladder (ABUNDANT, where no gap is expressible).
func nextLevelAbove(level shared.SupplyLevel) shared.SupplyLevel {
	for i, l := range supplyLadder {
		if l == level && i+1 < len(supplyLadder) {
			return supplyLadder[i+1]
		}
	}
	return level
}

// Floors reports the resolved floors in force — what the operator's knob actually became
// after defaulting and gap-raising, not what was passed in.
func (p *BuyPolicy) Floors() (buy, resume shared.SupplyLevel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buyFloor, p.resumeFloor
}

// Decide rules on one material at its terminal factory and RECORDS the ruling.
//
// The hysteresis is one-directional and lives entirely in this method: a material that is
// currently buying pauses the moment supply drops below the buy floor, and a material that
// is currently paused resumes only once supply reaches the RESUME floor — not merely the
// buy floor it fell through. Reading the resume side against the buy floor is the chatter
// bug: pause, one unit regenerates, resume, immediately deplete.
func (p *BuyPolicy) Decide(good, factory string, supply shared.SupplyLevel) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()

	buy := false
	if p.paused[good] {
		buy = supply.Order() >= p.resumeFloor.Order()
	} else {
		buy = supply.Order() >= p.buyFloor.Order()
	}
	p.paused[good] = !buy

	return Decision{
		Good:        good,
		Factory:     factory,
		Supply:      supply,
		Buy:         buy,
		Paused:      !buy,
		BuyFloor:    p.buyFloor,
		ResumeFloor: p.resumeFloor,
	}
}

// FleetPaused reports whether the DELIVERY FLEET is paused: only when EVERY gate material
// is paused, never when any one is.
//
// Because a hull fills greedily from any eligible material, delivery still has useful work
// while even one material is buyable; treating one pause as a fleet pause would move
// workers away from capacity delivery can still use. An empty list is not a paused fleet
// either — nothing was observed, and reporting "paused" would send an operator to tune a
// knob that changes nothing.
func (p *BuyPolicy) FleetPaused(goods []string) bool {
	if len(goods) == 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, good := range goods {
		if !p.paused[good] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test -race ./internal/domain/manufacturing/gate/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40`
Expected: `ok`, no `FAIL`. The `-race` flag matters here — `TestBuyPolicy_DecideIsSafeUnderConcurrentUse` is only meaningful under it.

- [ ] **Step 5: Commit (BEFORE the probe)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(gate): supply-anchored buy/pause policy with hysteresis and a Decision record

Buy at or above the buy floor; pause below it; resume only at the RESUME floor.
Two thresholds, not one: a single threshold chatters at the boundary -- pause,
one unit regenerates, resume, immediately deplete.

Every ruling materializes as a Decision with a LogLine that names good, factory,
observed supply and the resume condition IN THE MESSAGE (the container log
renderer drops metadata maps). A paused delivery fleet and an idle one were
indistinguishable; that is the opacity this design exists to remove.

FleetPaused is EVERY material, never any: a hull fills greedily from whatever is
eligible, so one paused material still leaves delivery useful work.

Pause state is in-memory per process (worker-registry precedent). A restart
re-derives it at the cost of one tick and never a spend." -- gobot/internal/domain/manufacturing/gate/buy_policy.go gobot/internal/domain/manufacturing/gate/buy_policy_test.go
```

- [ ] **Step 6: Mutation-probe the hysteresis**

The resume floor is the guard. Break it so resuming reads against the BUY floor — the single-threshold chatter bug — and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|buy = supply.Order() >= p.resumeFloor.Order()|buy = supply.Order() >= p.buyFloor.Order()|' internal/domain/manufacturing/gate/buy_policy.go && ! grep -q 'p.resumeFloor.Order()' internal/domain/manufacturing/gate/buy_policy.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestBuyPolicy' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/domain/manufacturing/gate/buy_policy.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestBuyPolicy_ResumeRequiresOneLevelAboveTheBuyFloor` and `--- FAIL: TestBuyPolicy_OscillatingAtTheBoundaryDoesNotChatter`, then `RESTORED`. Zero named tests means the hysteresis is untested — stop and fix the test.

---

### Task 3: `gate.PlanFill` — greedy max-cargo mixed fill, `trade_volume` tranching, skip reasons

"Always maximize cargo usage. A hull fills its hold, not a per-material tranche." Mixed loading is also what gives the pause rule an escape valve: a hull that fills from any eligible material simply loads the other one and departs instead of idling.

Two corrections are baked in here, both load-bearing:

1. **There is no stock field on the market.** An independent DB probe confirmed the market exposes a supply LEVEL and a per-transaction `trade_volume` and **no stock count**. Trip availability is therefore `trade_volume × gateMaxTranchesPerStop` where that constant is **4** — a bound that stops one stop monopolising a mixed trip. Do not reference a nonexistent `available_supply` field.
2. **`capacity_left <= 0`, not `== 0`** (upheld spec objection). Written `<= 0` so the guard is honest even though the take arithmetic makes a negative unreachable.

Skip-reason precedence is `hold_full → bill_satisfied → paused → no_supply` (adjudication #1): the first two are facts independent of policy, and calling a met bill "paused" would send an operator to tune a knob that changes nothing. `hold_full` is a real outcome the greedy loop produces that none of the spec's three named reasons describes honestly.

**Files:**
- Create: `gobot/internal/domain/manufacturing/gate/fill.go`
- Test: `gobot/internal/domain/manufacturing/gate/fill_test.go`

**Interfaces:**
- Consumes: nothing. Pure. The caller (Task 8) projects its quote-carrying wrapper down to `[]Material` before calling.
- Produces: `gateMaxTranchesPerStop`; `type Material struct{ Good string; Remaining, TradeVolume int; Paused bool }`; `type Stop struct{ Good string; Units, Tranches int }`; `type Skip struct{ Good, Reason string }`; skip-reason consts; `type Trip struct{ Capacity int; Stops []Stop; Skips []Skip }`; `(Trip) Loaded() int`; `(Trip) LogLine() string`; `PlanFill(capacity int, materials []Material) Trip`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/domain/manufacturing/gate/fill_test.go`:

```go
package gate

import (
	"strings"
	"testing"
)

// Fill greedily, by remaining bill descending, until the hold is full.
func TestPlanFill_FillsGreedilyByRemainingBillDescending(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 200, TradeVolume: 40},
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
	})

	if len(trip.Stops) == 0 {
		t.Fatal("PlanFill loaded nothing from two eligible materials")
	}
	if trip.Stops[0].Good != "FAB_MATS" {
		t.Fatalf("first stop is %s; the greatest remaining bill (FAB_MATS, 900) must be filled first", trip.Stops[0].Good)
	}
	if trip.Loaded() != 80 {
		t.Fatalf("Loaded() = %d, want the full 80-unit hold", trip.Loaded())
	}
}

// THE TRANCHE BOUND. Trip availability is trade_volume x gateMaxTranchesPerStop, so a
// material with a huge outstanding bill and a small trade volume cannot take the whole
// hold and starve the other material out of a mixed trip.
func TestPlanFill_OneStopCannotMonopoliseAMixedTrip(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "FAB_MATS", Remaining: 1000, TradeVolume: 10},          // available = 10*4 = 40
		{Good: "ADVANCED_CIRCUITRY", Remaining: 500, TradeVolume: 20}, // available = 20*4 = 80
	})

	if len(trip.Stops) != 2 {
		t.Fatalf("Stops = %+v, want a MIXED trip of 2 stops; one stop monopolised the hold", trip.Stops)
	}
	for _, s := range trip.Stops {
		if s.Good == "FAB_MATS" && s.Units > 10*gateMaxTranchesPerStop {
			t.Fatalf("FAB_MATS took %d units, above its %d-unit trip availability (trade_volume 10 x %d tranches)",
				s.Units, 10*gateMaxTranchesPerStop, gateMaxTranchesPerStop)
		}
	}
	if trip.Loaded() != 80 {
		t.Fatalf("Loaded() = %d, want 80 — the second material should fill what the first could not take", trip.Loaded())
	}
}

// Fill NEVER exceeds the remaining bill: buying past demand is over-supply.
func TestPlanFill_NeverExceedsTheRemainingBill(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 12, TradeVolume: 40}})

	if trip.Loaded() != 12 {
		t.Fatalf("Loaded() = %d, want exactly the 12-unit remaining bill", trip.Loaded())
	}
}

func TestPlanFill_NeverExceedsHullCapacity(t *testing.T) {
	trip := PlanFill(30, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, TradeVolume: 40},
	})

	if trip.Loaded() > 30 {
		t.Fatalf("Loaded() = %d, above the 30-unit hold", trip.Loaded())
	}
}

// THE PAUSE ESCAPE VALVE: one material paused, the hull fills entirely with the other
// rather than idling.
func TestPlanFill_APausedMaterialIsSkippedAndTheOtherFillsTheHold(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40, Paused: true},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, TradeVolume: 40},
	})

	if len(trip.Stops) != 1 || trip.Stops[0].Good != "ADVANCED_CIRCUITRY" {
		t.Fatalf("Stops = %+v, want the one eligible material only", trip.Stops)
	}
	if trip.Loaded() != 80 {
		t.Fatalf("Loaded() = %d, want a full hold — a paused material must not leave the hull idle", trip.Loaded())
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Good != "FAB_MATS" || trip.Skips[0].Reason != SkipPaused {
		t.Fatalf("Skips = %+v, want FAB_MATS skipped as %q", trip.Skips, SkipPaused)
	}
}

// PRECEDENCE. A material whose bill is already MET is reported bill_satisfied even when
// it is also paused: calling a met bill "paused" sends an operator to tune a knob that
// changes nothing.
func TestPlanFill_ASatisfiedBillIsNeverReportedAsPaused(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 0, TradeVolume: 40, Paused: true}})

	if len(trip.Skips) != 1 {
		t.Fatalf("Skips = %+v, want exactly one", trip.Skips)
	}
	if trip.Skips[0].Reason != SkipBillSatisfied {
		t.Fatalf("skip reason = %q, want %q — a met bill is a fact independent of policy", trip.Skips[0].Reason, SkipBillSatisfied)
	}
}

// The full precedence chain: hold_full > bill_satisfied > paused > no_supply.
func TestPlanFill_SkipReasonPrecedenceIsHoldFullThenBillThenPausedThenNoSupply(t *testing.T) {
	// One material fills the hold; every later material must read hold_full regardless of
	// its own bill, pause state or trade volume.
	trip := PlanFill(40, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 0, TradeVolume: 0, Paused: true},
	})
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipHoldFull {
		t.Fatalf("Skips = %+v, want ADVANCED_CIRCUITRY skipped as %q once the hold was full", trip.Skips, SkipHoldFull)
	}

	// With room left, a paused material outranks a zero-trade-volume one.
	trip = PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 0, Paused: true}})
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipPaused {
		t.Fatalf("Skips = %+v, want %q (policy outranks a market read that only matters if we were going to buy)", trip.Skips, SkipPaused)
	}
}

// A zero/absent trade volume is NO SUPPLY, not a zero-unit stop. A stop that buys
// nothing would be a trip leg with no purpose and a divide-by-zero in the tranche count.
func TestPlanFill_ZeroTradeVolumeIsNoSupplyNotAZeroUnitStop(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 0}})

	if len(trip.Stops) != 0 {
		t.Fatalf("Stops = %+v, want none from an unbuyable market", trip.Stops)
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipNoSupply {
		t.Fatalf("Skips = %+v, want %q", trip.Skips, SkipNoSupply)
	}
}

// Buys are bounded by trade_volume per transaction: 80 units at trade_volume 20 is 4
// transactions. This is a market constraint, not an architectural one.
func TestPlanFill_TranchesAreCeilOfTakeOverTradeVolume(t *testing.T) {
	trip := PlanFill(80, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 20}})

	if len(trip.Stops) != 1 {
		t.Fatalf("Stops = %+v, want one", trip.Stops)
	}
	if trip.Stops[0].Units != 80 || trip.Stops[0].Tranches != 4 {
		t.Fatalf("stop = %+v, want 80 units in 4 tranches", trip.Stops[0])
	}

	// A remainder rounds UP: 45 units at trade_volume 20 is 3 transactions, not 2.
	trip = PlanFill(45, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 20}})
	if trip.Stops[0].Tranches != 3 {
		t.Fatalf("45 units at trade_volume 20 = %d tranches, want 3", trip.Stops[0].Tranches)
	}
}

// OBSERVABILITY. The trip log must name capacity, what was loaded, and what was skipped
// AND WHY -- in the message, since the container log renderer drops metadata maps.
func TestTrip_LogLineNamesCapacityLoadedAndEverySkipWithItsReason(t *testing.T) {
	trip := PlanFill(80, []Material{
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
		{Good: "ADVANCED_CIRCUITRY", Remaining: 900, TradeVolume: 40, Paused: true},
	})
	line := trip.LogLine()

	for _, want := range []string{"80", "FAB_MATS", "ADVANCED_CIRCUITRY", SkipPaused} {
		if !strings.Contains(line, want) {
			t.Fatalf("trip log line %q does not name %q — a trip that skipped a material must say which and why", line, want)
		}
	}
}

func TestPlanFill_EmptyMaterialsProducesAnEmptyTrip(t *testing.T) {
	trip := PlanFill(80, nil)
	if len(trip.Stops) != 0 || len(trip.Skips) != 0 || trip.Loaded() != 0 {
		t.Fatalf("PlanFill(80, nil) = %+v, want an empty trip", trip)
	}
}

// A hull with no usable hold loads nothing, and says so as hold_full rather than
// silently producing an empty trip that reads like "nothing to do".
func TestPlanFill_NonPositiveCapacityLoadsNothingAndSaysWhy(t *testing.T) {
	trip := PlanFill(0, []Material{{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40}})

	if trip.Loaded() != 0 {
		t.Fatalf("Loaded() = %d on a zero-capacity hull", trip.Loaded())
	}
	if len(trip.Skips) != 1 || trip.Skips[0].Reason != SkipHoldFull {
		t.Fatalf("Skips = %+v, want %q", trip.Skips, SkipHoldFull)
	}
}

// PlanFill must not reorder or mutate the caller's slice: the drain reuses it to record
// its per-material decisions after planning.
func TestPlanFill_DoesNotMutateTheCallersSlice(t *testing.T) {
	materials := []Material{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 200, TradeVolume: 40},
		{Good: "FAB_MATS", Remaining: 900, TradeVolume: 40},
	}
	PlanFill(80, materials)

	if materials[0].Good != "ADVANCED_CIRCUITRY" || materials[1].Good != "FAB_MATS" {
		t.Fatalf("PlanFill reordered the caller's slice: %+v", materials)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFill|TestTrip' 2>&1 | tail -20`
Expected: FAIL — `undefined: PlanFill`.

- [ ] **Step 3: Write minimal implementation**

Create `gobot/internal/domain/manufacturing/gate/fill.go`:

```go
package gate

import (
	"fmt"
	"sort"
	"strings"
)

// gateMaxTranchesPerStop bounds how many trade_volume transactions one stop may perform
// on a single trip, and so bounds how much of the hold one material may take.
//
// It exists because the market exposes NO stock count. It reports a supply LEVEL and a
// per-transaction trade_volume, and nothing else — so there is no quantity to read and
// no available_supply field to consult. The supply level still gates WHETHER we buy, via
// BuyPolicy; this bounds only how much one stop can lift, so a material with a large
// outstanding bill and a small trade volume cannot monopolise a mixed trip and leave the
// other material's factory unvisited.
//
// 4 is the weakest inference in this design and is flagged as such: revisit it against
// live fill data once phase 2 runs.
const gateMaxTranchesPerStop = 4

// Skip reasons, in PRECEDENCE order. hold_full and bill_satisfied are facts independent
// of policy and therefore outrank it: reporting a met bill as "paused" would send an
// operator to tune a knob that changes nothing. hold_full is a real outcome the greedy
// loop produces that none of the spec's three named reasons describes honestly.
const (
	SkipHoldFull      = "hold_full"
	SkipBillSatisfied = "bill_satisfied"
	SkipPaused        = "paused"
	SkipNoSupply      = "no_supply"
)

// Material is one gate material as the fill planner sees it: what the site still needs,
// what the market will sell per transaction, and whether the buy policy has paused it.
//
// It deliberately carries no market quote. The caller holds the quote (it needs the
// waypoint and price to actually buy) and PROJECTS down to this type before planning, so
// the fill arithmetic cannot accidentally depend on a price or a waypoint symbol.
type Material struct {
	Good        string
	Remaining   int
	TradeVolume int
	Paused      bool
}

// Stop is one factory visit on the trip: what to buy and in how many transactions.
type Stop struct {
	Good     string
	Units    int
	Tranches int
}

// Skip is one material the trip did NOT load, and why. This is half the point of the
// type: a trip that loaded one material out of two must be able to say which it left and
// for what reason, or a paused fleet and a finished one look identical.
type Skip struct {
	Good   string
	Reason string
}

// Trip is one hull's planned mixed load.
type Trip struct {
	Capacity int
	Stops    []Stop
	Skips    []Skip
}

// Loaded is the total units the trip plans to carry.
func (t Trip) Loaded() int {
	total := 0
	for _, s := range t.Stops {
		total += s.Units
	}
	return total
}

// LogLine renders the whole fill outcome for the container log: capacity, what was
// loaded, and what was skipped with its reason. All in the MESSAGE — the container log
// renderer drops metadata maps, so a trip reporting itself only in metadata is as
// invisible as one that said nothing.
func (t Trip) LogLine() string {
	loaded := make([]string, 0, len(t.Stops))
	for _, s := range t.Stops {
		loaded = append(loaded, fmt.Sprintf("%s x%d (%d tranche(s))", s.Good, s.Units, s.Tranches))
	}
	skipped := make([]string, 0, len(t.Skips))
	for _, s := range t.Skips {
		skipped = append(skipped, fmt.Sprintf("%s: %s", s.Good, s.Reason))
	}

	loadedText := "nothing"
	if len(loaded) > 0 {
		loadedText = strings.Join(loaded, ", ")
	}
	if len(skipped) == 0 {
		return fmt.Sprintf("Gate delivery trip: %d/%d units of hold loaded — %s", t.Loaded(), t.Capacity, loadedText)
	}
	return fmt.Sprintf("Gate delivery trip: %d/%d units of hold loaded — %s; skipped %s",
		t.Loaded(), t.Capacity, loadedText, strings.Join(skipped, ", "))
}

// PlanFill builds the greedy max-cargo mixed load: fill from eligible factories, by
// remaining bill descending, until the hold is full.
//
// Mixed loads are the default where factories are co-located. Both terminal factories are
// typically in the same system, so one trip amortizes the expensive gate leg across both
// materials instead of paying it twice. Mixed loading is also what gives the pause rule an
// escape valve — with one material paused a single-material hull would idle, whereas a
// hull that fills from any eligible material simply loads the other and departs.
//
// Every material the trip does not load is recorded with a reason. The precedence
// (hold_full, bill_satisfied, paused, no_supply) is deliberate and is asserted by the
// package tests.
func PlanFill(capacity int, materials []Material) Trip {
	trip := Trip{Capacity: capacity}
	if len(materials) == 0 {
		return trip
	}

	// Sort a COPY: the caller reuses its slice to record per-material decisions after
	// planning, and reordering it under them would misattribute those records.
	ordered := make([]Material, len(materials))
	copy(ordered, materials)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Remaining != ordered[j].Remaining {
			return ordered[i].Remaining > ordered[j].Remaining
		}
		return ordered[i].Good < ordered[j].Good // deterministic tie-break
	})

	capacityLeft := capacity
	for _, m := range ordered {
		// <= 0, not == 0 (upheld spec objection). The take below is bounded by capacityLeft
		// so a negative is unreachable, but writing the guard as an equality would be a claim
		// about the arithmetic rather than about the hold, and the next edit could make it false.
		if capacityLeft <= 0 {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipHoldFull})
			continue
		}
		if m.Remaining <= 0 {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipBillSatisfied})
			continue
		}
		if m.Paused {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipPaused})
			continue
		}
		if m.TradeVolume <= 0 {
			// Nothing buyable per transaction: not a zero-unit stop (which would be a trip leg
			// with no purpose, and a divide by zero in the tranche count).
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipNoSupply})
			continue
		}

		take := min(m.Remaining, capacityLeft, m.TradeVolume*gateMaxTranchesPerStop)
		if take <= 0 {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipNoSupply})
			continue
		}

		trip.Stops = append(trip.Stops, Stop{
			Good:     m.Good,
			Units:    take,
			Tranches: (take + m.TradeVolume - 1) / m.TradeVolume, // ceil: a remainder is its own transaction
		})
		capacityLeft -= take
	}
	return trip
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test -race ./internal/domain/manufacturing/gate/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40`
Expected: `ok`, no `FAIL`.

- [ ] **Step 5: Commit (BEFORE the probe)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(gate): greedy max-cargo mixed fill with trade_volume tranching and skip reasons

A hull fills its HOLD, not a per-material tranche: fill from eligible factories
by remaining bill descending until the hold is full. Mixed loads amortize the
expensive gate leg across both materials, and give the pause rule an escape
valve -- with one material paused the hull loads the other and departs instead
of idling.

Trip availability is trade_volume x 4, because the market exposes a supply LEVEL
and a per-transaction trade_volume and NO stock count. There is no
available_supply field to read. The bound stops one material with a large bill
and a small trade volume from monopolising a mixed trip.

Every unloaded material is recorded with a reason, precedence hold_full >
bill_satisfied > paused > no_supply: the first two are facts independent of
policy, and reporting a met bill as 'paused' would send an operator to tune a
knob that changes nothing.

capacity_left is guarded <= 0 rather than == 0 (upheld spec objection)." -- gobot/internal/domain/manufacturing/gate/fill.go gobot/internal/domain/manufacturing/gate/fill_test.go
```

- [ ] **Step 6: Mutation-probe the tranche bound**

The `trade_volume × gateMaxTranchesPerStop` bound is the guard that keeps a mixed trip mixed. Remove it and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|take := min(m.Remaining, capacityLeft, m.TradeVolume\*gateMaxTranchesPerStop)|take := min(m.Remaining, capacityLeft)|' internal/domain/manufacturing/gate/fill.go && ! grep -q 'gateMaxTranchesPerStop)' internal/domain/manufacturing/gate/fill.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFill' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/domain/manufacturing/gate/fill.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanFill_OneStopCannotMonopoliseAMixedTrip`, then `RESTORED`. Zero named tests means the bound is untested — stop and fix the test.

- [ ] **Step 7: Mutation-probe the skip-reason precedence**

Adjudication #1 is a guard too: a met bill must never be reported as paused. Swap the two checks and confirm the named test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && perl -0pi -e 's/(\t\tif m\.Remaining <= 0 \{\n\t\t\ttrip\.Skips = append\(trip\.Skips, Skip\{Good: m\.Good, Reason: SkipBillSatisfied\}\)\n\t\t\tcontinue\n\t\t\}\n)(\t\tif m\.Paused \{\n\t\t\ttrip\.Skips = append\(trip\.Skips, Skip\{Good: m\.Good, Reason: SkipPaused\}\)\n\t\t\tcontinue\n\t\t\}\n)/$2$1/' internal/domain/manufacturing/gate/fill.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFill' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/domain/manufacturing/gate/fill.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanFill_ASatisfiedBillIsNeverReportedAsPaused`, then `RESTORED`.

---

### Task 4: Persist the delivery floors on the pipeline row, migration `051`, and the restart-survival round trip

This is the pattern-C half of the knob: the floors live on the construction pipeline row, which is what the drain re-reads every leg. The design deliberately does NOT put them on a manufacturing config key — per CLI-PRIMER §3.1 the manufacturing coordinator is **pattern B**, whose `resolve*Config` clears persisted keys and re-injects from `config.yaml` on every build, so a live tune of a pattern-B knob is clobbered on the next daemon restart. "That is the same class of defect as the inert `prefer-buy` override this design removes."

`ReconstitutePipeline` is a long positional constructor with one call site. Do **not** extend it — `modelToPipeline` already calls `pipeline.SetMaterials(...)` after reconstruction, and the floors follow that same post-reconstruct setter shape. Blast radius stays at zero call sites.

**Files:**
- Modify: `gobot/internal/domain/manufacturing/pipeline.go` (two fields beside `minSupply` at :93)
- Modify: `gobot/internal/domain/manufacturing/pipeline_construction.go` (getters + setter, beside `SetMinSupply` at :75)
- Modify: `gobot/internal/adapters/persistence/models_manufacturing.go` (two columns, beside `MinSupply` at :30)
- Modify: `gobot/internal/adapters/persistence/manufacturing_pipeline_repository.go` (`pipelineToModel` ~:277, `modelToPipeline` ~:288)
- Create: `gobot/migrations/051_add_construction_delivery_floors.up.sql`
- Create: `gobot/migrations/051_add_construction_delivery_floors.down.sql`
- Test: `gobot/internal/adapters/persistence/manufacturing_pipeline_delivery_floors_test.go`

**Interfaces:**
- Consumes: `manufacturing.NewConstructionPipeline`, `GormManufacturingPipelineRepository.Create/FindByID/Update`.
- Produces: `(*ManufacturingPipeline).DeliveryBuyFloor() string`; `(*ManufacturingPipeline).DeliveryResumeFloor() string`; `(*ManufacturingPipeline).SetDeliveryFloors(buyFloor, resumeFloor string)`; model fields `DeliveryBuyFloor`, `DeliveryResumeFloor`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/adapters/persistence/manufacturing_pipeline_delivery_floors_test.go`:

```go
package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// KNOB LIVENESS, restart half (RULINGS #2). The delivery buy/resume floors are pattern-C
// live knobs persisted on the construction pipeline row. A `construction override
// --buy-floor/--resume-floor` write must survive a daemon bounce -- the pattern-B clobber
// this design explicitly avoids has to be pinned by a test, not just by a comment.
func TestConstructionPipelineDeliveryFloorsSurvivePersistReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "GATEFLOOR-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-GATEFLOOR-I1", 1, 3, 5)
	pipeline.SetDeliveryFloors("LIMITED", "MODERATE")
	require.NoError(t, repo.Create(ctx, pipeline))

	// Reload from the DB -- the daemon-bounce equivalent.
	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.NotNil(t, reloaded)

	require.Equal(t, "LIMITED", reloaded.DeliveryBuyFloor(), "the live buy floor must survive a daemon restart")
	require.Equal(t, "MODERATE", reloaded.DeliveryResumeFloor(), "the live resume floor must survive a daemon restart")
}

// A LIVE re-tune (the `construction override` write path: load, set, Update) must also
// round-trip -- not only the value set before the first Create.
func TestConstructionPipelineDeliveryFloorsSurviveALiveRetune(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "GATEFLOOR-RETUNE")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-GATEFLOOR-I2", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline))

	live, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	live.SetDeliveryFloors("HIGH", "ABUNDANT")
	require.NoError(t, repo.Update(ctx, live))

	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.Equal(t, "HIGH", reloaded.DeliveryBuyFloor(), "a live buy-floor tune must persist")
	require.Equal(t, "ABUNDANT", reloaded.DeliveryResumeFloor(), "a live resume-floor tune must persist")
}

// UNSET is the ARMED DEFAULT, not an off switch. A pipeline created before this feature
// (and every pipeline created without the flags) reloads with empty floors, which the
// reader resolves to the MODERATE/HIGH defaults at the point of use. Empty must NOT be
// persisted as some sentinel that a later read mistakes for a real tune.
func TestConstructionPipelineDeliveryFloorsDefaultToUnsetAndRoundTripEmpty(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedPlayer(t, db, 1, "GATEFLOOR-UNSET")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	pipeline := manufacturing.NewConstructionPipeline("X1-GATEFLOOR-I3", 1, 3, 5)
	require.Equal(t, "", pipeline.DeliveryBuyFloor(), "a new pipeline must default the buy floor to unset")
	require.Equal(t, "", pipeline.DeliveryResumeFloor(), "a new pipeline must default the resume floor to unset")
	require.NoError(t, repo.Create(ctx, pipeline))

	reloaded, err := repo.FindByID(ctx, pipeline.ID())
	require.NoError(t, err)
	require.Equal(t, "", reloaded.DeliveryBuyFloor())
	require.Equal(t, "", reloaded.DeliveryResumeFloor())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/persistence/ -run 'TestConstructionPipelineDeliveryFloors' 2>&1 | tail -20`
Expected: FAIL — `pipeline.SetDeliveryFloors undefined`.

- [ ] **Step 3: Add the domain fields, getters and setter**

In `gobot/internal/domain/manufacturing/pipeline.go`, immediately after the `goodOverrides` field (:94), add:

```go
	deliveryBuyFloor    string // Gate delivery fleet's supply BUY floor, e.g. "MODERATE". Empty = unset (reader resolves the armed default).
	deliveryResumeFloor string // Gate delivery fleet's supply RESUME floor, e.g. "HIGH". Empty = unset (reader resolves the armed default).
```

In `gobot/internal/domain/manufacturing/pipeline_construction.go`, after `SetGoodOverrides` (:87), add:

```go
// DeliveryBuyFloor and DeliveryResumeFloor are the gate DELIVERY fleet's supply-anchored
// buy/pause thresholds, persisted on this construction pipeline row.
//
// They are LIVE KNOBS on the pattern-C surface: the drain re-reads them off this row on
// every leg, so a `construction override --buy-floor/--resume-floor` write takes effect on
// the next leg with no restart, and the value survives a daemon bounce (RULINGS #2).
//
// The row is deliberately NOT a manufacturing config key. The manufacturing coordinator is
// pattern B — its resolve*Config clears persisted keys and re-injects from config.yaml on
// every build — so a floor stored there would appear to work, silently revert on the next
// restart, and give no indication it had. That is the same defect class as the inert
// prefer-buy override this design removes.
//
// Empty means UNSET, which the reader resolves to the ARMED defaults (MODERATE buy, HIGH
// resume). Unset is a default, never an off switch: nothing here is default-off.
func (p *ManufacturingPipeline) DeliveryBuyFloor() string { return p.deliveryBuyFloor }

func (p *ManufacturingPipeline) DeliveryResumeFloor() string { return p.deliveryResumeFloor }

// SetDeliveryFloors sets both thresholds. Both are set together because they are one
// decision: a resume floor is only meaningful relative to the buy floor it sits above, and
// letting them be written independently invites a half-applied tune that collapses the
// hysteresis gap. An empty value clears that floor back to the armed default.
func (p *ManufacturingPipeline) SetDeliveryFloors(buyFloor, resumeFloor string) {
	p.deliveryBuyFloor = buyFloor
	p.deliveryResumeFloor = resumeFloor
}
```

- [ ] **Step 4: Add the model columns and prove the drift gate fails**

In `gobot/internal/adapters/persistence/models_manufacturing.go`, after `MinSupply` (:30), add:

```go
	DeliveryBuyFloor    string  `gorm:"column:delivery_buy_floor;size:20;default:''"`    // Gate delivery fleet supply BUY floor; '' = unset (armed default MODERATE)
	DeliveryResumeFloor string  `gorm:"column:delivery_resume_floor;size:20;default:''"` // Gate delivery fleet supply RESUME floor; '' = unset (armed default HIGH)
```

Now run the repo's own column-drift gate — it must FAIL, because the columns exist in the model and no migration defines them:

Run: `cd gobot && go test ./internal/adapters/persistence/ -run 'TestModelColumnsBackedByMigrations' 2>&1 | grep -E "^(--- FAIL|FAIL|ok|    )" | head -20`
Expected: FAIL naming `manufacturing_pipelines.delivery_buy_floor` and `manufacturing_pipelines.delivery_resume_floor`, with the SQLSTATE 42703 warning. **This failure is the point** — it is the repo proving, before the migration is written, that an un-migrated column is a production write error waiting to happen.

- [ ] **Step 5: Write migration 051**

Create `gobot/migrations/051_add_construction_delivery_floors.up.sql`:

```sql
-- Backs ManufacturingPipelineModel.DeliveryBuyFloor / DeliveryResumeFloor
-- (internal/adapters/persistence/models_manufacturing.go) — the gate DELIVERY fleet's
-- supply-anchored buy/pause thresholds.
--
-- WHY THESE LIVE ON THE PIPELINE ROW AND NOT IN config.yaml. The right hysteresis gap is
-- not knowable in advance: too narrow and the fleet chatters at the supply boundary, too
-- wide and it starves while usable stock sits at the factory. So the thresholds must be
-- adjustable in real time, without a restart — a pattern-C live knob, re-read per tick,
-- on the same `construction override` verb that already carries --min-supply and
-- --price-ceiling-mult.
--
-- The manufacturing coordinator is pattern B: its resolve*Config clears persisted keys and
-- re-injects from config.yaml on every build. A floor stored on a manufacturing config key
-- would therefore appear to tune, silently revert on the next daemon restart, and give no
-- indication it had. That is the same defect class as the inert prefer-buy override this
-- design removes, so the value is persisted on the pipeline row instead, where the same
-- durable path that already carries min_supply and max_workers keeps it.
--
-- NAMED DISTINCTLY FROM min_supply, deliberately. min_supply is the construction
-- pipeline's ADMISSION floor (whether a material is promoted to READY, per sp-yexq) — a
-- different decision at a different stage. Two supply thresholds with confusable names
-- would be its own opacity, which is the failure this whole design exists to correct.
--
-- DEFAULT '' MEANS UNSET, WHICH IS THE ARMED DEFAULT, NOT AN OFF SWITCH. The reader
-- resolves '' to MODERATE (buy) and HIGH (resume) at the point of use. There is no
-- disabled state: these are tunables, and the path they sit in always runs. Existing rows
-- therefore need no backfill — every pre-existing pipeline reads as "armed at the
-- defaults", which is exactly what it should have been all along.
--
-- Migration-backed because boot AutoMigrate failure is NON-FATAL: without this, a boot
-- where AutoMigrate could not run would leave the floor writes hitting SQLSTATE 42703
-- (undefined_column). manufacturing_pipelines is CREATE'd by an earlier migration, so it
-- is checkable by TestModelColumnsBackedByMigrations and model and migration are held in
-- lockstep.
--
-- APPLY THIS TO PRODUCTION BEFORE DEPLOYING THE DAEMON THAT WRITES THESE COLUMNS.
--
-- No index. Both columns are read only as part of the pipeline row the drain already loads
-- by primary key or by construction_site (both indexed), and written only on an operator
-- tune; an index would charge every pipeline write for a sort of a handful of rows.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS): a no-op on any database where boot AutoMigrate
-- already added them. Type/size/default mirror the GORM tags exactly (VARCHAR(20) NOT NULL
-- DEFAULT '') so a fresh database and an AutoMigrated one converge.

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS delivery_buy_floor VARCHAR(20) NOT NULL DEFAULT '';

ALTER TABLE manufacturing_pipelines
    ADD COLUMN IF NOT EXISTS delivery_resume_floor VARCHAR(20) NOT NULL DEFAULT '';

COMMENT ON COLUMN manufacturing_pipelines.delivery_buy_floor IS 'Gate DELIVERY fleet: buy while the terminal factory''s supply is at or above this level. '''' = unset, which the reader resolves to the armed default MODERATE. Distinct from min_supply, which is the pipeline''s READY-admission floor.';

COMMENT ON COLUMN manufacturing_pipelines.delivery_resume_floor IS 'Gate DELIVERY fleet: once paused, resume buying only when supply recovers to this level. '''' = unset, which the reader resolves to the armed default HIGH. Two thresholds, not one: a single threshold chatters at the boundary.';
```

Create `gobot/migrations/051_add_construction_delivery_floors.down.sql`:

```sql
-- Rollback the gate delivery fleet's buy/resume floors from manufacturing_pipelines.
--
-- DROPPING THESE LOSES EVERY OPERATOR TUNE, and the fleet reverts to the armed defaults
-- (MODERATE buy, HIGH resume) rather than stopping — the delivery path always runs, so
-- there is no stall risk here, only a loss of tuning. If the fleet was tuned away from the
-- defaults because the defaults chattered or starved it in this era's markets, that
-- diagnosis is lost with the columns; record the values before rolling back.
--
-- Requires rolling the code back too: a binary that still writes these columns would hit
-- SQLSTATE 42703 (undefined_column) on the first tune. Nothing else reads them, and no
-- pipeline state, material progress or hull assignment is touched by the drop.

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS delivery_buy_floor;

ALTER TABLE manufacturing_pipelines
    DROP COLUMN IF EXISTS delivery_resume_floor;
```

- [ ] **Step 6: Map both floors through the repository**

In `gobot/internal/adapters/persistence/manufacturing_pipeline_repository.go`, in `pipelineToModel` beside `MinSupply: p.MinSupply(),` (~:277) add:

```go
		DeliveryBuyFloor:    p.DeliveryBuyFloor(),
		DeliveryResumeFloor: p.DeliveryResumeFloor(),
```

In `modelToPipeline`, after the `ReconstitutePipeline(...)` call returns and **before** the materials block, add:

```go
	// Set through the post-reconstruct setter rather than by extending ReconstitutePipeline's
	// positional signature: that constructor already takes 20 positional arguments, and every
	// one added is another chance for two strings to be transposed at the single call site with
	// no compiler complaint. SetMaterials below is set the same way for the same reason.
	pipeline.SetDeliveryFloors(m.DeliveryBuyFloor, m.DeliveryResumeFloor)
```

- [ ] **Step 7: Run the tests to verify they pass**

```bash
cd gobot && go test ./internal/adapters/persistence/ -run 'TestConstructionPipelineDeliveryFloors|TestModelColumnsBackedByMigrations' 2>&1 | grep -E "^(ok|FAIL|---)" | head -20
```
Expected: PASS for all three floor round-trip tests AND for the drift gate (the migration now backs both columns).

Then the whole package under race:

```bash
cd gobot && go test -race ./internal/adapters/persistence/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL`. If a `NewTestConnection` test skips for want of a database, that is the package's existing behaviour — note it in the handback, do not "fix" it.

- [ ] **Step 8: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(construction): persist the gate delivery buy/resume floors on the pipeline row (migration 051)

The two thresholds are pattern-C LIVE knobs: the drain re-reads them off this row
on every leg, so a tune takes effect with no restart AND survives a daemon bounce
(RULINGS #2).

They are deliberately not a manufacturing config key. That coordinator is pattern
B -- resolve*Config clears persisted keys and re-injects from config.yaml on every
build -- so a floor stored there would appear to tune, silently revert on the next
restart, and give no indication it had. Same defect class as the inert prefer-buy
override this design removes.

Named distinctly from min_supply on purpose: that is the pipeline's READY-admission
floor, a different decision at a different stage, and two confusable supply
thresholds would be their own opacity.

'' = unset = the ARMED defaults (MODERATE buy, HIGH resume), resolved at the point
of use. Not an off switch; existing rows need no backfill.

Migration-backed because boot AutoMigrate failure is non-fatal: without 051 the
floor writes would hit SQLSTATE 42703 on a database where it could not run. APPLY
051 TO PRODUCTION BEFORE DEPLOYING THIS DAEMON." -- gobot/internal/domain/manufacturing/pipeline.go gobot/internal/domain/manufacturing/pipeline_construction.go gobot/internal/adapters/persistence/models_manufacturing.go gobot/internal/adapters/persistence/manufacturing_pipeline_repository.go gobot/migrations/051_add_construction_delivery_floors.up.sql gobot/migrations/051_add_construction_delivery_floors.down.sql gobot/internal/adapters/persistence/manufacturing_pipeline_delivery_floors_test.go
```

- [ ] **Step 9: Mutation-probe the persistence mapping**

A silently-dropped mapping is exactly how a "live" knob becomes restart-only. Break the read-back and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|	pipeline.SetDeliveryFloors(m.DeliveryBuyFloor, m.DeliveryResumeFloor)|	pipeline.SetDeliveryFloors("", "")|' internal/adapters/persistence/manufacturing_pipeline_repository.go && grep -q 'SetDeliveryFloors("", "")' internal/adapters/persistence/manufacturing_pipeline_repository.go && echo "MUTATION APPLIED" && go test ./internal/adapters/persistence/ -run 'TestConstructionPipelineDeliveryFloors' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/adapters/persistence/manufacturing_pipeline_repository.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestConstructionPipelineDeliveryFloorsSurvivePersistReload` and `--- FAIL: TestConstructionPipelineDeliveryFloorsSurviveALiveRetune`, then `RESTORED`.

---

### Task 5: `--buy-floor` / `--resume-floor` on `construction override` — new RPC, daemon single-writer, CLI dispatch

Adjudication #5: **a separate RPC behind the same `construction override` verb.** `ConstructionGoodOverride` requires `good`, which pipeline-wide floors do not have — so one verb, two RPCs, dispatched on which flags were set. `ConstructionWorkerCap` is the exact precedent for the new RPC's shape: site + player, delegating to a `Mutate...` on `DaemonServer`, which is the single writer (RULINGS #3).

`--good` therefore becomes conditionally required: required for the per-good override, forbidden for the pipeline-wide floors. Naming is deliberate — these must not be confusable with `--min-supply`, which is the pipeline's READY-admission floor.

**Files:**
- Modify: `gobot/pkg/proto/daemon/daemon.proto` (rpc + 2 messages, after `ConstructionWorkerCap` at :283 / :1781)
- Modify: `gobot/internal/adapters/grpc/container_ops_construction.go` (after `MutateConstructionMaxWorkers` at :439)
- Modify: `gobot/internal/adapters/grpc/daemon_service_construction.go` (after `ConstructionWorkerCap` at :183)
- Modify: `gobot/internal/adapters/cli/daemon_client_construction.go` (after `ConstructionWorkerCap` at :205)
- Modify: `gobot/internal/adapters/cli/construction_override.go` (flags, validation, dispatch)
- Test: `gobot/internal/adapters/cli/construction_override_test.go` (append)

**Interfaces:**
- Consumes: `(*ManufacturingPipeline).SetDeliveryFloors` and `DeliveryBuyFloor`/`DeliveryResumeFloor` from Task 4; `shared.IsValidSupply` from `internal/domain/shared/supply_level.go`; `pipelineRepo.FindByConstructionSite` / `Update`.
- Produces: `pb.ConstructionDeliveryFloorsRequest/Response`; `(*DaemonServer).MutateConstructionDeliveryFloors(ctx, constructionSite string, playerID int, buyFloor, resumeFloor string) (*ConstructionDeliveryFloorsResult, error)`; `(*DaemonClient).ConstructionDeliveryFloors(...)`; CLI `buildConstructionDeliveryFloorsRequest` + `runConstructionDeliveryFloors`.

- [ ] **Step 1: Write the failing test**

Append to `gobot/internal/adapters/cli/construction_override_test.go`:

```go
// The pipeline-wide floors are a DIFFERENT decision from the per-good override: they have
// no good. Passing --good with them is a mistake worth rejecting loudly rather than
// silently ignoring, because an operator who typed it believes it did something.
func TestBuildConstructionDeliveryFloorsRequest_RejectsGoodAndRequiresSite(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", good: "FAB_MATS", buyFloor: "MODERATE"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--good must be rejected: the delivery floors are pipeline-wide")

	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{buyFloor: "MODERATE"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "--site is required")
}

// Tier values are validated at the CLI boundary. ParseSupplyLevel is deliberately lenient
// (unknown -> MODERATE) for scanned market data; operator input is not, or a typo would
// silently become a floor the operator never chose.
func TestBuildConstructionDeliveryFloorsRequest_RejectsAnInvalidTier(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "PLENTIFUL"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLENTIFUL")

	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", resumeFloor: "LOTS"}, &PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "LOTS")
}

// A resume floor at or below the buy floor collapses the hysteresis to the single
// threshold that chatters. Reject it at the boundary, naming both values, rather than
// letting the domain silently raise it -- an operator who asked for a gap of zero has a
// mental model worth correcting.
func TestBuildConstructionDeliveryFloorsRequest_RejectsANonPositiveHysteresisGap(t *testing.T) {
	_, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "HIGH", resumeFloor: "MODERATE"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "HIGH")
	require.Contains(t, err.Error(), "MODERATE")

	_, err = buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "HIGH", resumeFloor: "HIGH"},
		&PlayerIdentifier{PlayerID: 1})
	require.Error(t, err, "an equal resume floor is a zero gap")
}

// Setting ONE floor is legal: the other keeps whatever is already persisted. The request
// carries only the provided knob, so an unset one leaves that dimension unchanged --
// matching how --min-supply/--price-ceiling-mult already behave on this verb.
func TestBuildConstructionDeliveryFloorsRequest_SendsOnlyTheProvidedFloors(t *testing.T) {
	req, err := buildConstructionDeliveryFloorsRequest(
		constructionOverrideFlags{site: "X1-VB74-I55", buyFloor: "LIMITED"}, &PlayerIdentifier{PlayerID: 1})
	require.NoError(t, err)
	require.NotNil(t, req.BuyFloor)
	require.Equal(t, "LIMITED", *req.BuyFloor)
	require.Nil(t, req.ResumeFloor, "an unset resume floor must leave that dimension unchanged")
}

// fakeConstructionFloorsClient records the request and serves a canned response.
type fakeConstructionFloorsClient struct {
	gotReq  *pb.ConstructionDeliveryFloorsRequest
	resp    *pb.ConstructionDeliveryFloorsResponse
	respErr error
}

func (f *fakeConstructionFloorsClient) ConstructionDeliveryFloors(_ context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error) {
	f.gotReq = req
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

// The confirmation must state the RESOLVED floors in force and that no restart is needed
// -- the operator's only evidence the knob is live.
func TestRunConstructionDeliveryFloors_ReportsTheResolvedFloorsAndLiveness(t *testing.T) {
	client := &fakeConstructionFloorsClient{resp: &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite: "X1-VB74-I55", BuyFloor: "LIMITED", ResumeFloor: "MODERATE", Changed: true,
	}}
	req := &pb.ConstructionDeliveryFloorsRequest{ConstructionSite: "X1-VB74-I55"}

	msg, err := runConstructionDeliveryFloors(context.Background(), client, req)
	require.NoError(t, err)
	require.Same(t, req, client.gotReq)
	require.Contains(t, msg, "LIMITED")
	require.Contains(t, msg, "MODERATE")
	require.Contains(t, strings.ToLower(msg), "no restart")
}

func TestRunConstructionDeliveryFloors_NoOpReportsUnchanged(t *testing.T) {
	client := &fakeConstructionFloorsClient{resp: &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite: "X1-VB74-I55", BuyFloor: "MODERATE", ResumeFloor: "HIGH", Changed: false,
	}}
	msg, err := runConstructionDeliveryFloors(context.Background(), client,
		&pb.ConstructionDeliveryFloorsRequest{ConstructionSite: "X1-VB74-I55"})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "unchanged")
}

// ONE VERB, TWO RPCs (adjudication #5), dispatched on which flags were set. The floors
// have no good; the per-good override requires one. Mixing them in one call is ambiguous,
// so it is rejected rather than silently resolved to one of the two.
func TestConstructionOverrideFlags_DispatchesFloorsAndPerGoodSeparately(t *testing.T) {
	require.True(t, constructionOverrideFlags{buyFloor: "MODERATE"}.anyFloorSet())
	require.True(t, constructionOverrideFlags{resumeFloor: "HIGH"}.anyFloorSet())
	require.False(t, constructionOverrideFlags{minSupply: "LIMITED"}.anyFloorSet())

	require.True(t, constructionOverrideFlags{minSupply: "LIMITED"}.anyKnobSet())
	require.False(t, constructionOverrideFlags{buyFloor: "MODERATE"}.anyKnobSet(),
		"a delivery floor is not a per-good knob; anyKnobSet must not claim it")
}
```

Add `pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"` to the test imports if it is not already there (it is — the file already builds `pb.ConstructionGoodOverrideRequest`).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/cli/ -run 'ConstructionDeliveryFloors|DispatchesFloors' 2>&1 | tail -20`
Expected: FAIL — `undefined: buildConstructionDeliveryFloorsRequest`, `f.buyFloor undefined`.

- [ ] **Step 3: Add the proto RPC and messages**

In `gobot/pkg/proto/daemon/daemon.proto`, after the `ConstructionWorkerCap` rpc (:283) add:

```proto
  // ConstructionDeliveryFloors sets the gate DELIVERY fleet's supply-anchored buy/resume
  // thresholds on a RUNNING construction pipeline, live, with no restart. A SEPARATE rpc
  // from ConstructionGoodOverride because these floors are PIPELINE-WIDE and that request
  // requires a `good`, which they do not have. The CLI presents both behind the one
  // `construction override` verb and dispatches on which flags were set.
  rpc ConstructionDeliveryFloors(ConstructionDeliveryFloorsRequest) returns (ConstructionDeliveryFloorsResponse);
```

After the `ConstructionWorkerCapResponse` message (:1781) add:

```proto
// ConstructionDeliveryFloorsRequest sets the gate delivery fleet's supply buy/resume
// thresholds on a running construction pipeline. construction_site identifies the target.
// Both floors are optional: an unset one leaves that dimension unchanged, so an operator
// can widen or narrow the hysteresis gap from either end. Values are the SupplyLevel
// vocabulary (ABUNDANT|HIGH|MODERATE|LIMITED|SCARCE); the CLI validates strictly at the
// boundary and rejects a resume floor that is not strictly above the buy floor.
message ConstructionDeliveryFloorsRequest {
  string construction_site = 1;
  optional int32 player_id = 2;
  optional string agent_symbol = 3;
  optional string buy_floor = 4;
  optional string resume_floor = 5;
}

// ConstructionDeliveryFloorsResponse returns the floors now in force after the mutation.
message ConstructionDeliveryFloorsResponse {
  string construction_site = 1;
  // The RESOLVED floors persisted on the pipeline, so the operator sees what the knob
  // actually became rather than what was sent.
  string buy_floor = 2;
  string resume_floor = 3;
  // changed is false when the verb was a no-op (both floors already matched).
  bool changed = 4;
}
```

Regenerate the Go stubs (daemon proto only — the Python routing service is unrelated):

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative pkg/proto/daemon/*.proto; echo "protoc=$?"
```
Expected: `protoc=0`. If `protoc` or the plugins are missing, run `make install-tools` first (Makefile:409-410).

- [ ] **Step 4: Add the daemon single-writer mutation**

In `gobot/internal/adapters/grpc/container_ops_construction.go`, after `MutateConstructionMaxWorkers` (:439), add:

```go
// ConstructionDeliveryFloorsResult reports the floors in force after the mutation.
type ConstructionDeliveryFloorsResult struct {
	ConstructionSite string
	BuyFloor         string
	ResumeFloor      string
	Changed          bool
}

// MutateConstructionDeliveryFloors sets the gate delivery fleet's supply buy/resume
// thresholds on the RUNNING construction pipeline for constructionSite, persisting them on
// the pipeline row (RULINGS #2) with no restart. The drain re-reads them off that row on
// every leg, so the tune takes effect on the next leg.
//
// An EMPTY floor argument leaves that dimension unchanged, so an operator can widen or
// narrow the hysteresis gap from either end without restating the other. Clearing a floor
// back to the armed default is done by naming the default explicitly, not by sending an
// empty string -- an empty argument that meant "reset" would make "leave unchanged"
// inexpressible, and those two intents must not share an encoding on a money-adjacent knob.
//
// Validation lives at the CLI boundary (strict tier check + the strictly-above-buy-floor
// gap check). This layer re-checks the ORDERING anyway, fail-closed: a caller that reached
// the daemon another way must not be able to persist a zero-gap hysteresis, which would
// reintroduce exactly the boundary chatter the second floor exists to prevent.
func (s *DaemonServer) MutateConstructionDeliveryFloors(ctx context.Context, constructionSite string, playerID int, buyFloor, resumeFloor string) (*ConstructionDeliveryFloorsResult, error) {
	if buyFloor != "" && !shared.IsValidSupply(buyFloor) {
		return nil, fmt.Errorf("invalid delivery buy floor %q: want one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE", buyFloor)
	}
	if resumeFloor != "" && !shared.IsValidSupply(resumeFloor) {
		return nil, fmt.Errorf("invalid delivery resume floor %q: want one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE", resumeFloor)
	}

	pipelineRepo := persistence.NewGormManufacturingPipelineRepository(s.db)

	pipeline, err := pipelineRepo.FindByConstructionSite(ctx, constructionSite, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to locate construction pipeline for %s: %w", constructionSite, err)
	}
	if pipeline == nil {
		return nil, fmt.Errorf("no active construction pipeline for %s (player %d) — start one before setting its delivery floors", constructionSite, playerID)
	}

	// An unset argument keeps the persisted value; the effective pair is what gets checked
	// and written, so a one-sided tune is still validated against the other side.
	nextBuy := pipeline.DeliveryBuyFloor()
	if buyFloor != "" {
		nextBuy = buyFloor
	}
	nextResume := pipeline.DeliveryResumeFloor()
	if resumeFloor != "" {
		nextResume = resumeFloor
	}

	// Fail closed on a collapsed gap. Both-unset is fine (the reader resolves the armed
	// MODERATE/HIGH defaults); one-sided is checked against the other side's resolved value.
	effectiveBuy, effectiveResume := gate.DefaultBuyFloor, gate.DefaultResumeFloor
	if nextBuy != "" {
		effectiveBuy = shared.ParseSupplyLevel(nextBuy)
	}
	if nextResume != "" {
		effectiveResume = shared.ParseSupplyLevel(nextResume)
	}
	if effectiveResume.Order() <= effectiveBuy.Order() {
		return nil, fmt.Errorf("delivery resume floor %s must be strictly above the buy floor %s — an equal or lower resume floor collapses the hysteresis into the single threshold that chatters at the supply boundary", effectiveResume, effectiveBuy)
	}

	changed := pipeline.DeliveryBuyFloor() != nextBuy || pipeline.DeliveryResumeFloor() != nextResume
	result := &ConstructionDeliveryFloorsResult{
		ConstructionSite: constructionSite,
		BuyFloor:         nextBuy,
		ResumeFloor:      nextResume,
		Changed:          changed,
	}
	if !changed {
		return result, nil // idempotent verb: nothing to persist
	}

	pipeline.SetDeliveryFloors(nextBuy, nextResume)
	if err := pipelineRepo.Update(ctx, pipeline); err != nil {
		return nil, fmt.Errorf("failed to persist delivery floors (buy %s, resume %s) for pipeline %s: %w", nextBuy, nextResume, pipeline.ID(), err)
	}
	return result, nil
}
```

Add the two imports this needs to that file if absent: `"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"` and `"github.com/andrescamacho/spacetraders-go/internal/domain/shared"`.

- [ ] **Step 5: Add the RPC handler**

In `gobot/internal/adapters/grpc/daemon_service_construction.go`, after `ConstructionWorkerCap` (:183), add:

```go
// ConstructionDeliveryFloors sets the gate delivery fleet's supply buy/resume thresholds on
// a running construction pipeline. Resolves the player like the sibling construction RPCs,
// then delegates the persisted-row mutation to the daemon — the single writer (RULINGS #3).
// The drain re-reads the floors off the pipeline row on every leg, so the tune converges on
// the next leg with no restart.
func (s *daemonServiceImpl) ConstructionDeliveryFloors(ctx context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error) {
	var pid int32
	if req.PlayerId != nil {
		pid = *req.PlayerId
	}
	playerID, err := s.resolvePlayerID(ctx, pid, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	buyFloor, resumeFloor := "", ""
	if req.BuyFloor != nil {
		buyFloor = *req.BuyFloor
	}
	if req.ResumeFloor != nil {
		resumeFloor = *req.ResumeFloor
	}

	result, err := s.daemon.MutateConstructionDeliveryFloors(ctx, req.ConstructionSite, playerID, buyFloor, resumeFloor)
	if err != nil {
		return nil, fmt.Errorf("failed to set construction delivery floors: %w", err)
	}

	return &pb.ConstructionDeliveryFloorsResponse{
		ConstructionSite: result.ConstructionSite,
		BuyFloor:         result.BuyFloor,
		ResumeFloor:      result.ResumeFloor,
		Changed:          result.Changed,
	}, nil
}
```

- [ ] **Step 6: Add the CLI client wrapper**

In `gobot/internal/adapters/cli/daemon_client_construction.go`, after `ConstructionWorkerCap` (:205), add:

```go
// ConstructionDeliveryFloors sends the gate delivery fleet's buy/resume floor tune to the
// daemon. The request is built and validated by the CLI boundary; this only carries it.
func (c *DaemonClient) ConstructionDeliveryFloors(ctx context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error) {
	resp, err := c.client.ConstructionDeliveryFloors(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	return resp, nil
}
```

- [ ] **Step 7: Add the flags, validation and dispatch**

In `gobot/internal/adapters/cli/construction_override.go`:

Extend `constructionOverrideFlags` (:194) with:

```go
	buyFloor    string
	resumeFloor string
```

Add beside `anyKnobSet` (:205):

```go
// anyFloorSet reports whether a PIPELINE-WIDE delivery floor was provided. It is
// deliberately separate from anyKnobSet: the floors and the per-good override are two
// different decisions on one verb, routed to two different RPCs (the per-good request
// requires a `good`, which pipeline-wide floors do not have).
func (f constructionOverrideFlags) anyFloorSet() bool {
	return f.buyFloor != "" || f.resumeFloor != ""
}
```

Add the builder and runner:

```go
// constructionDeliveryFloorsMutator is the narrow daemon surface the delivery-floor half of
// `construction override` needs. By construction it exposes ONLY the floors RPC — no
// pipeline restart/stop — so "no restart" is guaranteed by the surface this verb can reach.
type constructionDeliveryFloorsMutator interface {
	ConstructionDeliveryFloors(ctx context.Context, req *pb.ConstructionDeliveryFloorsRequest) (*pb.ConstructionDeliveryFloorsResponse, error)
}

// buildConstructionDeliveryFloorsRequest validates the pipeline-wide floor flags at the
// boundary and assembles the gRPC request.
//
// --good is REJECTED here rather than ignored. These floors are pipeline-wide, so a
// supplied good does nothing — and an operator who typed it believes it did something,
// which is exactly the class of silent no-op this design exists to remove.
//
// Tiers are validated strictly. shared.ParseSupplyLevel is deliberately lenient (unknown ->
// MODERATE) because it parses scanned market data; operator input is not, or a typo becomes
// a floor nobody chose.
//
// A resume floor that is not STRICTLY ABOVE the buy floor is rejected, naming both values.
// The domain would silently raise it, but silently correcting an operator leaves them with
// a wrong mental model of a knob they are actively tuning.
func buildConstructionDeliveryFloorsRequest(f constructionOverrideFlags, playerIdent *PlayerIdentifier) (*pb.ConstructionDeliveryFloorsRequest, error) {
	if f.site == "" {
		return nil, fmt.Errorf("--site is required (the construction site whose pipeline to tune)")
	}
	if f.good != "" {
		return nil, fmt.Errorf("--good cannot be combined with --buy-floor/--resume-floor: the delivery floors are PIPELINE-WIDE, not per-good (use --min-supply/--price-ceiling-mult for a per-good override)")
	}
	if f.clear {
		return nil, fmt.Errorf("--clear removes a per-good override; to reset the delivery floors, set them explicitly (--buy-floor MODERATE --resume-floor HIGH, the armed defaults)")
	}
	if _, err := parseMinSupplyFlag(f.buyFloor); err != nil {
		return nil, fmt.Errorf("invalid --buy-floor value %q: must be one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE", f.buyFloor)
	}
	if _, err := parseMinSupplyFlag(f.resumeFloor); err != nil {
		return nil, fmt.Errorf("invalid --resume-floor value %q: must be one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE", f.resumeFloor)
	}
	if f.buyFloor != "" && f.resumeFloor != "" {
		if manufacturing.SupplyLevel(f.resumeFloor).Order() <= manufacturing.SupplyLevel(f.buyFloor).Order() {
			return nil, fmt.Errorf("--resume-floor %s must be strictly above --buy-floor %s: an equal or lower resume floor collapses the hysteresis into the single threshold that chatters at the supply boundary", f.resumeFloor, f.buyFloor)
		}
	}

	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.ConstructionDeliveryFloorsRequest{
		ConstructionSite: f.site,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}
	// Only PROVIDED floors become non-nil, so an unset one leaves that dimension unchanged
	// and the gap can be tuned from either end.
	if f.buyFloor != "" {
		req.BuyFloor = &f.buyFloor
	}
	if f.resumeFloor != "" {
		req.ResumeFloor = &f.resumeFloor
	}
	return req, nil
}

// runConstructionDeliveryFloors sends the floor tune and formats the operator-facing result.
// It reports the RESOLVED floors now in force — what the knob became, not what was sent —
// and states the liveness, which is the operator's only evidence the tune took.
func runConstructionDeliveryFloors(ctx context.Context, client constructionDeliveryFloorsMutator, req *pb.ConstructionDeliveryFloorsRequest) (string, error) {
	resp, err := client.ConstructionDeliveryFloors(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to set the delivery floors on %s: %w", req.ConstructionSite, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• %s delivery floors are already buy=%s / resume=%s — unchanged.\n",
			resp.ConstructionSite, resp.BuyFloor, resp.ResumeFloor), nil
	}
	return fmt.Sprintf("✓ set the %s delivery floors to buy=%s / resume=%s. The drain re-reads them off the pipeline row on every leg, so this takes effect on the next leg; no restart, and it survives a daemon bounce.\n",
		resp.ConstructionSite, resp.BuyFloor, resp.ResumeFloor), nil
}
```

Register the flags in `newConstructionOverrideCommand` (after :369):

```go
	cmd.Flags().StringVar(&f.buyFloor, "buy-floor", "", "Gate DELIVERY fleet: buy while the terminal factory's supply is at or above this level (pipeline-wide; default MODERATE)")
	cmd.Flags().StringVar(&f.resumeFloor, "resume-floor", "", "Gate DELIVERY fleet: once paused, resume only when supply recovers to this level (pipeline-wide; default HIGH, must be above --buy-floor)")
```

Replace the body of the command's `RunE` dispatch so it routes to whichever RPC the flags name. Read the existing `RunE` (:335-362) first; the change is to insert the floors branch **before** the per-good branch:

```go
			// ONE VERB, TWO RPCs (adjudication #5). The pipeline-wide floors and the per-good
			// override are different decisions on different scopes — the per-good request
			// requires a `good`, which the floors do not have — so the verb dispatches on which
			// flags were set rather than forcing them into one request shape.
			if f.anyFloorSet() {
				req, err := buildConstructionDeliveryFloorsRequest(f, playerIdent)
				if err != nil {
					return err
				}
				client, err := connectDaemon()
				if err != nil {
					return err
				}
				defer client.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				msg, err := runConstructionDeliveryFloors(ctx, client, req)
				if err != nil {
					return err
				}
				fmt.Print(msg)
				return nil
			}
```

Extend the command's `Long` help with the new knobs, after the existing knob list:

```
Pipeline-wide gate DELIVERY fleet knobs (no --good; these apply to the whole pipeline):
  --buy-floor          buy while the terminal factory's supply is AT OR ABOVE this level (default MODERATE)
  --resume-floor       once paused, resume only when supply recovers to this level (default HIGH)

Two thresholds, not one: a single threshold chatters at the boundary — pause, one unit
regenerates, resume, immediately deplete. --resume-floor must be strictly above --buy-floor.
These are TUNABLES, not feature flags: they ship armed at MODERATE/HIGH and adjust a value
in a path that always runs. They are distinct from --min-supply, which is the pipeline's
READY-admission floor — a different decision at a different stage.

  spacetraders construction override --site X1-VB74-I55 --buy-floor LIMITED --resume-floor MODERATE
```

- [ ] **Step 8: Run tests to verify they pass**

```bash
cd gobot && go test -race ./internal/adapters/cli/... ./internal/adapters/grpc/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL`.

```bash
cd gobot && go build ./... ; echo "build=$?"; go vet ./... ; echo "vet=$?"
```
Expected: `build=0`, `vet=0`. (`go vet` compiles test files, so it catches stale call sites `go build` misses.)

- [ ] **Step 9: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(construction): --buy-floor/--resume-floor on construction override (live, no restart)

One verb, two RPCs. ConstructionGoodOverride requires a 'good', which the
pipeline-wide delivery floors do not have, so they get their own RPC and the CLI
dispatches on which flags were set. --good is REJECTED with the floors rather
than ignored: an operator who typed it believes it did something.

Validated strictly at the boundary. ParseSupplyLevel is lenient by design
(unknown -> MODERATE) because it parses scanned market data; operator input is
not, or a typo silently becomes a floor nobody chose. A resume floor that is not
strictly above the buy floor is rejected naming both values -- the domain would
raise it silently, but silently correcting an operator mid-tune leaves them with
a wrong model of the knob. The daemon re-checks the ordering fail-closed.

Named distinctly from --min-supply, which is the READY-admission floor: two
confusable supply thresholds would be their own opacity." -- gobot/pkg/proto/daemon/daemon.proto gobot/pkg/proto/daemon/daemon.pb.go gobot/pkg/proto/daemon/daemon_grpc.pb.go gobot/internal/adapters/grpc/container_ops_construction.go gobot/internal/adapters/grpc/daemon_service_construction.go gobot/internal/adapters/cli/daemon_client_construction.go gobot/internal/adapters/cli/construction_override.go gobot/internal/adapters/cli/construction_override_test.go
```

- [ ] **Step 10: Mutation-probe the hysteresis-gap guard**

The strictly-above check is the guard that keeps an operator from collapsing the hysteresis. Break it and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|if manufacturing.SupplyLevel(f.resumeFloor).Order() <= manufacturing.SupplyLevel(f.buyFloor).Order() {|if false {|' internal/adapters/cli/construction_override.go && grep -q 'if false {' internal/adapters/cli/construction_override.go && echo "MUTATION APPLIED" && go test ./internal/adapters/cli/ -run 'TestBuildConstructionDeliveryFloorsRequest' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/adapters/cli/construction_override.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestBuildConstructionDeliveryFloorsRequest_RejectsANonPositiveHysteresisGap`, then `RESTORED`.

---

### Task 6: `ProductionExecutor.BuyAtTerminalFactory` — pinned-source buy with every money guard unchanged

The delivery fleet buys at the factory phase 1's `TerminalFactory` resolved. That waypoint is already decided by the time the leg runs, so this path must **not** re-run `selectInputSource` — which would be free to pick a different market and quietly undo the topology resolution.

The money guards are the thing that must NOT change. `buyGood`'s tranche loop already re-checks `spendFloorBreached` and holds a cross-container spend reservation through `buyInputTranche` on **every** iteration, failing closed. Rather than write a second loop that could drift from it, extract the existing loop verbatim into `fillFromSource` and have both callers share it. The refactor is behaviour-preserving by construction: `buyGood` keeps calling it with exactly the arguments it computed before.

**Files:**
- Modify: `gobot/internal/application/manufacturing/services/production_executor.go` (`buyGood` :224-325)
- Create: `gobot/internal/application/manufacturing/services/production_executor_gate_buy.go`
- Test: `gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go`

**Interfaces:**
- Consumes: `NavigateAndDock`, `makeRoomForInputBuy`, `newHullFill`, `trancheAsk`, `spendFloorBreached`, `buyInputTranche`, `WithHullFillTarget`, `sourceModeEligible` — all existing, all unchanged.
- Produces: `(e *ProductionExecutor) fillFromSource(ctx context.Context, ship *navigation.Ship, good string, source *MarketLocatorResult, systemSymbol string, playerID int, mode inputSourceMode) (*ProductionResult, error)`; `(e *ProductionExecutor) BuyAtTerminalFactory(ctx context.Context, ship *navigation.Ship, good string, source *MarketLocatorResult, units int, systemSymbol string, playerID int, opContext *shared.OperationContext) (*ProductionResult, error)`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go`:

```go
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// selectorRefusingMarketRepo is the dock-race market with its SELECTOR lookup poisoned.
// BuyAtTerminalFactory buys at a source phase 1's TerminalFactory already resolved, so it
// must never consult the source selector — and the only way to prove a call did not happen
// is to make it fail loudly if it does.
type selectorRefusingMarketRepo struct {
	dockRaceMarketRepo
}

func (r *selectorRefusingMarketRepo) FindCheapestMarketSelling(_ context.Context, _, _ string, _ int) (*market.CheapestMarketResult, error) {
	return nil, errors.New("source selector consulted: BuyAtTerminalFactory must buy at the PINNED terminal factory")
}

// newPinnedSourceExecutor mirrors newDockRaceExecutor but with the selector poisoned, and
// optionally a live-treasury client so the money floor can be driven. cargoCapacity 40,
// market trade_volume 10, ask 10 — same numbers as the hull-fill harness.
func newPinnedSourceExecutor(t *testing.T, apiClient domainPorts.APIClient) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()

	repo := &dockRaceShipRepo{
		location:      dockRaceOrigin,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
	}
	marketRepo := &selectorRefusingMarketRepo{}
	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		marketRepo,
		NewMarketLocator(marketRepo, nil, nil, nil),
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		apiClient,
	)
	return executor, repo, mediator
}

// pinnedSource is the terminal-factory quote phase 1's TerminalFactory hands back.
func pinnedSource() *MarketLocatorResult {
	return &MarketLocatorResult{
		WaypointSymbol: dockRaceMarketWP,
		Supply:         "HIGH",
		Activity:       "STRONG",
		Price:          10,
		TradeVolume:    10,
	}
}

// THE PINNING. The buy happens at the source it was GIVEN. Consulting the selector would
// be free to pick a different market and silently undo the topology resolution — so the
// selector is poisoned, and reaching it fails the test.
func TestBuyAtTerminalFactory_BuysAtThePinnedSourceWithoutConsultingTheSelector(t *testing.T) {
	executor, repo, mediator := newPinnedSourceExecutor(t, nil)

	result, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, pinnedSource(), 30, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result == nil || result.WaypointSymbol != dockRaceMarketWP {
		t.Fatalf("result = %+v, want the buy recorded at the pinned terminal factory %s", result, dockRaceMarketWP)
	}
	if result.QuantityAcquired != 30 {
		t.Fatalf("QuantityAcquired = %d, want the 30 units planned", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() != 3 {
		t.Fatalf("30 units at trade_volume 10 must take 3 tranche buys, got %d", mediator.purchaseAttempts())
	}
}

// The units the fill planner allocated are a CAP, not a suggestion: buying past them would
// over-supply a material whose bill the trip already sized, or steal hold space the trip
// allocated to the other material.
func TestBuyAtTerminalFactory_NeverBuysPastTheUnitsItWasGiven(t *testing.T) {
	executor, repo, _ := newPinnedSourceExecutor(t, nil)

	result, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, pinnedSource(), 12, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired != 12 {
		t.Fatalf("QuantityAcquired = %d, want exactly the 12 units allocated", result.QuantityAcquired)
	}
}

// MONEY GUARD, UNCHANGED. The per-tranche working-capital floor still governs this buy and
// still fails CLOSED: when the next tranche would breach, the fill stops and delivers what
// is aboard rather than forcing the buy. Treasury is scripted to deplete mid-fill.
func TestBuyAtTerminalFactory_StopsWhenTheNextTrancheWouldBreachTheSpendFloor(t *testing.T) {
	// Credits fall below the enforced floor after the first tranche, so tranche 2 is refused.
	depleting := &sequentialCreditsAPIClient{credits: []int{
		effectiveReserveFloor() + 1000,
		effectiveReserveFloor() - 1,
	}}
	executor, repo, mediator := newPinnedSourceExecutor(t, depleting)

	result, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, pinnedSource(), 40, "X1-DR", 1, nil)
	if err != nil {
		t.Fatalf("BuyAtTerminalFactory returned error: %v", err)
	}
	if result.QuantityAcquired >= 40 {
		t.Fatalf("QuantityAcquired = %d — the fill ran to completion through a breached spend floor", result.QuantityAcquired)
	}
	if mediator.purchaseAttempts() >= 4 {
		t.Fatalf("purchaseAttempts = %d — the floor must stop the loop, not merely slow it", mediator.purchaseAttempts())
	}
}

// FAIL CLOSED on a source that cannot be bought from. A zero trade volume means no
// transaction size exists; buying blind against it is how a fill spins.
func TestBuyAtTerminalFactory_RefusesAnUnbuyableSource(t *testing.T) {
	executor, repo, mediator := newPinnedSourceExecutor(t, nil)

	zeroVolume := pinnedSource()
	zeroVolume.TradeVolume = 0
	if _, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, zeroVolume, 30, "X1-DR", 1, nil); err == nil {
		t.Fatal("BuyAtTerminalFactory accepted a source with trade_volume 0; want a refusal")
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("purchaseAttempts = %d — nothing may be bought against an unbuyable source", mediator.purchaseAttempts())
	}
}

// A nil source, or a non-positive unit allocation, is a caller bug. Refuse rather than
// resolve a source ourselves — resolving one here is exactly the pinning this method exists
// to preserve.
func TestBuyAtTerminalFactory_RefusesANilSourceOrANonPositiveAllocation(t *testing.T) {
	executor, repo, _ := newPinnedSourceExecutor(t, nil)

	if _, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, nil, 30, "X1-DR", 1, nil); err == nil {
		t.Fatal("BuyAtTerminalFactory accepted a nil source; want a refusal, never a self-resolved fallback")
	}
	if _, err := executor.BuyAtTerminalFactory(context.Background(), repo.buildShip(),
		dockRaceGood, pinnedSource(), 0, "X1-DR", 1, nil); err == nil {
		t.Fatal("BuyAtTerminalFactory accepted a 0-unit allocation; want a refusal")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/manufacturing/services/ -run 'TestBuyAtTerminalFactory' 2>&1 | tail -20`
Expected: FAIL — `executor.BuyAtTerminalFactory undefined`.

- [ ] **Step 3: Extract the shared tranche loop (behaviour-preserving)**

In `gobot/internal/application/manufacturing/services/production_executor.go`, replace everything in `buyGood` **after** the `resolveInputSource` block (i.e. from `playerIDValue := shared.MustNewPlayerID(playerID)` at :241 through `return fill.result(), nil` at :324) with a single delegation, and move the removed body verbatim into a new method:

```go
	return e.fillFromSource(ctx, ship, node.Good, marketResult, systemSymbol, playerID, mode)
}

// fillFromSource navigates to source, makes room, and runs the tranche loop until the hold,
// the trip target or a guard stops it.
//
// It is shared by the SELECTED-source path (buyGood, which resolves the market first) and
// the PINNED-source path (BuyAtTerminalFactory, whose market phase 1's topology already
// resolved). Extracted rather than duplicated on purpose: every money guard in this loop is
// re-checked per iteration and fails closed, and a second copy would be free to drift from
// this one silently — which is how a guard stops guarding without any test noticing.
//
// Behaviour is unchanged from the inline version: same order, same guards, same stop
// conditions. Only the caller's source resolution moved out.
func (e *ProductionExecutor) fillFromSource(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	source *MarketLocatorResult,
	systemSymbol string,
	playerID int,
	mode inputSourceMode,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	playerIDValue := shared.MustNewPlayerID(playerID)

	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), source.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	updatedShip, roomFreed := e.makeRoomForInputBuy(ctx, updatedShip, good, playerIDValue)
	if !roomFreed {
		return &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: source.WaypointSymbol}, nil
	}

	// A zero-volume market cannot be bought from.
	if source.TradeVolume <= 0 {
		return nil, fmt.Errorf("trade volume is zero for %s", good)
	}

	fill := newHullFill(ctx, updatedShip, source, good)
	for {
		trancheQty := fill.nextTranche()
		if trancheQty <= 0 {
			break // STOP: hold full, or fill target / remaining bill met
		}

		ask := source.Price
		if fill.loopFill {
			liveAsk, ok := e.trancheAsk(ctx, fill, systemSymbol, playerID, mode)
			if !ok {
				break // STOP: unreadable ask or ceiling breach — deliver what is aboard, park the rest
			}
			ask = liveAsk
		}

		// Both money guards are re-checked EVERY iteration against live treasury and fail
		// CLOSED (RULINGS #4): once the NEXT tranche would breach, the loop stops and delivers
		// what is aboard rather than forcing the buy.
		projectedCost := trancheQty * ask
		if breached, enforcedFloor := e.spendFloorBreached(ctx, playerID, projectedCost); breached {
			logSpendFloorStop(ctx, good, source.WaypointSymbol, fill.acquired, projectedCost, enforcedFloor)
			break
		}

		purchaseCmd := &shipCargo.PurchaseCargoCommand{
			ShipSymbol: updatedShip.ShipSymbol(),
			GoodSymbol: good,
			Units:      trancheQty,
			PlayerID:   playerIDValue,
		}

		response, parked, err := e.buyInputTranche(ctx, purchaseCmd, fill, playerID, projectedCost)
		if err != nil {
			return nil, fmt.Errorf("failed to purchase cargo: %w", err)
		}
		if parked {
			break // STOP: concurrent cap (reserveConcurrentSpendOrPark logged the cause)
		}
		if response == nil {
			// Empty tranche persisted across the retry bound — the market is drained.
			logEmptyTrancheStop(ctx, good, source.WaypointSymbol, fill.acquired)
			break
		}

		fill.record(response.UnitsAdded, response.TotalCost)
		logger.Log("INFO", fmt.Sprintf("Purchased %d units of %s for %d credits", response.UnitsAdded, good, response.TotalCost), map[string]interface{}{
			"good":       good,
			"quantity":   response.UnitsAdded,
			"total_cost": response.TotalCost,
			"market":     source.WaypointSymbol,
		})

		if !fill.loopFill {
			break // single-tranche (goods-factory input) path: exactly one buy, unchanged
		}
		if response.UnitsAdded <= 0 {
			break // safety: no forward progress (never spin)
		}
	}

	return fill.result(), nil
}
```

Verify the extraction changed nothing before going further:

```bash
cd gobot && go test -race ./internal/application/manufacturing/services/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL`. Every pre-existing hull-fill, spend-floor, price-ceiling and empty-tranche test must still pass **before** anything new is added. If one fails here, the extraction was not verbatim — fix that, do not adjust the test.

- [ ] **Step 4: Write `BuyAtTerminalFactory`**

Create `gobot/internal/application/manufacturing/services/production_executor_gate_buy.go`:

```go
package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// BuyAtTerminalFactory buys `units` of `good` at a PINNED source — the waypoint phase 1's
// GateTopology.TerminalFactory already resolved as this era's exporter of the good.
//
// It deliberately does NOT call selectInputSource. That selector is free to pick a
// different market, and doing so here would silently undo the topology resolution: the
// delivery fleet's whole contract is that it buys the terminal factory's OUTPUT, and a
// source chosen by price could be any market in the system. The caller resolved the
// waypoint by ROLE; this method's job is to honour that, not to re-decide it.
//
// EVERY MONEY GUARD IS UNCHANGED. The fill runs through the same fillFromSource loop the
// selected-source path uses, so the per-tranche working-capital floor (spendFloorBreached,
// re-read against live treasury) and the cross-container concurrent-spend reservation
// (buyInputTranche) both still govern every tranche, and both still fail CLOSED — the loop
// stops and delivers what is aboard rather than forcing a buy. Nothing here reads, moves or
// weakens a floor (RULINGS #4).
//
// The price ceiling also still applies per tranche (mode sourceModeEligible). The spec says
// price is deliberately not a gate for this fleet, and supply-anchoring does pace against
// our own market impact — but removing an existing guard is not this phase's to do, and the
// degradation is graceful: a laddering ask stops the fill and the hull delivers what it has.
//
// units is a CAP from the trip plan, not a target to chase: exceeding it would over-supply a
// material whose bill the trip already sized, or consume hold space allocated to the other
// material on a mixed trip.
//
// Refuses (error, nil result) on a nil source, a non-positive allocation, or a source with
// no transaction size. Each is a caller bug, and the alternative — resolving a source here —
// is precisely the pinning this method exists to preserve.
func (e *ProductionExecutor) BuyAtTerminalFactory(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	source *MarketLocatorResult,
	units int,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext,
) (*ProductionResult, error) {
	if source == nil {
		return nil, fmt.Errorf("cannot buy %s: no terminal factory was resolved — refusing to pick a source here", good)
	}
	if source.WaypointSymbol == "" {
		return nil, fmt.Errorf("cannot buy %s: the resolved terminal factory has no waypoint", good)
	}
	if units <= 0 {
		return nil, fmt.Errorf("cannot buy %s at %s: the trip allocated %d units", good, source.WaypointSymbol, units)
	}
	if source.TradeVolume <= 0 {
		return nil, fmt.Errorf("cannot buy %s at %s: the market reports no per-transaction trade volume", good, source.WaypointSymbol)
	}

	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	// Stamp the trip's allocation as the fill target so the tranche loop tops the hold up
	// toward it (bounded by hull capacity) instead of carrying a single trade-volume tranche.
	// fraction 0 resolves to the full hull; units is the binding cap.
	ctx = WithHullFillTarget(ctx, units, 0)

	// sourceModeEligible keeps the per-tranche price-ceiling re-check live, exactly as it is
	// on the selected-source path. Passing a weaker mode would disable an existing guard.
	return e.fillFromSource(ctx, ship, good, source, systemSymbol, playerID, sourceModeEligible)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd gobot && go test -race ./internal/application/manufacturing/services/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL` — the new `TestBuyAtTerminalFactory_*` tests plus every pre-existing test in the package.

- [ ] **Step 6: Commit (BEFORE the probe)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(manufacturing): BuyAtTerminalFactory buys at the pinned exporter, guards unchanged

The delivery fleet buys the terminal factory's OUTPUT, at the waypoint phase 1's
TerminalFactory resolved by EXPORT role. This path therefore does NOT call
selectInputSource: that selector picks by price and would be free to choose any
market in the system, silently undoing the topology resolution the caller just
performed.

The tranche loop is EXTRACTED from buyGood into fillFromSource and shared, not
duplicated. Every money guard in it is re-checked per iteration and fails closed
-- the per-tranche working-capital floor against live treasury, and the
cross-container concurrent-spend reservation -- and a second copy of that loop
would be free to drift from this one with no test noticing. buyGood's behaviour
is unchanged; only its source resolution moved out.

The per-tranche price ceiling stays live (sourceModeEligible). The spec says
price is not a gate for this fleet, but removing an existing guard is not this
phase's to do, and the degradation is graceful: a laddering ask stops the fill
and the hull delivers what is aboard.

units is a cap from the trip plan, not a target: over-buying would over-supply a
sized bill or eat the hold space a mixed trip allocated to the other material." -- gobot/internal/application/manufacturing/services/production_executor.go gobot/internal/application/manufacturing/services/production_executor_gate_buy.go gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go
```

- [ ] **Step 7: Mutation-probe the money floor on this path**

The point of sharing `fillFromSource` is that the floor governs the gate buy too. Prove it. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && perl -0pi -e 's/\t\tif breached, enforcedFloor := e\.spendFloorBreached\(ctx, playerID, projectedCost\); breached \{/\t\tif breached, enforcedFloor := false, 0; breached {/' internal/application/manufacturing/services/production_executor.go && grep -q 'breached, enforcedFloor := false, 0' internal/application/manufacturing/services/production_executor.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/services/ -run 'TestBuyAtTerminalFactory|TestBuyGood_HullFill' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/services/production_executor.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestBuyAtTerminalFactory_StopsWhenTheNextTrancheWouldBreachTheSpendFloor`, then `RESTORED`. A run that names **zero** tests means the gate buy is spending unguarded — stop immediately; this is the highest-severity failure mode in the plan.

- [ ] **Step 8: Mutation-probe the source pinning**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && perl -0pi -e 's/\treturn e\.fillFromSource\(ctx, ship, good, source, systemSymbol, playerID, sourceModeEligible\)/\tresolvedCtx, selected, mode, parked, err := e.resolveInputSource(ctx, good, systemSymbol, playerID)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tif parked != nil {\n\t\treturn parked, nil\n\t}\n\treturn e.fillFromSource(resolvedCtx, ship, good, selected, systemSymbol, playerID, mode)/' internal/application/manufacturing/services/production_executor_gate_buy.go && grep -q 'resolveInputSource' internal/application/manufacturing/services/production_executor_gate_buy.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/services/ -run 'TestBuyAtTerminalFactory' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/application/manufacturing/services/production_executor_gate_buy.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestBuyAtTerminalFactory_BuysAtThePinnedSourceWithoutConsultingTheSelector`, then `RESTORED`.

---

### Task 7: Tag the 4 GATE hulls by role at purchase; widen the observer and the re-tag guards

Role assignment goes through `AssignFleet` — the single write path for `dedicated_fleet`, defended by `preserveDedicatedFleetTag` against concurrent ship saves. `BuyForConstruction` already calls it; the change is *which* tag it writes.

Two orphaning hazards come with the new tags, and both must close in this same task or role-tagged hulls become invisible:

1. **The observer.** `observeFleetShape` counts `GateWorkers` off `== manufacturingFleetTag`. A role-tagged hull would count as nothing, `GateWorkers` would under-report, and the ramp would keep buying past its target of 4.
2. **The re-tag guards.** `retagGateWorkers` skips any hull whose tag `!= manufacturingFleetTag`, so both the surplus release and the EXPANSION trade redirect would silently refuse to touch a role-tagged hull, stranding it dedicated forever.

Adjudication #4 stands: legacy `manufacturing`-tagged hulls carry no role and stay on the existing path. Phase 3 re-roles them.

**Files:**
- Modify: `gobot/internal/adapters/grpc/bootstrap_ports_gate.go` (`BuyForConstruction` :358, `retagGateWorkers` :329)
- Modify: `gobot/internal/adapters/grpc/bootstrap_ports_observe.go` (`observeFleetShape` :114)
- Test: `gobot/internal/adapters/grpc/bootstrap_ports_gate_role_test.go` (create)

**Interfaces:**
- Consumes: `gate.NextRole`, `gate.IsGateFleetTag`, `gate.ParseFleetTag`, `gate.Role.FleetTag()` from Task 1; `navigation.ShipRepository.FindAllByPlayer` / `AssignFleet`.
- Produces: `nextGateRole(ctx context.Context, shipRepo navigation.ShipRepository, playerID shared.PlayerID) (gate.Role, error)`; unchanged external signatures for `BuyForConstruction`, `ReleaseSurplusGateWorkers`, `ReleaseGateWorkersToTrade`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/adapters/grpc/bootstrap_ports_gate_role_test.go`:

```go
package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The GATE phase buys 4 hulls in D/F/F/D order, and the role is derived from the LIVE
// fleet, not from a cursor: a restart mid-ramp must resume the order correctly.
func TestNextGateRole_DerivesTheOrderFromTheLiveFleet(t *testing.T) {
	cases := []struct {
		name  string
		fleet []*navigation.Ship
		want  gate.Role
	}{
		{"no gate hulls yet", nil, gate.RoleDelivery},
		{"one delivery", []*navigation.Ship{
			reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
		}, gate.RoleFactory},
		{"one of each", []*navigation.Ship{
			reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
			reclaimHull(t, "GATE-8", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
		}, gate.RoleFactory},
		{"one delivery two factory", []*navigation.Ship{
			reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
			reclaimHull(t, "GATE-8", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
			reclaimHull(t, "GATE-9", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
		}, gate.RoleDelivery},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeReclaimShipRepo{all: tc.fleet}
			got, err := nextGateRole(context.Background(), repo, shared.MustNewPlayerID(1))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// A LEGACY manufacturing hull carries no role, so it must not shift the D/F/F/D order:
// counting it as either role would skew every subsequent purchase.
func TestNextGateRole_LegacyHullsDoNotShiftTheOrder(t *testing.T) {
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{
		reclaimHull(t, "MFG-7", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "MFG-8", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit),
	}}
	got, err := nextGateRole(context.Background(), repo, shared.MustNewPlayerID(1))
	require.NoError(t, err)
	require.Equal(t, gate.RoleDelivery, got, "the first ROLE-tagged hull is delivery regardless of legacy hulls")
}

// FAIL CLOSED on an unreadable fleet: a role derived from an unknown count would be a
// guess, and a mis-roled hull is a hull working the wrong half of the operation.
func TestNextGateRole_FailsClosedOnAFleetReadError(t *testing.T) {
	repo := &fakeReclaimShipRepo{findErr: context.DeadlineExceeded}
	_, err := nextGateRole(context.Background(), repo, shared.MustNewPlayerID(1))
	require.Error(t, err)
}

// THE OBSERVER. All three gate tags increment GateWorkers. A role-tagged hull counted as
// nothing would under-report the workforce and let the staged ramp buy past its target.
func TestObserveFleetShape_CountsEveryGateTagAsAGateWorker(t *testing.T) {
	ships := []*navigation.Ship{
		reclaimHull(t, "MFG-7", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "GATE-8", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "GATE-9", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "CONTRACT-4", 40, contractFleetTag, navigation.NavStatusInOrbit),
	}
	var obs bootstrapCmd.Observation
	observeFleetShape(ships, &obs)

	require.Equal(t, 3, obs.GateWorkers, "all three gate tags must count; an undercount lets the ramp over-buy")
	require.Len(t, obs.GateWorkerHulls, 3, "GateWorkerHulls must stay in lock-step with GateWorkers")
	require.Len(t, obs.Haulers, 1, "a contract hauler must not be absorbed into the gate count")
}

// THE RE-TAG GUARDS. Surplus release and the EXPANSION trade redirect must be able to
// touch a ROLE-tagged hull. Guarding on the legacy tag alone would strand every role-tagged
// hull dedicated forever, with no path back to the idle pool or the trade fleet.
func TestRetagGateWorkers_ReleasesRoleTaggedHullsNotOnlyLegacyOnes(t *testing.T) {
	delivery := reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit)
	factory := reclaimHull(t, "GATE-8", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit)
	legacy := reclaimHull(t, "MFG-9", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{delivery, factory, legacy}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"GATE-7", "GATE-8", "MFG-9"})
	require.NoError(t, err)
	require.Equal(t, 3, released, "every gate tag must be releasable, or a role-tagged hull is stranded dedicated")
	require.Equal(t, []assignFleetCall{
		{symbol: "GATE-7", fleet: ""},
		{symbol: "GATE-8", fleet: ""},
		{symbol: "MFG-9", fleet: ""},
	}, repo.assigned)
}

// Widening the guard must NOT widen it to foreign fleets: a contract or trade hull is
// never a gate hull, and re-tagging one would be a poach (RULINGS #7).
func TestRetagGateWorkers_StillRefusesForeignFleets(t *testing.T) {
	foreign := reclaimHull(t, "CONTRACT-4", 40, contractFleetTag, navigation.NavStatusInOrbit)
	traded := reclaimHull(t, "TRADE-5", 40, tradeFleetTag, navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{foreign, traded}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"CONTRACT-4", "TRADE-5"})
	require.NoError(t, err)
	require.Equal(t, 0, released, "a foreign-fleet hull must never be re-tagged by a gate path")
	require.Nil(t, repo.assigned)
}

// The mid-task guard survives the widening: a role-tagged hull in transit is mid-haul and
// must never be yanked out from under its leg.
func TestRetagGateWorkers_StillSkipsARoleTaggedHullMidHaul(t *testing.T) {
	midHaul := reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInTransit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{midHaul}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"GATE-7"})
	require.NoError(t, err)
	require.Equal(t, 0, released)
	require.Nil(t, repo.assigned, "a hull mid-haul must finish its leg before any reassignment")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/adapters/grpc/ -run 'TestNextGateRole|TestObserveFleetShape_CountsEveryGateTag|TestRetagGateWorkers' 2>&1 | tail -20`
Expected: FAIL — `undefined: nextGateRole`, and (once that compiles) `TestObserveFleetShape_CountsEveryGateTagAsAGateWorker` reporting `GateWorkers = 1, want 3`.

- [ ] **Step 3: Derive the role and tag at purchase**

In `gobot/internal/adapters/grpc/bootstrap_ports_gate.go`, add the import `"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"` and replace `BuyForConstruction` (:356-371) with:

```go
// nextGateRole is the role the next gate hull should carry, derived from the LIVE fleet.
//
// Reading the counts rather than keeping a cursor is what makes the D/F/F/D order correct
// after a daemon restart, a hull loss, or an operator re-tag: there is no stored position to
// fall out of step with the fleet. Legacy manufacturing hulls are deliberately NOT counted —
// they carry no role (phase 3 re-roles them), and folding them into either count would skew
// every subsequent purchase.
//
// FAILS CLOSED on an unreadable fleet. A role guessed from an unknown count is a hull
// working the wrong half of the operation, which is worse than a purchase deferred one tick.
func nextGateRole(ctx context.Context, shipRepo navigation.ShipRepository, playerID shared.PlayerID) (gate.Role, error) {
	ships, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("cannot derive the next gate role from an unreadable fleet: %w", err)
	}
	delivery, factory := 0, 0
	for _, ship := range ships {
		if ship == nil {
			continue
		}
		role, ok := gate.ParseFleetTag(ship.DedicatedFleet())
		if !ok {
			continue // legacy or foreign: carries no role
		}
		if role == gate.RoleDelivery {
			delivery++
		} else {
			factory++
		}
	}
	return gate.NextRole(delivery, factory), nil
}

// BuyForConstruction buys one hull (reuse the asset-agnostic batch-purchase path) and
// dedicates it to the gate fleet under its ROLE tag, so the drain claims it as that role's
// worker.
//
// The role is derived from the live fleet BEFORE the buy, so a failure to resolve it costs
// a deferred purchase rather than a mis-roled hull. The order is delivery, factory, factory,
// delivery: first hull delivery so any already-accumulated factory stock starts moving
// immediately, and the interleave means stopping after two leaves one of each rather than
// two of the same — the state that actually occurs when treasury is tight.
//
// AssignFleet remains the single write path for the tag (RULINGS #3); only the value written
// changed.
func (a *bootstrapGateWorkerAcquirer) BuyForConstruction(ctx context.Context, playerID int, shipType, yard string) (bootstrapCmd.BuyResult, error) {
	pid, perr := shared.NewPlayerID(playerID)
	if perr != nil {
		return bootstrapCmd.BuyResult{}, perr
	}
	// Resolve the role BEFORE spending: an unreadable fleet defers the buy instead of
	// producing a hull tagged by guess.
	role, rerr := nextGateRole(ctx, a.shipRepo, pid)
	if rerr != nil {
		return bootstrapCmd.BuyResult{}, rerr
	}

	bought, err := a.bootstrapAcquirer.Buy(ctx, playerID, shipType, yard)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	if derr := a.shipRepo.AssignFleet(ctx, bought.ShipSymbol, role.FleetTag(), pid); derr != nil {
		return bought, derr
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Dedicated gate worker %s to the %s role (fleet tag %q)", bought.ShipSymbol, role, role.FleetTag()), map[string]interface{}{
		"ship": bought.ShipSymbol, "gate_role": role.String(), "dedicated_fleet": role.FleetTag(),
	})
	return bought, nil
}
```

Add `"github.com/andrescamacho/spacetraders-go/internal/application/common"` to the file's imports if absent.

- [ ] **Step 4: Widen the re-tag guard**

In the same file, in `retagGateWorkers` (:329), replace:

```go
		if ship.DedicatedFleet() != manufacturingFleetTag {
			continue // re-tagged/adopted since the observation → skip
		}
```

with:

```go
		// Any GATE tag qualifies — the two role tags and the legacy one. Guarding on the
		// legacy tag alone would strand every role-tagged hull dedicated forever: neither the
		// surplus release nor the EXPANSION trade redirect could ever touch it, and nothing
		// else writes that column. Still deliberately NARROW: a contract or trade hull is not
		// a gate hull, and re-tagging one here would be a poach (RULINGS #7).
		if !gate.IsGateFleetTag(ship.DedicatedFleet()) {
			continue // foreign fleet, undedicated, or re-tagged since the observation → skip
		}
```

- [ ] **Step 5: Widen the observer**

In `gobot/internal/adapters/grpc/bootstrap_ports_observe.go`, in `observeFleetShape` (:114), replace `} else if s.DedicatedFleet() == manufacturingFleetTag {` with:

```go
		} else if gate.IsGateFleetTag(s.DedicatedFleet()) {
			// EVERY gate tag — the delivery role, the factory role, and the legacy one — is a
			// gate worker. This total is the worker-sizing "have" count, so a role-tagged hull
			// counted as nothing would under-report the workforce and let the staged top-up buy
			// past gateWorkerTarget. Appended in lock-step so len(GateWorkerHulls)==GateWorkers.
```

keeping the existing body (the `obs.GateWorkers++` and the `GateWorkerHulls` append) unchanged. Add the `gate` import.

- [ ] **Step 6: Run tests to verify they pass**

```bash
cd gobot && go test -race ./internal/adapters/grpc/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL`. Every pre-existing surplus-release and trade-redirect test must still pass — the guard was widened, not replaced, so a legacy hull behaves exactly as before.

```bash
cd gobot && go test -race ./internal/application/bootstrap/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL`. The ramp's sizing arithmetic reads `obs.GateWorkers`, which now counts three tags.

- [ ] **Step 7: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(bootstrap): tag GATE hulls by role at purchase, and stop role-tagged hulls being orphaned

BuyForConstruction now writes the ROLE tag (gate-delivery / gate-factory) in
D/F/F/D order, derived from the LIVE fleet rather than a cursor so a restart
mid-ramp resumes the order correctly. The role resolves BEFORE the spend: an
unreadable fleet defers the purchase instead of producing a hull tagged by guess.
AssignFleet is still the single write path; only the value changed.

Two orphaning hazards close with it, and both had to land in the same commit:

- The observer counted GateWorkers off the legacy tag alone, so a role-tagged
  hull counted as nothing and the staged ramp would have bought past its target
  of 4. All three gate tags now increment it.
- retagGateWorkers skipped any hull not carrying the legacy tag, so neither the
  surplus release nor the EXPANSION trade redirect could ever touch a role-tagged
  hull -- and nothing else writes that column, so it would have stayed dedicated
  forever. The guard now admits any GATE tag, and still refuses foreign fleets.

Legacy manufacturing hulls keep carrying no role and stay on the existing path;
re-roling live hulls is phase 3's reallocation." -- gobot/internal/adapters/grpc/bootstrap_ports_gate.go gobot/internal/adapters/grpc/bootstrap_ports_observe.go gobot/internal/adapters/grpc/bootstrap_ports_gate_role_test.go
```

- [ ] **Step 8: Mutation-probe the observer widening**

An under-counted workforce is a silent over-buy. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|} else if gate.IsGateFleetTag(s.DedicatedFleet()) {|} else if s.DedicatedFleet() == manufacturingFleetTag {|' internal/adapters/grpc/bootstrap_ports_observe.go && grep -q 'DedicatedFleet() == manufacturingFleetTag' internal/adapters/grpc/bootstrap_ports_observe.go && echo "MUTATION APPLIED" && go test ./internal/adapters/grpc/ -run 'TestObserveFleetShape' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/adapters/grpc/bootstrap_ports_observe.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestObserveFleetShape_CountsEveryGateTagAsAGateWorker`, then `RESTORED`.

- [ ] **Step 9: Mutation-probe the re-tag guard widening**

A stranded hull is invisible until someone counts hulls by hand. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|if !gate.IsGateFleetTag(ship.DedicatedFleet()) {|if ship.DedicatedFleet() != manufacturingFleetTag {|' internal/adapters/grpc/bootstrap_ports_gate.go && grep -q 'DedicatedFleet() != manufacturingFleetTag' internal/adapters/grpc/bootstrap_ports_gate.go && echo "MUTATION APPLIED" && go test ./internal/adapters/grpc/ -run 'TestRetagGateWorkers' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/adapters/grpc/bootstrap_ports_gate.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestRetagGateWorkers_ReleasesRoleTaggedHullsNotOnlyLegacyOnes`, then `RESTORED`.

---

### Task 8: Role-aware drain — claim under the hull's own tag, and run the delivery leg

> 🛑 **STOP — READ THE FLAGGED SECTION BEFORE EXECUTING THIS TASK.**
> `[FLAGS]` item 1 (near the end of this document, under `## Flagged`) is a **REQUIRED AMENDMENT
> to Task 8, Step 5**, and it must be applied before you run this task.
>
> The defect: `deliverGateLeg` as drafted below returns **without** going through
> `completeSupply` / `completeOrDefer`, which leaves its task `EXECUTING` forever and stalls the
> drain **silently** — no error, no log, just a worker that never comes back. The corrected code
> is in that flagged item.
>
> This pointer exists because the amendment sits ~900 lines *after* the body it corrects, and an
> implementer reading top-down would otherwise ship the stalling version. Apply the amendment,
> then execute.

**The single most valuable finding from the design pass, and the one this task exists to honour:** `ClaimShip` authorizes a NEW claim only when `model.DedicatedFleet == operation` (`adapters/api/ship_repository_claims.go`, the guard that returns `ShipDedicatedToOtherFleetError`). A role-tagged hull claimed under the drain's **default** identity is rejected at the DB and silently never works — a defect that ships green and does nothing. Discovery must query every gate fleet tag, and each hull must be claimed under **its own** tag.

The allowlist is deliberate: an undedicated hull still claims under the drain's identity, and a foreign-pinned hull is **never** claimed under its own tag — which would otherwise defeat the no-poach guard entirely.

Adjudication #6 is an instruction for this task: **READ the surrounding builder in `main.go` before adding the wiring line**, do not pattern-match. At `cmd/spacetraders-daemon/main.go:996` (`SetConstructionSiteSource`), `goodsMarketLocator`, `constructionExecutor` and `goods.ExportToImportMap` are all already in scope — confirm that before writing.

The floors are read off the pipeline row **on every leg**, never cached at handler construction. That per-leg read is the whole pattern-C property, and Step 1 pins it with a test that fails if someone hoists it.

**Files:**
- Create: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go`
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go` (`selectHaulers` :54, `constructionLot` :111)
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator.go` (`runSupplyWorker` :512, handler fields, `SetGateDelivery`)
- Modify: `gobot/cmd/spacetraders-daemon/main.go` (one wiring line after :996)
- Test: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go`

**Interfaces:**
- Consumes: `gate.IsGateFleetTag`, `gate.RoleFleetTags`, `gate.NewBuyPolicy`, `gate.PlanFill`, `gate.Material`, `gate.Decision` (Tasks 1-3); `(*ManufacturingPipeline).DeliveryBuyFloor/DeliveryResumeFloor` (Task 4); `(*ProductionExecutor).BuyAtTerminalFactory` (Task 6); `(*GateTopology).TerminalFactory` (phase 1); `ConstructionProducer.DeliverToConstructionSite`.
- Produces: `GateTopologyResolver`, `GateBuyer` interfaces; `(h *RunConstructionCoordinatorHandler) SetGateDelivery(topology GateTopologyResolver, buyer GateBuyer)`; `claimIdentityFor(cmd, ship) string`; `deliverGateLeg(ctx, cmd, systemSymbol, lot, playerID) bool`; `constructionLot.claimIdentity`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go`:

```go
package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// CLAIM IDENTITY -- the defect that ships green and does nothing. ClaimShip authorizes a
// NEW claim only when the hull's dedicated_fleet EQUALS the operation string. A role-tagged
// hull claimed under the drain's DEFAULT identity is rejected at the DB, so the hull is
// discovered, dispatched, and then silently never works.
func TestClaimIdentityFor_ARoleTaggedHullClaimsUnderItsOwnTag(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{}

	for _, tag := range []string{gate.DeliveryFleetTag, gate.FactoryFleetTag, gate.LegacyFleetTag} {
		got := h.claimIdentityFor(cmd, gateTestHull(t, "GATE-7", tag))
		if got != tag {
			t.Fatalf("claimIdentityFor(tag %q) = %q; ClaimShip authorizes only when tag == operation, so a mismatch means the hull is rejected at the DB and never works", tag, got)
		}
	}
}

// An UNDEDICATED hull is opportunistic capacity: it claims under the drain's own identity,
// exactly as before.
func TestClaimIdentityFor_AnUndedicatedHullClaimsUnderTheDrainIdentity(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{}

	if got := h.claimIdentityFor(cmd, gateTestHull(t, "FREE-7", "")); got != operationManufacturing {
		t.Fatalf("claimIdentityFor(undedicated) = %q, want the drain identity %q", got, operationManufacturing)
	}
}

// THE ALLOWLIST IS THE POINT. Claiming under "whatever tag the hull carries" would let the
// drain claim a CONTRACT or TRADE hull under that fleet's identity and pass the no-poach
// guard -- defeating it entirely. Only GATE tags are honoured.
func TestClaimIdentityFor_AForeignPinnedHullIsNeverClaimedUnderItsOwnTag(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{}

	for _, tag := range []string{"contract", "trade", "purchasing", "warehouse"} {
		got := h.claimIdentityFor(cmd, gateTestHull(t, "FOREIGN-7", tag))
		if got == tag {
			t.Fatalf("claimIdentityFor(foreign tag %q) = %q — claiming under a foreign fleet's identity defeats the no-poach guard", tag, got)
		}
		if got != operationManufacturing {
			t.Fatalf("claimIdentityFor(foreign tag %q) = %q, want the drain identity so ClaimShip REJECTS it", tag, got)
		}
	}
}

// A per-launch DedicatedFleet override still wins for undedicated hulls, so the existing
// per-launch pinning is unaffected.
func TestClaimIdentityFor_HonoursThePerLaunchDrainIdentity(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{DedicatedFleet: "gate-alt"}

	if got := h.claimIdentityFor(cmd, gateTestHull(t, "FREE-7", "")); got != "gate-alt" {
		t.Fatalf("claimIdentityFor = %q, want the per-launch identity %q", got, "gate-alt")
	}
}

// THE PER-LEG FLOOR READ. The floors are pattern-C live knobs: they must be re-read off the
// pipeline row on EVERY leg. Caching them at handler construction, or hoisting the read out
// of the leg, silently turns a live knob into a restart-only one -- which looks identical
// from the outside until an operator tunes it and nothing happens.
func TestDeliverGateLeg_ReadsTheFloorsOffThePipelineRowOnEveryLeg(t *testing.T) {
	h, repo, topo, buyer := newGateDeliveryHandler(t)
	pipeline := repo.pipelines["PIPE-1"]

	// Leg 1 under the ARMED defaults (unset row): MODERATE supply is at the buy floor, so it buys.
	topo.supply = "MODERATE"
	if !h.deliverGateLeg(context.Background(), gateTestCmd(), "X1-GT", gateTestLot(t), shared.MustNewPlayerID(1)) {
		t.Fatal("leg 1: MODERATE is at the default MODERATE buy floor and must buy")
	}
	if buyer.calls() == 0 {
		t.Fatal("leg 1 bought nothing")
	}

	// An operator raises the buy floor between legs -- the live tune.
	pipeline.SetDeliveryFloors("HIGH", "ABUNDANT")
	buyer.reset()

	// Leg 2 must honour the NEW floor: MODERATE is now below it, so the leg pauses.
	h.deliverGateLeg(context.Background(), gateTestCmd(), "X1-GT", gateTestLot(t), shared.MustNewPlayerID(1))
	if buyer.calls() != 0 {
		t.Fatalf("leg 2 bought %d time(s) after the buy floor was raised to HIGH — the floors were not re-read off the pipeline row, so the knob is restart-only", buyer.calls())
	}
}

// STANDING DOWN. When EVERY material is paused the leg spends nothing -- and says so. A
// paused fleet and an idle one must not look identical.
func TestDeliverGateLeg_StandsDownAndRecordsWhenEveryMaterialIsPaused(t *testing.T) {
	h, _, topo, buyer := newGateDeliveryHandler(t)
	topo.supply = "SCARCE"
	logs := captureGateLegLogs(t, h)

	h.deliverGateLeg(context.Background(), gateTestCmd(), "X1-GT", gateTestLot(t), shared.MustNewPlayerID(1))

	if buyer.calls() != 0 {
		t.Fatalf("a fully paused fleet bought %d time(s); it must spend nothing", buyer.calls())
	}
	joined := strings.Join(logs(), "\n")
	for _, want := range []string{"PAUSED", "FAB_MATS", "SCARCE", "MODERATE", "HIGH"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the pause was not recorded (%q missing) — a paused fleet and an idle one would look identical:\n%s", want, joined)
		}
	}
}

// ONE PAUSED MATERIAL IS NOT A PAUSED FLEET: the hull loads the other one and departs.
func TestDeliverGateLeg_OnePausedMaterialStillDepartsWithTheOther(t *testing.T) {
	h, _, topo, buyer := newGateDeliveryHandler(t)
	topo.supplyByGood = map[string]string{"FAB_MATS": "SCARCE", "ADVANCED_CIRCUITRY": "HIGH"}

	if !h.deliverGateLeg(context.Background(), gateTestCmd(), "X1-GT", gateTestLot(t), shared.MustNewPlayerID(1)) {
		t.Fatal("the leg stood down with one material still eligible — the fleet must never idle on capacity it can use")
	}
	if buyer.goods()["ADVANCED_CIRCUITRY"] == 0 {
		t.Fatalf("the eligible material was not bought: %v", buyer.goods())
	}
	if buyer.goods()["FAB_MATS"] != 0 {
		t.Fatalf("the PAUSED material was bought: %v", buyer.goods())
	}
}

// A topology refusal (nothing exports the good this era) is recorded and skipped, never
// substituted with another waypoint and never fatal to the whole leg.
func TestDeliverGateLeg_RecordsATopologyRefusalAndKeepsGoing(t *testing.T) {
	h, _, topo, buyer := newGateDeliveryHandler(t)
	topo.errByGood = map[string]error{"FAB_MATS": errors.New("no market in X1-GT exports FAB_MATS")}
	logs := captureGateLegLogs(t, h)

	h.deliverGateLeg(context.Background(), gateTestCmd(), "X1-GT", gateTestLot(t), shared.MustNewPlayerID(1))

	if buyer.goods()["FAB_MATS"] != 0 {
		t.Fatal("a good with no resolvable exporter was bought anyway")
	}
	if buyer.goods()["ADVANCED_CIRCUITRY"] == 0 {
		t.Fatal("one unresolvable material aborted the whole leg; the other was still buyable")
	}
	if !strings.Contains(strings.Join(logs(), "\n"), "FAB_MATS") {
		t.Fatal("the topology refusal was not recorded")
	}
}

// The trip outcome is recorded: capacity, what was loaded, what was skipped and why.
func TestDeliverGateLeg_RecordsTheTripOutcome(t *testing.T) {
	h, _, topo, _ := newGateDeliveryHandler(t)
	topo.supplyByGood = map[string]string{"FAB_MATS": "SCARCE", "ADVANCED_CIRCUITRY": "HIGH"}
	logs := captureGateLegLogs(t, h)

	h.deliverGateLeg(context.Background(), gateTestCmd(), "X1-GT", gateTestLot(t), shared.MustNewPlayerID(1))

	joined := strings.Join(logs(), "\n")
	if !strings.Contains(joined, gate.SkipPaused) || !strings.Contains(joined, "FAB_MATS") {
		t.Fatalf("the trip did not record what it skipped and why:\n%s", joined)
	}
}

// Discovery must query EVERY gate tag. A pool queried under one tag only leaves the other
// role's hulls invisible -- they are excluded from FindIdleLightHaulers (which drops every
// dedicated hull) and never surface anywhere else.
func TestSelectHaulers_DiscoversEveryGateFleetTag(t *testing.T) {
	queried := &recordingFleetQueryRepo{
		byFleet: map[string][]*navigation.Ship{
			gate.DeliveryFleetTag: {gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)},
			gate.FactoryFleetTag:  {gateTestHull(t, "GATE-8", gate.FactoryFleetTag)},
			operationManufacturing: {gateTestHull(t, "MFG-9", gate.LegacyFleetTag)},
		},
	}
	h := &RunConstructionCoordinatorHandler{shipRepo: queried}

	ships, err := h.selectHaulers(context.Background(), gateTestCmd(), shared.MustNewPlayerID(1), "X1-GT")
	if err != nil {
		t.Fatalf("selectHaulers returned error: %v", err)
	}

	for _, tag := range append(gate.RoleFleetTags(), operationManufacturing) {
		if !queried.wasQueried(tag) {
			t.Fatalf("selectHaulers never queried the %q pool; those hulls are invisible to the drain", tag)
		}
	}
	seen := map[string]bool{}
	for _, s := range ships {
		if seen[s.ShipSymbol()] {
			t.Fatalf("selectHaulers returned %s twice; a duplicated hull would be dispatched to two lots", s.ShipSymbol())
		}
		seen[s.ShipSymbol()] = true
	}
	if len(ships) != 3 {
		t.Fatalf("selectHaulers returned %d hulls, want all 3 gate-tagged hulls", len(ships))
	}
}
```

Write the fixtures the tests above name in the same file: `gateTestHull` (a `navigation.NewShip` with the given tag and a 80-unit hold, mirroring `reclaimHull` in the grpc package), `gateTestCmd` (`&RunConstructionCoordinatorCommand{PlayerID: 1, ContainerID: "C-1"}`), `gateTestLot` (a `constructionLot` whose task is a `DELIVER_TO_CONSTRUCTION` task on `PIPE-1`), `newGateDeliveryHandler` (a handler with `drainStubPipelineRepo` holding a construction pipeline `PIPE-1` with FAB_MATS and ADVANCED_CIRCUITRY material targets, plus a `stubGateTopology` and a `countingGateBuyer` wired through `SetGateDelivery`), `captureGateLegLogs` (drains the `capturingLogger` the package already uses — see `capturing_logger_test.go`), and `recordingFleetQueryRepo` (a `navigation.ShipRepository` recording which fleet tags `FindIdleShipsByFleet` looked up). Model them on the existing `drainStubPipelineRepo` (`run_construction_coordinator_test.go:181`) and `capturing_logger_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/application/manufacturing/commands/ -run 'TestClaimIdentityFor|TestDeliverGateLeg|TestSelectHaulers_DiscoversEveryGateFleetTag' 2>&1 | tail -20`
Expected: FAIL — `h.claimIdentityFor undefined`, `h.deliverGateLeg undefined`.

- [ ] **Step 3: Claim under the hull's own tag**

In `run_construction_coordinator_dispatch.go`, add after `dedicatedFleet` (:39):

```go
// claimIdentityFor is the ClaimShip operation string for ONE hull.
//
// This is load-bearing and easy to get silently wrong. ClaimShip authorizes a NEW claim only
// when the hull's dedicated_fleet EQUALS the operation (ship_repository_claims.go: a
// mismatch returns ShipDedicatedToOtherFleetError). So a hull tagged gate-delivery, claimed
// under the drain's DEFAULT "manufacturing" identity, is rejected at the DB — the hull is
// discovered, paired with a lot, dispatched, and then silently never works. That failure
// ships green.
//
// The GATE-tag allowlist is deliberate and must not be relaxed to "whatever tag the hull
// carries". Claiming under any tag would let the drain claim a CONTRACT or TRADE hull under
// that fleet's own identity and sail straight past the dedication guard, defeating the
// no-poach rule entirely. An undedicated hull ("") claims under the drain's identity as
// before; a foreign-pinned hull claims under the drain's identity too, precisely so
// ClaimShip REJECTS it.
func (h *RunConstructionCoordinatorHandler) claimIdentityFor(cmd *RunConstructionCoordinatorCommand, ship *navigation.Ship) string {
	if tag := ship.DedicatedFleet(); gate.IsGateFleetTag(tag) {
		return tag
	}
	return h.dedicatedFleet(cmd)
}
```

Add `claimIdentity string` to `constructionLot` (:111) with:

```go
	// claimIdentity is the ClaimShip operation this lot's hull must be claimed under: its OWN
	// gate tag, or the drain's identity for an undedicated/foreign hull. Resolved at plan time
	// from the paired hull, because the worker that claims runs long after the tick that
	// planned it and must not re-derive from a tag that may have changed underneath.
	claimIdentity string
```

Set it where the lots are built in `planDispatchLots`, on both append sites:

```go
			lots = append(lots, constructionLot{task: task, ship: hull, claimIdentity: h.claimIdentityFor(cmd, hull)})
```
```go
			lots = append(lots, constructionLot{task: clone, ship: hull, ephemeral: true, claimIdentity: h.claimIdentityFor(cmd, hull)})
```

`planDispatchLots` therefore needs `cmd` — add it as a parameter (`func (h *RunConstructionCoordinatorHandler) planDispatchLots(ctx context.Context, cmd *RunConstructionCoordinatorCommand, tasks []*manufacturing.ManufacturingTask, idleShips []*navigation.Ship, maxLots int) []constructionLot`) and update the single call site in `drainOnce` (:382).

In `run_construction_coordinator.go`, in `runSupplyWorker` (:512), replace the claim:

```go
	// Atomic claim under THE HULL'S OWN identity: a role-tagged gate hull is claimed under its
	// own tag (ClaimShip authorizes only when tag == operation, so the drain's default identity
	// would be REJECTED and the hull would silently never work), an undedicated hull under the
	// drain's identity, and a hull pinned to ANOTHER fleet is rejected at the DB, not clobbered.
	identity := lot.claimIdentity
	if identity == "" {
		identity = h.dedicatedFleet(cmd) // defensive: a lot built without one keeps today's behaviour
	}
	if err := h.shipRepo.ClaimShip(ctx, hull, cmd.ContainerID, playerID, identity); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Skipping hauler %s for construction: claim rejected under identity %q: %v", hull, identity, err), nil)
		return // lot stays undispatched; the material's task is retried next tick
	}
```

- [ ] **Step 4: Discover every gate fleet tag**

In `selectHaulers` (:54), replace the single `FindIdleShipsByFleet` call with a loop over the drain's identity plus every role tag, de-duplicated by symbol:

```go
	// Discover the drain's own identity AND every gate ROLE pool. FindIdleLightHaulers excludes
	// every dedicated hull by design (ship_pool_manager.go: `if ship.DedicatedFleet() != "" {
	// continue }`), so a role-tagged hull appears in NO pool unless its own tag is queried here —
	// it would simply be invisible to the drain forever.
	fleets := append([]string{fleet}, gate.RoleFleetTags()...)
	seen := make(map[string]bool)
	var dedicatedIdle []*navigation.Ship
	for _, f := range fleets {
		if f == "" || seen["\x00fleet:"+f] {
			continue // a per-launch identity equal to a role tag must not be scanned twice
		}
		seen["\x00fleet:"+f] = true
		found, _, err := contract.FindIdleShipsByFleet(ctx, playerID, h.shipRepo, f, contract.RequireCargoCapacity)
		if err != nil {
			return nil, fmt.Errorf("failed to discover %s construction haulers: %w", f, err)
		}
		for _, ship := range found {
			if ship == nil || seen[ship.ShipSymbol()] {
				continue // one hull, one lot: a duplicate would be dispatched twice
			}
			seen[ship.ShipSymbol()] = true
			dedicatedIdle = append(dedicatedIdle, ship)
		}
	}
	dedicatedIdle = haulersInSystem(dedicatedIdle, systemSymbol)
```

The `ExclusiveDedicatedFleet` block below must check membership across the same set — change its `FleetHasMembers(ctx, playerID, h.shipRepo, fleet)` to loop `fleets` and report true if **any** has members.

- [ ] **Step 5: Write the delivery leg**

Create `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go`:

```go
package commands

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// GateTopologyResolver resolves this era's terminal factory for a gate material — the
// waypoint that EXPORTS it. *services.GateTopology satisfies it. Only TerminalFactory is
// declared: the delivery fleet buys terminal OUTPUT and never walks the recipe graph, so
// IsRaw/Inputs are deliberately out of this interface (they are also unsound — the recipe
// map is cyclic and !hasRecipe is false for every real raw material, bead sp-4irrr).
type GateTopologyResolver interface {
	TerminalFactory(ctx context.Context, good, systemSymbol string, playerID int) (*mfgServices.MarketLocatorResult, error)
}

// GateBuyer buys a gate material at a PINNED terminal factory.
// *services.ProductionExecutor satisfies it via BuyAtTerminalFactory.
type GateBuyer interface {
	BuyAtTerminalFactory(ctx context.Context, ship *navigation.Ship, good string, source *mfgServices.MarketLocatorResult, units int, systemSymbol string, playerID int, opContext *shared.OperationContext) (*mfgServices.ProductionResult, error)
}

// gateDelivery is the drain's delivery-fleet collaborator set plus the live buy policy.
//
// The policy is held here rather than rebuilt per leg because its PAUSE STATE must persist
// across legs — that state is the hysteresis. It is rebuilt only when the operator changes
// the floors, which resets the pause state deliberately: the rule changed, so re-deriving
// under the new rule costs one tick and never a spend.
type gateDelivery struct {
	topology GateTopologyResolver
	buyer    GateBuyer

	mu     sync.Mutex
	policy *gate.BuyPolicy
	buy    string // the floors the cached policy was built from
	resume string
}

func (g *gateDelivery) enabled() bool { return g != nil && g.topology != nil && g.buyer != nil }

// policyFor returns the buy policy for the floors CURRENTLY on the pipeline row, rebuilding
// it only when they changed.
func (g *gateDelivery) policyFor(buyFloor, resumeFloor string) *gate.BuyPolicy {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policy == nil || g.buy != buyFloor || g.resume != resumeFloor {
		g.policy = gate.NewBuyPolicy(shared.SupplyLevel(buyFloor), shared.SupplyLevel(resumeFloor))
		g.buy, g.resume = buyFloor, resumeFloor
	}
	return g.policy
}

// SetGateDelivery wires the delivery fleet: phase 1's role-based topology and the pinned
// terminal-factory buy. OPTIONAL, following SetTreeResolver/SetInventorySource — a nil in
// either argument leaves the delivery leg unwired and the drain behaves exactly as before,
// which keeps every existing coordinator test unchanged.
func (h *RunConstructionCoordinatorHandler) SetGateDelivery(topology GateTopologyResolver, buyer GateBuyer) {
	if topology == nil || buyer == nil {
		return
	}
	h.gate = &gateDelivery{topology: topology, buyer: buyer}
}

// gateMaterial is one gate material WITH its live market quote. The quote is carried, not
// re-fetched: the leg needs the waypoint and trade volume to buy, the supply level to
// decide, and re-reading between the decision and the buy would let the two disagree.
//
// It is projected down to gate.Material before planning the fill, so the fill arithmetic
// stays pure and cannot accidentally depend on a price or a waypoint symbol.
type gateMaterial struct {
	good      string
	remaining int
	source    *mfgServices.MarketLocatorResult // nil when this era exports the good nowhere
}

// deliverGateLeg runs one delivery hull's leg: decide, record, fill, buy, deliver.
//
// THE FLOORS ARE READ OFF THE PIPELINE ROW HERE, ON EVERY LEG. That per-leg read is what
// makes the knob live; hoisting it to handler construction would silently turn a pattern-C
// tunable into a restart-only one, which looks identical from the outside until an operator
// tunes it and nothing happens.
//
// Reports whether the leg delivered anything.
func (h *RunConstructionCoordinatorHandler) deliverGateLeg(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	lot constructionLot,
	playerID shared.PlayerID,
) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task

	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("WARNING", fmt.Sprintf("Gate delivery: cannot read pipeline %s for %s — standing down this leg rather than buying against an unknown bill: %v", task.PipelineID(), task.Good(), err), nil)
		return false
	}

	// LIVE floors, re-read this leg.
	policy := h.gate.policyFor(pipeline.DeliveryBuyFloor(), pipeline.DeliveryResumeFloor())

	// Resolve each material's terminal factory and quote.
	materials := make([]gateMaterial, 0, len(pipeline.Materials()))
	for _, target := range pipeline.Materials() {
		m := gateMaterial{good: target.TradeSymbol(), remaining: target.RemainingQuantity()}
		source, terr := h.gate.topology.TerminalFactory(ctx, m.good, systemSymbol, cmd.PlayerID)
		if terr != nil || source == nil {
			// A refusal, never a substitution: sending a hull to some other waypoint is how cargo
			// ends up somewhere that cannot accept it. Recorded so the miss is diagnosable.
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: no terminal factory for %s in %s this era — skipping it on this trip: %v", m.good, systemSymbol, terr), map[string]interface{}{
				"good": m.good, "system": systemSymbol,
			})
			materials = append(materials, m)
			continue
		}
		m.source = source
		materials = append(materials, m)
	}

	// DECIDE and RECORD, per material. Every ruling is logged: a paused fleet and an idle one
	// must not look identical, which is the whole reason this design exists.
	goodsSeen := make([]string, 0, len(materials))
	paused := make(map[string]bool, len(materials))
	for _, m := range materials {
		if m.source == nil {
			continue // unresolvable: not a policy decision, and must not count toward a fleet pause
		}
		goodsSeen = append(goodsSeen, m.good)
		decision := policy.Decide(m.good, m.source.WaypointSymbol, shared.ParseSupplyLevel(m.source.Supply))
		paused[m.good] = decision.Paused
		level := "INFO"
		if decision.Paused {
			level = "WARNING"
		}
		logger.Log(level, decision.LogLine(), map[string]interface{}{
			"good": decision.Good, "factory": decision.Factory, "supply": string(decision.Supply),
			"buy_floor": string(decision.BuyFloor), "resume_floor": string(decision.ResumeFloor),
			"paused": decision.Paused,
		})
	}

	// The FLEET is paused only when EVERY material is: a hull fills greedily from any eligible
	// material, so one pause still leaves useful work.
	if policy.FleetPaused(goodsSeen) {
		logger.Log("WARNING", fmt.Sprintf("Gate delivery fleet PAUSED: every gate material is below its buy floor — %s stands down this leg and spends nothing", lot.ship.ShipSymbol()), map[string]interface{}{
			"ship": lot.ship.ShipSymbol(), "materials": len(goodsSeen),
		})
		return false
	}

	// Project to the pure fill planner and PLAN the mixed load.
	capacity := 0
	if cargo := lot.ship.Cargo(); cargo != nil {
		capacity = cargo.Capacity - cargo.Units
	}
	planInput := make([]gate.Material, 0, len(materials))
	for _, m := range materials {
		fm := gate.Material{Good: m.good, Remaining: m.remaining, Paused: paused[m.good]}
		if m.source != nil {
			fm.TradeVolume = m.source.TradeVolume
		}
		planInput = append(planInput, fm)
	}
	trip := gate.PlanFill(capacity, planInput)
	logger.Log("INFO", trip.LogLine(), map[string]interface{}{
		"ship": lot.ship.ShipSymbol(), "capacity": trip.Capacity, "loaded": trip.Loaded(),
		"stops": len(trip.Stops), "skips": len(trip.Skips),
	})
	if len(trip.Stops) == 0 {
		return false
	}

	// BUY each stop at its PINNED terminal factory, then deliver the whole hold to the gate.
	sources := make(map[string]*mfgServices.MarketLocatorResult, len(materials))
	for _, m := range materials {
		sources[m.good] = m.source
	}
	delivered := 0
	for _, stop := range trip.Stops {
		result, berr := h.gate.buyer.BuyAtTerminalFactory(ctx, lot.ship, stop.Good, sources[stop.Good], stop.Units, systemSymbol, cmd.PlayerID, h.operationContext(cmd))
		if berr != nil {
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: buying %d %s at %s failed; delivering what is aboard: %v", stop.Units, stop.Good, sources[stop.Good].WaypointSymbol, berr), nil)
			continue
		}
		if result == nil || result.QuantityAcquired == 0 {
			continue // the money/price guards stopped the fill; nothing to deliver for this good
		}
		units, derr := h.producer.DeliverToConstructionSite(ctx, lot.ship.ShipSymbol(), stop.Good, task.ConstructionSite(), playerID)
		if derr != nil {
			logger.Log("WARNING", fmt.Sprintf("Gate delivery: delivering %s to %s failed: %v", stop.Good, task.ConstructionSite(), derr), nil)
			continue
		}
		delivered += units
		logger.Log("INFO", fmt.Sprintf("Gate delivery: supplied %d %s to %s via %s", units, stop.Good, task.ConstructionSite(), lot.ship.ShipSymbol()), map[string]interface{}{
			"good": stop.Good, "units": units, "construction_site": task.ConstructionSite(), "ship": lot.ship.ShipSymbol(),
		})
		// Only the LOT'S OWN material is recorded here. A mixed trip's other material is picked up
		// by reconcilePipelinesFromSite on the next tick, which re-reads the LIVE site and raises
		// the delivered counters from the server — the authoritative source. Recording it here
		// would need a second material's task and pipeline lock, and would double-count against
		// that reconcile.
		if stop.Good == task.Good() {
			h.recordDelivery(ctx, task, units)
		}
	}
	return delivered > 0
}
```

Add the field to `RunConstructionCoordinatorHandler` (beside `warehouse` at :174):

```go
	// gate is the delivery-fleet collaborator set + live buy policy. Wired by SetGateDelivery;
	// nil leaves the delivery leg off and the drain byte-identical to before.
	gate *gateDelivery
```

Route to it in `supplyTask` (`run_construction_coordinator_supply.go:35`), immediately after `claimTaskForSupply` and **before** `deliverOnHandCargo`:

```go
	// A DELIVERY-role hull buys terminal-factory output and hauls it to the gate. It never
	// fabricates and never walks the recipe graph, so it takes its own leg rather than the
	// shared source-then-deliver path.
	if h.gate.enabled() && lot.claimIdentity == gate.DeliveryFleetTag {
		return h.deliverGateLeg(ctx, cmd, systemSymbol, lot, playerID)
	}
```

- [ ] **Step 6: Wire it in the daemon**

**READ `cmd/spacetraders-daemon/main.go` around the construction-coordinator builder (:984-997) before editing** — confirm for yourself that `goodsMarketLocator`, `constructionExecutor` and `goods.ExportToImportMap` are in scope there, rather than assuming it from this plan. Then add after `SetConstructionSiteSource(constructionSiteRepo)` (:996):

```go
	// The gate DELIVERY fleet: phase 1's role-based topology resolves this era's terminal
	// factory (the waypoint that EXPORTS each gate material — never a hardcoded symbol), and the
	// executor buys there with every money guard unchanged. Optional collaborator: unwired, the
	// drain behaves exactly as before.
	constructionCoordinatorHandler.SetGateDelivery(
		goodsServices.NewGateTopology(goodsMarketLocator, goods.ExportToImportMap),
		constructionExecutor,
	)
```

- [ ] **Step 7: Run tests to verify they pass**

```bash
cd gobot && go test -race ./internal/application/manufacturing/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok`, no `FAIL`. Every pre-existing drain test must still pass: the delivery leg is gated on `h.gate.enabled()` and on the hull carrying the delivery tag, and no existing test wires either.

```bash
cd gobot && go build ./... ; echo "build=$?"; go vet ./... ; echo "vet=$?"
```
Expected: `build=0`, `vet=0`.

- [ ] **Step 8: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "feat(construction): role-aware drain — claim under the hull's own tag, run the delivery leg

CLAIM IDENTITY IS LOAD-BEARING. ClaimShip authorizes a NEW claim only when the
hull's dedicated_fleet EQUALS the operation string, so a gate-delivery hull
claimed under the drain's default 'manufacturing' identity is rejected at the DB:
discovered, paired, dispatched, and then silently never works. Each hull is now
claimed under its OWN gate tag, and discovery queries every gate pool --
FindIdleLightHaulers drops every dedicated hull, so a role tag that is never
queried is a hull that is invisible forever.

The allowlist is deliberate. Claiming under 'whatever tag the hull carries' would
let the drain claim a contract or trade hull under that fleet's identity and sail
past the dedication guard, defeating no-poach entirely. Only GATE tags are
honoured; an undedicated hull uses the drain identity, and a foreign-pinned hull
uses it too, precisely so ClaimShip rejects it.

The delivery leg reads the buy/resume floors off the pipeline row ON EVERY LEG.
That read is what makes the knob live: hoisting it to construction would turn a
pattern-C tunable into a restart-only one, indistinguishable from working until
an operator tunes it and nothing happens.

Every decision is recorded -- per-material buy/pause with factory, supply and
resume condition, and the trip outcome with what was loaded and what was skipped
and why. A paused fleet and an idle one are no longer the same log." -- gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go gobot/internal/application/manufacturing/commands/run_construction_coordinator.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_supply.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go gobot/cmd/spacetraders-daemon/main.go
```

- [ ] **Step 9: Mutation-probe the claim identity (KEEP THIS PROBE)**

This is the defect that ships green and does nothing. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && perl -0pi -e 's/\tif tag := ship\.DedicatedFleet\(\); gate\.IsGateFleetTag\(tag\) \{\n\t\treturn tag\n\t\}\n//' internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && ! grep -q 'gate.IsGateFleetTag(tag)' internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestClaimIdentityFor' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestClaimIdentityFor_ARoleTaggedHullClaimsUnderItsOwnTag`, then `RESTORED`. Zero named tests means a role-tagged hull could be claimed under the wrong identity with nothing noticing — stop.

- [ ] **Step 10: Mutation-probe the per-leg floor read**

Hoist the read out of the leg and confirm the liveness test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|	policy := h.gate.policyFor(pipeline.DeliveryBuyFloor(), pipeline.DeliveryResumeFloor())|	policy := h.gate.policyFor("", "")|' internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && grep -q 'policyFor("", "")' internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestDeliverGateLeg' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestDeliverGateLeg_ReadsTheFloorsOffThePipelineRowOnEveryLeg`, then `RESTORED`.

- [ ] **Step 11: Mutation-probe the EVERY-vs-ANY fleet pause**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && sed -i '' 's|	if policy.FleetPaused(goodsSeen) {|	if len(paused) > 0 \&\& paused[goodsSeen[0]] {|' internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestDeliverGateLeg' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -5; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestDeliverGateLeg_OnePausedMaterialStillDepartsWithTheOther`, then `RESTORED`.

---

### Task 9: Era-invariance source guards over both new surfaces, and the full verification sweep

A test that fails the build if a waypoint symbol is ever hardcoded into the new layers. Without it the constraint is a comment that erodes — and it erodes silently, because a hardcoded symbol works perfectly right up until the era rolls.

Scope is the files phase 2 adds. Goods names are invariant and explicitly fine; the guard must not flag `FAB_MATS` or `ADVANCED_CIRCUITRY`. Each guard is calibrated against a known-bad string so a green result cannot be vacuous.

**Files:**
- Modify: `gobot/internal/domain/manufacturing/gate/fill_test.go` (append the package guard)
- Test: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go` (append)
- Test: `gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go` (append)

**Interfaces:**
- Consumes: nothing. Reads source files from disk.
- Produces: no exported symbols.

- [ ] **Step 1: Write the guard for the pure policy package**

Append to `gobot/internal/domain/manufacturing/gate/fill_test.go`:

```go
// Waypoint symbols look like X1-AB12-C3. They are regenerated every era, so any literal in
// this layer is a bug that survives exactly until the next era rolls — and works perfectly
// until then, which is why it needs a build-failing guard rather than a comment.
//
// Goods names are the invariant and are deliberately NOT flagged: every era's gate requires
// FAB_MATS and ADVANCED_CIRCUITRY. Locations are not invariant and are resolved at runtime
// by market role.
func TestGatePolicyPackage_ContainsNoWaypointLiterals(t *testing.T) {
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	// The guard must be able to fail. If the pattern cannot match a known-bad string, a green
	// result would mean nothing.
	if !waypointLiteral.MatchString(`x := "X1-KP23-F46"`) {
		t.Fatal("waypoint-literal pattern failed its own calibration — it cannot detect a real symbol")
	}
	// ...and it must not fire on the invariants, or the guard would be unusable and get deleted.
	if waypointLiteral.MatchString(`good := "FAB_MATS"`) || waypointLiteral.MatchString(`good := "ADVANCED_CIRCUITRY"`) {
		t.Fatal("waypoint-literal pattern flags a GOODS name; goods are era-invariant and must be nameable directly")
	}

	for _, file := range []string{"role.go", "buy_policy.go", "fill.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
			t.Fatalf("%s contains hardcoded waypoint symbols %v — resolve locations by market role instead", file, found)
		}
		if strings.Contains(string(src), "X1-") {
			t.Fatalf("%s references an X1- prefixed symbol", file)
		}
	}
}
```

Add `"os"` and `"regexp"` to that file's imports (`strings` and `testing` are already there).

- [ ] **Step 2: Write the guard for the delivery leg**

Append to `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go`:

```go
// The delivery leg resolves every location through GateTopology at runtime. A waypoint
// literal here would pin the fleet to one era's map and then quietly send hulls nowhere.
func TestGateDeliveryLeg_ContainsNoWaypointLiterals(t *testing.T) {
	const file = "run_construction_coordinator_gate_delivery.go"
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	if !waypointLiteral.MatchString(`x := "X1-KP23-F46"`) {
		t.Fatal("waypoint-literal pattern failed its own calibration")
	}
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
		t.Fatalf("%s contains hardcoded waypoint symbols %v — the terminal factory is resolved by EXPORT role, per era", file, found)
	}
	if strings.Contains(string(src), "X1-") {
		t.Fatalf("%s references an X1- prefixed symbol", file)
	}
}
```

Add `"os"` and `"regexp"` to that file's imports.

- [ ] **Step 3: Write the guard for the pinned buy**

Append to `gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go`:

```go
// BuyAtTerminalFactory takes its source as an argument precisely so no waypoint is ever
// decided here. A literal in this file would be a source the caller did not choose.
func TestBuyAtTerminalFactorySource_ContainsNoWaypointLiterals(t *testing.T) {
	const file = "production_executor_gate_buy.go"
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	if !waypointLiteral.MatchString(`x := "X1-KP23-F46"`) {
		t.Fatal("waypoint-literal pattern failed its own calibration")
	}
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
		t.Fatalf("%s contains hardcoded waypoint symbols %v — the source is the caller's, never this file's", file, found)
	}
	if strings.Contains(string(src), "X1-") {
		t.Fatalf("%s references an X1- prefixed symbol", file)
	}
}
```

Add `"os"`, `"regexp"` and `"strings"` to that file's imports.

- [ ] **Step 4: Run the guards to verify they pass**

```bash
cd gobot && go test ./internal/domain/manufacturing/gate/ ./internal/application/manufacturing/commands/ ./internal/application/manufacturing/services/ -run 'ContainsNoWaypointLiterals' -v 2>&1 | grep -E "^(--- (PASS|FAIL)|ok|FAIL)" | head -20
```
Expected: PASS for all three.

- [ ] **Step 5: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "test(gate): guard both new phase-2 surfaces against waypoint-symbol literals

Waypoint numbering is regenerated every era, so a hardcoded symbol is a latent
era-rollover bug that works perfectly until it does not. Each guard is calibrated
against a known-bad string so a green result cannot be vacuous, and against the
GOODS names so it cannot fire on the invariants it must permit -- a guard that
flagged FAB_MATS would be unusable and would get deleted." -- gobot/internal/domain/manufacturing/gate/fill_test.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go gobot/internal/application/manufacturing/services/production_executor_gate_buy_test.go
```

- [ ] **Step 6: Prove each guard can actually fail**

Three separate ONE-invocation probes. A PASS in any of these means that guard is inert and must be fixed.

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && printf '\nvar eraSpecificSmokeTest = "X1-KP23-F46"\n' >> internal/domain/manufacturing/gate/fill.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'ContainsNoWaypointLiterals' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -3; git checkout -- internal/domain/manufacturing/gate/fill.go && echo "RESTORED"
```

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && printf '\nvar eraSpecificSmokeTest = "X1-KP23-F46"\n' >> internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'ContainsNoWaypointLiterals' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -3; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go && echo "RESTORED"
```

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && printf '\nvar eraSpecificSmokeTest = "X1-KP23-F46"\n' >> internal/application/manufacturing/services/production_executor_gate_buy.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/services/ -run 'ContainsNoWaypointLiterals' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -3; git checkout -- internal/application/manufacturing/services/production_executor_gate_buy.go && echo "RESTORED"
```

Expected for each: `MUTATION APPLIED`, then `--- FAIL`, then `RESTORED`.

- [ ] **Step 7: Full verification sweep**

Formatting, build and vet first (`go vet` compiles test files, so it catches stale call sites `go build` misses — `planDispatchLots` gained a parameter in Task 8, so this is not a formality):

```bash
cd gobot && gofmt -l ./internal/ ./cmd/ ; echo "gofmt-done"; go build ./... ; echo "build=$?"; go vet ./... ; echo "vet=$?"
```
Expected: `gofmt -l` prints **nothing** before `gofmt-done`, then `build=0`, `vet=0`.

Now the whole suite, filtered so it cannot exhaust context. `grep -v` on `^ok ` keeps only the lines that matter:

```bash
cd gobot && go test ./... 2>&1 | grep -vE "^ok " | head -60
```
Expected: only `[no test files]` lines — **no** `FAIL`.

Then the race detector over every package phase 2 touched:

```bash
cd gobot && go test -race ./internal/domain/manufacturing/... ./internal/application/manufacturing/... ./internal/adapters/grpc/... ./internal/adapters/cli/... ./internal/adapters/persistence/... ./internal/application/bootstrap/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```
Expected: `ok` for every package, no `FAIL`. The `BuyPolicy` concurrency test and the drain's worker goroutines are only meaningful under `-race`.

Finally, confirm the protected paths were never touched:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git diff --name-only main...HEAD | grep -E "gobot/internal/captain/|cmd/captain-gate/|city/agents/" ; echo "protected-paths-hits=$?"
```
Expected: no filenames printed and `protected-paths-hits=1` (grep's "no match"). Any filename here is a plan violation — stop and report it.

- [ ] **Step 8: Commit any formatting fixes**

If `gofmt -l` named files, format and commit them:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot && gofmt -w $(gofmt -l ./internal/ ./cmd/) && cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && git commit --no-verify -m "style(gate): gofmt the phase-2 surfaces" -- gobot/
```

If it named nothing, skip this step — do not create an empty commit.

---

## Handback

Report: worktree path, branch, commit SHAs (one per task), filtered test counts, and the mutation results from **every** probe step, naming the test each probe killed. A probe that named zero tests is a finding, not a formality — report it explicitly rather than moving on.

Also report anything you disagreed with or could not do, and any pre-existing test you had to touch (with which one and why — a pre-existing test that now fails was encoding old behaviour, and that is a finding worth surfacing, not a nuisance to fix quietly).

**Do NOT merge to main and do NOT run captain-gate — the orchestrator gates.**

### DEPLOY ORDERING — migration 051 goes first

**Migration `051` must be applied to the production database BEFORE the daemon that writes those columns is deployed.**

Boot `AutoMigrate` would add the columns, but **AutoMigrate failure is non-fatal**. A boot where it could not run leaves the daemon up and apparently healthy while every `delivery_buy_floor` / `delivery_resume_floor` write hits **SQLSTATE 42703 (undefined_column)**. The operator-visible symptom would be a `construction override --buy-floor` that reports success at the CLI and changes nothing — the exact silent-no-op failure class this design exists to remove, reintroduced by a deploy-ordering mistake rather than by code.

Order:
1. Apply `051_add_construction_delivery_floors.up.sql` to production.
2. Verify both columns exist on `manufacturing_pipelines`.
3. Deploy the daemon.

The migration is idempotent (`ADD COLUMN IF NOT EXISTS`), so applying it to a database where AutoMigrate already added the columns is a no-op — there is no reason to skip step 1 on any environment.

---

## Flagged — what the spec and the design notes did not settle

These are honest gaps, not hedges. Each names what is unresolved, what the plan does in the meantime, and who should rule.

### 1. REQUIRED AMENDMENT to Task 8, Step 5 — the delivery leg must close its task

**This is a defect in this plan, found while writing it, and it must be fixed before Task 8 is executed.**

`deliverGateLeg` as written returns `delivered > 0` directly. Every other path out of `supplyTask` funnels through `completeSupply` / `completeOrFail` / `completeOrDefer`, which persist the task's terminal status **and** enqueue the next single-load replenishment. A leg that returns without them leaves its task `EXECUTING` forever: the ready queue drains to nothing, replenishment never re-stages, and the drain goes quiet while reporting RUNNING — a stall that looks exactly like a finished gate.

Amend the leg's two exits to reuse the existing completion machinery rather than inventing a second one:

```go
	// Terminal outcomes go through the SAME completion path every other supply path uses.
	// Skipping it leaves the task EXECUTING forever: nothing re-stages the next load, and a
	// silently-drained ready queue is indistinguishable from a finished gate.
	leg := &supplyLeg{lot: lot, ship: lot.ship, pipeline: pipeline, delivered: delivered}
	if delivered > 0 {
		return h.completeSupply(ctx, leg, delivered)
	}
	return h.completeOrDefer(ctx, leg)
```

and use `completeOrDefer(ctx, &supplyLeg{lot: lot, ship: lot.ship, pipeline: pipeline})` for the fleet-paused stand-down and the empty-trip return, so a paused fleet PARKS its task for the SupplyMonitor to re-activate instead of failing it toward death. Add a test asserting a completed leg enqueues replenishment and a paused leg parks rather than fails.

### 2. The spec's 50k floor does not describe the path this buy actually runs through

The spec and the design notes both say "the 50k `common.ImmutableReserveFloor` applies to **both** fleets, unchanged," and instruct that it not be read, moved or weakened. The plan complies literally — it calls `spendFloorBreached` unchanged and touches no floor.

But that guard does not enforce 50k on this path. `production_executor_spend_guards.go` sets `defaultWorkingCapitalReserve = common.NonContractWorkingCapitalFloor` (sp-q8bon raised the factory/construction input floor from the 50k base to the 150k non-contract floor, because margin-blind gate-fill buys once dragged treasury 638k→142k and deadlocked the contract engine against *its* 50k floor). `budgetedReserveFloor` then raises it further by construction's share of deployable capital when a capital work sensor is wired.

So the delivery fleet is guarded **more** strictly than the spec's number implies, never less. That is the safe direction and nothing in this plan changes it. **But the spec's stated figure is wrong for this path**, and an operator reading "50k" while the fleet parks at 150k+ will misdiagnose it as a bug. Someone should correct the spec's Money Guards section, or explicitly bless the higher floor. Not this plan's call.

### 3. Price as a gate — spec says no, the code path says yes

Spec: "Price is deliberately **not** a gate here." The plan nonetheless keeps the per-tranche `inputPriceCeilingParked` re-check live on the delivery buy (`sourceModeEligible` in `BuyAtTerminalFactory`), because disabling an existing money-adjacent guard is not phase 2's to do and RULINGS #4 says guards are never weakened.

Consequence: a laddering ask can stop a fill part-way. The degradation is graceful — the hull delivers what is aboard and the next leg retries — but it is a behaviour the spec says should not exist. **Orchestrator ruling needed:** keep the ceiling (current plan), or scope it out for the delivery role only. If the latter, it belongs in its own commit with its own justification, not folded into Task 6.

### 4. A mixed trip's second material is recorded a tick late

Nothing in the spec says how a mixed load's *other* material gets recorded against the pipeline. `recordDelivery` is keyed to the lot's task, and a mixed trip delivers a good that task does not name.

The plan records only the lot's own material and relies on the drain's existing per-tick `reconcilePipelinesFromSite`, which re-reads the LIVE construction site and RAISES the delivered counters from the server — the authoritative source, and raise-only, so it cannot lose a delivery. Net effect: the second material's progress is correct within one tick, and the only cost is that one tick's buy sizing may not net it out yet (which is fail-safe — it under-plans, never over-buys).

Cleaner alternatives exist (record against both materials under `recordMu`), but they need a second task handle and would double-count against the reconcile. **Flagged for review against live fill data**, alongside item 5.

### 5. `gateMaxTranchesPerStop = 4` is the weakest inference in the design

Adjudicated ACCEPT-but-flagged, and repeated here so it is not lost: an independent DB probe confirmed the market exposes a supply LEVEL and a per-transaction `trade_volume` and **no stock count**, so trip availability had to be derived. 4 is a judgement call. The supply level still gates *whether* we buy; this only bounds how much one stop can lift. **Revisit against live fill data once phase 2 runs** — if mixed trips consistently under-fill, this constant is the first suspect.

### 6. The pattern-B guard gap is mitigated, not closed

Upheld spec objection: the round-trip test is necessary but not sufficient. A pattern-B regression would not reintroduce a config key in the same commit — it would arrive later as someone "tidying" the floors into `config.yaml` alongside the other manufacturing knobs, at which point the round-trip test still passes (the domain object still round-trips) while the live value is clobbered on the next boot.

The plan's mitigation is the strongest available: `TestDeliverGateLeg_ReadsTheFloorsOffThePipelineRowOnEveryLeg` plus its mutation probe pin the **per-leg read**, so a hoist fails loudly. That does not catch the config-key reintroduction itself. Already filed as a bead (`58df3bda`); it stays open after phase 2.

### 7. Two of the four gate hulls run the legacy path until phase 3

Phase 2 tags `gate-factory` hulls and claims them under their own tag, but gives that role **no new behaviour** — the delivery leg fires only for `gate.DeliveryFleetTag`, so a factory-tagged hull runs today's source-then-deliver path unchanged. That is deliberate (Decomposition item 3 assigns recursive feeding to phase 3), and it is safe, but it means the D/F/F/D order buys two hulls whose role is currently only a label.

Worth stating plainly because it affects how phase 2 will *look* in production: throughput will not double. Half the new hulls behave exactly as before until phase 3 lands.

### 8. Reallocation on pause is out of scope, by the spec's own decomposition

The spec's Fleet Mechanics section describes workers moving to the factory role when delivery pauses and back when it resumes, with a thrash guard. Decomposition item 3 assigns that to phase 3 — "reallocation cannot be built before there is a pause to react to." Phase 2 therefore *produces* the trigger (`BuyPolicy.FleetPaused`, EVERY-not-ANY) and records it, but moves no workers. Confirming that reading explicitly, since the two sections could be read as contradicting each other.

### 9. Characterization tests are deferred with the deletions

Risk 2 and the Testing section require characterization tests over current acquisition behaviour "before deleting the depth cap or the `isTargetGood` branch." Phase 2 deletes nothing — Decomposition item 4 owns the deletions — so no characterization work is included here. **That obligation transfers to phase 4 intact**, and phase 4's plan should open by restating it. Do not let it evaporate between phases.

### 10. Smaller decisions this plan made that no source settled

- **`--good` with `--buy-floor`/`--resume-floor` is rejected, not ignored.** The floors are pipeline-wide; an operator who typed `--good` believes it did something. Rejecting is consistent with this design's whole thesis, but it is a choice.
- **`--clear` does not apply to the floors.** Resetting them means naming the defaults explicitly. An empty value that meant "reset" would make "leave unchanged" inexpressible, and those two intents must not share an encoding on a money-adjacent knob.
- **Pause state resets when the floors are re-tuned.** The policy object is rebuilt on a floor change, discarding pause state. Defensible on adjudication #2's own reasoning — the rule changed, so re-deriving under the new rule costs one tick and never a spend — but the notes did not consider a mid-run re-tune.
- **A resume floor not strictly above the buy floor is rejected at the CLI and re-checked fail-closed at the daemon**, even though `NewBuyPolicy` would silently raise it. Silently correcting an operator mid-tune leaves them with a wrong model of the knob they are actively adjusting.
- **`nextGateRole` fails closed on an unreadable fleet**, deferring the purchase. A role guessed from an unknown count is a hull working the wrong half of the operation, which is worse than one tick of delay.
