# Retire Off-Gate Warp Expansion — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire off-gate warp expansion — the explorer hull class and its whole demand-to-buy chain — so no code path can emit explorer demand onto the latching bridge, while leaving the warp/charter stack intact for the manual `spacetraders ship warp` verb.

**Architecture:** The off-gate slice is a driver (`parkedsensing/offgate.go`) that writes a demand signal onto a latching in-memory bridge (`adapters/expansion.ExplorerOffGateBridge`), whose only reader is the fleet autosizer's `ExplorerDemandProvider`. Retirement removes the driver, the bridge, the reader, and the four explorer-only adapters that fed them, in an order that proves the bridge can never hold a standing demand *before* any of it is deleted. The warp executor, charter, navigator and escape reader are shared with the operator's manual `ship warp` verb and are not touched — retirement removes one of two callers, not the stack.

**Tech Stack:** Go, Postgres, the gobot daemon's coordinator/container model

## Global Constraints

- All code lands via worktree → captain-gate → main. Never merge by hand.
- Money guards fail closed and are never weakened (RULINGS #4).
- Features ship ARMED — no feature flags, no default-off arming seam (standing order).
- Nothing stored that can be derived (RULINGS #2).
- Protected paths, never modified: `gobot/internal/captain/**`, `gobot/cmd/captain-gate/**`, `city/agents/**`, `gc` source.
- Comment discipline: `ENGINEERING.md §6`. A density ratchet is live — run `make comment-audit-check`; if a touched package regresses, TRIM rather than re-baseline.
- Test output must be filtered (`grep -E '^(FAIL|--- FAIL)|panic:|DATA RACE'`), never dumped raw.
- `go build` skips `_test.go` — `go vet ./...` is what catches signature breaks and must be run.
- Known pre-existing flake, not a lane's fault: `TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient` in `internal/adapters/api` (~1 run in 4–9), filed sp-odim4.
- Commits use an explicit pathspec (`-- gobot/`) — the bd hook otherwise injects `.beads/issues.jsonl`, which must never be committed.

---

## Orientation — read this before Task 1

**Worktree root:** `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate`
**Go module root:** `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot` — every `go`/`make` command below runs from there.
Sibling worktrees live under `.claude/worktrees/`; scope every search to the paths given here, never to the repo root.

**The hazard, in one paragraph.** `parkedsensing.advanceOffGate` writes an `OffGateDemandSignal` onto `OffGateDemandSink` on *every* tick. The concrete sink, `expansion.ExplorerOffGateBridge`, **latches**: it answers every read from the last write, forever. Its only reader is the autosizer's `ExplorerDemandProvider`. Delete the reader while the writer still runs and you strand a bridge latching demand nobody serves; delete the writer carelessly and you can leave a `Demanded: true` standing from the last tick before the deploy. That is why Task 1 proves the emit path is gone before anything else is removed.

**THE METHOD WARNING — this is a step, not a footnote.** You **cannot** establish that a Go interface is dead by grepping its name. Interfaces are satisfied *implicitly*: an adapter can satisfy `OffGateSelector` without the string `OffGateSelector` appearing anywhere in its file — and in this codebase one does (`expansion.OffGateWarpTargetSelector` satisfies `parkedsensing.OffGateSelector` by shape alone). Deadness is established by finding **who consumes the port STRUCT that holds the interface** — here `OffGatePorts`, and above it `ExpandPorts.OffGate` and `SensingEnginePorts.OffGate`. Every task below that deletes an interface carries an explicit consumer-of-the-struct verification step. A step that said "grep for references and delete if none" would be wrong and would delete live code.

**Surface corrections found while verifying the bead (sp-mn3it).** Use these numbers, not the bead's:

| Bead says | Actually |
|---|---|
| Wiring at `main.go:331-332` (two fields) | **`main.go:331-334` — FOUR fields**: `offGateSelect`, `offGateDemand`, `idleExplorer`, `explorerWarp` |
| `OffGatePorts` literal at `main.go:388-390` | **`main.go:388-393`** — the literal assigns all four |
| (not mentioned) | Construction also at **`main.go:1158`** (bridge), **`main.go:1175-1180`** (selector/finder/dispatcher), **`main.go:1200`** (bridge handed to the autosizer handler), **`main.go:1403-1406`** (the `sensingWiring` literal) |
| (not mentioned) | `SensingEnginePorts.OffGate` at **`sensing_engine_ports.go:169`**, copied through at **`:363`** — a second ports struct above `ExpandPorts` |
| (not mentioned) | Deleting `ExplorerOffGateBridge` **forces** removal of the `offGateDemand fleetCmd.OffGateDemandSource` parameter of `NewFleetAutosizerCoordinatorHandler` (`fleet_autosizer_ports.go:44`) and the provider registration at `:69` — a compiler consequence, not a scope choice |

Everything else in the bead verified correct: `advanceOffGate` called once at `expansion.go:473`; `retractOffGateDemand` called once at `expansion.go:413`; `OffGateTargetSelector` (`offgate_types.go:48`) and `ShipyardCoverageReader` (`offgate_types.go:57`) are orphaned declarations with zero implementations; `reachesAny` (`reach.go:290`) has exactly one caller, `expansion.go:469`.

**Scope line for this slice.** Everything that can **emit, carry, read, or ARM** explorer demand goes now. Inert *vocabulary* waits for the autosizer deletion (epic slice 4):

- **IN:** the parkedsensing driver, `OffGatePorts` and its four interfaces, the two orphaned declarations, the four explorer-only adapters, the universe-roster stack, the autosizer's explorer demand provider + fleet source + its `offGateDemand` parameter, the 5 `autosizer_*_explorer*` tune keys and their whole config chain, and the `HullClassExplorer` arms in `classDisabled` / `classGuardConfig`.
- **DEFERRED to slice 4 (autosizer deletion):** `hullbuy.HullClassExplorer` (`domain/hullbuy/hull_class.go:23`), its `"explorer"` arm in `hullbuy.DedicatedFleet` (`:47-48`), and the two `hull_class_test.go` rows that pin them (`:21`, `:50`). **Reason:** `hullbuy` is a shared vocabulary package owned by no coordinator — its own doc comment says so — imported by both the fleet autosizer and the dedicated contract scaler, which slice 4 rewrites. After this slice the constant is *inert*: nothing constructs an explorer order, nothing registers an explorer provider, so it cannot reach a purchase and RULINGS #4 is untouched either way. Touching `hullbuy` here buys zero behaviour and creates a merge-conflict surface with slice 4. Contrast the demand chain, which is **not** deferrable: leaving `ExplorerDemandProvider` alive against a writer-less bridge is exactly the "latching bridge nobody serves" the bead names as the hazard, and the compiler forces its removal the moment the bridge goes.
- **NOT DEAD, do not touch:** no `heavy` config surface dies in this slice. `autosizer_heavy_cap`, `autosizer_heavy_unserved_lanes_min`, `autosizer_heavy_treasury_pct_per_purchase`, `autosizer_max_price_heavies`, `autosizer_ship_type_heavies` and the heavy demand provider all stay live — `heavy_cap` re-homing is slice 3 of epic sp-sy3dl and is planned separately.

**MUST NOT be removed — the warp/charter stack.** `ExecuteWarpRoute`, `route_executor_warp.go`, `route_executor_warp_errors.go`, `warp_system_charter.go`, `warp_navigator.go`, `warp_escape_reader.go`, `container.ContainerTypeWarp`, the `"warp_ship"` recovery key, and `HasWarpDrive()` are all reached by the manual operator verb `spacetraders ship warp` (`adapters/cli/ship_navigate.go:157` → `grpc/container_ops_ship.go:100` → `application/ship/commands/navigation/warp_ship.go:65`, wired at `main.go:1116-1136`). Task 9 proves they still pass.

**Money-safety of the intermediate states.** Between Task 1 and Task 6 the bridge exists but is never written. `ExplorerOffGateBridge.ExplorerDemand` returns `ok=false` until a player's first emit, and `ExplorerDemandProvider` treats `ok=false` as `Readable: false` — it fails **CLOSED**. `ExplorerHullsEnabled` also defaults false and appears in no checked-in YAML. Every intermediate commit is therefore strictly money-safer than the state before it (RULINGS #4 moves in the safe direction only).

---

## Task 1 — Prove no tick can emit explorer demand, then remove the two call sites

The proof is written and demonstrated **failing** before a single line of the driver is deleted. The removal of the two call sites is the minimal change that makes it pass.

**Note on the assertion's shape.** The proof asserts the sink is **never called at all**, not "called with a zero signal". `retractOffGateDemand` existed to clear a latch that a live raiser could set; with no raiser, the correct steady state is "never written", and a bridge never written reads UNREADABLE, which makes the autosizer's explorer pass fail CLOSED. That is strictly safer than a written zero. Two existing tests assert the opposite (that a paused tick *must* write) and are replaced in this task — they were pinning the old contract, and the contract is what is changing.

**Files:**
- Create: `gobot/internal/application/parkedsensing/offgate_retraction_test.go`
- Modify: `gobot/internal/application/parkedsensing/expansion.go` (lines 409-416 — the spend-pause branch; lines 465-474 — the reach discriminator + the `advanceOffGate` call)
- Modify: `gobot/internal/application/parkedsensing/expansion_spend_pause_test.go` (delete lines 173-253 — `TestAdvanceExpansion_SpendPaused_RetractsStandingExplorerDemand` and `TestAdvanceExpansion_SpendPaused_RetractsThroughAPartiallyWiredSlice`, with both doc comments)

**Interfaces:**
- Consumes: `AdvanceExpansion(ctx context.Context, p ExpandPorts, playerID int, k ExpandKnobs, budgetRate float64) (ExpandReport, error)`; `OffGatePorts{Select OffGateSelector; Demand OffGateDemandSink; Explorer ExplorerFinder; Warp ExplorerDispatcher}`; `OffGateDemandSink.EmitOffGateDemand(playerID int, signal OffGateDemandSignal)`; the package test harness `newExpandHarness() *expandHarness` (`expansion_test.go:493`), `(*expandHarness).ports() ExpandPorts` (`:507`), `staffedYardRow(system, waypoint string) QueuedSlot` (`staging_test.go:35`)
- Produces: no production symbols. Test-only: `recordingDemandSink`, `alwaysFindsSelector`, `alwaysFindsExplorer`, `recordingWarpDispatcher`, `sealedPocketExpandPorts(t *testing.T) (ExpandPorts, *recordingDemandSink, map[string]bool)`

**Steps:**

- [ ] Confirm you are on the right branch and it is clean.
  ```bash
  git -C /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate rev-parse --abbrev-ref HEAD
  git -C /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate status --short
  ```
  Expect `sp-offgate` and no output from `status`.

- [ ] Write the failing proof. Create `gobot/internal/application/parkedsensing/offgate_retraction_test.go` with exactly this content:
  ```go
  package parkedsensing

  import (
  	"context"
  	"testing"
  )

  // offgate_retraction_test.go is THE PROOF that retiring off-gate warp expansion cannot leave a
  // latched explorer demand standing, and it is deliberately written BEFORE a line of the driver is
  // deleted. Removing the code first and testing after would invert the risk: the assertion would be
  // authored against a tree that can no longer contradict it.
  //
  // IT ASSERTS THE SINK IS NEVER CALLED, not that it is called with a zero. The bridge answers every
  // read from its last write and reads UNREADABLE until its first write for a player; the autosizer's
  // explorer pass fails CLOSED on unreadable. "Never written" is therefore strictly safer than
  // "written zero", and it is the state retirement actually delivers.
  //
  // ALL FOUR PORTS ARE WIRED WITH LIVE FAKES, and that is the calibration. advanceOffGate returns
  // immediately when OffGatePorts.wired() is false, so a fixture that left one port nil would pass
  // this test against the UNMODIFIED driver and prove nothing at all.

  // recordingDemandSink records every emit so the assertion can be "never called".
  type recordingDemandSink struct{ calls []OffGateDemandSignal }

  func (r *recordingDemandSink) EmitOffGateDemand(_ int, s OffGateDemandSignal) {
  	r.calls = append(r.calls, s)
  }

  // alwaysFindsSelector hands back a usable off-gate target on every call, so nothing downstream of
  // the emit can be the reason the sink stayed quiet.
  type alwaysFindsSelector struct{}

  func (alwaysFindsSelector) SelectTarget(_ context.Context, _ int, _ OffGateSelectionParams) (OffGateTarget, bool, error) {
  	return OffGateTarget{SystemSymbol: "X1-FARAWAY", FromSystem: "X1-HOME", WarpFuelCost: 240, Value: 2}, true, nil
  }

  // alwaysFindsExplorer owns an idle explorer, so the dispatch half is reachable too.
  type alwaysFindsExplorer struct{}

  func (alwaysFindsExplorer) IdleExplorer(_ context.Context, _ int) (string, bool, error) {
  	return "EXPLORER-1", true, nil
  }

  // recordingWarpDispatcher records every dispatch: a retired slice must not fly a hull either.
  type recordingWarpDispatcher struct{ calls []string }

  func (w *recordingWarpDispatcher) DispatchExplorer(_ context.Context, _ int, ship string, _ OffGateTarget) error {
  	w.calls = append(w.calls, ship)
  	return nil
  }

  // sealedPocketExpandPorts builds the ONE ledger shape that can reach the emit path: a system we
  // hold and staff, a target with outstanding charting work, and an EMPTY gate adjacency — which is
  // what "every way out is under construction" looks like to the reach search, since it only ever
  // sees traversable edges. A fixture with any gate route lets the gate pass suppress off-gate
  // demand and the test passes for the wrong reason.
  func sealedPocketExpandPorts(t *testing.T) (ExpandPorts, *recordingDemandSink, map[string]bool) {
  	t.Helper()
  	h := newExpandHarness()
  	h.ledger.systems = []ExpandSystem{
  		{System: "X1-HOME", Verdict: VerdictInScope},
  		{System: "X1-SEALED", Verdict: VerdictPending, UnchartedCount: 5},
  	}
  	h.gates.adjacency = map[string][]string{"X1-HOME": {}, "X1-SEALED": {}}
  	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-HOME", "X1-HOME-YARD")}
  	for i := range h.ledger.systems {
  		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
  	}

  	sink := &recordingDemandSink{}
  	p := h.ports()
  	p.OffGate = OffGatePorts{
  		Select:   alwaysFindsSelector{},
  		Demand:   sink,
  		Explorer: alwaysFindsExplorer{},
  		Warp:     &recordingWarpDispatcher{},
  	}
  	return p, sink, h.whitelist
  }

  // NO TICK EMITS EXPLORER DEMAND — armed or paused, sealed or not. Both halves are exercised because
  // the two call sites are on opposite sides of the spend gate: advanceOffGate below it,
  // retractOffGateDemand inside it. A test that ran only one half would leave the other free to write.
  func TestAdvanceExpansion_NoTickEmitsExplorerDemand(t *testing.T) {
  	for _, spend := range []bool{true, false} {
  		p, sink, whitelist := sealedPocketExpandPorts(t)
  		if _, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
  			SpendEnabled: spend, MinBudgetRate: 0.05, Whitelist: whitelist,
  		}, 1.0); err != nil {
  			t.Fatalf("spend=%v: unexpected error: %v", spend, err)
  		}
  		if len(sink.calls) != 0 {
  			t.Fatalf("spend=%v: the tick wrote %d signal(s) to the explorer-demand bridge: %+v — the bridge LATCHES, so any write is a standing demand the autosizer can buy against",
  				spend, len(sink.calls), sink.calls)
  		}
  	}
  }

  // AND NO TICK FLIES ONE. The dispatch half is downstream of the emit, so it should fall with it —
  // asserted separately because a driver could in principle warp without emitting.
  func TestAdvanceExpansion_NoTickDispatchesAnExplorer(t *testing.T) {
  	p, _, whitelist := sealedPocketExpandPorts(t)
  	warp := p.OffGate.Warp.(*recordingWarpDispatcher)
  	if _, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
  		SpendEnabled: true, MinBudgetRate: 0.05, Whitelist: whitelist,
  	}, 1.0); err != nil {
  		t.Fatalf("unexpected error: %v", err)
  	}
  	if len(warp.calls) != 0 {
  		t.Fatalf("the tick dispatched %d warp(s): %v — off-gate warp expansion is retired", len(warp.calls), warp.calls)
  	}
  }
  ```

- [ ] Run it and see it FAIL. This RED is the calibration — it proves the fixture actually reaches the emit path.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/application/parkedsensing/ -run 'TestAdvanceExpansion_NoTick' 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE|bridge LATCHES|dispatched'
  ```
  Expect `--- FAIL: TestAdvanceExpansion_NoTickEmitsExplorerDemand` and `--- FAIL: TestAdvanceExpansion_NoTickDispatchesAnExplorer`, with the "the bridge LATCHES" message showing at least one emitted signal. **If either test PASSES here, stop** — the fixture is not reaching `advanceOffGate` (most likely `wired()` is false or the gate adjacency admits a route), and the proof is vacuous.

- [ ] Remove the spend-pause call site. In `gobot/internal/application/parkedsensing/expansion.go`, replace lines 409-416:
  ```go
  	if !k.SpendEnabled {
  		// RETRACTED, NOT MERELY UNRAISED. The explorer-demand bridge latches, so a
  		// pause that merely stopped calling advanceOffGate would leave the last
  		// `Demanded: true` standing forever and the autosizer buying against it.
  		retractOffGateDemand(p, playerID)
  		rep.SpendingPaused = true
  		return rep, nil
  	}
  ```
  with:
  ```go
  	if !k.SpendEnabled {
  		rep.SpendingPaused = true
  		return rep, nil
  	}
  ```

- [ ] Remove the `advanceOffGate` call site and the reach discriminator that exists only to feed it. In the same file, replace lines 465-474:
  ```go
  	// OFF-GATE, LAST, and only once the gate passes have had their turn. Warp is the expensive
  	// fallback — a heavy hull and a multi-system flight against a probe that walks a gate for free —
  	// so a single gate-reachable target suppresses it. A read failure fails the tick rather than
  	// reading as "the frontier is exhausted", which would raise explorer demand off a DB hiccup.
  	gateReachable, err := reach.reachesAny(ctx, targets, book)
  	if err != nil {
  		return rep, err
  	}
  	advanceOffGate(ctx, p, playerID, targets, gateReachable, &rep)
  	return rep, nil
  }
  ```
  with:
  ```go
  	return rep, nil
  }
  ```
  (`reachesAny`'s only caller was this block; `gateReachable` would otherwise be a declared-and-unused compile error. The method itself is removed in Task 2.)

- [ ] Delete the two tests that pinned the old retraction contract. In `gobot/internal/application/parkedsensing/expansion_spend_pause_test.go`, delete lines 173-253 — from the comment block beginning `// THE LATCH, and the reason the pause RETRACTS off-gate demand` (line 173) through the closing brace of `TestAdvanceExpansion_SpendPaused_RetractsThroughAPartiallyWiredSlice` (line 253), inclusive of both doc comments and both functions. Verify the boundaries first:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    sed -n '172,175p;251,256p' internal/application/parkedsensing/expansion_spend_pause_test.go
  ```
  Expect line 173 to be `// THE LATCH, and the reason the pause RETRACTS off-gate demand rather than merely stopping to raise`, line 253 to be a bare `}`, and line 255 to be `// --- the free half stays free -------------------------------------------------`.

