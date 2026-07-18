# Lane report: app-ops (RPP L1-L3)

**Lane key:** app-ops
**Scope (recursive, edit-permitted):** `internal/application/{auth,bootstrap,capacity,common,expansion,gas,health,liveconfig,logging,mediator,player,scouting,setup,storage,system}`
**Range:** L1 Readability, L2 Complexity, L3 Responsibilities (within-package only)
**Baseline:** origin/main green — `go build ./...`, `go vet`, and `go test -race -count=1` over all 15 packages pass with 0 failures before and after.

## Executive summary

This lane is, with one small exception, **already at or above the L1-L3 bar**. Every
package was read or structurally surveyed. The code shows extensive evidence of prior
refactoring passes: magic numbers already extracted into documented named constants,
long coordinators already decomposed into many small well-named methods, guard clauses
used pervasively, and a strong load-bearing WHY/bead comment culture. The disciplined
outcome for a behavior-preserving sweep on a live money-earning system is therefore a
**single safe L1 win** plus an honest catalogue of what was examined and the genuinely
architectural (L4+) items deferred as candidates. No L2 or L3 change was found that is
simultaneously behavior-preserving-with-high-confidence, genuinely clarifying, and worth
the risk; forcing one would have been churn, which the sweep brief explicitly warns against.

## Smells found and changes applied, per level

### L1 Readability

**Found & fixed — builtin shadowing / sibling inconsistency (bootstrap):**
- `run_bootstrap_reconcile.go` `maybeBuyProbe` declared a local `cap` (`cap := int64(...)`),
  shadowing the Go `cap` builtin, while its two sibling capital-gate functions
  (`maybeBuyHauler`, `maybeBuyGateWorker`) already used `capBudget`. Renamed the local to
  `capBudget` so all three capital-gate paths read identically.
- `run_bootstrap_income.go` `firstUnservedHub(..., cap int)` had a parameter `cap` shadowing
  the builtin. Renamed to `hubCap`.
- Behavior-preserving: log-message text and the `"cap"` structured-log map key were left
  unchanged (ops greps them); only local/parameter identifiers changed.
- Commit: `refactor(L1): app-ops — rename cap builtin-shadow to capBudget/hubCap [sp-1z4q]`

**Surveyed, nothing to change:**
- Repeated domain string literals (`GAS_GIANT`, `MARKETPLACE`, `SHIPYARD`, `JUMP_GATE`,
  `UNCHARTED`, `HYDROCARBON`, `LIQUID_*`): grep found **no** in-file repetition — all already
  const-extracted or single-use.
- Dead unexported top-level functions: a repo-wide reference scan of all 205 unexported
  package-level funcs in scope found **zero** with <2 references (no dead code).
- Magic numbers: inline multi-digit / time literals are already named constants with
  rationale comments (e.g. `scanIntervalFloor/Cap`, `defaultTourStartJitterMax`,
  `backfillDefaultTickSeconds`, `defaultDiscoveryShare`, the scout-post `default*` block).
- Redundant WHAT-comments: the comment culture here is overwhelmingly WHY/bead-referenced
  and load-bearing. The only borderline WHAT-comments are short step/section labels in
  multi-step methods (e.g. `// Remove from ships map`); these aid navigation and match the
  house style, so removing them would be churn, not hygiene — left as-is per the brief.

### L2 Complexity

**Surveyed, nothing safe+valuable to change.** The longest functions
(`ensurePartitions` 207, `reconcileOnce` 183, `executeSiphoning` 169, `resolveConfig` 169,
gas `Handle` 166, `decideAndMaybeBuy` 163) are long because of (a) structured logging with
inline map literals and (b) cohesive linear workflows with clear inline step markers — not
deep nesting or tangled conditionals. They already read top-to-bottom as guarded, early-return
sequences. No self-contained block was found whose extraction would genuinely clarify without
introducing behavior risk. `resolveConfig`/`resolveSizerConfig`/`resolveBootstrapConfig` are
long only because they enumerate config keys 1:1 (each key is a load-bearing string that must
not move); that is the correct shape, not a smell.

### L3 Responsibilities (within-package)

**Surveyed, nothing safe+valuable to change.** The two candidate patterns were examined and
deliberately deferred (see below): the scout-post god-file split (large/risky) and the storage
ship-iteration micro-duplication (locking-sensitive, 3 lines). Neither meets the safe-win bar.

## Deferred within-scope items (L3, not applied — recorded per brief)

1. **`scouting/commands/run_scout_post_coordinator.go` god-file (3525 LOC).** By size this is a
   god-file, but it is *already* decomposed into ~90 small, well-named methods around one
   handler type; the smell is file length, not method design. A pure within-package file split
   (manning-stall / reposition / gate-chart-sweep / surplus-relay / drift+budget state /
   partitions) is mechanically safe (the gate would catch any dropped decl) but is a large
   ~3500-line move-diff on the lane's most actively-armed file (recent beads sp-u8jc, sp-6vep,
   freshsizer). Per "prefer many small safe wins over few risky large ones," deferred to a
   dedicated focused task rather than folded into a broad multi-package sweep.

