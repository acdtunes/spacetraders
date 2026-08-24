# Contract Sourcing Hull Selection: Route-ETA Ranking

**Date:** 2026-08-23
**Status:** Approved (Admiral, this date)
**Objective:** Contract cycle-time. PLAYBOOK §3: "contract $/hr is won on CYCLE-TIME."
**Origin:** Era torwind-2026-08-23 live observation, after three same-shape dispatch patches in one
night (sp-5koz4 cooldown-gated readmission, sp-94quv position-as-ownership placement, sp-66zwd
arbitrary tie order). PLAYBOOK §10: three bugs sharing a root cause means fix the structure.

## Problem

`SelectHullForCargo` (internal/domain/contract/hull_for_cargo.go) ranks contract sourcing
candidates by `travelTime = FlightModeCruise.TravelTime(straightLineDistance, engineSpeed)` —
a single-hop, refuel-blind, Euclidean estimate. Reality on the live fleet: a "715-unit" leg ran
5 route segments with 3 refuel stops and took 11m27s. The estimator cannot see hop structure,
refuel stops, or flight modes, so candidate order can invert against real arrival times —
directly costing cycle time on every mis-ranked dispatch.

Additionally, `shouldSkipShipInTransit` (internal/domain/contract/ship_selector.go) excludes
every IN_TRANSIT hull outright. A hull 30 seconds from arriving beside the source market loses
to an idle hull minutes away, or the selection fails over to a worse candidate entirely.

The daemon already computes real fuel-aware routes — `PlanRoute` on the OR-Tools routing
service (pkg/proto/routing/routing.proto), called through `application/ship.RoutePlanner` —
but only AFTER selection, to fly the chosen hull. The selection itself never asks.

## Design

Rank candidates on real route ETAs obtained from the existing planner, at selection time, in
parallel, under a time budget, failing open to today's ranking. Include unclaimed IN_TRANSIT
hulls with arrival-adjusted ETAs.

### Non-goals

- No new OR-Tools model, no new proto surface, no routing-service changes. `PlanRoute` is
  reused as-is (stateless local Dijkstra; zero SpaceTraders API budget — PLAYBOOK §6 wall
  untouched).
- No multi-job assignment optimization. The contract engine is serial (one contract at a
  time); there is nothing to co-optimize against. If contract work ever parallelizes,
  revisit with PartitionFleet.
- No change to Tier 2/4 command-frigate last-resort semantics (RULINGS #7).

### Layering

- **Domain** (`hull_for_cargo.go`): stays pure. `hullFit.travelTime` becomes a SUPPLIED
  value (seconds) instead of being computed from `DistanceTo`. Ranking ladder, tiers, and
  tiebreaks are otherwise unchanged. Unit-testable with plain numbers, no gRPC.
- **Application** (`ship_selector.go`): already fetches the system graph
  (`graphProvider.GetGraph`) and resolves the fleet. Gains the ETA phase: one
  `RoutePlanner.PlanRoute` call per candidate, issued in parallel, budget 2s total,
  results folded into the hullFit inputs.

### ETA definition

| Hull state | ETA |
|---|---|
| Idle / docked / in-orbit | `PlanRoute(current position → source).total_time_seconds` |
| IN_TRANSIT (unclaimed) | `(arrivalTime − now)` + `PlanRoute(destination → source).total_time_seconds` |

Domain invariant already held by this codebase (twin-fidelity rule): an IN_TRANSIT ship's
`CurrentLocation()` IS its destination, and `Ship.arrivalTime` is populated while in transit.
So the in-transit case needs no new state — `PlanRoute` from `CurrentLocation()` already
plans from the arrival point; the selector adds the remaining transit time.

### Candidate eligibility change

`shouldSkipShipInTransit` is replaced: an IN_TRANSIT hull is eligible iff it is UNCLAIMED
(not assigned to any container). A hull owned by another container stays excluded —
one container per hull (RULINGS #3, #7). The practically-affected population is hulls
mid-HOMING (fire-and-forget relocation, no claim), which is exactly the movement worth
interrupting for a paying contract.

### Ranking

Tier 1 orders by supplied ETA; capacity remains the first tiebreak; the sp-66zwd
home-slot-displacement tiebreak remains as the final tiebreak (real ETAs rarely tie exactly,
so it becomes a rare-path guard rather than the primary mechanism). Tiers 2–4 unchanged.

### Fuel approximation (declared, fails safe)

An IN_TRANSIT hull's `Fuel().Current` is last-known, not post-arrival. The ETA call passes a
conservative fuel figure (current minus the in-flight leg's remaining burn, floored at 0), so
error inserts EXTRA refuel stops and returns a PESSIMISTIC ETA. Wrong only in the direction
that under-favors the in-transit hull — never fabricates an optimistic win.

**Correction (2026-08-23, final-review follow-up):** the adjustment above is deliberately NOT
implemented, and the code is right to skip it. `ship.StartTransit` deducts the leg's fuel burn at
DEPARTURE (`internal/adapters/api/ship_repository_actions.go`), so an in-transit hull's
`Fuel().Current` already IS its post-arrival fuel — subtracting "remaining burn" again would
double-deduct and manufacture spurious refuel stops. `route_eta.go` passes `Fuel().Current`
unmodified to `PlanRoute`; this is correct as shipped. Do not "fix" it back to the rule above.

## Error handling — fail OPEN (RULINGS #1)

This ranking spends no credits; it is not a money guard. It must never block a dispatch.

| Failure | Response |
|---|---|
| `success: false` for ONE candidate | Drop that candidate (genuinely unroutable to the source) |
| `success: false` for ALL candidates | Fall back to straight-line ranking over all candidates |
| Routing transport error | Global fallback, all candidates |
| ETA phase exceeds 2s budget | Global fallback, all candidates |

Fallback is all-or-nothing by design: mixing real route ETAs with straight-line estimates in
one comparison lets a fictional number beat a real one. When candidates cannot all be priced
the same way, all are priced the old way.

Every fallback logs WARN with cause. The `Ship selection completed` log line gains
`ranking_mode: route_eta | fallback_straight_line` and per-candidate ETA seconds beside the
existing distance figure, so the decision basis is visible in the log line itself.

## Testing

**Domain (pure):**
- In-transit hull with smaller total ETA beats idle hull with larger ETA (core behavior).
- Nearer-by-ETA beats farther-by-ETA (precedence guard: ETA supply didn't reorder tiers).
- Exact ETA tie → capacity → home-slot displacement (sp-66zwd guard preserved).
- Candidates present ⇒ a hull is always returned (RULINGS #1, asserted directly).

**Application (fake RoutePlanner, building on `internal/adapters/routing/mock_routing_client.go`):**
- Happy path: ETAs used, `ranking_mode: route_eta` logged.
- One unroutable → dropped, remainder ranked on ETA.
- All unroutable → straight-line fallback + WARN.
- Transport error → global fallback + WARN.
- Hang past budget → global fallback, selection completes within wall-clock bound.
- ETA calls run in parallel (assert with a concurrency-recording fake).

**Live acceptance (PLAYBOOK §10 — merged is not proven):** on the running fleet, observe a
`Ship selection completed` line with `ranking_mode: route_eta` and non-zero ETAs, and at
least one dispatch that ranked an IN_TRANSIT hull. Verify at the effect point.

## Supersession note

sp-66zwd (tie → prefer displaced hull) remains merged and becomes the last-resort tiebreak
under this design. If sp-66zwd's implementation lands after this design, rebase its tiebreak
onto the ETA-supplied hullFit rather than the straight-line one.
