# Multi-Hop Trade-Tour Optimizer — Design (sp-1ek0)

**Date:** 2026-07-09 · **Status:** Approved by Admiral · **Epic:** sp-1ek0
**Designed:** Admiral × harbormaster (superpowers:brainstorming session)

## Problem

The trade engine executes single-lane circuits (buy A → sell B, repeat). A heavy freighter
(225–360 hold) flying one lane wastes hold on half-empty legs and dumps volume into single
sinks, laddering prices down (live evidence: D39 medicine bid 1,844→1,562 over 80u; K79
feeds −47%/churn; C37 SHIP_PARTS vol-6 cliff). A multi-hop tour — buy at hop, sell profitable
cargo at the next, rebuy, continue — fills the hold both directions and splits volume across
sinks. Estimated 2–3× per-hull trade income. Trade is the only unbounded income engine
(contracts capped by API 4511; factories capped by in-system sinks), so this serves the
100M-per-era push directly.

The differentiator (Admiral requirement): the optimizer must model **market depth and the
supply/demand curve** — a constant-price optimizer reproduces the −891k/−258k incident
class at machine speed.

## Decisions (locked)

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Execution model | **A→B**: captain-launched one-shot tours (`workflow tour-run`), graduate to autonomous circuit | Every prior autonomous money engine had a live incident on first contact (trade −3.5M, factories −938k, arb 3× overspend) |
| 2 | Phase-0 model ambition | **B**: full empirical fit from ledger + scout history before first flight | Admiral's call; the data exists and identification is clean (see Phase 0) |
| 3 | Graduation gate A→B | **Quantitative**: 10 completed one-shot tours with (i) zero guard violations, (ii) realized $/hr ≥ 1.5× the same hull's trailing single-lane $/hr, (iii) median plan-vs-realized price error ≤ ±15% | (iii) proves the model, not just profit — a profitable tour on a wrong model is luck |
| 4 | Solver | **Two-stage**: beam search over hop sequences (width ~50) + per-sequence tranche-LP | Milliseconds at our scale (~50–100 markets, ≤6 hops); each stage inspectable; explainable rejections. Monolithic CP-SAT deferred unless telemetry shows the beam leaving value on the table |
| 5 | Snapshot transport | Request-carried (Go assembles, Python stateless) | No second DB reader; testable; consistent with existing routing RPCs |

## Architecture

```
scouts → market cache (Go/postgres)
             │
   Go assembles snapshot ──► OptimizeTradeTour RPC (Python routing service, stateless)
             │                          │ beam + tranche-LP over fitted curves
             │                          ▼
   tour_run container ◄──────────── TourPlan (legs, tranches, projections, top-3 rejects, model version)
   (Go daemon, single-writer)
             │ per leg: travel() → dock → LIVE re-verify → execute → telemetry
             ▼
   ledger + planned-vs-realized telemetry → graduation report + model recalibration
```

- **Plan binds routing** (hop order, goods, tranche ceilings); **prices are re-verified live
  at every dock** (rqwm lesson: guards/plans must bind execution, but live reality outranks
  the plan on prices).
- **Fail open:** planner unreachable/timeout/infeasible → tour-run exits with a structured
  "tour unavailable" report; single-lane trade-route remains the fallback. Trading is never
  blocked on the planner.

## Phase 0 — Market model (empirical fit)

Python pipeline, colocated with the existing analysis idiom
(`gobot/analysis/market_dynamics_analysis.py` is prior art — 27.7h of market dynamics
already analyzed with pandas/numpy). Output consumed by the planner.

**Impact curves** — price as a function of cumulative units executed:
- Fit per (supply level, activity level) tier, piecewise-linear by tranche
  (units expressed relative to `tradeVolume`).
- Identification: our own ledger transaction *sequences* are the treatment events.
  The D39 ladder (4 consecutive 20u sells, bid 1,844→1,562) and K79 churn (−47%) are
  calibration series, not noise. Sample bias inverts here: self-generated impact events
  are exactly what identifies the curve.
- Sell side is concave (decreasing marginal revenue), buy side convex (increasing marginal
  cost) — piecewise-linearization keeps the downstream LP linear.

**Recovery** — drift back toward equilibrium:
- Half-life per activity tier, fitted from scout `MarketPriceHistory` time-series on
  markets we did **not** trade (the control set).

**Era handling:**
- Curve *shapes* per (supply, activity) are structural — they transfer across era resets.
- Equilibrium *levels* always come from the live scout snapshot, never from the artifact.
- Consequence: era reset requires fresh scans only, no refit-wait. The pipeline remains
  rerunnable (`make calibrate-market-model`) for periodic refinement from accumulated
  tour telemetry.

