# Retire Off-Gate Warp Expansion — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire off-gate warp expansion — the explorer hull class and its whole demand-to-buy chain — so no code path can emit explorer demand onto the latching bridge, while leaving the warp/charter stack intact for the manual `spacetraders ship warp` verb.

**Architecture:** The off-gate slice is a driver (`parkedsensing/offgate.go`) that writes a demand signal onto a latching in-memory bridge (`adapters/expansion.ExplorerOffGateBridge`), whose only reader is the fleet autosizer's `ExplorerDemandProvider`. Retirement removes the driver, the bridge, the reader, and the four explorer-only adapters that fed them, in an order that proves the bridge can never hold a standing demand *before* any of it is deleted. The warp executor, charter, navigator and escape reader are shared with the operator's manual `ship warp` verb and are not touched — retirement removes one of two callers, not the stack. The two enter at *different* methods: the retired dispatcher called `ExecuteWarpRoute`, the manual verb calls `ExecuteWarpLeg`. Both stay; `ExecuteWarpRoute` is simply caller-less afterwards, which is deliberate and filed as sp-bwzy3.

**Tech Stack:** Go, Postgres, the gobot daemon's coordinator/container model

## Global Constraints

- All code lands via worktree → captain-gate → main. Never merge by hand.
- Money guards fail closed and are never weakened (RULINGS #4).
- Features ship ARMED — no feature flags, no default-off arming seam (standing order).
- Nothing stored that can be derived (RULINGS #2).
- Protected paths, never modified: `gobot/internal/captain/**`, `gobot/cmd/captain-gate/**`, `city/agents/**`, `gc` source.
- Test output must be filtered (`grep -E '^(FAIL|--- FAIL)|panic:|DATA RACE'`), never dumped raw.
- `go build` skips `_test.go` — `go vet ./...` is what catches signature breaks and must be run.
- Known pre-existing flake, not a lane's fault: `TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient` in `internal/adapters/api` (~1 run in 4–9), filed sp-odim4.
- Commits use an explicit pathspec. **`git commit -am … -- <pathspec>` is invalid** and aborts with `fatal: paths '…' with -a does not make sense` (verified, git 2.50.1, exit 128, nothing committed). Every commit in this plan therefore uses `git add -A gobot/ && git commit --no-verify -m "…"`, never `-am` with a pathspec.
- **`gofmt -l` is a LISTER, not a gate**: it prints offending files and exits 0, so `gofmt -l . && git commit …` commits unformatted code. Every format gate in this plan is `gofmt -w . && test -z "$(gofmt -l .)"`, which writes the fix and then fails non-zero if anything is still unformatted.

### THE ADDRESSING RULE — read this before the first cut

**Every edit in this plan is CONTENT-ADDRESSED. The quoted text is the contract; line numbers are advisory hints only.**

Each edit step gives you four things, in this order:

1. **A uniqueness check** — a `grep -c` on one distinctive line of the block. It must print exactly `1`. If it prints anything else the tree has moved under this plan: stop, read the file, and re-locate by content. (`grep -c` exits non-zero on a zero count, so under `set -euo pipefail` a missing anchor aborts the step loudly rather than silently.)
2. **A FIND block** — the exact text as it stands in the file today, with enough leading and trailing context to be unambiguous. **When the edit touches the end of a function, an `if`, a `for`, a struct or a composite literal, the closing `}` is INSIDE the FIND block.** When the edit is a deletion whose neighbour is separated by a blank line, **the blank line is INSIDE the FIND block** (a deletion that stops one line short leaves two consecutive blanks, which `gofmt` rewrites and the format gate then trips on).
3. **A REPLACE block** (or "delete this block", which means replace it with nothing).
4. **A line-number hint in parentheses, explicitly marked advisory** — e.g. *(currently ~`:465-475`, verify by content)*. Never cut by that number.

**Consequences you can rely on:**

- **Ranges cannot go stale.** A block that has moved is still found by its text.
- **Off-by-one at a brace is impossible**, because the brace is inside the quoted block.
- **Intra-file ordering stops mattering.** There is no bottom-up rule in this plan and no per-file ordering banner: within a file, apply the edits in any order. Where two edits in one file touch adjacent lines, the FIND blocks are written so that either order leaves the other still locatable; where that needed care it is called out on the step.

**A note on very large deletions.** A few blocks run to dozens of lines. Those are addressed by a **HEAD anchor** (the first line deleted, quoted verbatim and uniqueness-checked), a **TAIL anchor** (the last few lines deleted, quoted verbatim, ending on the closing `}`), and the **FIRST SURVIVING LINE** (quoted verbatim). Delete from the head anchor through the tail anchor's last line inclusive. That is still content addressing — three exact strings, no line numbers.

**MARKDOWN SCAFFOLDING WARNING.** Every code block below is indented **two spaces** so it nests inside its `- [ ]` list item. That two-space prefix is markdown, not source. **Strip it.** The Go files in this repo indent with **TABS**; the FIND text you match against the file is what remains after removing the two-space prefix.

### Shell discipline

**Every shell block in this plan begins with `set -euo pipefail`.** This is not decoration — without it a failed command prints a success signature:

- `( cd /nonexistent && rm -f foo && grep -rn zzz . ; echo "exit=$?" )` prints `exit=1`, which several stop-rules in earlier drafts documented as a PASS.
- `out=$( cd /nonexistent && go build ./... 2>&1 | grep -E '^[^ ]' )` is empty, which "expect no output" reads as a PASS.

With `set -euo pipefail` and `cd` on its own line, a bad path aborts the block before anything can vote. Where a non-zero exit is genuinely expected (a `grep` that may legitimately find nothing; a `go build` this plan deliberately runs against a non-compiling tree) the step ends that command with an explicit `|| true` and says why. **Never add `|| true` to a gate.**

### Grep stop-rule discipline

Every `grep` stop-rule in this plan states its **expected hits explicitly** — the file:line set, or the file set, that the grep is known to return on the tree at that point. A stop-rule with no stated expectation is not a stop-rule.

**STANDING ESCAPE CLAUSE, applying to every grep stop-rule in this plan:** an unexpected hit that is **comment-only** is acceptable — note it, fix the comment, continue. An unexpected hit in **code** is not: stop and investigate. A stop-rule exists to catch a live consumer, not stale prose.

---

## Orientation — read this before Task 1

**Worktree root:** `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec`
**Go module root:** `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot` — every `go`/`make` command below runs from there.
Sibling worktrees live under `.claude/worktrees/`; scope every search to the paths given here, never to the repo root.

**THE WORKTREE DOES NOT EXIST YET — the first step below creates it.** Do **not** substitute the existing `.claude/worktrees/sp-offgate`: that worktree is checked out on branch `sp-offgate` at `a6321f31`, whose tree differs from `main` across `internal/application/trading/commands` (long-haul arb), `internal/adapters/grpc` (container retention), `internal/application/manufacturing/services` and six other areas — 22 files, ~2000 lines that `main` has and it does not. Executing this plan there would revert live work. Cut a fresh worktree off `main`.

**The hazard, in one paragraph.** `parkedsensing.advanceOffGate` writes an `OffGateDemandSignal` onto `OffGateDemandSink` on *every* tick. The concrete sink, `expansion.ExplorerOffGateBridge`, **latches**: it answers every read from the last write, forever. Its only reader is the autosizer's `ExplorerDemandProvider`. Delete the reader while the writer still runs and you strand a bridge latching demand nobody serves; delete the writer carelessly and you can leave a `Demanded: true` standing from the last tick before the deploy. That is why Task 1 proves the emit path is gone before anything else is removed.

**THE METHOD WARNING — this is a step, not a footnote.** You **cannot** establish that a Go interface is dead by grepping its name. Interfaces are satisfied *implicitly*: an adapter can satisfy `OffGateSelector` without the string `OffGateSelector` appearing anywhere in its file — and in this codebase one does (`expansion.OffGateWarpTargetSelector` satisfies `parkedsensing.OffGateSelector` by shape alone). Deadness is established by finding **who consumes the port STRUCT that holds the interface** — here `OffGatePorts`, and above it `ExpandPorts.OffGate` and `SensingEnginePorts.OffGate`. Every task below that deletes an interface carries an explicit consumer-of-the-struct verification step. A step that said "grep for references and delete if none" would be wrong and would delete live code.

**Surface corrections found while verifying the bead (sp-mn3it).** Use these, not the bead's numbers. All positions below are **advisory** — locate by the text this plan quotes.

| Bead says | Actually |
|---|---|
| Wiring at `main.go:331-332` (two fields) | **`main.go:331-334` — FOUR fields**: `offGateSelect`, `offGateDemand`, `idleExplorer`, `explorerWarp` |
| `OffGatePorts` literal at `main.go:388-390` | **`main.go:385-393`** — a three-line doc comment plus a literal assigning all four |
| (not mentioned) | Construction also at **`main.go:1170`** (bridge, comment block `:1164-1169`), **`main.go:1187-1192`** (selector/finder/dispatcher, comment block `:1172-1186`, blank separator at `:1171`), **`main.go:1212`** (bridge handed to the autosizer handler), **`main.go:1415-1418`** (the `sensingWiring` literal) |
| (not mentioned) | `parkedsensing` is imported at **`main.go:36`** and `main.go:388` is its only use **of the identifier**. `grep parkedsensing main.go` does **not** go silent afterwards: `:17` is the `parkedSensingAdapters` import path and `:1382` is an unrelated comment, both of which contain the lowercase string. The identifier test is `grep -n 'parkedsensing\.' cmd/spacetraders-daemon/main.go` |
| (not mentioned) | `SensingEnginePorts.OffGate` at **`sensing_engine_ports.go:169`**, copied through at **`:363`** — a second ports struct above `ExpandPorts` |
| (not mentioned) | Deleting `ExplorerOffGateBridge` **forces** removal of the `offGateDemand fleetCmd.OffGateDemandSource` parameter of `NewFleetAutosizerCoordinatorHandler` (`fleet_autosizer_ports.go:45` — `:44` is the LIVE `scannedYards` parameter) and the provider registration at `:69` — a compiler consequence, not a scope choice. This plan's FIND block quotes `scannedYards` and writes it back, so the wrong-parameter cut is structurally impossible |
| (not mentioned) | `NewFleetAutosizerCoordinatorHandler` has a second real call site: `internal/adapters/grpc/fleet_autosizer_heavy_wiring_test.go:65-67`, 11 positional args. (`cmd/spacetraders-daemon/heavy_target_single_instance_test.go:156,179` also spell the name but inside **backtick raw-string fixtures** fed to an AST parser — they do not compile against the signature and must not be edited.) |

Everything else in the bead verified correct: `advanceOffGate` called once at `expansion.go:473`; `retractOffGateDemand` called once at `expansion.go:413`; `OffGateTargetSelector` (`offgate_types.go:48`) and `ShipyardCoverageReader` (`offgate_types.go:57`) are orphaned declarations with zero implementations; `reachesAny` (`reach.go:290`) has exactly one caller, `expansion.go:469`.

**Scope line for this slice.** Everything that can **emit, carry, read, or ARM** explorer demand goes now. Inert *vocabulary* waits for the autosizer deletion (epic slice 4):

- **IN:** the parkedsensing driver, `OffGatePorts` and its four interfaces, the two orphaned declarations, the four explorer-only adapters, the universe-roster stack, the autosizer's explorer demand provider + fleet source + its `offGateDemand` parameter, the 5 `autosizer_*_explorer*` tune keys and their whole config chain, the `HullClassExplorer` arms in `classDisabled` / `classGuardConfig`, and the four **test** files that would stop compiling once those arms and the alias go (`fleet_autosizer_guards_test.go`, `fleet_autosizer_heavy_cap_test.go`, `run_fleet_autosizer_coordinator_test.go`, `command_factory_fleet_autosizer_live_test.go` — each with an explicit disposition in Task 7).
- **DEFERRED to slice 4 (autosizer deletion):** `hullbuy.HullClassExplorer` (`domain/hullbuy/hull_class.go:23`), its `"explorer"` arm in `hullbuy.DedicatedFleet` (`:47`), and the two `hull_class_test.go` rows that pin them (`:21`, `:50`). **Reason:** `hullbuy` is a shared vocabulary package owned by no coordinator — its own doc comment says so — imported by both the fleet autosizer and the dedicated contract scaler, which slice 4 rewrites. After this slice the constant is *inert*: nothing constructs an explorer order, nothing registers an explorer provider, so it cannot reach a purchase and RULINGS #4 is untouched either way. Touching `hullbuy` here buys zero behaviour and creates a merge-conflict surface with slice 4. Contrast the demand chain, which is **not** deferrable: leaving `ExplorerDemandProvider` alive against a writer-less bridge is exactly the "latching bridge nobody serves" the bead names as the hazard, and the compiler forces its removal the moment the bridge goes.
- **NOT DEAD, do not touch:** no `heavy` config surface dies in this slice. `autosizer_heavy_cap`, `autosizer_heavy_unserved_lanes_min`, `autosizer_heavy_treasury_pct_per_purchase`, `autosizer_max_price_heavies`, `autosizer_ship_type_heavies` and the heavy demand provider all stay live — `heavy_cap` re-homing is slice 3 of epic sp-sy3dl and is planned separately.

**MUST NOT be removed — the warp/charter stack.** `route_executor_warp.go`, `route_executor_warp_errors.go`, `warp_system_charter.go`, `warp_navigator.go`, `warp_escape_reader.go`, `container.ContainerTypeWarp`, the `"warp_ship"` recovery key, and `HasWarpDrive()` are all reached by the manual operator verb `spacetraders ship warp` (`adapters/cli/ship_navigate.go:157` → `grpc/container_ops_ship.go:100` → `application/ship/commands/navigation/warp_ship.go:65`, wired at `main.go:1127` (`routeExecutor.WithWarpSupport`), `:1145` (`NewWarpShipHandler`) and `:1146` (`RegisterHandler[*shipNav.WarpShipCommand]`) — advisory positions). Task 9 proves they still pass.

**THREE WARP TEST FILES ARE PROTECTED LIVE FIXTURES.** `internal/application/ship/route_executor_warp_test.go`, `internal/application/ship/route_executor_warp_persistence_test.go` and `internal/application/ship/commands/navigation/warp_ship_test.go` between them hold **22 live code references** to `explorer` — fixture names such as `newWarpExplorerShip`, ship symbols, module identifiers. They are the proof that the manual verb still works. **Never renamed, never edited, never swept.** Any sweep in this plan that would otherwise reach them is anchored to comments or scoped away.

**A CORRECTION THE WHOLE PLAN DEPENDS ON — the manual verb rides `ExecuteWarpLeg`, NOT `ExecuteWarpRoute`.** `warp_ship.go:16-17` declares `WarpLegExecutor` with the single method `ExecuteWarpLeg`, and the verb calls it at `warp_ship.go:98`. `ExecuteWarpRoute` is a **different** method (`route_executor_warp.go:41`) whose only non-test call site in the tree is `explorer_dispatch_adapter.go:98` — which Task 5 deletes. So after this retirement `ExecuteWarpRoute` has **zero non-test callers**, and that is the **expected post-state, not a failure**.

**DECISION, already taken — do not re-open it.** `ExecuteWarpRoute` **STAYS**. Deleting it is out of scope here and is filed separately as **sp-bwzy3**. Every step below is written to that decision: Task 5's early tripwire watches `ExecuteWarpLeg`'s live caller (not `ExecuteWarpRoute`'s), and Task 9 writes an honest comment on `route_executor.go:107` recording that the method currently has no caller and pointing at sp-bwzy3 — it must **not** claim the manual verb calls it.

**Money-safety of the intermediate states.** Between Task 1 and Task 6 the bridge exists but is never written. `ExplorerOffGateBridge.ExplorerDemand` returns `ok=false` until a player's first emit, and `ExplorerDemandProvider` treats `ok=false` as `Readable: false` — it fails **CLOSED**. `ExplorerHullsEnabled` also defaults false and appears in no checked-in YAML. Every intermediate commit is therefore strictly money-safer than the state before it (RULINGS #4 moves in the safe direction only).

**Comment discipline (`ENGINEERING.md §6`) and why the ratchet is NOT a pass/fail gate here.** `make comment-audit-check` is **already RED on the untouched tree** for six of the ten packages this plan touches — `internal/application/parkedsensing` (45.46% > 44.54%), `internal/application/scouting/commands` (36.50% > 36.50%), `internal/application/fleet/commands` (36.99% > 36.65%), `internal/adapters/grpc` (28.25% > 28.16%), `internal/adapters/api` (21.50% > 20.87%) and `cmd/spacetraders-daemon` (42.39% > 41.92%). Its recorded baseline predates unrelated commits. Running it as a gate would fail this lane for other people's work. **The gate this plan uses instead is a captured-baseline comparison**: Orientation step 3 records every touched package's ratio on the PRE-Task-1 tree, and Task 10 requires that **no package regresses relative to that capture**.

**Steps:**

- [ ] Create the executor worktree off `main`. **Do this first — every path in this plan assumes it exists.**
  ```bash
  set -euo pipefail
  git -C /Users/andres.dandrea/IdeaProjects/cities/spacetraders worktree add \
    /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec \
    -b sp-offgate-exec main
  ```
  If the branch already exists from an abandoned attempt, delete it (`git -C … branch -D sp-offgate-exec`) and re-run — do **not** reuse a lane worktree.

- [ ] Confirm you are on the right branch, it is clean, and it matches `main`.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git rev-parse --abbrev-ref HEAD
  git status --short
  git diff --stat main -- gobot/
  ```
  Expect `sp-offgate-exec`, no output from `status`, and no output from `diff --stat`.

- [ ] **Capture the comment-density baseline on the PRE-Task-1 tree.** This is what Task 10 compares against; the repo's own recorded baseline is stale and already red (see above).
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  make comment-audit 2>&1 | grep -E '^(internal/application/parkedsensing|internal/application/scouting/commands|internal/application/fleet/commands|internal/adapters/expansion|internal/adapters/grpc|internal/adapters/api|internal/adapters/metrics|internal/infrastructure/config|internal/domain/system|internal/application/ship|cmd/spacetraders-daemon)[[:space:]]' \
    | sort > "${TMPDIR:-/tmp}/offgate-comment-baseline.txt"
  cat "${TMPDIR:-/tmp}/offgate-comment-baseline.txt"
  wc -l < "${TMPDIR:-/tmp}/offgate-comment-baseline.txt"
  ```
  Expect **11** rows. On the tree this plan was written against they read (advisory — use whatever your capture prints, not these):
  `internal/infrastructure/config 49.0%`, `internal/application/parkedsensing 45.5%`, `cmd/spacetraders-daemon 42.4%`, `internal/application/fleet/commands 37.0%`, `internal/application/scouting/commands 36.5%`, `internal/domain/system 35.4%`, `internal/adapters/expansion 33.3%`, `internal/application/ship 29.7%`, `internal/adapters/grpc 28.3%`, `internal/adapters/metrics 26.3%`, `internal/adapters/api 21.5%`.
  **If the file has fewer than 11 rows, stop** — a package name changed and the Task 10 comparison would silently skip it.

---

## Task 1 — Prove no tick can emit explorer demand, then remove the two call sites

The proof is written and demonstrated **failing** before a single line of the driver is deleted. The removal of the two call sites is the minimal change that makes it pass.

**Note on the assertion's shape.** The proof asserts the sink is **never called at all**, not "called with a zero signal". `retractOffGateDemand` existed to clear a latch that a live raiser could set; with no raiser, the correct steady state is "never written", and a bridge never written reads UNREADABLE, which makes the autosizer's explorer pass fail CLOSED. That is strictly safer than a written zero.

**FOUR existing tests assert the opposite** — that a tick *must* write to the bridge — and all four go. Two are replaced here (`expansion_spend_pause_test.go`: `TestAdvanceExpansion_SpendPaused_RetractsStandingExplorerDemand`, `TestAdvanceExpansion_SpendPaused_RetractsThroughAPartiallyWiredSlice`); the other two live in `offgate_test.go` and die with that file in Task 2 (`TestAdvanceExpansion_AReachableGateTargetSuppressesOffGateDemand`, which asserts an explicit no-demand write at `offgate_test.go:198-201`, and `TestAdvanceExpansion_TheDemandBridgeIsWrittenEvenWhenNothingIsDemanded`, whose whole subject is the anti-dormancy write, `:219`). They were pinning the old contract, and the contract is what is changing.

**Files:**
- Create: `gobot/internal/application/parkedsensing/offgate_retraction_test.go`
- Modify: `gobot/internal/application/parkedsensing/expansion.go` — two independent content-addressed edits: the reach discriminator + `advanceOffGate` call *(advisory ~`:465-475`)*, and the spend-pause branch *(advisory ~`:409-416`)*
- Modify: `gobot/internal/application/parkedsensing/expansion_spend_pause_test.go` — one large deletion *(advisory ~`:172-253`)*

**Interfaces:**
- Consumes: `AdvanceExpansion(ctx context.Context, p ExpandPorts, playerID int, k ExpandKnobs, budgetRate float64) (ExpandReport, error)`; `OffGatePorts{Select OffGateSelector; Demand OffGateDemandSink; Explorer ExplorerFinder; Warp ExplorerDispatcher}`; `OffGateDemandSink.EmitOffGateDemand(playerID int, signal OffGateDemandSignal)`; the package test harness `newExpandHarness() *expandHarness` (`expansion_test.go:493`), `(*expandHarness).ports() ExpandPorts` (`:507`), `staffedYardRow(system, waypoint string) QueuedSlot` (`staging_test.go:35`)
- Produces: no production symbols. Test-only: `recordingDemandSink`, `alwaysFindsSelector`, `alwaysFindsExplorer`, `recordingWarpDispatcher`, `sealedPocketExpandPorts(t *testing.T) (ExpandPorts, *recordingDemandSink, map[string]bool)`

**Steps:**

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

  // sealedPocketExpandPorts builds the ONE ledger shape that reaches the WHOLE driver: a system we
  // hold and staff, a target with outstanding charting work, and an EMPTY gate adjacency — which is
  // what "every way out is under construction" looks like to the reach search, since it only ever
  // sees traversable edges.
  //
  // THE SEALED POCKET IS FOR THE DISPATCH HALF, not the emit half. advanceOffGate writes the sink on
  // the gate-reachable path TOO — an explicit zero signal (offgate.go:149-151), the anti-dormancy
  // write — so any wired fixture at all would turn the emit assertion RED. What only a sealed pocket
  // reaches is the selector and the warp dispatch below it; on a fixture with any gate route
  // TestAdvanceExpansion_NoTickDispatchesAnExplorer would pass vacuously against the LIVE driver.
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
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/application/parkedsensing/ -run 'TestAdvanceExpansion_NoTick' 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE|bridge LATCHES|dispatched' || true
  ```
  Expect `--- FAIL: TestAdvanceExpansion_NoTickEmitsExplorerDemand` and `--- FAIL: TestAdvanceExpansion_NoTickDispatchesAnExplorer`, with the "the bridge LATCHES" message showing at least one emitted signal. **If either test PASSES here, stop** — the fixture is not reaching `advanceOffGate` (most likely `wired()` is false or the gate adjacency admits a route), and the proof is vacuous.

- [ ] **Remove the `advanceOffGate` call site and the reach discriminator that exists only to feed it.** File: `gobot/internal/application/parkedsensing/expansion.go` *(advisory ~`:465-475`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'advanceOffGate(ctx, p, playerID, targets, gateReachable, &rep)' internal/application/parkedsensing/expansion.go
  ```
  FIND (the trailing `}` closes `AdvanceExpansion` and is part of the block):
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
  REPLACE WITH:
  ```go
  	return rep, nil
  }
  ```
  (`reachesAny`'s only caller was this block; `gateReachable` would otherwise be a declared-and-unused compile error. The method itself is removed in Task 2.)

- [ ] **Remove the spend-pause call site.** Same file *(advisory ~`:409-416`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'retractOffGateDemand(p, playerID)' internal/application/parkedsensing/expansion.go
  ```
  FIND:
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
  REPLACE WITH:
  ```go
  	if !k.SpendEnabled {
  		rep.SpendingPaused = true
  		return rep, nil
  	}
  ```

- [ ] **Delete the two tests that pinned the old retraction contract.** File: `gobot/internal/application/parkedsensing/expansion_spend_pause_test.go` *(advisory ~`:172-253`)*. This is a large deletion — use the three anchors.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/application/parkedsensing/expansion_spend_pause_test.go
  grep -c '^// THE LATCH, and the reason the pause RETRACTS off-gate demand rather than merely stopping to raise$' "$F"
  grep -c 'retraction wrote %+v, want the zero signal' "$F"
  grep -c '^// --- the free half stays free' "$F"
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // THE LATCH, and the reason the pause RETRACTS off-gate demand rather than merely stopping to raise
  ```
  **TAIL ANCHOR** — all four lines are deleted; the last is the bare `}` that closes `TestAdvanceExpansion_SpendPaused_RetractsThroughAPartiallyWiredSlice`:
  ```go
  	if latest != (OffGateDemandSignal{}) {
  		t.Fatalf("retraction wrote %+v, want the zero signal", latest)
  	}
  }
  ```
  **FIRST SURVIVING LINE** — do not delete; the blank line above it survives too:
  ```go
  // --- the free half stays free -------------------------------------------------
  ```
  **Post-condition:** exactly ONE blank line between the `}` that closes the test above the head anchor and the `// --- the free half stays free` banner. Two blanks means the leading blank was missed and `gofmt` will rewrite the file.

- [ ] Run the proof and see it PASS.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/application/parkedsensing/ -run 'TestAdvanceExpansion_NoTick' 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  Expect `ok  	github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing`.

- [ ] Run the whole package. Several off-gate tests in `offgate_test.go` now fail — that is expected and correct, they assert the retired behaviour, and Task 2 deletes them. Record which ones fail so Task 2 can confirm it deleted exactly that set.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/application/parkedsensing/ 2>&1 | grep -E '^(FAIL|--- FAIL)|panic:|DATA RACE' || true
  ```
  Expect `--- FAIL` for exactly these **seven**, all in `offgate_test.go`:
  `TestAdvanceExpansion_SealedFrontierWarpsTheIdleExplorerToTheOffGateTarget` (`:139`),
  `TestAdvanceExpansion_AReachableGateTargetSuppressesOffGateDemand` (`:175`; fails at `:198-201`, which asserts an explicit no-demand write),
  `TestAdvanceExpansion_TheDemandBridgeIsWrittenEvenWhenNothingIsDemanded` (`:211`; fails at `:219`),
  `TestAdvanceExpansion_NoExplorerOwnedRaisesDemandAndWarpsNothing` (`:230`),
  `TestAdvanceExpansion_DemandWithoutATargetWarpsNothing` (`:251`),
  `TestAdvanceExpansion_AFailingSelectorRaisesDemandButNeverWarps` (`:270`),
  `TestAdvanceExpansion_OffGateSelectionIsBoundedByTheExplorersFuelCapacity` (`:320`).

  **These two must NOT appear, and their absence is not a problem:** `TestAdvanceExpansion_AnUnreadableExplorerFleetWarpsNothing` (`:289-299`) and `TestAdvanceExpansion_ARefusedWarpDoesNotFailTheTick` (`:303-315`). Each asserts only "no error returned + zero warps dispatched", which a never-invoked driver satisfies trivially — they go green, not red. Likewise `TestAdvanceExpansion_AnUnwiredOffGateSliceIsInert` (`:340`) and `TestAdvanceExpansion_AnUnreadableGateGraphNeverRaisesExplorerDemand` (`:398`, whose error still comes from an upstream pass, as its own doc comment records) stay green. Any *other* failing test is a real regression — stop and investigate.

- [ ] Build and vet.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./internal/application/parkedsensing/
  ```
  Expect no output from either.

- [ ] Commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "test(parkedsensing): prove no tick can emit explorer demand, then cut the two off-gate call sites (sp-mn3it, epic sp-sy3dl)

  The proof is written and demonstrated failing BEFORE any deletion. It asserts the
  demand sink is NEVER CALLED — stronger than 'called with zero', because a bridge
  never written reads UNREADABLE and the autosizer's explorer pass fails CLOSED on
  unreadable. All four OffGate ports are wired with live fakes so the fixture cannot
  pass vacuously through wired()==false.

  Cuts advanceOffGate, retractOffGateDemand and the reachesAny discriminator that
  existed only to feed the former. Replaces the two spend-pause tests that pinned the
  old retract-on-pause contract."
  ```

---

## Task 2 — Delete the off-gate driver and the tests that pinned it

The call sites are gone; the driver functions are now unreachable. Go does not error on an unused package-level function, so this is a separate, verifiable step.

**Files:**
- Modify: `gobot/internal/application/parkedsensing/offgate.go` — delete everything below the ranking-constant block *(advisory ~`:101-235`)*, then narrow the import block *(advisory ~`:3-8`)*
- Modify: `gobot/internal/application/parkedsensing/reach.go` — delete `reachesAny` *(advisory ~`:285-304`, and `:304` is EOF)*
- Modify: `gobot/internal/application/parkedsensing/expansion.go` — one stale doc-comment line pair *(advisory ~`:333-334`)*
- Delete: `gobot/internal/application/parkedsensing/offgate_test.go` (444 lines)

**Interfaces:**
- Consumes: nothing new
- Produces: removes `advanceOffGate(ctx context.Context, p ExpandPorts, playerID int, targets []ExpandSystem, gateReachable bool, rep *ExpandReport)`, `retractOffGateDemand(p ExpandPorts, playerID int)`, `dispatchExplorer(ctx context.Context, off OffGatePorts, playerID int, target OffGateTarget, rep *ExpandReport)`, `(*gateReach).reachesAny(ctx context.Context, targets []ExpandSystem, book *slotBook) (bool, error)`

**Steps:**

- [ ] Verify the three driver functions have zero remaining call sites. This is a *function* deadness check, and for functions a name grep is sound — unlike an interface, a function can only be reached by its name.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'advanceOffGate\|retractOffGateDemand\|dispatchExplorer\|reachesAny' --include='*.go' . || true
  ```
  **Expected hits — declarations and prose only, no call sites:**
  - `internal/application/parkedsensing/offgate.go` — the three declarations and their doc comments;
  - `internal/application/parkedsensing/reach.go` — the `reachesAny` declaration and its doc comment;
  - `internal/application/parkedsensing/expansion.go` — one stale doc comment naming `advanceOffGate` (fixed below).
  Escape clause as stated in Global Constraints.

- [ ] **Delete the driver functions** from `gobot/internal/application/parkedsensing/offgate.go` *(advisory: the blank at `:101` through EOF at `:235`)*.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/application/parkedsensing/offgate.go
  grep -c '^// retractOffGateDemand withdraws any standing explorer demand, and it is what a spend pause does$' "$F"
  grep -c '^	offGateFuelWeight    = 1$' "$F"
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // retractOffGateDemand withdraws any standing explorer demand, and it is what a spend pause does
  ```
  **TAIL:** delete through **the end of the file**.
  **LAST SURVIVING LINES** — the file must end exactly here, with `)` as its final line and **no trailing blank**:
  ```go
  const (
  	offGateWarpRangeFuel = 800
  	offGateValueWeight   = 100
  	offGateFuelWeight    = 1
  )
  ```
  Confirm:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  tail -6 internal/application/parkedsensing/offgate.go
  test -z "$(gofmt -l internal/application/parkedsensing/offgate.go)"
  ```
  Expect the const block with `)` last, and the `test -z` to pass silently. (`gofmt -l` on its own exits 0 whatever it prints, so it can never be a gate — it is wrapped here for the same reason every format gate in this plan is.)

- [ ] **Drop the now-unused imports from `offgate.go`.** The remaining content uses only `context`; `fmt` and `internal/application/logging` were used solely by the deleted functions *(advisory ~`:3-8`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'internal/application/logging' internal/application/parkedsensing/offgate.go
  ```
  FIND — **in `internal/application/parkedsensing/offgate.go` ONLY.** This is the one FIND block in this plan that is not unique across the tree: the sibling `internal/application/parkedsensing/counterstaff.go` has a byte-identical import block and must not be touched. Edit the named file; do not run a tree-wide replace.
  ```go
  import (
  	"context"
  	"fmt"

  	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
  )
  ```
  REPLACE WITH:
  ```go
  import (
  	"context"
  )
  ```

- [ ] **Delete `reachesAny`** from `gobot/internal/application/parkedsensing/reach.go` *(advisory: the blank at `:285` through EOF at `:304`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^// reachesAny reports whether ANY outstanding charting target is within this walker' internal/application/parkedsensing/reach.go
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // reachesAny reports whether ANY outstanding charting target is within this walker's reach of a
  ```
  **TAIL:** delete through **the end of the file**.
  **LAST SURVIVING LINES** — the file must end exactly here, `}` last, no trailing blank:
  ```go
  		out = append(out, c.system)
  	}
  	return out, nil
  }
  ```

- [ ] **Fix the stale doc comment** in `gobot/internal/application/parkedsensing/expansion.go` *(advisory ~`:333-334`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'advanceOffGate raises' internal/application/parkedsensing/expansion.go
  ```
  FIND:
  ```go
  //     requestSeeds writes a SPARE want the buy queue funds, advanceOffGate raises
  //     explorer demand the autosizer funds, and claimSpares deletes a placement row,
  ```
  REPLACE WITH:
  ```go
  //     requestSeeds writes a SPARE want the buy queue funds, and claimSpares deletes a placement row,
  ```
  (Task 3 edits the two lines immediately *above* these. The two edits are independent: each FIND still matches whichever runs first.)

- [ ] Delete the test file.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/application/parkedsensing/offgate_test.go
  ```

- [ ] Run the package and see it green — the seven failures recorded in Task 1 are now gone.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/application/parkedsensing/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  Expect `ok  	github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing`.

- [ ] Build, vet, format. `gofmt -w` first, then a `test -z` that actually fails non-zero.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./internal/application/parkedsensing/
  gofmt -w internal/application/parkedsensing/
  test -z "$(gofmt -l internal/application/parkedsensing/)"
  ```
  Expect no output and exit 0. If `gofmt -w` changed anything, re-read the changed file before committing — a rewrite means a deletion missed its blank separator.

- [ ] Commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "refactor(parkedsensing): delete the off-gate driver and the tests that pinned it (sp-mn3it)

  advanceOffGate, dispatchExplorer, retractOffGateDemand and gateReach.reachesAny had
  zero call sites after the previous commit — for FUNCTIONS a name grep settles deadness,
  because a function can only be reached by its name. offgate_test.go goes with them: it
  asserted the retired behaviour end to end."
  ```

---

## Task 3 — Delete `OffGatePorts`, its four interfaces, and every wiring seam above them

This is the compile-breaking task and the one the method warning is about. `OffGateSelector`, `OffGateDemandSink`, `ExplorerFinder` and `ExplorerDispatcher` are satisfied *implicitly* by adapters that never name them. Their deadness is established by finding who consumes `OffGatePorts` — and above it `ExpandPorts.OffGate` and `SensingEnginePorts.OffGate`.

**The edits in this task are content-addressed and mutually independent. Apply them in any order.** Where two edits touch the same file within a few lines of each other (`expansion.go`, `sensing_engine_ports.go`, `main.go`), the FIND blocks are written so that either order leaves the other still locatable; each such pair says so on the step.

**Files:**
- Delete: `gobot/internal/application/parkedsensing/offgate.go` (after Task 2 it holds only the four interfaces, `OffGatePorts`, `wired()` and the ranking constants, all consumed by nothing)
- Delete: `gobot/internal/application/parkedsensing/offgate_retraction_test.go` (its job is done — see the step below for why, and Task 10 for its successor)
- Modify: `gobot/internal/application/parkedsensing/expansion.go` — three edits *(advisory ~`:331-332`, ~`:305-315`, ~`:216-221`)*
- Modify: `gobot/internal/application/scouting/commands/sensing_engine_ports.go` — three edits *(advisory ~`:160`, ~`:165-169`, ~`:363`)*
- Modify: `gobot/cmd/spacetraders-daemon/main.go` — six edits *(advisory ~`:1415-1418`, ~`:1170-1192`, ~`:1164-1170`, ~`:385-393`, ~`:330-334`, ~`:36`)*
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_stall.go` — six edits *(advisory ~`:53-57`, ~`:92-93`, ~`:100`, ~`:166-172`, ~`:180`, ~`:183-188`)*
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_stall_test.go` — one atomic edit *(advisory ~`:151-186`)*
- Modify: `gobot/internal/adapters/metrics/stall_metrics_test.go` — two lines *(advisory ~`:59`, ~`:69`)* still using the `"off_gate_no_target"` reason label whose only emitter this task deletes; they are raw strings, so they keep compiling and passing — stale, not broken — and must be re-pointed at a surviving reason
- Modify: `gobot/internal/application/parkedsensing/gateread.go` — one comment *(advisory ~`:184`)*
- Modify: `gobot/internal/application/parkedsensing/expansion_gate_read_test.go` — one comment *(advisory ~`:540`)*

**Interfaces:**
- Consumes: `scoutingCmd.SensingEnginePorts`, `parkedsensing.ExpandPorts`, `parkedsensing.ExpandReport`
- Produces: removes `parkedsensing.OffGatePorts`, `parkedsensing.OffGateSelector`, `parkedsensing.OffGateDemandSink`, `parkedsensing.ExplorerFinder`, `parkedsensing.ExplorerDispatcher`, `ExpandPorts.OffGate`, `ExpandReport.OffGateDemanded/OffGateTarget/OffGateWarped`, `SensingEnginePorts.OffGate`, `stallReasonOffGateNoTarget`

**Steps:**

- [ ] **THE METHOD STEP — establish deadness by consumer of the port struct, not by name.** Do NOT grep the four interface names to decide they are dead; interfaces are satisfied implicitly and at least one adapter in this tree (`expansion.OffGateWarpTargetSelector`) satisfies `OffGateSelector` without ever naming it. Instead enumerate every consumer of the STRUCT that holds them, at each of the three levels. Each grep is run separately so a zero result cannot hide behind a later one.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  echo '--- level 1: OffGatePorts ---'
  grep -rn 'OffGatePorts' --include='*.go' . || true
  echo '--- level 2: the .OffGate FIELD (leading dot: a field READ, not the literal key) ---'
  grep -rn '\.OffGate\b' --include='*.go' . || true
  echo '--- level 3a: report flags (unique to ExpandReport) ---'
  grep -rn 'OffGateDemanded\|OffGateWarped' --include='*.go' . || true
  echo '--- level 3b: the OffGateTarget REPORT FIELD, SCOPED to internal/application/ ---'
  grep -rnE '\.OffGateTarget\b|OffGateTarget: |OffGateTarget +string' --include='*.go' internal/application/ || true
  ```
  **Expected hits, level 1:** the declaration in `offgate.go`; the field in `expansion.go`; the field in `sensing_engine_ports.go`; the literal in `main.go`; two comments (`gateread.go`, `expansion_gate_read_test.go`); and `offgate_retraction_test.go`.
  **Expected hits, level 2:** `sensing_engine_ports.go` (the `OffGate: p.OffGate,` copy-through), `offgate_retraction_test.go`, and **one COMMENT in `main.go`** inside the bridge comment block. Note what level 2 is *not*: `\.OffGate\b` **cannot** match the `main.go` ports literal, because that line is `OffGate: parkedsensing.OffGatePorts{` — a composite-literal key with no leading dot. The `main.go` comment names four things that die here (`offGateSelector`, `idleExplorerPort`, `explorerWarpDispatcher`, `SensingEnginePorts.OffGate`); it is rewritten in a step below.
  **Expected hits, level 3a:** `expansion.go` (the report fields), `probe_sensing_stall.go`, `probe_sensing_stall_test.go`.
  **Expected hits, level 3b:** `expansion.go` (`OffGateTarget   string`), `probe_sensing_stall.go` (`rep.OffGateTarget == ""`), `probe_sensing_stall_test.go` (two rows), `offgate.go` (`rep.OffGateTarget = signal.Target.SystemSymbol`).
  **Why 3b is SCOPED to `internal/application/`:** unscoped, the same anchored pattern also matches the live adapter uses of the `parkedsensing.OffGateTarget` *struct* — **nine** sites, `internal/adapters/expansion/off_gate_target.go:59,62,66,72,75,88,209` and `internal/adapters/expansion/explorer_dispatch_adapter.go:66,113`. Those files are deleted in Task 5 and are outside this task's licence; unscoped this grep is a guaranteed FALSE HALT. (An unanchored `OffGateTarget\b` is worse still — it also drags in the struct declaration Task 4 owns.)
  **Only that enumeration licenses the deletion.** If a hit falls outside these lists, apply the standing escape clause: comment-only is acceptable, code is a stop.

- [ ] Delete the interfaces file.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/application/parkedsensing/offgate.go
  ```

- [ ] Delete the Task-1 behavioural proof. **Why this is not vandalism:** it asserted "the sink is never called" through the `OffGateDemandSink` port, and that port no longer exists — an assertion about a mechanism cannot outlive the mechanism. Its job was to make the removal safe, and it did that: it was demonstrated RED against the live driver and GREEN after the cut. Its successor is the durable structural invariant in Task 10, which asserts the vocabulary is absent from the tree and therefore cannot be satisfied by anything.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/application/parkedsensing/offgate_retraction_test.go
  ```

- [ ] **Fix the second stale sentence in the tick's doc comment.** `gobot/internal/application/parkedsensing/expansion.go` *(advisory ~`:331-332`)*. Task 2 rewrote the two lines immediately *below* these; the two edits are independent and either order works.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'the seed machinery and the off-gate' internal/application/parkedsensing/expansion.go
  ```
  FIND:
  ```go
  //   - SPEND-INTENT, run only when SpendEnabled: the seed machinery and the off-gate
  //     fallback. None spends a credit DIRECTLY, but each asks an engine that can —
  ```
  REPLACE WITH:
  ```go
  //   - SPEND-INTENT, run only when SpendEnabled: the seed machinery. It spends no credit
  //     DIRECTLY, but it asks engines that can —
  ```

- [ ] **Remove the `ExpandReport` fields.** Same file *(advisory ~`:305-315`)*. The struct's closing `}` is inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^	OffGateWarped   int$' internal/application/parkedsensing/expansion.go
  ```
  FIND:
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
  }
  ```
  REPLACE WITH:
  ```go
  }
  ```

- [ ] **Remove the `ExpandPorts.OffGate` field and fix the dangling cross-reference above it — ONE edit.** Same file *(advisory ~`:216-221`)*. The struct's closing `}` is inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'in the same spirit as OffGatePorts' internal/application/parkedsensing/expansion.go
  ```
  FIND:
  ```go
  	// unaffected, in the same spirit as OffGatePorts.
  	GateRead GateReader
  	// OffGate is the warp-expansion slice: the ports that raise explorer demand and
  	// warp an explorer past a sealed gate frontier. See offgate.go.
  	OffGate OffGatePorts
  }
  ```
  REPLACE WITH:
  ```go
  	// unaffected.
  	GateRead GateReader
  }
  ```
  (The sentence this terminates already reads "…the pass does nothing and the rest of the tick is unaffected", two lines up, so the clause is simply dropped rather than restated.)

- [ ] **Remove the `SensingEnginePorts` copy-through.** `gobot/internal/application/scouting/commands/sensing_engine_ports.go` *(advisory ~`:362-363`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^		OffGate:     p.OffGate,$' internal/application/scouting/commands/sensing_engine_ports.go
  ```
  FIND:
  ```go
  		ListingMemo: p.ListingMemo,
  		OffGate:     p.OffGate,
  ```
  REPLACE WITH:
  ```go
  		ListingMemo: p.ListingMemo,
  ```

- [ ] **Remove the `SensingEnginePorts.OffGate` field and fix the cross-reference above it — ONE edit.** Same file *(advisory ~`:160-170`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'for the same reason OffGate is not' internal/application/scouting/commands/sensing_engine_ports.go
  ```
  FIND:
  ```go
  	// Deliberately NOT in the ready() check below, for the same reason OffGate is not: the rest of
  	// the tick must keep running on a daemon whose gate resolver is absent, and the pass is inert
  	// until it is present. The daemon wires it unconditionally.
  	GateRead  parkedsensing.GateReader
  	Uncharted parkedsensing.UnchartedCatalog
  	// OffGate is the warp-expansion slice: the ports that raise explorer demand onto the fleet
  	// autosizer's buy bridge and warp an explorer past a sealed gate frontier. Deliberately NOT in
  	// the ready() check below — the gate passes must keep running on a daemon whose off-gate
  	// collaborators are absent, and the slice is inert until all four are present.
  	OffGate  parkedsensing.OffGatePorts
  	SeedShip parkedsensing.SeedCommander
  ```
  REPLACE WITH:
  ```go
  	// Deliberately NOT in the ready() check below: the rest of the tick must keep running on a
  	// daemon whose gate resolver is absent, and the pass is inert until it is present. The daemon
  	// wires it unconditionally.
  	GateRead  parkedsensing.GateReader
  	Uncharted parkedsensing.UnchartedCatalog
  	SeedShip  parkedsensing.SeedCommander
  ```
  **`gofmt` WILL rewrite this struct, and that is EXPECTED here — it is the one place in this plan where it is.** Removing the comment merges two field-alignment groups (`GateRead`/`Uncharted` with `SeedShip`/`Scan`/`SpreadOf`), so gofmt re-pads the whole run. Run it immediately and read the result:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  gofmt -w internal/application/scouting/commands/sensing_engine_ports.go
  git -C /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec diff -- gobot/internal/application/scouting/commands/sensing_engine_ports.go
  ```
  Expect the diff to show ONLY the deleted comment + field and re-padding of the surviving field names. Anything else means the FIND matched the wrong region.

- [ ] **Remove the daemon `sensingWiring` assignments.** `gobot/cmd/spacetraders-daemon/main.go` *(advisory ~`:1414-1419`)*. Both blank separators are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^		offGateSelect: offGateSelector,$' cmd/spacetraders-daemon/main.go
  ```
  FIND:
  ```go
  		remoteMarket: remoteMarketPort,

  		offGateSelect: offGateSelector,
  		offGateDemand: explorerOffGateBridge,
  		idleExplorer:  idleExplorerPort,
  		explorerWarp:  explorerWarpDispatcher,

  		db:              db,
  ```
  REPLACE WITH:
  ```go
  		remoteMarket: remoteMarketPort,

  		db:              db,
  ```

- [ ] **Remove the `OffGatePorts` literal.** Same file *(advisory ~`:385-393`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'OffGate: parkedsensing.OffGatePorts{' cmd/spacetraders-daemon/main.go
  ```
  FIND:
  ```go
  		Uncharted: catalog,
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
  REPLACE WITH:
  ```go
  		Uncharted: catalog,
  ```

- [ ] **Remove the `sensingWiring` struct fields.** Same file *(advisory ~`:329-335`)*. One of the two blank separators is inside the block; leaving both would give two consecutive blanks and `gofmt` would rewrite.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^	offGateSelect \*expansionAdapters.OffGateWarpTargetSelector$' cmd/spacetraders-daemon/main.go
  ```
  FIND:
  ```go
  	remoteMarket *parkedSensingAdapters.RemoteMarketPort

  	offGateSelect *expansionAdapters.OffGateWarpTargetSelector
  	offGateDemand *expansionAdapters.ExplorerOffGateBridge
  	idleExplorer  *expansionAdapters.IdleExplorerPort
  	explorerWarp  *expansionAdapters.ExplorerWarpDispatcher

  	db              *gorm.DB
  ```
  REPLACE WITH:
  ```go
  	remoteMarket *parkedSensingAdapters.RemoteMarketPort

  	db              *gorm.DB
  ```

- [ ] **Delete the three now-unused constructions AND the comment block above them.** Same file *(advisory ~`:1170-1193`)*. `offGateSelector`, `idleExplorerPort` and `explorerWarpDispatcher` become declared-and-unused locals the moment their assignments are gone — that IS a Go compile error, so they go here, not in Task 5.
  **THIS IS THE STEP THAT PREVIOUSLY DELETED LIVE CODE.** An earlier draft addressed it as "lines 1172-1192" and applied it after other `main.go` cuts had shifted the file 13 lines; the range then landed on `reachableYardFinder` and `heavyTargetFinder`, both still consumed at the handler call below. The FIND block below makes that impossible: it is anchored at the top on `explorerOffGateBridge := …` (which SURVIVES this task) and at the bottom on `reachableYardFinder := …` (which SURVIVES this task), and both are written back unchanged.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^	// OFF-GATE WARP EXPANSION, write side' cmd/spacetraders-daemon/main.go
  ```
  FIND (the blank line after the bridge construction is inside the block; so is the blank before `reachableYardFinder`):
  ```go
  	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()

  	// OFF-GATE WARP EXPANSION, write side. The fleet's 56-system ledger sits behind 50 outbound
  	// gate edges of which ALL 50 are under construction, so gate expansion is finished and warp is
  	// the only exit.
  	//
  	//   - offGateSelector ranks gate-unreachable systems by exploration value against warp fuel,
  	//     joining the universe roster against the stored gate graph. Its roster read is a cached
  	//     whole-universe crawl (long TTL), NOT a per-tick fetch.
  	//   - idleExplorerPort finds the bought+dedicated explorer to warp — idle-only, which is what
  	//     makes the dispatch idempotent without any cross-tick state.
  	//   - explorerWarpDispatcher resolves an arrival waypoint FIRST (fail-closed: an uncharted
  	//     destination warps nothing) and then runs the warp on a background goroutine, so the
  	//     sensing tick never waits out a flight.
  	//
  	// The dispatcher is handed the daemon's lifetime context by the sensing coordinator, so a warp
  	// survives the tick that launched it and is cancelled only on shutdown.
  	offGateSelector := expansionAdapters.NewOffGateWarpTargetSelector(
  		expansionAdapters.NewUniverseSystemsCache(apiClient, playerRepo, nil, 0),
  		gateGraphService,
  	)
  	idleExplorerPort := expansionAdapters.NewIdleExplorerPort(shipRepo)
  	explorerWarpDispatcher := expansionAdapters.NewExplorerWarpDispatcher(routeExecutor, shipRepo, warpWaypointSource)

  	reachableYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)
  ```
  REPLACE WITH:
  ```go
  	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()

  	reachableYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)
  ```
  **`explorerOffGateBridge` STAYS in this task** — the handler argument below still consumes it, and Task 6 removes both together.

- [ ] **Rewrite the bridge comment block.** Same file *(advisory ~`:1164-1170`)*. It currently names `offGateSelector`, `idleExplorerPort`, `explorerWarpDispatcher` and `SensingEnginePorts.OffGate` — all four of which this task deletes. This edit and the one above share the `explorerOffGateBridge := …` anchor line, which neither changes, so either order works.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'The cross-coordinator off-gate demand bridge the FLEET autosizer' cmd/spacetraders-daemon/main.go
  ```
  FIND:
  ```go
  	// The cross-coordinator off-gate demand bridge the FLEET autosizer's explorer BUY path
  	// reads. Its only writer is the probe-sensing expansion pass: see offGateSelector /
  	// idleExplorerPort / explorerWarpDispatcher below, handed to the sensing coordinator's
  	// SensingEnginePorts.OffGate. Leave the bridge without a writer and it never sees a first
  	// emit, so it reads UNREADABLE (ok=false), the explorer buy fails closed, and the whole
  	// path goes dormant without a single error.
  	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()
  ```
  REPLACE WITH:
  ```go
  	// The cross-coordinator off-gate demand bridge the FLEET autosizer's explorer BUY path
  	// reads. It has NO writer: off-gate warp expansion is retired (sp-mn3it), so the bridge
  	// never sees a first emit, reads UNREADABLE (ok=false), and the explorer buy fails closed.
  	// Deleted with the whole read chain in the next commit.
  	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()
  ```
  (This is a live-code comment, not archaeology: it is what makes the intermediate commit's money-safety legible. It disappears with the bridge in Task 6.)

- [ ] **Delete the now-orphaned `parkedsensing` import.** Same file *(advisory ~`:36`)*. The ports literal was the file's only use of the identifier, so the import is now unused — a Go compile error.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^	parkedsensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"$' cmd/spacetraders-daemon/main.go
  ```
  FIND (delete this line):
  ```go
  	parkedsensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
  ```
  Then confirm — **use the IDENTIFIER test, not a bare package-name grep**:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -n 'parkedsensing\.' cmd/spacetraders-daemon/main.go || echo "NO parkedsensing IDENTIFIER USES — correct"
  ```
  Expect `NO parkedsensing IDENTIFIER USES — correct`. **A bare `grep -n 'parkedsensing' main.go` does NOT go silent and must not be used as the check:** `:17` is the `parkedSensingAdapters` import path (`internal/adapters/parkedsensing`) and `:1382` is an unrelated comment, both containing the lowercase string, so that grep exits 0 both before and after this edit.

- [ ] **Remove the stall-verdict reads.** `gobot/internal/application/scouting/commands/probe_sensing_stall.go` — six independent content-addressed edits, any order.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/application/scouting/commands/probe_sensing_stall.go
  grep -c 'stallReasonOffGateNoTarget health.StallReason' "$F"
  grep -c 'a frontier system discovered, a warp' "$F"
  grep -c 't.expand.OffGateWarped > 0' "$F"
  grep -c 'expansionStallVerdict maps one off-gate/expansion pass' "$F"
  grep -c 'rep.OffGateWarped > 0' "$F"
  grep -c 'rep.OffGateDemanded && rep.OffGateTarget' "$F"
  ```
  **(1)** FIND *(advisory ~`:53-57`)*:
  ```go
  	// stallReasonOffGateNoTarget: THE MEASURED PRODUCTION FAILURE. The gate-reachable frontier is
  	// exhausted — there IS charting work and not one target is within gate reach — and no warp
  	// target could be selected either. The fleet is sealed in whatever pocket it currently holds,
  	// and every layer reports it as "0 discovered".
  	stallReasonOffGateNoTarget health.StallReason = "off_gate_no_target"
  ```
  DELETE this block.
  **(2)** FIND *(advisory ~`:92-93`)*:
  ```go
  // bought or re-tasked, a claim reaped, a placement advanced, a frontier system discovered, a warp
  // dispatched. The rotation size is deliberately NOT an effect — a steady rotation is what an idle
  ```
  REPLACE WITH:
  ```go
  // bought or re-tasked, a claim reaped, a placement advanced, a frontier system discovered. The
  // rotation size is deliberately NOT an effect — a steady rotation is what an idle
  ```
  **(3)** FIND *(advisory ~`:99-101`; the closing `}` of `anyEffect` is inside the block)*:
  ```go
  		t.expand.Actions > 0 || t.expand.Discovered > 0 || t.expand.OffGateWarped > 0
  }
  ```
  REPLACE WITH:
  ```go
  		t.expand.Actions > 0 || t.expand.Discovered > 0
  }
  ```
  **(4)** FIND *(advisory ~`:166-172`; the `func` line is inside the block and is written back unchanged)*:
  ```go
  // expansionStallVerdict maps one off-gate/expansion pass to its verdict.
  //
  // The off-gate no-target case is checked AFTER progress for the same reason failures are: a
  // frontier that is still charting is not sealed, whatever the warp selector did. It is checked
  // BEFORE idle because "demand raised, no target found" is the exact shape that reads as a fully
  // charted galaxy while a jump gate sits unread.
  func expansionStallVerdict(rep parkedsensing.ExpandReport, err error) health.TickOutcome {
  ```
  REPLACE WITH:
  ```go
  // expansionStallVerdict maps one expansion pass to its verdict: failures first, then progress,
  // then idle.
  //
  // A sealed gate frontier is now a PERMANENT, DELIBERATE state — the gate network is the reachable
  // universe (sp-mn3it) — so a pass with nothing left to do reads IDLE rather than blocked. There is
  // no action left to take, and a verdict that reported that condition BLOCKED on every tick would
  // be a standing false alarm.
  func expansionStallVerdict(rep parkedsensing.ExpandReport, err error) health.TickOutcome {
  ```
  **(5)** FIND *(advisory ~`:180`)*:
  ```go
  	if rep.Actions > 0 || rep.Discovered > 0 || rep.OffGateWarped > 0 || rep.SeedsRequested > 0 || rep.SeedsClaimed > 0 || rep.MarketsFound > 0 {
  ```
  REPLACE WITH:
  ```go
  	if rep.Actions > 0 || rep.Discovered > 0 || rep.SeedsRequested > 0 || rep.SeedsClaimed > 0 || rep.MarketsFound > 0 {
  ```
  **(6)** FIND *(advisory ~`:183-188`; BOTH the `if`'s closing `}` and the function's closing `}` are inside the block, and `return health.TickIdle()` is written back)*:
  ```go
  	if rep.OffGateDemanded && rep.OffGateTarget == "" {
  		return health.TickBlocked(stallReasonOffGateNoTarget,
  			"the gate-reachable frontier is exhausted and NO warp target could be selected — the fleet is sealed in the systems it already holds, and every layer reports this as '0 discovered'")
  	}
  	return health.TickIdle()
  }
  ```
  REPLACE WITH:
  ```go
  	return health.TickIdle()
  }
  ```

  **Why this is correct and not a lost alarm:** `off_gate_no_target` fired when the gate-reachable frontier was exhausted *and* no warp target could be selected. With off-gate warp retired, "the gate network is the whole reachable universe" is the accepted, deliberate steady state (spec: *"Any system not gate-connected is unreachable by design"*). A verdict that reported that permanent condition as BLOCKED on every tick would be a standing false alarm, which is worse than idle.

- [ ] **Rewrite the stall verdict table — ONE atomic edit.** `gobot/internal/application/scouting/commands/probe_sensing_stall_test.go` *(advisory ~`:151-186`)*. Three rows go, the fourth is kept with the retired field stripped and the case renamed, and the table's doc comment is rewritten — all in a single FIND/REPLACE, so no ordering question arises.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^// THE OFF-GATE PRODUCTION FAILURE, and the rest of' internal/application/scouting/commands/probe_sensing_stall_test.go
  ```
  FIND:
  ```go
  // THE OFF-GATE PRODUCTION FAILURE, and the rest of the expansion pass's verdict table.
  //
  // Driven against the pass's REPORT rather than through a synthesised 400-line expansion fixture:
  // the report is the DTO the engine hands the coordinator, and the behaviour under test is the
  // coordinator's projection of it. Reaching OffGateDemanded through the real engine would require
  // standing up a ledger, a gate graph, a slot book and a target ordering — i.e. testing the
  // expansion engine, which owns its own tests, rather than this mapping.
  func TestExpansionStallVerdictTable(t *testing.T) {
  	cases := []struct {
  		name       string
  		rep        parkedsensing.ExpandReport
  		err        error
  		wantStatus health.StallOutcome
  		wantReason health.StallReason
  	}{
  		{
  			name:       "sealed pocket: demand raised, no warp target — the 33-system region behind an unread gate",
  			rep:        parkedsensing.ExpandReport{OffGateDemanded: true},
  			wantStatus: health.StallBlocked,
  			wantReason: stallReasonOffGateNoTarget,
  		},
  		{
  			name:       "off-gate demand WITH a target is not a stall — the fleet has somewhere to go",
  			rep:        parkedsensing.ExpandReport{OffGateDemanded: true, OffGateTarget: "X1-QQ9"},
  			wantStatus: health.StallIdle,
  		},
  		{
  			name:       "a dispatched warp is progress",
  			rep:        parkedsensing.ExpandReport{OffGateDemanded: true, OffGateTarget: "X1-QQ9", OffGateWarped: 1},
  			wantStatus: health.StallProgress,
  		},
  		{
  			name:       "charting progress outranks a failed selector — a moving frontier is not sealed",
  			rep:        parkedsensing.ExpandReport{Discovered: 2, OffGateDemanded: true},
  			wantStatus: health.StallProgress,
  		},
  ```
  REPLACE WITH:
  ```go
  // The expansion pass's verdict table.
  //
  // Driven against the pass's REPORT rather than through a synthesised 400-line expansion fixture:
  // the report is the DTO the engine hands the coordinator, and the behaviour under test is the
  // coordinator's projection of it, not the expansion engine — which owns its own tests.
  func TestExpansionStallVerdictTable(t *testing.T) {
  	cases := []struct {
  		name       string
  		rep        parkedsensing.ExpandReport
  		err        error
  		wantStatus health.StallOutcome
  		wantReason health.StallReason
  	}{
  		{
  			name:       "charting progress is progress — a moving frontier is not idle",
  			rep:        parkedsensing.ExpandReport{Discovered: 2},
  			wantStatus: health.StallProgress,
  		},
  ```

- [ ] **Re-point the stale stall-metric label.** `gobot/internal/adapters/metrics/stall_metrics_test.go` *(advisory ~`:59`, ~`:69`)*. This task deletes the only emitter of `"off_gate_no_target"`; these are **raw strings**, so the test keeps compiling and keeps passing — it is stale, not broken, and no build gate will find it. Re-point both at a reason that still exists (`stallReasonExpansionError` = `"expansion_error"`), keeping the two label sets distinct so the test still covers "one series drains to 0, another holds its streak".
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/adapters/metrics/stall_metrics_test.go
  grep -c 'c.RecordStallStreak("off_gate_expansion", "", "off_gate_no_target", 3)' "$F"
  grep -c 'live off-gate block holds its streak' "$F"
  ```
  **(1)** FIND:
  ```go
  	c.RecordStallStreak("off_gate_expansion", "", "off_gate_no_target", 3)
  ```
  REPLACE WITH:
  ```go
  	c.RecordStallStreak("parked_sensing", "", "expansion_error", 3)
  ```
  **(2)** FIND:
  ```go
  		{"live off-gate block holds its streak", map[string]string{"coordinator": "off_gate_expansion", "scope": "", "reason": "off_gate_no_target"}, 3},
  ```
  REPLACE WITH:
  ```go
  		{"live expansion block holds its streak", map[string]string{"coordinator": "parked_sensing", "scope": "", "reason": "expansion_error"}, 3},
  ```
  Then confirm — **and note the expected SURVIVING hit**:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'off_gate_no_target\|off_gate_expansion' --include='*.go' . || true
  ```
  **Expected: exactly ONE hit** — `internal/application/scouting/commands/probe_sensing_stall.go:29`, `expansionStallCoordinator = "off_gate_expansion"`. **That constant is LIVE and out of scope**: it is the coordinator label the expansion pass reports its stall streak under, it has nothing to do with the retired warp slice, and renaming it would break the operator's Grafana series. `off_gate_no_target` must return **zero** hits. Do not expect a silent grep here — an earlier draft did, and it was wrong.

- [ ] **Fix the two remaining comment cross-references.**
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'That is the same contract OffGatePorts carries' internal/application/parkedsensing/gateread.go
  grep -c 'the same contract OffGatePorts carries' internal/application/parkedsensing/expansion_gate_read_test.go
  ```
  **(1)** `gobot/internal/application/parkedsensing/gateread.go` *(advisory ~`:183-184`)* — FIND:
  ```go
  // A NIL READER IS A WIRING GAP, NOT A SWITCH, and the pass then costs literally nothing — not even the
  // per-system mapping sweep. That is the same contract OffGatePorts carries.
  ```
  REPLACE WITH:
  ```go
  // A NIL READER IS A WIRING GAP, NOT A SWITCH, and the pass then costs literally nothing — not even the
  // per-system mapping sweep.
  ```
  **(2)** `gobot/internal/application/parkedsensing/expansion_gate_read_test.go` *(advisory ~`:539-540`)* — FIND:
  ```go
  // WITH NO READER WIRED THE TICK IS EXACTLY WHAT IT WAS. A nil port is a wiring gap, not a switch, and
  // the pass degrades to doing nothing rather than panicking — the same contract OffGatePorts carries.
  ```
  REPLACE WITH:
  ```go
  // WITH NO READER WIRED THE TICK IS EXACTLY WHAT IT WAS. A nil port is a wiring gap, not a switch, and
  // the pass degrades to doing nothing rather than panicking.
  ```

- [ ] Build and vet the whole module — this is the step that catches every seam the greps missed. `go build` skips `_test.go`, so vet is mandatory here.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./...
  ```
  Expect no output from either.

- [ ] Run the affected packages.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/application/parkedsensing/ ./internal/application/scouting/... ./internal/adapters/metrics/ ./cmd/spacetraders-daemon/ 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  Expect an `ok` for each and no `FAIL`.

- [ ] Format and commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  gofmt -w .
  test -z "$(gofmt -l .)"
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "refactor(parkedsensing,scouting,daemon): delete OffGatePorts, its four interfaces and every wiring seam (sp-mn3it)

  Deadness established by enumerating consumers of the PORT STRUCT at all three levels
  (OffGatePorts, ExpandPorts.OffGate, SensingEnginePorts.OffGate) — NOT by grepping the
  interface names, which would be unsound: interfaces are satisfied implicitly and
  expansion.OffGateWarpTargetSelector satisfies OffGateSelector without naming it.

  Also drops the off_gate_no_target stall verdict. A sealed gate frontier is now a
  permanent deliberate state, so reporting it BLOCKED every tick would be a standing
  false alarm; idle is the honest verdict. The expansionStallCoordinator label
  ('off_gate_expansion') is LEFT ALONE — it names the coordinator, not the retired
  slice, and the operator's dashboards key on it. The stall-metrics test row that
  carried the retired reason is re-pointed at a surviving one — a raw string, so no
  build gate would ever have found it."
  ```

---

## Task 4 — Delete `offgate_types.go`, including the two orphaned declarations

`OffGateTargetSelector` and `ShipyardCoverageReader` were declared and never wired — they document a trigger that was never implemented. They are **not** the live ports: the live selector was `OffGateSelector` in `offgate.go`, already gone.

**Files:**
- Delete: `gobot/internal/application/parkedsensing/offgate_types.go` (71 lines: `OffGateTarget`, `OffGateSelectionParams`, `OffGateTargetSelector`, `ShipyardCoverageReader`, `OffGateDemandSignal`)

**Interfaces:**
- Consumes: nothing
- Produces: removes `parkedsensing.OffGateTarget`, `parkedsensing.OffGateSelectionParams`, `parkedsensing.OffGateTargetSelector`, `parkedsensing.ShipyardCoverageReader`, `parkedsensing.OffGateDemandSignal`

**Steps:**

- [ ] **Apply the method warning to the two orphans specifically.** `OffGateTargetSelector` and `ShipyardCoverageReader` are interfaces, so a name grep cannot prove nothing satisfies them — but for these two the *inverse* argument settles it, and it must be made explicitly: an implicitly-satisfied interface is only reachable if some value is assigned to a variable, field or parameter **of that interface type**. Enumerate every such site:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'OffGateTargetSelector\|ShipyardCoverageReader\|GateShipyardsScanExhausted' --include='*.go' . || true
  ```
  **Expected hits — exactly these two files, declarations and prose only:**
  - `internal/application/parkedsensing/offgate_types.go:44,48,52,57,58` — the two declarations and their doc comments;
  - `internal/adapters/expansion/off_gate_target.go:45` — **a COMMENT**, reading `// coordinator's commands.OffGateTargetSelector driven port. It SELECTS only — nothing warps.` It is an "implements" claim by SHAPE only: nothing in that file names the type in code, and the file itself dies in Task 5. Note it and move on; it is not a consumer.

  **Zero fields, zero parameters, zero variables of these types anywhere.** With no site of the interface type, no value can ever be assigned to one, so no implementation is reachable regardless of what shapes exist. That is the sound form of the argument; "no name references, therefore dead" is not.

- [ ] Enumerate the consumers of the three remaining value types in the file. These are structs, not interfaces, so a name grep IS sound for them.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'OffGateTarget\b\|OffGateSelectionParams\|OffGateDemandSignal' --include='*.go' . || true
  ```
  **Expected hits:** `internal/application/parkedsensing/offgate_types.go` (the declarations) and `internal/adapters/expansion/{off_gate_target.go,off_gate_target_test.go,explorer_dispatch_adapter.go,explorer_offgate_bridge.go}` — the adapters, which Tasks 5 and 6 delete.

  **On the ordering, honestly stated:** every one of those four consumers dies in Task 5 or Task 6, so running *this* task **after** them would leave zero churn, not more — an early draft's rationale was backwards. Task 4 runs here for a different and better reason: it keeps the deletion **bottom-up through the dependency graph** (the vocabulary the adapters import goes first, so each adapter deletion afterwards is a plain removal rather than a fix-up), and it groups the whole non-compiling window into one reviewable commit (Tasks 4-6). The cost is a deliberate, bounded, two-task period where the tree does not build.

- [ ] Delete the file. The `internal/adapters/expansion` package will not compile until Tasks 5 and 6 land — that is expected and is why the compile gate below is scoped.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/application/parkedsensing/offgate_types.go
  ```

- [ ] Prove the parkedsensing package itself is clean and self-contained.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./internal/application/parkedsensing/...
  go test ./internal/application/parkedsensing/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  Expect no build output and one `ok` line.

- [ ] Confirm the only remaining breakage is the adapters Tasks 5 and 6 delete — nothing else. **`go build` is EXPECTED to fail here**, so its failure is absorbed explicitly; the `cd` above is not.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./... 2>&1 | grep -E '^[^ ]' | sort -u || true
  ```
  Expect errors reported ONLY for files under `internal/adapters/expansion/` (`off_gate_target.go`, `explorer_dispatch_adapter.go`, `explorer_offgate_bridge.go`). Any other package named here is a seam Task 3 missed — stop and fix it before continuing. **An EMPTY result is also a failure**: it would mean the build unexpectedly succeeded, i.e. the file was not actually deleted.

- [ ] Do NOT commit yet — the tree does not build. **Task 6 completes the compile, not Task 5.** `explorer_offgate_bridge.go` references the deleted `parkedsensing` types at `:14`, `:27`, `:34`, `:39` and `:41`, and that file is deleted in **Task 6** — so `internal/adapters/expansion` is still broken at the end of Task 5, by design.

---

## Task 5 — Delete the explorer-only adapters, each verified individually

Three adapters go here; the fourth (the bridge) goes in Task 6 because it drags the autosizer read side with it. **Verify each on its own before deleting it — do not batch.**

**Files:**
- Delete: `gobot/internal/adapters/expansion/idle_explorer_port.go` (71 lines)
- Delete: `gobot/internal/adapters/expansion/explorer_dispatch_adapter.go` (140 lines)
- Delete: `gobot/internal/adapters/expansion/off_gate_target.go` (214 lines)
- Delete: `gobot/internal/adapters/expansion/off_gate_target_test.go` (234 lines)

**Interfaces:**
- Consumes: `ship.RouteExecutor.ExecuteWarpRoute` (via `warpRouteRunner`) — note this is `ExecuteWarpRoute`'s **only** non-test call site in the tree; deleting the dispatcher leaves the method caller-less, which is the accepted post-state (sp-bwzy3), not a defect. Also `navigation.ShipRepository`, `gategraph` adjacency
- Produces: removes `expansion.IdleExplorerPort` + `NewIdleExplorerPort(ships idleShipReader) *IdleExplorerPort`, `expansion.ExplorerWarpDispatcher` + `NewExplorerWarpDispatcher(routes warpRouteRunner, ships shipBySymbolReader, arrivals arrivalWaypointResolver) *ExplorerWarpDispatcher`, `expansion.OffGateWarpTargetSelector` + `NewOffGateWarpTargetSelector(universe UniverseSystemsProvider, gateGraph gateAdjacencyReader) *OffGateWarpTargetSelector`, `expansion.UniverseSystemsProvider`

**Steps:**

- [ ] **Verify `idle_explorer_port.go` individually.** Its only consumer was the daemon construction, removed in Task 3.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'IdleExplorerPort\|NewIdleExplorerPort\|idleShipReader\|explorerDedicatedFleet' --include='*.go' . || true
  ```
  **Expected hits:** only inside `internal/adapters/expansion/idle_explorer_port.go`. Note it is the sole consumer of `hullbuy.DedicatedFleet(hullbuy.HullClassExplorer)` outside `hullbuy` itself — deleting it is what makes that constant inert (and deferrable to slice 4).

- [ ] Delete it and confirm the package's other files do not reference it.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/adapters/expansion/idle_explorer_port.go
  grep -rn 'IdleExplorer' --include='*.go' internal/adapters/ || echo "NO IdleExplorer REFERENCES IN internal/adapters/ — correct"
  ```
  Expect `NO IdleExplorer REFERENCES IN internal/adapters/ — correct`.

- [ ] **Verify `explorer_dispatch_adapter.go` individually.** Its only consumer was the daemon construction, removed in Task 3. It is the one deletion that touches the shared warp stack's caller set, so check what it consumes before removing it.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'ExplorerWarpDispatcher\|NewExplorerWarpDispatcher\|warpRouteRunner\|arrivalWaypointResolver\|shipBySymbolReader' --include='*.go' . || true
  echo '--- what it calls into the shared warp stack ---'
  grep -n 'ExecuteWarpRoute\|routes\.' internal/adapters/expansion/explorer_dispatch_adapter.go || true
  ```
  **Expected hits, first grep:** only inside `internal/adapters/expansion/explorer_dispatch_adapter.go`.
  **Expected hits, second grep:** `:23` and `:44` (comments), `:27` (a comment naming the port), `:29` (the `warpRouteRunner` interface method it *requires*), and `:98` (`d.routes.ExecuteWarpRoute(...)`, the call). This confirms the file is a *caller* of `ExecuteWarpRoute`, not a definer.

- [ ] Delete it, then immediately prove the **manual verb's** entrypoint survives.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/adapters/expansion/explorer_dispatch_adapter.go
  echo '--- THE TRIPWIRE: the manual verb rides ExecuteWarpLeg ---'
  grep -rn 'ExecuteWarpLeg' --include='*.go' internal/application/ship/commands/navigation/
  ```
  **THE TRIPWIRE IS ON `ExecuteWarpLeg`, NOT `ExecuteWarpRoute`.** Expect the `WarpLegExecutor` interface declaring it (`warp_ship.go:16-17`) and the live call at `warp_ship.go:98`. **If either is missing, stop** — the retirement has cut the manual verb's path, which it must not. (Note this grep is deliberately NOT `|| true`: zero hits here IS the failure.) Task 9 is the full proof; this is the early tripwire.

  **`ExecuteWarpRoute` now has ZERO non-test callers, and that is the expected post-state — do NOT stop on it.** `explorer_dispatch_adapter.go:98` was its only one. The method stays (Orientation, "DECISION, already taken"); its fate is filed as **sp-bwzy3**. A `grep -rn 'ExecuteWarpRoute' … | grep -v _test.go` at this point returns only the definition at `route_executor_warp.go:41` and comments — that is correct, not a failure.

- [ ] **Verify `off_gate_target.go` individually.** Its only consumer was the daemon construction, removed in Task 3.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'OffGateWarpTargetSelector\|NewOffGateWarpTargetSelector\|UniverseSystemsProvider\|betterOffGateTarget\|frontierEdges\|nearestEdgeWarp\|explorationValue' --include='*.go' . || true
  echo '--- is gateAdjacencyReader shared with another file in the package? ---'
  grep -rn 'gateAdjacencyReader' --include='*.go' internal/adapters/expansion/ || true
  ```
  **Expected hits, first grep:** only inside `internal/adapters/expansion/off_gate_target.go` and `off_gate_target_test.go`.
  **Second grep:** if `gateAdjacencyReader` is declared in `off_gate_target.go` but used by another file in the package, move the declaration to that file rather than deleting it.

- [ ] Delete the selector and its test.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/adapters/expansion/off_gate_target.go internal/adapters/expansion/off_gate_target_test.go
  ```

- [ ] Confirm the only remaining breakage is the bridge, which Task 6 removes. **`go build` is EXPECTED to fail here.**
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./... 2>&1 | grep -E '^[^ ]' | sort -u || true
  ```
  Expect errors reported ONLY for `internal/adapters/expansion/explorer_offgate_bridge.go` (it imports `parkedsensing` for the types deleted in Task 4) and `internal/adapters/expansion/universe_systems.go` if it referenced anything from the deleted selector. Anything else is a missed seam — stop. **An EMPTY result is a failure**, not a pass: it would mean the deletions did not take.

- [ ] Do NOT commit yet — the tree does not build. Task 6 completes the compile.

---

## Task 6 — Delete the latching bridge and the autosizer read chain it forces

`ExplorerOffGateBridge` is the bridge itself. Its read side (`ExplorerDemand`) is consumed by `fleetCmd.NewExplorerDemandProvider`, which is registered from `NewFleetAutosizerCoordinatorHandler`'s `offGateDemand` parameter. Removing the bridge makes that parameter unsatisfiable — the compiler, not a scope choice, drags the whole read chain in with it.

**Files:**
- Delete: `gobot/internal/adapters/expansion/explorer_offgate_bridge.go` (59 lines)
- Delete: `gobot/internal/application/fleet/commands/fleet_autosizer_explorer.go` (116 lines)
- Delete: `gobot/internal/application/fleet/commands/fleet_autosizer_explorer_test.go` (143 lines)
- Delete: `gobot/internal/application/fleet/commands/fleet_autosizer_explorer_wiring_test.go` — **the whole file, unconditionally.** It holds **four** tests, not two: `TestExplorer_ClassDisabled_OptInDefaultOff` (`:11`), `TestExplorer_ResolveDefaults_NothingBootArms` (`:24`), `TestExplorer_ClassGuardConfig_RealBounds` (`:50`) and `TestExplorer_Reconcile_DisarmedSkipsClassEntirely` (`:67`), plus the `spyExplorerProvider` fixture (`:94-103`). Every one of them is explorer-only; nothing in the file survives the retirement.
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_ports.go` — three edits *(advisory ~`:32-34`, ~`:44-46`, ~`:62-69`)*
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_demand_ports.go` — one edit *(advisory ~`:122-136`)*
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_heavy_wiring_test.go` — one edit *(advisory ~`:65-67`)*. `go build` will NOT catch this; only `go vet ./...` or `go test` will
- Modify: `gobot/cmd/spacetraders-daemon/main.go` — two edits. **Both have already moved by Task 3.** Post-Task-3 the bridge block sits at roughly `:1149-1153` and the handler argument at roughly `:1172` (base-tree `:1164-1170` and `:1212`, shifted by Task 3's 15 deleted lines above them, its 3-line comment shrink and its 22-line construction cut). Those arithmetic estimates are **advisory and may be off by one**; the FIND text below is the authority and is anchored on lines that survive

**Interfaces:**
- Consumes: `fleetCmd.RunFleetAutosizerCoordinatorHandler.AddDemandProvider(p ClassDemandProvider)`
- Produces: `NewFleetAutosizerCoordinatorHandler(server *DaemonServer, apiClient *api.SpaceTradersClient, ledgerTreasury *persistence.LedgerTreasury, shipRepo navigation.ShipRepository, med common.Mediator, waypointRepo *persistence.GormWaypointRepository, eventStore captain.EventStore, marketRepo market.MarketRepository, scannedYards scannedYardRanker, heavyYards heavyYardInventory) *fleetCmd.RunFleetAutosizerCoordinatorHandler` — **one parameter shorter**. Removes `expansion.ExplorerOffGateBridge`, `fleetCmd.OffGateDemandSource`, `fleetCmd.ExplorerFleetSource`, `fleetCmd.ExplorerDemandProvider`, `fleetCmd.NewExplorerDemandProvider`, `grpc.autosizerExplorerFleetSource`

**Steps:**

- [ ] **Verify the bridge individually, by consumer of the object — both sides.** The bridge plays two ports at once (write via `EmitOffGateDemand`, read via `ExplorerDemand`), so both must come up empty-or-doomed.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  echo '--- the object ---'
  grep -rn 'ExplorerOffGateBridge' --include='*.go' . || true
  echo '--- write side ---'
  grep -rn 'EmitOffGateDemand' --include='*.go' . || true
  echo '--- read side ---'
  grep -rn 'ExplorerDemand\b\|OffGateDemandSource' --include='*.go' . || true
  ```
  **Expected — the object:** `internal/adapters/expansion/explorer_offgate_bridge.go` and two sites in `cmd/spacetraders-daemon/main.go` (the construction and the handler argument) only.
  **Expected — the write side: ZERO call sites.** Task 1 removed the only one; this is the retraction, proven structurally. The method declaration on the bridge itself is the sole hit.
  **Expected — the read side:** `fleet_autosizer_explorer.go`, `fleet_autosizer_ports.go` (the parameter, the comment and the registration), `explorer_offgate_bridge.go` (the method), and the autosizer explorer tests. That read-side set is exactly what this task removes.

- [ ] Delete the bridge and the demand provider.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/adapters/expansion/explorer_offgate_bridge.go \
     internal/application/fleet/commands/fleet_autosizer_explorer.go \
     internal/application/fleet/commands/fleet_autosizer_explorer_test.go \
     internal/application/fleet/commands/fleet_autosizer_explorer_wiring_test.go
  ```

- [ ] **Remove the explorer provider registration.** `gobot/internal/adapters/grpc/fleet_autosizer_ports.go` *(advisory ~`:62-69`)*. The blank separator is inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'NewExplorerDemandProvider' internal/adapters/grpc/fleet_autosizer_ports.go
  ```
  FIND (the leading `	}))` closes the heavy provider registration and is written back unchanged):
  ```go
  	}))

  	// Explorer class (slice C): reads slice-B off-gate demand through the cross-coordinator
  	// bridge (offGateDemand) and the live explorer-pool count (dedicate-at-purchase "explorer" fleet).
  	// DORMANT until BOTH armed (explorer_hulls_enabled, default off — classDisabled skips it otherwise)
  	// AND the frontier raises off-gate demand into the bridge, so registering it here changes no live
  	// behaviour and nothing auto-buys. The frontier warps the bought hull (SetExplorerDispatchPort).
  	h.AddDemandProvider(fleetCmd.NewExplorerDemandProvider(offGateDemand, &autosizerExplorerFleetSource{shipRepo: shipRepo}))
  ```
  REPLACE WITH:
  ```go
  	}))
  ```

- [ ] **Remove the constructor parameter.** Same file *(advisory ~`:44-46`)*. **The LIVE `scannedYards` parameter is quoted in the FIND and written back unchanged, so cutting the wrong line is structurally impossible.** (`scannedYards` is consumed further down the constructor; an earlier draft's line-addressed cut landed on it.)
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'offGateDemand fleetCmd.OffGateDemandSource,' internal/adapters/grpc/fleet_autosizer_ports.go
  ```
  FIND:
  ```go
  	scannedYards scannedYardRanker,
  	offGateDemand fleetCmd.OffGateDemandSource,
  	heavyYards heavyYardInventory,
  ```
  REPLACE WITH:
  ```go
  	scannedYards scannedYardRanker,
  	heavyYards heavyYardInventory,
  ```

- [ ] **Update the constructor doc comment.** Same file *(advisory ~`:32-34`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'and the opt-in explorer class' internal/adapters/grpc/fleet_autosizer_ports.go
  ```
  FIND:
  ```go
  // concrete port to the daemon's live collaborators and registering the light + heavy demand
  // providers (and the opt-in explorer class).
  ```
  REPLACE WITH:
  ```go
  // concrete port to the daemon's live collaborators and registering the light + heavy demand providers.
  ```

- [ ] **Fix the other real call site.** `gobot/internal/adapters/grpc/fleet_autosizer_heavy_wiring_test.go` *(advisory ~`:65-67`)*. `buildAutosizerHandler` passes **11 positional args**; the 10th is the `offGateDemand` slot.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'scannedYards, nil, heavyYards,' internal/adapters/grpc/fleet_autosizer_heavy_wiring_test.go
  ```
  FIND:
  ```go
  	return NewFleetAutosizerCoordinatorHandler(
  		&DaemonServer{}, nil, nil, shipRepo, nil, nil, nil, nil, scannedYards, nil, heavyYards,
  	)
  ```
  REPLACE WITH:
  ```go
  	return NewFleetAutosizerCoordinatorHandler(
  		&DaemonServer{}, nil, nil, shipRepo, nil, nil, nil, nil, scannedYards, heavyYards,
  	)
  ```
  **`cmd/spacetraders-daemon/heavy_target_single_instance_test.go:156` and `:179` also spell the constructor's name with an `offGateDemand`-shaped argument — leave them alone.** They live inside backtick raw-string Go fixtures fed to an AST parser; they never compile against the real signature, and editing them would break that test's expectations.

- [ ] **Remove the explorer fleet source.** `gobot/internal/adapters/grpc/fleet_autosizer_demand_ports.go` *(advisory ~`:122-136`)*. Both blank separators and both closing braces are inside the block, and **`countShips` — shared with the light and heavy fleet sources — is written back unchanged**.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'autosizerExplorerFleetSource struct' internal/adapters/grpc/fleet_autosizer_demand_ports.go
  ```
  FIND:
  ```go
  	return out
  }

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

  func countShips(ctx context.Context, shipRepo navigation.ShipRepository, playerID int, pred func(*navigation.Ship) bool) (int, error) {
  ```
  REPLACE WITH:
  ```go
  	return out
  }

  func countShips(ctx context.Context, shipRepo navigation.ShipRepository, playerID int, pred func(*navigation.Ship) bool) (int, error) {
  ```

- [ ] **Remove the daemon handler argument.** `gobot/cmd/spacetraders-daemon/main.go` *(advisory: post-Task-3 ~`:1172`)*. The surviving arguments either side are quoted and written back.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'explorerOffGateBridge, // Explorer demand provider reads off-gate demand through this bridge' cmd/spacetraders-daemon/main.go
  ```
  FIND:
  ```go
  		reachableYardFinder,
  		explorerOffGateBridge, // Explorer demand provider reads off-gate demand through this bridge
  		heavyTargetFinder,     // sp-fwk8z: the SHARED heavy target — the reservation price term, one definition
  ```
  REPLACE WITH:
  ```go
  		reachableYardFinder,
  		heavyTargetFinder, // sp-fwk8z: the SHARED heavy target — the reservation price term, one definition
  ```

- [ ] **Remove the bridge construction and the comment Task 3 rewrote.** Same file *(advisory: post-Task-3 ~`:1149-1153`)*. The blank separator is inside the block and `reachableYardFinder := …` is quoted and written back.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()' cmd/spacetraders-daemon/main.go
  ```
  FIND (this is the Task-3-rewritten comment; if it does not match verbatim, Task 3's rewrite step was skipped — go back and do it rather than improvising a range):
  ```go
  	// The cross-coordinator off-gate demand bridge the FLEET autosizer's explorer BUY path
  	// reads. It has NO writer: off-gate warp expansion is retired (sp-mn3it), so the bridge
  	// never sees a first emit, reads UNREADABLE (ok=false), and the explorer buy fails closed.
  	// Deleted with the whole read chain in the next commit.
  	explorerOffGateBridge := expansionAdapters.NewExplorerOffGateBridge()

  	reachableYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)
  ```
  REPLACE WITH:
  ```go
  	reachableYardFinder := shipyardQuery.NewReachableYardFinder(shipyardInventoryRepo, gateGraphService)
  ```
  Then confirm the identifier is gone from the file:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -n 'explorerOffGateBridge' cmd/spacetraders-daemon/main.go || echo "NO explorerOffGateBridge REFERENCES — correct"
  ```
  Expect `NO explorerOffGateBridge REFERENCES — correct`.

- [ ] The tree should now build for the first time since Task 4. Build and vet the whole module — vet is what catches the call-site arity break `go build` would miss in `_test.go`.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./...
  ```
  Expect no output from either. If vet reports a `NewFleetAutosizerCoordinatorHandler` arity error in a test, fix that call site — the signature lost one parameter.

- [ ] Run the affected packages.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/adapters/expansion/ ./internal/adapters/grpc/ ./internal/application/fleet/... ./cmd/spacetraders-daemon/ 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  Expect `ok` for each and no `FAIL`.

- [ ] Format and commit Tasks 4–6 together (they were one compile unit).
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  gofmt -w .
  test -z "$(gofmt -l .)"
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "refactor(expansion,fleet,grpc): delete offgate_types, the explorer-only adapters and the latching bridge (sp-mn3it)

  offgate_types.go incl. the two ORPHANED declarations — OffGateTargetSelector and
  ShipyardCoverageReader. Their deadness is established soundly: there is no field,
  parameter or variable ANYWHERE of either interface type, so no value can ever be
  assigned to one and no implementation is reachable, whatever shapes exist. 'No name
  references, therefore dead' would not have been a valid argument.

  Each adapter verified on its own before deletion: idle_explorer_port, then
  explorer_dispatch_adapter (with an immediate check that the manual 'ship warp' verb's
  own entrypoint, ExecuteWarpLeg, still has its live caller at warp_ship.go:98), then
  off_gate_target. ExecuteWarpRoute is a DIFFERENT method and is now caller-less by
  design — it stays, and its fate is filed separately as sp-bwzy3.

  Deleting ExplorerOffGateBridge FORCES the read chain: the write side had zero call
  sites (that is the retraction, proven structurally), and the compiler takes the
  offGateDemand parameter, ExplorerDemandProvider and autosizerExplorerFleetSource with
  it. NewFleetAutosizerCoordinatorHandler is one parameter shorter, which also drops the
  10th positional arg in fleet_autosizer_heavy_wiring_test.go."
  ```

---

## Task 7 — Delete the explorer class config, tune keys and autosizer class arms

Leaving `autosizer_explorer_hulls_enabled` as an accepted tune key would advertise an arming seam for a class that no longer exists: an operator could set it and get silence. It goes with the class.

**All edits in this task are content-addressed and mutually independent. Apply them in any order.**

**Files:**
- Modify: `gobot/internal/adapters/grpc/container_ops_fleet_autosizer.go` — three edits *(advisory ~`:82-88`, ~`:160-177`, ~`:212-220`)*
- Modify: `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator.go` — four edits *(advisory ~`:43-54`, ~`:64-72`, ~`:122-134`, ~`:313-328`)*
- Modify: `gobot/internal/application/fleet/commands/run_fleet_autosizer_reconcile.go` — four edits *(advisory ~`:42-52`, ~`:69-76`, ~`:109-126`, ~`:206-210`)*
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_act.go` — two edits *(advisory ~`:188-193`, ~`:199-207`)*
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_types.go` — two edits *(advisory ~`:10`, ~`:26-32`)*
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_guards.go` — three PROSE edits *(advisory ~`:34-35`, ~`:52-55`, ~`:246-247`)*
- Modify: `gobot/internal/infrastructure/config/fleet_autosizer.go` — two edits *(advisory ~`:27-28`, ~`:97-123`)*
- Modify: `gobot/internal/adapters/grpc/container_ops_tune.go` — one in-line fragment *(advisory ~`:141`)*; **keep the `expansion_enabled` key**
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_heartbeat.go` — one line *(advisory ~`:421`)*
- Modify: `gobot/internal/application/scouting/commands/probe_sensing_config.go` — one comment *(advisory ~`:99-101`)*

**Four TEST files also break here, and each needs an explicit disposition.** Deleting the `HullClassExplorer` alias and the explorer config fields breaks all four. **`go build` will pass and say nothing** — it skips `_test.go`. Only `go vet ./...` and `go test` see them.
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_guards_test.go` — **delete the whole explorer block**: the section banner, the `explorerPassingRequest()` fixture, the sp-r7eiu note and the three tests it feeds. *Nothing here survives:* the fixture's whole subject is the explorer class. **The note also contains the string `ExplorerDemandProvider`**, which is on Task 10's retired-vocabulary list — leave it and Task 10's new invariant test fails on its first run.
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_heavy_cap_test.go` — **keep the test, drop the explorer arm.** The test's real subject is that the heavy cap is heavy-scoped, which is still true and still worth pinning for lights.
- Modify: `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator_test.go` — **delete `TestReconcile_ExplorerOptIn` entirely.** Its whole subject is the opt-in arming gate this task removes. `fakeDemandProvider` and `newHandlerWith` are shared and stay.
- Modify: `gobot/internal/adapters/grpc/command_factory_fleet_autosizer_live_test.go` — **delete the three explorer round-trip tests.** All three pin the arming seam being retired.

**Interfaces:**
- Consumes: `configReader.OptionalBool/OptionalInt/OptionalString`
- Produces: `DemandParams{LightRotationSlots float64}` — the other two fields removed. `RunFleetAutosizerCoordinatorCommand` and `autosizerRunConfig` each lose 5 explorer fields. `config.FleetAutosizerConfig` loses 5 mapstructure fields.

**Steps:**

- [ ] Confirm no checked-in YAML sets any explorer knob — if one did, deleting the field would silently change a live fleet's config rather than removing a dead one.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  grep -rn 'explorer_hulls_enabled\|fleet_ceiling_explorer\|explorer_treasury_pct_per_purchase\|max_price_explorer\|ship_type_explorer' --include='*.yaml' --include='*.yml' . \
    || echo "NO YAML SETS ANY EXPLORER KNOB — correct"
  ```
  Expect `NO YAML SETS ANY EXPLORER KNOB — correct`. If any file matches, stop and report — that is a live arming the bead's evidence did not anticipate.

- [ ] **Remove the config read block.** `gobot/internal/adapters/grpc/container_ops_fleet_autosizer.go` *(advisory ~`:212-220`)*. The literal's closing `}` and the surviving `// sp-y2ptq:` comment are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'ExplorerHullsEnabled:           cfg.OptionalBool' internal/adapters/grpc/container_ops_fleet_autosizer.go
  ```
  FIND:
  ```go
  		ZeroEffectAlarmTicks: cfg.OptionalInt("autosizer_zero_effect_alarm_ticks", 0),

  		ExplorerHullsEnabled:           cfg.OptionalBool("autosizer_explorer_hulls_enabled"),
  		FleetCeilingExplorer:           cfg.OptionalInt("autosizer_fleet_ceiling_explorer", 0),
  		ExplorerTreasuryPctPerPurchase: cfg.OptionalInt("autosizer_explorer_treasury_pct_per_purchase", 0),
  		MaxPriceExplorer:               int64(cfg.OptionalInt("autosizer_max_price_explorer", 0)),
  		ShipTypeExplorer:               cfg.OptionalString("autosizer_ship_type_explorer"),
  		// sp-y2ptq: autosizer contract-delivery class removed — no autosizer_contract_delivery_* reads.
  	}
  ```
  REPLACE WITH:
  ```go
  		ZeroEffectAlarmTicks: cfg.OptionalInt("autosizer_zero_effect_alarm_ticks", 0),
  		// sp-y2ptq: autosizer contract-delivery class removed — no autosizer_contract_delivery_* reads.
  	}
  ```

- [ ] **Remove the config write block.** Same file *(advisory ~`:160-177`)*. The surviving `// sp-y2ptq:` comment is inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'The opt-in arming bool is written ONLY when true' internal/adapters/grpc/container_ops_fleet_autosizer.go
  ```
  FIND:
  ```go
  	// Explorer class. The opt-in arming bool is written ONLY when true (an absent key reads
  	// as DISARMED, so nothing boot-arms it — mirrors warehouse_hulls_enabled).
  	if fa.ExplorerHullsEnabled {
  		config["autosizer_explorer_hulls_enabled"] = true
  	}
  	if fa.FleetCeilingExplorer != 0 {
  		config["autosizer_fleet_ceiling_explorer"] = fa.FleetCeilingExplorer
  	}
  	if fa.ExplorerTreasuryPctPerPurchase != 0 {
  		config["autosizer_explorer_treasury_pct_per_purchase"] = fa.ExplorerTreasuryPctPerPurchase
  	}
  	if fa.MaxPriceExplorer != 0 {
  		config["autosizer_max_price_explorer"] = int(fa.MaxPriceExplorer)
  	}
  	if fa.ShipTypeExplorer != "" {
  		config["autosizer_ship_type_explorer"] = fa.ShipTypeExplorer
  	}
  	// sp-y2ptq: the autosizer's contract-delivery class was removed (dedicated scaler owns it) — no
  ```
  REPLACE WITH:
  ```go
  	// sp-y2ptq: the autosizer's contract-delivery class was removed (dedicated scaler owns it) — no
  ```

- [ ] **Remove the 5 tune keys.** Same file *(advisory ~`:82-88`)*. The slice's closing `}` is inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^	"autosizer_explorer_hulls_enabled",$' internal/adapters/grpc/container_ops_fleet_autosizer.go
  ```
  FIND:
  ```go
  	"autosizer_zero_effect_alarm_ticks",
  	"autosizer_explorer_hulls_enabled",
  	"autosizer_fleet_ceiling_explorer",
  	"autosizer_explorer_treasury_pct_per_purchase",
  	"autosizer_max_price_explorer",
  	"autosizer_ship_type_explorer",
  }
  ```
  REPLACE WITH:
  ```go
  	"autosizer_zero_effect_alarm_ticks",
  }
  ```

- [ ] **Remove the `classDisabled` arm.** `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator.go` *(advisory ~`:313-328`; this block ENDS the file — its final `}` is the last line)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'func (c autosizerRunConfig) classDisabled' internal/application/fleet/commands/run_fleet_autosizer_coordinator.go
  ```
  FIND:
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
  REPLACE WITH:
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
  Note the receiver `c` is now unused by the body; leave it — the method is on `autosizerRunConfig` by contract and `go vet` does not flag an unused receiver. The file must still end with `}` and no trailing blank.

- [ ] **Remove the command fields.** Same file *(advisory ~`:122-134`)*. Both blank separators and the struct's closing `}` are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'ExplorerHullsEnabled is the opt-IN arming knob' internal/application/fleet/commands/run_fleet_autosizer_coordinator.go
  ```
  FIND:
  ```go
  	ZeroEffectAlarmTicks int

  	// Explorer class. ExplorerHullsEnabled is the opt-IN arming knob (default OFF
  	// — nothing boot-arms it). FleetCeilingExplorer is the HARD CAP (default 1); MaxPriceExplorer is
  	// the price ceiling (default ~819k+premium); ExplorerTreasuryPctPerPurchase is the 25% rule.
  	ExplorerHullsEnabled           bool
  	FleetCeilingExplorer           int
  	ExplorerTreasuryPctPerPurchase int
  	MaxPriceExplorer               int64
  	ShipTypeExplorer               string

  	// No contract-delivery class knobs here: the dedicated scaler owns contract capacity.
  }
  ```
  REPLACE WITH:
  ```go
  	ZeroEffectAlarmTicks int

  	// No contract-delivery class knobs here: the dedicated scaler owns contract capacity.
  }
  ```

