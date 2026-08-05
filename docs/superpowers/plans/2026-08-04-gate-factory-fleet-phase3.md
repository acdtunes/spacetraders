# Gate Factory Fleet + Role Reallocation (Phase 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `RoleFactory` its behaviour. Today the tag is written and nothing reads it — `runsGateLeg` returns false for it, so a `gate-factory` hull takes the shared legacy path and phase 2's D/F/F/D order buys two hulls whose role is only a label. Phase 3 delivers: a bounded, cycle-safe recursive **feed walk** that keeps the terminal factories supplied with their inputs and never touches their output; a **feeding leg** on the drain that executes one step of that walk per hull per tick; and **pause-driven role reallocation** with a thrash guard, so a delivery pause becomes a self-shortening feedback loop instead of idle capacity. It also **arms the whole two-fleet feature** by re-roling the legacy `manufacturing` hulls, which today make phase 2 inert on any fleet that already holds four of them.

**Architecture:** Two new pure policy objects join phase 2's three in `internal/domain/manufacturing/gate`: `PlanFeed` (the recursive walk — `visited` + depth cap + the curated raw predicate, with every stop recorded and why) and `PlanReallocation` (the EVERY-material pause trigger, the minimum-dwell thrash guard, and a per-tick move cap, expressed as a deviation from the D/F/F/D **baseline mix** so an unpause returns to the designed split instead of stampeding every hull back to delivery). Neither does I/O. The drain (`RunConstructionCoordinatorHandler`) gains one optional collaborator group via `SetGateFactory` — phase 1's `GateTopology` already answers `IsRaw`/`Inputs`/`FeedTarget`/`ValidateFeedDestination`, so the recipe seam is not new infrastructure — and `runsGateLeg` becomes `gateLegRole`, ONE shared predicate returning the **role** so both its call sites keep asking their own question off one answer. The executor gains `FeedFactory`: navigate, validate the destination imports the cargo (the sp-b27a2 guard, reused not re-implemented), dock, sell. The input BUY reuses phase 2's `BuyAtTerminalFactory` unchanged, so **`fillFromSource` is not edited by this phase at all**.

**Tech Stack:** Go 1.x, standard `testing`. No new dependencies, no new migration, no new RPC, no new CLI verb.

## Global Constraints

Copied verbatim from the task brief and from `docs/superpowers/specs/2026-08-03-gate-construction-two-fleet-design.md`. Where the two disagree the brief wins; where the spec carries a CORRECTION block, the correction supersedes the surrounding prose.

1. **KEEP the fabricate depth cap AND `visited`.** The spec originally said phase 3 could delete the cap because the recipe graph was an acyclic DAG. **That was false and is struck** (bead sp-4irrr, landed `7a14b400`): `goods.ExportToImportMap` is CYCLIC — `IRON_ORE → EXPLOSIVES → LIQUID_HYDROGEN → MACHINERY → IRON → IRON_ORE` — and both gate materials feed into it. `GateTopology.IsRaw` now uses `goods.IsMineableRawMaterial` and cuts traversal at the ore, but **that is not a proof of acyclicity** (the curated list is hand-maintained; the map is game data). Termination = curated predicate **+** `visited` **+** cap. All three.
2. **Do NOT mix `GateTopology.Inputs` with `goods.GetRequiredInputs`.** They diverge in **content**, not just shape: `Inputs("IRON_ORE")` is `nil` while `GetRequiredInputs("IRON_ORE")` is still `{EXPLOSIVES}`. A walk that swapped one for the other would descend an ore into the cycle and stop terminating. **This plan uses `GateTopology.Inputs` exclusively, behind the narrow `gate.Recipe` interface, and never imports `goods` into the walk.** Reason: the walk's question is "what must I still source", which is `Inputs`' contract; `GetRequiredInputs` answers "what does this recipe list", a fabricate-eligibility question, and is correct only for its own four callers.
3. **`AssignFleet` is the single write path** for `dedicated_fleet` (`adapters/api/ship_repository_claims.go:432`), defended by `preserveDedicatedFleetTag`. All reallocation goes through it — never a general ship save.
4. **`ClaimShip` authorizes only when `tag == operation`** (exact equality). A re-roled hull must be claimed under its NEW tag or it is rejected at the DB and silently never works.
5. **`gateLegRole` is the shared routing predicate**, called from both `supplyTask`'s routing and `planDispatchLots`' decline. If it is widened at one site and not the other the decline becomes broader than the routing — phase 2's D2 defect. Keep them sharing one function.
6. **The routing half has NO behavioural pin.** Diverging the routing site kills no test: `supplyTask` claims the task upstream of the branch and the pinning test asserts `claimCount == 1`, which is structurally incapable of observing which leg ran. Phase 3 widens routing, so it **must** add a test that observes **which leg ran**.
7. **No feature flag, no default-off, no arm seam. Ship ARMED.**
8. **Money guards untouched.** `fillFromSource` is the single spend primitive for two callers; any edit to it must be mutation-probed against **both**. **This plan does not edit it.** The floor on this path is `common.NonContractWorkingCapitalFloor` = **150k**, not the 50k base.
9. **ZERO waypoint-symbol literals** in production code. Goods names are invariant and fine.
10. **Protected paths:** never `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.

Carried forward from phase 2, still binding:

- **Delivery is "paused" only when EVERY gate material is paused**, never when any one is. "Because a hull fills greedily from any eligible material, delivery still has useful work while even one material is buyable; moving workers then would starve delivery of capacity it can still use."
- **Supply ordering is SCARCE < LIMITED < MODERATE < HIGH < ABUNDANT.** Pause state is in-memory per process; a restart re-derives it at the cost of one tick and never a spend.
- **Purchase order is D / F / F / D**, and it is load-bearing at every partial N (1→1D, 2→1D/1F, 3→1D/2F, 4→2D/2F). Phase 3 reuses `gate.NextRole` to derive the reallocation **baseline**, so there is no second mix rule to drift.
- **Both policies must record their decisions**, in the MESSAGE — the container log renderer drops metadata maps. Observability ships **inside each task**, never as a final task.

### Environment

**Every command in this plan runs from the WORKTREE, not the repo root.** The worktree is:

```
/Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3
```

Cut it from **local `main`** (origin/main lags by dozens of commits; local main carries phase 1 + phase 2 + `7a14b400`):

```bash
git -C /Users/andres.dandrea/IdeaProjects/cities/spacetraders worktree add \
  /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 -b gate-factory-phase3 main
```

Phase 2's plan used the repo-root path in all nine tasks and every implementer had to work around it. Do not repeat that: **absolute worktree paths only, in every step, including read-only ones.**

**Test output must be filtered.** ~4550 tests across ~107 packages; a raw `go test ./...` will exhaust an agent's context. Always use the filtered forms given in each task:

```
go test -race ./<pkg>/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

`go build` and `go vet` must be run **UNPIPED**. This shell is **zsh** — `PIPESTATUS` does not work, so a piped build hides its own exit status.

**Commits use `git commit --no-verify -- <pathspec>`** (a bd hook auto-stages `.beads/issues.jsonl`; `--no-verify` plus an explicit pathspec keeps the commit code-only).

**Commit BEFORE every mutation probe.** `git checkout --` restores to HEAD and silently erases uncommitted work.

**A mutation probe that names ZERO tests is an infra failure, not a kill.** `go test` prints `[build failed]` in the same shape as a test failure while naming no test. Every probe below states the exact test name it must kill; if that name does not appear, stop and fix the fixture before continuing.

## File Structure

| File | Responsibility |
|---|---|
| `gobot/internal/domain/manufacturing/gate/feed.go` (create) | `Recipe` seam, `PlanFeed` recursive walk, `visited` + depth cap + raw terminator, `FeedStep`/`FeedStop`, the trip log line |
| `gobot/internal/domain/manufacturing/gate/feed_test.go` (create) | Termination tests (real map, synthetic cycle, deep chain), ordering, per-guard falsifiability |
| `gobot/internal/domain/manufacturing/gate/reallocation.go` (create) | `BaselineMix`, `Worker`, `PlanReallocation` (EVERY-paused trigger, dwell, per-tick cap), `ReallocationPlan` + log line |
| `gobot/internal/domain/manufacturing/gate/reallocation_test.go` (create) | Trigger, dwell, cap, baseline-return, legacy-adoption, busy-hull tests |
| `gobot/internal/application/manufacturing/services/production_executor_gate_feed.go` (create) | `FeedFactory` — validate destination, navigate+dock, sell inputs. Spends nothing |
| `gobot/internal/application/manufacturing/services/production_executor_gate_feed_test.go` (create) | Destination-refusal, units-reported, no-buy-path tests |
| `gobot/internal/application/manufacturing/services/production_executor_dock.go` (modify) | `deliverInputs` returns `(revenue, units)` — the feed leg needs units for its record |
| `gobot/internal/application/manufacturing/services/production_executor_fabricate.go` (modify) | Update the single `deliverInputs` call site; extract `feedDestinationRefusedFor` so `FeedFactory` shares the guard |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go` (modify) | `runsGateLeg` → `gateLegRole` (returns the role); `SetGateFactory`; `gateFactory` collaborator struct |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_supply.go` (modify) | Route on the role: factory → feed leg, delivery → delivery leg |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go` (modify) | Decline scoped to `RoleDelivery` off the SAME predicate |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go` (create) | `feedGateLeg` — flush, plan, resolve, buy, feed, record, complete |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go` (create) | Feed-leg behaviour + **the which-leg-ran routing pin** |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go` (create) | `reallocateGateRoles` — the tick hook, dwell ledger, `AssignFleet` writes, legacy adoption |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc_test.go` (create) | Trigger/guard/claim-lifecycle/legacy-arming tests |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator.go` (modify) | `factory` collaborator field, the dwell ledger (`roleMu`/`roleSince`), and the `reallocateGateRoles` call in the tick, ahead of hauler discovery |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_test.go` (modify) | Test doubles only: `drainFakeShipRepo.AssignFleet` recorder (`fleetAssignment`, `assignErr`), `drainStubTaskRepo.lastStatus` |
| `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go` (modify) | Test double only: `countingGateBuyer.acquireZero`, so a money-guard refusal is expressible |
| `gobot/internal/domain/manufacturing/gate/fill_test.go` (modify) | Raise the era-invariance sweep's own coverage assertion from 3 files to 5 |
| `gobot/cmd/spacetraders-daemon/main.go` (modify) | Wire `SetGateFactory` alongside `SetGateDelivery` — ships ARMED |

---

### Task 1: `gate.PlanFeed` — the bounded, cycle-safe recursive feed walk

The factory role's whole policy, pure. It answers one question: *given a gate material, which inputs must reach which factories, in what order, and where did the walk stop and why.*

**Termination is three-fold and every layer is load-bearing.** The spec's original "the recipe DAG bottoms out, so delete the cap" is struck (sp-4irrr): `goods.ExportToImportMap` is CYCLIC — `IRON_ORE → EXPLOSIVES → LIQUID_HYDROGEN → MACHINERY → IRON → IRON_ORE` — and both gate materials feed into it. The curated `IsMineableRawMaterial` predicate cuts that loop at the ore, but the list is hand-maintained and the map is game data, so it is not a proof of acyclicity. This walk carries the curated predicate **and** `visited` **and** the depth cap.

A property worth stating precisely, because it is what makes the probes in Step 6 possible: **`visited` alone terminates (finite good set, each queued once) and the cap alone terminates (bounded depth, finite branching); the raw predicate alone does NOT.** So each of the two structural guards can be broken *individually* without hanging the test process — which is exactly why neither may be deleted on the other's strength, and why each gets its own probe.

**`visited` bounds the WALK, not the WORK.** A good reached from two parents still yields a step per parent — MICROPROCESSORS' factory needs SILICON_CRYSTALS delivered even though ELECTRONICS' factory needs it too — but it is queued once. Conflating the two would drop real feeding work.

**Constraint 2 is discharged here.** The walk consumes `GateTopology.Inputs` through the narrow `Recipe` seam and **never** `goods.GetRequiredInputs`. `Inputs("IRON_ORE")` is `nil` while `GetRequiredInputs("IRON_ORE")` is `{EXPLOSIVES}`; swapping them descends an ore into the cycle and stops terminating. `feed.go` imports no `goods` package at all, so the substitution is not expressible in production code.

**Files:**
- Create: `gobot/internal/domain/manufacturing/gate/feed.go`
- Test: `gobot/internal/domain/manufacturing/gate/feed_test.go`

**Interfaces:**
- Consumes: `Recipe` (`IsRaw(good string) bool`, `Inputs(good string) []string`) — `*services.GateTopology` satisfies it structurally.
- Produces: `DefaultFeedDepthCap`; consts `FeedStopRaw`, `FeedStopAlreadyPlanned`, `FeedStopDepthCap`; `type Recipe interface{...}`; `type FeedStep struct{ Input, Target string; Depth int }`; `type FeedStop struct{ Good, Reason string; Depth int }`; `type FeedPlan struct{ Root string; Steps []FeedStep; Stops []FeedStop }`; `(FeedPlan) LogLine() string`; `PlanFeed(root string, recipes Recipe, depthCap int) FeedPlan`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/domain/manufacturing/gate/feed_test.go`:

```go
package gate

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// mapRecipe mirrors services.GateTopology's Inputs/IsRaw contract over an arbitrary recipe map,
// so the walk can be exercised against BOTH the real game data and synthetic pathologies without
// the domain package importing the services layer.
//
// It reproduces the post-sp-4irrr semantics exactly: IsRaw answers the CURATED mineable predicate
// OR "absent from the map", and Inputs returns nil for anything IsRaw calls raw. That
// biconditional is what the walk's termination rests on, so a fake that got it wrong would test a
// contract production does not have.
type mapRecipe struct {
	recipes  map[string][]string
	mineable map[string]bool
}

func (m mapRecipe) IsRaw(good string) bool {
	if m.mineable[good] {
		return true
	}
	inputs, ok := m.recipes[good]
	return !ok || len(inputs) == 0
}

func (m mapRecipe) Inputs(good string) []string {
	if m.IsRaw(good) {
		return nil
	}
	inputs := m.recipes[good]
	out := make([]string, len(inputs))
	copy(out, inputs)
	return out
}

// realRecipe is the LIVE game data behind the same seam: the exact map and the exact curated
// predicate GateTopology reads.
func realRecipe() mapRecipe {
	mineable := make(map[string]bool)
	for good := range goods.ExportToImportMap {
		if goods.IsMineableRawMaterial(good) {
			mineable[good] = true
		}
	}
	return mapRecipe{recipes: goods.ExportToImportMap, mineable: mineable}
}

func stepKeys(plan FeedPlan) []string {
	keys := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		keys = append(keys, s.Input+"->"+s.Target)
	}
	return keys
}

func hasStop(plan FeedPlan, good, reason string) bool {
	for _, s := range plan.Stops {
		if s.Good == good && s.Reason == reason {
			return true
		}
	}
	return false
}

// THE REAL MAP TERMINATES, from both gate materials, and bottoms out on the CURATED raw
// predicate rather than on the cap. Every era's gate needs these two goods and the recipe graph
// is a game constant, so naming them directly is era-invariant (locations are not, and none
// appear here).
func TestPlanFeed_TerminatesOnRealGameDataFromBothGateMaterials(t *testing.T) {
	for _, root := range []string{"FAB_MATS", "ADVANCED_CIRCUITRY"} {
		plan := PlanFeed(root, realRecipe(), DefaultFeedDepthCap)

		if len(plan.Steps) == 0 {
			t.Fatalf("PlanFeed(%s) produced no feeding work; both gate materials are fabricated goods with inputs", root)
		}
		for _, stop := range plan.Stops {
			if stop.Reason == FeedStopDepthCap {
				t.Fatalf("PlanFeed(%s) hit the DEPTH CAP at %s (depth %d) — the curated raw predicate should bottom this chain out first; the cap is the backstop, not the terminator", root, stop.Good, stop.Depth)
			}
		}
	}
}

// FAB_MATS = {IRON, QUARTZ_SAND}; IRON = {IRON_ORE}; QUARTZ_SAND and IRON_ORE are curated
// mineable raws. The walk must feed the FAB_MATS factory with both of its inputs and feed IRON's
// factory with the ore — and must STOP at both raws.
func TestPlanFeed_FabMatsFeedsIronAndQuartzThenStopsAtTheOre(t *testing.T) {
	plan := PlanFeed("FAB_MATS", realRecipe(), DefaultFeedDepthCap)

	for _, want := range []string{"IRON->FAB_MATS", "QUARTZ_SAND->FAB_MATS", "IRON_ORE->IRON"} {
		if !strings.Contains(strings.Join(stepKeys(plan), " "), want) {
			t.Fatalf("PlanFeed(FAB_MATS) steps %v are missing %q", stepKeys(plan), want)
		}
	}
	for _, raw := range []string{"QUARTZ_SAND", "IRON_ORE"} {
		if !hasStop(plan, raw, FeedStopRaw) {
			t.Fatalf("PlanFeed(FAB_MATS) did not stop at the curated raw %s; stops = %+v", raw, plan.Stops)
		}
	}
	// IRON_ORE's own recipe entry is {EXPLOSIVES}. Descending it is the sp-4irrr cycle
	// (IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE).
	for _, key := range stepKeys(plan) {
		if strings.HasPrefix(key, "EXPLOSIVES->") {
			t.Fatalf("the walk descended an ore into EXPLOSIVES (%q) — that is the cyclic branch, and Inputs() returns nil for a curated raw precisely so it cannot be entered", key)
		}
	}
}

// SHALLOWEST FIRST. The terminal factory's OWN inputs are what it is starved of, so they must be
// planned ahead of anything deeper: a hull that runs one step per trip works the binding
// constraint first.
func TestPlanFeed_OrdersStepsShallowestFirst(t *testing.T) {
	plan := PlanFeed("ADVANCED_CIRCUITRY", realRecipe(), DefaultFeedDepthCap)

	last := 0
	for _, step := range plan.Steps {
		if step.Depth < last {
			t.Fatalf("steps are not depth-ordered: %+v came after depth %d", step, last)
		}
		last = step.Depth
	}
	if plan.Steps[0].Target != "ADVANCED_CIRCUITRY" {
		t.Fatalf("first step targets %s; the terminal factory's own inputs must be planned first", plan.Steps[0].Target)
	}
}

// A good reached from TWO parents yields a step per parent — MICROPROCESSORS' factory needs
// SILICON_CRYSTALS even though ELECTRONICS' factory needs it too. visited bounds the WALK, not
// the WORK, and conflating the two would silently drop real feeding.
func TestPlanFeed_ASharedInputIsFedToEveryFactoryThatNeedsIt(t *testing.T) {
	plan := PlanFeed("ADVANCED_CIRCUITRY", realRecipe(), DefaultFeedDepthCap)

	keys := strings.Join(stepKeys(plan), " ")
	for _, want := range []string{"SILICON_CRYSTALS->ELECTRONICS", "SILICON_CRYSTALS->MICROPROCESSORS"} {
		if !strings.Contains(keys, want) {
			t.Fatalf("steps %v are missing %q — visited must bound traversal, not the work list", stepKeys(plan), want)
		}
	}
}

// CYCLE GUARD, falsifiable without a hang. The cap is still in force, so a walk that lost
// `visited` terminates and produces DUPLICATE steps rather than spinning — which is what makes
// this assertable at all.
func TestPlanFeed_ACyclicRecipeProducesNoDuplicateSteps(t *testing.T) {
	cyclic := mapRecipe{recipes: map[string][]string{
		"ALPHA": {"BETA"},
		"BETA":  {"ALPHA"},
	}}

	plan := PlanFeed("ALPHA", cyclic, DefaultFeedDepthCap)

	seen := map[string]bool{}
	for _, key := range stepKeys(plan) {
		if seen[key] {
			t.Fatalf("PlanFeed walked the ALPHA<->BETA cycle and planned %q twice; steps = %v", key, stepKeys(plan))
		}
		seen[key] = true
	}
	if !hasStop(plan, "ALPHA", FeedStopAlreadyPlanned) {
		t.Fatalf("the cycle closed back on ALPHA but no already_planned stop was recorded; stops = %+v — an operator cannot tell a cut cycle from an empty chain", plan.Stops)
	}
}

// DEPTH CAP, falsifiable without a hang. A deep ACYCLIC chain of non-raw goods: `visited` never
// fires here, so only the cap can stop it.
func TestPlanFeed_ADeepAcyclicChainStopsAtTheDepthCap(t *testing.T) {
	deep := mapRecipe{recipes: map[string][]string{
		"L0": {"L1"}, "L1": {"L2"}, "L2": {"L3"}, "L3": {"L4"}, "L4": {"L5"},
	}}

	plan := PlanFeed("L0", deep, 3)

	for _, step := range plan.Steps {
		if step.Depth > 3 {
			t.Fatalf("step %+v is at depth %d, past the cap of 3 — the cap is the backstop the cyclic recipe map makes non-optional", step, step.Depth)
		}
	}
	if !hasStop(plan, "L3", FeedStopDepthCap) {
		t.Fatalf("no depth_cap stop was recorded at L3; stops = %+v — a truncated chain must say it was truncated, or a starved factory looks like a satisfied one", plan.Stops)
	}
}

// An unset cap resolves to the armed default. There is no off state: a 0 is an unset knob, never
// a disabled guard, and a NEGATIVE cap must not disable it either.
func TestPlanFeed_UnsetOrNegativeCapResolvesToTheArmedDefault(t *testing.T) {
	deep := mapRecipe{recipes: map[string][]string{
		"L0": {"L1"}, "L1": {"L2"}, "L2": {"L3"}, "L3": {"L4"}, "L4": {"L5"},
	}}

	for _, cap := range []int{0, -1} {
		plan := PlanFeed("L0", deep, cap)
		for _, step := range plan.Steps {
			if step.Depth > DefaultFeedDepthCap {
				t.Fatalf("cap %d: step %+v exceeded the default cap %d — an unset cap must never mean unbounded", cap, step, DefaultFeedDepthCap)
			}
		}
	}
}

// A raw ROOT has no feeding work, and says so. Returning an empty plan silently would be
// indistinguishable from a walk that failed.
func TestPlanFeed_ARawRootPlansNothingAndRecordsWhy(t *testing.T) {
	plan := PlanFeed("IRON_ORE", realRecipe(), DefaultFeedDepthCap)

	if len(plan.Steps) != 0 {
		t.Fatalf("PlanFeed(IRON_ORE) planned %v; a curated raw material is bought, never fed", stepKeys(plan))
	}
	if !hasStop(plan, "IRON_ORE", FeedStopRaw) {
		t.Fatalf("stops = %+v, want a raw stop for the root", plan.Stops)
	}
}

// Degenerate inputs are answered, not panicked on: the drain calls this per material per leg and
// a nil seam (an unwired collaborator) must not take the tick down.
func TestPlanFeed_IsTotalOverDegenerateInput(t *testing.T) {
	if plan := PlanFeed("", realRecipe(), DefaultFeedDepthCap); len(plan.Steps) != 0 {
		t.Fatalf("PlanFeed(\"\") planned %v", stepKeys(plan))
	}
	if plan := PlanFeed("FAB_MATS", nil, DefaultFeedDepthCap); len(plan.Steps) != 0 {
		t.Fatalf("PlanFeed with a nil Recipe planned %v", stepKeys(plan))
	}
}

// OBSERVABILITY. A feed plan must be diagnosable from the log alone: what it is feeding, how many
// steps, and where the walk stopped WITH the reason — all in the MESSAGE, because the container
// log renderer drops metadata maps.
func TestFeedPlan_LogLineNamesTheRootTheStepsAndEveryStopReason(t *testing.T) {
	line := PlanFeed("FAB_MATS", realRecipe(), DefaultFeedDepthCap).LogLine()

	for _, want := range []string{"FAB_MATS", "IRON", "QUARTZ_SAND", FeedStopRaw} {
		if !strings.Contains(line, want) {
			t.Fatalf("feed plan log line %q does not name %q", line, want)
		}
	}

	empty := PlanFeed("IRON_ORE", realRecipe(), DefaultFeedDepthCap).LogLine()
	if empty == "" {
		t.Fatal("an empty feed plan produced no log line; a factory fleet with nothing to feed and one that failed to plan must not look identical")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFeed|TestFeedPlan' 2>&1 | tail -20
```

