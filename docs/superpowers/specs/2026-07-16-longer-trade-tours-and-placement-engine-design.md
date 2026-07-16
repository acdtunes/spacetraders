# Longer Trade Tours + OR-Tools Placement/Relocation Engine — Design Spec

- **Date:** 2026-07-16
- **Status:** Approved (design), pending build
- **Author:** economy-analyst (brainstormed with the Admiral)
- **Origin:** Admiral consult — "run longer trade routes spanning ~10 systems." Analysis reframed this: the live arb solver is already multi-hop but clamped to 2 systems; the win is longer *impact-aware* tours plus an explicit **placement/relocation** loop, with tour **closure** as an opt-in mode.
- **Build owner:** shipwright (economy-analyst is read-only; this spec + the linked epic are the handoff).

## Context — what exists today (verified 2026-07-16)

- Live arb runs on the **tour coordinator** (`run_tour_coordinator.go:1631` → `RoutingClient.OptimizeTradeTour` → Python `utils/tour_solver.py`). Confirmed live via populated `tour_leg_telemetry` + `transactions.operation_type='tour'`. The Go one-hop `scanLanes` path (`run_trade_route_coordinator_*`) is secondary/dormant.
- `tour_solver.py` is **already multi-hop**: beam search (`BEAM_WIDTH=50`) over hop sequences, the sp-tl68 **impact model** (fitted buy/sell tranche curves) baked into `score_sequence`, a fleet **absorption ledger** (sp-78ai) that nets outstanding depth, deposit/stock legs, and a **switchable $/hr objective** (`TOUR_SOLVER_OBJECTIVE`, sp-1wp8 — env flip, no proto change).
- It is clamped by **`MAX_TOUR_SYSTEMS = 2`** (a hard module constant; *"Admiral revision 2026-07-09: start + 1 gate neighbor"*), even though `MAX_HOPS_DEFAULT = 6` — the solver's design envelope is 6 hops.
- **OR-Tools is already in the service:** `ortools==9.15.6755` pinned; `utils/routing_engine.py` uses the `pywrapcp` routing library (RoutingModel/RoutingIndexManager) at 3 solve sites for the nav/fueled tours. CP-SAT (`ortools.sat`) and GLOP are available in the venv. The trade tour is the *only* tour path not on OR-Tools.

**Empirical grounding (analyst, 2026-07-16, impact-adjusted):** in the live reachable graph (134 systems, 503 usable gate edges) only **105 edges are profitable**; $/leg *decays* with route length (1-hop 1.16M → 10-hop 428k) because fat legs are scarce (top-10 = 31% of profit). **547 profitable circuits ≤6 legs** exist; best is a 6-leg loop at **614k/leg**. Conclusion: the target is a **~5-6 system tour**, selected on **$/hr not total profit**, with closure available — *not* a 10-system chain.

## Objective

Raise sustained per-hull **$/hr** by (1) letting the impact-aware solver plan **longer tours** (≤5-6 systems) selected on a **rate** objective using **OR-Tools** for the sequencing core, with **open** (wander-outward) and **closed** (return-to-anchor circuit) modes; and (2) adding a **placement/relocation loop** that keeps each hull anchored where opportunity is richest, moving it when the local pocket is exhausted.

### Non-goals (v1)
- Contract/manufacturing fleet sizing (own coordinators; the Capacity Reconciler spec covers contract topology).
- Fleet-wide *joint* hull→anchor assignment (Tier-2 seam; v1 placement is per-hull greedy against the shared absorption ledger).
- Replacing the tranche/impact scorer or the absorption ledger — the OR-Tools work replaces the **beam sequencer**, not the pricing.
- Consolidating the dormant `scanLanes` path (separate cleanup).

## Locked decisions (with rationale)

| Axis | Decision | Why |
|------|----------|-----|
| Route length | Request-driven cap, target 5-6 systems; **default stays 2 until the replay gate passes** | Reverses the 2026-07-09 clamp *safely* — the safeguards that made the clamp prudent (absorption ledger, rate objective) have since landed; governed rollout, not a blind flip |
| Objective | **Rate** ($/hr) primary for longer tours | $/leg decays with length; profit-primary over-lengthens into thin filler legs. Rate + the built-in per-crossing time cost self-limits length |
| Sequencer | **OR-Tools** prize-collecting tour (routing library / CP-SAT) replaces the beam, behind the existing `Planner` seam | Beam's optimality gap widens with more systems + closure; OR-Tools already drives the sibling nav tours; open/closed is a *native* start/end-node toggle |
| Tour shape | **Open (default) + Closed (opt-in) as modes**; closure anchors on the ship's **current system** (floating), fixed-home optional | Open fits the frontier-expansion redeploy; closed gives saturation-resistant circuits for settled regions. Floating closure needs no home infra |
| Placement value | **Peak $/hr, fleet-relative**, behind a `PlacementValue` seam for **sustained** later | Ships fast + interpretable; sustained (earnings-until-saturation) is the correct-but-heavier Tier-2 |
| Relocation | Single **placement score** argmax over reachable systems (self-triggering); staleness gate + park floor | Unifies "when to leave" and "where to go"; guardrails prevent phantom-chasing and fuel-thrashing |