- [ ] **Remove the `DemandParams` fields.** Same file *(advisory ~`:64-72`)*. The struct's closing `}` is inside the block, and the surviving `LightRotationSlots` field is written back.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'MaxExplorerHulls is the explorer HARD CAP' internal/application/fleet/commands/run_fleet_autosizer_coordinator.go
  ```
  FIND:
  ```go
  	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
  	LightRotationSlots float64
  	// ExplorerHullsEnabled ARMS the explorer class. Default false (opt-in): when false the
  	// explorer provider emits ZERO demand unconditionally, so a bare deploy buys no explorer.
  	ExplorerHullsEnabled bool
  	// MaxExplorerHulls is the explorer HARD CAP (the class fleet ceiling, default 1): the provider
  	// never wants more than this regardless of the off-gate signal's count.
  	MaxExplorerHulls int
  }
  ```
  REPLACE WITH:
  ```go
  	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
  	LightRotationSlots float64
  }
  ```

- [ ] **Remove the coordinator defaults.** Same file *(advisory ~`:43-54`)*. Both blank separators are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'defaultShipTypeExplorer               = "SHIP_EXPLORER"' internal/application/fleet/commands/run_fleet_autosizer_coordinator.go
  ```
  FIND:
  ```go
  	defaultShipTypeHeavies = "SHIP_HEAVY_FREIGHTER"

  	// Explorer class. Opt-IN (default OFF, arming knob). The HARD CAP is 1; the
  	// PRICE CEILING defaults to ~819k SHIP_EXPLORER + a premium (a REAL default, never 0=off — the
  	// explorer's price ceiling is a required guard); the 25%-treasury big-ticket affordability rule
  	// applies (the explorer buys an ~819k hull).
  	defaultFleetCeilingExplorer           = 1
  	defaultExplorerTreasuryPctPerPurchase = 25
  	defaultMaxPriceExplorer               = 900000
  	defaultShipTypeExplorer               = "SHIP_EXPLORER"

  	// The autosizer carries no contract-delivery class defaults — the dedicated scaler owns
  ```
  REPLACE WITH:
  ```go
  	defaultShipTypeHeavies = "SHIP_HEAVY_FREIGHTER"

  	// The autosizer carries no contract-delivery class defaults — the dedicated scaler owns
  ```