Expected: FAIL — `undefined: PlanFeed`. Not a build failure of the whole package: `role.go`, `buy_policy.go` and `fill.go` still compile.

- [ ] **Step 3: Write minimal implementation**

Create `gobot/internal/domain/manufacturing/gate/feed.go`:

```go
package gate

import (
	"fmt"
	"strings"
)

// DefaultFeedDepthCap is how deep the feed walk may descend below a gate material before it
// stops and says so. Root is depth 0, its direct inputs depth 1.
//
// 3 is deliberately the SAME number as services.defaultFabricateMaxDepth. It is not a coupling —
// the two caps bound different walks — but they bound the same recipe graph for the same reason,
// and a gate chain that the resolver would fabricate three levels down is exactly the chain this
// walk must be able to feed three levels down. Both gate materials bottom out on the curated raw
// predicate well inside it (FAB_MATS at depth 2, ADVANCED_CIRCUITRY at depth 3), so on live data
// the cap is a BACKSTOP that never fires — which is the whole point of keeping it.
//
// This is a tunable's default, not a feature flag: an unset (or negative) cap resolves here
// rather than disabling the guard. There is no unbounded mode.
const DefaultFeedDepthCap = 3

// Feed-walk stop reasons. Every good the walk declines to descend records one, because a walk
// that stops silently rebuilds the exact opacity this design exists to remove: a starved factory
// and a satisfied one would look identical.
const (
	// FeedStopRaw: the curated mineable-raw predicate bottomed the chain out. This is the
	// PRIMARY terminator and the one that should fire on live data.
	FeedStopRaw = "raw"
	// FeedStopAlreadyPlanned: the good is already on the walk. This is the CYCLE GUARD and the
	// diamond de-duplicator in one — the same mechanism, two effects. The step is still recorded
	// (this parent's factory really does need that input); only the traversal stops.
	FeedStopAlreadyPlanned = "already_planned"
	// FeedStopDepthCap: the backstop fired. On live data this should never appear; if it does,
	// either the recipe map changed or the curated raw list has fallen behind it.
	FeedStopDepthCap = "depth_cap"
)

// Recipe is the narrow view of the era's recipe graph the feed walk needs.
// *services.GateTopology satisfies it.
//
// IT MUST BE GateTopology.Inputs, NEVER goods.GetRequiredInputs. The two diverge in CONTENT, not
// merely in shape: since sp-4irrr, Inputs("IRON_ORE") is nil (the curated predicate calls it raw)
// while GetRequiredInputs("IRON_ORE") is still {"EXPLOSIVES"}. A walk built on the latter descends
// an ore into IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE and stops
// terminating. Declaring the seam here, consumer-side, and importing no goods package in this
// file, makes that substitution inexpressible in production code rather than merely discouraged.
type Recipe interface {
	// IsRaw reports whether good must be bought rather than fabricated — the curated
	// mineable-raw predicate, plus "absent from the recipe map".
	IsRaw(good string) bool
	// Inputs returns what good's factory must be fed, or nil when good is raw.
	Inputs(good string) []string
}

// FeedStep is one input that must reach one factory: buy Input at whatever exports it, deliver it
// into the factory that exports Target.
//
// Target is the GOOD, not a waypoint. Locations are resolved at runtime by market role — waypoint
// numbering is regenerated every era, so a symbol at this layer is a bug that survives exactly
// until the next era rolls.
type FeedStep struct {
	Input  string
	Target string
	Depth  int
}

// FeedStop is one good the walk declined to descend, and why.
type FeedStop struct {
	Good   string
	Reason string
	Depth  int
}

// FeedPlan is the whole feeding requirement for one gate material.
type FeedPlan struct {
	Root  string
	Steps []FeedStep
	Stops []FeedStop
}

// LogLine renders the plan for the container log. Everything is in the MESSAGE: the container log
// renderer drops metadata maps, so a plan that reported itself only in metadata would be exactly
// as invisible as one that said nothing.
func (p FeedPlan) LogLine() string {
	steps := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, fmt.Sprintf("%s->%s(d%d)", s.Input, s.Target, s.Depth))
	}
	stops := make([]string, 0, len(p.Stops))
	for _, s := range p.Stops {
		stops = append(stops, fmt.Sprintf("%s: %s", s.Good, s.Reason))
	}

	stepText := "nothing"
	if len(steps) > 0 {
		stepText = strings.Join(steps, ", ")
	}
	if len(stops) == 0 {
		return fmt.Sprintf("Gate factory feed plan for %s: %d step(s) — %s", p.Root, len(p.Steps), stepText)
	}
	return fmt.Sprintf("Gate factory feed plan for %s: %d step(s) — %s; walk stopped at %s",
		p.Root, len(p.Steps), stepText, strings.Join(stops, ", "))
}

// PlanFeed walks the recipe graph below root and returns every input that must reach every
// factory in the chain, shallowest first.
//
// SHALLOWEST FIRST is a breadth-first walk on purpose. The terminal factory's own inputs are what
// it is actually starved of, so a hull that runs one step per trip works the binding constraint
// before anything deeper.
//
// TERMINATION IS THREE-FOLD AND NONE OF THE THREE IS REDUNDANT (sp-4irrr):
//
//  1. recipes.IsRaw — the CURATED mineable-raw predicate. This is the primary terminator and the
//     one that fires on live data; it is what cuts the IRON_ORE loop.
//  2. visited — the cycle guard. The recipe map is game data and the curated list is
//     hand-maintained, so neither is a proof of acyclicity.
//  3. depthCap — the backstop. THE SPEC'S ORIGINAL "the DAG bottoms out, so delete the cap" WAS
//     FALSE AND IS STRUCK: goods.ExportToImportMap closes at least
//     IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE, and both gate
//     materials feed into it.
//
// Note that (2) and (3) each terminate ALONE, while (1) does not. That is deliberate: it is what
// lets each be broken individually in a mutation probe without hanging the test process, and it
// is exactly why neither may be deleted on the other's strength.
//
// VISITED BOUNDS THE WALK, NOT THE WORK. A good reached from two parents still yields one step per
// parent — MICROPROCESSORS' factory needs SILICON_CRYSTALS delivered even though ELECTRONICS'
// factory needs it too — but it is queued once.
//
// Total over degenerate input: a nil seam, an empty root and a non-positive cap all answer rather
// than panic. The drain calls this per material per leg, and an unwired collaborator must not take
// a tick down.
func PlanFeed(root string, recipes Recipe, depthCap int) FeedPlan {
	plan := FeedPlan{Root: root}
	if recipes == nil || root == "" {
		return plan
	}
	if depthCap <= 0 {
		depthCap = DefaultFeedDepthCap
	}

	type pending struct {
		good  string
		depth int
	}
	visited := map[string]bool{root: true}
	queue := []pending{{good: root, depth: 0}}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if recipes.IsRaw(current.good) {
			plan.Stops = append(plan.Stops, FeedStop{Good: current.good, Reason: FeedStopRaw, Depth: current.depth})
			continue
		}
		if current.depth >= depthCap {
			plan.Stops = append(plan.Stops, FeedStop{Good: current.good, Reason: FeedStopDepthCap, Depth: current.depth})
			continue
		}

		for _, input := range recipes.Inputs(current.good) {
			// The step is recorded even for a good already on the walk: THIS factory needs THIS
			// input regardless of who else does. Only the traversal below is de-duplicated.
			plan.Steps = append(plan.Steps, FeedStep{Input: input, Target: current.good, Depth: current.depth + 1})
			if visited[input] {
				plan.Stops = append(plan.Stops, FeedStop{Good: input, Reason: FeedStopAlreadyPlanned, Depth: current.depth + 1})
				continue
			}
			visited[input] = true
			queue = append(queue, pending{good: input, depth: current.depth + 1})
		}
	}
	return plan
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/domain/manufacturing/gate/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

Expected: `ok`, no `FAIL`. Then, UNPIPED:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go vet ./internal/domain/manufacturing/gate/...
```

- [ ] **Step 5: Commit (BEFORE the probes — `git checkout --` erases uncommitted work)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): the recursive feed walk — curated raw predicate + visited + depth cap (phase 3)

PlanFeed answers what the factory role exists to do: which inputs must reach
which factories below a gate material, shallowest first, and where the walk
stopped and why.

TERMINATION IS THREE-FOLD AND NONE IS REDUNDANT (sp-4irrr). The spec's original
'the recipe DAG bottoms out, so delete the cap' was FALSE and is struck:
ExportToImportMap closes IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY
-> IRON -> IRON_ORE and both gate materials feed into it. The curated
mineable-raw predicate cuts that loop, but the list is hand-maintained and the
map is game data, so visited and the cap both stay.

visited and the cap each terminate ALONE; the raw predicate does not. That is
what lets each be probed individually without hanging, and why neither may be
deleted on the other's strength.

The seam is GateTopology.Inputs, never goods.GetRequiredInputs -- the two
diverge in CONTENT (Inputs('IRON_ORE') is nil, GetRequiredInputs is
{EXPLOSIVES}), and this file imports no goods package so the swap is not
expressible.

visited bounds the WALK, not the WORK: a shared input is still fed to every
factory that needs it." -- gobot/internal/domain/manufacturing/gate/feed.go gobot/internal/domain/manufacturing/gate/feed_test.go
```

- [ ] **Step 6a: Mutation-probe the CYCLE GUARD (`visited`)**

Break the traversal de-duplication and confirm a NAMED test dies. The cap is still in force, so this terminates rather than hangs. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\t\tif visited\[input\] \{\n\t\t\t\tplan\.Stops = append\(plan\.Stops, FeedStop\{Good: input, Reason: FeedStopAlreadyPlanned, Depth: current\.depth \+ 1\}\)\n\t\t\t\tcontinue\n\t\t\t\}\n//' internal/domain/manufacturing/gate/feed.go && ! grep -q 'FeedStopAlreadyPlanned, Depth' internal/domain/manufacturing/gate/feed.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFeed' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanFeed_ACyclicRecipeProducesNoDuplicateSteps` (and very likely `--- FAIL: TestPlanFeed_TerminatesOnRealGameDataFromBothGateMaterials`, since COPPER would be re-queued from both ELECTRONICS and MICROPROCESSORS), then `RESTORED`. **A run that names zero tests is an infra failure, not a kill** — `[build failed]` prints in the same shape. Stop and fix before continuing.

- [ ] **Step 6b: Mutation-probe the DEPTH CAP**

Break the backstop and confirm a NAMED test dies. `visited` is still in force, and the fixture chain is acyclic, so this terminates rather than hangs. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\tif current\.depth >= depthCap \{\n\t\t\tplan\.Stops = append\(plan\.Stops, FeedStop\{Good: current\.good, Reason: FeedStopDepthCap, Depth: current\.depth\}\)\n\t\t\tcontinue\n\t\t\}\n//' internal/domain/manufacturing/gate/feed.go && ! grep -q 'Reason: FeedStopDepthCap' internal/domain/manufacturing/gate/feed.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFeed' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanFeed_ADeepAcyclicChainStopsAtTheDepthCap` and `--- FAIL: TestPlanFeed_UnsetOrNegativeCapResolvesToTheArmedDefault`, then `RESTORED`.

- [ ] **Step 6c: Mutation-probe the RAW TERMINATOR**

Break the curated predicate's use and confirm the walk descends the ore into the cycle. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\tif recipes\.IsRaw\(current\.good\) \{/\t\tif false \&\& recipes.IsRaw(current.good) {/' internal/domain/manufacturing/gate/feed.go && grep -q 'if false && recipes.IsRaw' internal/domain/manufacturing/gate/feed.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanFeed' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanFeed_FabMatsFeedsIronAndQuartzThenStopsAtTheOre` (the EXPLOSIVES assertion is the one that catches the descent into the cycle) and `--- FAIL: TestPlanFeed_TerminatesOnRealGameDataFromBothGateMaterials` (the cap now fires), then `RESTORED`.

> Note: `Inputs()` returns nil for a raw good, so with `IsRaw` neutered the walk still cannot enumerate a raw's children through a correct `Recipe`. The `mapRecipe` fake reproduces that biconditional faithfully, which is why this probe kills on the *stop record* and the *cap firing* rather than on a runaway. If this probe names zero tests, the fake has drifted from `GateTopology`'s contract — fix the fake, do not weaken the test.

---

### Task 2: `gate.PlanReallocation` — the pause trigger, the thrash guard, and the baseline mix

"When delivery pauses, its workers move to the factory role; when it unpauses, they move back." Read naively, *back* means every factory hull returns to delivery — including the two the D/F/F/D order **bought** as factory hulls. That empties the factory fleet, starves the terminal factories, and re-trips the pause: a thrash at the design level that no dwell timer can fix.

So this policy expresses reallocation as a **deviation from a baseline**, and the baseline is the purchase order's own mix. `BaselineMix(n)` is derived by running phase 2's `NextRole` n times — the same function, already pinned at every partial N — so there is no second mix rule to drift from the first. Paused, the target is *all factory*. Unpaused, the target is the baseline. Convergence is one move per tick, so the fleet walks back to 2D/2F rather than stampeding.

**Three guards, all of them thrash guards at different timescales:**

- **Busy** — a hull mid-haul is never moved. `AssignFleet` deliberately does not evict a live claim (dedication is "who may claim this next"), but a re-tag mid-flight would still poison the NEXT claim: `constructionLot.claimIdentity` is frozen at plan time, so the worker would present the stale tag and `ClaimShip` would reject it. Moving only idle, unheld hulls makes that unreachable rather than merely unlikely.
- **Dwell** — a minimum time in a role, so supply oscillating at the buy floor cannot oscillate the workforce. The clock is the DRAIN's, not the ship's: a hull this process has never moved has a zero `RoleSince` and is immediately eligible. A restart therefore re-derives, costing at most one move and never a spend — the same reasoning phase 2 applied to pause state.
- **Per-tick move cap** — one hull per tick by default. Each move is free, but a burst would swing the whole fleet on a single noisy observation.

**The spec's stated thrash constraints do not exist.** It says "Existing `worker_rebalancer` constraints apply (`ferry_cooldown_secs`, `max_concurrent_ferries`)". The worker-rebalancer coordinator was **deleted** in `712b6f66` (sp-hoj8u, "retire all factory/profit-manufacturing ops... + now-moot worker-rebalancer coordinator"). `WorkerRebalancerConfig` survives as dead config: `s.workerRebalancerConfig` is written at `daemon_server.go:272` and **read by nothing**, `EnabledOrDefault()` has zero callers, `worker_rebalancer_coordinator` sits in `retiredCommandTypes` (`daemon_server_recovery.go:29`) with no builder, and no launch site exists. Both named knobs appear at exactly four lines in the Go tree, all declarations. They are also the wrong *shape*: they govern a cross-system **ferry** of idle **undedicated** light haulers, and `ship_pool_manager.go:94` hard-skips any tagged hull, so a `gate-factory` hull could never have entered that path. This task therefore declares its own constraints and says so. See the Flagged section.

**Files:**
- Create: `gobot/internal/domain/manufacturing/gate/reallocation.go`
- Test: `gobot/internal/domain/manufacturing/gate/reallocation_test.go`

**Interfaces:**
- Consumes: `time` and this package's own `Role`/`NextRole`/`ParseFleetTag`. Pure — no clock, no repository; the caller passes `Now`.
- Produces: `DefaultRoleDwell`, `DefaultMaxRoleMovesPerTick`; move-reason consts `MoveReasonLegacyAdoption`, `MoveReasonPauseToFactory`, `MoveReasonResumeToBaseline`; skip consts `MoveSkipBusy`, `MoveSkipDwell`, `MoveSkipMoveCap`; `BaselineMix(n int) (delivery, factory int)`; `type Worker struct{ Ship, FleetTag string; Idle bool; RoleSince time.Time }`; `type Move struct{ Ship, From string; To Role; Reason string }`; `type MoveSkip struct{ Ship, Reason string }`; `type ReallocationInput struct{...}`; `type ReallocationPlan struct{...}`; `(ReallocationPlan) LogLine() string`; `PlanReallocation(in ReallocationInput) ReallocationPlan`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/domain/manufacturing/gate/reallocation_test.go`:

```go
package gate

import (
	"strings"
	"testing"
	"time"
)

var reallocNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// idleWorker is a hull eligible on every guard: idle, and never moved by this process.
func idleWorker(ship, tag string) Worker {
	return Worker{Ship: ship, FleetTag: tag, Idle: true}
}

func movedShips(plan ReallocationPlan) []string {
	out := make([]string, 0, len(plan.Moves))
	for _, m := range plan.Moves {
		out = append(out, m.Ship+"->"+m.To.String())
	}
	return out
}

func skipReason(plan ReallocationPlan, ship string) string {
	for _, s := range plan.Skips {
		if s.Ship == ship {
			return s.Reason
		}
	}
	return ""
}

// THE BASELINE IS THE PURCHASE ORDER. BaselineMix must equal what D/F/F/D produces at every N,
// because it is derived from the SAME NextRole and there must be no second mix rule to drift.
func TestBaselineMix_MatchesTheDFFDPurchaseOrderAtEveryN(t *testing.T) {
	cases := []struct{ n, wantDelivery, wantFactory int }{
		{0, 0, 0}, {1, 1, 0}, {2, 1, 1}, {3, 1, 2}, {4, 2, 2}, {8, 4, 4},
	}
	for _, tc := range cases {
		delivery, factory := BaselineMix(tc.n)
		if delivery != tc.wantDelivery || factory != tc.wantFactory {
			t.Fatalf("BaselineMix(%d) = %dD/%dF, want %dD/%dF", tc.n, delivery, factory, tc.wantDelivery, tc.wantFactory)
		}
	}
	if delivery, factory := BaselineMix(-3); delivery != 0 || factory != 0 {
		t.Fatalf("BaselineMix(-3) = %dD/%dF; a negative census is answered, not extrapolated", delivery, factory)
	}
}

// PAUSED: delivery hulls move to the factory role. This is what makes the pause a
// self-shortening feedback loop — the hulls go feed the factory that is low, so supply recovers
// sooner and delivery resumes.
func TestPlanReallocation_APausedFleetMovesDeliveryHullsToTheFactoryRole(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("F-1", FactoryFleetTag),
			idleWorker("F-2", FactoryFleetTag),
		},
		MaxMoves: 4,
	})

	if len(plan.Moves) != 2 {
		t.Fatalf("moves = %v, want both delivery hulls moved to factory", movedShips(plan))
	}
	for _, m := range plan.Moves {
		if m.To != RoleFactory {
			t.Fatalf("move %+v targets %v; a paused fleet moves TOWARD the factory role", m, m.To)
		}
		if m.From != DeliveryFleetTag {
			t.Fatalf("move %+v came from %q; a factory hull is already where the paused target wants it and must not be re-tagged", m, m.From)
		}
		if m.Reason != MoveReasonPauseToFactory {
			t.Fatalf("move %+v reason = %q, want %q — an operator must be able to tell WHY a hull changed role", m, m.Reason, MoveReasonPauseToFactory)
		}
	}
}

// UNPAUSED: the fleet returns to the D/F/F/D BASELINE, not to all-delivery.
//
// A literal reading of "they move back" would return every factory hull to delivery, including
// the two the purchase order BOUGHT as factory hulls — emptying the factory fleet, starving the
// terminal factories, and re-tripping the pause. That is a thrash no dwell timer can fix.
func TestPlanReallocation_AnUnpausedFleetReturnsToTheBaselineMixNotToAllDelivery(t *testing.T) {
	workers := []Worker{
		idleWorker("H-1", FactoryFleetTag),
		idleWorker("H-2", FactoryFleetTag),
		idleWorker("H-3", FactoryFleetTag),
		idleWorker("H-4", FactoryFleetTag),
	}

	// Converge with an unbounded move cap so the END STATE is asserted, not the pacing.
	plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: false, Workers: workers, MaxMoves: 10})

	if len(plan.Moves) != 2 {
		t.Fatalf("moves = %v, want exactly 2 hulls back to delivery (baseline 2D/2F), not all 4", movedShips(plan))
	}
	for _, m := range plan.Moves {
		if m.To != RoleDelivery {
			t.Fatalf("move %+v targets %v; an unpaused fleet is short of DELIVERY", m, m.To)
		}
		if m.Reason != MoveReasonResumeToBaseline {
			t.Fatalf("move %+v reason = %q, want %q", m, m.Reason, MoveReasonResumeToBaseline)
		}
	}
	if plan.WantDelivery != 2 || plan.WantFactory != 2 {
		t.Fatalf("target mix = %dD/%dF, want the 2D/2F baseline for 4 hulls", plan.WantDelivery, plan.WantFactory)
	}
}

// A fleet already AT its target moves nothing. Reallocation is a correction, not a heartbeat.
func TestPlanReallocation_AFleetAtItsTargetMovesNothing(t *testing.T) {
	workers := []Worker{
		idleWorker("D-1", DeliveryFleetTag),
		idleWorker("D-2", DeliveryFleetTag),
		idleWorker("F-1", FactoryFleetTag),
		idleWorker("F-2", FactoryFleetTag),
	}

	plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: false, Workers: workers, MaxMoves: 10})

	if len(plan.Moves) != 0 {
		t.Fatalf("moves = %v; a fleet already at the baseline must not churn", movedShips(plan))
	}
}

// LEGACY ADOPTION — THE ARMING FIX. A hull carrying the legacy "manufacturing" tag holds no
// role, so phase 2's leg never fires for it. On a fleet already holding four of them the ramp
// buys nothing (GateWorkers == gateWorkerTarget), so no role tag is EVER written and the entire
// two-fleet feature is inert. Adopting them is what arms it, and it costs no purchase.
//
// The adoption order is the D/F/F/D order itself, via the same NextRole the purchase path uses.
func TestPlanReallocation_LegacyHullsAreAdoptedIntoRolesInTheDFFDOrder(t *testing.T) {
	workers := []Worker{
		idleWorker("L-1", LegacyFleetTag),
		idleWorker("L-2", LegacyFleetTag),
		idleWorker("L-3", LegacyFleetTag),
		idleWorker("L-4", LegacyFleetTag),
	}

	// One move per tick is the default; drive four ticks and re-feed the plan's own decisions
	// back in, exactly as the drain does.
	tagOf := map[string]string{"L-1": LegacyFleetTag, "L-2": LegacyFleetTag, "L-3": LegacyFleetTag, "L-4": LegacyFleetTag}
	order := make([]Role, 0, 4)
	for tick := 0; tick < 4; tick++ {
		live := make([]Worker, 0, len(workers))
		for _, w := range workers {
			live = append(live, idleWorker(w.Ship, tagOf[w.Ship]))
		}
		plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: false, Workers: live, MaxMoves: 1})
		if len(plan.Moves) != 1 {
			t.Fatalf("tick %d: moves = %v, want exactly one (the default per-tick cap)", tick, movedShips(plan))
		}
		move := plan.Moves[0]
		if move.Reason != MoveReasonLegacyAdoption {
			t.Fatalf("tick %d: move %+v reason = %q, want %q", tick, move, move.Reason, MoveReasonLegacyAdoption)
		}
		tagOf[move.Ship] = move.To.FleetTag()
		order = append(order, move.To)
	}

	want := []Role{RoleDelivery, RoleFactory, RoleFactory, RoleDelivery}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("adoption order = %v, want the D/F/F/D purchase order %v — the mix rule must be NextRole, not a second one", order, want)
		}
	}
}

