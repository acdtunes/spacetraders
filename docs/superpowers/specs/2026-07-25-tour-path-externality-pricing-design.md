# Tour-path recovery-externality pricing — fleet anti-herd for the trade fleet

**Status:** design, pending approval
**Author:** economy-analyst (brainstormed with the Admiral, 2026-07-25)
**Parent:** `sp-78ai` part (b) — the unbuilt half of the 1ek0 P2 flagship
**Era:** torwind-2026-07-19 (reset 2026-07-26T13:00Z) — design durable, arm and measure this era

---

## 1. Problem

The trade fleet concentrates. Measured live, matched 3-hour windows either side of the
`sp-4ki1x` visibility deploy (22:15Z):

| 3h window | sink systems | top-3 rev share | top-5 rev share | gross |
|---|---|---|---|---|
| pre-fix 19:15–22:15 | 29 | 32.7% | 45.3% | 28.8M |
| post-fix 22:15–01:15 | 34 | 36.7% | **51.0%** | 33.2M |

The fleet reaches **more** systems (+17%) and earns **more** (+15%) while concentrating
**harder** into its top sinks. Widening the visible market universe did not disperse the
herd — it gave the solvers a better menu to pile onto.

This matters because arbitrage is sink-limited (PLAYBOOK §2): selling crushes the bid
~1.5%/tranche (40–60% on a dump) and a crushed lane is dead 12–24h. Concentration does not
merely split profit N ways; it destroys the lane for every hull and for hours.

**Caveats, stated honestly.** 3h windows, one boundary, n small; the concentration deltas
are modest enough to be variance. Gross revenue, not net — per PLAYBOOK §8 income is never
projected from transaction sums. The finding is directionally consistent across both
concentration measures and matches theory, but it is suggestive, not proven. It justifies
the build; it does not by itself validate it.

## 2. What already exists (do NOT rebuild)

This was the dominant finding of the design pass. Most of the coordination layer is built
and armed. Verified in live code, not inferred:

- **Fleet-wide absorption ledger** — `sp-78ai` L2/L3/L4. Persisted `market_absorption_ledger`
  table (`AbsorptionLedgerGORM`), `SetAbsorptionLedger` injected into the tour, arb, and
  trade-route coordinators, dead-container reclaim sweep, `assembleAbsorption` per plan
  (`run_tour_coordinator.go:1850`).
- **`net_absorption`** (`tour_solver.py:407`) — nets other containers' outstanding depth out
  of a pool's tranche schedule. `units_planned` drops tranches from the **head** (capacity
  *and* the best prices); `units_recovering` drops from the **tail** (capacity only, because
  the live quote already reflects executed crush).
- **Shadowed-lane exclusion** — `filterShadowedLanes` removes lanes whose sell side is
  shadowed (`run_trade_route_coordinator_lanes.go:126`).