- [ ] Run the proof and see it PASS.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/application/parkedsensing/ -run 'TestAdvanceExpansion_NoTick' 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect `ok  	github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing`.

- [ ] Run the whole package. Several off-gate tests in `offgate_test.go` now fail — that is expected and correct, they assert the retired behaviour, and Task 2 deletes them. Record which ones fail so Task 2 can confirm it deleted exactly that set.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/application/parkedsensing/ 2>&1 | grep -E '^(FAIL|--- FAIL)|panic:|DATA RACE'
  ```
  Expect `--- FAIL` for exactly: `TestAdvanceExpansion_SealedFrontierWarpsTheIdleExplorerToTheOffGateTarget`, `TestAdvanceExpansion_NoExplorerOwnedRaisesDemandAndWarpsNothing`, `TestAdvanceExpansion_DemandWithoutATargetWarpsNothing`, `TestAdvanceExpansion_AFailingSelectorRaisesDemandButNeverWarps`, `TestAdvanceExpansion_AnUnreadableExplorerFleetWarpsNothing`, `TestAdvanceExpansion_ARefusedWarpDoesNotFailTheTick`, `TestAdvanceExpansion_OffGateSelectionIsBoundedByTheExplorersFuelCapacity`. Any *other* failing test is a real regression — stop and investigate.

- [ ] Build and vet.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./internal/application/parkedsensing/
  ```
  Expect no output from either.

- [ ] Commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git add gobot/internal/application/parkedsensing/offgate_retraction_test.go && \
    git commit --no-verify -m "test(parkedsensing): prove no tick can emit explorer demand, then cut the two off-gate call sites (sp-mn3it, epic sp-sy3dl)

The proof is written and demonstrated failing BEFORE any deletion. It asserts the
demand sink is NEVER CALLED — stronger than 'called with zero', because a bridge
never written reads UNREADABLE and the autosizer's explorer pass fails CLOSED on
unreadable. All four OffGate ports are wired with live fakes so the fixture cannot
pass vacuously through wired()==false.

Cuts advanceOffGate (expansion.go:473), retractOffGateDemand (expansion.go:413) and
the reachesAny discriminator that existed only to feed the former. Replaces the two
spend-pause tests that pinned the old retract-on-pause contract." -- gobot/
  ```

---

## Task 2 — Delete the off-gate driver and the tests that pinned it

The call sites are gone; the driver functions are now unreachable. Go does not error on an unused package-level function, so this is a separate, verifiable step.

**Files:**
- Modify: `gobot/internal/application/parkedsensing/offgate.go` (delete lines 102-235 — `retractOffGateDemand`, `advanceOffGate`, `dispatchExplorer` and their doc comments; keep lines 1-101, the interfaces and the ranking constants, until Task 3)
- Modify: `gobot/internal/application/parkedsensing/reach.go` (delete lines 286-303 — `reachesAny` and its doc comment)
- Delete: `gobot/internal/application/parkedsensing/offgate_test.go` (444 lines)

**Interfaces:**
- Consumes: nothing new
- Produces: removes `advanceOffGate(ctx context.Context, p ExpandPorts, playerID int, targets []ExpandSystem, gateReachable bool, rep *ExpandReport)`, `retractOffGateDemand(p ExpandPorts, playerID int)`, `dispatchExplorer(ctx context.Context, off OffGatePorts, playerID int, target OffGateTarget, rep *ExpandReport)`, `(*gateReach).reachesAny(ctx context.Context, targets []ExpandSystem, book *slotBook) (bool, error)`

**Steps:**

- [ ] Verify the three driver functions have zero remaining call sites. This is a *function* deadness check, and for functions a name grep is sound — unlike an interface, a function can only be reached by its name.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'advanceOffGate\|retractOffGateDemand\|dispatchExplorer\|reachesAny' --include='*.go' .
  ```
  Expect hits only inside `internal/application/parkedsensing/offgate.go` (declarations and doc comments), `internal/application/parkedsensing/reach.go` (the `reachesAny` declaration) and `internal/application/parkedsensing/expansion.go:333` (a stale doc comment naming `advanceOffGate`, fixed below). No call sites.

- [ ] Delete the driver functions. In `gobot/internal/application/parkedsensing/offgate.go`, delete everything from line 102 (the comment `// retractOffGateDemand withdraws any standing explorer demand, ...`) to the end of the file at line 235. Confirm the file now ends with the const block:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    tail -8 internal/application/parkedsensing/offgate.go
  ```
  Expect the `offGateWarpRangeFuel = 800` / `offGateValueWeight = 100` / `offGateFuelWeight = 1` const block and its closing `)`.

- [ ] Drop the now-unused imports from `offgate.go`. The file's remaining content uses only `context`; `fmt` and `internal/application/logging` were used solely by the deleted functions. Replace the import block at lines 3-8:
  ```go
  import (
  	"context"
  	"fmt"

  	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
  )
  ```
  with:
  ```go
  import (
  	"context"
  )
  ```

- [ ] Delete `reachesAny`. In `gobot/internal/application/parkedsensing/reach.go`, delete lines 286-303 — the comment beginning `// reachesAny reports whether ANY outstanding charting target is within this walker's reach` through the closing brace of the method. Verify the boundaries first:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    sed -n '284,305p' internal/application/parkedsensing/reach.go
  ```

- [ ] Fix the stale doc comment in `expansion.go:333`. Replace:
  ```go
  //     requestSeeds writes a SPARE want the buy queue funds, advanceOffGate raises
  //     explorer demand the autosizer funds, and claimSpares deletes a placement row,
  ```
  with:
  ```go
  //     requestSeeds writes a SPARE want the buy queue funds, and claimSpares deletes a placement row,
  ```

- [ ] Delete the test file.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/application/parkedsensing/offgate_test.go
  ```