// A legacy hull is adopted even while the fleet is PAUSED: it holds no role, so moving it can
// only fill a deficit. The paused target is all-factory, so that is where it goes.
func TestPlanReallocation_ALegacyHullIsAdoptedEvenWhilePaused(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{idleWorker("L-1", LegacyFleetTag)},
		MaxMoves:       1,
	})

	if len(plan.Moves) != 1 || plan.Moves[0].To != RoleFactory {
		t.Fatalf("moves = %v, want the legacy hull adopted into the factory role while paused", movedShips(plan))
	}
}

// THRASH GUARD 1 — BUSY. A hull mid-haul is never re-tagged. constructionLot.claimIdentity is
// FROZEN at plan time, so a re-tag in flight makes the worker present a stale tag and ClaimShip
// rejects it (it authorizes only when tag == operation) — the hull is dispatched and then
// silently never works.
func TestPlanReallocation_AHullMidHaulIsNeverMoved(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: false},
			idleWorker("D-2", DeliveryFleetTag),
		},
		MaxMoves: 10,
	})

	for _, m := range plan.Moves {
		if m.Ship == "D-1" {
			t.Fatalf("moved D-1 while it was mid-haul; its lot's frozen claim identity would then be rejected at the DB")
		}
	}
	if got := skipReason(plan, "D-1"); got != MoveSkipBusy {
		t.Fatalf("skip reason for the busy hull = %q, want %q — a hull held back must say so, or a stalled reallocation looks like a satisfied one", got, MoveSkipBusy)
	}
}

// THRASH GUARD 2 — DWELL. Supply oscillating at the buy floor must not oscillate the workforce.
func TestPlanReallocation_AHullInsideItsDwellWindowIsHeld(t *testing.T) {
	justMoved := Worker{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: true, RoleSince: reallocNow.Add(-time.Minute)}
	settled := Worker{Ship: "D-2", FleetTag: DeliveryFleetTag, Idle: true, RoleSince: reallocNow.Add(-time.Hour)}

	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{justMoved, settled},
		Dwell:          10 * time.Minute,
		MaxMoves:       10,
	})

	for _, m := range plan.Moves {
		if m.Ship == "D-1" {
			t.Fatalf("moved D-1 one minute into a 10-minute dwell — that is the oscillation the dwell exists to stop")
		}
	}
	if got := skipReason(plan, "D-1"); got != MoveSkipDwell {
		t.Fatalf("skip reason inside the dwell = %q, want %q", got, MoveSkipDwell)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].Ship != "D-2" {
		t.Fatalf("moves = %v; the settled hull must still move — the dwell holds one hull, not the whole policy", movedShips(plan))
	}
}

// A hull this process has NEVER moved has a zero RoleSince and is eligible immediately. The
// dwell clock is the drain's, not the ship's; refusing on an absent record would deadlock the
// arming after every restart.
func TestPlanReallocation_AnUnseenHullIsEligibleImmediately(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers:        []Worker{{Ship: "D-1", FleetTag: DeliveryFleetTag, Idle: true}}, // zero RoleSince
		Dwell:          time.Hour,
		MaxMoves:       1,
	})

	if len(plan.Moves) != 1 {
		t.Fatalf("moves = %v; a hull with no dwell record must be movable, or a restart deadlocks every reallocation", movedShips(plan))
	}
}

// THRASH GUARD 3 — PER-TICK MOVE CAP. Each move is free, but a burst would swing the whole
// fleet on one noisy observation.
func TestPlanReallocation_TheMoveCapBoundsOneTickAndRecordsWhoWasHeld(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
			idleWorker("D-3", DeliveryFleetTag),
		},
		MaxMoves: 1,
	})

	if len(plan.Moves) != 1 {
		t.Fatalf("moves = %v, want exactly 1 under a cap of 1", movedShips(plan))
	}
	held := 0
	for _, s := range plan.Skips {
		if s.Reason == MoveSkipMoveCap {
			held++
		}
	}
	if held != 2 {
		t.Fatalf("skips = %+v, want the 2 hulls the cap held recorded as %q", plan.Skips, MoveSkipMoveCap)
	}
}

// Unset knobs resolve to the ARMED defaults. There is no off state: a 0 is an unset knob, never
// a disabled policy, and a 0 move cap must not mean "move nothing".
func TestPlanReallocation_UnsetKnobsResolveToTheArmedDefaults(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			idleWorker("D-2", DeliveryFleetTag),
		},
	})

	if len(plan.Moves) != DefaultMaxRoleMovesPerTick {
		t.Fatalf("moves = %v with an unset cap; an unset knob must resolve to the armed default %d, never to zero", movedShips(plan), DefaultMaxRoleMovesPerTick)
	}
	if DefaultRoleDwell <= 0 {
		t.Fatalf("DefaultRoleDwell = %s; a non-positive dwell is no thrash guard at all", DefaultRoleDwell)
	}
}

// An empty fleet is answered, not panicked on. The drain calls this every tick, including
// before any gate hull exists.
func TestPlanReallocation_AnEmptyFleetIsAnswered(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{Now: reallocNow, DeliveryPaused: true})
	if len(plan.Moves) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want an empty plan for an empty fleet", plan)
	}
}

// OBSERVABILITY. The reallocation must be diagnosable from the log alone: paused state, the
// target mix, the census, every move, and every hull held back with its reason — in the MESSAGE,
// because the container log renderer drops metadata maps.
func TestReallocationPlan_LogLineNamesThePauseTheTargetTheMovesAndTheHolds(t *testing.T) {
	plan := PlanReallocation(ReallocationInput{
		Now:            reallocNow,
		DeliveryPaused: true,
		Workers: []Worker{
			idleWorker("D-1", DeliveryFleetTag),
			{Ship: "D-2", FleetTag: DeliveryFleetTag, Idle: false},
		},
		MaxMoves: 10,
	})

	line := plan.LogLine()
	for _, want := range []string{"PAUSED", "D-1", "factory", "D-2", MoveSkipBusy} {
		if !strings.Contains(line, want) {
			t.Fatalf("reallocation log line %q does not name %q", line, want)
		}
	}

	quiet := PlanReallocation(ReallocationInput{
		Now:     reallocNow,
		Workers: []Worker{idleWorker("D-1", DeliveryFleetTag), idleWorker("F-1", FactoryFleetTag)},
	}).LogLine()
	if quiet == "" {
		t.Fatal("a no-move reallocation produced no log line; a settled fleet and a broken reallocator must not look identical")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanReallocation|TestBaselineMix|TestReallocationPlan' 2>&1 | tail -20
```

Expected: FAIL — `undefined: PlanReallocation`, `undefined: BaselineMix`.

- [ ] **Step 3: Write minimal implementation**

Create `gobot/internal/domain/manufacturing/gate/reallocation.go`:

```go
package gate

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultRoleDwell is the minimum time a hull stays in a role before it may be moved again.
	// It is the reallocation's own hysteresis: the buy/resume floors stop supply chatter, this
	// stops the WORKFORCE chatter one level up.
	//
	// The spec directs reuse of the worker_rebalancer's ferry_cooldown_secs and
	// max_concurrent_ferries. Those knobs are DEAD: the coordinator was deleted in 712b6f66
	// (sp-hoj8u), WorkerRebalancerConfig is written into DaemonServer and read by nothing, and
	// worker_rebalancer_coordinator sits in retiredCommandTypes with no builder and no launch
	// site. They are also the wrong shape — they govern a cross-system ferry of UNDEDICATED
	// light haulers, and ship_pool_manager.go's `if ship.DedicatedFleet() != "" { continue }`
	// excludes every gate hull from that pool by construction. So this policy declares its own.
	//
	// 10m against a 30s drain tick: a supply level that dips and recovers inside ten minutes
	// costs zero role changes. Tunables' defaults, not feature flags — an unset value resolves
	// here rather than disabling the guard.
	DefaultRoleDwell = 10 * time.Minute

	// DefaultMaxRoleMovesPerTick bounds how far one tick may swing the workforce. A move spends
	// nothing, but a burst would re-role the whole fleet on a single noisy observation; at 1 a
	// 4-hull fleet still converges in under two minutes.
	DefaultMaxRoleMovesPerTick = 1
)

// Move reasons. A role change with no stated cause is exactly the opacity this design exists to
// remove — an operator seeing a hull flip roles must be able to tell a pause from an adoption.
const (
	MoveReasonLegacyAdoption   = "legacy hull adopted into a role fleet"
	MoveReasonPauseToFactory   = "delivery paused — every gate material is below its buy floor"
	MoveReasonResumeToBaseline = "delivery resumed — returning to the D/F/F/D baseline mix"
)

// Skip reasons: why a hull the target mix WANTED moved was held back. Only actionable declines
// are recorded — a hull already where the target wants it is not a decision.
const (
	MoveSkipBusy    = "busy"
	MoveSkipDwell   = "dwell"
	MoveSkipMoveCap = "move_cap"
)

// Worker is one gate hull as the reallocator sees it.
//
// Idle means idle AND not in transit AND not held by a live supply worker — the caller collapses
// all three, because all three mean the same thing here: something is mid-haul with this hull.
//
// RoleSince is when THIS PROCESS last moved the hull. The zero value means "never moved by us"
// and is eligible immediately: the dwell clock is the drain's, not the ship's, and refusing on an
// absent record would deadlock every reallocation after a restart. Losing it on restart costs at
// most one move and never a spend — the same trade phase 2 made for pause state.
type Worker struct {
	Ship      string
	FleetTag  string
	Idle      bool
	RoleSince time.Time
}

// Move is one role change to execute through AssignFleet — the single write path for the
// dedicated_fleet column (RULINGS #3).
type Move struct {
	Ship   string
	From   string // the hull's live fleet tag, for the log and for an idempotence check
	To     Role
	Reason string
}

// MoveSkip is one hull the target mix wanted moved and a guard held back.
type MoveSkip struct {
	Ship   string
	Reason string
}

// ReallocationInput is one tick's view. Now is passed in rather than read, so the policy stays
// pure and the dwell is testable without a sleep.
type ReallocationInput struct {
	Now time.Time
	// DeliveryPaused is EVERY gate material paused, never any one of them. A hull fills greedily
	// from whatever is eligible, so delivery still has useful work while one material is
	// buyable; moving workers then would starve delivery of capacity it can still use.
	DeliveryPaused bool
	Workers        []Worker
	Dwell          time.Duration
	MaxMoves       int
}

// ReallocationPlan is the tick's ruling, materialized.
type ReallocationPlan struct {
	DeliveryPaused bool
	WantDelivery   int
	WantFactory    int
	HaveDelivery   int
	HaveFactory    int
	Unroled        int
	Moves          []Move
	Skips          []MoveSkip
}

// LogLine renders the whole ruling for the container log — everything in the MESSAGE, because
// the container log renderer drops metadata maps.
func (p ReallocationPlan) LogLine() string {
	state := "running"
	if p.DeliveryPaused {
		state = "PAUSED"
	}
	moves := make([]string, 0, len(p.Moves))
	for _, m := range p.Moves {
		moves = append(moves, fmt.Sprintf("%s %s->%s (%s)", m.Ship, m.From, m.To, m.Reason))
	}
	held := make([]string, 0, len(p.Skips))
	for _, s := range p.Skips {
		held = append(held, fmt.Sprintf("%s: %s", s.Ship, s.Reason))
	}

	moveText := "no role changes"
	if len(moves) > 0 {
		moveText = strings.Join(moves, "; ")
	}
	line := fmt.Sprintf("Gate roles (delivery %s): have %dD/%dF + %d unroled, want %dD/%dF — %s",
		state, p.HaveDelivery, p.HaveFactory, p.Unroled, p.WantDelivery, p.WantFactory, moveText)
	if len(held) == 0 {
		return line
	}
	return line + "; held " + strings.Join(held, ", ")
}

// BaselineMix is the role split the D/F/F/D purchase order would have produced for n hulls.
//
// Derived by RUNNING NextRole rather than by a formula, deliberately: the purchase order is
// already pinned at every partial N by the role tests, and a second expression of the same rule
// is a second thing to drift. A negative census answers 0/0 rather than extrapolating.
func BaselineMix(n int) (delivery, factory int) {
	for i := 0; i < n; i++ {
		if NextRole(delivery, factory) == RoleDelivery {
			delivery++
			continue
		}
		factory++
	}
	return delivery, factory
}

// PlanReallocation rules on this tick's workforce split.
//
// THE TARGET IS A BASELINE, NOT A DIRECTION. A literal reading of "when delivery unpauses its
// workers move back" returns EVERY factory hull to delivery — including the two the purchase
// order bought as factory hulls — which empties the factory fleet, starves the terminal
// factories, and re-trips the pause. That is a thrash no dwell timer can fix, so the unpaused
// target is the D/F/F/D baseline and convergence is capped per tick.
//
// Paused, the target is ALL FACTORY. That is what makes the pause self-shortening: delivery
// pauses because a terminal factory is low, those hulls go feed it, it produces faster, supply
// recovers sooner, delivery resumes. It is also what makes an aggressive buy floor safe — over-
// buying costs a reallocation, not a stall.
//
// UNROLED (legacy-tagged) hulls are considered FIRST. They hold no role, so moving one can only
// ever fill a deficit and never open one — and it is the arming fix: on a fleet already holding
// four legacy hulls the purchase ramp buys nothing, so no role tag is ever written and the whole
// two-fleet feature is inert.
func PlanReallocation(in ReallocationInput) ReallocationPlan {
	plan := ReallocationPlan{DeliveryPaused: in.DeliveryPaused}
	if len(in.Workers) == 0 {
		return plan
	}

	dwell := in.Dwell
	if dwell <= 0 {
		dwell = DefaultRoleDwell
	}
	maxMoves := in.MaxMoves
	if maxMoves <= 0 {
		maxMoves = DefaultMaxRoleMovesPerTick
	}

	plan.WantDelivery, plan.WantFactory = BaselineMix(len(in.Workers))
	if in.DeliveryPaused {
		plan.WantDelivery, plan.WantFactory = 0, len(in.Workers)
	}

	for _, worker := range in.Workers {
		role, roled := ParseFleetTag(worker.FleetTag)
		switch {
		case !roled:
			plan.Unroled++
		case role == RoleDelivery:
			plan.HaveDelivery++
		default:
			plan.HaveFactory++
		}
	}

	haveDelivery, haveFactory := plan.HaveDelivery, plan.HaveFactory
	for _, worker := range unroledFirst(in.Workers) {
		role, roled := ParseFleetTag(worker.FleetTag)
		target, wanted := moveTarget(roled, role,
			plan.WantDelivery-haveDelivery, plan.WantFactory-haveFactory,
			haveDelivery, haveFactory)
		if !wanted {
			continue // already where the target mix wants it: not a decision, so not recorded
		}
		if len(plan.Moves) >= maxMoves {
			plan.Skips = append(plan.Skips, MoveSkip{Ship: worker.Ship, Reason: MoveSkipMoveCap})
			continue
		}
		if !worker.Idle {
			plan.Skips = append(plan.Skips, MoveSkip{Ship: worker.Ship, Reason: MoveSkipBusy})
			continue
		}
		if !worker.RoleSince.IsZero() && in.Now.Sub(worker.RoleSince) < dwell {
			plan.Skips = append(plan.Skips, MoveSkip{Ship: worker.Ship, Reason: MoveSkipDwell})
			continue
		}

		plan.Moves = append(plan.Moves, Move{
			Ship:   worker.Ship,
			From:   worker.FleetTag,
			To:     target,
			Reason: moveReason(in.DeliveryPaused, roled),
		})
		if roled {
			if role == RoleDelivery {
				haveDelivery--
			} else {
				haveFactory--
			}
		}
		if target == RoleDelivery {
			haveDelivery++
			continue
		}
		haveFactory++
	}
	return plan
}

// unroledFirst orders the candidates: legacy/unroled hulls, then the rest in input order. Stable
// and deterministic, so a tick's ruling is reproducible from its inputs alone.
func unroledFirst(workers []Worker) []Worker {
	ordered := make([]Worker, 0, len(workers))
	for _, worker := range workers {
		if _, roled := ParseFleetTag(worker.FleetTag); !roled {
			ordered = append(ordered, worker)
		}
	}
	for _, worker := range workers {
		if _, roled := ParseFleetTag(worker.FleetTag); roled {
			ordered = append(ordered, worker)
		}
	}
	return ordered
}

// moveTarget answers which role (if any) this hull should move to.
//
// When BOTH roles are short — an all-legacy fleet arming from scratch — the D/F/F/D order
// decides, through the SAME NextRole the purchase path uses. There is deliberately no second mix
// rule: one expression of the order, pinned once.
func moveTarget(roled bool, role Role, needDelivery, needFactory, haveDelivery, haveFactory int) (Role, bool) {
	var target Role
	switch {
	case needDelivery > 0 && needFactory > 0:
		target = NextRole(haveDelivery, haveFactory)
	case needFactory > 0:
		target = RoleFactory
	case needDelivery > 0:
		target = RoleDelivery
	default:
		return 0, false
	}
	if roled && role == target {
		return 0, false
	}
	return target, true
}

// moveReason names WHY, so a role change is diagnosable from the log without a code read.
func moveReason(paused, roled bool) string {
	if !roled {
		return MoveReasonLegacyAdoption
	}
	if paused {
		return MoveReasonPauseToFactory
	}
	return MoveReasonResumeToBaseline
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/domain/manufacturing/gate/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

Expected: `ok`, no `FAIL`. Then, UNPIPED: `go vet ./internal/domain/manufacturing/gate/...`

- [ ] **Step 5: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): pause-driven role reallocation with a baseline mix and a three-part thrash guard

Paused, the target is ALL FACTORY -- that is what makes the pause
self-shortening: the hulls go feed the factory that is low, so supply recovers
sooner and delivery resumes.

Unpaused, the target is the D/F/F/D BASELINE, not all-delivery. A literal 'they
move back' would return the two hulls the purchase order BOUGHT as factory
hulls, emptying the factory fleet, starving the terminal factories, and
re-tripping the pause -- a thrash no dwell timer can fix. BaselineMix is derived
by RUNNING NextRole, so there is no second mix rule to drift.

Three guards: a busy hull is never re-tagged (its lot's FROZEN claimIdentity
would then be rejected by ClaimShip, which authorizes only on tag == operation);
a minimum dwell stops workforce chatter at the supply boundary; a per-tick move
cap stops one noisy observation swinging the whole fleet.

Legacy 'manufacturing' hulls are ADOPTED into roles, in the D/F/F/D order. That
is the arming fix: on a fleet already holding four of them the ramp buys nothing
(GateWorkers == gateWorkerTarget), so no role tag is ever written and phase 2 is
inert.

The spec directs reuse of worker_rebalancer's ferry_cooldown_secs /
max_concurrent_ferries. Those are DEAD -- the coordinator was deleted in
712b6f66 (sp-hoj8u), the config struct is written and read by nothing, and the
knobs govern an UNDEDICATED cross-system ferry pool no gate hull can enter. This
policy declares its own and documents why." -- gobot/internal/domain/manufacturing/gate/reallocation.go gobot/internal/domain/manufacturing/gate/reallocation_test.go
```

- [ ] **Step 6a: Mutation-probe the BUSY guard**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\tif !worker\.Idle \{\n\t\t\tplan\.Skips = append\(plan\.Skips, MoveSkip\{Ship: worker\.Ship, Reason: MoveSkipBusy\}\)\n\t\t\tcontinue\n\t\t\}\n//' internal/domain/manufacturing/gate/reallocation.go && ! grep -q 'Reason: MoveSkipBusy' internal/domain/manufacturing/gate/reallocation.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanReallocation|TestReallocationPlan' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/reallocation.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanReallocation_AHullMidHaulIsNeverMoved` and `--- FAIL: TestReallocationPlan_LogLineNamesThePauseTheTargetTheMovesAndTheHolds`, then `RESTORED`.

- [ ] **Step 6b: Mutation-probe the DWELL guard**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\tif !worker\.RoleSince\.IsZero\(\) && in\.Now\.Sub\(worker\.RoleSince\) < dwell \{\n\t\t\tplan\.Skips = append\(plan\.Skips, MoveSkip\{Ship: worker\.Ship, Reason: MoveSkipDwell\}\)\n\t\t\tcontinue\n\t\t\}\n//' internal/domain/manufacturing/gate/reallocation.go && ! grep -q 'Reason: MoveSkipDwell' internal/domain/manufacturing/gate/reallocation.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanReallocation' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/reallocation.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanReallocation_AHullInsideItsDwellWindowIsHeld`, then `RESTORED`.

- [ ] **Step 6c: Mutation-probe the PER-TICK MOVE CAP**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|if len(plan.Moves) >= maxMoves {|if false \&\& len(plan.Moves) >= maxMoves {|' internal/domain/manufacturing/gate/reallocation.go && grep -q 'if false && len(plan.Moves) >= maxMoves' internal/domain/manufacturing/gate/reallocation.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanReallocation' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/reallocation.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanReallocation_TheMoveCapBoundsOneTickAndRecordsWhoWasHeld`, `--- FAIL: TestPlanReallocation_LegacyHullsAreAdoptedIntoRolesInTheDFFDOrder` and `--- FAIL: TestPlanReallocation_UnsetKnobsResolveToTheArmedDefaults`, then `RESTORED`.

- [ ] **Step 6d: Mutation-probe the BASELINE RETURN (the anti-stampede)**

Make the unpaused target all-delivery — the literal reading of "they move back" — and confirm the anti-stampede test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|plan.WantDelivery, plan.WantFactory = BaselineMix(len(in.Workers))|plan.WantDelivery, plan.WantFactory = len(in.Workers), 0|' internal/domain/manufacturing/gate/reallocation.go && grep -q 'plan.WantDelivery, plan.WantFactory = len(in.Workers), 0' internal/domain/manufacturing/gate/reallocation.go && echo "MUTATION APPLIED" && go test ./internal/domain/manufacturing/gate/ -run 'TestPlanReallocation' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/domain/manufacturing/gate/reallocation.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestPlanReallocation_AnUnpausedFleetReturnsToTheBaselineMixNotToAllDelivery` and `--- FAIL: TestPlanReallocation_AFleetAtItsTargetMovesNothing`, then `RESTORED`.

---

### Task 3: `ProductionExecutor.FeedFactory` — the driven-port seam that puts inputs INTO a factory

The delivery role's terminal is `DeliverToConstructionSite`. The factory role's terminal is a **sell into the factory's import listing** — the factory buys its own inputs — which is what `deliverInputs` already does inside `fabricateGood`. This task exposes that as a pinned-destination seam and **reuses the sp-b27a2 guard rather than re-implementing it**.

**This method spends nothing.** It navigates and sells. The input BUY is phase 2's `BuyAtTerminalFactory`, unchanged, which routes through `fillFromSource` with every money guard re-checked per tranche and failing closed. **Constraint 8 is discharged by not editing `fillFromSource` at all** — there is no new spend primitive in this phase, so there is no second caller to keep in step. The floor in force stays `defaultWorkingCapitalReserve` = `common.NonContractWorkingCapitalFloor` (**150k**), raised further by the per-operation capital budget.

Two small refactors ride along, each with an existing suite as its safety net:

1. `deliverInputs` returns `(revenue, units int)`. The feed leg's record needs units — "sold something" and "sold 60 IRON" are different facts to an operator watching a starved factory. It has exactly **one** caller (`production_executor_fabricate.go:141`).
2. `feedDestinationRefused`'s body is extracted to `feedDestinationRefusedFor(ctx, inputs, factoryWaypoint, playerID)`, and the existing method delegates. Sharing it is the point: a second copy of the sp-b27a2 guard is free to drift, and the failure mode of a drifted copy is a hull stranded at 80/80 with cargo it can neither deliver nor dump.

**Files:**
- Create: `gobot/internal/application/manufacturing/services/production_executor_gate_feed.go`
- Modify: `gobot/internal/application/manufacturing/services/production_executor_dock.go` (`deliverInputs` signature)
- Modify: `gobot/internal/application/manufacturing/services/production_executor_fabricate.go` (call site + guard extraction)
- Test: `gobot/internal/application/manufacturing/services/production_executor_gate_feed_test.go`

**Interfaces:**
- Consumes: `(*ProductionExecutor).NavigateAndDock`, `.deliverInputs`, `.gateTopology().ValidateFeedDestination`, `.marketRepo.GetMarketData`.
- Produces: `type FeedResult struct{ WaypointSymbol string; UnitsDelivered, Revenue int; Refused bool }`; `(*ProductionExecutor) FeedFactory(ctx context.Context, ship *navigation.Ship, destination *MarketLocatorResult, inputs []string, playerID int, opContext *shared.OperationContext) (*FeedResult, error)`; `(*ProductionExecutor) feedDestinationRefusedFor(ctx context.Context, inputs []string, factoryWaypoint string, playerID shared.PlayerID) bool`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/application/manufacturing/services/production_executor_gate_feed_test.go`:

```go
package services

import (
	"testing"
)

// THE FACTORY ROLE'S TERMINAL. FeedFactory puts inputs INTO a factory at a PINNED destination —
// the waypoint the caller already resolved by market role. It never re-decides where to go, and
// it never spends: the buy is BuyAtTerminalFactory's job, so every money guard stays on one path.
//
// The fixtures are the sp-b27a2 ones (newFeedDestinationRun): a hull LOADED with SILICON_CRYSTALS
// and COPPER, and a factory whose IMPORT listings are the variable under test.

// The same-chain case, which is the overwhelming majority in production: the factory imports
// every input aboard, so the hull flies there and delivers.
func TestFeedFactory_DeliversTheInputsAtAFactoryThatImportsThem(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("feeding a factory that imports every input must not error: %v", err)
	}
	if result == nil || result.Refused {
		t.Fatalf("result = %+v, want an accepted feed", result)
	}
	if !mediator.navigatedToFactory() {
		t.Fatalf("the hull never reached %s", fdFactoryWP)
	}
	if mediator.sellCount() == 0 {
		t.Fatal("no input was delivered at a factory that imports every one of them")
	}
	if result.UnitsDelivered == 0 {
		t.Fatalf("result = %+v; UnitsDelivered is what an operator reads to tell a fed factory from a starved one — 'sold something' is not the same fact as 'sold 20 units'", result)
	}
}

// THE sp-b27a2 GUARD, ON THE NEW PATH. The factory exports its output but imports none of the
// inputs aboard, so the hull would arrive with cargo it can neither deliver nor dump. Refuse the
// NAVIGATE — do not fly and hope, and never substitute another waypoint.
func TestFeedFactory_RefusesToFlyToADestinationThatCannotAcceptTheCargo(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, false)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("a refused destination parks the leg, it does not error it: %v", err)
	}
	if result == nil || !result.Refused {
		t.Fatalf("result = %+v, want Refused — a silently-skipped feed and a delivered one must not look the same", result)
	}
	if mediator.navigatedToFactory() {
		t.Fatalf("the hull was flown to %s, which imports none of its cargo — that is the sp-b27a2 stranding, reproduced on the factory-fleet path", fdFactoryWP)
	}
	if mediator.sellCount() != 0 {
		t.Fatalf("sold %d time(s) at a destination the leg refused to fly to", mediator.sellCount())
	}
}

