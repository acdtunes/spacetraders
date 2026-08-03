# Phase 2 design notes — salvaged, with adjudications

**Status:** NOT a plan. This is the surviving design output from a drafting pass whose plan file
was never written (the agent composed the document in its response, hit the output limit, and lost
tasks 1-7). The task list, the open decisions, and the spec objections below are real work product
and should seed the next drafting attempt rather than being re-derived.

**The process lesson, for whoever drafts next:** write each task to the file as it is finished.
A plan composed entirely in a response is one output limit away from total loss.

## Task list (9 tasks, one line each)

1. `gate.Role` vocabulary — delivery/factory, fleet tags, `IsGateFleetTag`, and the pure D/F/F/D
   purchase order derived from live counts.
2. `gate.BuyPolicy` — supply-anchored buy/pause with hysteresis, the `Decision` observability
   record, and `FleetPaused` (**every** material, not any).
3. `gate.PlanFill` — greedy max-cargo mixed fill, `trade_volume` tranching, per-material skip
   reasons, trip log line.
4. Persist `delivery_buy_floor` / `delivery_resume_floor` on the pipeline row + migration `051` +
   the restart-survival round-trip test.
5. `--buy-floor` / `--resume-floor` on `construction override` — new proto RPC, daemon
   single-writer, CLI flags and validation.
6. `ProductionExecutor.BuyAtTerminalFactory` — pinned-source buy at the exporter, every money
   guard called unchanged.
7. Tag the 4 GATE hulls by role at purchase; widen the observer and the surplus/trade re-tag
   guards so role-tagged hulls are not orphaned.
8. Role-aware drain: discover every gate tag, claim under the hull's own tag, and run the
   delivery leg (decide → record → fill → buy → deliver).
9. Era-invariance source guards over both new surfaces + full gofmt/build/vet/test/-race sweep.

## The single most valuable finding from the drafting pass

**Claim identity is load-bearing.** `ClaimShip` authorizes a dedicated hull only when
`tag == operation`. So a role-tagged hull claimed under the drain's *default* identity is rejected
at the DB and silently never works. Discovery must query every gate fleet tag, and each hull must
be claimed under **its own** tag. The allowlist is deliberate: an undedicated hull still claims
under the drain's identity, and a foreign-pinned hull is never claimed under its own tag — which
would otherwise defeat the no-poach guard entirely.

This is the kind of defect that ships green and does nothing. Task 8 carries a mutation probe for
it; keep that probe.

## Adjudications on the six open decisions (orchestrator, 2026-08-03)

| # | Decision | Ruling |
|---|---|---|
| 1 | Skip-reason precedence: `hold_full → bill_satisfied → paused → no_supply` | **ACCEPT.** The first two are facts independent of policy; calling a met bill "paused" would send an operator to tune a knob that changes nothing. `hold_full` is a real outcome the greedy loop produces that none of the spec's three named reasons describes honestly. |
| 2 | Pause state in-memory per process, not persisted | **ACCEPT.** Follows the worker-registry precedent. A restart re-derives: an unpaused start re-pauses on the first low read, costing one tick and never a spend. Persisting adds a write to the hot path and buys nothing. |
| 3 | `available_supply` derived as `trade_volume × gateMaxTranchesPerStop(4)` | **ACCEPT, flagged.** The drafter called this its weakest inference and it is. But it is forced: an independent DB probe confirmed the market exposes a supply LEVEL and a per-transaction `trade_volume` and **no stock count**. The supply level still gates *whether* we buy, via the policy; this only bounds how much one stop can lift so it cannot monopolise a mixed trip. Revisit against live fill data once phase 2 runs. |
| 4 | Legacy `manufacturing`-tagged hulls carry no role; leave them on the existing path | **ACCEPT.** Re-roling live hulls is phase 3's reallocation. Watch: until phase 3, a fleet with 4 legacy gate hulls ramps past `gateWorkerTarget` only if the observer's total is wrong — it is not (all three tags increment `GateWorkers`), so the ramp still stops at 4. |
| 5 | A separate RPC behind the same `construction override` verb | **ACCEPT.** `ConstructionGoodOverride` requires `good`, which pipeline-wide floors do not have. One verb, two RPCs, dispatched on which flags were set. |
| 6 | The `SetGateDelivery` wiring call site was located by grep but not read | **ACCEPT as a task instruction.** Task 8 must READ the surrounding builder before adding the line, not pattern-match. |

## Spec objections — both upheld

**1. The pattern-B liveness test as specified is necessary but NOT sufficient.** Upheld, and it
matters. The spec pins the hazard with a test that the knob survives a daemon restart. But a
pattern-B regression would not reintroduce a config key in the same commit — it would arrive later
as someone "tidying" the floors into `config.yaml` alongside the other manufacturing knobs, at
which point the round-trip test still passes (the domain object still round-trips) while the live
value is clobbered on the next boot.

The drafter declined to write a guard it could not make real, and said so, which is the correct
call. **Filed as a bead.** The best available mitigation for the plan: assert the floors are read
off the pipeline row **on every leg** rather than cached at construction — that per-tick read is
what makes the knob live, and a test that pins it will fail if someone hoists the read.

**2. `capacity_left == 0` should be `<= 0`.** Upheld, trivial. The greedy take can leave a small
positive remainder no material can fill; breaking only on exactly 0 walks the rest of the list
appending skips. Correct behaviour, noisier log. The spec's Batching pseudocode should say `<= 0`.

## Operator note that must survive to deploy

Migration `051` must be applied to the production database **before** the daemon that writes those
columns is deployed. Boot `AutoMigrate` would add them, but AutoMigrate failure is **non-fatal**,
so a boot where it could not run would leave the floor writes hitting SQLSTATE 42703.

## Constraints unchanged from phase 1

No feature flag / default-off / arm seam — ships ARMED. Money guards untouched (50k
`common.ImmutableReserveFloor`, RULINGS #4). Zero waypoint-symbol literals in production code;
goods names are invariant and fine. Protected paths: `gobot/internal/captain/**`,
`cmd/captain-gate/**`, `city/agents/**`. Filter all test output (~4550 tests / ~107 packages);
shell is zsh, so no `PIPESTATUS`.

**Do NOT use `GateTopology.IsRaw` or `Inputs`** — bead `sp-4irrr` (P1): the recipe map is cyclic
and `!hasRecipe` returns false for every real raw material. Phase 2 has no reason to touch the
recipe graph.