- [ ] Run the package and see it green — the seven failures recorded in Task 1 are now gone.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/application/parkedsensing/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect `ok  	github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing`.

- [ ] Build, vet, format.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./internal/application/parkedsensing/ && gofmt -l internal/application/parkedsensing/
  ```
  Expect no output from any of the three.

- [ ] Commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git commit --no-verify -am "refactor(parkedsensing): delete the off-gate driver and the tests that pinned it (sp-mn3it)

advanceOffGate, dispatchExplorer, retractOffGateDemand and gateReach.reachesAny had
zero call sites after the previous commit — for FUNCTIONS a name grep settles deadness,
because a function can only be reached by its name. offgate_test.go goes with them: it
asserted the retired behaviour end to end." -- gobot/
  ```

---

## Task 3 — Delete `OffGatePorts`, its four interfaces, and every wiring seam above them

This is the compile-breaking task and the one the method warning is about. `OffGateSelector`, `OffGateDemandSink`, `ExplorerFinder` and `ExplorerDispatcher` are satisfied *implicitly* by adapters that never name them. Their deadness is established by finding who consumes `OffGatePorts` — and above it `ExpandPorts.OffGate` and `SensingEnginePorts.OffGate`.

**Files:**
- Modify: `gobot/internal/application/parkedsensing/offgate.go` (delete the whole file — after Task 2 it holds only the four interfaces, `OffGatePorts`, `wired()` and the ranking constants, all consumed by nothing)
- Modify: `gobot/internal/application/parkedsensing/expansion.go` (delete lines 218-220 — the `OffGate OffGatePorts` field and its doc comment; delete lines 305-314 — the three `OffGate*` report fields and their doc comment)
- Modify: `gobot/internal/application/scouting/commands/sensing_engine_ports.go` (delete lines 165-169 — the `OffGate parkedsensing.OffGatePorts` field and its doc comment; delete line 363 — `OffGate: p.OffGate,`)
- Modify: `gobot/cmd/spacetraders-daemon/main.go` (delete lines 331-334 — the four `sensingWiring` fields; delete lines 385-393 — the doc comment and the `OffGate:` literal; delete lines 1403-1406 — the four assignments in the `sensingWiring` literal)
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_stall.go` (line 100 — drop `|| t.expand.OffGateWarped > 0`; lines 53-57 — delete `stallReasonOffGateNoTarget` and its comment; line 180 — drop `rep.OffGateWarped > 0 ||`; lines 183-185 — delete the `OffGateDemanded` verdict branch)
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_stall_test.go` (delete the three purely off-gate table rows at lines 166-181; strip the `OffGateDemanded` field from the fourth row at lines 182-186; rewrite the table's doc comment at lines 151-157)
- Modify: `gobot/internal/application/parkedsensing/expansion_gate_read_test.go` (line 540 — a comment referencing `OffGatePorts`)
- Modify: `gobot/internal/application/parkedsensing/gateread.go` (line 184 — a comment referencing `OffGatePorts`)
- Delete: `gobot/internal/application/parkedsensing/offgate_retraction_test.go` (its job is done — see the step below for why, and Task 10 for its successor)

**Interfaces:**
- Consumes: `scoutingCmd.SensingEnginePorts`, `parkedsensing.ExpandPorts`, `parkedsensing.ExpandReport`
- Produces: removes `parkedsensing.OffGatePorts`, `parkedsensing.OffGateSelector`, `parkedsensing.OffGateDemandSink`, `parkedsensing.ExplorerFinder`, `parkedsensing.ExplorerDispatcher`, `ExpandPorts.OffGate`, `ExpandReport.OffGateDemanded/OffGateTarget/OffGateWarped`, `SensingEnginePorts.OffGate`, `stallReasonOffGateNoTarget`

**Steps:**

- [ ] **THE METHOD STEP — establish deadness by consumer of the port struct, not by name.** Do NOT grep the four interface names to decide they are dead; interfaces are satisfied implicitly and at least one adapter in this tree (`expansion.OffGateWarpTargetSelector`) satisfies `OffGateSelector` without ever naming it. Instead enumerate every consumer of the STRUCT that holds them, at each of the three levels:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    echo '--- level 1: OffGatePorts ---'   && grep -rn 'OffGatePorts'    --include='*.go' . ; \
    echo '--- level 2: ExpandPorts.OffGate / .OffGate field reads ---' && grep -rn '\.OffGate\b' --include='*.go' . ; \
    echo '--- level 3: report fields ---'  && grep -rn 'OffGateDemanded\|OffGateTarget\b\|OffGateWarped' --include='*.go' .
  ```
  Expect level 1 to hit only: the declaration in `offgate.go`, the field in `expansion.go:220`, the field in `sensing_engine_ports.go:169`, the literal in `main.go:388`, and two comments (`gateread.go:184`, `expansion_gate_read_test.go:540`). Expect level 2 to hit only `sensing_engine_ports.go:363`, `main.go:388`, and the test file created in Task 1. Expect level 3 to hit only `expansion.go:305-314` and `probe_sensing_stall.go` + its test. **Only that enumeration licenses the deletion.** If any hit falls outside this list, stop: the port struct still has a live consumer and the interfaces are not dead.

- [ ] Delete the interfaces file.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/application/parkedsensing/offgate.go
  ```

- [ ] Delete the Task-1 behavioural proof. **Why this is not vandalism:** it asserted "the sink is never called" through the `OffGateDemandSink` port, and that port no longer exists — an assertion about a mechanism cannot outlive the mechanism. Its job was to make the removal safe, and it did that: it was demonstrated RED against the live driver and GREEN after the cut. Its successor is the durable structural invariant in Task 10, which asserts the vocabulary is absent from the tree and therefore cannot be satisfied by anything.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/application/parkedsensing/offgate_retraction_test.go
  ```

- [ ] Remove the `ExpandPorts` field. In `gobot/internal/application/parkedsensing/expansion.go`, delete lines 218-220:
  ```go
  	// OffGate is the warp-expansion slice: the ports that raise explorer demand and
  	// warp an explorer past a sealed gate frontier. See offgate.go.
  	OffGate OffGatePorts
  ```
  Then fix the dangling cross-reference two lines above it — replace `// unaffected, in the same spirit as OffGatePorts.` (line 216) with `// unaffected: the pass does nothing and the rest of the tick still runs.`

- [ ] Remove the `ExpandReport` fields. In the same file, delete lines 305-314:
  ```go
  	// OffGateDemanded reports that the gate-reachable frontier was exhausted this tick and explorer
  	// demand was raised; OffGateTarget names the system selected to warp to (empty when none was
  	// reachable), and OffGateWarped counts warps actually dispatched.
  	//
  	// A warp is NOT charged against MaxExpansionActions either: a dispatch is one command handed to a
  	// background goroutine at most once per tick and gated on owning an idle explorer at all, so
  	// charging it would let the rarest action in the engine be crowded out by routine seed steps.
  	OffGateDemanded bool
  	OffGateTarget   string
  	OffGateWarped   int
  ```

- [ ] Remove the `SensingEnginePorts` field. In `gobot/internal/application/scouting/commands/sensing_engine_ports.go`, delete lines 165-169:
  ```go
  	// OffGate is the warp-expansion slice: the ports that raise explorer demand onto the fleet
  	// autosizer's buy bridge and warp an explorer past a sealed gate frontier. Deliberately NOT in
  	// the ready() check below — the gate passes must keep running on a daemon whose off-gate
  	// collaborators are absent, and the slice is inert until all four are present.
  	OffGate  parkedsensing.OffGatePorts
  ```
  and delete line 363, `		OffGate:     p.OffGate,`. Then fix the cross-reference at line 160 — replace `// Deliberately NOT in the ready() check below, for the same reason OffGate is not: the rest of` with `// Deliberately NOT in the ready() check below: the rest of`.

- [ ] Remove the daemon wiring. In `gobot/cmd/spacetraders-daemon/main.go` delete lines 331-334:
  ```go
  	offGateSelect *expansionAdapters.OffGateWarpTargetSelector
  	offGateDemand *expansionAdapters.ExplorerOffGateBridge
  	idleExplorer  *expansionAdapters.IdleExplorerPort
  	explorerWarp  *expansionAdapters.ExplorerWarpDispatcher
  ```
  (and the now-empty blank line that separated them from the block above), delete lines 385-393:
  ```go
  		// Off-gate warp expansion (write side of the explorer demand bridge). Wired
  		// unconditionally rather than behind a knob: a separately-armed expansion engine
  		// just sits dormant, so this one drives the live tick.
  		OffGate: parkedsensing.OffGatePorts{
  			Select:   s.offGateSelect,
  			Demand:   s.offGateDemand,
  			Explorer: s.idleExplorer,
  			Warp:     s.explorerWarp,
  		},
  ```
  and delete lines 1403-1406:
  ```go
  		offGateSelect: offGateSelector,
  		offGateDemand: explorerOffGateBridge,
  		idleExplorer:  idleExplorerPort,
  		explorerWarp:  explorerWarpDispatcher,
  ```
  Leave `offGateSelector`, `explorerOffGateBridge`, `idleExplorerPort` and `explorerWarpDispatcher` *constructed* at lines 1158-1180 for now — `explorerOffGateBridge` is still consumed at line 1200 by the autosizer handler and the other three would become declared-and-unused. Tasks 5 and 6 remove them.

- [ ] Make the three now-unused constructions compile. `offGateSelector`, `idleExplorerPort` and `explorerWarpDispatcher` are declared-and-unused local variables the moment their assignments are gone — that IS a Go compile error, so they must be deleted in this step, not deferred. In `main.go`, delete lines 1160-1180 (the `// OFF-GATE WARP EXPANSION, write side.` comment block plus the three constructions `offGateSelector := ...`, `idleExplorerPort := ...`, `explorerWarpDispatcher := ...`). Keep `explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()` at line 1158 and its comment — line 1200 still consumes it.

- [ ] Remove the stall-verdict reads. In `gobot/internal/application/scouting/commands/probe_sensing_stall.go`:
  - delete lines 53-57 (the `stallReasonOffGateNoTarget` comment and const);
  - at line 100 replace `		t.expand.Actions > 0 || t.expand.Discovered > 0 || t.expand.OffGateWarped > 0` with `		t.expand.Actions > 0 || t.expand.Discovered > 0`;
  - at line 92 replace the doc-comment fragment `// bought or re-tasked, a claim reaped, a placement advanced, a frontier system discovered, a warp` + `// dispatched. The rotation size is deliberately NOT an effect — a steady rotation is what an idle` with `// bought or re-tasked, a claim reaped, a placement advanced, a frontier system discovered. The` + `// rotation size is deliberately NOT an effect — a steady rotation is what an idle`;
  - at line 180 replace `	if rep.Actions > 0 || rep.Discovered > 0 || rep.OffGateWarped > 0 || rep.SeedsRequested > 0 || rep.SeedsClaimed > 0 || rep.MarketsFound > 0 {` with `	if rep.Actions > 0 || rep.Discovered > 0 || rep.SeedsRequested > 0 || rep.SeedsClaimed > 0 || rep.MarketsFound > 0 {`;
  - delete lines 183-185:
    ```go
    	if rep.OffGateDemanded && rep.OffGateTarget == "" {
    		return health.TickBlocked(stallReasonOffGateNoTarget,
    			"the gate-reachable frontier is exhausted and NO warp target could be selected — the fleet is sealed in the systems it already holds, and every layer reports this as '0 discovered'")
    	}
    ```
  - and fix the function's doc comment at lines 169-170, replacing `// BEFORE idle because "demand raised, no target found" is the exact shape that reads as a fully` + `// charted galaxy while a jump gate sits unread.` with `// A sealed gate frontier is now a PERMANENT, DELIBERATE state — the gate network is the reachable` + `// universe (sp-mn3it) — so it reads idle rather than blocked: there is no action left to take.`

  **Why this is correct and not a lost alarm:** `off_gate_no_target` fired when the gate-reachable frontier was exhausted *and* no warp target could be selected. With off-gate warp retired, "the gate network is the whole reachable universe" is the accepted, deliberate steady state (spec: *"Any system not gate-connected is unreachable by design"*). A verdict that reported that permanent condition as BLOCKED on every tick would be a standing false alarm, which is worse than idle.

- [ ] Remove the stall test rows. Read the region first so the row boundaries are exact:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    sed -n '150,190p' internal/application/scouting/commands/probe_sensing_stall_test.go
  ```
  In `gobot/internal/application/scouting/commands/probe_sensing_stall_test.go`:
  - delete lines 166-181 — the three purely off-gate rows (`"sealed pocket: demand raised, no warp target — the 33-system region behind an unread gate"`, `"off-gate demand WITH a target is not a stall — the fleet has somewhere to go"`, `"a dispatched warp is progress"`);
  - keep the fourth row (lines 182-186) but strip the retired field and rename it, replacing
    ```go
    		{
    			name:       "charting progress outranks a failed selector — a moving frontier is not sealed",
    			rep:        parkedsensing.ExpandReport{Discovered: 2, OffGateDemanded: true},
    			wantStatus: health.StallProgress,
    		},
    ```
    with
    ```go
    		{
    			name:       "charting progress is progress — a moving frontier is not idle",
    			rep:        parkedsensing.ExpandReport{Discovered: 2},
    			wantStatus: health.StallProgress,
    		},
    ```
  - rewrite the table's doc comment at lines 151-157, replacing the block that begins `// THE OFF-GATE PRODUCTION FAILURE, and the rest of the expansion pass's verdict table.` and ends `// expansion engine, which owns its own tests, rather than this mapping.` with:
    ```go
    // The expansion pass's verdict table.
    //
    // Driven against the pass's REPORT rather than through a synthesised 400-line expansion fixture:
    // the report is the DTO the engine hands the coordinator, and the behaviour under test is the
    // coordinator's projection of it, not the expansion engine — which owns its own tests.
    ```

- [ ] Fix the two remaining comment cross-references.
  - `gobot/internal/application/parkedsensing/gateread.go:184`: replace `// per-system mapping sweep. That is the same contract OffGatePorts carries.` with `// per-system mapping sweep.`
  - `gobot/internal/application/parkedsensing/expansion_gate_read_test.go:540`: replace `// the pass degrades to doing nothing rather than panicking — the same contract OffGatePorts carries.` with `// the pass degrades to doing nothing rather than panicking.`

- [ ] Build and vet the whole module — this is the step that catches every seam the greps missed. `go build` skips `_test.go`, so vet is mandatory here.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./...
  ```
  Expect no output from either.

- [ ] Run the three affected packages.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/application/parkedsensing/ ./internal/application/scouting/... ./cmd/spacetraders-daemon/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect three `ok` lines and no `FAIL`.

- [ ] Format and commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && gofmt -l . && \
    cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git commit --no-verify -am "refactor(parkedsensing,scouting,daemon): delete OffGatePorts, its four interfaces and every wiring seam (sp-mn3it)

Deadness established by enumerating consumers of the PORT STRUCT at all three levels
(OffGatePorts, ExpandPorts.OffGate, SensingEnginePorts.OffGate) — NOT by grepping the
interface names, which would be unsound: interfaces are satisfied implicitly and
expansion.OffGateWarpTargetSelector satisfies OffGateSelector without naming it.

Also drops the off_gate_no_target stall verdict. A sealed gate frontier is now a
permanent deliberate state, so reporting it BLOCKED every tick would be a standing
false alarm; idle is the honest verdict." -- gobot/
  ```

---

## Task 4 — Delete `offgate_types.go`, including the two orphaned declarations

`OffGateTargetSelector` (line 48) and `ShipyardCoverageReader` (line 57) were declared and never wired — they document a trigger that was never implemented. They are **not** the live ports: the live selector was `OffGateSelector` in `offgate.go`, already gone.

**Files:**
- Delete: `gobot/internal/application/parkedsensing/offgate_types.go` (71 lines: `OffGateTarget`, `OffGateSelectionParams`, `OffGateTargetSelector`, `ShipyardCoverageReader`, `OffGateDemandSignal`)

**Interfaces:**
- Consumes: nothing
- Produces: removes `parkedsensing.OffGateTarget`, `parkedsensing.OffGateSelectionParams`, `parkedsensing.OffGateTargetSelector`, `parkedsensing.ShipyardCoverageReader`, `parkedsensing.OffGateDemandSignal`

**Steps:**

- [ ] **Apply the method warning to the two orphans specifically.** `OffGateTargetSelector` and `ShipyardCoverageReader` are interfaces, so a name grep cannot prove nothing satisfies them — but for these two the *inverse* argument settles it, and it must be made explicitly: an implicitly-satisfied interface is only reachable if some value is assigned to a variable, field or parameter **of that interface type**. Enumerate every such site:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'OffGateTargetSelector\|ShipyardCoverageReader\|GateShipyardsScanExhausted' --include='*.go' .
  ```
  Expect hits ONLY at `internal/application/parkedsensing/offgate_types.go:44-59` — the declarations and their doc comments. **Zero fields, zero parameters, zero variables of these types anywhere.** With no site of the interface type, no value can ever be assigned to one, so no implementation is reachable regardless of what shapes exist. That is the sound form of the argument; "no name references, therefore dead" is not.

- [ ] Enumerate the consumers of the three remaining value types in the file. These are structs, not interfaces, so a name grep IS sound for them.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'OffGateTarget\b\|OffGateSelectionParams\|OffGateDemandSignal' --include='*.go' .
  ```
  Expect hits in `internal/application/parkedsensing/offgate_types.go` (declarations) and in `internal/adapters/expansion/{off_gate_target.go,off_gate_target_test.go,explorer_dispatch_adapter.go,explorer_offgate_bridge.go}` — the adapters, which Tasks 5 and 6 delete. Those adapter references are the reason this task cannot be reordered after 5/6 is complete without churn, and the reason the file is deleted here rather than earlier.

- [ ] Delete the file. The `internal/adapters/expansion` package will not compile until Tasks 5 and 6 land — that is expected and is why the compile gate below is scoped.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/application/parkedsensing/offgate_types.go
  ```

- [ ] Prove the parkedsensing package itself is clean and self-contained.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./internal/application/parkedsensing/... && \
    go test ./internal/application/parkedsensing/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect no build output and one `ok` line.

- [ ] Confirm the only remaining breakage is the four adapters Tasks 5 and 6 delete — nothing else.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... 2>&1 | grep -E '^[^ ]' | sort -u
  ```
  Expect errors reported ONLY for files under `internal/adapters/expansion/` (`off_gate_target.go`, `explorer_dispatch_adapter.go`, `explorer_offgate_bridge.go`). Any other package named here is a seam Task 3 missed — stop and fix it before continuing.

- [ ] Do NOT commit yet — the tree does not build. Task 5 completes the compile.

---

## Task 5 — Delete the explorer-only adapters, each verified individually

Three adapters go here; the fourth (the bridge) goes in Task 6 because it drags the autosizer read side with it. **Verify each on its own before deleting it — do not batch.**

**Files:**
- Delete: `gobot/internal/adapters/expansion/idle_explorer_port.go` (71 lines)
- Delete: `gobot/internal/adapters/expansion/explorer_dispatch_adapter.go` (140 lines)
- Delete: `gobot/internal/adapters/expansion/off_gate_target.go` (214 lines)
- Delete: `gobot/internal/adapters/expansion/off_gate_target_test.go` (234 lines)

**Interfaces:**
- Consumes: `ship.RouteExecutor.ExecuteWarpRoute` (via `warpRouteRunner`), `navigation.ShipRepository`, `gategraph` adjacency
- Produces: removes `expansion.IdleExplorerPort` + `NewIdleExplorerPort(ships idleShipReader) *IdleExplorerPort`, `expansion.ExplorerWarpDispatcher` + `NewExplorerWarpDispatcher(routes warpRouteRunner, ships shipBySymbolReader, arrivals arrivalWaypointResolver) *ExplorerWarpDispatcher`, `expansion.OffGateWarpTargetSelector` + `NewOffGateWarpTargetSelector(universe UniverseSystemsProvider, gateGraph gateAdjacencyReader) *OffGateWarpTargetSelector`, `expansion.UniverseSystemsProvider`

**Steps:**

- [ ] **Verify `idle_explorer_port.go` individually.** Its only consumer was `main.go:1179`, removed in Task 3.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'IdleExplorerPort\|NewIdleExplorerPort\|idleShipReader\|explorerDedicatedFleet' --include='*.go' .
  ```
  Expect hits only inside `internal/adapters/expansion/idle_explorer_port.go`. Note it is the sole consumer of `hullbuy.DedicatedFleet(hullbuy.HullClassExplorer)` outside `hullbuy` itself — deleting it is what makes that constant inert (and deferrable to slice 4).

- [ ] Delete it and confirm the package's other files do not reference it.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/adapters/expansion/idle_explorer_port.go && \
    grep -rn 'IdleExplorer' --include='*.go' internal/adapters/ ; echo "exit=$?"
  ```
  Expect `exit=1` (no matches).

- [ ] **Verify `explorer_dispatch_adapter.go` individually.** Its only consumer was `main.go:1180`, removed in Task 3. It is the one deletion that touches the shared warp stack's caller set, so check what it consumes before removing it.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'ExplorerWarpDispatcher\|NewExplorerWarpDispatcher\|warpRouteRunner\|arrivalWaypointResolver\|shipBySymbolReader' --include='*.go' . && \
    echo '--- what it calls into the shared warp stack ---' && \
    grep -n 'ExecuteWarpRoute\|routes\.' internal/adapters/expansion/explorer_dispatch_adapter.go
  ```
  Expect the first grep to hit only inside `internal/adapters/expansion/explorer_dispatch_adapter.go`. The second confirms it is a *caller* of `ExecuteWarpRoute`, not a definer.

- [ ] Delete it, then immediately prove the warp stack it called survives with its other caller intact.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/adapters/expansion/explorer_dispatch_adapter.go && \
    grep -rn 'ExecuteWarpRoute\|ExecuteWarpLeg' --include='*.go' . | grep -v '_test.go'
  ```
  Expect the definitions in `internal/application/ship/route_executor_warp.go` and at least one live non-test caller reaching them from `internal/application/ship/commands/navigation/warp_ship.go` (the manual verb). **If zero non-test callers remain, stop** — the retirement has removed both callers, which it must not (Task 9 is the full proof, this is the early tripwire).

- [ ] **Verify `off_gate_target.go` individually.** Its only consumer was `main.go:1175`, removed in Task 3.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'OffGateWarpTargetSelector\|NewOffGateWarpTargetSelector\|UniverseSystemsProvider\|betterOffGateTarget\|frontierEdges\|nearestEdgeWarp\|explorationValue' --include='*.go' .
  ```
  Expect hits only inside `internal/adapters/expansion/off_gate_target.go` and `off_gate_target_test.go`. Note `gateAdjacencyReader` — check whether it is shared with another file in the package before you delete anything that declares it:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'gateAdjacencyReader' --include='*.go' internal/adapters/expansion/
  ```
  If it is declared in `off_gate_target.go` but used by another file in the package, move the declaration to that file rather than deleting it.

- [ ] Delete the selector and its test.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/adapters/expansion/off_gate_target.go internal/adapters/expansion/off_gate_target_test.go
  ```

- [ ] Confirm the only remaining breakage is the bridge, which Task 6 removes.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... 2>&1 | grep -E '^[^ ]' | sort -u
  ```
  Expect errors reported ONLY for `internal/adapters/expansion/explorer_offgate_bridge.go` (it imports `parkedsensing` for the types deleted in Task 4) and `internal/adapters/expansion/universe_systems.go` if it referenced anything from the deleted selector. Anything else is a missed seam — stop.

- [ ] Do NOT commit yet — the tree does not build. Task 6 completes the compile.

---

## Task 6 — Delete the latching bridge and the autosizer read chain it forces

`ExplorerOffGateBridge` is the bridge itself. Its read side (`ExplorerDemand`) is consumed by `fleetCmd.NewExplorerDemandProvider`, which is registered from `NewFleetAutosizerCoordinatorHandler`'s `offGateDemand` parameter. Removing the bridge makes that parameter unsatisfiable — the compiler, not a scope choice, drags the whole read chain in with it.

**Files:**
- Delete: `gobot/internal/adapters/expansion/explorer_offgate_bridge.go` (59 lines)
- Delete: `gobot/internal/application/fleet/commands/fleet_autosizer_explorer.go` (116 lines)
- Delete: `gobot/internal/application/fleet/commands/fleet_autosizer_explorer_test.go` (143 lines)
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_explorer_wiring_test.go` (delete `TestExplorer_ClassDisabled_OptInDefaultOff`, `TestExplorer_ResolveDefaults_NothingBootArms` and the `spyExplorerProvider` fixture — the whole file, 103 lines, if nothing else remains after)
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_ports.go` (delete line 44 `offGateDemand fleetCmd.OffGateDemandSource,`; delete lines 64-69 — the explorer comment block and the `AddDemandProvider` registration; update the constructor doc comment at line 33-34)
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_demand_ports.go` (delete lines 124-134 — the doc comment, the `autosizerExplorerFleetSource` type and its `ExplorerCount` method; `countShips` at line 136 is shared and stays)
- Modify: `gobot/cmd/spacetraders-daemon/main.go` (delete lines 1152-1158 — the bridge comment block and `explorerOffGateBridge := ...`; delete line 1200 — the `explorerOffGateBridge,` argument)

**Interfaces:**
- Consumes: `fleetCmd.RunFleetAutosizerCoordinatorHandler.AddDemandProvider(p ClassDemandProvider)`
- Produces: `NewFleetAutosizerCoordinatorHandler(server *DaemonServer, apiClient *api.SpaceTradersClient, ledgerTreasury *persistence.LedgerTreasury, shipRepo navigation.ShipRepository, med common.Mediator, waypointRepo *persistence.GormWaypointRepository, eventStore captain.EventStore, marketRepo market.MarketRepository, scannedYards scannedYardRanker, heavyYards heavyYardInventory) *fleetCmd.RunFleetAutosizerCoordinatorHandler` — **one parameter shorter**. Removes `expansion.ExplorerOffGateBridge`, `fleetCmd.OffGateDemandSource`, `fleetCmd.ExplorerFleetSource`, `fleetCmd.ExplorerDemandProvider`, `fleetCmd.NewExplorerDemandProvider`, `grpc.autosizerExplorerFleetSource`

**Steps:**

- [ ] **Verify the bridge individually, by consumer of the object — both sides.** The bridge plays two ports at once (write via `EmitOffGateDemand`, read via `ExplorerDemand`), so both must come up empty-or-doomed.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    echo '--- the object ---'    && grep -rn 'ExplorerOffGateBridge' --include='*.go' . ; \
    echo '--- write side ---'    && grep -rn 'EmitOffGateDemand'     --include='*.go' . ; \
    echo '--- read side ---'     && grep -rn 'ExplorerDemand\b\|OffGateDemandSource' --include='*.go' .
  ```
  Expect: the object at `internal/adapters/expansion/explorer_offgate_bridge.go` and `cmd/spacetraders-daemon/main.go:1158,1200` only; the **write side at zero call sites** (Task 1 removed the only one — this is the retraction proven structurally); the read side at `fleet_autosizer_explorer.go`, `fleet_autosizer_ports.go:44,69`, `explorer_offgate_bridge.go:51` and the autosizer explorer tests. That read-side set is exactly what this task removes.

- [ ] Delete the bridge and the demand provider.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/adapters/expansion/explorer_offgate_bridge.go \
       internal/application/fleet/commands/fleet_autosizer_explorer.go \
       internal/application/fleet/commands/fleet_autosizer_explorer_test.go \
       internal/application/fleet/commands/fleet_autosizer_explorer_wiring_test.go
  ```

- [ ] Remove the constructor parameter and the registration. In `gobot/internal/adapters/grpc/fleet_autosizer_ports.go`, delete line 44:
  ```go
  	offGateDemand fleetCmd.OffGateDemandSource,
  ```
  delete lines 64-69:
  ```go
  	// Explorer class (slice C): reads slice-B off-gate demand through the cross-coordinator
  	// bridge (offGateDemand) and the live explorer-pool count (dedicate-at-purchase "explorer" fleet).
  	// DORMANT until BOTH armed (explorer_hulls_enabled, default off — classDisabled skips it otherwise)
  	// AND the frontier raises off-gate demand into the bridge, so registering it here changes no live
  	// behaviour and nothing auto-buys. The frontier warps the bought hull (SetExplorerDispatchPort).
  	h.AddDemandProvider(fleetCmd.NewExplorerDemandProvider(offGateDemand, &autosizerExplorerFleetSource{shipRepo: shipRepo}))
  ```
  and update the doc comment at lines 33-34, replacing `// concrete port to the daemon's live collaborators and registering the light + heavy demand` + `// providers (and the opt-in explorer class).` with `// concrete port to the daemon's live collaborators and registering the light + heavy demand providers.`

- [ ] Remove the explorer fleet source. Confirm the extent first:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    sed -n '122,140p' internal/adapters/grpc/fleet_autosizer_demand_ports.go
  ```
  In `gobot/internal/adapters/grpc/fleet_autosizer_demand_ports.go`, delete lines 124-134:
  ```go
  // autosizerExplorerFleetSource counts the player's explorer-dedicated hulls (DedicatedFleet
  // "explorer", stamped by dedicate-at-purchase). It is the hard-cap basis and the shortfall's Current
  // the ExplorerDemandProvider reads; a read failure fails the class CLOSED (an unknowable pool must
  // never buy, lest it breach the hard cap of 1).
  type autosizerExplorerFleetSource struct{ shipRepo navigation.ShipRepository }

  func (s *autosizerExplorerFleetSource) ExplorerCount(ctx context.Context, playerID int) (int, error) {
  	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool {
  		return sh.DedicatedFleet() == "explorer"
  	})
  }
  ```
  **Keep `countShips` (line 136)** — it is the shared helper the light and heavy fleet sources use too.

- [ ] Remove the daemon construction and the argument. In `gobot/cmd/spacetraders-daemon/main.go` delete lines 1152-1158 (the comment block beginning `// The cross-coordinator off-gate demand bridge the FLEET autosizer's explorer BUY path` through `explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()`), and delete line 1200:
  ```go
  		explorerOffGateBridge, // Explorer demand provider reads off-gate demand through this bridge
  ```