// IT SPENDS NOTHING. The whole point of keeping the buy on BuyAtTerminalFactory is that there is
// exactly ONE spend primitive on this path; a purchase issued here would be a second one, outside
// the tranche loop's per-iteration, fail-closed money guards (RULINGS #4).
func TestFeedFactory_SpendsNothing(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)

	if _, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil); err != nil {
		t.Fatalf("FeedFactory: %v", err)
	}
	if spent := mediator.creditsSpent(); spent != 0 {
		t.Fatalf("FeedFactory spent %d credits; the buy belongs to BuyAtTerminalFactory so every money guard stays on ONE path", spent)
	}
}

// Refuses a caller bug rather than resolving around it. Picking a destination here would undo the
// role-based topology resolution the caller performed — the same pinning contract
// BuyAtTerminalFactory holds on the buy side.
func TestFeedFactory_RefusesAnUnresolvedOrEmptyRequest(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)
	ctx := feedDestinationCtx()

	if _, err := executor.FeedFactory(ctx, ship, nil, []string{"COPPER"}, 1, nil); err == nil {
		t.Fatal("a nil destination must be refused, not resolved here")
	}
	if _, err := executor.FeedFactory(ctx, ship, &MarketLocatorResult{}, []string{"COPPER"}, 1, nil); err == nil {
		t.Fatal("a destination with no waypoint must be refused")
	}
	if _, err := executor.FeedFactory(ctx, ship, &MarketLocatorResult{WaypointSymbol: fdFactoryWP}, nil, 1, nil); err == nil {
		t.Fatal("a feed naming no inputs must be refused: ValidateFeedDestination accepts an empty input list, so this would fly a hull somewhere for no reason")
	}
	if mediator.navigatedToFactory() {
		t.Fatal("a refused request must not move the hull")
	}
}

// deliverInputs' new units figure must be the units actually SOLD, not the goods count. The
// fixture sells 10 SILICON_CRYSTALS + 10 COPPER, so a units-vs-goods confusion (2 vs 20) is
// visible here and nowhere else.
func TestFeedFactory_ReportsUnitsSoldNotGoodsSold(t *testing.T) {
	executor, _, ship := newFeedDestinationRun(t, true)

	result, err := executor.FeedFactory(feedDestinationCtx(), ship,
		&MarketLocatorResult{WaypointSymbol: fdFactoryWP}, []string{"SILICON_CRYSTALS", "COPPER"}, 1, nil)
	if err != nil {
		t.Fatalf("FeedFactory: %v", err)
	}
	if result.UnitsDelivered != 20 {
		t.Fatalf("UnitsDelivered = %d, want 20 (10 SILICON_CRYSTALS + 10 COPPER) — a goods count would read 2", result.UnitsDelivered)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/application/manufacturing/services/ -run 'TestFeedFactory' 2>&1 | tail -20
```

Expected: FAIL — `executor.FeedFactory undefined (type *ProductionExecutor has no field or method FeedFactory)`.

- [ ] **Step 3a: Change `deliverInputs` to report units, and update its single caller**

In `gobot/internal/application/manufacturing/services/production_executor_dock.go`, change the signature and the two return points. Replace:

```go
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) int {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0
	deliveredGoods := 0
```

with:

```go
// It reports both the revenue and the UNITS delivered. Units is what the gate FACTORY leg records
// against a starved factory: "sold something" and "sold 60 IRON" are different facts to an
// operator, and only the second one says whether the feed is keeping up.
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) (revenue, units int) {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0
	totalUnits := 0
	deliveredGoods := 0
```

Inside the loop, after `totalRevenue += response.TotalRevenue`, add `totalUnits += response.UnitsSold`. Change the final `return totalRevenue` to `return totalRevenue, totalUnits`.

Then in `gobot/internal/application/manufacturing/services/production_executor_fabricate.go`, change the single call site (line ~141):

```go
	// The factory IMPORTS the inputs, so delivering them is a sell.
	deliveryRevenue, _ := e.deliverInputs(ctx, updatedShip, playerIDValue)
```

- [ ] **Step 3b: Extract the sp-b27a2 guard so both feed paths share ONE copy**

In `gobot/internal/application/manufacturing/services/production_executor_fabricate.go`, replace the body of `feedDestinationRefused` with a delegation and add the shared form beneath it:

```go
func (e *ProductionExecutor) feedDestinationRefused(
	ctx context.Context,
	run fabricationRun,
	factoryWaypoint string,
	playerID shared.PlayerID,
) bool {
	return e.feedDestinationRefusedFor(ctx, run.haulingInputs(), factoryWaypoint, playerID)
}

// feedDestinationRefusedFor is the sp-b27a2 guard itself, over an explicit input list.
//
// TWO callers share this ONE copy, and that sharing is the whole point: the fabricate path passes
// run.haulingInputs(), the gate FACTORY leg passes the inputs it bought for a feed step. A second
// copy would be free to drift, and the failure mode of a drifted copy is a hull at 80/80 with
// cargo it can neither deliver nor dump.
//
// There is no fallback to another waypoint on refusal. Substituting one is precisely how cargo
// ends up somewhere that cannot accept it, which is the incident this guard exists to prevent.
func (e *ProductionExecutor) feedDestinationRefusedFor(
	ctx context.Context,
	inputs []string,
	factoryWaypoint string,
	playerID shared.PlayerID,
) bool {
	// An unreadable listing is passed through as nil, which ValidateFeedDestination refuses.
	// Reading the destination's OWN listing is what makes the check exact: a system can hold
	// several markets importing a good, so the best-bid importer is not the question here.
	destination, err := e.marketRepo.GetMarketData(ctx, factoryWaypoint, playerID.Value())
	if err != nil {
		destination = nil
	}

	refusal := e.gateTopology().ValidateFeedDestination(destination, factoryWaypoint, inputs)
	if refusal == nil {
		return false
	}

	// Cause in the MESSAGE: the container-log renderer drops metadata.
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Refusing to haul %v to %s: %v — the hull would arrive with cargo it can neither deliver nor dump (sp-b27a2)",
		inputs, factoryWaypoint, refusal,
	), map[string]interface{}{
		"inputs": inputs, "factory": factoryWaypoint,
		"action": "feed_refused", "reason": "destination_cannot_accept_inputs",
		"error": refusal.Error(),
	})
	return true
}
```

> The log message loses `run.node.Good` and gains the input list. That is a deliberate improvement: the good being fabricated is not what strands, the cargo aboard is, and the existing tests assert on the NAVIGATE, not on the message text. If any test does assert the old text, that is a finding to report, not a nuisance to fix quietly.

- [ ] **Step 3c: Write `FeedFactory`**

Create `gobot/internal/application/manufacturing/services/production_executor_gate_feed.go`:

```go
package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FeedResult is one feed leg's outcome: where the inputs went, how much arrived, and — when the
// destination refused them — that the leg declined rather than delivered.
//
// Refused is a distinct field, not an error and not a zero. A leg that skipped silently and one
// that delivered nothing look identical from a zero-unit result, and telling those two apart is
// the entire reason this design exists.
type FeedResult struct {
	WaypointSymbol string
	UnitsDelivered int
	Revenue        int
	Refused        bool
}

// FeedFactory delivers the inputs a hull is carrying INTO a PINNED factory — the waypoint the
// caller already resolved by market role — and reports what arrived.
//
// It deliberately does NOT resolve a destination. The caller resolved it by role; re-deciding
// here would silently undo that, exactly as selecting a source inside BuyAtTerminalFactory would
// undo the terminal-factory pinning. Refusing an unusable request is correct; substituting
// another waypoint is the failure mode this phase exists to prevent.
//
// IT SPENDS NOTHING. Feeding a factory is a SELL into its import listing. The BUY that put the
// inputs aboard is BuyAtTerminalFactory's, which routes through the shared fillFromSource tranche
// loop where the working-capital floor (spendFloorBreached, re-read against live treasury) and
// the cross-container concurrent-spend reservation are re-checked EVERY iteration and both fail
// closed. Keeping the buy there means this phase adds no second spend primitive and edits none of
// the existing one (RULINGS #4). The floor in force is defaultWorkingCapitalReserve —
// common.NonContractWorkingCapitalFloor, the 150k non-contract band, NOT the 50k base — raised
// further by the per-operation capital budget when a work sensor is wired.
//
// THE sp-b27a2 GUARD RUNS BEFORE THE NAVIGATE, through the SAME feedDestinationRefusedFor the
// fabricate path uses. That incident dispatched IRON_ORE to a waypoint which did not import it,
// and the haulers then sat at 80/80 unable to deliver OR dump. Checking the destination's own
// listing before flying is the root-cause fix; deliverInputs' hold-what-it-cannot-sell behaviour
// only limits the damage after the hull is already at the wrong waypoint.
//
// Refuses (error, nil result) on a nil or unnamed destination and on an empty input list. The
// last one matters: ValidateFeedDestination accepts an empty list by design (carrying nothing
// cannot strand anything), so without this check a caller bug would fly a hull across a system to
// deliver nothing.
func (e *ProductionExecutor) FeedFactory(
	ctx context.Context,
	ship *navigation.Ship,
	destination *MarketLocatorResult,
	inputs []string,
	playerID int,
	opContext *shared.OperationContext,
) (*FeedResult, error) {
	if destination == nil {
		return nil, fmt.Errorf("cannot feed: no factory was resolved — refusing to pick a destination here")
	}
	if destination.WaypointSymbol == "" {
		return nil, fmt.Errorf("cannot feed: the resolved factory has no waypoint")
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("cannot feed %s: no inputs were named for the trip", destination.WaypointSymbol)
	}

	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}
	playerIDValue := shared.MustNewPlayerID(playerID)

	if e.feedDestinationRefusedFor(ctx, inputs, destination.WaypointSymbol, playerIDValue) {
		return &FeedResult{WaypointSymbol: destination.WaypointSymbol, Refused: true}, nil
	}

	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), destination.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to the feed target %s: %w", destination.WaypointSymbol, err)
	}

	// The factory IMPORTS these goods, so delivering them is a sell. deliverInputs holds back
	// anything this market will not take rather than aborting the whole delivery — a refused sell
	// must not poison the rest of the hold.
	revenue, units := e.deliverInputs(ctx, updatedShip, playerIDValue)
	return &FeedResult{WaypointSymbol: destination.WaypointSymbol, UnitsDelivered: units, Revenue: revenue}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass — the WHOLE services package, not just the new tests**

The `deliverInputs` signature change and the guard extraction touch shared code, so the whole package must be green:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/application/manufacturing/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

Expected: `ok` for every package, no `FAIL`. Then, UNPIPED — `go build` skips `_test.go`, so only `go vet` catches a stale call site in another package's tests:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go vet ./...
```

- [ ] **Step 5: Commit (BEFORE the probe)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): FeedFactory — deliver inputs INTO a pinned factory, spending nothing (phase 3)

The factory role's terminal. The delivery role unloads at the construction site;
the factory role SELLS into the factory's import listing, which is what
deliverInputs already does inside fabricateGood. Exposed here as a
pinned-destination seam that never re-resolves where to go.

IT SPENDS NOTHING. The buy stays on BuyAtTerminalFactory -> fillFromSource, so
this phase adds no second spend primitive and edits none of the existing one:
the per-tranche working-capital floor (150k NonContractWorkingCapitalFloor, not
the 50k base) and the concurrent-spend reservation are untouched (RULINGS #4).

The sp-b27a2 guard runs BEFORE the navigate, through the SAME
feedDestinationRefusedFor the fabricate path now delegates to. One copy, two
callers -- a drifted second copy strands a hull at 80/80 with cargo it can
neither deliver nor dump.

deliverInputs now also reports UNITS. 'Sold something' and 'sold 60 IRON' are
different facts to an operator watching a starved factory, and only the second
says whether the feed is keeping up. One call site updated." -- gobot/internal/application/manufacturing/services/production_executor_gate_feed.go gobot/internal/application/manufacturing/services/production_executor_gate_feed_test.go gobot/internal/application/manufacturing/services/production_executor_dock.go gobot/internal/application/manufacturing/services/production_executor_fabricate.go
```

- [ ] **Step 6a: Mutation-probe the sp-b27a2 guard on the NEW path**

Neuter the refusal inside `FeedFactory` and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|if e.feedDestinationRefusedFor(ctx, inputs, destination.WaypointSymbol, playerIDValue) {|if false \&\& e.feedDestinationRefusedFor(ctx, inputs, destination.WaypointSymbol, playerIDValue) {|' internal/application/manufacturing/services/production_executor_gate_feed.go && grep -q 'if false && e.feedDestinationRefusedFor' internal/application/manufacturing/services/production_executor_gate_feed.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/services/ -run 'TestFeedFactory' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/services/production_executor_gate_feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestFeedFactory_RefusesToFlyToADestinationThatCannotAcceptTheCargo`, then `RESTORED`.

- [ ] **Step 6b: Mutation-probe that the EXTRACTION did not break the ORIGINAL caller**

The extraction's hazard is that `feedDestinationRefused` stops actually consulting the guard while `FeedFactory` still does — the fabricate path would silently regain sp-b27a2. Break the delegation and confirm the ORIGINAL suite dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|return e.feedDestinationRefusedFor(ctx, run.haulingInputs(), factoryWaypoint, playerID)|return false // MUTANT|' internal/application/manufacturing/services/production_executor_fabricate.go && grep -q 'return false // MUTANT' internal/application/manufacturing/services/production_executor_fabricate.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/services/ -run 'TestFabricate' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/services/production_executor_fabricate.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestFabricate_RefusesToNavigateToAFactoryThatCannotAcceptTheInputs`, then `RESTORED`. **This probe is the reason the extraction is safe** — it proves the pre-existing caller still reaches the guard through the new indirection. Zero named tests here means the extraction is unpinned; stop.

---

### Task 4: `feedGateLeg` — the factory role's leg: plan, resolve, buy, feed

One hull, one tick, **one feed step**. The leg walks the outstanding gate materials neediest-first, takes the first step whose source and destination both resolve, buys a hull-load of that input at its exporter through the **unchanged** `BuyAtTerminalFactory`, and feeds it into the factory that needs it.

**One step per leg is the bound on factory-fleet spend.** There is no bill for an input — the construction site's bill is denominated in gate materials, not in IRON_ORE — so nothing downstream caps how much feedstock a hull could buy. What caps it is: one hull-load per leg (`BuyAtTerminalFactory` stamps the trip allocation as the fill target, bounded by hull capacity), the market's per-transaction `trade_volume`, and the working-capital floor re-checked per tranche and failing closed. That is deliberate and stated, because "how much is the factory fleet allowed to spend" is a question the spec never answers.

**The ABUNDANT skip is a fail-safe, not a knob.** A factory already exporting at the top of the supply ladder does not need feeding. Skipping it costs one deferred leg; feeding it burns treasury into a full warehouse. It is deliberately the ladder's top and nothing else — the moment this becomes a threshold it becomes a knob, and this phase adds none.

**The flush is not optional.** A factory hull can hold a gate material — a hull re-roled between ticks, a restart mid-leg. `marketBuys` refuses an EXPORT listing, so a terminal factory will not take FAB_MATS off a hull; that cargo would ride forever and the hold would never free. The leg therefore reuses `flushOnHandGateMaterials` (phase 2's, unchanged) to unload gate materials at the SITE before it plans anything. That is also why a flush that delivered something makes this leg `completeSupply` rather than defer.

**Terminal state.** A leg that delivered nothing to the site parks its task via `completeOrDefer`, exactly as phase 2's fleet-paused stand-down does. It never simply returns: a task left EXECUTING is never re-staged, the ready queue drains silently, and the drain reports RUNNING while doing nothing. See Flagged item 3 for the cost of that choice.

**Files:**
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go` (the `gateFactory` collaborator set + `SetGateFactory`)
- Create: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go`
- Test: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go`

**Interfaces:**
- Consumes: `gate.PlanFeed`; `GateBuyer.BuyAtTerminalFactory` (phase 2, unchanged); `(*services.ProductionExecutor).FeedFactory`; `(*services.GateTopology).TerminalFactory/IsRaw/Inputs`.
- Produces: `type GateFactoryTopology interface{ TerminalFactory(...); IsRaw(string) bool; Inputs(string) []string }`; `type GateFeeder interface{ FeedFactory(...) (*mfgServices.FeedResult, error) }`; `(*RunConstructionCoordinatorHandler) SetGateFactory(topology GateFactoryTopology, buyer GateBuyer, feeder GateFeeder)`; `(*RunConstructionCoordinatorHandler) feedGateLeg(ctx, cmd, systemSymbol, lot, playerID) bool`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go`:

```go
package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE FACTORY ROLE'S LEG. It feeds INPUTS into the factories that export the gate materials and
// never touches their output — that boundary is the whole two-fleet design.
//
// Driven through the drain's own seam (feedGateLeg), never through gate.PlanFeed, which has its
// own package tests.

// stubFactoryTopology answers all three role questions the feed leg asks: the exporter of a good
// (which doubles as the RAW SOURCE role, per phase 1), and the recipe seam.
//
// Its IsRaw/Inputs reproduce GateTopology's post-sp-4irrr biconditional exactly — Inputs returns
// nil for anything IsRaw calls raw — because the leg's termination rests on it.
type stubFactoryTopology struct {
	mu           sync.Mutex
	recipes      map[string][]string
	mineable     map[string]bool
	supplyByGood map[string]string
	errByGood    map[string]error
	asked        []string
}

func newStubFactoryTopology() *stubFactoryTopology {
	return &stubFactoryTopology{
		// FAB_MATS = {IRON, QUARTZ_SAND}; IRON = {IRON_ORE}. QUARTZ_SAND and IRON_ORE are raw.
		recipes:  map[string][]string{gateMaterialPrimary: {"IRON", "QUARTZ_SAND"}, "IRON": {"IRON_ORE"}},
		mineable: map[string]bool{"QUARTZ_SAND": true, "IRON_ORE": true},
	}
}

func (s *stubFactoryTopology) IsRaw(good string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mineable[good] {
		return true
	}
	inputs, ok := s.recipes[good]
	return !ok || len(inputs) == 0
}

func (s *stubFactoryTopology) Inputs(good string) []string {
	if s.IsRaw(good) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inputs := s.recipes[good]
	out := make([]string, len(inputs))
	copy(out, inputs)
	return out
}

func (s *stubFactoryTopology) TerminalFactory(_ context.Context, good, _ string, _ int) (*mfgServices.MarketLocatorResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, good)
	if err, ok := s.errByGood[good]; ok {
		return nil, err
	}
	supply := "MODERATE"
	if level, ok := s.supplyByGood[good]; ok {
		supply = level
	}
	return &mfgServices.MarketLocatorResult{
		WaypointSymbol: good + "-EXPORTER",
		Supply:         supply,
		Price:          100,
		TradeVolume:    gateTestTradeVolume,
	}, nil
}

func (s *stubFactoryTopology) goodsAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

// recordingFeeder records every factory feed: where it went and what it carried.
type recordingFeeder struct {
	mu    sync.Mutex
	calls []feedCall
	err   error
	units int
}

type feedCall struct {
	waypoint string
	inputs   []string
}

func (f *recordingFeeder) FeedFactory(_ context.Context, _ *navigation.Ship, destination *mfgServices.MarketLocatorResult, inputs []string, _ int, _ *shared.OperationContext) (*mfgServices.FeedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if destination == nil || destination.WaypointSymbol == "" {
		return nil, errors.New("feed attempted without a resolved destination")
	}
	f.calls = append(f.calls, feedCall{waypoint: destination.WaypointSymbol, inputs: append([]string(nil), inputs...)})
	units := f.units
	if units == 0 {
		units = 40
	}
	return &mfgServices.FeedResult{WaypointSymbol: destination.WaypointSymbol, UnitsDelivered: units}, nil
}

func (f *recordingFeeder) feeds() []feedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feedCall(nil), f.calls...)
}