- [ ] **Remove the `DemandParams` fills.** `gobot/internal/application/fleet/commands/run_fleet_autosizer_reconcile.go` *(advisory ~`:206-210`)*. The literal's closing `}` is inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'MaxExplorerHulls:     cfg.FleetCeilingExplorer,' internal/application/fleet/commands/run_fleet_autosizer_reconcile.go
  ```
  FIND:
  ```go
  	params := DemandParams{
  		LightRotationSlots:   cfg.LightRotationSlots,
  		ExplorerHullsEnabled: cfg.ExplorerHullsEnabled,
  		MaxExplorerHulls:     cfg.FleetCeilingExplorer,
  	}
  ```
  REPLACE WITH:
  ```go
  	params := DemandParams{
  		LightRotationSlots: cfg.LightRotationSlots,
  	}
  ```

- [ ] **Remove the reconcile defaults.** Same file *(advisory ~`:109-126`)*. The `}` closing the preceding `if` and the surviving `// PreferDemandProximalYard` comment are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'Explorer defaults. ExplorerHullsEnabled has NO fallback' internal/application/fleet/commands/run_fleet_autosizer_reconcile.go
  ```
  FIND:
  ```go
  		c.ZeroEffectAlarmTicks = defaultZeroEffectAlarmTicks
  	}
  	// Explorer defaults. ExplorerHullsEnabled has NO fallback — its false zero value IS the
  	// default (disarmed), so nothing boot-arms it. MaxPriceExplorer resolves to a REAL default (never
  	// 0=off, unlike MaxPrice{Lights,Heavies}) because the explorer's price ceiling is a required guard.
  	if c.FleetCeilingExplorer <= 0 {
  		c.FleetCeilingExplorer = defaultFleetCeilingExplorer
  	}
  	if c.ExplorerTreasuryPctPerPurchase <= 0 {
  		c.ExplorerTreasuryPctPerPurchase = defaultExplorerTreasuryPctPerPurchase
  	}
  	if c.MaxPriceExplorer <= 0 {
  		c.MaxPriceExplorer = defaultMaxPriceExplorer
  	}
  	if c.ShipTypeExplorer == "" {
  		c.ShipTypeExplorer = defaultShipTypeExplorer
  	}
  	// PreferDemandProximalYard defaults TRUE: nil (unset) → true; the *bool distinguishes an
  ```
  REPLACE WITH:
  ```go
  		c.ZeroEffectAlarmTicks = defaultZeroEffectAlarmTicks
  	}
  	// PreferDemandProximalYard defaults TRUE: nil (unset) → true; the *bool distinguishes an
  ```

- [ ] **Remove the copy block.** Same file *(advisory ~`:69-76`)*. The blank separator and the literal's closing `}` are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'ExplorerHullsEnabled:           cmd.ExplorerHullsEnabled,' internal/application/fleet/commands/run_fleet_autosizer_reconcile.go
  ```
  FIND:
  ```go
  		ZeroEffectAlarmTicks:        cmd.ZeroEffectAlarmTicks,

  		ExplorerHullsEnabled:           cmd.ExplorerHullsEnabled,
  		FleetCeilingExplorer:           cmd.FleetCeilingExplorer,
  		ExplorerTreasuryPctPerPurchase: cmd.ExplorerTreasuryPctPerPurchase,
  		MaxPriceExplorer:               cmd.MaxPriceExplorer,
  		ShipTypeExplorer:               cmd.ShipTypeExplorer,
  	}
  ```
  REPLACE WITH:
  ```go
  		ZeroEffectAlarmTicks:        cmd.ZeroEffectAlarmTicks,
  	}
  ```