- [ ] The tree should now build for the first time since Task 4. Build and vet the whole module — vet is what catches the call-site arity break `go build` would miss in `_test.go`.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./...
  ```
  Expect no output from either. If vet reports a `NewFleetAutosizerCoordinatorHandler` arity error in a test, fix that call site — the signature lost one parameter.

- [ ] Run the affected packages.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/adapters/expansion/ ./internal/adapters/grpc/ ./internal/application/fleet/... ./cmd/spacetraders-daemon/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect `ok` for each and no `FAIL`.

- [ ] Format and commit Tasks 4–6 together (they were one compile unit).
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && gofmt -l . && \
    cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git add -A gobot/ && \
    git commit --no-verify -m "refactor(expansion,fleet,grpc): delete offgate_types, the explorer-only adapters and the latching bridge (sp-mn3it)

offgate_types.go incl. the two ORPHANED declarations — OffGateTargetSelector and
ShipyardCoverageReader. Their deadness is established soundly: there is no field,
parameter or variable ANYWHERE of either interface type, so no value can ever be
assigned to one and no implementation is reachable, whatever shapes exist. 'No name
references, therefore dead' would not have been a valid argument.

Each adapter verified on its own before deletion: idle_explorer_port, then
explorer_dispatch_adapter (with an immediate check that ExecuteWarpRoute still has a
live non-test caller — the manual 'ship warp' verb), then off_gate_target.

Deleting ExplorerOffGateBridge FORCES the read chain: the write side had zero call
sites (that is the retraction, proven structurally), and the compiler takes the
offGateDemand parameter, ExplorerDemandProvider and autosizerExplorerFleetSource with
it. NewFleetAutosizerCoordinatorHandler is one parameter shorter." -- gobot/
  ```