// gateFeedFixture is one wired FACTORY-role drain plus every seam a test asserts on.
type gateFeedFixture struct {
	*gateLegFixture
	topo   *stubFactoryTopology
	feeder *recordingFeeder
}

func newGateFactoryHandler(t *testing.T) *gateFeedFixture {
	t.Helper()
	base := newGateDeliveryHandler(t)
	topo := newStubFactoryTopology()
	feeder := &recordingFeeder{}
	base.handler.SetGateFactory(topo, base.buyer, feeder)
	return &gateFeedFixture{gateLegFixture: base, topo: topo, feeder: feeder}
}

// gateFactoryLot is an EXECUTING lot claimed under the FACTORY tag — the state
// claimTaskForSupply leaves behind before supplyTask routes to a leg.
func gateFactoryLot(t *testing.T, ship *navigation.Ship) constructionLot {
	t.Helper()
	lot := gateTestLot(t, ship)
	lot.claimIdentity = gate.FactoryFleetTag
	return lot
}

func (f *gateFeedFixture) runFeed(t *testing.T, hull string) bool {
	t.Helper()
	ship := gateTestHull(t, hull, gate.FactoryFleetTag)
	return f.handler.feedGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateFactoryLot(t, ship), shared.MustNewPlayerID(1))
}

// THE BOUNDARY. The leg buys an INPUT and feeds it into the factory that needs it. It must never
// buy the gate material itself — that is the delivery fleet's leg, and doing both here is the
// serialized production-then-haul this design exists to split apart.
func TestFeedGateLeg_BuysAnInputAndFeedsItIntoTheFactoryThatNeedsIt(t *testing.T) {
	f := newGateFactoryHandler(t)

	f.runFeed(t, "GF-1")

	bought := f.buyer.goods()
	if len(bought) != 1 {
		t.Fatalf("bought %v, want exactly one input on a one-step leg", bought)
	}
	if _, boughtGateMaterial := bought[gateMaterialPrimary]; boughtGateMaterial {
		t.Fatalf("the factory leg bought %s — the gate material itself. It feeds INPUTS and never touches terminal output; buying it here re-serializes production and hauling", gateMaterialPrimary)
	}

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want exactly one factory fed", feeds)
	}
	if len(feeds[0].inputs) != 1 {
		t.Fatalf("feed carried %v; one step per leg means one input", feeds[0].inputs)
	}
	if _, ok := bought[feeds[0].inputs[0]]; !ok {
		t.Fatalf("fed %v but bought %v — the leg must deliver what it just bought", feeds[0].inputs, bought)
	}
}

// SHALLOWEST FIRST. The terminal factory's OWN inputs are what it is starved of, so the first
// step must target the terminal factory, not something three levels down.
func TestFeedGateLeg_FeedsTheTerminalFactoryBeforeAnythingDeeper(t *testing.T) {
	f := newGateFactoryHandler(t)

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v, want one", feeds)
	}
	// The destination is resolved by ROLE from the good — the exporter of FAB_MATS — never from
	// a hardcoded symbol.
	if feeds[0].waypoint != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("fed %s; the first step must target the TERMINAL factory (the %s exporter), which is what the fleet is actually starved of", feeds[0].waypoint, gateMaterialPrimary)
	}
	input := feeds[0].inputs[0]
	if input != "IRON" && input != "QUARTZ_SAND" {
		t.Fatalf("fed %s to the FAB_MATS factory; its recipe is {IRON, QUARTZ_SAND}", input)
	}
}

// THE ABUNDANT FAIL-SAFE. A factory already exporting at the top of the supply ladder does not
// need feeding; buying into a full warehouse burns treasury for nothing. Deliberately the ladder's
// TOP and nothing else — the moment this is a threshold it is a knob, and this phase adds none.
func TestFeedGateLeg_SkipsAFactoryWhoseOutputIsAlreadyAbundant(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.supplyByGood = map[string]string{gateMaterialPrimary: "ABUNDANT", gateMaterialSecondary: "ABUNDANT"}

	f.runFeed(t, "GF-1")

	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("fed %+v; a factory whose output is ABUNDANT needs no feedstock", feeds)
	}
	if bought := f.buyer.goods(); len(bought) != 0 {
		t.Fatalf("bought %v for a factory that is already full", bought)
	}
	if !strings.Contains(f.logLines(), "ABUNDANT") {
		t.Fatalf("the skip is invisible in the log:\n%s", f.logLines())
	}
}

// A step whose SOURCE cannot be resolved is skipped, and the leg moves on to the next step
// rather than standing the whole hull down. A refusal is never a substitution: sending a hull to
// some other waypoint is how cargo ends up somewhere it cannot be used (sp-b27a2).
func TestFeedGateLeg_SkipsAStepWhoseSourceCannotBeResolvedAndTakesTheNextOne(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.errByGood = map[string]error{"IRON": errors.New("no market exports IRON")}

	f.runFeed(t, "GF-1")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %+v; an unresolvable IRON source must not stand the hull down while QUARTZ_SAND is still feedable", feeds)
	}
	if feeds[0].inputs[0] != "QUARTZ_SAND" {
		t.Fatalf("fed %v, want the next resolvable step (QUARTZ_SAND)", feeds[0].inputs)
	}
	if !strings.Contains(f.logLines(), "IRON") {
		t.Fatalf("the declined IRON step is invisible in the log:\n%s", f.logLines())
	}
}

// A leg that can plan NOTHING parks its task through the SHARED completion machinery. Returning
// bare would leave the task EXECUTING forever: nothing re-stages the next load, the ready queue
// drains to nothing, and the drain reports RUNNING while doing nothing — a stall indistinguishable
// from a finished gate.
func TestFeedGateLeg_ParksItsTaskWhenNothingCanBeFed(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.topo.errByGood = map[string]error{"IRON": errors.New("no exporter"), "QUARTZ_SAND": errors.New("no exporter")}

	if drained := f.runFeed(t, "GF-1"); drained {
		t.Fatal("a leg that fed nothing must not report a drain")
	}
	if status := f.taskRepo.lastStatus(); status == "" {
		t.Fatal("the task was left EXECUTING; a silently-drained ready queue is indistinguishable from a finished gate")
	}
}

// THE FLUSH. A factory hull holding a GATE MATERIAL cannot sell it at a terminal factory —
// marketBuys refuses an EXPORT listing — so that cargo would ride forever and the hold would never
// free. Unload it at the SITE first, through the SAME path the delivery leg uses.
func TestFeedGateLeg_UnloadsAnyGateMaterialAboardAtTheSiteBeforePlanning(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestLadenHull(t, "GF-1", gateMaterialPrimary, gateTestHoldCapacity)
	ship.SetDedicatedFleet(gate.FactoryFleetTag)

	f.handler.feedGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateFactoryLot(t, ship), shared.MustNewPlayerID(1))

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a factory hull arrived full of %s and never unloaded it; the terminal factory will not buy its own export, so that hold never frees and the hull is wedged forever", gateMaterialPrimary)
	}
}

// A buy that acquired NOTHING — the money or price guards stopped the fill — must not fly the
// hull to a factory with an empty hold. Fail-closed: the guards' refusal is honoured, not
// worked around.
func TestFeedGateLeg_DoesNotFeedWhenTheBuyAcquiredNothing(t *testing.T) {
	f := newGateFactoryHandler(t)
	f.buyer.acquireZero = true

	f.runFeed(t, "GF-1")

	if feeds := f.feeder.feeds(); len(feeds) != 0 {
		t.Fatalf("flew to %+v with nothing aboard; a money-guard refusal must stand, not be routed around", feeds)
	}
	if !strings.Contains(f.logLines(), "nothing") {
		t.Fatalf("a refused buy is invisible in the log:\n%s", f.logLines())
	}
}

// OBSERVABILITY. The spec requires the factory feed to record: good, resolved feed target,
// dispatched vs declined WITH the reason. All in the MESSAGE — the container log renderer drops
// metadata maps.
func TestFeedGateLeg_RecordsTheGoodTheTargetAndTheOutcome(t *testing.T) {
	f := newGateFactoryHandler(t)

	f.runFeed(t, "GF-1")

	lines := f.logLines()
	for _, want := range []string{gateMaterialPrimary, "-EXPORTER", "GF-1"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("the feed leg's log does not name %q:\n%s", want, lines)
		}
	}
}

// UNWIRED, the leg is a no-op that parks rather than a nil panic. The drain must survive a
// partially-wired build (existing coordinator tests construct one).
func TestFeedGateLeg_IsSafeWhenTheFactoryCollaboratorsAreUnwired(t *testing.T) {
	f := newGateDeliveryHandler(t) // SetGateFactory deliberately NOT called
	ship := gateTestHull(t, "GF-1", gate.FactoryFleetTag)

	if drained := f.handler.feedGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateFactoryLot(t, ship), shared.MustNewPlayerID(1)); drained {
		t.Fatal("an unwired factory leg reported a drain")
	}
}
```

Two small fixture additions are needed in the existing phase-2 test files, because these tests read seams phase 2 never asserted on. Add to `run_construction_coordinator_gate_delivery_test.go`:

```go
// acquireZero models the money/price guards stopping the fill: the buy is attempted and returns
// a zero-quantity result, which is the shape fillFromSource produces when spendFloorBreached or
// the price ceiling trips. Distinct from an ERROR, which is a failed call.
func (b *countingGateBuyer) resultFor(good string, units int) *mfgServices.ProductionResult {
	if b.acquireZero {
		return &mfgServices.ProductionResult{QuantityAcquired: 0}
	}
	return &mfgServices.ProductionResult{QuantityAcquired: units}
}
```

and add the field `acquireZero bool` to `countingGateBuyer`, returning `b.resultFor(good, units)` in place of the current literal. Add to `drainStubTaskRepo` (in `run_construction_coordinator_test.go`) an accessor if one is absent:

```go
// lastStatus is the terminal status the drain persisted for the most recent task, "" if it
// persisted none — which is what a leg that skipped the completion machinery leaves behind.
func (r *drainStubTaskRepo) lastStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.updated) == 0 {
		return ""
	}
	return string(r.updated[len(r.updated)-1].Status())
}
```

> **Before writing these, READ the two files.** `drainStubTaskRepo`'s recorder field may already exist under another name, and `countingGateBuyer` may already have a hook. Reuse what is there; do not add a parallel recorder.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/application/manufacturing/commands/ -run 'TestFeedGateLeg' 2>&1 | tail -20
```

Expected: FAIL — `h.SetGateFactory undefined` / `h.feedGateLeg undefined`.

- [ ] **Step 3a: Add the collaborator set**

Append to `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go`:

```go
// GateFactoryTopology is the FACTORY role's view of the era's topology: where a good is exported
// (which doubles as the RAW SOURCE role — a raw good is bought from whatever exports it, and the
// two roles differ in the caller's intent, not in the resolution) plus the recipe seam.
// *services.GateTopology satisfies it.
//
// IsRaw/Inputs are GateTopology's, NEVER goods.IsRawMaterial/goods.GetRequiredInputs. The pairs
// diverge in CONTENT since sp-4irrr: Inputs("IRON_ORE") is nil while GetRequiredInputs is
// {"EXPLOSIVES"}, so a swap would descend an ore into
// IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE and stop terminating.
type GateFactoryTopology interface {
	TerminalFactory(ctx context.Context, good, systemSymbol string, playerID int) (*mfgServices.MarketLocatorResult, error)
	IsRaw(good string) bool
	Inputs(good string) []string
}

// GateFeeder delivers a hull's inputs INTO a pinned factory.
// *services.ProductionExecutor satisfies it via FeedFactory.
type GateFeeder interface {
	FeedFactory(ctx context.Context, ship *navigation.Ship, destination *mfgServices.MarketLocatorResult, inputs []string, playerID int, opContext *shared.OperationContext) (*mfgServices.FeedResult, error)
}

// gateFactory is the drain's FACTORY-fleet collaborator set.
//
// It carries its own buyer rather than reaching into gateDelivery's, so the two roles are
// independently wireable and neither's absence nil-panics the other. In production both are the
// SAME *ProductionExecutor — which is the point: one spend primitive, one set of money guards.
type gateFactory struct {
	topology GateFactoryTopology
	buyer    GateBuyer
	feeder   GateFeeder
}

func (g *gateFactory) enabled() bool {
	return g != nil && g.topology != nil && g.buyer != nil && g.feeder != nil
}

// SetGateFactory wires the factory fleet: phase 1's role-based topology (which also answers the
// recipe seam the feed walk needs), phase 2's pinned terminal-factory buy, and the feed terminal.
//
// OPTIONAL, following SetGateDelivery/SetTreeResolver — a nil in any argument leaves the feeding
// leg unwired and a factory-tagged hull keeps taking the shared path, so every existing
// coordinator test is unchanged. This is NOT a feature flag: main.go wires it unconditionally, so
// it ships ARMED. It is the same optional-collaborator pattern the drain already uses to keep its
// own fixtures buildable.
func (h *RunConstructionCoordinatorHandler) SetGateFactory(topology GateFactoryTopology, buyer GateBuyer, feeder GateFeeder) {
	if topology == nil || buyer == nil || feeder == nil {
		return
	}
	h.factory = &gateFactory{topology: topology, buyer: buyer, feeder: feeder}
}
```

and add the field to `RunConstructionCoordinatorHandler` in `run_construction_coordinator.go`, next to `gate`:

```go
	// factory is the FACTORY-fleet collaborator set: the recursive feed walk's topology seam, the
	// shared pinned buy, and the feed terminal. Wired by SetGateFactory; nil leaves the feeding
	// leg off and the drain byte-identical to before.
	factory *gateFactory
```

- [ ] **Step 3b: Write the leg**

Create `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go`:

```go
package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// feedGateLeg runs one FACTORY hull's leg: flush, plan, resolve, buy, feed.
//
// ONE STEP PER LEG, and that is the bound on factory-fleet spend. There is no bill for an INPUT —
// the construction site's bill is denominated in gate materials, not in IRON_ORE — so nothing
// downstream caps how much feedstock a hull could buy. What caps it is: one hull-load per leg
// (BuyAtTerminalFactory stamps the trip allocation as the fill target, bounded by hull capacity),
// the market's per-transaction trade_volume, and the working-capital floor re-checked EVERY
// tranche and failing closed. Nothing here reads, moves or weakens a floor (RULINGS #4).
//
// EVERY exit funnels through the SAME completion machinery the rest of supplyTask uses. A leg
// that simply returns leaves its task EXECUTING forever: nothing re-stages the next load, the
// ready queue drains to nothing, and the drain goes quiet while still reporting RUNNING — a stall
// indistinguishable from a finished gate.
//
// Reports whether the leg advanced this task, which for the factory role means only the flush:
// feeding a factory supplies no units to the construction site.
func (h *RunConstructionCoordinatorHandler) feedGateLeg(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	lot constructionLot,
	playerID shared.PlayerID,
) bool {
	logger := common.LoggerFromContext(ctx)
	task := lot.task

	if !h.factory.enabled() {
		// Unwired: park rather than nil-panic. Reachable only in a partially-built handler.
		return h.completeOrDefer(ctx, &supplyLeg{lot: lot, ship: lot.ship})
	}

	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: cannot read pipeline %s for %s — standing down this leg rather than feeding against an unknown bill: %v", task.PipelineID(), task.Good(), err), nil)
		return h.completeOrDefer(ctx, &supplyLeg{lot: lot, ship: lot.ship})
	}
	leg := &supplyLeg{lot: lot, ship: lot.ship, pipeline: pipeline}

	// FLUSH FIRST. A factory hull can hold a GATE MATERIAL — re-roled between ticks, or a restart
	// mid-leg. A terminal factory will not buy its own export (marketBuys refuses an EXPORT
	// listing), so that cargo would ride forever and the hold would never free. Unload it at the
	// SITE through the same path the delivery leg uses; cargo already aboard has zero market
	// impact and always advances the gate.
	freed := h.flushOnHandGateMaterials(ctx, leg, pipeline, playerID)

	billSource := pipeline
	if leg.pipeline != nil {
		billSource = leg.pipeline
	}

	step, input, target, planned := h.planGateFeed(ctx, cmd, systemSymbol, billSource)
	if !planned {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s found no feedable step this leg — every gate material is either satisfied, already ABUNDANT at its factory, or has no resolvable source and destination", lot.ship.ShipSymbol()), map[string]interface{}{
			"ship": lot.ship.ShipSymbol(), "action": "no_feed_step",
		})
		return h.completeOrDefer(ctx, leg)
	}

	// Size the buy against the free hold, including whatever the flush just released. The cached
	// *Ship is deliberately not updated by DeliverToConstructionSite (it writes the emptied hold
	// back through the repository), so the freed units are added explicitly rather than re-read.
	capacity := freed
	if cargo := lot.ship.Cargo(); cargo != nil {
		capacity += cargo.Capacity - cargo.Units
	}
	if capacity <= 0 {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s has no free hold after the flush — it can buy nothing this leg", lot.ship.ShipSymbol()), map[string]interface{}{"ship": lot.ship.ShipSymbol()})
		return h.completeOrDefer(ctx, leg)
	}

	// THE SAME pinned buy the delivery fleet uses, with every money guard unchanged.
	result, berr := h.factory.buyer.BuyAtTerminalFactory(ctx, lot.ship, step.Input, input, capacity, systemSymbol, cmd.PlayerID, h.operationContext(cmd))
	if berr != nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: buying %s at %s for the %s factory failed: %v", step.Input, input.WaypointSymbol, step.Target, berr), map[string]interface{}{
			"good": step.Input, "target": step.Target, "source": input.WaypointSymbol,
		})
		return h.completeOrDefer(ctx, leg)
	}
	if result == nil || result.QuantityAcquired == 0 {
		// The money or price guards stopped the fill. Fail closed: do NOT fly an empty hull to a
		// factory. Recorded loudly — a refused buy and an idle hull must not look the same.
		logger.Log("WARNING", fmt.Sprintf("Gate factory: acquired nothing of %s at %s — the spend guards stopped the fill, so %s feeds no factory this leg", step.Input, input.WaypointSymbol, lot.ship.ShipSymbol()), map[string]interface{}{
			"good": step.Input, "source": input.WaypointSymbol, "ship": lot.ship.ShipSymbol(), "action": "buy_acquired_nothing",
		})
		return h.completeOrDefer(ctx, leg)
	}

	fed, ferr := h.factory.feeder.FeedFactory(ctx, lot.ship, target, []string{step.Input}, cmd.PlayerID, h.operationContext(cmd))
	if ferr != nil {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: feeding %d %s into the %s factory at %s failed; the cargo stays aboard for the next leg: %v", result.QuantityAcquired, step.Input, step.Target, target.WaypointSymbol, ferr), map[string]interface{}{
			"good": step.Input, "target": step.Target, "factory": target.WaypointSymbol,
		})
		return h.completeOrDefer(ctx, leg)
	}
	if fed != nil && fed.Refused {
		logger.Log("WARNING", fmt.Sprintf("Gate factory: %s refused %s — %s keeps the cargo aboard rather than stranding it there (sp-b27a2)", target.WaypointSymbol, step.Input, lot.ship.ShipSymbol()), map[string]interface{}{
			"good": step.Input, "factory": target.WaypointSymbol, "action": "feed_refused",
		})
		return h.completeOrDefer(ctx, leg)
	}

	units := 0
	if fed != nil {
		units = fed.UnitsDelivered
	}
	logger.Log("INFO", fmt.Sprintf("Gate factory: %s fed %d %s into the %s factory at %s — the delivery fleet buys that factory's OUTPUT, never its inputs", lot.ship.ShipSymbol(), units, step.Input, step.Target, target.WaypointSymbol), map[string]interface{}{
		"ship": lot.ship.ShipSymbol(), "good": step.Input, "units": units,
		"target": step.Target, "factory": target.WaypointSymbol, "depth": step.Depth,
	})

	// The feed supplied no units to the CONSTRUCTION SITE, so leg.delivered reflects the flush
	// alone. completeOrDefer honours it: a leg that flushed COMPLETES, a leg that only fed PARKS
	// for the SupplyMonitor to re-activate. Parking is not a failure — it must never spend a
	// retry against a task the factory role was never going to close.
	if leg.delivered > 0 {
		return h.completeSupply(ctx, leg, leg.delivered)
	}
	return h.completeOrDefer(ctx, leg)
}

// planGateFeed picks THIS leg's single feed step: walk each outstanding gate material neediest
// first, and take the first step whose input source AND destination factory both resolve.
//
// Every declined step is logged with its reason. A walk that declines silently rebuilds the exact
// opacity this design exists to remove — a starved factory and a satisfied one would look the same.
func (h *RunConstructionCoordinatorHandler) planGateFeed(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	systemSymbol string,
	pipeline *manufacturing.ManufacturingPipeline,
) (gate.FeedStep, *mfgServices.MarketLocatorResult, *mfgServices.MarketLocatorResult, bool) {
	logger := common.LoggerFromContext(ctx)

	// Neediest gate material first, deterministic on a tie so a leg's choice is reproducible.
	materials := make([]*manufacturing.ConstructionMaterialTarget, 0, len(pipeline.Materials()))
	for _, target := range pipeline.Materials() {
		if target.RemainingQuantity() <= 0 {
			continue // a met bill needs no feeding
		}
		materials = append(materials, target)
	}
	sort.SliceStable(materials, func(i, j int) bool {
		if materials[i].RemainingQuantity() != materials[j].RemainingQuantity() {
			return materials[i].RemainingQuantity() > materials[j].RemainingQuantity()
		}
		return materials[i].TradeSymbol() < materials[j].TradeSymbol()
	})

	for _, material := range materials {
		plan := gate.PlanFeed(material.TradeSymbol(), h.factory.topology, gate.DefaultFeedDepthCap)
		logger.Log("INFO", plan.LogLine(), map[string]interface{}{
			"root": plan.Root, "steps": len(plan.Steps), "stops": len(plan.Stops),
		})

		for _, step := range plan.Steps {
			target, terr := h.factory.topology.TerminalFactory(ctx, step.Target, systemSymbol, cmd.PlayerID)
			if terr != nil || target == nil {
				logger.Log("WARNING", fmt.Sprintf("Gate factory: no factory in %s exports %s this era, so its %s feed is declined: %v", systemSymbol, step.Target, step.Input, terr), map[string]interface{}{
					"good": step.Input, "target": step.Target, "reason": "no_destination_factory",
				})
				continue
			}
			// THE ABUNDANT FAIL-SAFE. A factory already at the top of the supply ladder does not
			// need feedstock; buying into a full warehouse burns treasury for nothing. Deliberately
			// the ladder's TOP and nothing else — a threshold here would be a knob, and this phase
			// adds none.
			if shared.ParseSupplyLevel(target.Supply) == shared.SupplyLevelAbundant {
				logger.Log("INFO", fmt.Sprintf("Gate factory: the %s factory at %s is already ABUNDANT — declining its %s feed rather than buying into a full warehouse", step.Target, target.WaypointSymbol, step.Input), map[string]interface{}{
					"good": step.Input, "target": step.Target, "factory": target.WaypointSymbol, "reason": "target_abundant",
				})
				continue
			}
			source, serr := h.factory.topology.TerminalFactory(ctx, step.Input, systemSymbol, cmd.PlayerID)
			if serr != nil || source == nil {
				// A refusal, never a substitution: sending a hull to some other waypoint is how
				// cargo ends up somewhere that cannot accept it.
				logger.Log("WARNING", fmt.Sprintf("Gate factory: nothing in %s exports %s, so the %s factory cannot be fed it this leg: %v", systemSymbol, step.Input, step.Target, serr), map[string]interface{}{
					"good": step.Input, "target": step.Target, "reason": "no_input_source",
				})
				continue
			}
			return step, source, target, true
		}
	}
	return gate.FeedStep{}, nil, nil, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/application/manufacturing/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

Expected: `ok` for every package. Then, UNPIPED: `go vet ./internal/application/manufacturing/...`

- [ ] **Step 5: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): the factory role's feeding leg — one step per leg, inputs only (phase 3)

feedGateLeg walks the outstanding gate materials neediest-first, takes the first
step whose source and destination both resolve, buys a hull-load of that INPUT
through the UNCHANGED BuyAtTerminalFactory, and feeds it into the factory that
needs it. It never touches terminal output -- that boundary IS the two-fleet
design.

ONE STEP PER LEG is the bound on factory-fleet spend: there is no bill for an
input, so nothing downstream caps feedstock. What caps it is one hull-load per
leg, the market's trade_volume, and the per-tranche working-capital floor that
fails closed. No floor is read, moved or weakened (RULINGS #4).

An ABUNDANT destination is skipped -- a full factory needs no feedstock, and
buying into it burns treasury for nothing. Deliberately the ladder's TOP and
nothing else: a threshold would be a knob, and this phase adds none.

The flush is not optional: a terminal factory will not buy its own export, so a
factory hull holding a gate material would ride with it forever. It unloads at
the SITE first, through phase 2's path.

Every exit funnels through the shared completion machinery. A leg that simply
returned would leave its task EXECUTING forever and the drain would report
RUNNING while the ready queue silently drained." -- gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go gobot/internal/application/manufacturing/commands/run_construction_coordinator.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery_test.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_test.go
```