- [ ] **Remove the run-config fields.** Same file *(advisory ~`:42-52`)*. Both blank separators and the struct's closing `}` are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c '^	// Explorer class.$' internal/application/fleet/commands/run_fleet_autosizer_reconcile.go
  ```
  FIND:
  ```go
  	ZeroEffectAlarmTicks int

  	// Explorer class.
  	ExplorerHullsEnabled           bool
  	FleetCeilingExplorer           int
  	ExplorerTreasuryPctPerPurchase int
  	MaxPriceExplorer               int64
  	ShipTypeExplorer               string

  	// No contract-delivery class fields: the dedicated scaler owns contract capacity.
  }
  ```
  REPLACE WITH:
  ```go
  	ZeroEffectAlarmTicks int

  	// No contract-delivery class fields: the dedicated scaler owns contract capacity.
  }
  ```

- [ ] **Remove the `classGuardConfig` arm.** `gobot/internal/application/fleet/commands/fleet_autosizer_act.go` *(advisory ~`:199-207`)*. The surviving `case HullClassHeavy` and `default:` lines are inside the block and written back.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'return cfg.ShipTypeExplorer, cfg.MaxPriceExplorer, cfg.ExplorerTreasuryPctPerPurchase' internal/application/fleet/commands/fleet_autosizer_act.go
  ```
  FIND:
  ```go
  	case HullClassHeavy:
  		return cfg.ShipTypeHeavies, cfg.MaxPriceHeavies, cfg.HeavyTreasuryPctPerPurchase
  	case HullClassExplorer:
  		// The explorer's ship type (SHIP_EXPLORER), its price ceiling (~819k+premium — a REAL cap,
  		// not 0=off), and the 25% big-ticket affordability rule. The realized-$/hr payback exemption
  		// is applied class-gated INSIDE EvaluateGuards, not here — every knob returned here is a REAL
  		// guard bound the explorer must still clear.
  		return cfg.ShipTypeExplorer, cfg.MaxPriceExplorer, cfg.ExplorerTreasuryPctPerPurchase
  	default:
  ```
  REPLACE WITH:
  ```go
  	case HullClassHeavy:
  		return cfg.ShipTypeHeavies, cfg.MaxPriceHeavies, cfg.HeavyTreasuryPctPerPurchase
  	default:
  ```