---

## Task 7 — Delete the explorer class config, tune keys and autosizer class arms

Leaving `autosizer_explorer_hulls_enabled` as an accepted tune key would advertise an arming seam for a class that no longer exists: an operator could set it and get silence. It goes with the class.

**Files:**
- Modify: `gobot/internal/adapters/grpc/container_ops_fleet_autosizer.go` (delete the 5 keys at lines 83-87; delete the write block at lines 160-176; delete the read block at lines 214-218)
- Modify: `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator.go` (delete the defaults at lines 45-52; delete `DemandParams.ExplorerHullsEnabled`/`MaxExplorerHulls` at lines 66-71; delete the command fields at lines 124-131; delete the `HullClassExplorer` arm of `classDisabled` at lines 319-322)
- Modify: `gobot/internal/application/fleet/commands/run_fleet_autosizer_reconcile.go` (delete the run-config fields at lines 44-49; delete the copy block at lines 71-75; delete the defaults at lines 111-125; delete the `DemandParams` fills at lines 208-209)
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_act.go` (delete the `HullClassExplorer` arm of `classGuardConfig` at lines 201-206; trim the doc comment at lines 189-192)
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_types.go` (delete the `HullClassExplorer` alias and its doc comment at lines 27-31; trim line 10)
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_guards.go` (prose only — lines 34, 53-54, 247)
- Modify: `gobot/internal/infrastructure/config/fleet_autosizer.go` (delete the explorer block at lines 99-121)
- Modify: `gobot/internal/adapters/grpc/container_ops_tune.go` (line 141 — edit the prose, **keep the `expansion_enabled` key**)

**Interfaces:**
- Consumes: `configReader.OptionalBool/OptionalInt/OptionalString`
- Produces: `DemandParams{LightRotationSlots float64}` — the other two fields removed. `RunFleetAutosizerCoordinatorCommand` and `autosizerRunConfig` each lose 5 explorer fields. `config.FleetAutosizerConfig` loses 5 mapstructure fields.

**Steps:**

- [ ] Confirm no checked-in YAML sets any explorer knob — if one did, deleting the field would silently change a live fleet's config rather than removing a dead one.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    grep -rn 'explorer_hulls_enabled\|fleet_ceiling_explorer\|explorer_treasury_pct_per_purchase\|max_price_explorer\|ship_type_explorer' --include='*.yaml' --include='*.yml' . ; echo "exit=$?"
  ```
  Expect `exit=1` (no matches). If any file matches, stop and report — that is a live arming the bead's evidence did not anticipate.

- [ ] Remove the 5 tune keys. In `gobot/internal/adapters/grpc/container_ops_fleet_autosizer.go`, delete lines 83-87 from `fleetAutosizerConfigKeys`:
  ```go
  	"autosizer_explorer_hulls_enabled",
  	"autosizer_fleet_ceiling_explorer",
  	"autosizer_explorer_treasury_pct_per_purchase",
  	"autosizer_max_price_explorer",
  	"autosizer_ship_type_explorer",
  ```

- [ ] Remove the config write block. In the same file, delete lines 160-176 — the comment `// Explorer class. The opt-in arming bool is written ONLY when true ...` and the five `if fa.*Explorer* { config[...] = ... }` blocks.

- [ ] Remove the config read block. In the same file, delete lines 214-218:
  ```go
  		ExplorerHullsEnabled:           cfg.OptionalBool("autosizer_explorer_hulls_enabled"),
  		FleetCeilingExplorer:           cfg.OptionalInt("autosizer_fleet_ceiling_explorer", 0),
  		ExplorerTreasuryPctPerPurchase: cfg.OptionalInt("autosizer_explorer_treasury_pct_per_purchase", 0),
  		MaxPriceExplorer:               int64(cfg.OptionalInt("autosizer_max_price_explorer", 0)),
  		ShipTypeExplorer:               cfg.OptionalString("autosizer_ship_type_explorer"),
  ```

- [ ] Remove the coordinator defaults. In `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator.go`, delete lines 45-52:
  ```go
  	// Explorer class. Opt-IN (default OFF, arming knob). The HARD CAP is 1; the
  	// PRICE CEILING defaults to ~819k SHIP_EXPLORER + a premium (a REAL default, never 0=off — the
  	// explorer's price ceiling is a required guard); the 25%-treasury big-ticket affordability rule
  	// applies (the explorer buys an ~819k hull).
  	defaultFleetCeilingExplorer           = 1
  	defaultExplorerTreasuryPctPerPurchase = 25
  	defaultMaxPriceExplorer               = 900000
  	defaultShipTypeExplorer               = "SHIP_EXPLORER"
  ```