- [ ] **Step 6a: Mutation-probe the ABUNDANT fail-safe**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|if shared.ParseSupplyLevel(target.Supply) == shared.SupplyLevelAbundant {|if false \&\& shared.ParseSupplyLevel(target.Supply) == shared.SupplyLevelAbundant {|' internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && grep -q 'if false && shared.ParseSupplyLevel' internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestFeedGateLeg' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestFeedGateLeg_SkipsAFactoryWhoseOutputIsAlreadyAbundant`, then `RESTORED`.

- [ ] **Step 6b: Mutation-probe the fail-closed empty-buy guard**

Prove the leg honours a money-guard refusal instead of flying an empty hull. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|if result == nil \|\| result.QuantityAcquired == 0 {|if result == nil {|' internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && ! grep -q 'result.QuantityAcquired == 0' internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestFeedGateLeg' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestFeedGateLeg_DoesNotFeedWhenTheBuyAcquiredNothing`, then `RESTORED`.

- [ ] **Step 6c: Mutation-probe the FLUSH**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|freed := h.flushOnHandGateMaterials(ctx, leg, pipeline, playerID)|freed := 0 // MUTANT|' internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && grep -q 'freed := 0 // MUTANT' internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestFeedGateLeg' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_feed.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestFeedGateLeg_UnloadsAnyGateMaterialAboardAtTheSiteBeforePlanning`, then `RESTORED`.

---

### Task 5: `gateLegRole` — ONE shared predicate, and the pin that observes WHICH LEG RAN

`runsGateLeg` returns a boolean that is false for `RoleFactory`. Phase 3 needs the factory role routed somewhere new, and the naive widening — make it return true for both tags — **would be a fleet-killer**, in the opposite direction from phase 2's D2.

`dispatchableByHold` declines a hull when `runsGateLeg(...) && wedgedAtFullHold(...)`. `wedgedAtFullHold` means "the hold is full and nothing aboard is a material whose bill is still open". A factory hull's hold is full of **IRON_ORE** — never a bill material — so the predicate is TRUE for **every laden factory hull**. Widening the boolean would decline the entire factory fleet every tick, forever, and the feeding leg written in Task 4 would never run once. Phase 2's own comment says why the scoping exists: *"THE CALLER SCOPES THIS TO THE DELIVERY ROLE, and that scoping is load-bearing."*

So the shared function changes **shape, not just breadth**: it returns the **role**, and each call site asks its own question off that one answer.

- Routing asks *which leg* — and now has two.
- The decline asks *is this the delivery role* — and stays exactly as narrow as it was.

That keeps constraint 5's guarantee structural: there is still ONE function, so a phase-4 widening touches one place; and the decline can no longer become broader than the routing by accident, because the thing they share is the role, not a yes/no.

**Constraint 6 is discharged here.** The routing half has no behavioural pin today — `supplyTask` claims the task upstream of the branch, and the pinning test asserts `claimCount == 1`, which cannot observe which leg ran. This task adds a test that drives `supplyTask` itself and asserts on the **terminal the leg used**: the delivery leg unloads at the construction SITE, the feeding leg calls `FeedFactory`. Those are different observable side effects at different driven ports, so the test fails if routing sends a lot to the wrong leg.

**Files:**
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go` (`runsGateLeg` → `gateLegRole`)
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_supply.go` (routing)
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go` (decline)
- Test: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go` (append)

**Interfaces:**
- Consumes: `gate.ParseFleetTag`.
- Produces: `(*RunConstructionCoordinatorHandler) gateLegRole(claimIdentity string) (gate.Role, bool)`. **`runsGateLeg` is removed** — a wrapper would let the two questions drift again, which is the whole hazard.

- [ ] **Step 1: Write the failing test**

Append to `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go`:

```go
// gateFactoryReadyLot is a lot whose task is READY and whose claim identity is the FACTORY tag —
// the state supplyTask itself expects.
func gateFactoryReadyLot(t *testing.T, ship *navigation.Ship) constructionLot {
	t.Helper()
	lot := gateReadyLot(t, ship)
	lot.claimIdentity = gate.FactoryFleetTag
	return lot
}

// ---------------------------------------------------------------------------------------------
// THE ROUTING PIN — the test phase 2 could not write
// ---------------------------------------------------------------------------------------------
//
// The routing half of the shared predicate had NO behavioural pin: supplyTask claims the task
// UPSTREAM of the branch, and the pinning test asserts claimCount == 1, which is structurally
// incapable of observing which leg ran. Phase 3 widens routing, so it must be pinned by an
// observable that DIFFERS between the two legs.
//
// The observable is the driven port each leg terminates at: the delivery leg unloads at the
// construction SITE (DeliverToConstructionSite), the feeding leg calls FeedFactory. Neither is
// reachable from the other, so a mis-routed lot cannot pass both.

func TestSupplyTask_AFactoryTaggedLotRunsTheFEEDINGLegNotTheDeliveryLeg(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestHull(t, "GF-1", gate.FactoryFleetTag)

	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, gateFactoryReadyLot(t, ship), shared.MustNewPlayerID(1))

	if len(f.feeder.feeds()) == 0 {
		t.Fatalf("a factory-tagged lot fed no factory — it was routed to the DELIVERY leg (or to the shared legacy path), and the whole factory role is inert.\nlog:\n%s", f.logLines())
	}
	if f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a factory-tagged lot delivered %s to the construction site; the factory role feeds INPUTS and never touches terminal output", gateMaterialPrimary)
	}
}

func TestSupplyTask_ADeliveryTaggedLotStillRunsTheDELIVERYLeg(t *testing.T) {
	f := newGateFactoryHandler(t) // BOTH roles wired, so a mis-route has somewhere to go
	ship := gateTestHull(t, "GD-1", gate.DeliveryFleetTag)

	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, gateReadyLot(t, ship), shared.MustNewPlayerID(1))

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a delivery-tagged lot never supplied %s to the site — routing sent it to the feeding leg.\nlog:\n%s", gateMaterialPrimary, f.logLines())
	}
	if len(f.feeder.feeds()) != 0 {
		t.Fatalf("a delivery-tagged lot fed %+v; the delivery role buys terminal OUTPUT and never feeds", f.feeder.feeds())
	}
}

// An UNDEDICATED / legacy lot takes NEITHER gate leg. It runs the shared source-then-deliver path
// exactly as before, which is what keeps every pre-existing coordinator test valid.
func TestSupplyTask_ALegacyLotTakesNeitherGateLeg(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestHull(t, "GL-1", gate.LegacyFleetTag)
	lot := gateReadyLot(t, ship)
	lot.claimIdentity = gate.LegacyFleetTag

	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if len(f.feeder.feeds()) != 0 {
		t.Fatalf("a legacy lot fed %+v; it carries no role and must take the shared path", f.feeder.feeds())
	}
	if f.buyer.calls() != 0 {
		t.Fatalf("a legacy lot made %d pinned terminal-factory buy(s); that is a gate-leg operation", f.buyer.calls())
	}
	// The shared path is the producer's ProduceGood, not either gate leg's terminal.
	if len(f.producer.produceGoods) == 0 {
		t.Fatalf("a legacy lot reached neither gate leg NOR the shared source path — it did nothing at all.\nlog:\n%s", f.logLines())
	}
}

// ---------------------------------------------------------------------------------------------
// THE DECLINE MUST STAY SCOPED TO THE DELIVERY ROLE
// ---------------------------------------------------------------------------------------------

// THE FLEET-KILLER THIS TASK EXISTS TO PREVENT. wedgedAtFullHold means "the hold is full and
// nothing aboard is a material whose bill is still open" — and a factory hull's hold is full of
// IRON_ORE, which is NEVER a bill material. So the predicate is TRUE for every laden factory
// hull. Widening the shared routing condition to a boolean covering both roles would decline the
// entire factory fleet every tick, forever, and the feeding leg would never run once.
//
// The feeding leg recovers exactly this hull by a route the predicate cannot see: FeedFactory
// sells the inputs into the factory's import listing, which frees the hold.
func TestConstructionDrain_StillDispatchesAFactoryHullFullOfFabricationInputs(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	inputs, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-5", []*shared.CargoItem{inputs}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.FactoryFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("a FACTORY hull full of fabrication inputs was claimed %d time(s), want 1 — its leg SELLS those inputs into a factory, so declining it makes the whole factory fleet permanently invisible", got)
	}
}

// The falsifier for the test above: the DELIVERY-role decline must still fire. Without this, a
// decline that never fires would pass the factory test and silently reintroduce the tick-forever
// wedge phase 2 closed.
func TestConstructionDrain_StillDeclinesAWedgedDELIVERYHullAfterTheRoleSplit(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialPrimary, 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialSecondary, 200)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	if err := pipeline.RecordMaterialDelivery(gateMaterialPrimary, 40); err != nil {
		t.Fatalf("RecordMaterialDelivery: %v", err)
	}
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	stranded, err := shared.NewCargoItem(gateMaterialPrimary, gateMaterialPrimary, "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-9", []*shared.CargoItem{stranded})
	hull.SetDedicatedFleet(gate.DeliveryFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 0 {
		t.Fatalf("a wedged DELIVERY hull was claimed %d time(s) after the role split; the decline stopped firing and the tick-forever wedge is back", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/application/manufacturing/commands/ -run 'TestSupplyTask_|TestConstructionDrain_StillDispatchesAFactoryHull|TestConstructionDrain_StillDeclinesAWedgedDELIVERY' 2>&1 | tail -20
```

Expected: FAIL — `TestSupplyTask_AFactoryTaggedLotRunsTheFEEDINGLegNotTheDeliveryLeg` fails on "fed no factory" (routing still returns false for `RoleFactory`), and `TestConstructionDrain_StillDispatchesAFactoryHullFullOfFabricationInputs` currently PASSES (the decline is false for factory today) — note that in the handback: it becomes a regression guard the moment routing widens.

- [ ] **Step 3a: Replace the predicate**

In `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go`, replace `runsGateLeg` entirely:

```go
// gateLegRole is THE routing predicate: which gate leg (if any) will a lot with this claim
// identity actually run?
//
// It exists as ONE function because it has two callers that must never disagree — supplyTask,
// which routes to a leg, and the dispatch planner, which declines a hull on the strength of what
// that leg can do. When those drifted apart the result was D2: the decline was made about a leg
// the hull was not going to run, and a recoverable hull became permanently invisible.
//
// IT RETURNS THE ROLE, NOT A BOOLEAN, and that shape is load-bearing. The two callers ask
// DIFFERENT questions of the same fact:
//
//   - routing asks WHICH leg, and there are now two;
//   - the decline asks IS THIS THE DELIVERY ROLE, because wedgedAtFullHold ("full hold, nothing
//     aboard is a material whose bill is still open") is only sound for the delivery leg, whose
//     entire repertoire is flush-then-buy.
//
// A boolean widened to cover both roles would decline EVERY laden factory hull — their holds are
// full of IRON_ORE, which is never a bill material — and the factory fleet would never run once.
// Returning the role lets the decline stay exactly as narrow as it was while routing widens, with
// one function still shared, so a phase-4 widening touches one place.
//
// Each role's leg is gated on ITS OWN collaborators. With a role's leg unwired, a hull carrying
// that tag takes the shared fabricate path and recovers there like any other, so the decline is
// never made about a leg that is not going to run. That is the optional-collaborator pattern the
// drain already uses, NOT a feature flag: main.go wires both unconditionally.
//
// It takes the resolved IDENTITY rather than the hull deliberately. The dispatch planner resolves
// it from the live hull (claimIdentityFor) because that is where the lot's identity is minted; the
// worker must use the lot's FROZEN claimIdentity, because it runs long after the planning tick and
// claims under that exact value. Re-deriving from the hull inside the worker would let routing
// disagree with what was actually claimed — the hazard constructionLot.claimIdentity exists to
// prevent, and the same hazard the reallocator's busy guard defends from the other side.
func (h *RunConstructionCoordinatorHandler) gateLegRole(claimIdentity string) (gate.Role, bool) {
	role, ok := gate.ParseFleetTag(claimIdentity)
	if !ok {
		return 0, false // legacy, foreign, or undedicated: no role, no gate leg
	}
	if role == gate.RoleFactory {
		return role, h.factory.enabled()
	}
	return role, h.gate.enabled()
}
```

- [ ] **Step 3b: Route on the role**

In `gobot/internal/application/manufacturing/commands/run_construction_coordinator_supply.go`, replace the gate branch inside `supplyTask`:

```go
	// A ROLE-TAGGED gate hull takes its role's own leg rather than the shared source-then-deliver
	// path. A DELIVERY hull buys terminal-factory output and hauls it to the gate; a FACTORY hull
	// feeds INPUTS into the factories that export those materials and never touches their output.
	// The condition lives in gateLegRole because the dispatch planner declines hulls on the
	// strength of what these legs can do and must never disagree with this routing.
	if role, ok := h.gateLegRole(lot.claimIdentity); ok {
		if role == gate.RoleFactory {
			return h.feedGateLeg(ctx, cmd, systemSymbol, lot, playerID)
		}
		return h.deliverGateLeg(ctx, cmd, systemSymbol, lot, playerID)
	}
```

Add `"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"` to that file's imports.

- [ ] **Step 3c: Keep the decline scoped to the delivery role**

In `gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go`, inside `dispatchableByHold`, replace the condition:

```go
		// THE SAME FUNCTION supplyTask routes on — but the decline asks only about the DELIVERY
		// role. wedgedAtFullHold ("full hold, nothing aboard is a material whose bill is still
		// open") is sound only for the delivery leg, whose repertoire is flush-then-buy.
		//
		// A FACTORY hull's hold is full of fabrication INPUTS, which are never bill materials, so
		// the predicate is true for every laden one — declining on it would make the entire
		// factory fleet permanently invisible. Its leg recovers that hull by a route the predicate
		// cannot see: FeedFactory SELLS the inputs into the factory's import listing.
		//
		// Sharing gateLegRole keeps this in lockstep with the routing structurally, while letting
		// the two ask their own question of the same answer.
		role, runsGateLeg := h.gateLegRole(h.claimIdentityFor(cmd, ship))
		if !runsGateLeg || role != gate.RoleDelivery || !wedgedAtFullHold(ship, budget) {
			usable = append(usable, ship)
			continue
		}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/application/manufacturing/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

Expected: `ok` for every package. Then, UNPIPED: `go build ./...` and `go vet ./...` — `runsGateLeg` was removed, so any stray caller is a compile error, and `go vet` is what catches one that lives only in a `_test.go`.

- [ ] **Step 5: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): route on the ROLE — one shared predicate, two questions (phase 3)

runsGateLeg becomes gateLegRole and returns the ROLE, not a boolean. That shape
is load-bearing: the two callers ask different questions of the same fact.
Routing asks WHICH leg (there are now two); the decline asks IS THIS THE
DELIVERY ROLE, because wedgedAtFullHold is only sound for a leg whose entire
repertoire is flush-then-buy.

Widening the BOOLEAN would have been a fleet-killer in the opposite direction
from D2: a factory hull's hold is full of IRON_ORE, never a bill material, so
the predicate is TRUE for every laden factory hull. The decline would have
dropped the whole factory fleet every tick and the feeding leg would never have
run once.

One function still shared, so a phase-4 widening touches one place -- but the
decline can no longer become broader than the routing by accident, because what
they share is the role rather than a yes/no.

PINS THE ROUTING HALF, which had no behavioural pin: supplyTask claims upstream
of the branch, so claimCount == 1 cannot observe which leg ran. The new tests
assert on the DRIVEN PORT each leg terminates at -- DeliverToConstructionSite
for delivery, FeedFactory for the factory role -- which no mis-routed lot can
satisfy both of." -- gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_delivery.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_supply.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go
```

- [ ] **Step 6a: Mutation-probe the ROUTING half (the pin constraint 6 demands)**

Send factory lots to the delivery leg — the exact divergence that killed no test before this task — and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\tif role == gate\.RoleFactory \{\n\t\t\treturn h\.feedGateLeg\(ctx, cmd, systemSymbol, lot, playerID\)\n\t\t\}\n//' internal/application/manufacturing/commands/run_construction_coordinator_supply.go && ! grep -q 'h.feedGateLeg' internal/application/manufacturing/commands/run_construction_coordinator_supply.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestSupplyTask_' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_supply.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestSupplyTask_AFactoryTaggedLotRunsTheFEEDINGLegNotTheDeliveryLeg`, then `RESTORED`. **This is the probe that discharges constraint 6.** If it names zero tests, the routing is still unpinned — stop and fix the test, do not proceed.

- [ ] **Step 6b: Mutation-probe the DELIVERY-ONLY scoping of the decline**

Widen the decline to both roles — the fleet-killer — and confirm a NAMED test dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|if !runsGateLeg \|\| role != gate.RoleDelivery \|\| !wedgedAtFullHold(ship, budget) {|if !runsGateLeg \|\| !wedgedAtFullHold(ship, budget) {|' internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && ! grep -q 'role != gate.RoleDelivery' internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestConstructionDrain_StillDispatches' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestConstructionDrain_StillDispatchesAFactoryHullFullOfFabricationInputs`, then `RESTORED`. (`role` becomes unused, so if the compiler rejects it, add `_ = role` in the same invocation — a `[build failed]` here names zero tests and is an infra failure, not a kill.)

- [ ] **Step 6c: Mutation-probe that the decline still FIRES for delivery**

The mirror probe: neuter the decline entirely and confirm the phase-2 wedge guard dies. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|role, runsGateLeg := h.gateLegRole(h.claimIdentityFor(cmd, ship))|role, runsGateLeg := gate.Role(0), false // MUTANT|' internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && grep -q 'MUTANT' internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestConstructionDrain_' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_dispatch.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestConstructionDrain_DoesNotDispatchAHullWedgedAtAFullHold` and `--- FAIL: TestConstructionDrain_StillDeclinesAWedgedDELIVERYHullAfterTheRoleSplit`, then `RESTORED`.

---

### Task 6: `reallocateGateRoles` — the tick hook, the legacy adoption, and the claim lifecycle

This is where the two open questions the spec left to phase 3 are answered. Both answers are stated here and implemented below.

#### Decision 1 — legacy `manufacturing` hulls ARE re-roled

**Yes, and this is the arming fix.** Phase 2 deliberately left them alone (its adjudication #4) and deferred the call. The consequence today is not a matter of taste:

`bootstrap_ports_observe.go:116` counts **all three** gate tags into `obs.GateWorkers` — correctly, so the ramp cannot over-buy. `planGateWorkers` (`run_bootstrap_gate.go:73,79`) sets `desired := gateWorkerTarget` (**4**) and buys only while `desired > obs.GateWorkers`. So on a fleet already holding four legacy hulls, `4 > 4` is false: **the ramp buys nothing, `nextGateRole` never runs, no role tag is ever written, `gate.RoleFleetTags()` discovery finds two empty pools, and phases 1 through 3 are entirely inert on the very fleet they were built for.** That is a shipped-and-dead feature, not a deferred nicety.

Re-roling dissolves it for free: an `AssignFleet` write per hull, no purchase, RULINGS #4 untouched.

The alternative — narrow `obs.GateWorkers` to role-tagged hulls so the ramp buys four more — was rejected: it spends four hull prices to avoid a free re-tag and leaves eight gate hulls where the design wants four.

Scope is deliberately narrow: **only the exact `gate.LegacyFleetTag` value is adopted.** A drain launched with a custom `DedicatedFleet` (`cmd.DedicatedFleet`, e.g. `"gate-alt"`) carries a tag that `IsGateFleetTag` does not recognise, so its hulls are outside the reallocator's census entirely and are never touched. A contract or trade hull is likewise not a gate hull; re-tagging one would be a poach (RULINGS #7).

#### Decision 2 — reallocation WAITS for a natural release; it never moves a claim

**Wait.** Three reasons, in order of weight:

1. `AssignFleet`'s own contract says so: *"Deliberately does NOT reject a claimed or captain-reserved hull: dedication is permanent ownership ('who may claim this next'), orthogonal to current occupancy — the tag takes effect when the present claim is released, it does not evict the holder."* Moving a claim would mean fighting that design rather than using it.
2. The claim lifecycle makes yanking actively unsafe. `constructionLot.claimIdentity` is **frozen at plan time**; a worker claims under that exact frozen value long after the planning tick. A hull re-tagged in that window presents a stale tag, and `ClaimShip` — which authorizes a NEW claim only when `tag == operation` — rejects it with `ShipDedicatedToOtherFleetError`. The hull is dispatched and then silently never works. The busy guard makes that window unreachable rather than merely narrow: a hull the registry holds is not idle, and `supplies.admit` registers **before** the worker goroutine starts.
3. Responsiveness costs nothing. The dwell is 10 minutes and a leg is bounded by its task deadline, so a hull returns to idle long before the dwell would release it anyway. Moving claims would buy latency the dwell immediately spends.

The spec's "a hull mid-haul must finish its leg before reassignment" is therefore satisfied **structurally** — by only ever considering idle, unheld hulls — rather than by a wait-loop.

#### Placement

The hook runs after `reconcilePipelinesFromSite` (so the bill is current) and **before** `selectHaulers` (so a re-tag is visible to this tick's own discovery — `AssignFleet` invalidates the ship-list cache, and `claimIdentityFor` reads the live tag).

The pause it reacts to is the state phase 2's legs already wrote: `BuyPolicy.FleetPaused` over the **outstanding** gate materials. Filtering to outstanding is load-bearing — a material whose bill is MET is never decided on, so its pause state stays false and an unfiltered `FleetPaused` would read `false` forever the moment one material completes, exactly when the other is most likely starved.

**Files:**
- Create: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go`
- Modify: `gobot/internal/application/manufacturing/commands/run_construction_coordinator.go` (handler fields + the tick call)
- Test: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc_test.go`

**Interfaces:**
- Consumes: `gate.PlanReallocation`, `gate.Worker`, `(*gate.BuyPolicy).FleetPaused`, `navigation.ShipRepository.FindAllByPlayer`, `navigation.ShipRepository.AssignFleet`, `supplyWorkers.holds`.
- Produces: `(*RunConstructionCoordinatorHandler) reallocateGateRoles(ctx context.Context, cmd *RunConstructionCoordinatorCommand, tasks []*manufacturing.ManufacturingTask, playerID shared.PlayerID)`; handler fields `roleMu sync.Mutex`, `roleSince map[string]time.Time`.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc_test.go`:

```go
package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ROLE REALLOCATION. Two decisions this task settles and both are load-bearing:
//
//  1. LEGACY HULLS ARE ADOPTED. On a fleet already holding four "manufacturing" hulls the
//     purchase ramp buys nothing (obs.GateWorkers counts all three tags and already equals
//     gateWorkerTarget), so no role tag is ever written and phases 1-3 are entirely inert.
//     Adopting them costs an AssignFleet write and no purchase.
//
//  2. CLAIMS ARE NEVER MOVED. AssignFleet does not evict a holder by design, and
//     constructionLot.claimIdentity is FROZEN at plan time — so a hull re-tagged in flight
//     presents a stale tag and ClaimShip rejects it. Only idle, unheld hulls are moved.

// gateReallocFixture wires both roles and seeds a live gate fleet.
type gateReallocFixture struct {
	*gateFeedFixture
	ships []*navigation.Ship
}

func newGateReallocHandler(t *testing.T, tagged map[string]string) *gateReallocFixture {
	t.Helper()
	f := newGateFactoryHandler(t)
	ships := make([]*navigation.Ship, 0, len(tagged))
	for symbol, tag := range tagged {
		ships = append(ships, gateTestHull(t, symbol, tag))
	}
	f.shipRepo.mu.Lock()
	f.shipRepo.ships = ships
	f.shipRepo.mu.Unlock()
	return &gateReallocFixture{gateFeedFixture: f, ships: ships}
}

// pauseEveryMaterial drives the SAME policy object the delivery legs write to, so the trigger
// under test is the state production actually produces rather than a fabricated flag.
func (f *gateReallocFixture) pauseEveryMaterial() {
	policy := f.handler.gate.policyFor("", "")
	for _, good := range []string{gateMaterialPrimary, gateMaterialSecondary} {
		policy.Decide(good, good+"-EXPORTER", shared.SupplyLevelScarce)
	}
}

func (f *gateReallocFixture) reallocate(t *testing.T) {
	t.Helper()
	task := manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)
	f.handler.reallocateGateRoles(f.ctx(), gateTestCmd(), []*manufacturing.ManufacturingTask{task}, shared.MustNewPlayerID(1))
}

// assignments is every AssignFleet write the reallocator made, in order.
func (f *gateReallocFixture) assignments() []fleetAssignment {
	f.shipRepo.mu.Lock()
	defer f.shipRepo.mu.Unlock()
	return append([]fleetAssignment(nil), f.shipRepo.assigned...)
}

// THE TRIGGER. Delivery hulls move to the factory role only when EVERY gate material is paused.
func TestReallocateGateRoles_MovesDeliveryHullsToFactoryWhenEveryMaterialIsPaused(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"D-1": gate.DeliveryFleetTag, "D-2": gate.DeliveryFleetTag,
		"F-1": gate.FactoryFleetTag, "F-2": gate.FactoryFleetTag,
	})
	f.pauseEveryMaterial()

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("a fully paused delivery fleet moved nobody.\nlog:\n%s", f.logLines())
	}
	for _, w := range writes {
		if w.fleet != gate.FactoryFleetTag {
			t.Fatalf("wrote fleet %q for %s; a paused fleet moves TOWARD the factory role", w.fleet, w.ship)
		}
		if !strings.HasPrefix(w.ship, "D-") {
			t.Fatalf("re-tagged %s, which is already a factory hull", w.ship)
		}
	}
}

// EVERY, NOT ANY. One material still buyable leaves delivery real work — a hull fills greedily
// from whatever is eligible — so moving workers then would starve capacity delivery can use.
func TestReallocateGateRoles_DoesNotMoveWhenOnlyOneMaterialIsPaused(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"D-1": gate.DeliveryFleetTag, "D-2": gate.DeliveryFleetTag,
		"F-1": gate.FactoryFleetTag, "F-2": gate.FactoryFleetTag,
	})
	policy := f.handler.gate.policyFor("", "")
	policy.Decide(gateMaterialPrimary, gateMaterialPrimary+"-EXPORTER", shared.SupplyLevelScarce)
	policy.Decide(gateMaterialSecondary, gateMaterialSecondary+"-EXPORTER", shared.SupplyLevelHigh)

	f.reallocate(t)

	if writes := f.assignments(); len(writes) != 0 {
		t.Fatalf("moved %+v with ONE material still buyable; that starves delivery of capacity it can still use", writes)
	}
}