## Architecture — two layers

### Layer A — the tour solver (Python `tour_solver.py`)
Plans the best tour **anchorable at the ship's current system**. Changes:
1. **Request-drive the length cap.** Promote `MAX_TOUR_SYSTEMS` to a `TourConstraints` proto field (`max_tour_systems`); solver reads it, module constant becomes the default (kept at 2). Lets the Go caller and the harness sweep length without a redeploy.
2. **OR-Tools prize-collecting sequencer** (the core, replaces `beam_sequences`) — see the OR-Tools section. Emitted behind the existing `Planner`-style seam so beam stays as a fallback/reference.
3. **Closure mode.** A `closed: bool` (+ optional `anchor_system`) in the request. Open = free end node; Closed = end node pinned to the anchor (default = start/current system). Native to the routing library's start/end config.
4. **Rate objective** as the default for longer tours (`TOUR_SOLVER_OBJECTIVE=rate`) — already switchable; this epic makes it the armed default *after* replay validation.
5. **Wider candidate set.** The Go caller passes 2-3 gate-hops of candidate systems (today: 1), or the solver has nothing to sequence.

### Layer B — the placement/relocation loop (Go, `run_tour_coordinator.go` + reposition)
Owns "is the best local tour good enough; if not, where does the hull go." Evolves the existing `maybeReposition` (today: static ≥25k margin, ≤12 hops) into a **fleet-relative $/hr placement engine**. Structurally the same **SENSE → PLAN → DIFF** shape as the Capacity Reconciler; build on that seam so they share machinery.

## The placement/relocation metric (precise)

For any system `x` reachable from the hull's current position `s` (including `x = s`):

```
score(x) = E_x − β · D_x                      # Tier-0 (peak, linear deadhead charge)
```

The hull goes to (or stays at) **`argmax_x score(x)`**. Relocation is emergent — you leave `s` only when another system beats staying net of the move. Terms:

| Term | Meaning | Source |
|------|---------|--------|
| `E_x` | best projected tour $/hr anchorable at `x`, absorption-adjusted | solver `projected_credits_per_hour`; **profitable-edge-graph bound** to shortlist, full-solve top-N only |
| `D_x` | deadhead hours to reach `x` (`D_s = 0`) | nav/jump model (`jumpPath`, reposition bound) |
| `β` | deadhead charge = fleet rolling-median tour $/hr (trailing 1h) | `tour_leg_telemetry` |