2. **`storage` ship-iteration micro-duplication.** The 3-line
   `for _, symbol := range c.shipsByOperation[opID] { if ship := c.storageShips[symbol]; ship != nil {...} }`
   loop recurs in `coordinator.go` (GetTotalCargoAvailable, FindStorageShipWithSpace,
   ReserveSpaceForDeposit, GetStorageShipsForOperation) and `coordinator_basis.go`
   (operationHeldUnitsLocked). A shared helper is unsafe to extract cleanly because the public
   methods each acquire their own lock at different granularity (RLock vs Lock) while the
   `*Locked` variants assume the caller holds it — `sync.RWMutex` RLock is not reentrancy-safe,
   so a naive `shipsForOperation()` helper risks a deadlock. Low value (3 trivial lines) vs real
   risk; left as-is.

## L4-L6 candidates (architectural / cross-package — NOT edited)

1. **Remove the deprecated `common/compat.go` re-export shim (L4/L5).** `common/compat.go`
   self-documents as `DEPRECATED` — it re-exports mediator/auth/logging/player/ship-dto types
   and funcs "so existing code keeps working while we gradually migrate imports." **259 files**
   in `internal/` still reach these symbols through the shim. Completing the migration (repoint
   importers at the owning packages, then delete the shim) is a large, blast-radius-259
   dependency-direction cleanup — ideal for the Mikado method, wrong for an in-package sweep.
   Packages: `common` + ~all application/adapter importers.

2. **Extract a shared standing-coordinator loop harness (L5/L6).** `bootstrap`, `capacity`,
   `expansion`, and both `scouting` coordinators each hand-roll the same `Handle` skeleton:
   `errMon := health.NewMonitor(...)`; `for { select ctx.Done / default; seq++; reconcile;
   noteReconcile; sleepTick }`, plus optional-setter DI and a `liveConfigSnapshot` helper. The
   duplication is cross-package and structural. A shared runner (e.g. in `application/common`
   or a new `coordinatorloop` package) would dedupe the skeleton, but must be designed around
   the real per-coordinator variations (start jitter, dry-run, kill-switch, per-tick vs
   per-player state), so it is a genuine design task, not a mechanical move.

3. **Consolidate manual system-symbol extraction onto `shared.ExtractSystemSymbol` (L4, small).**
   `gas/commands/run_gas_coordinator.go` (`autoSelectGasGiant`, `planDryRunRoutes`) re-derives a
   system symbol by hand (`strings.Split(sym,"-")` then `parts[0]+"-"+parts[1]`) while the domain
   helper `shared.ExtractSystemSymbol` is used for the same purpose elsewhere (scout_markets,
   system queries). Consolidating removes duplication and a subtle behavior fork (the manual
   version errors on malformed input; the helper does not), but *because* of that error-shape
   difference it is not a pure behavior-preserving swap and was left for a deliberate change.

## Suspected issues (report-only — non-runtime, no behavior fix attempted)

- **gas `run_gas_coordinator.go` duplicate step label:** the spawn-siphon block and the main
  monitoring block are both commented `// Step 5:` (the monitoring loop should be Step 6).
  Documentation inconsistency only; no runtime effect. Left untouched to avoid comment churn.
- **gas `run_siphon_worker.go` stale line-ref comment:** a comment cites "(lines 218-235)" for
  the cooldown-retry logic, which now sits ~216-234. Line-number references in comments are an
  anti-pattern and drift; left untouched (rewording risks the strong-comment-culture guardrail).
- No genuine runtime bugs were found in scope. `player/commands/register_player.go`
  `SyncPlayerHandler` sets `updated = true` unconditionally after the credits check, which makes
  the earlier `if player.Credits != agentData.Credits { updated = true }` flag write moot — but
  this is intentional (metadata `last_synced` changes every sync), not a bug.

## Package-by-package verdict

| Package | Verdict |
|---|---|
| auth | pristine |
| bootstrap | pristine except the L1 `cap` shadow (fixed) |
| capacity | pristine (model coordinator: tiered dispatch, DryRun twin, invariant backstops) |
| common | pristine; carries the deprecated `compat.go` shim (L4 candidate) |
| expansion | pristine (breadth/depth policy fully decomposed, pure helpers) |
| gas | clean (spawnWorker/spec dedup already done); minor doc notes above |
| health | pristine (streak/effect trackers, error-loop emitter) |
| liveconfig | pristine |
| logging | pristine |
| mediator | pristine |
| player | pristine |
| scouting | clean; the 3525-LOC scout-post coordinator is a size-only god-file (deferred L3) |
| setup | pristine |
| storage | pristine; 3-line ship-iteration micro-dup (deferred L3) |
| system | pristine (gategraph BFS + backoff is exemplary) |

**Gate (final):** `go build ./...`, `go vet`, `go test -race -count=1` over all 15 packages — all pass.