- [ ] **Trim the `classGuardConfig` doc comment.** Same file *(advisory ~`:188-193`)*. The `func` line is inside the block and written back.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c "The explorer's HARD CAP of 1 is not missing" internal/application/fleet/commands/fleet_autosizer_act.go
  ```
  FIND:
  ```go
  // classGuardConfig resolves the per-class guard knobs from the run config.
  //
  // There is no class ceiling here. The explorer's HARD CAP of 1 is not missing: that cap lives
  // in ExplorerDemandProvider, which clamps its want to MaxExplorerHulls, so the class is capped
  // by its demand.
  func classGuardConfig(class HullClass, cfg autosizerRunConfig) (shipType string, maxPrice int64, treasuryPct int) {
  ```
  REPLACE WITH:
  ```go
  // classGuardConfig resolves the per-class guard knobs from the run config. There is no class
  // ceiling here: a class is bounded by its demand, its affordability and its price cap.
  func classGuardConfig(class HullClass, cfg autosizerRunConfig) (shipType string, maxPrice int64, treasuryPct int) {
  ```

- [ ] **Remove the class alias.** `gobot/internal/application/fleet/commands/fleet_autosizer_types.go` *(advisory ~`:26-32`)*. The surviving neighbours are inside the block and written back.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'HullClassExplorer = hullbuy.HullClassExplorer' internal/application/fleet/commands/fleet_autosizer_types.go
  ```
  FIND:
  ```go
  	// HullClassHeavy is the trade-tour pool (DedicatedFleet "trade"), sized to trade demand.
  	HullClassHeavy = hullbuy.HullClassHeavy
  	// HullClassExplorer is sized to slice-B off-gate demand and runs the SAME guard stack as every
  	// other class — there is no class-gated carve-out. Its ~819k spend is bounded by the demand
  	// gate, a HARD CAP of 1 (the class fleet ceiling) and a price ceiling. Opt-IN
  	// (explorer_hulls_enabled, default OFF) and double-gated, so a bare deploy buys nothing.
  	HullClassExplorer = hullbuy.HullClassExplorer
  	// HullClassContractDelivery is the capacity reconciler's contract-delivery capital pool. The
  ```
  REPLACE WITH:
  ```go
  	// HullClassHeavy is the trade-tour pool (DedicatedFleet "trade"), sized to trade demand.
  	HullClassHeavy = hullbuy.HullClassHeavy
  	// HullClassContractDelivery is the capacity reconciler's contract-delivery capital pool. The
  ```

- [ ] **Trim the package doc line.** Same file *(advisory ~`:10`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'N pluggable demand providers (lights, heavies, explorer)' internal/application/fleet/commands/fleet_autosizer_types.go
  ```
  FIND:
  ```go
  // N pluggable demand providers (lights, heavies, explorer) — the vdld pluggable-provider idiom.
  ```
  REPLACE WITH:
  ```go
  // N pluggable demand providers (lights, heavies) — the vdld pluggable-provider idiom.
  ```

- [ ] **Trim the guard-stack prose — THREE independent edits.** `gobot/internal/application/fleet/commands/fleet_autosizer_guards.go`. These are comment bullets in three separate blocks; an earlier draft applied them top-down by line number and corrupted the live bullet list. Content-addressed, order does not matter.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/application/fleet/commands/fleet_autosizer_guards.go
  grep -c 'explorer_exempt existed solely to cancel those two' "$F"
  grep -c 'its HARD CAP of 1, enforced in the demand provider itself' "$F"
  grep -c 'worker pool and the explorer for reasons that have nothing to do with them' "$F"
  ```
  **(1)** *(advisory ~`:34-35`)* FIND:
  ```go
  // saturation — the next hull flies a fresh lane). explorer_exempt existed solely to cancel those two
  // for one class and could never itself block. The autosizer therefore no longer forms an opinion on
  ```
  REPLACE WITH:
  ```go
  // saturation — the next hull flies a fresh lane). The autosizer therefore no longer forms an
  // opinion on
  ```
  **(2)** *(advisory ~`:52-55`)* FIND (the surviving heavy and light bullets are inside the block and written back):
  ```go
  //	              3-tick anti-thrash streak on its unserved-lane shortfall
  //	explorer    — its HARD CAP of 1, enforced in the demand provider itself (ExplorerDemandProvider
  //	              clamps want to MaxExplorerHulls), so the class stays capped without this guard
  //	light       — the factory-chain rotation math ALONE (ceil(chains × rotation_slots) + vacancies).
  ```
  REPLACE WITH:
  ```go
  //	              3-tick anti-thrash streak on its unserved-lane shortfall
  //	light       — the factory-chain rotation math ALONE (ceil(chains × rotation_slots) + vacancies).
  ```
  **(3)** *(advisory ~`:246-247`)* FIND:
  ```go
  // (HeaviesOwned) counts heavy hulls fleet-wide and would otherwise starve the light
  // worker pool and the explorer for reasons that have nothing to do with them.
  ```
  REPLACE WITH:
  ```go
  // (HeaviesOwned) counts heavy hulls fleet-wide and would otherwise starve the light
  // worker pool for reasons that have nothing to do with it.
  ```

- [ ] **Remove the infrastructure config block.** `gobot/internal/infrastructure/config/fleet_autosizer.go` *(advisory ~`:97-123`)*. Both blank separators are inside the block.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'explorer hull class (sp-a3yn slice C of sp-4imi)' internal/infrastructure/config/fleet_autosizer.go
  ```
  FIND:
  ```go
  	ZeroEffectAlarmTicks int `mapstructure:"zero_effect_alarm_ticks"`

  	// --- explorer hull class (sp-a3yn slice C of sp-4imi) ---
  	//
  	// The explorer auto-buys an ~819k SHIP_EXPLORER for REACH, not income (it charts new systems so
  	// the cheap probe frontier resumes). Because that spend is large and captain-reviewed, it is
  	// DEPLOY-INERT: ExplorerHullsEnabled defaults OFF and NOTHING boot-arms it — the buy requires BOTH
  	// (a) this flag armed AND (b) slice-B off-gate demand firing. Config+restart arming (not a live
  	// `tune`) is deliberate: a runtime tune cannot flip it. It is the sole opt-IN autosizer class.

  	// ExplorerHullsEnabled ARMS the explorer class. Absent/false = DISARMED (the class emits zero
  	// demand and buys nothing). Set true ONLY after the captain/human review signs off.
  	ExplorerHullsEnabled bool `mapstructure:"explorer_hulls_enabled"`
  	// FleetCeilingExplorer is the explorer HARD CAP (never own more than this). 0/absent → 1.
  	FleetCeilingExplorer int `mapstructure:"fleet_ceiling_explorer"`
  	// ExplorerTreasuryPctPerPurchase is the 25% big-ticket affordability rule for the ~819k buy.
  	// 0/absent → 25.
  	ExplorerTreasuryPctPerPurchase int `mapstructure:"explorer_treasury_pct_per_purchase"`
  	// MaxPriceExplorer is the explorer PRICE CEILING (~819k SHIP_EXPLORER + premium). Unlike
  	// MaxPrice{Lights,Heavies} it resolves to a REAL default (never 0=no-cap): the ceiling is a
  	// required guard on this large buy. 0/absent → 900000.
  	MaxPriceExplorer int64 `mapstructure:"max_price_explorer"`
  	// ShipTypeExplorer is the shipyard ship-type bought for the explorer class. 0/absent →
  	// "SHIP_EXPLORER" (the only warp-drive-carrying hull).
  	ShipTypeExplorer string `mapstructure:"ship_type_explorer"`

  	// sp-y2ptq: the autosizer's demand-driven contract-delivery hull class
  ```
  REPLACE WITH:
  ```go
  	ZeroEffectAlarmTicks int `mapstructure:"zero_effect_alarm_ticks"`

  	// sp-y2ptq: the autosizer's demand-driven contract-delivery hull class
  ```

- [ ] **Delete the stale `FleetCeilingExplorer SURVIVES` note.** Same file *(advisory ~`:27-28`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'FleetCeilingExplorer SURVIVES below' internal/infrastructure/config/fleet_autosizer.go
  ```
  FIND:
  ```go
  	// ErrorUnused) drops keys with no matching field, so a stale config.yaml still boots.
  	// FleetCeilingExplorer SURVIVES below: it is the explorer's demand-side hard cap, not a guard input.
  ```
  REPLACE WITH:
  ```go
  	// ErrorUnused) drops keys with no matching field, so a stale config.yaml still boots.
  ```

- [ ] **Edit the tune-key prose and KEEP the key.** `gobot/internal/adapters/grpc/container_ops_tune.go` *(advisory ~`:141`)*. This is a fragment INSIDE a long single-line description string — replace only the fragment.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c "no charting seed, no off-gate explorer (the autosizer's ~769k hull)." internal/adapters/grpc/container_ops_tune.go
  ```
  FIND (fragment):
  ```
  no probe for a coverage placement (operation_type='sensing coverage'), no charting seed, no off-gate explorer (the autosizer's ~769k hull).
  ```
  REPLACE WITH (fragment):
  ```
  no probe for a coverage placement (operation_type='sensing coverage'), no charting seed.
  ```
  Leave the rest of the description and the key itself untouched — `expansion_enabled` is the live probe-spending switch and is not part of this retirement.

- [ ] **Edit the heartbeat prose.** `gobot/internal/application/scouting/commands/probe_sensing_heartbeat.go` *(advisory ~`:421`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'spending paused: no seed purchase, no explorer demand' internal/application/scouting/commands/probe_sensing_heartbeat.go
  ```
  FIND:
  ```go
  		return summary + " (spending paused: no seed purchase, no explorer demand)"
  ```
  REPLACE WITH:
  ```go
  		return summary + " (spending paused: no seed purchase)"
  ```

- [ ] **Edit the sensing-config prose.** `gobot/internal/application/scouting/commands/probe_sensing_config.go` *(advisory ~`:99-101`)*.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'an explorer from the autosizer' internal/application/scouting/commands/probe_sensing_config.go
  ```
  FIND:
  ```go
  	// feeds BOTH engines that can: the expansion pass, which asks other engines to
  	// buy (a charting seed from the buy queue, an explorer from the autosizer), and
  	// the buy queue itself, which is what actually pays for a coverage probe.
  ```
  REPLACE WITH:
  ```go
  	// feeds BOTH engines that can: the expansion pass, which asks other engines to
  	// buy (a charting seed from the buy queue), and the buy queue itself, which is
  	// what actually pays for a coverage probe.
  ```

**THE FOUR TEST FILES. `go build ./...` passes right now and says nothing — it skips `_test.go`. `go vet ./...` and `go test` are what see these. Do all four before the gate below.**

- [ ] **Delete the explorer guard block.** `gobot/internal/application/fleet/commands/fleet_autosizer_guards_test.go` *(advisory ~`:256-325`)*. Large deletion — use the three anchors.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/application/fleet/commands/fleet_autosizer_guards_test.go
  grep -c '^// --- The EXPLORER class runs the SAME guard stack as everything else' "$F"
  grep -c 'apiSaturated.APIUtilReadable = false' "$F"
  grep -c '^func assertBlockedBy(t \*testing.T, r PurchaseRequest, want GuardName) {$' "$F"
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // --- The EXPLORER class runs the SAME guard stack as everything else ----------------------
  ```
  **TAIL ANCHOR** — all four lines are deleted; the last is the bare `}` closing `TestGuard_Explorer_ReusedTreasuryGuardsStillBite`:
  ```go
  	apiSaturated := explorerPassingRequest()
  	apiSaturated.APIUtilReadable = false // fail-closed still holds for the explorer
  	assertBlockedBy(t, apiSaturated, GuardAPIUtil)
  }
  ```
  **FIRST SURVIVING LINE** — `assertBlockedBy` is shared by every guard test in the file and stays; the blank line above it survives too:
  ```go
  func assertBlockedBy(t *testing.T, r PurchaseRequest, want GuardName) {
  ```
  **Post-condition:** exactly ONE blank line between the `}` closing the last surviving test above the head anchor and `func assertBlockedBy`.
  **Two things go with this block that would otherwise bite later:** the `HullClassExplorer` reference in `explorerPassingRequest()` (a compile break once the alias goes) and the string `ExplorerDemandProvider` in the sp-r7eiu note, which is on Task 10's retired-vocabulary list — leave that comment and Task 10's structural guard fails on its very first run.

- [ ] **Narrow the heavy-cap scope test to lights.** `gobot/internal/application/fleet/commands/fleet_autosizer_heavy_cap_test.go` *(advisory ~`:55-66`)*. **Keep the test** — the heavy cap really is heavy-scoped and that is still worth pinning — and drop only the explorer arm. **The `}` that closes `if class == HullClassExplorer {` is INSIDE the FIND block**; an earlier draft's "replace 55-64" stopped one line short of it and left a stray brace.
  Uniqueness check — must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c 'for _, class := range \[\]HullClass{HullClassLight, HullClassExplorer} {' internal/application/fleet/commands/fleet_autosizer_heavy_cap_test.go
  ```
  FIND:
  ```go
  // The heavy cap is HEAVY-SCOPED: it must never block a light or explorer buy, whatever the heavy
  // census says. Folding it into the shared ceiling guard would starve the worker pool.
  func TestGuard_HeavyCap_DoesNotApplyToOtherClasses(t *testing.T) {
  	for _, class := range []HullClass{HullClassLight, HullClassExplorer} {
  		r := passingRequest()
  		r.Class = class
  		r.HeaviesOwned = 99 // far over any cap
  		r.HeavyCap = 5
  		if class == HullClassExplorer {
  			r.MaxPriceClass = 900000
  		}
  		d := EvaluateGuards(r)
  ```
  REPLACE WITH:
  ```go
  // The heavy cap is HEAVY-SCOPED: it must never block a light buy, whatever the heavy census says.
  // Folding it into the shared ceiling guard would starve the worker pool.
  func TestGuard_HeavyCap_DoesNotApplyToOtherClasses(t *testing.T) {
  	for _, class := range []HullClass{HullClassLight} {
  		r := passingRequest()
  		r.Class = class
  		r.HeaviesOwned = 99 // far over any cap
  		r.HeavyCap = 5
  		d := EvaluateGuards(r)
  ```
  (The loop over a one-element slice is kept deliberately: it is the shape the next non-heavy class slots into, and collapsing it would lose the `class %s` message the assertion below it prints.)

- [ ] **Delete the opt-in reconcile test.** `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator_test.go` *(advisory ~`:68-92`)*.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/application/fleet/commands/run_fleet_autosizer_coordinator_test.go
  grep -c '^// Explorer is OPT-IN (not live-by-default)' "$F"
  grep -c 'explorer provider must run when explorer_hulls_enabled=true' "$F"
  grep -c '^// A provider infra error must not abort the whole tick' "$F"
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // Explorer is OPT-IN (not live-by-default): the explorer provider is skipped unless
  ```
  **TAIL ANCHOR** — all four lines are deleted; the last is the bare `}` closing `TestReconcile_ExplorerOptIn`:
  ```go
  	if ex2.calls != 1 {
  		t.Fatalf("explorer provider must run when explorer_hulls_enabled=true, got %d", ex2.calls)
  	}
  }
  ```
  **FIRST SURVIVING LINE** (the blank above it survives):
  ```go
  // A provider infra error must not abort the whole tick — the other classes still size.
  ```
  **Post-condition:** exactly ONE blank line between the `}` closing the test above and that comment. **`fakeDemandProvider` and `newHandlerWith` stay** — the tests either side use both.

- [ ] **Delete the three explorer round-trip tests.** `gobot/internal/adapters/grpc/command_factory_fleet_autosizer_live_test.go` *(advisory ~`:96-135`)*.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/adapters/grpc/command_factory_fleet_autosizer_live_test.go
  grep -c '^// DEPLOY-SAFETY PIN: with no explorer_hulls_enabled anywhere' "$F"
  grep -c 'unset live must clear the stale persisted arm' "$F"
  grep -c '^// The default-TRUE prefer_demand_proximal_yard round-trips' "$F"
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // DEPLOY-SAFETY PIN: with no explorer_hulls_enabled anywhere, the built command carries
  ```
  **TAIL ANCHOR** — all three lines are deleted; the last is the bare `}` closing `TestAutosizerExplorerArmingClearsStalePersisted`:
  ```go
  	require.False(t, cmd.ExplorerHullsEnabled,
  		"unset live must clear the stale persisted arm → explorer DISARMED")
  }
  ```
  **FIRST SURVIVING LINE** (the blank above it survives):
  ```go
  // The default-TRUE prefer_demand_proximal_yard round-trips: unset → nil (default true downstream);
  ```
  All three deleted tests pin the arming seam being retired: `TestAutosizerExplorerDisarmedByDefault`, `TestAutosizerResolvesExplorerKnobsFromLiveConfig` (whose `config.FleetAutosizerConfig` literal sets the five fields this task deletes) and `TestAutosizerExplorerArmingClearsStalePersisted` (whose launch-config map carries the `"autosizer_explorer_hulls_enabled"` key). Everything from the first surviving line on stays.

- [ ] Build, vet, format, test the fleet + grpc + config + scouting packages. **`go vet ./...` is the load-bearing step here** — it is what sees the four test files above.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./...
  gofmt -w .
  test -z "$(gofmt -l .)"
  go test ./internal/application/fleet/... ./internal/adapters/grpc/ ./internal/infrastructure/config/ ./internal/application/scouting/... 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  Expect no output from build/vet/gofmt and `ok` for every package.

- [ ] Confirm the deferral held: `hullbuy.HullClassExplorer` is now unreachable but still declared, and its own package test still passes.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'HullClassExplorer' --include='*.go' . || true
  go test ./internal/domain/hullbuy/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:' || true
  ```
  **Expected hits — exactly these, plus `ok`:**
  - `internal/domain/hullbuy/hull_class.go` — the declaration and the `DedicatedFleet` arm;
  - `internal/domain/hullbuy/hull_class_test.go` — the two rows that pin them;
  - `internal/adapters/grpc/hull_purchaser_test.go:55` — `{"explorer lands in the explorer fleet", hullbuy.HullClassExplorer, "explorer", true}`, **a live row in a shared `BuyAndDedicate` table test**. It is not a defect and not in scope: it exercises the generic dedicate-at-purchase mapping through the surviving `hullbuy` vocabulary, and it goes with the constant in slice 4.

  **The constant is therefore NOT "zero references outside its own package"** — that claim is false and must not be repeated in the commit message. What IS true, and is the whole basis of the deferral, is the stronger operational statement: **nothing constructs an explorer order and no provider registers the class, so the constant cannot reach a purchase.** RULINGS #4 is untouched either way.

- [ ] Commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "refactor(fleet,grpc,config): delete the explorer class config, its 5 tune keys and the autosizer class arms (sp-mn3it)

  autosizer_explorer_hulls_enabled and its four siblings are gone from
  fleetAutosizerConfigKeys, from the launch-config write/read, from
  FleetAutosizerConfig, from RunFleetAutosizerCoordinatorCommand,
  autosizerRunConfig, DemandParams and the coordinator defaults. Leaving an arming
  key for a class that no longer exists would advertise a seam an operator could set
  and get silence from. Verified beforehand that no checked-in YAML sets any of them.

  classDisabled and classGuardConfig lose their HullClassExplorer arms; both now fall
  to the 'unknown class: never act' default. Four test files go with them — the explorer
  guard block, the heavy-cap scope arm, the opt-in reconcile test and the three live
  round-trips. go build says nothing about any of them; go vet is what catches them.

  DEFERRED to the autosizer deletion: hullbuy.HullClassExplorer and the 'explorer' arm
  of hullbuy.DedicatedFleet. hullbuy is a shared vocabulary owned by no coordinator and
  imported by the dedicated contract scaler too. The constant still has a live reference
  outside hullbuy — grpc/hull_purchaser_test.go:55, a row in a shared BuyAndDedicate
  table — so it is NOT reference-free; what it IS after this commit is UNREACHABLE:
  nothing constructs an explorer order and no provider registers the class, so it cannot
  reach a purchase. Touching it here would only create conflict surface with slice 4."
  ```

---

## Task 8 — Delete the universe roster stack, each item verified individually

`universe_systems.go`, `client_universe.go`'s `ListSystems`, and `domain/system/universe.go` are explorer-only *in practice*. **Verify each on its own before deleting it — do not delete as a batch.**

**Files:**
- Delete: `gobot/internal/adapters/expansion/universe_systems.go` (253 lines)
- Delete: `gobot/internal/adapters/expansion/universe_systems_test.go` (374 lines)
- Modify: `gobot/internal/adapters/api/client_universe.go` — delete `ListSystems` *(advisory ~`:126-173`)*. The file keeps `GetJumpGate`, `GetWaypoint`, `ListWaypoints`, `GetConstruction`, `SupplyConstruction`, `extractSystemSymbol`
- Delete: `gobot/internal/adapters/api/client_systems_test.go` (55 lines — it tests only `ListSystems`)
- Delete: `gobot/internal/domain/system/universe.go` (21 lines — `SystemAPIData`, `SystemsListResponse`)

**Interfaces:**
- Consumes: `player.PlayerRepository`, `shared.Clock`
- Produces: removes `expansion.UniverseSystemsCache` + `NewUniverseSystemsCache(lister UniverseLister, playerRepo player.PlayerRepository, clock shared.Clock, ttl time.Duration) *UniverseSystemsCache`, `expansion.UniverseLister`, `(*api.SpaceTradersClient).ListSystems(ctx context.Context, token string, page, limit int) (*system.SystemsListResponse, error)`, `system.SystemAPIData`, `system.SystemsListResponse`

**Steps:**

- [ ] **Verify `universe_systems.go` individually.** Its only consumer was the selector's construction in `main.go`, removed in Task 3; its only in-package consumer was `off_gate_target.go`, deleted in Task 5.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'UniverseSystemsCache\|NewUniverseSystemsCache\|UniverseLister' --include='*.go' . || true
  echo '--- AllSystems, SCOPED (see below) ---'
  grep -rn 'AllSystems' --include='*.go' internal/adapters/expansion/ || true
  ```
  **Expected hits:** only inside `internal/adapters/expansion/universe_systems.go` and `universe_systems_test.go`.
  **`AllSystems` is deliberately scoped to `internal/adapters/expansion/`.** Unscoped it is a substring of the live, entirely unrelated `FindCheapestMarketsSellingAllSystems` (and of `TestFindIdleLightHaulers_EmptySystemFilter_ReturnsAllSystems`) — 11 extra hits across 7 files in `adapters/persistence`, `application/contract` and `application/trading`. Every one is live code, so an unscoped grep here is a guaranteed FALSE HALT under this task's own stop-rule.

- [ ] Delete it.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/adapters/expansion/universe_systems.go internal/adapters/expansion/universe_systems_test.go
  go build ./internal/adapters/expansion/...
  ```
  Expect no build output. The rest of the `expansion` package — `adapters.go`, `bootstrap_phase.go`, `candidates.go`, `frontier_bearing.go`, `shipyard_backfill.go` — is the expansion scanner, probe-buyer-fleet and shipyard-backfill coordinators' surface and stays.

- [ ] **Verify `ListSystems` individually — and check the port interfaces too.** A method on a concrete client can also be *required* by an interface it is passed as; `grep` on the name catches both the calls and any interface that declares it.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'ListSystems' --include='*.go' . || true
  echo '--- does any port interface require it? ---'
  grep -rn 'ListSystems' --include='*.go' internal/domain/ports/ internal/domain/routing/ \
    || echo "NO DOMAIN PORT DECLARES ListSystems — correct"
  ```
  **Expected hits, first grep:** only `internal/adapters/api/client_universe.go` (the declaration) and `internal/adapters/api/client_systems_test.go`.
  **Expected, second grep:** `NO DOMAIN PORT DECLARES ListSystems — correct` — so removing the method breaks no interface satisfaction.

- [ ] **Delete `ListSystems`.** `gobot/internal/adapters/api/client_universe.go` *(advisory ~`:126-173`)*. Large deletion — anchors below. **Its tail is deliberately NOT used as an anchor**: the method ends with the same six lines as `ListWaypoints` immediately above it (`Total`/`Page`/`Limit`/`},`/`}, nil`/`}`), so a tail match is ambiguous. Address the end by the FIRST SURVIVING LINE instead.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  F=internal/adapters/api/client_universe.go
  grep -c '^// ListSystems retrieves one page of the universe system list' "$F"
  grep -c '^// GetConstruction retrieves construction site information for a waypoint$' "$F"
  ```
  **HEAD ANCHOR** — this line and the **blank line immediately above it** are both deleted:
  ```go
  // ListSystems retrieves one page of the universe system list (GET /systems) with
  ```
  **TAIL RULE** — delete through the `}` that closes `func (c *SpaceTradersClient) ListSystems(...)`, i.e. the last line before the blank line that precedes this **FIRST SURVIVING LINE**:
  ```go
  // GetConstruction retrieves construction site information for a waypoint
  ```
  **Post-condition:** exactly ONE blank line between the `}` closing `ListWaypoints` (which stays) and `// GetConstruction …`. Confirm the identifier is gone:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -n 'ListSystems' internal/adapters/api/client_universe.go || echo "NO ListSystems IN client_universe.go — correct"
  ```
  Then delete its test:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/adapters/api/client_systems_test.go
  ```

- [ ] **Verify `domain/system/universe.go` individually — by TYPE, not by package.** The `system` package is imported by ~80 files, but that is for `WaypointAPIData`, `PaginationMeta`, the navigation graph and the gate types. Only the two types declared in `universe.go` matter here.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn 'SystemAPIData\|SystemsListResponse' --include='*.go' . || true
  ```
  **Expected hits:** ONLY inside `internal/domain/system/universe.go` itself (the two declarations, their doc comments, and the `Data []SystemAPIData` field). **If any other file appears, stop** — the type has a consumer outside the retired stack and must not be deleted.

- [ ] Delete it, and confirm `PaginationMeta` (which `SystemsListResponse` referenced) survives in its own file — it is used by `WaypointsListResponse`.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  rm internal/domain/system/universe.go
  grep -rn 'type PaginationMeta' --include='*.go' internal/domain/system/
  ```
  Expect exactly one hit, in `internal/domain/system/ports.go`. (This grep is deliberately NOT `|| true`: zero hits IS the failure — it would mean `PaginationMeta` had been declared in the deleted file, in which case restore the file with only that type in it and re-check.)

- [ ] Build, vet, format, and run the affected packages.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./...
  gofmt -w .
  test -z "$(gofmt -l .)"
  go test ./internal/adapters/api/ ./internal/adapters/expansion/ ./internal/domain/system/ 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok|\?)|panic:|DATA RACE' || true
  ```
  Expect no build/vet/gofmt output, **two `ok` lines** — `internal/adapters/api` and `internal/adapters/expansion` — and for `internal/domain/system` a `?   …  [no test files]` line, **not** an `ok`. That package has **zero** `_test.go` files (after this task it holds only `navigation_graph.go` and `ports.go`), so an `ok` for it is impossible and demanding three would be a stop-rule that can never be satisfied. If `internal/adapters/api` reports `TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient`, that is the known pre-existing flake (sp-odim4) — re-run that package once to confirm.

- [ ] Commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "refactor(expansion,api,domain): delete the universe roster stack (sp-mn3it)

  Each item verified on its own before deletion, not as a batch:
    - UniverseSystemsCache — sole consumer was off_gate_target.go, already gone;
    - SpaceTradersClient.ListSystems — sole caller was the cache, and NO domain port
      interface declares it, so removing the method breaks no satisfaction;
    - domain/system/universe.go — verified BY TYPE (SystemAPIData, SystemsListResponse),
      not by package: internal/domain/system is imported by ~80 files for
      WaypointAPIData/PaginationMeta/the navigation graph, none of which move.

  GET /systems is no longer called anywhere. The gate network is the reachable universe."
  ```

---

## Task 9 — Prove the manual `spacetraders ship warp` verb still works

Retirement removed one of two callers of the warp stack. This task proves the other one is intact, end to end from the CLI verb down to the executor.

**Files:**
- Modify: none (verification only), except the **seven** prose fixes listed below (six files, ten stale lines)

**PROTECTED — never renamed, never edited, never swept by this task:** `internal/application/ship/route_executor_warp_test.go`, `internal/application/ship/route_executor_warp_persistence_test.go`, `internal/application/ship/commands/navigation/warp_ship_test.go`. Between them they hold **22 live code references** to `explorer` — fixture constructors (`newWarpExplorerShip`), ship symbols and `MODULE_WARP_DRIVE_*` identifiers. They are the very evidence this task exists to produce. The sweep below is anchored to comment lines precisely so it cannot reach them.

**Interfaces:**
- Consumes: `shipNav.NewWarpShipHandler(routeExecutor, shipRepo, warpWaypointSource)`, **`ship.RouteExecutor.ExecuteWarpLeg`** (the method the verb actually rides, via `shipNav.WarpLegExecutor` — **not** `ExecuteWarpRoute`), `ship.NewWarpSystemCharter`, `ship.NewAPIWarpNavigator`, `ship.NewSystemEscapeReader`, `container.ContainerTypeWarp`, the `"warp_ship"` command-factory recovery key
- Produces: nothing

**Steps:**

- [ ] Confirm the whole manual-verb chain is still wired at the composition root, hop by hop.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  echo '--- CLI verb ---'
  grep -n 'warp' internal/adapters/cli/ship_navigate.go | head -8
  echo '--- gRPC container op ---'
  grep -n 'Warp\|warp' internal/adapters/grpc/container_ops_ship.go | head -8
  echo '--- handler + registration ---'
  grep -n 'WarpShipHandler\|WarpShipCommand\|WithWarpSupport\|NewWarpSystemCharter\|NewAPIWarpNavigator\|NewSystemEscapeReader' cmd/spacetraders-daemon/main.go
  echo '--- recovery key ---'
  grep -rn '"warp_ship"\|ContainerTypeWarp' --include='*.go' .
  echo '--- the verbs OWN executor method ---'
  grep -rn 'ExecuteWarpLeg' --include='*.go' internal/application/ship/commands/navigation/
  ```
  Expect a live hit at every hop: the CLI verb, the container op, `WithWarpSupport` + `NewWarpShipHandler` + `RegisterHandler[*shipNav.WarpShipCommand]` in `main.go`, and the `"warp_ship"` factory key with `ContainerTypeWarp`. **None of these greps carries `|| true`: a zero result IS the failure this step exists to catch.** The `main.go` hops sat at base-tree `:1127` / `:1145` / `:1146` and have drifted ~14 lines earlier by now — match the names, not the numbers.
  The last grep is the one that matters most: `warp_ship.go:16-17` (the `WarpLegExecutor` interface) and `warp_ship.go:98` (the call). **That is the verb's executor entrypoint.** `ExecuteWarpRoute` is a different method and is now caller-less by design (sp-bwzy3) — its absence from any caller list here is expected and is not a hop that failed.

- [ ] Run the whole warp test set — CLI, gRPC container op, handler, executor, persistence, charter, escape reader, API client.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./internal/adapters/cli/ ./internal/adapters/grpc/ ./internal/adapters/api/ ./internal/application/ship/... \
    -run 'Warp|warp' -v 2>&1 | grep -E '^(FAIL|--- FAIL|--- PASS|ok)|panic:|DATA RACE' || true
  ```
  Expect `--- PASS` for at least these, and zero `--- FAIL`:
  `TestShipWarpCommandIsRegisteredAlongsideItsSiblings`, `TestShipWarpRequiresShipAndDestinationFlags`, `TestShipWarpHelpPointsAtWhereARefusalIsReported`, `TestWarpShipSpawnsATrackedContainerClaimingTheHull`, `TestRefusedWarpReportsTheStrandNumbersInTheContainerLog`, `TestWarpShipCommandRebuildsFromPersistedConfigOnRestart`, `TestWarpAdditionLeavesSiblingShipVerbsUntouched`, `TestWarpShipPostsWaypointSymbolAndParsesFuelAndArrival`, `TestWarpShip_WarpCapableHullReachesDestinationInAnotherSystem`, `TestWarpShip_DrivelessHullRefused_TypedErrorReachesCaller`, `TestWarpShip_ServerFuelRefusalSurfacesTypedFieldsToTheOperator`, `TestWarpShip_DeadEndDestinationRefusedBeforeAnyWarpCall`, `TestWarpShip_UnresolvableDestinationFailsClosed`, `TestWarpShip_UnloadableHullReportedHonestly`, `TestWarpShip_RejectsWrongRequestType`, `TestExecuteWarpLeg_WarpsToReachableSystemWithAdequateFuel`, `TestExecuteWarpLeg_TakesTheFuelVerdictFromTheServer`, `TestExecuteWarpLeg_RefuelsToTheServersRequirementAndRetriesOnce`, `TestExecuteWarpLeg_RefusesDestinationTheHullCouldNeverLeave`, `TestExecuteWarpLeg_AllowsDestinationWithAWayOut`, `TestExecuteWarpRoute_MultiHopRefuelsBetweenLegs`, `TestExecuteWarpLeg_ChartsDestinationSystemOnArrival`, `TestExecuteWarpLeg_RefusesShipWithoutWarpDrive`, `TestExecuteWarpLeg_PersistsTheNewSystem_SoTheNextTickDoesNotPlanFromTheStaleOrigin`, `TestExecuteWarpLeg_PersistsTheDepartureSoTheStuckShipSweeperCanSeeTheHull`, `TestExecuteWarpRoute_PersistsEachCompletedLegBeforeAttemptingTheNext`, `TestExecuteWarpLeg_FailsClosedWhenTheDeparturePositionCannotBePersisted`, `TestExecuteWarpLeg_RecordsTheLandingSoTheRowStopsClaimingATransit`, `TestExecuteWarpLeg_RefusesAWarpItCannotRecord`, `TestWarpSystemCharter_ChartsGateEdgesWaypointsMarketsShipyards`.
  The previously-passing `TestOffGateSelect_ExcludesOutOfWarpRange` will NOT appear — its file was deleted in Task 5. That absence is expected; every other name above must still appear and pass.

- [ ] **Sweep for the stale warp-stack comments — ANCHORED TO COMMENT LINES.** The pattern matches only lines whose first non-space characters are `//`. That anchoring is what keeps the three PROTECTED warp test files out of the result: their `explorer` references are in **code**, not comments.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn --include='*.go' -E '^[[:space:]]*//' internal/application/ship/ cmd/spacetraders-daemon/main.go \
    | grep -Ei 'explorer|slice B|slice C' || true
  ```
  **Expected hits at this point — exactly these ten lines across six files:**
  - `internal/application/ship/route_executor.go:107`
  - `internal/application/ship/route_executor_warp.go:15`, `:16`, `:36`
  - `internal/application/ship/route_executor_warp_errors.go:14`, `:16`
  - `internal/application/ship/warp_system_charter.go:12`
  - `internal/application/ship/commands/navigation/warp_ship.go:36`
  - `cmd/spacetraders-daemon/main.go:1120`
  - `internal/application/ship/route_executor_warp_test.go:154` — **PROTECTED. Do NOT edit.** It is the doc comment on the `newWarpExplorerShip` fixture, describing a warp-capable hull. It names no retired mechanism, and renaming the fixture would touch a live test.

  **If any `cmd/spacetraders-daemon/main.go` line other than `:1120` still appears, an earlier task was left unfinished** — the bridge comment block and the off-gate write-side block are Task 3's and Task 6's, not this task's. Go back and finish them rather than editing them here.

  **This is the last chance to catch these.** Task 10's structural guard matches **identifiers**, not English, and contains none of these strings — nothing downstream will find a missed one.

- [ ] **Fix the seven comment sites.** All content-addressed, all independent, any order. Prose only — no behaviour.
  Uniqueness checks — each must print `1`:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -c "until a caller (slice C's explorer) invokes ExecuteWarpRoute." internal/application/ship/route_executor.go
  grep -c 'the clean, callable entrypoint slice B (off-gate target selection) and slice C' internal/application/ship/route_executor_warp.go
  grep -c 'range; slice B hands the ordered intermediate targets here and this drives each' internal/application/ship/route_executor_warp.go
  grep -c 'ship with no MODULE_WARP_DRIVE_\* installed. Only a SHIP_EXPLORER' internal/application/ship/route_executor_warp_errors.go
  grep -c 'happen as a side effect of the frontier explorer dispatcher choosing to send a hull' internal/application/ship/commands/navigation/warp_ship.go
  grep -c 'SHIP_EXPLORER warps into a fresh cluster off the jump-gate network, this is' internal/application/ship/warp_system_charter.go
  grep -c 'Its callers are the frontier explorer' cmd/spacetraders-daemon/main.go
  ```

  **(1) `internal/application/ship/route_executor.go`** *(advisory ~`:107`)* — **do NOT write "the manual 'ship warp' verb" as the caller here; that would be a new false claim.** The verb rides `ExecuteWarpLeg`; `ExecuteWarpRoute` has no caller at all after this slice.
  FIND:
  ```go
  // until a caller (slice C's explorer) invokes ExecuteWarpRoute.
  ```
  REPLACE WITH:
  ```go
  // until a caller invokes ExecuteWarpRoute. It has NO caller today: the off-gate explorer
  // dispatcher was its only one and is retired (sp-mn3it); the manual 'ship warp' verb rides
  // ExecuteWarpLeg instead. Whether the multi-leg route survives is filed as sp-bwzy3.
  ```

  **(2) `internal/application/ship/route_executor_warp.go`** *(advisory ~`:15-16`)* — **both lines**: `:15` ends mid-clause, so replacing only `:16` severs the sentence.
  FIND:
  ```go
  // the clean, callable entrypoint slice B (off-gate target selection) and slice C
  // (the explorer hull) invoke with a chosen target waypoint + ship.
  ```
  REPLACE WITH:
  ```go
  // the clean, callable entrypoint a caller invokes with a chosen target waypoint + ship —
  // today the manual 'ship warp' verb (shipNav.WarpLegExecutor).
  ```

  **(3) `internal/application/ship/route_executor_warp.go`** *(advisory ~`:36`)* —
  FIND:
  ```go
  // range; slice B hands the ordered intermediate targets here and this drives each
  ```
  REPLACE WITH:
  ```go
  // range; a caller hands the ordered intermediate targets here and this drives each
  ```

  **(4) `internal/application/ship/route_executor_warp_errors.go`** *(advisory ~`:14-16`)* —
  FIND:
  ```go
  // ship with no MODULE_WARP_DRIVE_* installed. Only a SHIP_EXPLORER
  // carries the drive; refusing here keeps the executor from emitting a warp the
  // live API would reject, and gives slice B/C a typed signal to pick a warp-capable
  ```
  REPLACE WITH:
  ```go
  // ship with no MODULE_WARP_DRIVE_* installed. Refusing here keeps the executor from
  // emitting a warp the live API would reject, and gives the caller a typed signal to
  // pick a warp-capable
  ```

  **(5) `internal/application/ship/commands/navigation/warp_ship.go`** *(advisory ~`:36`)* —
  FIND:
  ```go
  // happen as a side effect of the frontier explorer dispatcher choosing to send a hull
  ```
  REPLACE WITH:
  ```go
  // happen as a side effect of any coordinator choosing to send a hull
  ```

  **(6) `internal/application/ship/warp_system_charter.go`** *(advisory ~`:11-12`)* — **both lines, or you produce "When a a".** `:11` ends `// SystemCharter charts a whole star system on warp arrival. When a`, so substituting `:12` alone with a phrase that itself begins "a warp-capable hull" duplicates the article.
  FIND:
  ```go
  // SystemCharter charts a whole star system on warp arrival. When a
  // SHIP_EXPLORER warps into a fresh cluster off the jump-gate network, this is
  ```
  REPLACE WITH:
  ```go
  // SystemCharter charts a whole star system on warp arrival. When a warp-capable
  // hull arrives in a fresh cluster off the jump-gate network, this is
  ```

  **(7) `cmd/spacetraders-daemon/main.go`** *(advisory ~`:1120-1121`)* — the surviving line above is quoted and written back so the sentence stays intact.
  FIND (note the leading TAB on both lines — this comment is inside a function body):
  ```go
  	// graph provider as its waypoint source. Its callers are the frontier explorer
  	// dispatcher and the `ship warp` verb wired just below.
  ```
  REPLACE WITH:
  ```go
  	// graph provider as its waypoint source. Its caller is the `ship warp` verb wired just
  	// below.
  ```

- [ ] Re-run the anchored sweep and the warp set, and confirm nothing moved.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn --include='*.go' -E '^[[:space:]]*//' internal/application/ship/ cmd/spacetraders-daemon/main.go \
    | grep -Ei 'explorer|slice B|slice C' || true
  go build ./...
  gofmt -w .
  test -z "$(gofmt -l .)"
  go test ./internal/adapters/cli/ ./internal/adapters/grpc/ ./internal/adapters/api/ ./internal/application/ship/... \
    -run 'Warp|warp' 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```
  **Expected surviving sweep hits — exactly two, both correct:**
  - `internal/application/ship/route_executor.go` — the new comment from fix (1), which names the retired dispatcher deliberately and accurately;
  - `internal/application/ship/route_executor_warp_test.go:154` — the PROTECTED fixture doc comment.
  No `slice B`/`slice C` may survive anywhere. Then expect no build/gofmt output and `ok` for each package.

- [ ] Commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "test(ship): prove the manual 'spacetraders ship warp' verb survives the retirement (sp-mn3it)

  Retirement removed the off-gate caller of the warp stack. The whole chain is re-verified
  hop by hop at the composition root — CLI verb, gRPC container op, WarpShipHandler,
  WithWarpSupport, the charter/navigator/escape-reader, ContainerTypeWarp and the
  'warp_ship' restart-recovery key — and all 30 warp tests across cli/grpc/api/ship still
  pass. The verb's own executor entrypoint is ExecuteWarpLeg (shipNav.WarpLegExecutor),
  which keeps its live caller at warp_ship.go:98.

  Seven comments across six files that named the retired explorer dispatcher, SHIP_EXPLORER
  or 'slice B/C' are corrected. route_executor.go now states honestly that it has NO caller
  after this slice — the dispatcher was its only one — and points at sp-bwzy3 rather than
  claiming the manual verb calls it. The three warp TEST files keep their explorer-named
  fixtures untouched: they are live evidence, not archaeology."
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

**Before you write it: this test can only pass if Task 7 landed in full.** The sp-r7eiu note inside `fleet_autosizer_guards_test.go`'s explorer block carries the string `ExplorerDemandProvider` in a comment, and it is on the vocabulary list below. Task 7's guards_test deletion is what removes it. If this test fails on its first run naming that file, the fix is to finish Task 7, not to weaken the list.

- [ ] Write the durable invariant. Create `gobot/cmd/spacetraders-daemon/off_gate_retired_test.go` with exactly this content. It is a **new** composition-root pin test in the spirit of `heavy_target_single_instance_test.go`, which reads source because nothing else can see its invariant — but note the two are not the same shape: `heavy_target_single_instance_test.go` reads **one** file (`main.go`, located via `gatePaths` → `runtime.Caller(0)`) and parses it with `go/ast`; this one walks the whole tree with `filepath.WalkDir` and matches strings. Both are legitimate; do not "follow the existing idiom" by switching to `runtime.Caller`, because a tree-wide absence claim needs the whole tree. The `root := "../.."` below is correct on its own terms: `go test` runs with the package directory as cwd, and `cmd/spacetraders-daemon` is two levels below the module root.
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

- [ ] Run it and see it PASS.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test ./cmd/spacetraders-daemon/ -run TestOffGateWarpExpansionStaysRetired 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|panic:|reappeared' || true
  ```
  Expect `ok`. **If it names `fleet_autosizer_guards_test.go` for `"ExplorerDemandProvider"`, Task 7's guards_test deletion was skipped or truncated — go back and finish it.** Any other file named here is a genuine survivor of an earlier task and must be tracked down, not excluded from the list.

- [ ] **Mutation-probe the guard** — a test that has never been seen to fail is not a guard. Patch, verify the patch applied, run, and restore in ONE invocation, so an interrupted run cannot leave the tree dirty. The witness string is chosen so it cannot survive its own mutation.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  printf 'package main\n\n// probe fixture: sp-mn3it mutation check\ntype probeOffGateSink interface{ EmitOffGateDemand(playerID int, signal int) }\n' > cmd/spacetraders-daemon/zz_offgate_probe.go
  grep -q 'EmitOffGateDemand' cmd/spacetraders-daemon/zz_offgate_probe.go && echo "PROBE APPLIED"
  go test ./cmd/spacetraders-daemon/ -run TestOffGateWarpExpansionStaysRetired 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)|reappeared' || true
  rm -f cmd/spacetraders-daemon/zz_offgate_probe.go
  go test ./cmd/spacetraders-daemon/ -run TestOffGateWarpExpansionStaysRetired 2>&1 \
    | grep -E '^(FAIL|--- FAIL|ok)' || true
  ```
  Expect, in order: `PROBE APPLIED`, then `--- FAIL: TestOffGateWarpExpansionStaysRetired` naming `"EmitOffGateDemand"` and `zz_offgate_probe.go`, then after the restore `ok`. **If the probed run passes, the guard is inert** — most likely the walk root or the self-exclusion is wrong. Fix it before continuing.

- [ ] Confirm the probe file is gone and the tree is clean.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git status --short -- gobot/
  ```
  Expect only `?? gobot/cmd/spacetraders-daemon/off_gate_retired_test.go` (plus any uncommitted prose from earlier tasks). **No `zz_offgate_probe.go`.**

- [ ] **Compare the comment ratchet against the Orientation capture.** The repo's own recorded baseline is stale and already fails on six of these packages on an untouched tree, so it is not the gate. The gate is: **no package regresses relative to the pre-Task-1 capture.**
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  test -s "${TMPDIR:-/tmp}/offgate-comment-baseline.txt"
  make comment-audit 2>&1 | grep -E '^(internal/application/parkedsensing|internal/application/scouting/commands|internal/application/fleet/commands|internal/adapters/expansion|internal/adapters/grpc|internal/adapters/api|internal/adapters/metrics|internal/infrastructure/config|internal/domain/system|internal/application/ship|cmd/spacetraders-daemon)[[:space:]]' \
    | sort > "${TMPDIR:-/tmp}/offgate-comment-after.txt"
  join -j 1 \
    <(awk '{print $1, $2}' "${TMPDIR:-/tmp}/offgate-comment-baseline.txt" | sort) \
    <(awk '{print $1, $2}' "${TMPDIR:-/tmp}/offgate-comment-after.txt"    | sort) \
    | awk '{ if ($3+0 > $2+0) printf "REGRESSED  %-48s %s -> %s\n", $1, $2, $3; else printf "ok         %-48s %s -> %s\n", $1, $2, $3 } END { printf "compared %d package(s)\n", NR }'
  ```
  Expect **every line to start `ok`** and `compared 11 package(s)`. **Fewer than 11 compared means a package dropped out of one of the two reports and was silently skipped — stop and find out which.** For any `REGRESSED` line, TRIM the stalest archaeology in that package (`ENGINEERING.md §6`) and re-run. Deleting a heavily-commented driver raises the *ratio* of what remains, so a regression here is expected in principle and is fixed by trimming, never by re-baselining. Do **not** run `make comment-audit-baseline`.

- [ ] **Sweep for any remaining explorer/off-gate prose the structural guard cannot see.** The retired-vocabulary test above matches **identifiers**, not English — none of the stale comments Task 9 fixed would have tripped it, and nor will any that were missed.
  **This sweep is BOUNDED to the packages this plan touches, for two reasons.** Unbounded it returns **819 hits at base**, including generated protobuf (`pkg/proto/daemon/daemon.pb.go`, `daemon_grpc.pb.go`) and a large live surface in `internal/domain/` (`domain/navigation/ship_specs.go`, `ship_module.go`, `domain/shipyard/inventory.go`), `internal/adapters/cli` (the `ship warp` help text and examples), `internal/application/trading/commands` (off-gate *navigation* recovery tests, unrelated to warp expansion) and `internal/application/shipyard/commands` — every one of them live, correct, and out of scope. A sweep that returns them is not a stop-rule, it is noise.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  grep -rn --include='*.go' -Ei 'off-gate|offgate|SHIP_EXPLORER|explorer' \
    internal/application/parkedsensing \
    internal/application/scouting/commands \
    internal/application/fleet/commands \
    internal/adapters/expansion \
    internal/adapters/grpc \
    internal/adapters/api \
    internal/adapters/metrics \
    internal/infrastructure/config \
    internal/application/ship \
    cmd/spacetraders-daemon \
    | grep -v 'hull_purchaser_test.go' \
    | grep -v 'off_gate_retired_test.go' || true
  ```
  **Every surviving hit must fall in one of these four classes:**
  1. a deliberate, accurate reference to the retirement itself (naming `sp-mn3it`) — e.g. the new `route_executor.go` comment;
  2. `internal/application/scouting/commands/probe_sensing_stall.go` — `expansionStallCoordinator = "off_gate_expansion"`, the LIVE coordinator label (see Task 3);
  3. the three PROTECTED warp test files' live fixtures (`route_executor_warp_test.go`, `route_executor_warp_persistence_test.go`, `commands/navigation/warp_ship_test.go`);
  4. prose you now fix.
  **Nothing may still describe the explorer as a live class or a live caller.** `internal/domain/hullbuy` and `grpc/hull_purchaser_test.go` are excluded by design — that is the surface deferred to slice 4. `pkg/proto/` and `internal/domain/` are outside the bound for the reasons above.

- [ ] Full sweep: build, vet, format, and the whole suite with the race detector. This is the pre-gate gate.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go build ./...
  go vet ./...
  gofmt -w .
  test -z "$(gofmt -l .)"
  go test -race ./... 2>&1 | grep -E '^(FAIL|--- FAIL)|panic:|DATA RACE' || true
  ```
  Expect no output at all from the final grep. The only tolerated failure is `TestRequestFallsBackToGlobalBudgetTrackerWhenNoneSetOnClient` in `internal/adapters/api` (known flake, sp-odim4) — if it appears, re-run just that package to confirm it passes on a retry:
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec/gobot
  go test -race -count=1 ./internal/adapters/api/ 2>&1 | grep -E '^(FAIL|--- FAIL|ok)|panic:|DATA RACE' || true
  ```

- [ ] Final acceptance check against the bead's criteria — read them back and confirm each has evidence above.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders
  bd show sp-mn3it
  ```
  Confirm: (1) `advanceOffGate` and its call site removed — Task 1 + Task 2; (2) the explorer-demand bridge proven RETRACTED by test, not merely unread — Task 1's RED→GREEN behavioural proof plus Task 6's zero-write-call-sites enumeration plus this task's structural guard; (3) the two orphaned interface declarations removed — Task 4; (4) the manual `spacetraders ship warp` verb still works and its tests still pass — Task 9; (5) no explorer demand can be emitted from any path — the structural guard, mutation-probed; (6) build, vet, gofmt, full `-race` green — this task.

- [ ] Commit.
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git add -A gobot/
  git commit --no-verify -m "test(daemon): pin off-gate warp expansion RETIRED with a structural guard (sp-mn3it, epic sp-sy3dl)

  The behavioural proof from the first commit could not outlive the port it observed —
  an assertion cannot survive its own mechanism. Its successor asserts what IS durable:
  the retired vocabulary is absent from the source tree, so nothing can satisfy it. That
  is the only sound form of 'no implementation exists' for implicitly-satisfied Go
  interfaces, where searching for implementations proves nothing.

  Mutation-probed: reintroducing EmitOffGateDemand in a scratch file fails the guard by
  name, and the restore returns it to green. Note what it does NOT cover: it matches
  identifiers, not English, so stale explorer PROSE is invisible to it — that is swept
  separately, bounded to the packages this plan touches. Comment density compared against
  a capture taken on the pre-change tree (the repo's recorded baseline is stale and
  already red on six of these packages); no package regressed. Build, vet, gofmt and the
  full -race suite green."
  ```

- [ ] Hand off to the gate. Do **not** run captain-gate from this lane — report the branch and the commit list to the lead (RULINGS #13: all code lands via worktree → captain-gate → main, never merged by hand).
  ```bash
  set -euo pipefail
  cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/.claude/worktrees/sp-offgate-exec
  git log --oneline main..sp-offgate-exec
  git status --short
  ```
  Expect the task commits listed and a clean working tree.

---

## Self-review record

Run before this plan was committed, per the writing-plans self-review.

**1. Spec coverage.** Every requirement in sp-mn3it and in the parent spec's off-gate section maps to a task:

| Requirement | Task |
|---|---|
| An executor worktree exists before the first cut | Orientation |
| A comment-density baseline captured on the pre-change tree | Orientation (step 3), gated in 10 |
| Prove the bridge retracts BEFORE removing anything | 1 |
| `advanceOffGate` call site removed | 1 |
| `retractOffGateDemand` call site removed | 1 |
| `reachesAny` and its sole caller | 1 (caller), 2 (method) |
| The driver functions + `offgate_test.go` | 2 |
| `OffGatePorts` and its four interfaces | 3 |
| `main.go` fields and the ports literal | 3 |
| `ExpandPorts.OffGate`, `ExpandReport.OffGate*`, `SensingEnginePorts.OffGate` | 3 |
| `stallReasonOffGateNoTarget` + `rep.OffGate*` reads | 3 |
| The two orphaned declarations (`OffGateTargetSelector`, `ShipyardCoverageReader`) | 4 |
| Explorer-only adapters, **each individually verified** | 5 (three), 6 (the bridge) |
| The method warning as a STEP, not a footnote | 3 (headline step), 4, 5, 6, 8 (applied per item) |
| The autosizer read chain forced by the bridge deletion | 6 |
| The 5 `explorer` tune keys + config surface | 7 |
| `hullbuy.HullClassExplorer` + `DedicatedFleet` decision, with reason | Orientation §"Scope line", confirmed by a Task 7 step |
| `heavy` config surface that becomes dead | Orientation §"Scope line" — **none does**, stated with the reason |
| The four autosizer **test** files broken by the class arms, each with a disposition | 7 |
| `universe_systems.go`, `ListSystems`, `domain/system/universe.go`, each individually verified | 8 |
| Manual `ship warp` verb exercised and proven passing | 9 |
| `ExecuteWarpRoute` stays, caller-less by design; fate filed separately | Orientation §"DECISION", 5 (stop-rule), 9 (honest comment) — bead **sp-bwzy3** |
| Build, vet, gofmt, full `-race` green | 10 |

**2. Placeholder scan.** No "TBD", no "add appropriate error handling", no "write tests for the above", no "similar to Task N". Every edit quotes the exact current text of the block it changes and the exact text to put in its place; every deletion of more than a handful of lines quotes a head anchor, a tail anchor and the first surviving line. Every step carries a runnable command or literal before/after code. **Line numbers throughout are advisory hints only — see THE ADDRESSING RULE in Global Constraints.**

**3. Type/name consistency.** Cross-checked across tasks: `OffGatePorts` / `OffGateSelector` / `OffGateDemandSink` / `ExplorerFinder` / `ExplorerDispatcher` are the *live* parkedsensing ports (Task 3); `OffGateTargetSelector` and `ShipyardCoverageReader` are the *orphans* in `offgate_types.go` (Task 4) and are never conflated with them. The adapter side uses `OffGateWarpTargetSelector`, `ExplorerOffGateBridge`, `IdleExplorerPort`, `ExplorerWarpDispatcher`, `UniverseSystemsCache`. The autosizer side uses `OffGateDemandSource`, `ExplorerFleetSource`, `ExplorerDemandProvider`, `autosizerExplorerFleetSource`. `NewFleetAutosizerCoordinatorHandler`'s post-Task-6 signature is stated once, in full, in Task 6's Interfaces block, and the Task 6 vet step names the arity break it will catch.

**Six fixes applied inline during the first review.**
(a) Task 3 originally left `offGateSelector`, `idleExplorerPort` and `explorerWarpDispatcher` constructed in `main.go` for Task 5 to clean up — but removing their only *use* makes them declared-and-unused, which is a Go compile error, so their deletion moved into Task 3 with the reason recorded.
(b) Task 4's method-warning step originally read as a name grep; it was rewritten to make the sound argument — there is no field, parameter or variable anywhere *of either orphan's interface type*, so no value can be assigned to one and no implementation is reachable regardless of what shapes exist.
(c) Task 1's `expansion_spend_pause_test.go` deletion was off by two lines at the head; it now takes the blank separator, and is addressed by head/tail/first-survivor anchors rather than by a range at all.
(d) Task 3's `probe_sensing_stall_test.go` step deleted four rows; the fourth (`Discovered: 2`) is real non-off-gate coverage, so it is now kept with the retired field stripped and the case renamed, and the table's doc comment is rewritten — all as ONE atomic FIND/REPLACE.
(e) Task 6's `fleet_autosizer_demand_ports.go` cut would have taken the first line of `countShips` — a helper shared with the light and heavy fleet sources. `countShips`'s signature is now quoted inside the FIND block and written back, so the mistake is unrepresentable.
(f) Task 7's `fleet_autosizer_types.go` deletion quoted the block with one line elided as `...`; the full block is now inline, since an executor reading tasks out of order cannot reconstruct an elision.

---

## Revision record 1 — 2026-08-06 (after the FIRST adversarial verification)

The first draft **failed adversarial verification**: 11 independent agents ran it against the real code and returned **NO-GO — 8 BLOCKERs (all reproduced), 19 MAJORs**. Tasks 1, 2, 3, 6 and 7 each broke the build or halted on their own stop-rule; because the plan is a deletion cascade, the first failure stranded the other nine. Every finding was re-confirmed against `main @e9d930d2` before being applied. Eleven classes of fix landed:

1. **Six off-by-one line ranges corrected**, each verified with `sed -n` on the boundaries ±1: `expansion.go` 465-474 → 465-475; `reach.go` 286-303 → 285-304 (two sites); `probe_sensing_stall.go` 183-185 → 183-186; `fleet_autosizer_ports.go` 44 → 45 (`:44` is the LIVE `scannedYards` parameter — three sites); `run_fleet_autosizer_coordinator.go` 313-329 → 313-328 (the file is 328 lines); `client_universe.go` 127-173 → 126-173.
2. **The orphaned `parkedsensing` import** at `main.go:36` added to Task 3 — the ports literal is its only identifier use — together with the second stale sentence in the tick's doc comment.
3. **Four test files added to Task 7 with explicit dispositions** (`fleet_autosizer_guards_test.go`, `fleet_autosizer_heavy_cap_test.go`, `run_fleet_autosizer_coordinator_test.go`, `command_factory_fleet_autosizer_live_test.go`). `go build` passes on all four; `go vet` and `go test` do not. The false claim that `HullClassExplorer` has "zero references outside its own package" was deleted — `grpc/hull_purchaser_test.go:55` is a live row — and replaced with the true, stronger statement that the constant is **unreachable**.
4. **`fleet_autosizer_heavy_wiring_test.go`** added to Task 6 (11 positional args, the 10th is the `offGateDemand` slot), and the explorer-wiring-test inventory corrected from two tests to **four**. The unconditional `rm` was right; the inventory was wrong.
5. **All six `git commit --no-verify -am "…" -- gobot/` commands replaced.** That form is invalid — reproduced on this box (git 2.50.1): `fatal: paths '…' with -a does not make sense`, exit 128, nothing committed. Now `git add -A gobot/ && git commit --no-verify -m "…"`.
6. **Task 1's expected-failure list rebuilt** from the real assertions: added `AReachableGateTargetSuppressesOffGateDemand` and `TheDemandBridgeIsWrittenEvenWhenNothingIsDemanded`; removed `AnUnreadableExplorerFleetWarpsNothing` and `ARefusedWarpDoesNotFailTheTick`, which assert only "no error + zero warps" and go green, not red. "Two existing tests assert the opposite" → **four**, named.
7. **The false `ExecuteWarpRoute` premise removed everywhere.** The manual verb rides `ExecuteWarpLeg` (`warp_ship.go:16-17`, called at `:98`); `ExecuteWarpRoute`'s only non-test caller was `explorer_dispatch_adapter.go:98`, which Task 5 deletes. Per the standing decision, **`ExecuteWarpRoute` stays** (filed as **sp-bwzy3**): Task 5's tripwire now watches `ExecuteWarpLeg`, and Task 9 no longer instructs the executor to write a false comment into `route_executor.go:107`.
8. **Three colliding greps anchored**, each of which caused a guaranteed FALSE HALT: `OffGateTarget\b` (32 hits / 8 files, matches the live `parkedsensing.OffGateTarget` struct); `AllSystems` (a substring of the live `FindCheapestMarketsSellingAllSystems`, 11 extra hits / 7 files) scoped to `internal/adapters/expansion/`; and `\.OffGate\b`, whose expected hit set was wrong in both directions. A **standing escape clause** now covers every grep stop-rule: comment-only surprises are acceptable, code surprises are not.
9. **A LINE-DRIFT RULE was added to Global Constraints** — quoted code authoritative, numbers advisory, cuts applied bottom-up within each file, `main.go` re-read before every cross-task cut. *(Superseded by revision 2: the bottom-up half of that rule was asserted in three places while the executable steps violated it, so the whole addressing mechanism was replaced. See revision record 2.)*
10. **Every `gofmt` gate fixed.** `gofmt -l` only lists and exits 0, so `gofmt -l . && git commit` was committing unformatted code; all gates are now `gofmt -w . && test -z "$(gofmt -l .)"`. Ten deletion ranges were also extended back one line to take their blank separator.
11. **Confirmed MAJOR/MINORs.** Task 4's ordering rationale was inverted and is now stated honestly (and "Task 5 completes the compile" corrected to **Task 6**). Task 9's stale-comment list went from 4 sites to 7 comments / 10 lines, including `route_executor_warp.go:15` (editing only `:16` severs a clause), `:36`, `route_executor_warp_errors.go:14-16` and `main.go:1120-1121`; `warp_system_charter.go:11-12` is now edited as a pair, since editing `:12` alone yielded "When a **a** warp-capable hull". `internal/domain/system/` has **zero** test files, so Task 8 expects two `ok` lines and one `[no test files]`, never three `ok`. `stall_metrics_test.go` keeps a retired reason label as raw strings that no build gate can see, so Task 3 re-points them. Task 1's fixture rationale was corrected: `advanceOffGate` writes the sink on the gate-reachable path too (`offgate.go:149-151`), so the sealed pocket earns its place for the **dispatch** half, not the emit half. Task 10's "follows the existing idiom" claim was corrected — `heavy_target_single_instance_test.go` reads ONE file via `runtime.Caller(0)`; the `../..` walk depth is nonetheless right.

**Found during revision 1, not in the verification report:** `main.go`'s numbers below ~1150 were stale by +12 at base; `probe_sensing_stall.go`'s `expansionStallVerdict` doc comment is 166-171, not 169-170; four more `gofmt` boundary misses in Task 7; `heavy_target_single_instance_test.go:156,179` spell the constructor inside **backtick raw-string AST fixtures** and must NOT be edited; and a full retired-identifier coverage audit for all nine strings in Task 10's list.

---

## Revision record 2 — 2026-08-06 (after the SECOND adversarial verification)

Revision 1 shipped and **failed again: 3 BLOCKERs, 11 MAJORs — and four of five round-1 defect classes RECURRED**, including inside content revision 1 had just added. Patching instances had not converged, so this revision changes the **addressing mechanism** instead.

### The structural change

**Every edit step is now CONTENT-ADDRESSED.** A step gives the executor a `grep -c` uniqueness check, the exact current text (FIND), the exact replacement (REPLACE), and a line number only as a parenthetical marked advisory. The quoted text is the contract. Concretely:

- **Ranges cannot go stale**, so a cut cannot land on the wrong code because an earlier step moved the file.
- **Off-by-one at a brace is impossible**, because the closing `}` is inside the quoted block. So is the blank separator, wherever one is part of the edit.
- **Intra-file ordering stopped mattering**, so the **bottom-up ordering rule and all nine per-file ordering banners were deleted**. Round 2 proved that rule was asserted in three places while the executable steps violated it; a rule stated and not followed is worse than no rule.
- Every FIND block that touches a boundary quotes the **surviving neighbour** and writes it back — which is what makes the two worst failure modes unrepresentable rather than merely warned against (the `scannedYards` parameter in Task 6; `reachableYardFinder`/`heavyTargetFinder` in Task 3; `countShips` in Task 6; the `if class == HullClassExplorer {` brace in Task 7).
- Large deletions use a **head anchor / tail anchor / first-surviving-line** triple, each uniqueness-checked, with an explicit blank-line post-condition.

**Every shell block now begins with `set -euo pipefail`**, and every `cd` is on its own line, so a bad path aborts before anything can print a success signature. Both round-2 examples are dead: `( cd /nonexistent && … ; echo "exit=$?" )` cannot print `exit=1` as a pass, and `out=$( cd /nonexistent && go build … )` cannot come back empty as a pass. Where a non-zero exit is genuinely expected the step carries an explicit `|| true` and says why; **no gate carries one**. Several stop-rules that used the `; echo "exit=$?"` idiom were rewritten to print an explicit sentinel (`… || echo "NO … — correct"`) so silence can never be read as success.

**Every grep stop-rule now states its expected hits explicitly** and carries the escape clause (comment-only surprises acceptable, code surprises are a stop).

### The 12 fixes

1. **B1 — `sp-offgate-exec` was referenced 82 times and no step created it.** An Orientation step now creates it with `git worktree add … -b sp-offgate-exec main`, plus a clean/parity check. The plan does **not** retarget to the existing `sp-offgate` worktree: verified that branch is at `a6321f31` and its tree differs from `main` across 22 files / ~2000 lines of long-haul-arb, container-retention and manufacturing work that `main` has and it does not. The inverted last sentence of revision-1 item 11 ("the old `sp-offgate` path does not exist") is deleted — it does exist, which is exactly why the warning is needed.
2. **B2 — Task 3's `main.go` step order deleted live code.** With `1415-1418 → 385-393 → 331-334` applied first, base `:1185` had shifted to `:1172`, so the "1172-1192" cut removed `reachableYardFinder` (base `:1194`) and `heavyTargetFinder` (base `:1201-1206`), both still consumed at the handler call at base `:1211`/`:1213`. Reproduced arithmetically against the real file. Fixed structurally: the FIND block is anchored top and bottom on lines that SURVIVE (`explorerOffGateBridge := …` and `reachableYardFinder := …`), both written back. The block also now starts at the blank separator (base `1171-1192`, not `1172-1192`).
3. **B3 — `fleet_autosizer_heavy_cap_test.go` "replace 55-64" stopped one line short.** Confirmed `:65` is the `\t\t}` closing `if class == HullClassExplorer {`; a 55-64 replacement strands it. The brace is now inside the quoted FIND block. *(This file was ADDED by revision 1 — the fix reintroduced the very class it was fixing.)*
4. **M1 — Task 3's level-3b grep still collided.** Reproduced: the anchored pattern matches `parkedsensing.OffGateTarget` at nine adapter sites (`off_gate_target.go:59,62,66,72,75,88,209`; `explorer_dispatch_adapter.go:66,113`). The grep is now scoped to `internal/application/`, with the nine excluded sites named and explained.
5. **M2 — Task 3's `grep parkedsensing main.go` "expect exit=1" was wrong.** Reproduced exit 0: `:17` is the `parkedSensingAdapters` import path (`internal/adapters/parkedsensing`) and `:1382` an unrelated comment. The check is now `grep -n 'parkedsensing\.' …` — the identifier test the prose always claimed — with an explicit sentinel on the empty case, and the trap is documented in the Orientation table.
6. **M3 — Task 3's `off_gate_expansion` "expect exit=1" was wrong.** Reproduced: `probe_sensing_stall.go:29` (`expansionStallCoordinator = "off_gate_expansion"`) is LIVE and out of scope — it is the coordinator label the operator's dashboards key on. The step now expects exactly that one surviving hit, requires `off_gate_no_target` to be at zero, and the commit message says so.
7. **M4 — Task 4's grep expected hits only in `offgate_types.go`.** Reproduced the outlier: `off_gate_target.go:45`, a comment claiming to implement `commands.OffGateTargetSelector` by shape only. It is now listed as expected, with the note that the file dies in Task 5.
8. **M5 — Task 7's `fleet_autosizer_guards.go` steps ran TOP-DOWN (34 → 53-54 → 247)**, corrupting live comment bullets in the process. Verified all three sites; each is now an independent content-addressed edit whose FIND quotes the surviving neighbouring bullets, so order is irrelevant and the bullet list cannot be corrupted.
9. **M6 — Task 9's sweep returned 50 hits, 22 of them live code in three warp test files.** Reproduced exactly (12 in `route_executor_warp_test.go`, 8 in `route_executor_warp_persistence_test.go`, 2 in `commands/navigation/warp_ship_test.go`). The sweep is now anchored to comment lines (`grep -rnE '^[[:space:]]*//' … | grep -Ei …`), which returns 16 lines at base and exactly the 10 relevant ones by Task 9. All three files are declared **PROTECTED live fixtures** in the Orientation and in Task 9 — never renamed, never edited, never swept.
10. **M7 — Task 10's comment ratchet was already RED on the untouched tree.** Reproduced all five packages the report named, **plus a sixth it missed**: `internal/application/scouting/commands` (36.50% > 36.50%, a rounding-boundary regression). An Orientation step now captures every touched package's ratio on the pre-Task-1 tree via `make comment-audit`, and Task 10's gate is a `join`+`awk` comparison against that capture — "no package regresses relative to the capture" — with a row-count assertion so a package cannot drop out of the comparison unnoticed. `make comment-audit-check` is documented as not-a-gate, with the reason.
11. **M8 — Task 10's prose sweep returned 819 hits at base.** Reproduced. It is now bounded to the ten packages this plan touches plus `internal/adapters/metrics`, explicitly excluding `pkg/proto/` (generated `daemon.pb.go`, `daemon_grpc.pb.go`) and `internal/domain/` (live `ship_specs.go`, `ship_module.go`, `shipyard/inventory.go`), and four classes of legitimate surviving hit are enumerated.
12. **Ride-alongs.** The stale `173-253` in self-review item (c) is gone (that item now describes the anchor-based addressing). `probe_sensing_heartbeat.go` and `probe_sensing_config.go` are added to Task 7's Files block, where their edits already lived. Task 6's `main.go` positions are recorded as **post-Task-3** estimates (`~:1149-1153` for the bridge block, `~:1172` for the handler argument) with the arithmetic shown and marked "may be off by one" — the two numbers in the revision brief (`1149-1153` and `1173`) are mutually inconsistent by one line, since they assume different treatments of the `sensingWiring` struct's blank separator, and under content addressing neither is load-bearing.

### Found during revision 2, not in the verification report

- **A sixth package is already RED on the comment ratchet** — `internal/application/scouting/commands`. The report named five.
- **Task 3's proposed replacement for `expansion.go:216` was circular.** It would have produced "…the pass does nothing and the rest of the tick is **unaffected: the pass does nothing and the rest of the tick still runs.**" — the preceding line already carries that clause. The replacement is now simply `// unaffected.`, and the two edits the old plan made there (delete the field, then fix the cross-reference) are merged into one FIND/REPLACE.
- **`sensing_engine_ports.go` is the one place `gofmt -w` is EXPECTED to rewrite.** Removing the `OffGate` field and its comment merges two struct-field alignment groups, so gofmt re-pads `GateRead`/`Uncharted`/`SeedShip`/`Scan`/`SpreadOf`. Every other `gofmt` rewrite in this plan is a defect signal; this one is not, and the step now says so and shows how to verify it with `git diff`.
- **`client_universe.go`'s `ListSystems` has no unique tail anchor** — its last six lines are byte-identical to `ListWaypoints`' immediately above it. Task 8 addresses the end of that deletion by the FIRST SURVIVING LINE (`// GetConstruction …`) instead, with the reason stated.
- **Task 4's and Task 5's "expect errors only in X" steps could pass on an empty result**, which would mean the deletions never happened. Both now state that an empty result is a failure.
- **`grep -c` returning 0 exits non-zero**, which under `set -euo pipefail` aborts the step. That is deliberate and is now documented: a missing anchor fails loudly instead of printing `0` and continuing.
- **One FIND block is NOT unique across the tree** — `offgate.go`'s import block is byte-identical to `counterstaff.go`'s in the same package. The step now says so explicitly and forbids a tree-wide replace. It is the only such block in the plan; every other FIND matches exactly one file.
- **`cmd/spacetraders-daemon/main.go:1120-1121` is inside a function body and carries a leading TAB**, unlike every other comment Task 9 edits (all of which are package-level doc comments with no indent). A first pass at this revision quoted it without the tab; the verification harness below caught it. The FIND/REPLACE now carry the tab and the step says why.

### How this revision was verified

Because the quoted text is now the contract, it was checked mechanically rather than by eye:

1. **All 81 `grep -c` uniqueness anchors were extracted from this document and run against the real tree.** Every one returns exactly `1`.
2. **All 76 FIND / HEAD-ANCHOR / TAIL-ANCHOR / FIRST-SURVIVING-LINE blocks were extracted, had their two-space markdown scaffolding stripped, and were matched byte-for-byte against every `.go` file under `gobot/`.** Every block is present. The two flagged results are both understood: the `import (` block above (two files, per-file edit), and Task 6's `main.go` FIND, which quotes the comment **Task 3 writes** and therefore cannot exist on the base tree — that one was verified separately by confirming Task 6's FIND begins with Task 3's REPLACE **byte-for-byte**, so the hand-off between the two tasks cannot drift.
3. **Structural gates:** 125 `bash` fences, 125 `set -euo pipefail` openers, zero unmatched; zero edit steps addressed only by line number; zero `gofmt -l` used as a gate; zero `git commit -am` with a pathspec.

Deliberately **unchanged**, because two rounds of verification confirmed them: the deletion ORDER, the deadness arguments, the money-safety reasoning, the task structure, and the "prove the bridge retracts before deleting anything" ordering.