- [ ] Remove the `DemandParams` fields. In the same file, delete lines 66-71:
  ```go
  	// ExplorerHullsEnabled ARMS the explorer class. Default false (opt-in): when false the
  	// explorer provider emits ZERO demand unconditionally, so a bare deploy buys no explorer.
  	ExplorerHullsEnabled bool
  	// MaxExplorerHulls is the explorer HARD CAP (the class fleet ceiling, default 1): the provider
  	// never wants more than this regardless of the off-gate signal's count.
  	MaxExplorerHulls int
  ```

- [ ] Remove the command fields. In the same file, delete lines 124-131:
  ```go
  	// Explorer class. ExplorerHullsEnabled is the opt-IN arming knob (default OFF
  	// — nothing boot-arms it). FleetCeilingExplorer is the HARD CAP (default 1); MaxPriceExplorer is
  	// the price ceiling (default ~819k+premium); ExplorerTreasuryPctPerPurchase is the 25% rule.
  	ExplorerHullsEnabled           bool
  	FleetCeilingExplorer           int
  	ExplorerTreasuryPctPerPurchase int
  	MaxPriceExplorer               int64
  	ShipTypeExplorer               string
  ```

- [ ] Remove the `classDisabled` arm. In the same file, replace lines 313-329:
  ```go
  // classDisabled reports whether a class is frozen by config. Lights/heavies are LIVE BY DEFAULT
  // and unconditionally run; explorer is opt-IN (only runs when armed).
  func (c autosizerRunConfig) classDisabled(class HullClass) bool {
  	switch class {
  	case HullClassLight, HullClassHeavy:
  		return false
  	case HullClassExplorer:
  		// Opt-IN arming: the explorer class runs ONLY when explicitly armed, so a bare
  		// deploy skips it entirely and buys no ~819k ROI-exempt hull.
  		return !c.ExplorerHullsEnabled
  	default:
  		// HullClassContractDelivery falls here — never sized by the autosizer (the dedicated scaler
  		// owns it). "unknown class: never act".
  		return true
  	}
  }
  ```
  with:
  ```go
  // classDisabled reports whether a class is frozen by config. Lights and heavies are LIVE BY
  // DEFAULT and unconditionally run; every other class is unknown here and never acts.
  func (c autosizerRunConfig) classDisabled(class HullClass) bool {
  	switch class {
  	case HullClassLight, HullClassHeavy:
  		return false
  	default:
  		// HullClassContractDelivery falls here — never sized by the autosizer (the dedicated scaler
  		// owns it). "unknown class: never act".
  		return true
  	}
  }
  ```
  Note the receiver `c` is now unused by the body; leave it — the method is on `autosizerRunConfig` by contract and `go vet` does not flag an unused receiver.

- [ ] Remove the reconcile fields. In `gobot/internal/application/fleet/commands/run_fleet_autosizer_reconcile.go`, delete lines 44-49:
  ```go
  	// Explorer class.
  	ExplorerHullsEnabled           bool
  	FleetCeilingExplorer           int
  	ExplorerTreasuryPctPerPurchase int
  	MaxPriceExplorer               int64
  	ShipTypeExplorer               string
  ```
  delete lines 71-75:
  ```go
  		ExplorerHullsEnabled:           cmd.ExplorerHullsEnabled,
  		FleetCeilingExplorer:           cmd.FleetCeilingExplorer,
  		ExplorerTreasuryPctPerPurchase: cmd.ExplorerTreasuryPctPerPurchase,
  		MaxPriceExplorer:               cmd.MaxPriceExplorer,
  		ShipTypeExplorer:               cmd.ShipTypeExplorer,
  ```
  delete lines 111-125 (the `// Explorer defaults.` comment and the four `if c.*Explorer* <= 0 / == ""` blocks), and replace lines 206-210:
  ```go
  	params := DemandParams{
  		LightRotationSlots:   cfg.LightRotationSlots,
  		ExplorerHullsEnabled: cfg.ExplorerHullsEnabled,
  		MaxExplorerHulls:     cfg.FleetCeilingExplorer,
  	}
  ```
  with:
  ```go
  	params := DemandParams{
  		LightRotationSlots: cfg.LightRotationSlots,
  	}
  ```

- [ ] Remove the `classGuardConfig` arm. In `gobot/internal/application/fleet/commands/fleet_autosizer_act.go`, delete lines 201-206:
  ```go
  	case HullClassExplorer:
  		// The explorer's ship type (SHIP_EXPLORER), its price ceiling (~819k+premium — a REAL cap,
  		// not 0=off), and the 25% big-ticket affordability rule. The realized-$/hr payback exemption
  		// is applied class-gated INSIDE EvaluateGuards, not here — every knob returned here is a REAL
  		// guard bound the explorer must still clear.
  		return cfg.ShipTypeExplorer, cfg.MaxPriceExplorer, cfg.ExplorerTreasuryPctPerPurchase
  ```
  and replace the doc comment at lines 188-192:
  ```go
  // classGuardConfig resolves the per-class guard knobs from the run config.
  //
  // There is no class ceiling here. The explorer's HARD CAP of 1 is not missing: that cap lives
  // in ExplorerDemandProvider, which clamps its want to MaxExplorerHulls, so the class is capped
  // by its demand.
  ```
  with:
  ```go
  // classGuardConfig resolves the per-class guard knobs from the run config. There is no class
  // ceiling here: a class is bounded by its demand, its affordability and its price cap.
  ```

- [ ] Remove the class alias. In `gobot/internal/application/fleet/commands/fleet_autosizer_types.go`, delete lines 27-31:
  ```go
  	// HullClassExplorer is sized to slice-B off-gate demand and runs the SAME guard stack as every
  	// other class — there is no class-gated carve-out. Its ~819k spend is bounded by the demand
  	// gate, a HARD CAP of 1 (the class fleet ceiling) and a price ceiling. Opt-IN
  	// (explorer_hulls_enabled, default OFF) and double-gated, so a bare deploy buys nothing.
  	HullClassExplorer = hullbuy.HullClassExplorer
  ```
  and at line 10 replace `// N pluggable demand providers (lights, heavies, explorer) — the vdld pluggable-provider idiom.` with `// N pluggable demand providers (lights, heavies) — the vdld pluggable-provider idiom.`

- [ ] Trim the guard-stack prose. In `gobot/internal/application/fleet/commands/fleet_autosizer_guards.go`:
  - line 34: replace `// saturation — the next hull flies a fresh lane). explorer_exempt existed solely to cancel those two` + `// for one class and could never itself block. The autosizer therefore no longer forms an opinion on` with `// saturation — the next hull flies a fresh lane). The autosizer therefore no longer forms an opinion on`;
  - lines 53-54: delete the two-line `//	explorer    — its HARD CAP of 1, enforced in the demand provider itself (ExplorerDemandProvider` / `//	              clamps want to MaxExplorerHulls), so the class stays capped without this guard` entry;
  - line 247: replace `// worker pool and the explorer for reasons that have nothing to do with them.` with `// worker pool for reasons that have nothing to do with it.`

- [ ] Remove the infrastructure config block. In `gobot/internal/infrastructure/config/fleet_autosizer.go`, delete lines 99-121 — the `// --- explorer hull class (sp-a3yn slice C of sp-4imi) ---` banner through the `ShipTypeExplorer string \`mapstructure:"ship_type_explorer"\`` field, inclusive of all interleaved doc comments. Also update line 28, replacing `// FleetCeilingExplorer SURVIVES below: it is the explorer's demand-side hard cap, not a guard input.` with nothing (delete the line).

- [ ] Edit the tune-key prose and **keep the key**. In `gobot/internal/adapters/grpc/container_ops_tune.go:141`, inside the `expansion_enabled` description string, replace the fragment `no probe for a coverage placement (operation_type='sensing coverage'), no charting seed, no off-gate explorer (the autosizer's ~769k hull).` with `no probe for a coverage placement (operation_type='sensing coverage'), no charting seed.` Leave the rest of the description and the key itself untouched — `expansion_enabled` is the live probe-spending switch and is not part of this retirement.

- [ ] Edit the heartbeat prose. In `gobot/internal/application/scouting/commands/probe_sensing_heartbeat.go:421`, replace `		return summary + " (spending paused: no seed purchase, no explorer demand)"` with `		return summary + " (spending paused: no seed purchase)"`.

- [ ] Edit the sensing-config prose. In `gobot/internal/application/scouting/commands/probe_sensing_config.go:100`, replace `	// buy (a charting seed from the buy queue, an explorer from the autosizer), and` with `	// buy (a charting seed from the buy queue), and`.

- [ ] Build, vet, format, test the fleet + grpc + config + scouting packages.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./... && gofmt -l . && \
    go test ./internal/application/fleet/... ./internal/adapters/grpc/ ./internal/infrastructure/config/ ./internal/application/scouting/... 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect no output from build/vet/gofmt and `ok` for every package.

- [ ] Confirm the deferral held: `hullbuy.HullClassExplorer` is now inert but still declared, and its own package test still passes.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'HullClassExplorer' --include='*.go' . && \
    go test ./internal/domain/hullbuy/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:'
  ```
  Expect hits ONLY in `internal/domain/hullbuy/hull_class.go` (lines 21-23, 47-48) and `internal/domain/hullbuy/hull_class_test.go` (lines 21, 50), plus `ok`. That is the deferred surface, exactly as scoped — nothing outside `hullbuy` references it, so it can no longer reach a purchase.

- [ ] Commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git commit --no-verify -am "refactor(fleet,grpc,config): delete the explorer class config, its 5 tune keys and the autosizer class arms (sp-mn3it)

autosizer_explorer_hulls_enabled and its four siblings are gone from
fleetAutosizerConfigKeys, from the launch-config write/read, from
FleetAutosizerConfig, from RunFleetAutosizerCoordinatorCommand,
autosizerRunConfig, DemandParams and the coordinator defaults. Leaving an arming
key for a class that no longer exists would advertise a seam an operator could set
and get silence from. Verified beforehand that no checked-in YAML sets any of them.

classDisabled and classGuardConfig lose their HullClassExplorer arms; both now fall
to the 'unknown class: never act' default.

DEFERRED to the autosizer deletion: hullbuy.HullClassExplorer and the 'explorer' arm
of hullbuy.DedicatedFleet. hullbuy is a shared vocabulary owned by no coordinator and
imported by the dedicated contract scaler too; after this commit the constant is inert
(zero references outside its own package) so it cannot reach a purchase, and touching
it here would only create conflict surface with the autosizer slice." -- gobot/
  ```

---

## Task 8 — Delete the universe roster stack, each item verified individually

`universe_systems.go`, `client_universe.go`'s `ListSystems`, and `domain/system/universe.go` are explorer-only *in practice*. **Verify each on its own before deleting it — do not delete as a batch.**

**Files:**
- Delete: `gobot/internal/adapters/expansion/universe_systems.go` (253 lines)
- Delete: `gobot/internal/adapters/expansion/universe_systems_test.go` (374 lines)
- Modify: `gobot/internal/adapters/api/client_universe.go` (delete lines 127-173 — the `ListSystems` doc comment and method; the file keeps `GetJumpGate`, `GetWaypoint`, `ListWaypoints`, `GetConstruction`, `SupplyConstruction`, `extractSystemSymbol`)
- Delete: `gobot/internal/adapters/api/client_systems_test.go` (55 lines — it tests only `ListSystems`)
- Delete: `gobot/internal/domain/system/universe.go` (21 lines — `SystemAPIData`, `SystemsListResponse`)

**Interfaces:**
- Consumes: `player.PlayerRepository`, `shared.Clock`
- Produces: removes `expansion.UniverseSystemsCache` + `NewUniverseSystemsCache(lister UniverseLister, playerRepo player.PlayerRepository, clock shared.Clock, ttl time.Duration) *UniverseSystemsCache`, `expansion.UniverseLister`, `(*api.SpaceTradersClient).ListSystems(ctx context.Context, token string, page, limit int) (*system.SystemsListResponse, error)`, `system.SystemAPIData`, `system.SystemsListResponse`

**Steps:**