**Artifact:** versioned JSON (coefficients per tier + fit diagnostics + era stamp + git
provenance), checked into the repo, loaded by the planner at startup. Planner logs the
model version into every plan.

**Validation gate (hard — revised 2026-07-09, Admiral decision):** two checks, both
required before the artifact is accepted:
1. *Form:* the D39 ladder is injected into the validation step as a held-out fixture with
   its known at-the-time tier; the fit machinery must recover ~0.947/step within ±20% from
   those rows (proves the pipeline on the known incident).
2. *Coverage:* every fleet-relevant tier (n_obs ≥ 30) must fit within sane decay bounds
   (sell decay in [0.85, 1.0], buy growth in [1.0, 1.18]).
Rationale: tier labels in the live fit are tier-NOW (market_data snapshot), so a bygone
incident cannot anchor a live-tier assertion — 204 severe historical sell-steps all carry
today's RESTRICTED-era tags (sp-hqrb finding). sp-pf60 (record supply/activity on
market_price_history at capture time) restores a true live-incident gate for future fits.

## Planner — `OptimizeTradeTour` RPC

New RPC on the existing Python routing service (`gobot/services/routing-service/`,
proto in `gobot/pkg/proto/routing/`; the service already serves PlanRoute/OptimizeTour/
OptimizeFueledTour/PartitionFleet, and scout tours already consume PartitionFleet —
`scout_markets.go:302`).

**Request** (fully request-carried): market snapshot (per market-good: ask, bid,
tradeVolume, supply, activity, ObservedAt), ship (position, hold capacity, current cargo,
fuel), constraints (maxHops ≤ 6, maxSpend, minMarginPerUnit, workingCapitalReserve,
allowedSystems), model artifact version expected (mismatch → error, not silent fallback).

**Multi-system tours are first-class but scoped to 2 gate-adjacent systems (Admiral
simplification, 2026-07-09).** A tour spans at most `maxTourSystems = 2` (named constant,
config-overridable later): multi-hop within the start system, at most one outbound gate
crossing into a direct neighbor, multi-hop there, at most one return crossing. This
collapses the beam's graph to the proven lane pattern (NK36→GQ92, +385k hand-flown)
generalized to multi-hop on each side. Cross-gate hops compete on the $/hr objective net
of jump + cooldown time — no penalty term. Default `allowedSystems` = the start system +
its gate neighbors having fresh market data; the captain restricts further by flag.
Market census (2026-07-09): KA42 27 markets, GQ92 23, JP61 15, NK36 (home) 10, ZC66 6 —
NK36↔GQ92 alone is a 33-market tour graph. A system without fresh scans (all rows past
the 75-min age-cap) is invisible to the planner — probe scan coverage is the effective
boundary of the tour graph (see prereqs: probe flock, st-wisp-onno).

**Solve:**
1. Filter snapshot rows older than the age-cap (75 min — same constant as lane ranking;
   staleness discipline is shared, not reinvented).
2. Beam search over hop sequences (seeded from top spread-potential markets; default beam
   width 50, a named constant; travel times via the existing routing graph, jumps allowed
   both directions).
3. Per sequence: tranche-LP over the piecewise curves — decide buy/sell units per good per
   hop, respecting hold capacity flow conservation, tradeVolume tranche ceilings, spend
   constraints, and curve-decayed marginal prices.
4. Objective (**revised 2026-07-10, Admiral decision**): **maximum projected profit**,
   with every hop required to add positive marginal profit; credits/hour is computed,
   reported, and used as the tiebreak between equal-profit tours. Rationale: a single
   tour's $/hr rate prefers concentrated sink-dumps whenever travel is expensive —
   the exact laddering this epic exists to kill — because per-tour rate ignores the
   sink-recovery externality (~4h RESTRICTED / ~23h WEAK, per the fitted model).
   Profit-primary is the safe proxy; the graduation gate still measures REALIZED $/hr
   in the field. Recovery-externality pricing (charging dump tranches their fitted
   recovery time) is deferred to Phase 2 alongside absorption reservations.

**Response:** ordered legs (waypoint, buys[], sells[], expected unit prices per tranche,
projected leg P&L), tour totals ($ and $/hr), **top-3 rejected alternative tours with
one-line reasons** (observability parity with the lane-selection log line), model version,
snapshot age stats.

## Executor — `tour_run` container + `workflow tour-run` CLI

A one-shot daemon container (arb-run's twin — reuse its factory/runner/recovery pattern
wholesale):

- **Launch:** `spacetraders workflow tour-run --ship <S> [--max-hops N --max-spend C
  --min-margin M --replan-limit K] --agent <A>` → daemon RPC → container id returned;
  captain follows via `container logs`.