// A MET BILL IS NOT A PAUSE. A completed material is never decided on, so its pause state stays
// false forever — an unfiltered FleetPaused would then read false exactly when the OTHER material
// is starved, which is when reallocation matters most.
func TestReallocateGateRoles_IgnoresMaterialsWhoseBillIsAlreadyMet(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"D-1": gate.DeliveryFleetTag, "F-1": gate.FactoryFleetTag,
	})
	f.pipeline.setBill(gateMaterialSecondary, 0) // its bill is closed; no leg will ever decide on it
	f.handler.gate.policyFor("", "").Decide(gateMaterialPrimary, gateMaterialPrimary+"-EXPORTER", shared.SupplyLevelScarce)

	f.reallocate(t)

	if writes := f.assignments(); len(writes) == 0 {
		t.Fatalf("a fleet whose ONLY outstanding material is paused moved nobody — a met bill is being counted as an un-paused material.\nlog:\n%s", f.logLines())
	}
}

// THE ARMING FIX. Four legacy hulls, no purchase possible, no role tag in existence. The
// reallocator must adopt them, or every phase of this design is dead code on this fleet.
func TestReallocateGateRoles_AdoptsLegacyManufacturingHulls(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"L-1": gate.LegacyFleetTag, "L-2": gate.LegacyFleetTag,
		"L-3": gate.LegacyFleetTag, "L-4": gate.LegacyFleetTag,
	})

	f.reallocate(t)

	writes := f.assignments()
	if len(writes) == 0 {
		t.Fatalf("no legacy hull was adopted; with four of them the ramp buys nothing (GateWorkers == gateWorkerTarget), so no role tag is EVER written and the whole feature is inert.\nlog:\n%s", f.logLines())
	}
	if got, want := writes[0].fleet, gate.DeliveryFleetTag; got != want {
		t.Fatalf("the first adoption wrote %q, want %q — the D/F/F/D order puts delivery first so accumulated stock starts moving immediately", got, want)
	}
}

// NARROW BY DESIGN. A drain launched with its own dedicated fleet, and every foreign fleet, are
// outside the census entirely. Re-tagging one would be a poach (RULINGS #7).
func TestReallocateGateRoles_NeverTouchesAForeignOrCustomFleetTag(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"X-1": "contract", "X-2": "trade", "X-3": "gate-alt", "X-4": "",
	})
	f.pauseEveryMaterial()

	f.reallocate(t)

	if writes := f.assignments(); len(writes) != 0 {
		t.Fatalf("re-tagged %+v; only the two role tags and the legacy one are gate hulls", writes)
	}
}

// CLAIMS ARE NEVER MOVED. A hull a supply worker still holds is not idle, and re-tagging it would
// make its lot's FROZEN claim identity stale — ClaimShip would then reject the claim and the hull
// would be dispatched and silently never work.
func TestReallocateGateRoles_NeverMovesAHullASupplyWorkerStillHolds(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"D-1": gate.DeliveryFleetTag, "D-2": gate.DeliveryFleetTag,
	})
	f.pauseEveryMaterial()
	// Register D-1 as held, exactly as dispatchSupplyWorkers does before starting a worker.
	if _, admitted := f.handler.supplies.admit("D-1", "C-1", gateMaterialPrimary, 40, f.handler.clock.Now().Add(time.Hour)); !admitted {
		t.Fatal("fixture: could not register the in-flight worker")
	}

	f.reallocate(t)

	for _, w := range f.assignments() {
		if w.ship == "D-1" {
			t.Fatal("re-tagged a hull a live supply worker still holds; its lot's frozen claim identity would then be rejected at the DB")
		}
	}
}

// A hull IN TRANSIT is likewise never moved, even with no worker registered — a restart can leave
// a flying hull with no registration behind it.
func TestReallocateGateRoles_NeverMovesAHullInTransit(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{"D-1": gate.DeliveryFleetTag, "D-2": gate.DeliveryFleetTag})
	f.pauseEveryMaterial()
	for _, ship := range f.ships {
		if ship.ShipSymbol() == "D-1" {
			ship.SetNavStatus(navigation.NavStatusInTransit)
		}
	}

	f.reallocate(t)

	for _, w := range f.assignments() {
		if w.ship == "D-1" {
			t.Fatal("re-tagged a hull in transit — the house rule is never to yank a hull mid-delivery")
		}
	}
}

// THE DWELL, end to end. A hull the reallocator JUST moved must not be moved back on the very
// next tick when the pause flips.
func TestReallocateGateRoles_HonoursTheDwellAcrossTicks(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"D-1": gate.DeliveryFleetTag, "D-2": gate.DeliveryFleetTag,
		"F-1": gate.FactoryFleetTag, "F-2": gate.FactoryFleetTag,
	})
	f.pauseEveryMaterial()

	f.reallocate(t)
	moved := f.assignments()
	if len(moved) == 0 {
		t.Fatal("fixture is inert: nothing moved on the first tick, so the dwell cannot be observed")
	}
	// Apply the write to the live fleet, as the repository would, then UNPAUSE and re-run.
	for _, ship := range f.ships {
		for _, w := range moved {
			if ship.ShipSymbol() == w.ship {
				ship.SetDedicatedFleet(w.fleet)
			}
		}
	}
	f.handler.gate.policyFor("", "").Decide(gateMaterialPrimary, gateMaterialPrimary+"-EXPORTER", shared.SupplyLevelAbundant)
	f.handler.gate.policyFor("", "").Decide(gateMaterialSecondary, gateMaterialSecondary+"-EXPORTER", shared.SupplyLevelAbundant)

	before := len(f.assignments())
	f.reallocate(t)
	for _, w := range f.assignments()[before:] {
		if w.ship == moved[0].ship {
			t.Fatalf("%s was moved again immediately after its first move; the dwell is the workforce-level hysteresis and it is not being applied", w.ship)
		}
	}
}

// A FAILED WRITE STOPS EARLY and leaves a partial result. A hull left on its old tag is safe —
// it is simply retried a later tick — but continuing past an error would keep writing against a
// repository that just said no.
func TestReallocateGateRoles_StopsOnTheFirstFailedAssignAndSaysSo(t *testing.T) {
	f := newGateReallocHandler(t, map[string]string{
		"L-1": gate.LegacyFleetTag, "L-2": gate.LegacyFleetTag,
	})
	f.shipRepo.assignErr = errors.New("row lock timeout")

	f.reallocate(t)

	if !strings.Contains(f.logLines(), "row lock timeout") {
		t.Fatalf("a failed role write is invisible in the log:\n%s", f.logLines())
	}
}

// UNWIRED: the reallocator is a no-op, never a nil panic. Moving hulls into a role whose leg is
// not wired would strand them on a path that does nothing.
func TestReallocateGateRoles_IsANoOpWhenEitherRoleIsUnwired(t *testing.T) {
	base := newGateDeliveryHandler(t) // SetGateFactory deliberately NOT called
	base.shipRepo.mu.Lock()
	base.shipRepo.ships = []*navigation.Ship{gateTestHull(t, "L-1", gate.LegacyFleetTag)}
	base.shipRepo.mu.Unlock()

	task := manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)
	base.handler.reallocateGateRoles(base.ctx(), gateTestCmd(), []*manufacturing.ManufacturingTask{task}, shared.MustNewPlayerID(1))

	base.shipRepo.mu.Lock()
	defer base.shipRepo.mu.Unlock()
	if len(base.shipRepo.assigned) != 0 {
		t.Fatalf("re-tagged %+v while the factory leg is unwired; those hulls would be stranded on a role that does nothing", base.shipRepo.assigned)
	}
}

// THE TICK HOOK IS WIRED. A reallocation that exists but is never called is the same as one that
// does not exist — drive the real drain and assert the write happened.
func TestConstructionDrain_ReallocatesRolesDuringItsTick(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	hull := gateTestHull(t, "L-1", gate.LegacyFleetTag)
	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	handler.SetGateFactory(newStubFactoryTopology(), &countingGateBuyer{}, &recordingFeeder{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	shipRepo.mu.Lock()
	defer shipRepo.mu.Unlock()
	if len(shipRepo.assigned) == 0 {
		t.Fatal("a drain tick adopted no legacy hull — reallocateGateRoles exists but is never called, which is indistinguishable from not existing")
	}
}
```

Add the `AssignFleet` recorder to `drainFakeShipRepo` in `run_construction_coordinator_test.go` (it currently embeds the interface, so an un-stubbed call panics):

```go
// fleetAssignment is one AssignFleet write — the SINGLE write path for dedicated_fleet
// (RULINGS #3). Recording it at the port boundary is how a test proves the reallocator writes
// through it and not through a general ship save, which preserveDedicatedFleetTag would revert.
type fleetAssignment struct{ ship, fleet string }

func (r *drainFakeShipRepo) AssignFleet(_ context.Context, shipSymbol, fleet string, _ shared.PlayerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.assignErr != nil {
		return r.assignErr
	}
	r.assigned = append(r.assigned, fleetAssignment{ship: shipSymbol, fleet: fleet})
	return nil
}
```

with fields `assigned []fleetAssignment` and `assignErr error` added to the struct. Also import `time` in the realloc test file.

> **READ `run_construction_coordinator_test.go` before adding these.** If a fleet recorder already exists, reuse it.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/application/manufacturing/commands/ -run 'TestReallocateGateRoles|TestConstructionDrain_ReallocatesRoles' 2>&1 | tail -20
```

Expected: FAIL — `h.reallocateGateRoles undefined`.

- [ ] **Step 3a: Add the dwell ledger to the handler**

In `gobot/internal/application/manufacturing/commands/run_construction_coordinator.go`, add next to the `factory` field:

```go
	// roleMu guards roleSince, the reallocator's DWELL LEDGER: hull -> when this process last
	// changed its role. It is deliberately IN-MEMORY and per-process, following the pause state's
	// precedent: a restart re-derives, an unrecorded hull is immediately eligible, and the worst
	// case is one extra role change that spends nothing. Persisting it would add a write to the
	// tick for a guard whose only job is to damp oscillation over minutes.
	roleMu    sync.Mutex
	roleSince map[string]time.Time
```

- [ ] **Step 3b: Write the reallocator**

Create `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go`:

```go
package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reallocateGateRoles moves gate hulls between the delivery and factory roles, once per tick.
//
// THE PAUSE IS A SELF-SHORTENING FEEDBACK LOOP. Delivery pauses because a terminal factory is
// low; those hulls go feed that factory; it produces faster; supply recovers sooner; delivery
// resumes. The pause actively works to end itself instead of idling capacity — which is also what
// makes an aggressive buy floor safe: over-buying costs a reallocation, not a stall.
//
// IT NEVER MOVES A CLAIM. AssignFleet does not evict a holder by design ("the tag takes effect
// when the present claim is released"), and constructionLot.claimIdentity is FROZEN at plan time
// — a hull re-tagged in flight would present a stale tag, and ClaimShip authorizes a new claim
// only when tag == operation, so it would be rejected at the DB and the hull would be dispatched
// and silently never work. Considering only IDLE, UNHELD hulls makes that unreachable rather than
// merely rare, and it satisfies the spec's "a hull mid-haul must finish its leg" structurally.
//
// AssignFleet is the SINGLE WRITE PATH for the dedicated_fleet column (RULINGS #3). A general
// ship save would be reverted by preserveDedicatedFleetTag, silently.
//
// It spends nothing. RULINGS #4 is not in scope here and no floor is read.
func (h *RunConstructionCoordinatorHandler) reallocateGateRoles(
	ctx context.Context,
	cmd *RunConstructionCoordinatorCommand,
	tasks []*manufacturing.ManufacturingTask,
	playerID shared.PlayerID,
) {
	// BOTH roles must have a wired leg. Moving a hull into a role whose leg does not run would
	// strand it on a path that does nothing — worse than leaving it where it was.
	if !h.gate.enabled() || !h.factory.enabled() || len(tasks) == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)

	outstanding := h.outstandingGateMaterials(ctx, tasks)
	if len(outstanding) == 0 {
		return // nothing left to build: the workforce split no longer matters
	}

	workers, err := h.gateWorkforce(ctx, cmd, playerID)
	if err != nil {
		// Fail closed: never write against an unknown fleet.
		logger.Log("WARNING", fmt.Sprintf("Gate roles: cannot read the fleet, so no role is changed this tick: %v", err), nil)
		return
	}
	if len(workers) == 0 {
		return
	}

	// The pause state the DELIVERY LEGS already wrote. Reading it here rather than re-deciding
	// costs no market read and cannot disagree with what the legs actually did.
	pipeline := h.gatePipeline(ctx, tasks)
	buyFloor, resumeFloor := "", ""
	if pipeline != nil {
		buyFloor, resumeFloor = pipeline.DeliveryBuyFloor(), pipeline.DeliveryResumeFloor()
	}
	paused := h.gate.policyFor(buyFloor, resumeFloor).FleetPaused(outstanding)

	plan := gate.PlanReallocation(gate.ReallocationInput{
		Now:            h.clock.Now(),
		DeliveryPaused: paused,
		Workers:        workers,
	})
	logger.Log("INFO", plan.LogLine(), map[string]interface{}{
		"paused": paused, "moves": len(plan.Moves), "held": len(plan.Skips),
		"have_delivery": plan.HaveDelivery, "have_factory": plan.HaveFactory, "unroled": plan.Unroled,
	})

	for _, move := range plan.Moves {
		if err := h.shipRepo.AssignFleet(ctx, move.Ship, move.To.FleetTag(), playerID); err != nil {
			// Stop early and keep the partial result: a hull left on its old tag is safe and is
			// retried a later tick, but writing on past a repository that just refused is not.
			logger.Log("ERROR", fmt.Sprintf("Gate roles: could not re-tag %s from %q to %q — stopping this tick's reallocation: %v", move.Ship, move.From, move.To.FleetTag(), err), map[string]interface{}{
				"ship": move.Ship, "from": move.From, "to": move.To.FleetTag(),
			})
			return
		}
		h.stampRoleChange(move.Ship)
		logger.Log("INFO", fmt.Sprintf("Gate roles: %s is now a %s hull (was %q) — %s", move.Ship, move.To, move.From, move.Reason), map[string]interface{}{
			"ship": move.Ship, "from": move.From, "to": move.To.FleetTag(), "reason": move.Reason,
		})
	}
}

// outstandingGateMaterials is the gate materials whose bill is still open.
//
// FILTERING TO OUTSTANDING IS LOAD-BEARING. A material whose bill is MET is never decided on by a
// delivery leg, so its pause state stays false forever; an unfiltered FleetPaused would then read
// FALSE the moment one material completes — exactly when the other is most likely starved and
// reallocation matters most.
func (h *RunConstructionCoordinatorHandler) outstandingGateMaterials(ctx context.Context, tasks []*manufacturing.ManufacturingTask) []string {
	pipeline := h.gatePipeline(ctx, tasks)
	if pipeline == nil {
		return nil
	}
	goods := make([]string, 0, len(pipeline.Materials()))
	for _, material := range pipeline.Materials() {
		if material.RemainingQuantity() <= 0 {
			continue
		}
		goods = append(goods, material.TradeSymbol())
	}
	return goods
}

// gatePipeline reads the pipeline behind this tick's ready tasks. Every construction task in a
// tick belongs to the same gate, so the first task's pipeline is the fleet's bill.
func (h *RunConstructionCoordinatorHandler) gatePipeline(ctx context.Context, tasks []*manufacturing.ManufacturingTask) *manufacturing.ManufacturingPipeline {
	for _, task := range tasks {
		if task == nil || task.PipelineID() == "" {
			continue
		}
		pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
		if err == nil && pipeline != nil {
			return pipeline
		}
	}
	return nil
}

// gateWorkforce is the live gate fleet as the reallocator sees it: every hull carrying a gate tag
// (the two roles PLUS the legacy one), in the operating system.
//
// Idle collapses three facts that all mean the same thing here — something is mid-haul with this
// hull: the drain's own worker registry holds it, the ship is not idle, or it is in transit. The
// registry is the authoritative one; the other two catch a hull flying with no registration
// behind it, which a restart can leave.
//
// The LEGACY tag is included deliberately: those hulls carry no role, and adopting them is the
// only way a fleet that already holds four of them ever gets a role tag at all. A foreign fleet or
// a custom launch identity is NOT a gate tag and is never in this census — re-tagging one would be
// a poach (RULINGS #7).
func (h *RunConstructionCoordinatorHandler) gateWorkforce(ctx context.Context, cmd *RunConstructionCoordinatorCommand, playerID shared.PlayerID) ([]gate.Worker, error) {
	ships, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}
	systemSymbol := cmd.SystemSymbol
	workers := make([]gate.Worker, 0, len(ships))
	for _, ship := range ships {
		if ship == nil || !gate.IsGateFleetTag(ship.DedicatedFleet()) {
			continue
		}
		if systemSymbol != "" {
			location := ship.CurrentLocation()
			if location == nil || shared.ExtractSystemSymbol(location.Symbol) != systemSymbol {
				continue // out of system: not this drain's to re-role
			}
		}
		workers = append(workers, gate.Worker{
			Ship:      ship.ShipSymbol(),
			FleetTag:  ship.DedicatedFleet(),
			Idle:      ship.IsIdle() && !ship.IsInTransit() && !h.supplies.holds(ship.ShipSymbol()),
			RoleSince: h.roleChangedAt(ship.ShipSymbol()),
		})
	}
	return workers, nil
}

// roleChangedAt reports when this process last re-roled the hull; the zero value means never,
// which is eligible immediately.
func (h *RunConstructionCoordinatorHandler) roleChangedAt(ship string) time.Time {
	h.roleMu.Lock()
	defer h.roleMu.Unlock()
	return h.roleSince[ship]
}

func (h *RunConstructionCoordinatorHandler) stampRoleChange(ship string) {
	h.roleMu.Lock()
	defer h.roleMu.Unlock()
	if h.roleSince == nil {
		h.roleSince = make(map[string]time.Time)
	}
	h.roleSince[ship] = h.clock.Now()
}
```

Add `"time"` to the file's imports.

- [ ] **Step 3c: Call it from the tick**

In `drainOnce` (`run_construction_coordinator.go`), immediately after the `h.reconcilePipelinesFromSite(ctx, tasks, cmd.PlayerID)` line and BEFORE `systemSymbol` is resolved for discovery, insert:

```go
	// Re-role gate hulls BEFORE hauler discovery, so a re-tag is visible to THIS tick: AssignFleet
	// invalidates the ship-list cache and claimIdentityFor reads the live tag, so a hull adopted
	// here is discovered under its new pool and claimed under its new identity in the same tick.
	// It moves only idle, unheld hulls and spends nothing.
	h.reallocateGateRoles(ctx, cmd, tasks, shared.MustNewPlayerID(cmd.PlayerID))
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/application/manufacturing/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

Expected: `ok` for every package. Then, UNPIPED: `go vet ./...`

- [ ] **Step 5: Commit (BEFORE the probes)**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): pause-driven role reallocation in the drain tick + legacy adoption (phase 3)

DECISION 1 -- legacy 'manufacturing' hulls ARE re-roled. This is the ARMING FIX.
obs.GateWorkers counts all three gate tags, and planGateWorkers buys only while
desired(4) > GateWorkers, so on a fleet already holding four legacy hulls the
ramp buys NOTHING: nextGateRole never runs, no role tag is ever written, role
discovery finds two empty pools, and phases 1-3 are entirely inert on the very
fleet they were built for. Adoption costs one AssignFleet write and no purchase.
The alternative -- narrowing GateWorkers so the ramp buys four more -- spends
four hull prices to avoid a free re-tag.

DECISION 2 -- reallocation WAITS for a natural release; it never moves a claim.
AssignFleet does not evict a holder by design, and constructionLot.claimIdentity
is FROZEN at plan time, so a hull re-tagged in flight presents a stale tag and
ClaimShip rejects it (tag == operation) -- dispatched, then silently never
works. Only idle, unheld hulls move, which satisfies 'a hull mid-haul finishes
its leg' structurally rather than by a wait-loop. The 10m dwell dominates the
latency anyway.

The trigger is the pause state the delivery legs ALREADY wrote, over the
OUTSTANDING materials only: a met bill is never decided on, so an unfiltered
FleetPaused would read false the moment one material completes -- exactly when
the other is most likely starved.

Runs BEFORE hauler discovery so a re-tag lands in the same tick. AssignFleet is
the single write path (RULINGS #3). Spends nothing." -- gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc_test.go gobot/internal/application/manufacturing/commands/run_construction_coordinator.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_test.go
```

- [ ] **Step 6a: Mutation-probe the BUSY/claim-lifecycle guard**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|Idle:      ship.IsIdle() \&\& !ship.IsInTransit() \&\& !h.supplies.holds(ship.ShipSymbol()),|Idle:      true, // MUTANT|' internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && grep -q 'Idle:      true, // MUTANT' internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestReallocateGateRoles' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestReallocateGateRoles_NeverMovesAHullASupplyWorkerStillHolds` and `--- FAIL: TestReallocateGateRoles_NeverMovesAHullInTransit`, then `RESTORED`.

- [ ] **Step 6b: Mutation-probe the OUTSTANDING-materials filter**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\t\tif material\.RemainingQuantity\(\) <= 0 \{\n\t\t\tcontinue\n\t\t\}\n//' internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && ! grep -q 'material.RemainingQuantity() <= 0' internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestReallocateGateRoles' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestReallocateGateRoles_IgnoresMaterialsWhoseBillIsAlreadyMet`, then `RESTORED`.

- [ ] **Step 6c: Mutation-probe the gate-tag NARROWNESS (the no-poach guard)**

ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && sed -i '' 's|if ship == nil \|\| !gate.IsGateFleetTag(ship.DedicatedFleet()) {|if ship == nil {|' internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && ! grep -q 'gate.IsGateFleetTag(ship.DedicatedFleet())' internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && echo "MUTATION APPLIED" && go test ./internal/application/manufacturing/commands/ -run 'TestReallocateGateRoles' 2>&1 | grep -E "^(--- FAIL|FAIL|ok)" | head -8; git checkout -- internal/application/manufacturing/commands/run_construction_coordinator_gate_realloc.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestReallocateGateRoles_NeverTouchesAForeignOrCustomFleetTag`, then `RESTORED`. This is the RULINGS #7 poach guard; zero named tests means a contract hull could be re-tagged into the gate fleet with nothing objecting.

---

### Task 7: Wire it ARMED, extend the era-invariance guards, and sweep

Nothing above is reachable in production until `main.go` wires the factory collaborators. Wiring is **unconditional and alongside the existing `SetGateDelivery`** — no flag, no config key, no arm seam (constraint 7). The nil-guard inside `SetGateFactory` is the drain's optional-collaborator pattern for its own fixtures, not an off switch, and this task is what makes that distinction true rather than merely claimed.

Two source guards ride along, both build-failing rather than advisory:

- **Era invariance.** Waypoint symbols are regenerated every era, so a literal in these layers works perfectly until the next era rolls and then silently sends hulls nowhere. Phase 2's package sweep already globs `*.go`, so `feed.go` and `reallocation.go` are picked up automatically — but its `required` list and minimum count must be raised, or a future deletion could shrink the package back below the bar unnoticed.
- **Constraint 2, made structural.** `feed.go` must not name `GetRequiredInputs` and must not import the `goods` package. That is the difference between "we agreed not to swap the seams" and "the swap does not compile".

**Files:**
- Modify: `gobot/cmd/spacetraders-daemon/main.go`
- Modify: `gobot/internal/domain/manufacturing/gate/fill_test.go` (raise the sweep's own coverage assertion)
- Test: `gobot/internal/domain/manufacturing/gate/feed_test.go` (append the seam guard)
- Test: `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go` (append the era guard)

- [ ] **Step 1: Write the failing tests**

Append to `gobot/internal/domain/manufacturing/gate/feed_test.go`:

```go
// CONSTRAINT 2, MADE STRUCTURAL. GateTopology.Inputs and goods.GetRequiredInputs diverge in
// CONTENT, not merely in shape: since sp-4irrr, Inputs("IRON_ORE") is nil (the curated predicate
// calls it raw) while GetRequiredInputs("IRON_ORE") is still {"EXPLOSIVES"}. A walk built on the
// latter descends an ore into IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON ->
// IRON_ORE and stops terminating.
//
// The walk therefore consumes the recipe graph ONLY through the Recipe seam, and this guard makes
// the substitution fail the build instead of failing in production a week later. It is asserted on
// feed.go alone: this package's TEST file imports goods deliberately, to walk the real map.
func TestFeedWalk_DoesNotReachTheRecipeMapDirectly(t *testing.T) {
	const file = "feed.go"

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	// Prove the guard read the walk itself; an emptied or renamed file would pass vacuously.
	if !strings.Contains(string(src), "func PlanFeed(") {
		t.Fatalf("%s does not contain PlanFeed; the guard is reading the wrong file", file)
	}
	for _, forbidden := range []string{"GetRequiredInputs", "ExportToImportMap", "IsMineableRawMaterial", "domain/goods"} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("%s names %q. The walk must reach the recipe graph ONLY through the Recipe seam: Inputs(\"IRON_ORE\") is nil while GetRequiredInputs(\"IRON_ORE\") is {EXPLOSIVES}, so a direct read descends an ore into the cycle and stops terminating (sp-4irrr)", file, forbidden)
		}
	}
}
```

Add `"os"` to `feed_test.go`'s imports.

In `gobot/internal/domain/manufacturing/gate/fill_test.go`, raise the sweep's self-coverage assertion — the package now has five source files:

```go
	if len(scanned) < 5 {
		t.Fatalf("guard scanned %d source file(s) %v; the gate policy package has at least 5 — a sweep that reads nothing proves nothing", len(scanned), scanned)
	}
	for _, required := range []string{"role.go", "buy_policy.go", "fill.go", "feed.go", "reallocation.go"} {
```

Append to `gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go`:

```go
// The factory leg and the reallocator resolve every location at runtime — the feed destination by
// EXPORT role, the source likewise. A waypoint literal in either would pin the fleet to one era's
// map and then quietly send hulls nowhere.
func TestGateFactorySources_ContainNoWaypointLiterals(t *testing.T) {
	files := map[string]string{
		"run_construction_coordinator_gate_feed.go":    "feedGateLeg",
		"run_construction_coordinator_gate_realloc.go": "reallocateGateRoles",
	}
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	// Four shapes, not one: a regression that TIGHTENS the pattern still matches X1-KP23-F46, so
	// a single-string calibration stays green while the guard goes blind to the rest.
	for _, known := range []string{
		`x := "X1-KP23-F46"`,
		`"X1-UM5-J59"`,
		`"X1-DR-GATE"`,
		`"X1-BETA-MARKETPLACE"`,
	} {
		if !waypointLiteral.MatchString(known) {
			t.Fatalf("waypoint-literal pattern failed its own calibration on %s", known)
		}
	}
	// The goods are the invariant. A pattern that flagged them would be unusable and deleted.
	for _, invariant := range []string{`good := "FAB_MATS"`, `inputs := []string{"IRON", "QUARTZ_SAND"}`} {
		if waypointLiteral.MatchString(invariant) {
			t.Fatalf("waypoint-literal pattern flags %s; goods are era-invariant and must be nameable directly", invariant)
		}
	}

	for file, sentinel := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if !strings.Contains(string(src), sentinel) {
			t.Fatalf("%s does not contain %s; the guard is reading the wrong file and would pass vacuously", file, sentinel)
		}
		if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
			t.Fatalf("%s contains hardcoded waypoint symbols %v — every location is resolved by market role, per era", file, found)
		}
		if strings.Contains(string(src), "X1-") {
			t.Fatalf("%s references an X1- prefixed symbol", file)
		}
	}
}
```

Add `"os"` and `"regexp"` to that file's imports.

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test ./internal/domain/manufacturing/gate/ -run 'TestFeedWalk_DoesNotReach|TestGatePolicyPackage' 2>&1 | tail -20 && go test ./internal/application/manufacturing/commands/ -run 'TestGateFactorySources' 2>&1 | tail -20
```

