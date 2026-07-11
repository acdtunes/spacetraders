# Postmortem: Fleet Earners Dark 2h50m — Planner spend_cap Zeroed by Double Capital Guard

**Date:** 2026-07-11 (01:41Z → 03:31Z)
**Severity:** P0 — all 9 heavy earners idle; treasury 1.4M → 456k trough
**Status:** Resolved (2063c05 deployed 03:27Z; full trade chain verified live 03:31Z)
**Record:** sp-4hl5 carries the verbatim evidence chain; this doc is the synthesis.

## Impact

- Tour fleet (the primary earner, ~1–2M/hr proven) produced **zero revenue for 2h50m**.
- Treasury bled 1.4M → 456k — not through bad buys (guards held), but through
  factory/contract spend continuing with no tour income. Failure mode was
  idle-not-loss.
- Two harbormaster process failures delayed engagement ~40 minutes after the
  captain's page.

## Timeline (UTC)

| Time | Event |
|---|---|
| evening | 9 heavies profitable-but-thin (+40–80k/heavy, ~250k tranches). The configured 1M reserve is silently ineffective (int64 config bug — sp-ggk2's target): planner sees reserve 50k, subtraction harmless. |
| 00:47Z | Captain re-mans coordinator at reserve=1,000,000 (standing manual practice). Value persists in container config — still ineffective. |
| 01:41Z | Deploy boundary (ggk2/kk61/mtvg/ed4i). ggk2's int64 fix makes the 1M reserve **reach live launches for the first time**. Two failures arm at once: (1) planner spend_cap = max(0, 25%×treasury − 1,000,000) = **0** at any treasury < 4M → every tour infeasible; (2) discovery collapses origin-only at multiple origins (the 1ki5-class rot's tour face). **Treasury flatlines this exact minute.** |
| 01:41–02:42Z | 123 tour exits `reason='starvation'`/hr. Reads as churned-barren ground (true for the evening's 1-hop neighborhoods — but the fleet-wide zero was the spend_cap defect). |
| ~02:42Z | Captain: evidence mail + semantics question (egny3); manually disperses 4 heavies to fresher grounds — the only escape available, since reposition candidates are 1-hop-horizon only. |
| ~02:51Z | Captain files **P0 sp-4hl5** + notify mail (p7eov). The page lands in the shipwright inbox. |
| 02:50Z | Harbormaster's batched deploy (yqx4/nivi/ikx1/1ki5/1wp8). **Fixes the discovery half** — multi-system lists return — which unmasks the economics half: plans everywhere, all infeasible. Harbormaster misses the P0 during close-out (see Process Failures). |
| ~03:10Z | Admiral escalates. P0 found; single fable lane dispatched; Admiral calls out under-parallelization → split to 3 lanes (leg A code, leg B independent verdict, sp-1pli relaunch backoff). |
| 03:24Z | Leg A: root cause + fix merged (**2063c05** — dynamic-budget tours hand the planner reserve 0; buy-time floor stack remains the enforcement guard). |
| 03:27Z | Deployed (daemon rebuild + kickstart; routing/rate-objective untouched). |
| 03:28–03:31Z | **Zero** infeasibility lines; 5 feasible multi-leg plans (+17k/+19k/+35k projected); first tour buy (149,328 cr, floor engaged correctly); first realized telemetry leg; sells landing. Full chain live. Leg B independently converges: same mechanism, objective exonerated by 2×2 matrix. |

## Root cause chain (layered — no single villain)

1. **The defect** — `planForState` forwarded the absolute reserve into the solver,
   which computes `spend_cap = max(0, max_spend − reserve)`. Under the dynamic
   budget (max_spend = 25% of live treasury) the capital guard was applied
   **twice**: once by the 25% sizing + buy-time floor (correct), once by the
   planner subtraction (the defect). Latent since the dynamic budget existed.
2. **The enabler** — the int64 config bug meant no big reserve had ever reached
   the planner. ggk2's *correct* fix armed the latent defect: making a dormant
   config value effective is itself a deploy-wide behavior change, and the
   planner was an unenumerated consumer of that value.
3. **The mask** — the simultaneous origin-only discovery collapse at the same
   boundary made the incident read as a graph problem first (it genuinely was,
   half of it), splitting the diagnosis across two defects.
4. **The blindness multiplier** — the solver reports generic
   `no_profitable_tour` whether the market is dead or the budget was zeroed.
   ~70 minutes of misdiagnosis trace to the missing `infeasible_reason`.
5. **Process failures (harbormaster)** — see below; ~40 minutes of delayed
   engagement after a correctly-executed page.

## What went right

- **The captain's watch**: fast P0 with verbatim evidence + ranked suspects;
  correct emergency dispersal; calibration answers within minutes; caught the
  wrong "FIXED" claim on a failing origin within 5 minutes (the battle-rhythm
  acceptance reads working as designed).
- **Fail-closed guards**: zero bad buys during the incident. RULINGS #4 held.
- **Parallel lanes once forced**: leg A root-caused in ~40 min; leg B converged
  independently — the rate objective (+31.6% replay winner) was exonerated with
  evidence instead of reverted on suspicion.
- **sp-7yej lifecycle contract**: three daemon restarts tonight; containers
  re-adopted cleanly every time.

## Process failures (harbormaster) — each with a banked corrective

| Failure | Corrective (status) |
|---|---|
| P0 page sat unread through two mail sweeps (head-truncated listing pattern-matched as stale backlog) | Sweep to unread-zero with timestamp audit, every wake (banked, exercised same night — recovered the unanswered starvation question) |
| `reachable=false` sat in my own quoted tool output, read past it for the expected story | Read the whole anomalous line; anomalies in own output are evidence, not noise (folded into acceptance-read memory) |
| Deploy close-out reported "board settled" without a P0 board sweep | Every deploy close-out and wake ends with `bd list --priority=0` + fresh-mail check (banked, exercised) |
| "FIXED" declared after verifying a healthy origin (ZC66) while the failing origin (PD21) still collapsed | Acceptance reads must exercise the failing case named in the bug (banked) |
| P0 initially staffed with one lane carrying two independent legs | Decompose P0s into independent lanes immediately; cap exists to be used (Admiral-corrected live) |

## The deep lesson (new class)

**When a fix makes a previously-ineffective config value reach production,
enumerate every consumer of that value and test each at the newly-effective
magnitude.** ggk2 was correct, tested at the buy seam — and detonated two seams
downstream because "reserve=1M now works" was treated as a fix, not as the
behavior change it was. This generalizes the guard-rejects-a-class lesson from
values to consumers.

## Corrective actions

**Deployed:** 2063c05 (planner/reserve decouple); 81c53be (durable-first
discovery); 97490b2 (gate-probe backoff — unrelated but same night's boundary).

**Filed:** pct-stamp path verify (comment/code mismatch, one launch path may
still stamp no proportional floor); solver `infeasible_reason="reserve_exceeds_budget"`;
stranded-hull detector (sp-686e — TORWIND-2C at PD21 needs captain extraction);
sp-1pli relaunch backoff (in build at time of writing).

**Strategic (Admiral decision pending):** the 1-hop reposition horizon
(MAX_TOUR_SYSTEMS=2) made self-escape from churned ground impossible — the
night's true starvation face. This is the sp-mepj question, now with evidence.

**Doctrine (sent to surveyor for era-template inclusion):** sweep-to-zero,
P0-sweep-on-close-out, failing-origin acceptance reads, dormant-config consumer
enumeration.