- **Claim:** atomic ClaimShip, operation="trade" (l7h2 semantics; TORWIND-19's pin matches).
- **Per leg:** `travel()` (departure + arrival hops both live) → dock → **live re-verify**:
  current ask/bid vs plan; if degradation exceeds `tourPriceTolerancePct` (default 15,
  matching the gate metric), skip the affected trades and **re-plan once via a stateless
  RPC** from current position + cargo (max `tourMaxReplans`, default 2; exhausted →
  park + structured report, cargo intact) → execute: each transaction ≤ tradeVolume,
  1z2h ladder-breaker semantics on realized-vs-projected, bp6f working-capital floor
  live-checked before every buy, cumulative spend ≤ maxSpend across retries (5nqx rule).
- **Exit:** ending with unsold tour cargo = **failure** with a stranded-cargo report
  (5nqx rule). Success = all planned sells executed (or explicitly skipped legs reported).
- **Telemetry:** per leg, persist planned vs realized (units, prices, timing) — feeds the
  graduation report and model recalibration. Storage: a small table following the
  spend-reservation migration idiom (w3he).
- **Config:** all knobs in the persisted launch config (recovery-correct, RULINGS #5).
- **Default maxSpend:** 25% of live treasury at launch (GetAgent read; RULINGS #6) unless
  the captain overrides with `--max-spend`.

## Graduation (A→B) and Phase 2

- `spacetraders tour report` computes the three gate metrics from telemetry.
- Gate passed → phase-2 bead activates:
  - autonomous `tour-circuit` container (plan → execute → re-plan loop, trade-route's
    lifecycle),
  - **multi-hull absorption reservations** — the w3he spend-ledger pattern applied to
    market depth (a tour reserves the tranches it plans to consume; a second hull's
    planner sees residual depth) — required before a second hull ever tours
    (self-competition lesson),
  - curve-predicted defer timing for 1z2h's sourcing optimizer (shared model artifact).

## Error handling (summary)

| Failure | Behavior |
|---|---|
| Planner down / timeout | tour-run exits "tour unavailable" (structured), no trading blocked; single-lane fallback remains |
| Infeasible snapshot | structured reason (no lanes clear margin/floor) — mirrors "fail rather than substitute" |
| Leg price degradation > tolerance | skip affected trades → stateless re-plan (≤ replan limit) → park + report |
| Stranded cargo at exit | failure exit + report (never false success) |
| Model/artifact version mismatch | planner errors loudly; no silent fallback to a stale model |
| Era reset | fresh scans suffice (structural curves transfer); artifact era-stamp logged |
| Daemon restart mid-tour | container recovery via registry (RULINGS #2); cargo-aware resume: re-verify position/cargo, re-plan from current state |

## Testing

- **Model:** held-out incident validation (D39, K79) within ±20% — hard pipeline gate.
- **Planner:** golden-snapshot tests (fixed snapshot → expected tour); property tests
  (never exceeds hold/spend/reserve; tranches ≤ tradeVolume; hold flow conservation;
  $/hr non-negative for accepted plans); infeasible → structured reason; stale rows excluded.
- **Executor:** degradation → re-plan; planner-down → fail-open; stranded = failure;
  guard regression (floor, ladder-breaker, cumulative caps); restart-recovery resume;
  end-to-end sim (plan → fly → telemetry) in the 1z2h acceptance-sim style.
- All of it runs under `go test ./...` + the Python pipeline's own test suite, i.e. every
  captain-gate run exercises the Go side; the Python side gets a make target wired into CI.

## Delivery plan (child beads under sp-1ek0)

| Bead | Scope | Model tier | Dependency |
|---|---|---|---|
| P0 | Model pipeline + versioned artifact + validation gate | fable | probe-flock scan cadence helps (st-wisp-onno) but not blocking |
| P1a | Proto + `OptimizeTradeTour` + beam/LP solver + golden tests | fable (same agent continues from P0) | P0 artifact |
| P1b | `tour_run` container + CLI + telemetry + guards | opus | P1a proto merged |
| P2 | Graduation report + autonomous circuit + absorption reservations | filed now, built after gate passes | 10-tour gate |

## RULINGS compliance

#1 n/a (no contracts touched) · #2 restart-resilient (persisted launch config, registry
recovery, stateless re-plan) · #3 single-writer (Python never writes; Go container is the
only actor) · #4 all money guards inherited and live-checked · #5 all knobs parametrized ·
#6 default maxSpend from treasury fraction · #8 captain notified at every deploy ·
#12 standard gate discipline.