- **Fleet price de-ranking on the circuit path** — `sp-tl68`, armed at
  `cmd/spacetraders-daemon/main.go:909`: circuit lanes rank on *effective* spread (snapshot
  less this hull's self-compression, less live shared cooldown debt).
- **Multi-hop reach** — `sp-tp5c3` prices a crossing as `gate_hops × inter_system_seconds`;
  `candidate_hop_depth: 2`, `max_tour_systems: 4`, both armed in `config.yaml`.
- **Reposition machinery** — `reposition_reach` + `reposition_rate_floor`, armed, with
  deadhead decay 0.85/hop and a per-system anti-herd cap.

## 3. The gap — a path asymmetry

The live trade fleet flies **tours** (`tour-run-*` containers spawned by the trade-fleet
coordinator, MECHANICS §6.13), not circuits. The two paths are defended unequally:

| Signal | Circuit path | Tour path (live fleet) |
|---|---|---|
| Capacity netting (others' planned/recovering) | yes | yes |
| Shadowed-lane exclusion | yes | yes |
| Fleet price de-ranking / cooldown debt | yes (`sp-tl68`) | **no** |
| Recovery-externality cost | no | **no** |

**On tours, once capacity remains, nothing de-prices a hammered sink.** `net_absorption`
deliberately keeps head prices at step 0 — correct for *past* crush (already in the quote)
and it leaves *future* crush unpriced. Each hull independently observes remaining depth at a
top sink, is individually correct, and they converge. The fitted recovery half-lives are
loaded at `tour_handler.py:44-46` and, per `sp-78ai`'s own audit, are **"consumed by NOTHING
at decision time."**

Nothing charges a hull for the 3–7h recovery window its marginal tranche imposes on the fleet.

## 4. Design

A **price**, not a rule. Spread becomes emergent: at equal spread a hull prefers the sink the
fleet is not recovering. No territories, no mutex, no partition rebuilds — and a mispriced
externality costs margin instead of deadlocking a hull.

### 4.1 Component 1 — recovery-externality pricing (core)

Each planned **sell** tranche into `(sink, good)` carries a charge proportional to the crush
it inflicts, scaled by that market's activity-fitted recovery half-life, normalised against a
reference window, and weighted by a single fitted coefficient `externality_weight`.

Shape (exact functional form to be fitted during implementation):

```
ext_cost(tranche) = crush_inflicted(tranche)
                  × (recovery_halflife(activity) / reference_window)
                  × externality_weight
```

`recovery_halflife` reuses the already-loaded fitted table (GROWING 181min, WEAK 279,
STRONG 386, RESTRICTED 414, untagged 1074) — **one definition, no redeclaration in Python**,
mirroring the `sp-4ki1x` lesson about a second cap table drifting from the first.

**Why this cannot double-count** (the main technical risk, and it is structurally avoided):
three disjoint accounts —

1. *Past* crush → already in the live quote.
2. *Planned* depth by other hulls → netted as **capacity** by `net_absorption`.
3. *Future* crush this plan creates → **the new term**. Nothing else prices it.

### 4.2 Component 2 — cross-plan A-cap continuity

Today the 2-tranche-per-`(market, good, side)` A-cap **resets at every plan boundary**, so
consecutive plans lawfully rebuild the D39 ladder at the same sink (`sp-78ai` §0). Carry the
cap in the existing ledger keyed by `(sink, good, side)` under recovery decay, so a plan sees
its predecessor's consumption. This extends the existing store's authority; it is not new
storage.

### 4.3 Component 3 — proactive reposition targeting

Implements the chosen idle policy: **reposition instead of idle.** Today reposition fires
only reactively (margin-death, or under 40% of fleet median). Make it continuous: when a
hull's best *post-externality* lane value falls below the fleet's median opportunity, deadhead
it toward the region with the highest **unreserved** absorption — which the ledger already
knows. Reuses `reposition_reach`'s deadhead decay and per-system anti-herd cap; only the
trigger changes.

## 5. Non-goals

- **No new coordinator.** The trade-fleet coordinator and tour solver stay.
- **No territory partition / sink mutex.** Both were considered and refuted (§8).
- **No multi-hop engine work.** It exists and is armed; deeper reach is a config value.
- **Circuit-path depth awareness.** Already shipped (`sp-78ai` L4 + `sp-tl68`).

## 6. Guards and failure semantics (RULINGS #4)

- The externality term is an **objective** term, not a spend guard. Unreadable half-life or
  config **fails open to today's behaviour** — safe, because today's behaviour is the
  measured baseline.
- The cross-plan A-cap bounds **buy commitment** and therefore **fails closed**, consistent
  with the existing guard stack. A ledger read failure degrades to the current per-plan cap,
  never to unbounded.
- **No feature flag** (standing Admiral policy). Ships **armed** at a fitted default.
  Revert = `externality_weight: 0` + daemon restart — the same numeric-knob, revert-ready
  pattern already used for `candidate_hop_depth`.
- Existing money guards are untouched; nothing here relaxes a guard as a side effect.

## 7. Validation

**Primary KPI (the discriminator):** top-5 sink revenue share **falls** while net cr/hr holds
or rises. Spread alone is not success — spread *without* rate loss is.

**Honesty guard:** `tour_plan_rate{phase=projected|realized}` divergence must not worsen. If
projected improves while realized does not, the term has made the planner optimistic rather
than correct.

**Counter-metric / tuning rule:** if top-5 share falls *and* net cr/hr falls,
`externality_weight` is too high — reduce it.

**Measurement discipline:** net credit deltas over closed hours, never transaction-sum gross
(PLAYBOOK §8). Matched-length windows only — unequal windows mechanically distort
concentration and produced a spurious result during this design pass. Minimum 4 clean
post-arm hours before crediting any effect.

**Baseline to beat:** 34 sink systems, top-3 36.7%, top-5 51.0% (22:15–01:15Z, 2026-07-25).

## 8. Alternatives considered and refuted

- **VRP territory partition** (mirroring the scout-post probe partitioning, MECHANICS §6.3).
  Proven in-repo and gives a hard non-overlap guarantee, but optimises *coverage* rather than
  cr/hr: a hull handed a poor territory earns poorly while a rich one is under-exploited, and
  territories need constant rebuilding as freshness shifts — freshness being the most volatile
  input we have, at 30min/hop to rebalance.
- **Hard sink claim / mutex.** Simplest and absolutely prevents crush, but it is 1-bit
  quantisation of the pricing approach: it blocks a deep EXCHANGE sink (trade volume ~180–240)
  that could comfortably absorb several hulls, and introduces starvation and deadlock risk.
- **Build a new trading coordinator.** Refuted: duplicates shipped, wired machinery.

## 9. Risks

0. **BLOCKING — the fitted recovery table is from a previous era.** Discovered during planning:
   `services/routing-service/model_artifacts/market_model.json` is stamped
   `era: torwind-2026-07-05`; the live era is `torwind-2026-07-19`. PLAYBOOK §12 rules last era's
   coefficients FALSE PRIORS. Sample counts are also thin — `STRONG n_series=1`,
   `GROWING n_series=3`, untagged `n_series=3`; only `RESTRICTED` (22) and `WEAK` (20) are
   meaningfully fitted. Since this design *multiplies by* those half-lives, pricing against them
   would make the charge arbitrary for exactly the fast-moving activities where it matters most.
   **Mitigation:** re-calibrate before arming (plan Task 1), and fall back to the pooled untagged
   half-life for any tier with `n_series < 5`.

1. **The externality is mispriced.** Mitigated by a single fitted coefficient, the
   counter-metric above, and a numeric revert.
2. **The measurement is small-n.** The build is justified on a suggestive signal. Mitigated by
   the 4-hour minimum and the paired share/rate discriminator.
3. **Era boundary.** Reset 2026-07-26T13:00Z. PLAYBOOK §1 bars arming anything unvalidated
   inside the final 12h — arming must complete by ~2026-07-26T01:00Z to leave a measurement
   window, or it waits for next era.
4. **Interaction with the hop-depth sweep.** The `sp-1cuy6` depth numbers were measured
   through the `sp-4ki1x` defect and are known-pessimistic. Re-running that sweep and arming
   this term at the same time would confound both. Sequence them.

## 10. Open questions for implementation

- Exact functional form and fitted default for `externality_weight`.
- Whether the charge applies to sell tranches only, or to buy-side depletion at source
  markets as well (sell-side first; buy-side is a smaller externality).
- Whether component 3 (reposition trigger) should land in the same change or follow, given it
  is behaviourally the largest and most visible shift.