Expected: the two new guards compile and pass immediately if the implementation is already clean (they are regression guards, not drivers) — the one that MUST fail first is `TestGatePolicyPackage_ContainsNoWaypointLiterals`, which now demands five files and the two new names. If any guard passes before its subject exists, that is a vacuous pass: check the sentinel assertion fired.

- [ ] **Step 3: Wire it in `main.go`**

In `gobot/cmd/spacetraders-daemon/main.go`, immediately after the existing `SetGateDelivery(...)` block (~line 1002), add:

```go
	// The gate FACTORY fleet: the SAME phase 1 topology object answers the recipe seam the
	// recursive feed walk needs (IsRaw/Inputs — never goods.GetRequiredInputs, which returns a
	// recipe for every ore and would send the walk into the cyclic part of the map), the SAME
	// executor performs the pinned input buy through the one shared tranche loop, and feeds the
	// factory by selling into its import listing. Wired unconditionally, alongside the delivery
	// fleet: this ships ARMED. The nil-guard inside SetGateFactory exists for the drain's own
	// fixtures, not as an off switch.
	constructionCoordinatorHandler.SetGateFactory(
		goodsServices.NewGateTopology(goodsMarketLocator, goods.ExportToImportMap),
		constructionExecutor,
		constructionExecutor,
	)
```

- [ ] **Step 4: Full verification sweep**

Build and vet UNPIPED (zsh has no working `PIPESTATUS`, so a piped build hides its own status; and `go build` skips `_test.go`, so only `go vet` catches a stale call site living in another package's tests):

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go build ./...
```

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go vet ./...
```

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && gofmt -l ./cmd ./internal
```

Expected: no output from any of the three.

Then the touched packages, then the whole suite — **filtered, every time**:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./internal/domain/manufacturing/... ./internal/application/manufacturing/... ./internal/adapters/grpc/... 2>&1 | grep -E "^(ok|FAIL|---)" | head -40
```

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./... 2>&1 | grep -E "^(FAIL|---)" | head -60
```

Expected: **no output at all** from the last one. ~4550 tests across ~107 packages; the grep is deliberately `FAIL|---` only, because printing the `ok` lines for 107 packages is what exhausts a lane's context. To confirm the run actually happened rather than silently failing to start, follow it with:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && go test -race ./... 2>&1 | grep -cE "^ok"
```

Expected: a count near 107. **A count of 0 is a failed run, not a clean one** — silence from a failed command reads as success, and that misreading has cost this project real incidents.

- [ ] **Step 5: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3 && git commit --no-verify -m "feat(gate): wire the factory fleet ARMED + era-invariance and recipe-seam guards (phase 3)

main.go wires SetGateFactory unconditionally alongside SetGateDelivery: no flag,
no config key, no arm seam. The nil-guard inside the setter is the drain's
optional-collaborator pattern for its own fixtures, and this commit is what
makes that distinction true rather than merely claimed.

Two build-failing guards:

  - The gate policy package's waypoint-literal sweep now asserts it reads FIVE
    source files by name, so a future deletion cannot shrink the package back
    below the bar unnoticed. Symbols are regenerated every era; a literal works
    perfectly until the next era rolls and then sends hulls nowhere.

  - feed.go may not name GetRequiredInputs, ExportToImportMap,
    IsMineableRawMaterial or import domain/goods. Inputs('IRON_ORE') is nil
    while GetRequiredInputs('IRON_ORE') is {EXPLOSIVES}, so a direct read
    descends an ore into the cycle and stops terminating (sp-4irrr). This turns
    'we agreed not to swap the seams' into 'the swap does not compile'." -- gobot/cmd/spacetraders-daemon/main.go gobot/internal/domain/manufacturing/gate/feed_test.go gobot/internal/domain/manufacturing/gate/fill_test.go gobot/internal/application/manufacturing/commands/run_construction_coordinator_gate_feed_test.go
```

- [ ] **Step 6: Mutation-probe the WIRING**

A feature wired nowhere is indistinguishable from one that does not exist, and this project has shipped that failure repeatedly (closed ≠ armed). Prove the wiring is load-bearing. ONE invocation:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/gate-factory-phase3/gobot && perl -0pi -e 's/\tconstructionCoordinatorHandler\.SetGateFactory\(\n\t\tgoodsServices\.NewGateTopology\(goodsMarketLocator, goods\.ExportToImportMap\),\n\t\tconstructionExecutor,\n\t\tconstructionExecutor,\n\t\)\n//' cmd/spacetraders-daemon/main.go && ! grep -q 'SetGateFactory' cmd/spacetraders-daemon/main.go && echo "MUTATION APPLIED" && go build ./cmd/... && echo "BUILD OK — THE WIRING IS UNPINNED BY ANY TEST"; git checkout -- cmd/spacetraders-daemon/main.go && echo "RESTORED"
```

Expected: `MUTATION APPLIED`, `BUILD OK — THE WIRING IS UNPINNED BY ANY TEST`, `RESTORED`.

**This probe is expected to find NOTHING, and that finding is the point.** `main.go` composition has no unit test in this repo and this plan does not add one — a composition-root test would assert wiring against itself. Record the result in the handback as a known gap, and verify the wiring **at deploy** instead: after `make restart-daemon`, the first construction drain tick must log a `Gate factory feed plan for ...` line or a `Gate roles ...` line. If neither appears, the factory fleet is wired but inert, which is exactly the class of failure this repo has hit before.

---

## Handback

Report: worktree path, branch, one commit SHA per task, filtered test counts, and the result of **every** mutation probe naming the test each one killed. A probe that named zero tests is a finding, not a formality — report it explicitly rather than moving on. `go test` prints `[build failed]` in the same shape as a failure while naming no test, so "zero tests named" means *infra failure*, not *guard covered*.

Also report anything you disagreed with, anything you could not do, and any pre-existing test you had to touch — with which one and why. A pre-existing test that now fails was encoding old behaviour, and that is a finding worth surfacing, not a nuisance to fix quietly. In particular, the `deliverInputs` signature change and the `feedDestinationRefused` extraction (Task 3) touch shared code; if either broke a test, say which.

**Do NOT merge to main and do NOT run captain-gate — the orchestrator gates.**

### Deploy verification — this phase ships ARMED and has no flag to check

There is no migration and no new config key, so the deploy is a plain daemon restart. But **armed is not the same as working**, and this repo has repeatedly shipped features that were closed, merged, and inert. After `make restart-daemon`, the first construction drain tick must produce at least one of:

- `Gate factory feed plan for FAB_MATS: N step(s) — ...`
- `Gate roles (delivery running): have ...`

If the drain ticks and neither line appears, `SetGateFactory` did not take effect. If `Gate roles` appears but always reports `no role changes` on a fleet holding legacy hulls, the adoption census is not seeing them — check whether the drain was launched with a custom `DedicatedFleet`, which puts its hulls outside `IsGateFleetTag` by design.

---

## Flagged — what the spec did not settle, and what I believe is wrong in it

These are honest gaps, not hedges. Each names what is unresolved, what the plan does in the meantime, and who should rule.

### 1. The spec's thrash-guard constraints do not exist — WRONG IN THE SPEC

Fleet Mechanics says: *"Existing `worker_rebalancer` constraints apply (`ferry_cooldown_secs`, `max_concurrent_ferries`)."* Both are unreachable:

- The worker-rebalancer coordinator was **deleted** in `712b6f66` (sp-hoj8u). `worker_rebalancer_coordinator` is in `retiredCommandTypes` (`daemon_server_recovery.go:29`) with no builder and no launch site; `command_factory_registry.go:146` says so explicitly.
- `WorkerRebalancerConfig` survives as dead config. `s.workerRebalancerConfig` is assigned at `daemon_server.go:272` and **read by nothing**; `EnabledOrDefault()` has zero callers; `resolveWorkerRebalancerConfig` — named in two comments — does not exist. Both knobs appear at exactly four lines in the Go tree, all declarations. `config.yaml.example:327-335` still advertises defaults no code implements.
- They are also the wrong shape. They govern a cross-system **ferry** of idle **undedicated** light haulers; `ship_pool_manager.go:94` (`if ship.DedicatedFleet() != "" { continue }`) excludes every tagged hull, so a `gate-factory` hull could never have entered that path.

Task 2 therefore declares its own (`DefaultRoleDwell = 10m`, `DefaultMaxRoleMovesPerTick = 1`) and documents why. **The spec's Fleet Mechanics section should be corrected**, and someone should decide whether the dead `WorkerRebalancerConfig` struct plus its `config.yaml.example` block are deleted or left as documented debt. Not this plan's call.

### 2. "They move back" is under-specified, and the literal reading is a fleet-killer

The spec says only *"when it unpauses, they move back."* Read literally that returns every factory hull to delivery — including the two the D/F/F/D order **bought** as factory hulls — which empties the factory fleet, starves the terminal factories, and re-trips the pause. Task 2 resolves it by making the unpaused target the **D/F/F/D baseline mix** rather than a direction, derived from the same `NextRole` the purchase path uses.

That is my inference, not the spec's words. If the intent really was "every reallocated hull returns and the bought mix is restored by tracking which hulls were moved", that is a different (stateful, restart-fragile) design and should be ruled on explicitly.

### 3. The feeding leg parks its material task every time it runs

`feedGateLeg` supplies nothing to the construction site, so it exits through `completeOrDefer`, which parks the task PENDING for the `SupplyMonitor` to re-activate. That is the same terminal phase 2's fleet-paused stand-down already uses, and skipping the completion machinery entirely would be far worse (a task left EXECUTING is never re-staged and the ready queue drains silently).

The cost: on a tick where a factory hull takes a material's **single** persisted ready task, that material ping-pongs READY → EXECUTING → PENDING. The mitigation already present is the fan-out — `planDispatchLots` pass 2 mints **ephemeral** clones, and an ephemeral lot never defers the original — so in the normal case a delivery hull holds the real task and a factory hull holds a clone. But lot-to-hull pairing is **not role-aware**, so the reverse pairing happens too.

**Watch this in production.** If the ready queue is observed thrashing, the fix is to make `planDispatchLots` pass 1 prefer delivery-role hulls for the persisted task and hand factory hulls clones. That is a real change to the planner and deliberately out of this phase's scope.

### 4. A factory hull can still wedge at a full hold, and nothing declines it

Task 5 deliberately scopes the `wedgedAtFullHold` decline to `RoleDelivery`, because a factory hull full of **inputs** is recoverable (`FeedFactory` sells them into the factory). But a factory hull full of a **met-bill gate material** is not: `flushOnHandGateMaterials` skips a material whose bill is closed, so the hold never frees, `capacity <= 0`, and the leg parks — every tick, consuming a dispatch slot.

It is strictly narrower than the pre-phase-2 failure (one degraded hull, not a fleet stall), it is logged loudly (`no free hold after the flush`), and it can only arise from a hull re-roled while laden or a restart mid-leg — both of which the busy guard makes rare. Getting the cargo off needs a sell/dump path, which is money-adjacent and out of scope, exactly as phase 2 concluded for the delivery side.

### 5. Factory-fleet spend has no bill to bound it

A gate material has a construction bill; **an input does not**. Nothing downstream tells the factory fleet when to stop buying IRON_ORE. What bounds it in this plan is: one step per leg, one hull-load per step, the market's `trade_volume`, the ABUNDANT fail-safe on the destination, and the unchanged per-tranche working-capital floor (150k, failing closed).

That is a real bound but it is **my** bound — the spec is silent. If the factory fleet is observed over-buying feedstock, the next lever is a supply floor on the *input's own source* (mirroring the delivery buy floor), which would be a new knob and therefore a deliberate decision, not a patch.

### 6. The 150k floor, again

Phase 2 flagged this and it is still true: the spec's Money Guards CORRECTION already names `common.NonContractWorkingCapitalFloor` = 150k as the real floor on this path, but the spec's Testing section still says *"both roles refuse to spend below the 50k floor"*. The factory role's buy goes through the same `spendFloorBreached`, so it is guarded at **150k or higher**, never less. Safe direction, wrong number in the doc. Someone should reconcile the Testing section with the CORRECTION block.

### 7. Phase 4's obligations, restated so they do not evaporate

Phase 2's plan flagged that Risk 2's **characterization tests** ("pin current acquisition behavior before deleting the depth cap or the `isTargetGood` branch") transfer to phase 4. Phase 3 deletes nothing either, so that obligation transfers again, **plus one more**: the fabricate depth cap is now doubly load-bearing — `services.defaultFabricateMaxDepth` for the resolver, `gate.DefaultFeedDepthCap` for the feed walk — and the spec's original "delete the cap" line remains struck for both. Phase 4's plan should open by restating this.

### 8. Smaller decisions this plan made that no source settled

- **One feed step per leg**, not a full chain per trip. A hull carries one input to one factory. Simpler to reason about, bounded in spend, and it means the shallowest (most binding) step is worked first every tick. A multi-step trip would amortize travel better and is the obvious later optimization.
- **The ABUNDANT skip** is new policy the spec never mentions. It is deliberately the ladder's top and not a threshold, so it adds no knob; a full factory needing no feedstock is not a judgement call.
- **`DefaultFeedDepthCap = 3`** matches `services.defaultFabricateMaxDepth` by choice, not by coupling. Both gate chains bottom out on the curated raw predicate inside it (FAB_MATS at depth 2, ADVANCED_CIRCUITRY at depth 3), so on live data the cap never fires — which is what a backstop should do.
- **Reallocation requires BOTH roles wired.** Moving a hull into a role whose leg does not run would strand it on a path that does nothing, which is worse than leaving it where it was.
- **The dwell ledger is in-memory**, following the pause state's precedent. A restart re-derives; the worst case is one extra role change that spends nothing.
- **Only the exact `LegacyFleetTag` is adopted.** A drain launched with a custom `DedicatedFleet` carries a tag `IsGateFleetTag` does not recognise, so those hulls are outside the census entirely — deliberate, and worth knowing before someone launches one and wonders why nothing is adopted.
- **`runsGateLeg` is removed rather than kept as a wrapper.** A boolean wrapper over a role-returning function would let the two questions drift apart again, which is the entire hazard the shared predicate exists to close.