**Guardrails (both required):**
- **Staleness gate:** drop candidates whose market snapshot age > `MAX_SNAPSHOT_AGE` (reuse the solver's 75 min) — don't deadhead to a mirage. Side-benefit: fresh-scan value → exploration pressure toward the frontier.
- **Park floor:** if `max_x score(x) < φ` (default `φ = 0.3 × fleet-median`), hold position — don't fuel-thrash a globally saturated fleet.

**Seam:** `PlacementValue` computes `E_x` as **peak** now; Tier-2 swaps in **sustained** — `E_x·H → ∫₀ᴴ rate_x(t)·dt` with residency `H_x` estimated from absorption depth at `x`, so deep circuits outrank shallow spikes. Same argmax skeleton.

## OR-Tools usage (explicit — Admiral requirement)

The **sequencing core** is formulated as a **prize-collecting TSP / orienteering** on the candidate systems' market waypoints, solved with OR-Tools — the same `pywrapcp` routing library already running `routing_engine.py`:

- **Optional nodes (prize-collecting):** you need not visit every market — `routing.AddDisjunction([node], penalty)` with `penalty` = the profit forgone by skipping it. This is the native OR-Tools idiom for "collect the valuable stops, skip the rest."
- **Open vs closed = start/end node config:** `RoutingIndexManager(n, 1, start, end)` — `end = start` (or `= anchor`) yields a **closed** circuit; a free/dummy end node yields an **open** path. Closure is a *native toggle*, not custom control flow.
- **Objective = profit − time cost:** arc costs carry `INTER_SYSTEM_TRAVEL_SECONDS`; the routing objective maximizes collected prize net of travel, matching the rate objective. Length self-limits (each crossing is priced).
- **Two-stage split (recommended, lower-risk):** OR-Tools chooses the **sequence** (node prizes = an optimistic multi-good packing bound, mirroring the beam's current bound); the existing greedy/impact **tranche scorer** prices the chosen sequence exactly. OR-Tools replaces **stage 1 (beam)**, keeps **stage 2 (pricing)**. Falls back to the beam behind the `Planner` seam.

**Latency & node sizing (the one knob that decides speed).** The routing runs `GUIDED_LOCAL_SEARCH` with a time-limited *anytime* search — the exact metaheuristic `routing_engine.py` already runs live in this service, so this is a reused proven-fast path, not a new unknown. Speed is governed by node count. Markets average **12/system** (p90 21, max 35), so the model **must prune to markets with profitable pairings** among a **bounded candidate-system shortlist** *before* building it: naive (all 3-gate-hop systems × 12 markets) ≈ 150-200 nodes; pruned ≈ **30-80 nodes → near-optimal sub-second**. Profitability is sparse (105/503 edges) so most markets drop out anyway. Set the anytime `time_limit` **low (2-5s, not the 60s ceiling)** and take best-so-far; beam-fallback on timeout. Keep impact/tranche pricing **out of the routing objective** (precomputed prize bounds — the two-stage split above) so each local-search move stays O(1). Longer tours also *reduce* replan frequency (~every 30-50 min vs 10), so solve count drops fleet-wide. sp-f1yk benchmarks cap-5/6 vs cap-2 before arming.

**Seams for the harder combinatorics (later, not v1):**
- **GLOP (LP):** if longer tours + **carry-through** goods (a good held across several legs) break the greedy tranche independence, the multi-leg hold-packing becomes a resource-constrained allocation → GLOP behind the tranche-scorer signature (the docstring already anticipates this).
- **CP-SAT:** Tier-2 **fleet-wide** hull→anchor assignment (maximize summed placement score under mutual-saturation) is a genuine assignment problem → CP-SAT, replacing per-hull greedy placement.

## Risks & governance

- **Solve latency.** OR-Tools prize-collecting over 5-6 systems × market waypoints against the `routing.timeout.tsp = 60s` budget. If solves slow, the fleet's replan cadence drops — which can cost more $/hr than longer routes gain. **Benchmark cap 5-6 vs 2 before arming; cap the OR-Tools search time and fall back to beam on timeout.**
- **Staleness.** A committed 5-6 leg tour runs ~30-50 min; tail legs execute against an aging snapshot. Partly bound by the absorption ledger; monitor realized-vs-projected on tail legs.
- **Concentration externality** (the original reason for the clamp). Now bounded by the sp-78ai ledger + rate objective — **re-verify at the higher cap** in the harness.
- **Reverses the 2026-07-09 clamp.** Rollout is **governed**: cap becomes request-driven with default 2; experiment at 5-6 in the twin + `replay_objective.py`; arm in prod only after the **fleet-$/hr replay gate** shows a win.

## Verification (trust the harness > the code)

- **`replay_objective.py`** (exists): replay longer-tour + rate-objective on real history; the arming gate = clear fleet-$/hr win vs the cap-2 baseline.
- **Twin/bootstrap harness:** seed a multi-system market scenario; assert (a) OR-Tools sequencing matches a brute-force optimum on small instances, (b) closed mode returns to anchor, (c) placement loop relocates on an exhausted pocket and parks on a globally-saturated one, (d) **solve-latency** stays within budget at cap 5-6.
- **No-regression:** at `max_tour_systems=2, objective=profit` the solver is byte-for-byte the current behavior (default-safe).

## Build decomposition (→ shipwright epic)

1. **Request-drive the cap** — `max_tour_systems` proto field + solver reads it (default 2). Foundation; default-safe.
2. **OR-Tools prize-collecting sequencer** — routing-library formulation (disjunction prizes, arc time costs), behind the `Planner` seam, beam as fallback. Two-stage: OR-Tools sequence → existing tranche scorer.
3. **Closure mode** — `closed`/`anchor_system` request fields → start/end node config; floating anchor = current system.
4. **Rate-objective arming** — make rate the default for longer tours, gated by `replay_objective.py`.
5. **Wider candidate set** — Go caller passes 2-3 gate-hops of allowed systems/waypoints.
6. **Placement/relocation loop** — `score(x)=E_x−β·D_x`, `PlacementValue` seam, staleness gate, park floor; evolve `maybeReposition`. Candidate shortlist via the profitable-edge graph, full-solve top-N.
7. **Harness + replay verification** — OR-Tools-vs-brute-force optimality, closed-returns-to-anchor, relocate/park behaviors, solve-latency benchmark, fleet-$/hr replay gate.
- *Future (seams):* GLOP carry-through tranche allocation; CP-SAT fleet-wide assignment; sustained `PlacementValue`.

## Calibration params (config, not code)
`max_tour_systems` (default 2 → 5-6 post-gate), `TOUR_SOLVER_OBJECTIVE` (profit → rate post-gate), `closed` default, deadhead charge `β` (= fleet-median), fleet-median window (1h), staleness gate (75 min), park floor `φ` (0.3×median), candidate jump-bound + shortlist top-N, OR-Tools search-time cap + beam-fallback threshold.