- [ ] **Verify `universe_systems.go` individually.** Its only consumer was the selector's construction at `main.go:1176`, removed in Task 3; its only in-package consumer was `off_gate_target.go:34`, deleted in Task 5.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'UniverseSystemsCache\|NewUniverseSystemsCache\|UniverseLister\|AllSystems' --include='*.go' .
  ```
  Expect hits only inside `internal/adapters/expansion/universe_systems.go` and `universe_systems_test.go`.

- [ ] Delete it.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/adapters/expansion/universe_systems.go internal/adapters/expansion/universe_systems_test.go && \
    go build ./internal/adapters/expansion/...
  ```
  Expect no build output. The rest of the `expansion` package — `adapters.go`, `bootstrap_phase.go`, `candidates.go`, `frontier_bearing.go`, `shipyard_backfill.go` — is the expansion scanner, probe-buyer-fleet and shipyard-backfill coordinators' surface and stays.

- [ ] **Verify `ListSystems` individually — and check the port interfaces too.** A method on a concrete client can also be *required* by an interface it is passed as; `grep` on the name catches both the calls and any interface that declares it.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'ListSystems' --include='*.go' . ; \
    echo '--- does any port interface require it? ---' ; \
    grep -rn 'ListSystems' --include='*.go' internal/domain/ports/ internal/domain/routing/ ; echo "ports exit=$?"
  ```
  Expect the first grep to hit only `internal/adapters/api/client_universe.go:134` (the declaration) and `internal/adapters/api/client_systems_test.go`. Expect `ports exit=1` — no domain port declares it, so removing the method breaks no interface satisfaction.

- [ ] Delete `ListSystems`. In `gobot/internal/adapters/api/client_universe.go`, delete lines 127-173 — the doc comment beginning `// ListSystems retrieves one page of the universe system list (GET /systems) with` through the closing `}` of the method (the line immediately before `// GetConstruction retrieves construction site information for a waypoint`). Verify the boundaries first:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    sed -n '125,129p;171,177p' internal/adapters/api/client_universe.go
  ```
  Then delete its test:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/adapters/api/client_systems_test.go
  ```

- [ ] **Verify `domain/system/universe.go` individually — by TYPE, not by package.** The `system` package is imported by ~80 files, but that is for `WaypointAPIData`, `PaginationMeta`, the navigation graph and the gate types. Only the two types declared in `universe.go` matter here.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    grep -rn 'SystemAPIData\|SystemsListResponse' --include='*.go' .
  ```
  Expect hits ONLY inside `internal/domain/system/universe.go` itself (the declarations and the `Data []SystemAPIData` field). **If any other file appears, stop** — the type has a consumer outside the retired stack and must not be deleted.

- [ ] Delete it, and confirm `PaginationMeta` (which `SystemsListResponse` referenced) survives in its own file — it is used by `WaypointsListResponse`.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    rm internal/domain/system/universe.go && \
    grep -rn 'type PaginationMeta' --include='*.go' internal/domain/system/
  ```
  Expect one hit in `internal/domain/system/ports.go` or `navigation_graph.go`. If `PaginationMeta` was declared *in* `universe.go`, restore the file with only that type in it and re-check.

- [ ] Build, vet, format, and run the affected packages.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./... && gofmt -l . && \
    go test ./internal/adapters/api/ ./internal/adapters/expansion/ ./internal/domain/system/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect no build/vet/gofmt output and `ok` for each package. If `internal/adapters/api` reports `TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient`, that is the known pre-existing flake (sp-odim4) — re-run that package once to confirm.

- [ ] Commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git commit --no-verify -am "refactor(expansion,api,domain): delete the universe roster stack (sp-mn3it)

Each item verified on its own before deletion, not as a batch:
  - UniverseSystemsCache — sole consumer was off_gate_target.go, already gone;
  - SpaceTradersClient.ListSystems — sole caller was the cache, and NO domain port
    interface declares it, so removing the method breaks no satisfaction;
  - domain/system/universe.go — verified BY TYPE (SystemAPIData, SystemsListResponse),
    not by package: internal/domain/system is imported by ~80 files for
    WaypointAPIData/PaginationMeta/the navigation graph, none of which move.

GET /systems is no longer called anywhere. The gate network is the reachable universe." -- gobot/
  ```

---

## Task 9 — Prove the manual `spacetraders ship warp` verb still works

Retirement removed one of two callers of the warp stack. This task proves the other one is intact, end to end from the CLI verb down to the executor.

**Files:**
- Modify: none (verification only), except the four prose fixes listed below

**Interfaces:**
- Consumes: `shipNav.NewWarpShipHandler(routeExecutor, shipRepo, warpWaypointSource)`, `ship.RouteExecutor.ExecuteWarpRoute`, `ship.NewWarpSystemCharter`, `ship.NewAPIWarpNavigator`, `ship.NewSystemEscapeReader`, `container.ContainerTypeWarp`, the `"warp_ship"` command-factory recovery key
- Produces: nothing

**Steps:**

- [ ] Confirm the whole manual-verb chain is still wired at the composition root, hop by hop.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    echo '--- CLI verb ---'        && grep -n 'warp' internal/adapters/cli/ship_navigate.go | head -8 ; \
    echo '--- gRPC container op ---' && grep -n 'Warp\|warp' internal/adapters/grpc/container_ops_ship.go | head -8 ; \
    echo '--- handler + registration ---' && grep -n 'WarpShipHandler\|WarpShipCommand\|WithWarpSupport\|NewWarpSystemCharter\|NewAPIWarpNavigator\|NewSystemEscapeReader' cmd/spacetraders-daemon/main.go ; \
    echo '--- recovery key ---'    && grep -rn '"warp_ship"\|ContainerTypeWarp' --include='*.go' .
  ```
  Expect a live hit at every hop: the CLI verb, the container op, `WithWarpSupport` + `NewWarpShipHandler` + `RegisterHandler[*shipNav.WarpShipCommand]` in `main.go` (around lines 1116-1136), and the `"warp_ship"` factory key with `ContainerTypeWarp`.

- [ ] Run the whole warp test set — CLI, gRPC container op, handler, executor, persistence, charter, escape reader, API client.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./internal/adapters/cli/ ./internal/adapters/grpc/ ./internal/adapters/api/ ./internal/application/ship/... \
      -run 'Warp|warp' -v 2>&1 | grep -E '^(FAIL|--- FAIL|--- PASS|ok)|panic:|DATA RACE'
  ```
  Expect `--- PASS` for at least these, and zero `--- FAIL`:
  `TestShipWarpCommandIsRegisteredAlongsideItsSiblings`, `TestShipWarpRequiresShipAndDestinationFlags`, `TestShipWarpHelpPointsAtWhereARefusalIsReported`, `TestWarpShipSpawnsATrackedContainerClaimingTheHull`, `TestRefusedWarpReportsTheStrandNumbersInTheContainerLog`, `TestWarpShipCommandRebuildsFromPersistedConfigOnRestart`, `TestWarpAdditionLeavesSiblingShipVerbsUntouched`, `TestWarpShipPostsWaypointSymbolAndParsesFuelAndArrival`, `TestWarpShip_WarpCapableHullReachesDestinationInAnotherSystem`, `TestWarpShip_DrivelessHullRefused_TypedErrorReachesCaller`, `TestWarpShip_ServerFuelRefusalSurfacesTypedFieldsToTheOperator`, `TestWarpShip_DeadEndDestinationRefusedBeforeAnyWarpCall`, `TestWarpShip_UnresolvableDestinationFailsClosed`, `TestWarpShip_UnloadableHullReportedHonestly`, `TestWarpShip_RejectsWrongRequestType`, `TestExecuteWarpLeg_WarpsToReachableSystemWithAdequateFuel`, `TestExecuteWarpLeg_TakesTheFuelVerdictFromTheServer`, `TestExecuteWarpLeg_RefuelsToTheServersRequirementAndRetriesOnce`, `TestExecuteWarpLeg_RefusesDestinationTheHullCouldNeverLeave`, `TestExecuteWarpLeg_AllowsDestinationWithAWayOut`, `TestExecuteWarpRoute_MultiHopRefuelsBetweenLegs`, `TestExecuteWarpLeg_ChartsDestinationSystemOnArrival`, `TestExecuteWarpLeg_RefusesShipWithoutWarpDrive`, `TestExecuteWarpLeg_PersistsTheNewSystem_SoTheNextTickDoesNotPlanFromTheStaleOrigin`, `TestExecuteWarpLeg_PersistsTheDepartureSoTheStuckShipSweeperCanSeeTheHull`, `TestExecuteWarpRoute_PersistsEachCompletedLegBeforeAttemptingTheNext`, `TestExecuteWarpLeg_FailsClosedWhenTheDeparturePositionCannotBePersisted`, `TestExecuteWarpLeg_RecordsTheLandingSoTheRowStopsClaimingATransit`, `TestExecuteWarpLeg_RefusesAWarpItCannotRecord`, `TestWarpSystemCharter_ChartsGateEdgesWaypointsMarketsShipyards`.
  The previously-passing `TestOffGateSelect_ExcludesOutOfWarpRange` will NOT appear — its file was deleted in Task 5. That absence is expected; every other name above must still appear and pass.

- [ ] Fix the four warp-stack comments that now name a retired caller. These are prose only — no behaviour, and they keep the comment ratchet honest by removing stale archaeology rather than adding to it.
  - `gobot/internal/application/ship/route_executor.go:107`: replace `// until a caller (slice C's explorer) invokes ExecuteWarpRoute.` with `// until a caller (the manual 'ship warp' verb) invokes ExecuteWarpRoute.`
  - `gobot/internal/application/ship/route_executor_warp.go:16`: replace `// (the explorer hull) invoke with a chosen target waypoint + ship.` with `// invoke with a chosen target waypoint + ship.`
  - `gobot/internal/application/ship/commands/navigation/warp_ship.go:36`: replace the fragment `// happen as a side effect of the frontier explorer dispatcher choosing to send a hull` with `// happen as a side effect of any coordinator choosing to send a hull` (read lines 33-39 first and preserve the surrounding sentence).
  - `gobot/internal/application/ship/warp_system_charter.go:12`: replace `// SHIP_EXPLORER warps into a fresh cluster off the jump-gate network, this is` with `// a warp-capable hull arrives in a fresh cluster off the jump-gate network, this is`.

- [ ] Re-run the warp set after the prose edits and confirm nothing moved.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && gofmt -l . && \
    go test ./internal/adapters/cli/ ./internal/adapters/grpc/ ./internal/adapters/api/ ./internal/application/ship/... \
      -run 'Warp|warp' 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```
  Expect no build/gofmt output and `ok` for each package.

- [ ] Commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git commit --no-verify -am "test(ship): prove the manual 'spacetraders ship warp' verb survives the retirement (sp-mn3it)

Retirement removed ONE OF TWO callers of the warp stack. The whole chain is re-verified
hop by hop at the composition root — CLI verb, gRPC container op, WarpShipHandler,
WithWarpSupport, the charter/navigator/escape-reader, ContainerTypeWarp and the
'warp_ship' restart-recovery key — and all 30 warp tests across cli/grpc/api/ship still
pass. Four comments that named the retired explorer dispatcher as the caller now name
the surviving one." -- gobot/
  ```

---

## Task 10 — The durable structural invariant, the comment ratchet, and the full sweep

The Task-1 behavioural proof could not outlive the port it asserted on. Its successor is structural: the explorer-demand vocabulary is **absent from the tree**, so nothing can satisfy it — which is the only form of "no implementation exists" that is sound for implicitly-satisfied Go interfaces.

**Files:**
- Create: `gobot/cmd/spacetraders-daemon/off_gate_retired_test.go`

**Interfaces:**
- Consumes: `path/filepath.WalkDir`, `os.ReadFile`, `testing.T`
- Produces: test-only — `TestOffGateWarpExpansionStaysRetired`

**Steps:**

- [ ] Write the durable invariant. Create `gobot/cmd/spacetraders-daemon/off_gate_retired_test.go` with exactly this content — it follows the house composition-root pin-test idiom already used by `heavy_target_single_instance_test.go` (a test that reads the source tree because nothing else can see the invariant):
  ```go
  package main

  // OFF-GATE WARP EXPANSION IS RETIRED, and this is the guard that keeps it retired (sp-mn3it).
  //
  // WHY A SOURCE-LEVEL CHECK AND NOT A BEHAVIOURAL ONE. The thing being asserted is that no code
  // path can EMIT explorer demand onto a latching bridge. A behavioural test needs a port to observe,
  // and the port is exactly what was deleted — an assertion cannot outlive its own mechanism. Nor can
  // the claim be made by grepping for an interface's implementations: Go interfaces are satisfied
  // IMPLICITLY, so an adapter can satisfy one without ever naming it, and a "no implementations
  // found" search proves nothing. What CAN be asserted soundly is absence of the vocabulary itself:
  // if no file declares OffGateDemandSink, then no value can be of that type, so no shape can satisfy
  // it, whatever it is called.
  //
  // It lives in the composition root because that is where the wiring it forbids would have to appear.

  import (
  	"os"
  	"path/filepath"
  	"strings"
  	"testing"
  )

  // retiredOffGateIdentifiers is the vocabulary of the retired slice. Any one of them reappearing in
  // Go source means the demand path is being rebuilt.
  var retiredOffGateIdentifiers = []string{
  	"OffGateDemandSink",
  	"OffGateDemandSignal",
  	"OffGateDemandSource",
  	"EmitOffGateDemand",
  	"ExplorerOffGateBridge",
  	"ExplorerDemandProvider",
  	"advanceOffGate",
  	"retractOffGateDemand",
  	"OffGatePorts",
  }

  func TestOffGateWarpExpansionStaysRetired(t *testing.T) {
  	root := "../.."
  	found := map[string][]string{}

  	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
  		if err != nil {
  			return err
  		}
  		if d.IsDir() {
  			// Skip build output and anything vendored; this test reads OUR source only.
  			if d.Name() == "vendor" || d.Name() == ".git" || d.Name() == "bin" {
  				return filepath.SkipDir
  			}
  			return nil
  		}
  		if !strings.HasSuffix(path, ".go") {
  			return nil
  		}
  		// The guard must not find itself.
  		if strings.HasSuffix(path, "off_gate_retired_test.go") {
  			return nil
  		}
  		src, readErr := os.ReadFile(path)
  		if readErr != nil {
  			return readErr
  		}
  		text := string(src)
  		for _, ident := range retiredOffGateIdentifiers {
  			if strings.Contains(text, ident) {
  				found[ident] = append(found[ident], path)
  			}
  		}
  		return nil
  	})
  	if err != nil {
  		t.Fatalf("walking the source tree: %v", err)
  	}

  	if len(found) != 0 {
  		for ident, files := range found {
  			t.Errorf("%q reappeared in %v — off-gate warp expansion is RETIRED (sp-mn3it): the demand bridge LATCHES, so re-introducing a writer strands a standing demand nobody serves",
  				ident, files)
  		}
  	}
  }
  ```
  Note `root := "../.."` — `go test` runs with the package directory as cwd, and `cmd/spacetraders-daemon` is two levels below the module root.

- [ ] Run it and see it PASS.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test ./cmd/spacetraders-daemon/ -run TestOffGateWarpExpansionStaysRetired 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|reappeared'
  ```
  Expect `ok`.

- [ ] **Mutation-probe the guard** — a test that has never been seen to fail is not a guard. Patch, verify the patch applied, run, and restore in ONE invocation, so an interrupted run cannot leave the tree dirty. The witness string is chosen so it cannot survive its own mutation.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    printf 'package main\n\n// probe fixture: sp-mn3it mutation check\ntype probeOffGateSink interface{ EmitOffGateDemand(playerID int, signal int) }\n' > cmd/spacetraders-daemon/zz_offgate_probe.go && \
    grep -q 'EmitOffGateDemand' cmd/spacetraders-daemon/zz_offgate_probe.go && echo "PROBE APPLIED" && \
    go test ./cmd/spacetraders-daemon/ -run TestOffGateWarpExpansionStaysRetired 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|reappeared' ; \
    rm -f cmd/spacetraders-daemon/zz_offgate_probe.go && \
    go test ./cmd/spacetraders-daemon/ -run TestOffGateWarpExpansionStaysRetired 2>&1 | grep -E '^(FAIL|--- FAIL|ok)'
  ```
  Expect, in order: `PROBE APPLIED`, then `--- FAIL: TestOffGateWarpExpansionStaysRetired` naming `"EmitOffGateDemand"` and `zz_offgate_probe.go`, then after the restore `ok`. **If the probed run passes, the guard is inert** — most likely the walk root or the self-exclusion is wrong. Fix it before continuing.

- [ ] Confirm the probe file is gone and the tree is clean.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git status --short -- gobot/
  ```
  Expect only `?? gobot/cmd/spacetraders-daemon/off_gate_retired_test.go` (plus any uncommitted prose from earlier tasks). **No `zz_offgate_probe.go`.**

- [ ] Run the comment-density ratchet on every touched package. If any regressed, TRIM the package's comments rather than re-baselining.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    for pkg in internal/application/parkedsensing internal/application/scouting/commands internal/application/fleet/commands internal/adapters/expansion internal/adapters/grpc internal/adapters/api internal/infrastructure/config internal/domain/system internal/application/ship cmd/spacetraders-daemon ; do \
      echo "== $pkg" ; make comment-audit-check ONLY=$pkg ; done
  ```
  Expect no failure line for any package. A regression here is expected in principle — deleting a heavily-commented driver raises the *ratio* of what remains — so if one trips, trim the stalest archaeology in that package (ENGINEERING.md §6) and re-run. Do **not** run `make comment-audit-baseline`.

- [ ] Full sweep: build, vet, format, and the whole suite with the race detector. This is the pre-gate gate.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go build ./... && go vet ./... && gofmt -l . && \
    go test -race ./... 2>&1 | grep -E '^(FAIL|--- FAIL)|panic:|DATA RACE'
  ```
  Expect no output at all. The only tolerated failure is `TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient` in `internal/adapters/api` (known flake, sp-odim4) — if it appears, re-run just that package to confirm it passes on a retry:
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate/gobot && \
    go test -race -count=1 ./internal/adapters/api/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE'
  ```

- [ ] Final acceptance check against the bead's criteria — read them back and confirm each has evidence above.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders && bd show sp-mn3it
  ```
  Confirm: (1) `advanceOffGate` and its call site removed — Task 1 + Task 2; (2) the explorer-demand bridge proven RETRACTED by test, not merely unread — Task 1's RED→GREEN behavioural proof plus Task 6's zero-write-call-sites enumeration plus this task's structural guard; (3) the two orphaned interface declarations removed — Task 4; (4) the manual `spacetraders ship warp` verb still works and its tests still pass — Task 9; (5) no explorer demand can be emitted from any path — the structural guard, mutation-probed; (6) build, vet, gofmt, full `-race` green — this task.

- [ ] Commit.
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git add gobot/cmd/spacetraders-daemon/off_gate_retired_test.go && \
    git commit --no-verify -am "test(daemon): pin off-gate warp expansion RETIRED with a structural guard (sp-mn3it, epic sp-sy3dl)

The behavioural proof from the first commit could not outlive the port it observed —
an assertion cannot survive its own mechanism. Its successor asserts what IS durable:
the retired vocabulary is absent from the source tree, so nothing can satisfy it. That
is the only sound form of 'no implementation exists' for implicitly-satisfied Go
interfaces, where searching for implementations proves nothing.

Mutation-probed: reintroducing EmitOffGateDemand in a scratch file fails the guard by
name, and the restore returns it to green. Comment ratchet clean on all ten touched
packages; build, vet, gofmt and the full -race suite green." -- gobot/
  ```

- [ ] Hand off to the gate. Do **not** run captain-gate from this lane — report the branch and the commit list to the lead (RULINGS #13: all code lands via worktree → captain-gate → main, never merged by hand).
  ```bash
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate && \
    git log --oneline main..sp-offgate && git status --short
  ```
  Expect the task commits listed and a clean working tree.

---

## Self-review record

Run before this plan was committed, per the writing-plans self-review.

**1. Spec coverage.** Every requirement in sp-mn3it and in the parent spec's off-gate section maps to a task:

| Requirement | Task |
|---|---|
| Prove the bridge retracts BEFORE removing anything | 1 |
| `advanceOffGate` call site (`expansion.go:473`) removed | 1 |
| `retractOffGateDemand` call site (`expansion.go:413`) removed | 1 |
| `reachesAny` (`reach.go:290`) and its sole caller | 1 (caller), 2 (method) |
| The driver functions + `offgate_test.go` | 2 |
| `OffGatePorts` and its four interfaces | 3 |
| `main.go` fields (331-334) and the ports literal (388-393) | 3 |
| `ExpandPorts.OffGate`, `ExpandReport.OffGate*`, `SensingEnginePorts.OffGate` | 3 |
| `stallReasonOffGateNoTarget` + `rep.OffGate*` reads | 3 |
| The two orphaned declarations (`OffGateTargetSelector`, `ShipyardCoverageReader`) | 4 |
| Explorer-only adapters, **each individually verified** | 5 (three), 6 (the bridge) |
| The method warning as a STEP, not a footnote | 3 (headline step), 4, 5, 6, 8 (applied per item) |
| The autosizer read chain forced by the bridge deletion | 6 |
| The 5 `explorer` tune keys + config surface | 7 |
| `hullbuy.HullClassExplorer` + `DedicatedFleet` decision, with reason | Orientation §"Scope line", confirmed by a Task 7 step |
| `heavy` config surface that becomes dead | Orientation §"Scope line" — **none does**, stated with the reason |
| `universe_systems.go`, `ListSystems`, `domain/system/universe.go`, each individually verified | 8 |
| Manual `ship warp` verb exercised and proven passing | 9 |
| Build, vet, gofmt, full `-race` green | 10 |

**2. Placeholder scan.** No "TBD", no "add appropriate error handling", no "write tests for the above", no "similar to Task N". Every deletion names exact file paths and line ranges; every step carries a runnable command or the literal before/after code. Where a line range could have drifted (the `fleet_autosizer_types.go` alias block, the `probe_sensing_stall_test.go` rows, `client_universe.go`'s method boundary, `fleet_autosizer_demand_ports.go`'s method extent), the step includes a `sed -n` boundary check before the cut rather than asserting the number.

**3. Type/name consistency.** Cross-checked across tasks: `OffGatePorts` / `OffGateSelector` / `OffGateDemandSink` / `ExplorerFinder` / `ExplorerDispatcher` are the *live* parkedsensing ports (Task 3); `OffGateTargetSelector` and `ShipyardCoverageReader` are the *orphans* in `offgate_types.go` (Task 4) and are never conflated with them. The adapter side uses `OffGateWarpTargetSelector`, `ExplorerOffGateBridge`, `IdleExplorerPort`, `ExplorerWarpDispatcher`, `UniverseSystemsCache`. The autosizer side uses `OffGateDemandSource`, `ExplorerFleetSource`, `ExplorerDemandProvider`, `autosizerExplorerFleetSource`. `NewFleetAutosizerCoordinatorHandler`'s post-Task-6 signature is stated once, in full, in Task 6's Interfaces block, and the Task 6 vet step names the arity break it will catch.

**Six fixes applied inline during review.**
(a) Task 3 originally left `offGateSelector`, `idleExplorerPort` and `explorerWarpDispatcher` constructed in `main.go` for Task 5 to clean up — but removing their only *use* makes them declared-and-unused, which is a Go compile error, so their deletion moved into Task 3 with the reason recorded.
(b) Task 4's method-warning step originally read as a name grep; it was rewritten to make the sound argument — there is no field, parameter or variable anywhere *of either orphan's interface type*, so no value can be assigned to one and no implementation is reachable regardless of what shapes exist.
(c) Task 1's `expansion_spend_pause_test.go` range was off by two lines — re-read and corrected to 173-253, with the expected first and last line quoted so the executor can confirm.
(d) Task 3's `probe_sensing_stall_test.go` step deleted four rows; the fourth (`Discovered: 2`) is real non-off-gate coverage, so it is now kept with the retired field stripped and the case renamed, and the table's doc comment is rewritten rather than partially cut.
(e) Task 6's `fleet_autosizer_demand_ports.go` range was 124-135, which would have taken the first line of `countShips` — a helper shared with the light and heavy fleet sources. Corrected to 124-134 with an explicit "keep `countShips`" note.
(f) Task 7's `fleet_autosizer_types.go` deletion quoted the block with one line elided as `...`; the full five lines are now inline, since an executor reading tasks out of order cannot reconstruct an elision.
