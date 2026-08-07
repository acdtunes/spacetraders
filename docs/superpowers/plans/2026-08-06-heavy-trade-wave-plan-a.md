# Heavy/Probe Trade Wave — Plan A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship migration steps 1–3 of epic sp-sy3dl. **End state, which is working deployable software:** the fleet-growth coordinator is the fleet's only heavy buyer; the wave alternates between buying heavies and buying probes from one predicate derived every tick and read by both spenders; the heavy-yard pricing errand runs from the growth coordinator; the probe drain is gated on the wave and no longer carries the heavy hold in its buy floor. **The autosizer still exists and still runs, but is inert for heavies** — its light class continues untouched. Deleting it is Plan B.

**Architecture:** One pure predicate (`common.DeriveWave`) sits beside `common.HeavyReserve` and is the single definition of the regime. A new `FLEET_GROWTH_COORDINATOR` container owns the heavy buy path and drops into the autosizer's shape, reusing its container plumbing, its fail-closed purchase guard stack and its pricing errand. The `parkedsensing` probe drain gains a gate on the same predicate, read through one port over the same shared query instances.

**Tech Stack:** Go, gorm/Postgres, Prometheus (`internal/adapters/metrics`), the daemon's container/mediator/liveconfig plumbing. Tests are stdlib `testing` with hand-written fakes; `go test ./... -race` is the gate.

**Base:** main at `18d51968`. Verified during planning; line numbers may drift.

---

## Global Constraints

Every task's requirements implicitly include this section.

- **NO FEATURE FLAGS.** Standing Admiral order, overriding the usual ship-default-off instinct. Everything here ships **ARMED**: no default-off seam, no arming step, no `*_enabled` flag introduced to stage a rollout. Operator knobs with a live default (`growth_enabled`, `heavy_cap`, `growth_runway_milli_hours`) are controls, not flags — they ship on. Money guards are not flags.
- **RULINGS #4 — money guards fail CLOSED and are never weakened.** Any unreadable input (price, treasury, heavy census, lane count, cargo ledger, API utilization) BLOCKS a purchase. No change here may relax a guard as a side effect. The working-capital term is added **above** `common.ImmutableReserveFloor` (50000), never below it, and never replaces it.
- **RULINGS #2 — derived, never stored.** The wave predicate is recomputed from durable facts on every tick in every consumer. Nothing about the wave is persisted, cached, or carried between ticks: no cursor, no accumulated savings counter, no streak on the predicate. The high-water treasury clause 4 judges is **a query, not stored state** — it is re-derived from ledger rows on the tick that asks, which is what keeps this clean.
- **RULINGS #5 (as bounded 2026-07-25)** — operational values are knobs; values the game's own rules fix are documented constants. The discriminating test is "would an operator change this because of something they observed?"
- **RULINGS #6** — the 25%-of-treasury rule governs heavy purchases, unchanged.
- **Comment discipline (`ENGINEERING.md` §6, binding).** No archaeology, no "previously we…", no bead-ids in source, **no pinned measured numbers** (no prices, hull counts, percentages, "measured live"). Say why a bound exists and what breaks without it — never the arithmetic that picked it. Every derivation in this plan belongs in the plan and in commit messages, not in a comment. Run `make comment-audit-check ONLY=<touched packages>` from `gobot/` before each gate; no package may end denser than it started.
- **Ordering is a safety property, not a preference.** The spec's order exists so no step leaves a latched bridge, an unserved demand, or a silently-zeroed reserve. Plan A's ordering is justified against that invariant in "Task order" below, and the one place Plan A must reach into step 5's territory to preserve it is called out explicitly.
- **One definition, two consumers, lockstep-pinned.** The idiom the codebase already trusts for `HeavyReserve`/`HoldAt` (`internal/application/common/heavy_reserve.go:92-105`, pinned by `TestHeavyReserveLockstep` at `heavy_reserve_test.go:41`). Every quantity two spenders must agree on is defined once and called by both, and constructed once at the composition root so a second instance is conspicuous.
- **All code lands via worktree → `captain-gate` → main (RULINGS #13).** Never merge by hand. Commit in the worktree before gating; use `git commit --no-verify -- <pathspec>` so the bd hook does not stage `issues.jsonl`. **The gate rewrites the commit**, so verify a landed lane by tree equality against main, not by ancestry.
- **Protected paths** — never modify `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.

### Build & verify commands

From `/Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot`:

```bash
go build ./...
go vet ./...            # gates signature changes go build skips (it compiles _test.go)
gofmt -l .              # must print nothing
go test ./internal/... 2>&1 | grep -Ev '^(ok|---|\?)' | head -50   # filter; a raw run floods context
go test ./... -race     # the pre-merge sweep
make comment-audit-check ONLY=<touched packages>
```

---

## Clause 4: RULED — reachability is judged on high-water treasury

**Ruling (2026-08-06):** clause 4 becomes a reachability test over a **high-water treasury**, not the spec's live-treasury form and not the `surplus > 0` I first recommended. This section records the evidence that decided it, because the choice is a money guard and the reasoning has to survive the next reader.

### The defect being fixed

Spec §88 clause 4 is `surplus ≥ entry_threshold`, expressed as `HeavyReserve(...).HoldAt(treasury) > 0` (§101), with `HeavyReserveEntrySharePct = 50` (`common/heavy_reserve.go:77`, `:224`). Against staging's priced heavy that puts engagement at a fixed treasury of `50,000 + (1,916,613 × 50/100) = 1,008,306`.

**Staging treasury oscillates across that line within single trade cycles** — roughly 119k to 1.5M — clearing it in about one sample in five. So clause 4 does not fail to fire; it **flaps at roughly a 20% duty cycle, keyed on where the tick lands in a trade cycle.** Three consequences, and the third is fatal:

1. **The mode stops meaning anything.** HEAVY is supposed to mean "this fleet is credibly saving for a hull". It comes to mean "a tour just sold its cargo".
2. **It is inverted.** Treasury peaks immediately after a sale, so the predicate says HEAVY *then*, and flips to PROBE once cargo is bought and the money is committed. The probe-buying window opens at the trough, where `ProbeBuyFloor` is most likely to hold it shut anyway.
3. **The mechanism never engages.** Pausing the trickle is the entire design: the treasury must be held still long enough to reach the lump. A wave that flips every trade cycle spends most of its ticks releasing probe buying, and the treasury oscillates *through* the threshold instead of climbing past it.

One trough sample shows the arithmetic concretely — treasury 479,798 gives surplus 429,798 against an entry of 958,306, so `HoldAt` returns 0, matching the observed `buy_heavy_reserve: 0`. That confirms the mechanism, but it is **one sample of an oscillation, not a steady state.**

### Why not `surplus > 0`

It buys stability by deleting the §395 anti-deadlock regression. With the reachability clause gone there is no longer any notion of an *unreachable* heavy, so "an unaffordable heavy must not pause probe buying" becomes untestable — the property survives only through clause 3 (no priced target), which is a weaker and different guarantee. A predicate that is stable because it stopped asking the question is not a fix.

### Why not income rate × runway — the repo already tried this and deleted it

This is the option with the strongest evidence against it, and the evidence is the project's own history.

Commit **`248f8799`** (sp-71fvl, 2026-07-25) removed `incomeWindow` — a rolling mean over the last N realized-$/hr observations, with a `math.Inf(-1)` sentinel until the window filled, built precisely so a single payout could not trip a gate — **and then removed the income term from the bootstrap GATE entirely.** The surviving rationale is at `run_bootstrap_reconcile.go:332-334`: realized $/hr is *"a spiky signal one contract payout swings from net-negative to a false all-clear in a single tick"*. `bootstrap_types.go:70-74` now marks `IncomePerHour` **observability only**, *"it drives no phase transition"*.

What replaced it in `gateFunded` (`run_bootstrap_reconcile.go:349-354`) is two **structural** bars — fleet size against the live scaler target, and **treasury surplus over `ImmutableReserveFloor`**. So this codebase has already faced this exact question for this exact class of gate, tried smoothing, concluded smoothing was insufficient, and landed on a treasury-surplus structural bar. Repeating the experiment would be re-litigating a settled decision.

It also needs a time-to-reach horizon constant with no derivation behind it, and a rate over a window is a second windowed quantity for two consumers to disagree about.

### Why not treasury + in-flight cargo value

It is the truest statement of position, and in principle the right answer. It loses on what exists.

A valuation does exist — `Briefing.inventoryValue` (`internal/captain/briefing_source.go:351`), joining `ships.cargo_inventory` to `market_data`, zero API calls. But:

- It is an **unexported method on `*Briefing`**, bound to that struct's `db` and `playerID`. Not a port, not in `application/` or `domain/`. And `internal/captain/**` is a protected path this plan may not modify.
- It is **Postgres-only by construction** (`jsonb_array_elements`) with an explicit *"degrades to 'n/a' on a non-Postgres store — exactly the fail-open contract"* (`briefing_source.go:330-332`). **Fail-open is the wrong direction for a money guard.**
- Its price side takes `MAX(sell_price)` across **every waypoint ever cached, with no freshness filter and no reachability scope**. That marks the hold at the best bid anywhere in the universe on data of arbitrary age — the optimistic direction, which inflates the position.
- The cost-basis gap is documented: `ships.cargo_inventory` records units, not price.

So the real work is not moving a query, it is **adding the freshness and reachability bound that does not exist** — genuinely new surface on the price side. And it does not fully de-cycle anyway: cargo is bought at the ask and sold at the bid, so treasury + hold is not conserved across a cycle.

### The chosen measure: high-water treasury over a trade-cycle window

```
reachable  iff  HeavyReserve(...).HoldAt(highWaterTreasury) > 0
```

**It is the same function, given a different balance.** `HoldAt` already computes `clamp(0, ask, (balance − ImmutableReserveFloor) − ask × entryPct/100)`, so `HoldAt(highWater) > 0` is exactly `highWater − floor ≥ entry`. Nothing about the entry arithmetic is copied, re-derived, or weakened — including its overflow-safe two-term form (`heavy_reserve.go:224`) — and the cap, priced-target and capability clauses keep flowing through `HeavyReserve` untouched. The diff is one argument.

**What changes is the question being asked.** `HoldAt(liveTreasury)` answers *"how much may I withhold at this instant"* — a question about the moment, and the right one for a withholding decision. `HoldAt(highWaterTreasury)` answers *"is this ask reachable by this fleet"* — a question about capacity, and the right one for a regime. Wrapped as `common.Reachable(target, highWater) bool` so the concept has a name and one call site rather than a bare comparison a reader could mistake for the withholding rung.

**Why it satisfies the constraint.** The peak *is* the signal: a max over a window that spans a full trade cycle does not move with the cycle's phase. At staging the high-water sits at the band's top through the whole oscillation, so the mode is constant across 119k–1.5M — which is the property being bought.

**Why the evidence supports trusting `balance_after`:**

| Concern | Evidence |
|---|---|
| Is it populated? | `NOT NULL` in the original `CREATE TABLE` (`migrations/008`, `011`); no `ADD COLUMN` migration anywhere in 53 files, so there is no backfilled cohort. |
| Could a writer leave it blank? | There is exactly **one** `ledger.NewTransaction` call site and **one** `Create` call in the whole non-test tree (`record_transaction.go:140`, `:157`). All nine transaction types route through it, and all seven writer paths supply an authoritative API balance. |
| Could it be a meaningless zero? | `Validate()` rejects `amount == 0` and enforces `after == before + amount` (`transaction.go:137`, `:144-153`), so a zero `balance_after` is always a genuine arithmetic result, never a blank field. |
| Would a money guard trust it? | One already does. `LedgerTreasury.Credits` — the treasury read behind every money guard — serves from this column (`ledger_treasury.go:168`). |
| Has it been measured? | `ledger_treasury.go:15-19`: across 3,086 consecutive transaction intervals the unrecorded-spend gap was zero and the ledger matched the live API to the credit. The same measurement before the jump-fee fix read −7,036,882, which is what an incomplete ledger looks like. |
| Which way does the known drift run? | The one documented failure mode — a cold-start anchor that never re-anchored (`record_transaction.go:204-214`) — drives values **down**. A MAX ignores low outliers. |

**The residual, stated plainly.** An unrecorded spend leaves the derived chain reading high until the next authoritative row re-anchors it, and unlike the point read there is no freshness bound that can catch it — a peak is permanent for the life of its window. **That residual is bounded to an opportunity cost, and this is the argument that makes the whole choice safe:** the wave predicate authorises no spend. An inflated high-water can only produce a HEAVY wave, which pauses probe buying; the heavy purchase itself remains gated by `guardAffordability` reading the **live** treasury through the full fail-closed stack, plus the 25% rule (RULINGS #6), plus the working-capital term. So the worst case is a paused trickle, never an unaffordable buy. This is the same posture `HeavyReserve` already documents for itself — *"this predicate authorises no spend of its own — it only withholds"* (`heavy_reserve.go:119-124`).

**Window: reuse the one Task 2 promotes, renamed for what it actually measures.** Both consumers of that constant need the same physical property — *at least one full trade cycle of history* — so that a measurement taken mid-cycle is not a measurement of the cycle's phase. Term (a)'s cargo outflow needs it to be a rate rather than a snapshot; the high-water needs it so the max spans a peak. One constant, named `TradeCycleWindow`, with the sizing criterion stated as a property: **a peak lands in the window only if the cycle is no longer than the window**, and a window shorter than the cycle puts the flapping straight back. A second, differently-sized window for the same fleet is the drift this design exists to remove.

**Empty window is UNREADABLE, not zero.** A window with no rows must not read as a zero high-water. This repo has been bitten by that three times (`ledger_treasury.go:136-143`, `era_repository.go:234-243`, `detectors_credits.go:86-99` all carry the "EMPTY IS NOT ZERO" warning). Scan into a slice or a nullable, and report `readable=false`, which the predicate turns into PROBE — the release direction.

### What the §395 anti-deadlock regression now asserts

The spec asks for it by name: *"An **unreachable** heavy must **NOT** pause probe buying."* Under the live-treasury form that test could only ever sample a moment; on an oscillating fleet it would pass at the trough and fail at the peak while testing nothing about reachability. It is now:

> **A fleet whose PEAK treasury across a full trade cycle is far below the entry threshold must not pause probe buying.** Priced target present, unserved lanes present, high-water well under `floor + entry` → PROBE; probe buying continues; `ImmutableReserveFloor` still binds.

This is **equivalent protection and a strictly stronger statement**. The property sp-zg71k needs is "a heavy nobody can plausibly afford must not freeze the other spender". The old form asked "are we short right now", which a rich fleet fails five times an hour. The new form asks "has this fleet touched enough money in a full cycle to be credibly saving", which is what `HeavyReserveEntrySharePct` was always documented to mean — *"a fleet holding half a hull's price is credibly saving for it; a fleet holding a fifth of it is not saving, it is starving"* (`heavy_reserve.go:70-76`). The constant keeps its meaning; it is now evaluated against the fleet's capacity rather than its phase.

### Residual risk this introduces, named rather than hidden

The two consumers tick on different periods, so their trailing windows differ by up to one tick interval and could in principle contain different peaks. That can only matter when a threshold-deciding peak enters or leaves in that gap. It is bounded, it is the ordinary cost of any windowed shared signal, and it is exactly what the two-reader `fleet_growth_wave{reader=...}` gauge exists to catch — which gives that metric a concrete failure to detect rather than a hypothetical one. The lockstep test pins the pure function; the gauge catches the inputs diverging.

## How the seven open questions are resolved

Answered once, here, even where two of them nominally touch Plan B's territory.

| # | Question | Resolution |
|---|---|---|
| 1 | Hysteresis on the wave predicate | **DECIDED: no hysteresis on the predicate.** Keep the existing 3-tick anti-thrash streak on the heavy BUY only. Structural, not taste — see below. |
| 2 | `runway_milli_hours`: value and const-vs-tunable | **DECIDED: tunable `growth_runway_milli_hours`, default 2000 (2h), bounds 0–10000** — the exact shape, units and bounds of `capital_multiplier_k_milli`. |
| 3 | `observed_typical_hold_fill_cost` derivation | **DECIDED: largest single `PURCHASE_CARGO` row (absolute value) in the same trailing window term (a) already reads.** One query, two statistics, no new schema knowledge. |
| 4 | `contract_delivery` and `light` provider disposal | **DECIDED: both demand classes die (in Plan B); both constants live.** The open half — does light demand come back — is answered NO on code evidence. |
| 5 | Does the growth coordinator inherit `sizing_enabled`? | **DECIDED: yes, as `growth_enabled`, same 1=on/2=off encoding, same "off stops the READS" semantics — plus a new obligation the spec missed: off must force the wave to PROBE.** |
| 6 | Where `heavy_cap` lives | **ANSWERED IN CODE by sp-59cl1, now merged.** Plan A consumes its mechanism and makes one declaration change — a *replacement*, not an addition, and that distinction is a safety property. |
| 7 | Does the wave predicate need its own metric? | **DECIDED: yes, and emitted by BOTH consumers with a `reader` label** — one gauge cannot detect the split-brain this design's central invariant forbids. |

### 1 — Hysteresis: none on the predicate

The spec framed this as a taste tradeoff. It is a correctness constraint.

The predicate has two consumers in **two different containers**: `FLEET_GROWTH_COORDINATOR` (tick default 900s) and `PROBE_SENSING_COORDINATOR` (its own `tick_secs`). A streak is per-container in-memory tick state (`autosizerState.heavyShortfallStreak`, `run_fleet_autosizer_coordinator.go:162`, advanced at `fleet_autosizer_act.go:238-244`). Two containers cannot share one in-memory streak. Making the predicate stateful therefore forces one of:

- **(a)** persist the streak — a stored derivation, and the thing RULINGS #2 exists to prevent; or
- **(b)** give each consumer its own streak — which diverge whenever the tick periods differ, which **is** the split-brain the lockstep idiom exists to prevent.

Both are worse than flapping. And flapping's cost here is bounded: a HEAVY→PROBE flip releases probe buying for one drain tick (still behind `ProbeBuyFloor`, the probe cap, and the immutable floor); a PROBE→HEAVY flip pauses it for one tick. Neither makes a wrong purchase — every buy is independently guarded.

**What is kept:** `heavy_unserved_lanes_min` (default 3) stays, on the heavy buy, inside the growth coordinator, where the tick state already lives and where `guardDemand` already judges it (`fleet_autosizer_guards.go:224-237`). Renamed `growth_unserved_lanes_min` with the launch key; behaviour unchanged. The asymmetry is deliberate: a spurious HEAVY wave costs one paused probe tick, a spurious heavy purchase costs a seven-figure hull.

**Rechecked against the ruled clause 4, not inherited from the earlier analysis.** The flapping the ruling fixes entered through clause 4's dependence on the *instantaneous* balance, not through the lane signal — so hysteresis was never the remedy for it, and the high-water form removes it at the source. The one thing the new measure could contribute is a step: a sliding max decreases only when the window slides past its peak, so it is a slowly-decaying staircase rather than an oscillation, and it can cross the entry threshold at most once per window slide instead of twice per trade cycle. A streak on the predicate would still have to be held per-container by two coordinators that cannot share it, so the structural argument above is untouched and the answer stands.

### 2 — `growth_runway_milli_hours`: tunable, default 2000

**Tunable**, because RULINGS #5 as bounded (2026-07-25) asks "would an operator change this because of something they observed?" and the answer is plainly yes — "heavies are never affordable, the runway term is eating the treasury" is exactly the observation that drives a retune. The direct precedent has the same units, the same milli encoding, the same bounds and the same role one layer down: `capital_multiplier_k_milli`, default 2000, bounds 0–10000 (`container_ops_tune.go:146`).

**It is not a feature flag.** It ships at its default with the coordinator armed; there is no off-state that disables the coordinator and no arming step. Setting it to 0 returns the guard to exactly `ImmutableReserveFloor` — never below it — so RULINGS #4 holds at every setting.

**Value 2000** because it is the *same fleet's cargo runway*, measured over the *same window* by the *same ledger port*, as the drain's. Two spenders reserving different amounts of one observed quantity is the drift this design exists to remove. Note this is not double-reservation: each guard asks independently "after I buy this, is 2h of runway still left?", which is the correct semantics for both.

### 3 — `observed_typical_hold_fill_cost`: largest single cargo row in the same window

The term exists because term (a) under-reserves exactly when capacity was just added — a fresh hull has no spend history, so the observed runway does not contain it (spec §165). So the number must be **per-hull-scale and must not dilute as the fleet grows**.

Three candidate grains were checked against what the ledger actually exposes. `internal/domain/ledger/transaction.go:169-231` has `Amount`, `Timestamp`, `TransactionType`, `Category`, `Description`, `Metadata`, `RelatedEntityType`, `RelatedEntityID`, `OperationType` — **and no ship-symbol accessor**. `QueryOptions` (`ledger/ports.go:48-69`) cannot group. So per-hull and per-fill grains both require new query surface and an assumption about what `PURCHASE_CARGO` writes into `RelatedEntityID`, which is unverified.

**Chosen: `largestSingleFill` = max absolute `Amount()` over the `PURCHASE_CARGO` rows already being scanned for term (a).** It needs zero new schema knowledge, reuses the proven row handling (absolute value **per row**, so a stray positive row adds to measured outflow instead of cancelling real spend out of it — `adapters/parkedsensing/treasury_ports.go:58-79`), and a malformed row can only raise it, which makes the guard stricter. It behaves correctly at every boundary:

| State | term (a) runway | term (b) hold-fill | Dominant | Correct? |
|---|---|---|---|---|
| Fresh capacity: 1 hull spending, 5 trade hulls | 2 × outflow | 5 × largest fill | (b) | yes — 4 hulls are about to start buying |
| Mature: all 5 hulls spending | 2 × outflow | 5 × largest fill | (a) | yes — the committed spend is real |
| Cold start: no cargo rows at all | 0 | 0 | neither | yes — the guard is exactly `ImmutableReserveFloor` |
| Ledger unreadable | — | — | — | fail closed, no buy |

**Window:** the same one every other fleet-scale observation uses. `application/parkedsensing.cargoSpendLookback = time.Hour` (`buyports.go:53-55`) is unexported and the growth coordinator cannot import it, so Task 2 promotes it to `domain/fleetgrowth.TradeCycleWindow` and all three consumers — term (a), the hold-fill term and the high-water read — reference the one constant. The name states the property all three need: **at least one full trade cycle of history**, so a measurement taken mid-cycle is not a measurement of the cycle's phase.

### 4 — `contract_delivery` and `light`

Re-verified against main **after** the sp-tg9no slice-1 revert, so this is the pre-slice-1 contract-scaler shape:

- **`contract_delivery`** — no demand provider, never had one. `AddDemandProvider` has exactly two non-test call sites, Light (`fleet_autosizer_ports.go:49`) and Heavy (`:57`). Nothing to re-home; the contract scaler owns that fleet end to end. `hullbuy.HullClassContractDelivery` survives — load-bearing at `contract_scaler_ports.go:130` (`PriceFor`) and `:169` (`BuyAndHome`, dedicating to the exclusive `"contract"` fleet).
- **`light` demand** — structurally dead and dies with the autosizer in Plan B. Its chain count reads running `goods_factory_coordinator` containers (`fleet_autosizer_demand_ports.go:33`) and that type is in `retiredCommandTypes` (`daemon_server_recovery.go:27`, under "The factory-ops retirement"); `Vacancies` returns a hardcoded 0 (`:49-53`). **Plan A leaves it running untouched.**
- **`hullbuy.HullClassLight` the CONSTANT is load-bearing and the revert makes it more so, not less.** `contract_scaler_ports.go:188-191` drives `BuyHull` with it to buy "ONE UNDEDICATED light hull for a DEPOT role (warehouse/stocker)", relying on `hullbuy.DedicatedFleet(light) == ""` so the depot grower re-dedicates afterwards. The warehouse/stocker depot path is present on main today, so that consumer is live.
- **The open half — does anything want light demand back? NO, decided on code evidence.** `goods_factory_coordinator`, `siting_coordinator`, `worker_rebalancer_coordinator` and `manufacturing_coordinator` were retired *together* as one family. The gate factory is owned by the construction coordinator (`domain/manufacturing/gate/role.go:35`). Re-homing light demand would mean re-instating a retired coordinator family — a new epic, not a migration step.

### 5 — `growth_enabled`, and the deadlock the spec missed

**Inherited, renamed, same encoding, same semantics — plus one new obligation.**

The switch survives because the cost structure survives: `PriceFor` walks every system the fleet occupies × every SHIPYARD waypoint in it, and `resolveHullPrice` repeats that walk per alternative in `TradeHullPreferenceOrder`, all **before** `EvaluateGuards` can block (`run_fleet_autosizer_reconcile.go:132-157`). That belongs to price resolution, not to the autosizer.

**Renamed to `growth_enabled`** because knobs are registered per container type (`tunableKnobsByContainerType`, keyed `string(container.ContainerTypeFleetAutosizer)`). A knob on a new container type is a new key *whatever it is called*, so keeping the old name would be actively misleading. There is no live `sizing_enabled` override to preserve — the live override on staging is `expansion_enabled` (Task 6).

**Encoding 1=on / 2=off**, non-negotiable: `tune <key> 0` DELETES the key and means revert-to-default, so 0/1 makes "off" unexpressible. Both siblings say so in their registered descriptions.

**The new obligation.** `growth_enabled = 2` stops the growth coordinator's reads. If the drain still evaluated the wave and saw HEAVY, probe buying would pause **forever for a buyer that is switched off** — the sp-zg71k deadlock re-created through the master switch. So `growth_enabled` is the predicate's **first clause**: off ⇒ PROBE, unconditionally. This is the same rule `resolveHeavyCap`'s `!containerExists` rung already states — "no heavy buyer ⇒ nothing to save for" (`heavy_reserve_port.go:251`) — applied to the switch instead of the container. Pinned by a test in Task 4 and again in Task 6.

### 6 — `heavy_cap`: answered in code, and Plan A must *replace* the declaration, not add to it

sp-59cl1 is **merged into main** as `7698c79f` — note the gate rewrote the commit, so `f57aaa96` is not an ancestor of HEAD although the work is there. Read the merged version, not the bead's proposal.

Its actual mechanism, verified on main:

- `hullbuy.HeavyBuyerContainers() []container.ContainerType` (`hull_class.go:73-75`) declares which container types buy heavies. It currently returns `{ContainerTypeFleetAutosizer}`.
- `AutosizerCapPort` → `HeavyBuyerCapPort` (`heavy_reserve_port.go:279`). Its query fetches RUNNING/PENDING containers matching `container_type IN (declared) OR config LIKE '%heavy_cap%'`, ordered `CASE status WHEN 'RUNNING' THEN 0 ELSE 1 END, id ASC` (`:328-339`).
- `selectHeavyBuyer` (`:374-387`) scans that ordered list and **returns the first DECLARED owner**, ignoring any knob-carrying stranger it passed on the way. Only when no declared owner exists does the stranger win, loudly, through `warnUndeclaredHeavyBuyer`.
- The prefixed launch key is matched on the `_heavy_cap` **suffix** (`:486`), so the prefix travels with its owner.

**The consequence Plan A must act on.** `selectHeavyBuyer` returns the *first* declared owner in `RUNNING`-then-`id ASC` order. With **both** `ContainerTypeFleetAutosizer` and `ContainerTypeFleetGrowth` declared and both RUNNING, the winner is decided by container id — arbitrary, and quite possibly the autosizer, which after Task 4 no longer buys heavies. The withholder would then be reading a cap off a container the spender never consults. That is precisely the silently-divergent reserve spec step 5 exists to prevent, arriving one step earlier than the spec anticipated because Plan A stops before deletion.

**So Plan A REPLACES the declaration** — autosizer out, growth in — **in the same commit that switches the autosizer's heavy class off** (Task 4 Step 11b). Replacement is correct by the declaration's own documented semantics ("WHICH COORDINATORS BUY HEAVY HULLS"): the autosizer stops buying heavies at that commit. It is also quiet: with only growth declared, the scan passes the autosizer's still-present `heavy_cap` config as a knobbed stranger and *still* returns growth as authoritative, so no `warnUndeclaredHeavyBuyer` fires (`:347-355`).

**One deploy-window caveat, recorded in Task 4's deploy notes:** between the binary landing and the growth container being launched, no declared owner exists and the autosizer's leftover `heavy_cap` config makes it the knobbed stranger, so `sensing_heavy_cap_undeclared_buyer` WARNs each tick and the cap resolves off it. This is noise, not harm — the reserve it produces no longer reaches the drain's floor once Task 6 lands, and the wave port reports PROBE because no growth container exists. Launch the growth container promptly and the WARN stops.

### 7 — The wave metric: yes, and from both readers

The spec's precedent (`sizing_enabled` emitted on both paths every tick, `run_fleet_autosizer_reconcile.go:126-130`) argues for one always-present gauge, and that argument holds: a HEAVY wave and a stalled coordinator both look like "no probes bought", and an operator must read the state from a series rather than infer it from the absence of one.

But the wave needs more than that precedent gives. It is one definition read by **two coordinators on different tick periods**, and the failure this design most needs to detect is not "is the coordinator alive" — it is **"do the two consumers disagree?"** A single gauge written by the growth coordinator cannot see a drain that computed PROBE while the coordinator computed HEAVY.

**Decided:** one gauge `fleet_growth_wave`, labelled `player_id` **and `reader`** (`"growth"` | `"drain"`), 1 = HEAVY, 0 = PROBE, emitted every tick by both consumers on every path including the paused and error paths. Two series that must be identical; a divergence or a gap is visible directly. That is the runtime half of the lockstep test.

**Plus a companion:** `fleet_growth_wave_probe_reason`, always emitted by the growth coordinator, carrying which clause forced PROBE (`growth_disabled` | `lanes_unreadable` | `lanes_served` | `treasury_unreadable` | `unreachable` | `none`). Without it, "PROBE" has six meanings and the operator cannot tell "saving is impossible this era" from "the lane surface is down". This is the same pairing `autosizer_heavy_reserve_credits` / `..._target_credits` already ships for the same reason (`fleet_autosizer_metrics.go:68-77`).

---

## What has already landed (do not re-plan it)

| Spec step | Status |
|---|---|
| 4. Resolve the explorer bridge | **DONE, merged** (`ce82ab64`, sp-mn3it). The bridge is not retracted — it and every producer and its one consumer are **deleted**, so there is no sink left to latch. A structural guard sits at `cmd/spacetraders-daemon/off_gate_retired_test.go:25-35`. Plan A does not touch it; two guard gaps found during planning are recorded in Plan B preconditions. |
| 5. Re-home `heavy_cap` | **DONE, merged** as `7698c79f` (sp-59cl1). Plan A consumes it and makes one declaration change (open question 6). |
| Kept surface: the buy primitive | **DONE, merged** (sp-bs4oa). `HullClass` + constants, `DedicatedFleet`, `BuyOrder`, `BuyResult`, `YardPriceReader`, `DefaultHeavyCap` live in `internal/domain/hullbuy`; `fleet/commands` keeps type aliases (`fleet_autosizer_types.go:20-33`, `fleet_autosizer_act.go:77-79`, `run_fleet_autosizer_coordinator.go:35`). The concrete `autosizerPurchaser` stayed at `adapters/grpc/fleet_autosizer_ports.go:236` and is shared with the contract scaler. |

**Spec correction, for the record:** §347-362's "Kept surface" is written against the pre-sp-bs4oa tree. Its two dependencies are still real, but the first is discharged. Plan A adds a **third** kept dependency the spec does not list, and Plan A is what makes it true rather than hypothetical:

### The Kept surface as Plan A leaves it

1. **The hull-buy vocabulary** — safe in `domain/hullbuy`. Deleting the autosizer's files removes only the aliases.
2. **The concrete purchaser** — `autosizerPurchaser` at `adapters/grpc/fleet_autosizer_ports.go:236`. The contract scaler constructs its own (`contract_scaler_ports.go:73`) and drives it through `BuyAndHome` (`:166`, class `contract_delivery`) and `BuyHull` (`:188`, class `light`, deliberately untagged so the warehouse/stocker depot grower re-dedicates). **Rename the file and type in Plan B; never delete.**
3. **NEW — the purchase guard stack.** `fleet_autosizer_guards.go` (402 lines: `GuardName`, `PurchaseRequest`, `PurchaseDecision`, `EvaluateGuards`, six guards) is documented PURE and imports only `fmt`, `strings`, `common`. It is *fleet purchase guards*, not *autosizer guards*. **The growth coordinator lives in the same package (`internal/application/fleet/commands`) and reuses it directly.** This is the single most important scoping decision in this plan: the alternative — a growth coordinator with its own guard stack — is a second money-guard implementation, which is precisely the drift RULINGS #4 and the lockstep idiom exist to prevent. `fleet_autosizer_types.go`'s `ClassDemand`/`Shortfall` is kept for the same reason. Task 3 renames both files off the autosizer's name so Plan B's deletion cannot sweep them by pattern.
4. **The heavy-yard pricing errand** — moves to the growth coordinator in Task 5, intact.
5. **The stall escalation seam** — `fleet_autosizer_stall.go`, wired via `health.NewStallEscalator` (`fleet_autosizer_ports.go:120`). Reused by the growth coordinator; a coordinator that refuses every tick must not look identical to one with nothing to do.

---

## File Structure

### Created

| Path | Responsibility |
|---|---|
| `gobot/internal/application/common/growth_wave.go` | **The one wave definition.** `Wave`, `WaveInputs`, `WaveProbeReason`, `Reachable`, `DeriveWave`. Pure — no clock, no I/O, no stored state. Sits beside `heavy_reserve.go` because `Reachable` IS `HoldAt`, given the high-water balance instead of the live one. |
| `gobot/internal/domain/fleetgrowth/working_capital.go` | Pure working-capital math. Integer milli-hours, every factor clamped before the product, product before the divide. |
| `gobot/internal/domain/fleetgrowth/window.go` | `TradeCycleWindow` — the ONE trailing window all three fleet-scale observations are measured over. |
| `gobot/internal/application/trading/queries/unserved_lane_reader.go` | `UnservedLaneReader` — `profitable_lanes − trade_hulls`, floored at 0. Promoted out of `adapters/grpc`'s unexported `autosizerHeavySources` so ONE instance serves both wave consumers. |
| `gobot/internal/application/fleet/commands/run_fleet_growth_coordinator.go` | Command, handler, setters, `Handle` loop, per-container state. |
| `gobot/internal/application/fleet/commands/run_fleet_growth_reconcile.go` | Config resolution, `reconcileOnce`, `growth_enabled` master switch, zero-effect alarm. |
| `gobot/internal/application/fleet/commands/fleet_growth_act.go` | Tick inputs, wave evaluation, the heavy buy path. |
| `gobot/internal/application/fleet/commands/fleet_growth_tune.go` | `growth_enabled` + `heavy_cap` live knobs, `FleetGrowthTunableDefaults`. |
| `gobot/internal/adapters/grpc/container_ops_fleet_growth.go` | Launch path, config-key list, resolve/inject, recovery builder. |
| `gobot/internal/adapters/grpc/fleet_growth_ports.go` | Composition of every concrete port. |
| `gobot/internal/adapters/metrics/fleet_growth_metrics.go` | The wave gauges + the growth coordinator's own series. |
| `gobot/internal/adapters/parkedsensing/wave_port.go` | The drain's wave read: the heavy target, the lane count, the high-water treasury, the cap and `growth_enabled`, into one `DeriveWave` call. Every read behind it is local. |
| Test files beside each of the above | |

### Modified

| Path | Change |
|---|---|
| `.../fleet/commands/fleet_autosizer_guards.go` → `purchase_guards.go` | Renamed (Task 3). `PurchaseRequest` gains `WorkingCapital int64`; `guardAffordability` subtracts it. |
| `.../fleet/commands/fleet_autosizer_types.go` → `hull_classes.go` | Renamed (Task 3); package doc rewritten. |
| `.../fleet/commands/fleet_autosizer_stall.go` → `fleet_growth_stall.go` | Renamed; receiver retargeted (Task 4). |
| `.../fleet/commands/fleet_autosizer_heavy_pricing.go` → `heavy_pricing_errand.go` | Renamed; receiver retargeted (Task 5). |
| `.../fleet/commands/run_fleet_autosizer_coordinator.go:290-299` | `classDisabled` — the heavy class is switched OFF (Task 4). |
| `.../adapters/grpc/fleet_autosizer_demand_ports.go` | Heavy source delegates to the promoted reader; light sources untouched. |
| `gobot/internal/domain/ledger/ports.go` | Adds the `TreasuryHighWaterReader` narrow side port, beside `GateFeeAggregator` and for its stated reason. |
| `gobot/internal/adapters/persistence/transaction_repository.go` | Implements `TreasuryHighWaterSince` — `MAX(balance_after)` over the window, empty scanned into a slice so empty is never zero. |
| `gobot/internal/adapters/parkedsensing/treasury_ports.go` | `CargoSpendPort` gains `CargoOutflowSince` (sum **and** largest single row, one pass). |
| `gobot/internal/adapters/parkedsensing/heavy_reserve_port.go` | `HeavyBuyerCapPort` gains `GrowthEnabled` off the same container row. |
| `gobot/internal/domain/hullbuy/hull_class.go` | `HeavyBuyerContainers()` — **autosizer replaced by growth** (Task 4). |
| `gobot/internal/application/parkedsensing/buyports.go` | `BuyPorts.HeavyReserve` → a wave reader; `cargoSpendLookback` references `fleetgrowth.TradeCycleWindow`. |
| `gobot/internal/application/parkedsensing/buyqueue.go` | The wave gate; `heavyHold` leaves the `ProbeBuyFloor` capex term. |
| `gobot/internal/application/scouting/commands/probe_sensing_heartbeat.go`, `sensing_engine_ports.go` | A `held` reason for the HEAVY wave; the port swap. |
| `gobot/internal/domain/container/container.go` | `ContainerTypeFleetGrowth`. |
| `gobot/internal/adapters/grpc/command_factory_registry.go`, `container_ops_tune.go` | `fleet_growth` registry entry; `"growth"` operation alias + knob bounds. |
| `gobot/cmd/spacetraders-daemon/main.go` | Composition-root wiring for `UnservedLaneReader`, the growth coordinator, and the wave port. |

### Deleted by Plan A

Nothing. Plan A only adds, renames and rewires. Every deletion is Plan B's.

---

## Task order and why it is this order

| # | Task | Ordering constraint it satisfies |
|---|---|---|
| 1 | The wave predicate (pure) | Nothing depends on it yet; it is the definition both later consumers call. |
| 2 | The ledger observation surface: cargo outflow, high-water treasury, working capital | The reads and the pure math the predicate and the guard consume. One shared window. No wiring into a decision yet, so nothing here changes behaviour. |
| 3 | Shared unserved-lane read + the working-capital guard term + the two kept-surface renames | All touch live code and must be proven **behaviour-identical against the running autosizer** before anything is built on them. Nothing here changes a buying decision. |
| 4 | The growth coordinator; autosizer's heavy class OFF; `HeavyBuyerContainers` **replaced** — one commit | **Three facts must change atomically:** two heavy buyers must never run at once, and the cap declaration must follow the buyer or the withholder and the spender read different containers. Splitting this leaves a window where the reserve is computed against a cap nobody enforces. |
| 5 | Move the pricing errand | After 4, because an unpriced yard must have a buyer to become priceable *for*. |
| 6 | Wire the wave into the drain; remove the `expansion_enabled` override | **After 4** — pausing probes for a buyer that cannot yet buy is the deadlock. The override removal is the LAST step within the task: probe buying must not resume until the wave owns it. |

**Migration-step mapping:** spec step 1 → Tasks 1–4; step 2 → Task 5; step 3 → Task 6.

---
### Task 1: The wave predicate — one pure definition

**Files:**
- Create: `gobot/internal/application/common/growth_wave.go`
- Test: `gobot/internal/application/common/growth_wave_test.go`

**Interfaces:**
- Consumes: `common.HeavyReserveTarget` and its `HoldAt(int64) int64` (`heavy_reserve.go:59`, `:210`); `common.ImmutableReserveFloor` (`reserve_floor.go:11`).
- Produces: `common.Wave` (`WaveHeavy`, `WaveProbe`), `common.WaveInputs`, `common.WaveProbeReason` (`WaveProbeReasonNone`, `...GrowthDisabled`, `...LanesUnreadable`, `...LanesServed`, `...CapacityUnreadable`, `...Unreachable`), `common.Reachable(HeavyReserveTarget, int64) bool`, and `common.DeriveWave(WaveInputs) (Wave, WaveProbeReason)`. Task 4 and Task 6 both call `DeriveWave` and nothing else.

- [ ] **Step 1: Write the failing test**

Create `gobot/internal/application/common/growth_wave_test.go`:

```go
package common

import "testing"

func target(ask int64) HeavyReserveTarget {
	return HeavyReserve(HeavyReserveInputs{
		CapabilityOpen: ask > 0, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: ask,
	})
}

// heavyInputs is the canonical HEAVY state: lanes unserved, under cap, a priced target, and a
// high-water mark comfortably past the entry threshold.
func heavyInputs() WaveInputs {
	return WaveInputs{
		GrowthEnabled:         true,
		UnservedLanes:         3,
		UnservedLanesReadable: true,
		Target:                target(1_000_000),
		HighWaterTreasury:     2_000_000,
		HighWaterReadable:     true,
	}
}

func TestDeriveWave_AllClausesTrue_IsHeavy(t *testing.T) {
	w, reason := DeriveWave(heavyInputs())
	if w != WaveHeavy || reason != WaveProbeReasonNone {
		t.Fatalf("expected HEAVY with no probe reason, got %q/%q", w, reason)
	}
}

// The master switch is the FIRST clause. Off means there is no heavy buyer, so there is nothing
// to save for and probe buying must NOT be paused — pausing it for a switched-off buyer is a
// deadlock with no spender able to clear it.
func TestDeriveWave_GrowthDisabled_IsProbe(t *testing.T) {
	in := heavyInputs()
	in.GrowthEnabled = false
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonGrowthDisabled {
		t.Fatalf("expected PROBE/growth_disabled, got %q/%q", w, reason)
	}
}

func TestDeriveWave_LanesServed_IsProbe(t *testing.T) {
	in := heavyInputs()
	in.UnservedLanes = 0
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonLanesServed {
		t.Fatalf("expected PROBE/lanes_served, got %q/%q", w, reason)
	}
}

// Every unreadable input yields PROBE. PROBE authorises no spend of its own — the drain's floor,
// probe cap and the immutable reserve all still bind — so it is the release direction, matching
// HeavyReserve's documented treatment of a blind signal.
func TestDeriveWave_UnreadableInputs_AreProbe(t *testing.T) {
	cases := map[string]struct {
		mutate func(*WaveInputs)
		reason WaveProbeReason
	}{
		"lane surface down":     {func(in *WaveInputs) { in.UnservedLanesReadable = false }, WaveProbeReasonLanesUnreadable},
		"empty ledger window":   {func(in *WaveInputs) { in.HighWaterReadable = false }, WaveProbeReasonCapacityUnreadable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := heavyInputs()
			tc.mutate(&in)
			if w, reason := DeriveWave(in); w != WaveProbe || reason != tc.reason {
				t.Fatalf("expected PROBE/%q, got %q/%q", tc.reason, w, reason)
			}
		})
	}
}

// EMPTY IS NOT ZERO. A window with no ledger rows must arrive as unreadable, never as a
// high-water of 0 — a zero would read as a genuine "this fleet has never held money", which is
// a different and much stronger claim than "we could not see".
func TestDeriveWave_ZeroHighWaterIsNotTheSameAsUnreadable(t *testing.T) {
	in := heavyInputs()
	in.HighWaterTreasury, in.HighWaterReadable = 0, true
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
		t.Fatalf("a readable zero is a genuine unreachable, got %q/%q", w, reason)
	}
	in.HighWaterReadable = false
	if _, reason := DeriveWave(in); reason != WaveProbeReasonCapacityUnreadable {
		t.Fatalf("an unreadable window must be distinguishable from a readable zero, got %q", reason)
	}
}

// At or over the cap, and with no priced target, HeavyReserve already returns 0 — the predicate
// expresses those clauses THROUGH it rather than re-deriving them, so a change to the reserve's
// rungs can never leave the wave reading a stale rule.
func TestDeriveWave_ReserveRungsCarryTheCapAndPriceClauses(t *testing.T) {
	cases := map[string]HeavyReserveInputs{
		"at the cap":       {CapabilityOpen: true, HeaviesOwned: 5, HeavyCap: 5, TargetYardPrice: 1_000_000},
		"no priced target": {CapabilityOpen: true, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: 0},
		"no capability":    {CapabilityOpen: false, HeaviesOwned: 1, HeavyCap: 5, TargetYardPrice: 1_000_000},
	}
	for name, ri := range cases {
		t.Run(name, func(t *testing.T) {
			in := heavyInputs()
			in.Target = HeavyReserve(ri)
			if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
				t.Fatalf("expected PROBE/unreachable, got %q/%q", w, reason)
			}
		})
	}
}

// THE §395 ANTI-DEADLOCK REGRESSION, restated on the ruled measure. A fleet whose PEAK treasury
// across a full trade cycle is far below the entry threshold has not touched enough money to be
// credibly saving for this hull, and must not have its probe buying paused for it.
//
// This is a strictly stronger statement than the instantaneous form it replaces: that one asked
// "are we short at this instant", which a fleet that is genuinely rich fails several times an
// hour purely on trade-cycle phase.
func TestDeriveWave_PeakFarBelowEntry_DoesNotPauseProbes(t *testing.T) {
	in := heavyInputs()
	in.Target = target(1_916_613) // entry = 958,306; engagement needs 1,008,306
	in.HighWaterTreasury = 400_000
	if w, reason := DeriveWave(in); w != WaveProbe || reason != WaveProbeReasonUnreachable {
		t.Fatalf("an unreachable heavy must leave probe buying running: got %q/%q", w, reason)
	}
}

// THE PROPERTY THE RULING BUYS: the mode is CONSTANT across a trade cycle. The live balance
// swings across the whole observed band while the high-water mark — the peak of that same band —
// does not move, so every point in the cycle yields the same regime. A predicate that answered
// differently at the trough than at the peak would be reporting phase, not economics.
func TestDeriveWave_ModeIsConstantAcrossTheTradeCycle(t *testing.T) {
	reachable := heavyInputs()
	reachable.Target = target(1_916_613)
	reachable.HighWaterTreasury = 1_500_000 // the band's peak clears floor + entry

	unreachable := reachable
	unreachable.HighWaterTreasury = 400_000

	// Sweeping the live balance must change NOTHING: it is not an input to the predicate at all,
	// which is precisely what removes the flapping. The sweep is the falsifier — a predicate that
	// reintroduced a live-balance term would break here and nowhere else.
	for live := int64(119_000); live <= 1_500_000; live += 37_313 {
		reachable.LiveTreasuryForReporting = live
		unreachable.LiveTreasuryForReporting = live
		if w, _ := DeriveWave(reachable); w != WaveHeavy {
			t.Fatalf("reachable fleet flipped to %q at live balance %d", w, live)
		}
		if w, _ := DeriveWave(unreachable); w != WaveProbe {
			t.Fatalf("unreachable fleet flipped to %q at live balance %d", w, live)
		}
	}
}

// LOCKSTEP: the reachability clause must be HoldAt with a different balance, never a copy of its
// arithmetic. A second derivation would pass every case above and still drift the moment HoldAt's
// entry computation changes, so this pins the two against each other across the whole boundary.
func TestDeriveWave_ReachabilityIsHoldAtOnTheHighWater(t *testing.T) {
	tgt := target(1_000_000)
	for hw := int64(0); hw <= 2_000_000; hw += 9_973 {
		in := WaveInputs{
			GrowthEnabled: true, UnservedLanes: 1, UnservedLanesReadable: true,
			Target: tgt, HighWaterTreasury: hw, HighWaterReadable: true,
		}
		w, _ := DeriveWave(in)
		wantHeavy := tgt.HoldAt(hw) > 0
		if (w == WaveHeavy) != wantHeavy {
			t.Fatalf("wave and HoldAt disagree at high-water %d: wave=%q HoldAt=%d", hw, w, tgt.HoldAt(hw))
		}
		if Reachable(tgt, hw) != wantHeavy {
			t.Fatalf("Reachable disagrees with HoldAt at high-water %d", hw)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/common/ -run TestDeriveWave -v 2>&1 | tail -20
```

Expected: FAIL — `undefined: DeriveWave`, `undefined: WaveInputs`, `undefined: WaveHeavy`.

- [ ] **Step 3: Write the implementation**

Create `gobot/internal/application/common/growth_wave.go`:

```go
package common

// Wave is the regime one tick is in: HEAVY buys trade capacity, PROBE buys sensing coverage.
// It is DERIVED on every tick by every consumer and never stored, so a restart re-derives it
// from durable facts, no cursor can go stale, and there is no re-fire hazard.
type Wave string

const (
	// WaveHeavy pauses probe buying so the treasury can reach a heavy hull's ask.
	WaveHeavy Wave = "heavy"
	// WaveProbe runs probe buying at full speed. It AUTHORISES nothing: the drain's own floor,
	// its probe cap and the immutable reserve all still bind.
	WaveProbe Wave = "probe"
)

// WaveProbeReason names which clause forced PROBE. It exists because PROBE otherwise has one
// name and several meanings, and an operator watching probe buying continue cannot tell "this
// fleet cannot plausibly reach a hull" from "the lane surface is down" without it.
type WaveProbeReason string

const (
	WaveProbeReasonNone               WaveProbeReason = ""
	WaveProbeReasonGrowthDisabled     WaveProbeReason = "growth_disabled"
	WaveProbeReasonLanesUnreadable    WaveProbeReason = "lanes_unreadable"
	WaveProbeReasonLanesServed        WaveProbeReason = "lanes_served"
	WaveProbeReasonCapacityUnreadable WaveProbeReason = "capacity_unreadable"
	WaveProbeReasonUnreachable        WaveProbeReason = "unreachable"
)

// Reachable answers whether a fleet can PLAUSIBLY reach an ask, judged on the highest balance it
// has held across a full trade cycle rather than on the balance it happens to hold right now.
//
// WHY THE PEAK AND NOT THE LIVE BALANCE. A trading fleet's treasury oscillates by more than a
// hull's price within a single cycle as hulls buy and sell cargo, so an instantaneous balance
// reports WHERE IN THE CYCLE THE TICK LANDED, not what the fleet can afford. A regime chosen from
// that flips with every tour — and worse, flips the wrong way, since treasury peaks just after a
// sale and troughs once the money is committed to cargo. The peak across a cycle is the same
// number wherever in the cycle it is sampled, which is the whole property.
//
// IT IS HoldAt, GIVEN A DIFFERENT BALANCE — never a copy of its arithmetic. HoldAt already means
// "surplus above the immutable floor, less the entry share of the ask", so the entry computation,
// its overflow-safe two-term form and every rung of HeavyReserve above it are reused rather than
// restated. The two calls ask genuinely different questions and both are correct:
//
//	HoldAt(liveTreasury)      — how much may I withhold AT THIS INSTANT? A question about the
//	                            moment, and the right one for a withholding decision.
//	Reachable(t, highWater)   — is this ask reachable BY THIS FLEET? A question about capacity,
//	                            and the right one for choosing a regime.
//
// It authorises no spend. Every purchase remains gated by the full fail-closed guard stack
// against the LIVE balance, so an over-stated capacity can only ever pause probe buying — never
// approve a hull the fleet cannot pay for.
func Reachable(t HeavyReserveTarget, highWaterTreasury int64) bool {
	return t.HoldAt(highWaterTreasury) > 0
}

// WaveInputs are the facts the predicate judges. Every one is read fresh by the caller on the
// tick that asks; nothing here is remembered between ticks.
type WaveInputs struct {
	// GrowthEnabled is the heavy buyer's master switch as read this tick. Off means no heavy
	// buyer exists, which is why it is the first clause: pausing probe buying to save for a
	// purchase nothing can make is a deadlock no spender is able to clear.
	GrowthEnabled bool

	// UnservedLanes is the profitable, feasible lane count beyond the current trade pool — the
	// capacity-short signal. UnservedLanesReadable=false means the surface could not be read,
	// which yields PROBE rather than a guess.
	UnservedLanes         int
	UnservedLanesReadable bool

	// Target is what the fleet would be saving toward, from HeavyReserve — the ONE definition.
	// The cap clause, the priced-target clause and the capability clause are all expressed
	// through it rather than beside it, so they cannot drift from the reservation's own rungs.
	Target HeavyReserveTarget

	// HighWaterTreasury is the highest balance held across a full trade cycle — the fleet's
	// demonstrated capacity, and the balance the reachability clause judges.
	//
	// HighWaterReadable=false means the window could not be read or held no rows. EMPTY IS NOT
	// ZERO: a zero high-water is the strong claim "this fleet has never held money", which is a
	// genuine unreachable, while an unreadable window is "we could not see". Collapsing them
	// would report a blind read as a verdict.
	HighWaterTreasury int64
	HighWaterReadable bool

	// LiveTreasuryForReporting is carried for the decision log and the gauges ONLY. It is
	// deliberately NOT an input to any clause: a live balance here is what made the regime a
	// function of trade-cycle phase, and the name is what stops it being wired back in.
	LiveTreasuryForReporting int64
}

// DeriveWave is the ONE definition of the regime. The growth coordinator's heavy buyer and the
// sensing probe drain both call THIS function; a second derivation in either would let one spend
// while the other withholds, which is the split-brain the whole design exists to prevent.
//
// EVERY "CANNOT" ANSWER IS PROBE. That is the release direction, matching HeavyReserve's own
// documented treatment of a blind signal: PROBE withholds nothing and authorises nothing, so a
// wrong PROBE costs a deferred purchase, while a wrong HEAVY pauses coverage growth against a
// target nobody can justify.
//
// Pure: no clock, no I/O, no stored state — inputs to a regime.
func DeriveWave(in WaveInputs) (Wave, WaveProbeReason) {
	if !in.GrowthEnabled {
		return WaveProbe, WaveProbeReasonGrowthDisabled
	}
	if !in.UnservedLanesReadable {
		return WaveProbe, WaveProbeReasonLanesUnreadable
	}
	if in.UnservedLanes <= 0 {
		return WaveProbe, WaveProbeReasonLanesServed
	}
	if !in.HighWaterReadable {
		return WaveProbe, WaveProbeReasonCapacityUnreadable
	}
	if !Reachable(in.Target, in.HighWaterTreasury) {
		return WaveProbe, WaveProbeReasonUnreachable
	}
	return WaveHeavy, WaveProbeReasonNone
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/common/ -run 'TestDeriveWave|TestHeavyReserve' -v 2>&1 | tail -30
go vet ./internal/application/common/ && gofmt -l internal/application/common/
```

Expected: all PASS; `gofmt` prints nothing.

- [ ] **Step 5: Mutation probes — prove the tests actually hold the predicate**

Each probe must NAME the tests it kills. Run each as ONE atomic invocation (patch, verify the patch applied, test, restore), and **commit first** so a failed restore is recoverable.

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
F=internal/application/common/growth_wave.go
cp $F /tmp/growth_wave.orig && \
sed -i '' 's|if !in.GrowthEnabled {|if false \&\& !in.GrowthEnabled {|' $F && \
grep -q 'if false && !in.GrowthEnabled' $F && echo "MUTATION APPLIED" && \
go test ./internal/application/common/ -run TestDeriveWave 2>&1 | grep -E '^(---|FAIL|ok)' ; \
cp /tmp/growth_wave.orig $F && go test ./internal/application/common/ -run TestDeriveWave 2>&1 | tail -2
```

Expected: `MUTATION APPLIED`, then `--- FAIL: TestDeriveWave_GrowthDisabled_IsProbe`, then `ok` after restore. A run that names ZERO tests is an infrastructure failure, not a passing probe.

Repeat with these, each atomically:

- `s|if in.UnservedLanes <= 0 {|if in.UnservedLanes < 0 {|` → must kill `TestDeriveWave_LanesServed_IsProbe`.
- `s|if !in.HighWaterReadable {|if false {|` → must kill `TestDeriveWave_UnreadableInputs_AreProbe` and `TestDeriveWave_ZeroHighWaterIsNotTheSameAsUnreadable`. This is the EMPTY-IS-NOT-ZERO probe.
- `s|return t.HoldAt(highWaterTreasury) > 0|return int64(t) > 0|` in `Reachable` → must kill `TestDeriveWave_PeakFarBelowEntry_DoesNotPauseProbes` **and** `TestDeriveWave_ReachabilityIsHoldAtOnTheHighWater`. This is the plausible "simplification" a future reader could make, and the lockstep test is what refuses it.
- **The ruling's own probe.** `s|Reachable(in.Target, in.HighWaterTreasury)|Reachable(in.Target, in.LiveTreasuryForReporting)|` → must kill `TestDeriveWave_ModeIsConstantAcrossTheTradeCycle`. **This is the probe that matters most in the whole plan**: it re-creates the exact defect the ruling fixes, and the constancy sweep is the only test that catches it. If it kills nothing, the sweep is not sweeping and must be fixed before proceeding.

- [ ] **Step 6: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
git add internal/application/common/growth_wave.go internal/application/common/growth_wave_test.go
git commit --no-verify -m "feat(common): the wave predicate — one derived-per-tick definition of the heavy/probe regime

Sits beside HeavyReserve and expresses its cap, priced-target and reachability
clauses THROUGH HoldAt rather than beside it, so the two cannot drift.

REACHABILITY IS JUDGED ON THE HIGH-WATER BALANCE, not the live one. A trading
fleet's treasury swings by more than a hull's price within one trade cycle, so an
instantaneous balance reports where in the cycle the tick landed rather than what
the fleet can afford — and reports it inverted, since treasury peaks just after a
sale and troughs once the money is committed to cargo. Reachable() is HoldAt given
a different balance: same entry arithmetic, a different question. HoldAt(live) asks
how much may be withheld at this instant; Reachable asks whether this fleet can
reach the ask at all.

The live balance is still carried, named LiveTreasuryForReporting so it cannot be
wired back into a clause by accident, and a sweep across the observed band asserts
the mode is CONSTANT — the property the whole design buys.

Every unreadable input yields PROBE (the release direction: PROBE authorises no
spend). An empty ledger window is unreadable, never a zero high-water. growth_enabled
is the first clause so a switched-off buyer can never pause probe buying. Pure;
nothing stored (RULINGS #2). 5 mutation probes, each naming its kills."
```

---

### Task 2: The ledger observation surface — cargo outflow, high-water treasury, working capital

**Files:**
- Create: `gobot/internal/domain/fleetgrowth/working_capital.go` (+ test)
- Create: `gobot/internal/domain/fleetgrowth/window.go` (`TradeCycleWindow`)
- Modify: `gobot/internal/domain/ledger/ports.go` (add the `TreasuryHighWaterReader` narrow side port)
- Modify: `gobot/internal/adapters/persistence/transaction_repository.go` (implement it)
- Test: `gobot/internal/adapters/persistence/transaction_repository_test.go`
- Modify: `gobot/internal/application/parkedsensing/buyports.go:53-55` (reference the shared window)
- Modify: `gobot/internal/adapters/parkedsensing/treasury_ports.go` (add `CargoOutflowSince`)
- Test: `gobot/internal/adapters/parkedsensing/treasury_ports_test.go`

**Why one task and not two.** Everything here is the growth coordinator's *observation of the ledger*: two reads over the same table, one shared window constant, and the pure math they feed. Splitting the high-water read into its own task would give two tasks touching the same repository, the same model and the same window — and a reviewer could not meaningfully approve one while rejecting the other. It is a larger task than it was before the ruling; that is the re-cost.

**Interfaces:**
- Consumes: `ledger.TransactionRepository.FindByPlayer`, `ledger.TransactionTypePurchaseCargo`, `ledger.QueryOptions` (`internal/domain/ledger/ports.go:48`); `common.ImmutableReserveFloor`; the `GateFeeAggregator` narrow-side-port precedent (`ledger/ports.go:26-45`) and its implementation shape (`transaction_repository.go:124-156`).
- Produces:
  - `fleetgrowth.TradeCycleWindow` (a `time.Duration` const).
  - `fleetgrowth.WorkingCapital(runwayMilliHours int, cargoOutflow int64, tradeHulls int, largestSingleFill int64) int64`.
  - `ledger.TreasuryHighWaterReader` with `TreasuryHighWaterSince(ctx, playerID shared.PlayerID, since time.Time) (highWater int64, readable bool, err error)` — Task 4 and Task 6 both consume it, through ONE instance built at the composition root.
  - `(*parkedsensing.CargoSpendPort).CargoOutflowSince(ctx, playerID int, since time.Time) (total int64, largestSingle int64, err error)` — Task 4 consumes this through a `CargoOutflowReader` interface it declares.

- [ ] **Step 1: Write the failing test for the pure math**

Create `gobot/internal/domain/fleetgrowth/working_capital_test.go`:

```go
package fleetgrowth

import "testing"

// The runway term dominates on a busy fleet whose hulls all have spend history.
func TestWorkingCapital_RunwayDominates(t *testing.T) {
	// 2000 milli-hours of a 500_000 outflow = 1_000_000; hold-fill = 5 × 80_000 = 400_000.
	got := WorkingCapital(2000, 500_000, 5, 80_000)
	if got != 1_000_000 {
		t.Fatalf("expected the runway term to dominate at 1000000, got %d", got)
	}
}

// The hold-fill term dominates exactly when capacity was just added: the fresh hulls have no
// spend history, so the observed runway does not yet contain them.
func TestWorkingCapital_HoldFillDominatesOnFreshCapacity(t *testing.T) {
	// 2000 milli-hours of a 100_000 outflow = 200_000; hold-fill = 5 × 80_000 = 400_000.
	got := WorkingCapital(2000, 100_000, 5, 80_000)
	if got != 400_000 {
		t.Fatalf("expected the hold-fill term to dominate at 400000, got %d", got)
	}
}

// A cold fleet has observed nothing, so it reserves nothing above the immutable floor. This is
// the case that proves the formula adds a term rather than replacing the floor.
func TestWorkingCapital_NothingObservedReservesNothing(t *testing.T) {
	if got := WorkingCapital(2000, 0, 0, 0); got != 0 {
		t.Fatalf("expected 0 working capital with nothing observed, got %d", got)
	}
}

// A malformed observation may only ever make the guard STRICTER. Every factor is clamped
// non-negative BEFORE the product is formed, so a negative multiplier and a negative outflow
// cannot multiply into a phantom windfall (RULINGS #4).
func TestWorkingCapital_MalformedInputsCannotWeakenTheGuard(t *testing.T) {
	cases := map[string]int64{
		"negative runway multiplier": WorkingCapital(-9000, 500_000, 5, 80_000),
		"negative outflow":           WorkingCapital(2000, -500_000, 5, 80_000),
		"both negative":              WorkingCapital(-9000, -500_000, 5, 80_000),
		"negative hull count":        WorkingCapital(0, 0, -5, 80_000),
		"negative fill":              WorkingCapital(0, 0, 5, -80_000),
	}
	for name, got := range cases {
		if got < 0 {
			t.Fatalf("%s: working capital went negative (%d) — that would LOWER the floor", name, got)
		}
	}
	if got := WorkingCapital(-9000, -500_000, 5, 80_000); got != 400_000 {
		t.Fatalf("clamping must leave the surviving term intact, got %d", got)
	}
}

// The product is formed BEFORE the divide, so a sub-hour runway cannot round to zero.
func TestWorkingCapital_SubHourRunwayDoesNotRoundAway(t *testing.T) {
	if got := WorkingCapital(400, 500_000, 0, 0); got != 200_000 {
		t.Fatalf("expected 0.4h of a 500000 outflow = 200000, got %d", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/domain/fleetgrowth/ -v 2>&1 | tail -10
```

Expected: FAIL — no such package / `undefined: WorkingCapital`.

- [ ] **Step 3: Write the pure math**

Create `gobot/internal/domain/fleetgrowth/working_capital.go`:

```go
// Package fleetgrowth holds the pure money math the fleet-growth coordinator judges a heavy
// purchase against. No clock, no I/O, no stored state — observations to a credit count.
package fleetgrowth

// WorkingCapital is the credits a heavy purchase must leave behind ON TOP OF the immutable
// reserve floor. It is a term ABOVE that floor and never a replacement for it, so no money
// guard is relaxed by adding it (RULINGS #4).
//
//	working_capital = max(
//	    runwayMilliHours × cargoOutflow / 1000,
//	    tradeHulls × largestSingleFill,
//	)
//
// TWO TERMS, BECAUSE ONE OF THEM IS BLIND EXACTLY WHEN IT MATTERS. The runway term asks what
// the fleet is already committed to spending: the busier it is, the more of the treasury is
// spoken for, and buying a hull out of money the trading fleet is about to spend on cargo
// stalls the trades that fund everything else. But a hull bought a minute ago has no spend
// history, so the observed runway does not contain it — and that is precisely the moment
// capacity was added. The hold-fill term is per-hull-scale by construction and therefore does
// not dilute as the pool grows, which is what makes it bind on fresh capacity.
//
// MILLI-HOURS AND INTEGERS THROUGHOUT. Sub-hour runway is the operating range, and a float
// would put NaN inside a money guard, where it fails every comparison and passes every clamp.
// Integer milli cannot express NaN at all, so the failure mode does not exist rather than
// being defended against.
//
// FAILS STRICTER. Every factor is clamped non-negative BEFORE its product is formed, so a
// negative multiplier and a negative observation cannot multiply into a phantom windfall; the
// product is formed before the divide so a legitimate sub-hour reserve cannot round to zero.
// A malformed observation can therefore only ever raise this number.
func WorkingCapital(runwayMilliHours int, cargoOutflow int64, tradeHulls int, largestSingleFill int64) int64 {
	runway := maxInt64(0, int64(runwayMilliHours)) * maxInt64(0, cargoOutflow) / 1000
	holdFill := maxInt64(0, int64(tradeHulls)) * maxInt64(0, largestSingleFill)
	return maxInt64(runway, holdFill)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/domain/fleetgrowth/ -v 2>&1 | tail -15
```

Expected: all PASS.

- [ ] **Step 5: Give the shared observation window one home and one name**

Create `gobot/internal/domain/fleetgrowth/window.go`:

```go
package fleetgrowth

import "time"

// TradeCycleWindow is the trailing window every fleet-scale money observation is measured over.
//
// ONE CONSTANT, THREE CONSUMERS, and they all need the same physical property: at least one full
// trade cycle of history, so that a measurement taken mid-cycle is a measurement of the FLEET and
// not of the cycle's phase. The cargo-runway term needs it to be a rate rather than a snapshot;
// the hold-fill term needs it to have seen a representative purchase; the high-water treasury
// needs it to span a peak.
//
// THE SIZING CRITERION IS A PROPERTY, NOT A NUMBER: a peak lands inside the window only if the
// trade cycle is no longer than the window. Size it below the cycle and the high-water mark
// itself starts oscillating, which puts back exactly the flapping the reachability clause was
// changed to remove. Two differently-sized windows over one fleet is the same failure a step
// earlier — two spenders measuring one quantity and disagreeing about it.
const TradeCycleWindow = time.Hour
```

Then edit `gobot/internal/application/parkedsensing/buyports.go:53-55`, replacing the local const with a reference:

```go
// cargoSpendLookback names the shared observation window for call-site readability. The value is
// not this package's to choose: the fleet-growth coordinator measures the same fleet's cargo
// outflow over the same window, and a window that differed between them would have two money
// guards reserving against two different measurements of one quantity.
const cargoSpendLookback = fleetgrowth.TradeCycleWindow
```

Add the `internal/domain/fleetgrowth` import to that file. Leave `domain/parkedsensing/floor.go` alone — `ProbeBuyFloor` takes the rate as a parameter and never sees the window, so the window was never that package's.

- [ ] **Step 6: Write the failing test for the two-statistic ledger read**

Add to `gobot/internal/adapters/parkedsensing/treasury_ports_test.go` (create the file if absent; follow the existing fake-repository idiom in that package's tests):

```go
// CargoOutflowSince returns BOTH statistics from ONE pass over the same rows: the sum the
// runway term needs and the largest single row the hold-fill term needs. Two queries would be
// two chances for the two terms to see different windows.
func TestCargoOutflowSince_ReturnsSumAndLargestFromOnePass(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000, -80_000, -10_000}}
	port := NewCargoSpendPort(repo)

	total, largest, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 130_000 {
		t.Fatalf("expected the absolute sum 130000, got %d", total)
	}
	if largest != 80_000 {
		t.Fatalf("expected the largest single row 80000, got %d", largest)
	}
	if repo.calls != 1 {
		t.Fatalf("both statistics must come from ONE query, got %d", repo.calls)
	}
}

// A stray POSITIVE row (a refund, a correction) must ADD to measured outflow rather than
// cancel real spend out of it, and must be able to raise the largest-single figure — a
// malformed row may only ever make the guard stricter.
func TestCargoOutflowSince_StrayPositiveRowMakesTheGuardStricter(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000, 90_000}}
	port := NewCargoSpendPort(repo)

	total, largest, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 130_000 {
		t.Fatalf("a positive row must add, not cancel: got %d", total)
	}
	if largest != 90_000 {
		t.Fatalf("a positive row must be able to raise the largest single figure: got %d", largest)
	}
}

// No rows in the window is a genuine zero, not an error: a fleet that has bought no cargo
// reserves nothing above the immutable floor.
func TestCargoOutflowSince_NoRowsIsZero(t *testing.T) {
	port := NewCargoSpendPort(&fakeTxnRepo{})
	total, largest, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil || total != 0 || largest != 0 {
		t.Fatalf("expected (0,0,nil), got (%d,%d,%v)", total, largest, err)
	}
}
```

- [ ] **Step 7: Run it to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/adapters/parkedsensing/ -run TestCargoOutflowSince -v 2>&1 | tail -10
```

Expected: FAIL — `port.CargoOutflowSince undefined`.

- [ ] **Step 8: Implement the two-statistic read**

Add to `gobot/internal/adapters/parkedsensing/treasury_ports.go`, beside `AbsCargoBuySpendSince`:

```go
// CargoOutflowSince reports the trading fleet's cargo outflow since `since` as BOTH statistics
// the working-capital formula needs, from ONE pass over one row set: the absolute sum, and the
// largest single row.
//
// ONE QUERY BECAUSE THEY MUST SEE THE SAME ROWS. The two terms of the working-capital formula
// are a max() over each other, and computing them from two reads would let a row land between
// them and make the comparison meaningless.
//
// THE LARGEST SINGLE ROW IS THE PER-HULL SCALE. The ledger carries no ship symbol, so a
// per-hull mean is not derivable here; the largest observed cargo purchase is, it does not
// dilute as the pool grows, and an outlier can only raise it — which makes the guard stricter,
// the only direction a money guard may move.
//
// Ledger expenses are stored NEGATIVE and the absolute value of each row is taken
// individually, so a stray positive row still ADDS to measured outflow instead of cancelling
// real spend out of it.
func (p *CargoSpendPort) CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (int64, int64, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, 0, err
	}
	cargo := ledger.TransactionTypePurchaseCargo
	rows, err := p.txns.FindByPlayer(ctx, pid, ledger.QueryOptions{
		TransactionType: &cargo,
		StartDate:       &since,
		Limit:           cargoSpendScan,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read recent cargo outflow: %w", err)
	}
	var total, largest int64
	for _, row := range rows {
		amount := int64(row.Amount())
		if amount < 0 {
			amount = -amount
		}
		total += amount
		if amount > largest {
			largest = amount
		}
	}
	return total, largest, nil
}
```

- [ ] **Step 9: Write the failing test for the high-water read**

Add to `gobot/internal/adapters/persistence/transaction_repository_test.go`, matching the fixture idiom already in that file.

```go
// The high-water mark is the PEAK balance across the window — not the latest, and not an
// average. It is what makes the reachability clause independent of where in a trade cycle the
// tick landed.
func TestTreasuryHighWaterSince_ReturnsThePeakNotTheLatest(t *testing.T) {
	repo := seedTransactions(t,
		txAt(hoursAgo(0.9), 300_000),
		txAt(hoursAgo(0.5), 1_500_000), // the peak
		txAt(hoursAgo(0.1), 119_000),   // the latest
	)
	hw, readable, err := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if err != nil || !readable {
		t.Fatalf("expected a readable peak, got readable=%v err=%v", readable, err)
	}
	if hw != 1_500_000 {
		t.Fatalf("expected the peak 1500000, got %d", hw)
	}
}

// EMPTY IS NOT ZERO, and this repo has been bitten by exactly this three times. A window with no
// rows must report UNREADABLE — a zero would be the strong claim "this fleet has never held
// money", which the predicate would correctly treat as a genuine unreachable.
func TestTreasuryHighWaterSince_EmptyWindowIsUnreadableNotZero(t *testing.T) {
	repo := seedTransactions(t) // no rows
	hw, readable, err := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("an empty window is not an error: %v", err)
	}
	if readable {
		t.Fatalf("an empty window must be UNREADABLE, got readable with high-water %d", hw)
	}
}

// A genuine zero balance in the window is READABLE. The two cases must stay distinguishable, or
// a blind read reports as a verdict.
func TestTreasuryHighWaterSince_GenuineZeroIsReadable(t *testing.T) {
	repo := seedTransactions(t, txAt(hoursAgo(0.5), 0))
	hw, readable, err := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if err != nil || !readable || hw != 0 {
		t.Fatalf("expected a readable 0, got (%d,%v,%v)", hw, readable, err)
	}
}

// Rows outside the window are not the fleet's current capacity. A windfall two cycles ago must
// age out, or the mark never decays and the fleet is judged on history it has spent.
func TestTreasuryHighWaterSince_IgnoresRowsOutsideTheWindow(t *testing.T) {
	repo := seedTransactions(t,
		txAt(hoursAgo(3), 9_000_000), // long gone
		txAt(hoursAgo(0.5), 400_000),
	)
	hw, readable, _ := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if !readable || hw != 400_000 {
		t.Fatalf("expected the in-window peak 400000, got %d readable=%v", hw, readable)
	}
}

// Scoped per player, like every other read on this table.
func TestTreasuryHighWaterSince_IsPlayerScoped(t *testing.T) {
	repo := seedTransactionsForPlayers(t,
		playerTx(player, hoursAgo(0.5), 400_000),
		playerTx(otherPlayer, hoursAgo(0.5), 9_000_000),
	)
	hw, _, _ := repo.TreasuryHighWaterSince(ctx(), player, time.Now().Add(-time.Hour))
	if hw != 400_000 {
		t.Fatalf("another player's balance leaked in: got %d", hw)
	}
}
```

- [ ] **Step 10: Run it, then implement the narrow side port**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/adapters/persistence/ -run TestTreasuryHighWaterSince -v 2>&1 | tail -10
```

Expected: FAIL — `TreasuryHighWaterSince` undefined.

Add to `gobot/internal/domain/ledger/ports.go`, beside `GateFeeAggregator` and following its documented reasoning verbatim in shape:

```go
// TreasuryHighWaterReader reports the highest balance a player held across a trailing window —
// the fleet's demonstrated capacity, as opposed to the balance it happens to hold right now.
//
// DELIBERATELY SEPARATE FROM TransactionRepository, though the same GORM type satisfies both.
// This is a single-purpose analytical read, and folding it into the broad repository interface
// would oblige roughly a dozen unrelated test fakes to grow a method none of them will ever call.
// The narrow port keeps the cost of the read with the code that wants it.
//
// EMPTY IS NOT ZERO, and readable is what carries the difference. A window with no rows means the
// capacity could not be observed; a window whose rows peak at zero means the fleet genuinely held
// nothing. Collapsing them would report a blind read as a verdict, which in a money guard is the
// failure that matters.
//
// It reads balance_after, the same column the live-treasury guard already serves from. A MAX is
// robust to the ledger's one documented drift mode, which runs downward; the residual it does
// accept is an unrecorded spend leaving the mark stale-HIGH for the life of the window. That
// residual authorises no spend — every purchase is still gated against the live balance — so it
// can only ever cost a paused pass, never approve a hull the fleet cannot pay for.
type TreasuryHighWaterReader interface {
	TreasuryHighWaterSince(ctx context.Context, playerID shared.PlayerID, since time.Time) (highWater int64, readable bool, err error)
}
```

Implement it on `*GormTransactionRepository` in `transaction_repository.go`, beside `PerOriginGateFees` and in the same style:

```go
func (r *GormTransactionRepository) TreasuryHighWaterSince(
	ctx context.Context, playerID shared.PlayerID, since time.Time,
) (int64, bool, error) {
	// Scanned into a SLICE, never a bare int64. Scan into a scalar returns (zero, nil) on an
	// empty result, which here would report a fleet that has never traded as one that has never
	// held a credit — a blind read wearing a verdict's clothing.
	var peaks []int64
	err := r.db.WithContext(ctx).Model(&TransactionModel{}).
		Select("MAX(balance_after)").
		Where("player_id = ?", playerID.Value()).
		// `timestamp`, not created_at: it is the half of idx_player_timestamp that bounds this
		// scan, and it is the column every other windowed read on this table already uses.
		Where("timestamp >= ?", since).
		Scan(&peaks).Error
	if err != nil {
		return 0, false, fmt.Errorf("failed to read treasury high-water: %w", err)
	}
	if len(peaks) == 0 {
		return 0, false, nil
	}
	return peaks[0], true, nil
}
```

**Verify the empty case against the real driver, not just a fake.** `MAX()` over zero rows returns one row containing SQL NULL, not zero rows — confirm which shape GORM hands back here and make `readable=false` cover it either way. Getting this wrong is the exact bug the three "EMPTY IS NOT ZERO" comments in this repo were written after.

- [ ] **Step 11: Run the full affected suites**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go build ./... && go vet ./... && gofmt -l . | head
go test ./internal/domain/fleetgrowth/ ./internal/domain/ledger/ ./internal/adapters/persistence/ ./internal/adapters/parkedsensing/ ./internal/application/parkedsensing/ 2>&1 | grep -Ev '^(ok|\?)' | head -20
```

Expected: build/vet clean, `gofmt` silent, no failures. The `cargoSpendLookback` rehome must be behaviour-identical — the drain's existing tests are the proof.

- [ ] **Step 12: Mutation probes**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
F=internal/domain/fleetgrowth/working_capital.go
cp $F /tmp/wc.orig && \
sed -i '' 's|return maxInt64(runway, holdFill)|return runway|' $F && \
grep -q 'return runway$' $F && echo "MUTATION APPLIED" && \
go test ./internal/domain/fleetgrowth/ 2>&1 | grep -E '^(---|FAIL|ok)' ; \
cp /tmp/wc.orig $F && go test ./internal/domain/fleetgrowth/ 2>&1 | tail -2
```

Expected: kills `TestWorkingCapital_HoldFillDominatesOnFreshCapacity`. Then, each atomically:

- `s|maxInt64(0, cargoOutflow)|cargoOutflow|` → must kill `TestWorkingCapital_MalformedInputsCannotWeakenTheGuard`.
- In `transaction_repository.go`, `s|if len(peaks) == 0 {\n\t\treturn 0, false, nil\n\t}||` (delete the empty rung) → must kill `TestTreasuryHighWaterSince_EmptyWindowIsUnreadableNotZero`. **This is the probe that matters here** — it is the failure this repo has already shipped three times.
- `s|Select("MAX(balance_after)")|Select("balance_after")|` combined with an ordering change → must kill `TestTreasuryHighWaterSince_ReturnsThePeakNotTheLatest`. If a plain latest-row read passes the suite, the test is not distinguishing peak from latest and the fixture needs a peak that is not the newest row.

- [ ] **Step 11: Comment audit and commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
make comment-audit-check ONLY="internal/domain/fleetgrowth internal/domain/ledger internal/adapters/persistence internal/adapters/parkedsensing internal/application/parkedsensing"
git add internal/domain/fleetgrowth internal/domain/ledger/ports.go \
        internal/adapters/persistence/transaction_repository.go internal/adapters/persistence/transaction_repository_test.go \
        internal/adapters/parkedsensing/treasury_ports.go internal/adapters/parkedsensing/treasury_ports_test.go \
        internal/application/parkedsensing/buyports.go
git commit --no-verify -m "feat(fleetgrowth): the ledger observation surface behind the growth coordinator

Three reads of one fleet, over ONE window.

WORKING CAPITAL: max(runway, hold-fill), both derived from observed PURCHASE_CARGO
rows — no hardcoded hull price, which is wrong across most of the range it covers.
The hold-fill term binds exactly when capacity was just added, where the runway term
is blind. Integer milli-hours; every factor clamped before its product and the
product before the divide, so a malformed observation can only make the guard
stricter (RULINGS #4).

HIGH-WATER TREASURY: the peak balance across the window, which is what lets the wave
judge whether a fleet can REACH an ask rather than whether it happens to be holding
one at this instant. A narrow side port rather than a method on the broad repository
interface, following the GateFeeAggregator precedent and its stated reasoning. Empty
window reports UNREADABLE, never zero — a zero is the strong claim that a fleet has
never held money, and this repo has shipped that confusion three times.

ONE WINDOW, named for the property all three reads need: at least one full trade
cycle, so a measurement taken mid-cycle measures the fleet and not the cycle's phase.
Sized below the cycle, the high-water mark itself oscillates.

4 mutation probes, each naming its kills."
```

---

### Task 3: The shared unserved-lane read, the working-capital guard term, and the kept-surface renames

Three small, independent enablers the growth coordinator needs. All touch existing code and must be proven behaviour-identical before anything is built on them. The renames matter beyond tidiness: they take the guard stack and the hull-class vocabulary out of the `fleet_autosizer_*` namespace so **Plan B's deletion cannot sweep them by filename pattern.**

**Files:**
- Create: `gobot/internal/application/trading/queries/unserved_lane_reader.go`
- Create: `gobot/internal/application/trading/queries/unserved_lane_reader_test.go`
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_demand_ports.go:61-122` (delegate to the promoted reader)
- Rename: `gobot/internal/application/fleet/commands/fleet_autosizer_guards.go` → `purchase_guards.go`
- Rename: `gobot/internal/application/fleet/commands/fleet_autosizer_types.go` → `hull_classes.go`
- Modify: `purchase_guards.go` — `PurchaseRequest.WorkingCapital`, `guardAffordability`
- Modify: `hull_classes.go` — package doc rewritten off the autosizer's name
- Modify: `gobot/internal/application/fleet/commands/fleet_autosizer_guards_test.go` → `purchase_guards_test.go`

**Interfaces:**
- Consumes: `navigation.ShipRepository.FindAllByPlayer`, `shared.NewPlayerID`, `shared.ExtractSystemSymbol`, and the existing `CountProfitableLanes(ctx, playerID int, systems []string) (int, bool, error)` contract satisfied by `tradingQueries.ProfitableLaneReader` (`profitable_lane_reader.go:53`).
- Produces:
  - `queries.NewUnservedLaneReader(shipRepo navigation.ShipRepository, lanes ProfitableLaneCounter) *UnservedLaneReader` with `UnservedLaneCount(ctx, playerID int) (count int, readable bool, err error)`.
  - `commands.PurchaseRequest.WorkingCapital int64` — Task 4 sets it; every other caller leaves it zero, which is byte-identical.

- [ ] **Step 1: Write the failing test for the promoted reader**

Create `gobot/internal/application/trading/queries/unserved_lane_reader_test.go`. Mirror the fake-ship-repo idiom already used in this package's tests.

```go
package queries

import (
	"context"
	"errors"
	"testing"
)

type fakeLaneCounter struct {
	count      int
	readable   bool
	err        error
	lastSystem []string
}

func (f *fakeLaneCounter) CountProfitableLanes(ctx context.Context, playerID int, systems []string) (int, bool, error) {
	f.lastSystem = systems
	return f.count, f.readable, f.err
}

func TestUnservedLaneCount_SubtractsTheTradePool(t *testing.T) {
	// 7 profitable lanes, 2 trade-dedicated hulls => 5 unserved.
	repo := fakeShipRepoWith(t, tradeHulls(2), otherHulls(3))
	r := NewUnservedLaneReader(repo, &fakeLaneCounter{count: 7, readable: true})

	got, readable, err := r.UnservedLaneCount(context.Background(), 1)
	if err != nil || !readable {
		t.Fatalf("expected a readable count, got readable=%v err=%v", readable, err)
	}
	if got != 5 {
		t.Fatalf("expected 7 profitable − 2 trade hulls = 5 unserved, got %d", got)
	}
}

// The pool already covering every lane is never a NEGATIVE demand.
func TestUnservedLaneCount_PoolExceedsLanes_ClampsToZero(t *testing.T) {
	repo := fakeShipRepoWith(t, tradeHulls(9))
	r := NewUnservedLaneReader(repo, &fakeLaneCounter{count: 2, readable: true})
	got, readable, _ := r.UnservedLaneCount(context.Background(), 1)
	if !readable || got != 0 {
		t.Fatalf("expected a readable 0, got %d readable=%v", got, readable)
	}
}

// FAIL CLOSED on a genuine read failure — both spenders that consume this must refuse to act
// on a signal they could not see.
func TestUnservedLaneCount_FailsClosedOnReadFailure(t *testing.T) {
	t.Run("ship read", func(t *testing.T) {
		r := NewUnservedLaneReader(erroringShipRepo(errors.New("db down")), &fakeLaneCounter{count: 7, readable: true})
		_, readable, err := r.UnservedLaneCount(context.Background(), 1)
		if readable || err == nil {
			t.Fatalf("a ship read failure must be unreadable with an error, got readable=%v err=%v", readable, err)
		}
	})
	t.Run("lane surface", func(t *testing.T) {
		r := NewUnservedLaneReader(fakeShipRepoWith(t, tradeHulls(1)), &fakeLaneCounter{readable: false})
		_, readable, _ := r.UnservedLaneCount(context.Background(), 1)
		if readable {
			t.Fatalf("an unreadable lane surface must be unreadable here too")
		}
	})
}

// A READABLE zero is a genuine zero (empty cache, no floor-clearing lane) — no demand, no buy
// — and must NOT read as fail-closed, which would be indistinguishable from an outage.
func TestUnservedLaneCount_ReadableZeroIsNotFailClosed(t *testing.T) {
	r := NewUnservedLaneReader(fakeShipRepoWith(t, tradeHulls(0)), &fakeLaneCounter{count: 0, readable: true})
	got, readable, err := r.UnservedLaneCount(context.Background(), 1)
	if !readable || err != nil || got != 0 {
		t.Fatalf("expected (0,true,nil), got (%d,%v,%v)", got, readable, err)
	}
}

// The lane surface is scanned over the systems the fleet actually holds hulls in.
func TestUnservedLaneCount_ScansTheSystemsTheFleetOccupies(t *testing.T) {
	lanes := &fakeLaneCounter{count: 1, readable: true}
	r := NewUnservedLaneReader(fakeShipRepoWith(t, hullsInSystems("X1-AA", "X1-AA", "X1-BB")), lanes)
	if _, _, err := r.UnservedLaneCount(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lanes.lastSystem) != 2 {
		t.Fatalf("expected the two DISTINCT systems, got %v", lanes.lastSystem)
	}
}
```

Write the `fakeShipRepoWith` / `tradeHulls` / `otherHulls` / `hullsInSystems` / `erroringShipRepo` helpers in the same file, constructing `*navigation.Ship` values the way this package's existing tests do. Read `gobot/internal/application/trading/queries/profitable_lane_reader_test.go` first and match it.

- [ ] **Step 2: Run it to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/trading/queries/ -run TestUnservedLaneCount -v 2>&1 | tail -10
```

Expected: FAIL — `undefined: NewUnservedLaneReader`.

- [ ] **Step 3: Write the promoted reader**

Create `gobot/internal/application/trading/queries/unserved_lane_reader.go`. The body is lifted verbatim from `adapters/grpc/fleet_autosizer_demand_ports.go:78-122`; only its home changes.

```go
package queries

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tradeFleetTag is the dedication a hull carries when it belongs to the trade-tour pool.
const tradeFleetTag = "trade"

// ProfitableLaneCounter counts the profitable, feasible lanes ranked across the given systems,
// read-only, off the persisted market cache. Satisfied by ProfitableLaneReader.
type ProfitableLaneCounter interface {
	CountProfitableLanes(ctx context.Context, playerID int, systems []string) (count int, readable bool, err error)
}

// UnservedLaneReader surfaces the trade solver's profitable-but-unflown lane count: the number
// of profitable, feasible lanes the player's trading grounds rank BEYOND the current trade-hull
// pool. That surplus IS the capacity-short signal.
//
// ONE INSTANCE, TWO CONSUMERS. The fleet-growth coordinator's heavy demand and the sensing
// drain's wave gate both read this. It is constructed once at the composition root precisely so
// a second one is conspicuous: two readers of one quantity is how the spender and the
// withholder end up disagreeing about whether the fleet is capacity-short.
//
// READ-ONLY: it never perturbs the trade coordinator — it consumes the same pure ranking off
// the market cache that the trade circuit itself uses.
type UnservedLaneReader struct {
	ships navigation.ShipRepository
	lanes ProfitableLaneCounter
}

// NewUnservedLaneReader wires the capacity-short signal over its two read sources.
func NewUnservedLaneReader(ships navigation.ShipRepository, lanes ProfitableLaneCounter) *UnservedLaneReader {
	return &UnservedLaneReader{ships: ships, lanes: lanes}
}

// UnservedLaneCount reports profitable lanes beyond the trade pool, floored at 0.
//
// It fails CLOSED (readable=false) on a genuine ship or market read failure: acting on a
// capacity signal nobody could read is exactly the runaway the guard stacks exist to prevent.
// A READABLE zero — an empty cache, or no lane clearing the profit floor — is a genuine zero
// and is reported as such, because a fleet with nothing to fly must not be indistinguishable
// from a fleet whose sensors are down.
func (r *UnservedLaneReader) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, false, nil
	}
	ships, err := r.ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, false, err
	}
	profitable, readable, err := r.lanes.CountProfitableLanes(ctx, playerID, distinctShipSystems(ships))
	if err != nil || !readable {
		return 0, false, err
	}
	heavies := 0
	for _, sh := range ships {
		if sh.DedicatedFleet() == tradeFleetTag {
			heavies++
		}
	}
	unserved := profitable - heavies
	if unserved < 0 {
		unserved = 0
	}
	return unserved, true, nil
}

// distinctShipSystems returns the distinct systems the player's hulls stand in — the trading
// grounds the lane count scans.
func distinctShipSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		loc := sh.CurrentLocation()
		if loc == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}
```

- [ ] **Step 4: Point the autosizer's existing source at it (behaviour-identical)**

Edit `gobot/internal/adapters/grpc/fleet_autosizer_demand_ports.go`. Replace `autosizerHeavySources.UnservedLaneCount`'s body and the local `distinctShipSystems` with delegation, so there is exactly one implementation while both coordinators exist:

```go
type autosizerHeavySources struct {
	shipRepo navigation.ShipRepository
	unserved *tradingQueries.UnservedLaneReader
}

func (s *autosizerHeavySources) HeavyCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.DedicatedFleet() == "trade" })
}

// UnservedLaneCount delegates to the shared reader: one definition of the capacity-short
// signal, consumed by every spender that acts on it.
func (s *autosizerHeavySources) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	return s.unserved.UnservedLaneCount(ctx, playerID)
}
```

Then update its construction in `gobot/internal/adapters/grpc/fleet_autosizer_ports.go:53-56` to take the reader instead of the raw lane reader, and delete the now-unused local `profitableLaneCounter` interface and `distinctShipSystems` from `fleet_autosizer_demand_ports.go`.

- [ ] **Step 5: Verify the delegation is behaviour-identical**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go build ./... && go vet ./...
go test ./internal/application/trading/queries/ ./internal/adapters/grpc/ 2>&1 | grep -Ev '^(ok|\?)' | head -20
```

Expected: build/vet clean, no failures. **The autosizer's existing heavy-demand tests are the proof of identity** — if any of them changed expectations, the move was not a move.

- [ ] **Step 6: Rename the guard file, then write the failing test for the working-capital term**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
git mv internal/application/fleet/commands/fleet_autosizer_guards.go internal/application/fleet/commands/purchase_guards.go
git mv internal/application/fleet/commands/fleet_autosizer_guards_test.go internal/application/fleet/commands/purchase_guards_test.go
git mv internal/application/fleet/commands/fleet_autosizer_guard_simplification_test.go internal/application/fleet/commands/purchase_guard_simplification_test.go
git mv internal/application/fleet/commands/fleet_autosizer_types.go internal/application/fleet/commands/hull_classes.go
```

`hull_classes.go` carries the package doc for `internal/application/fleet/commands`, which currently opens "Package commands holds the fleet capacity autosizer". Rewrite it so it describes what the package will hold once the autosizer is gone — the growth coordinator, the shared purchase guard stack, and the hull-class aliases — without narrating the change:

```go
// Package commands holds the fleet's HULL-BUYING coordinators and the fail-CLOSED money-guard
// stack they judge every purchase through. Spending is irreversible and not-buying is safe, so
// any unreadable input (price, treasury, census, lane count, API utilization) BLOCKS.
//
// The guard stack is the package's, not any one coordinator's: it is judged pure, it is shared,
// and it outlives whichever coordinator currently calls it.
package commands
```

Add to `purchase_guards_test.go`:

```go
// Working capital is a term ABOVE the immutable floor. A purchase that clears the floor and
// the margin must still be refused when it would not leave the observed working capital behind.
func TestGuardAffordability_WorkingCapitalRaisesTheFloor(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
		// treasury 1_000_000 − floor 50_000 = 950_000 >= 700_000 + 200_000 = 900_000 : PASSES today.
	}
	if v := guardAffordability(req); !v.Passed {
		t.Fatalf("baseline must pass before the new term is added: %s", v.Detail)
	}
	req.WorkingCapital = 100_000
	// 1_000_000 − 50_000 − 100_000 = 850_000 < 900_000 : must now BLOCK.
	v := guardAffordability(req)
	if v.Passed {
		t.Fatalf("working capital must raise the effective floor: %s", v.Detail)
	}
	if !strings.Contains(v.Detail, "working capital") {
		t.Fatalf("the arithmetic must NAME the term an operator would retune: %s", v.Detail)
	}
}

// The term is NEVER waived, including for the heavy purchase itself. The heavy RESERVE is
// waived because that reservation exists FOR this buy; working capital is the trading fleet's
// committed cargo spend, which the purchase does not fund and must not consume.
func TestGuardAffordability_WorkingCapitalIsNotWaivedForHeavies(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
		HeavyReserve: 400_000, WorkingCapital: 100_000,
	}
	v := guardAffordability(req)
	if v.Passed {
		t.Fatalf("the heavy reserve is waived but working capital is not: %s", v.Detail)
	}
}

// A zero term is byte-identical to the pre-existing behaviour — the migration's safety net
// while both coordinators exist.
func TestGuardAffordability_ZeroWorkingCapitalIsUnchanged(t *testing.T) {
	req := PurchaseRequest{
		Class: HullClassHeavy, TreasuryReadable: true,
		LiveTreasury: 1_000_000, Price: 700_000, MarginOverFloor: 200_000,
	}
	if v := guardAffordability(req); !v.Passed {
		t.Fatalf("a zero working-capital term must change nothing: %s", v.Detail)
	}
}
```

- [ ] **Step 7: Run it to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/fleet/commands/ -run TestGuardAffordability_WorkingCapital -v 2>&1 | tail -10
```

Expected: FAIL — `unknown field WorkingCapital`.

- [ ] **Step 8: Add the field and the guard term**

In `purchase_guards.go`, add to `PurchaseRequest` immediately after `HeavyReserve` (around the current `:155`):

```go
	// WorkingCapital is the credits the TRADING fleet's observed activity has already spoken
	// for — derived by fleetgrowth.WorkingCapital from ledger observations, never a constant.
	// It raises this buy's effective floor ABOVE the immutable reserve and is never subtracted
	// from it, so adding it can only ever make this guard stricter (RULINGS #4).
	//
	// NOT WAIVED FOR THE HEAVY BUY, unlike HeavyReserve. The reservation is waived because it
	// is being accumulated FOR this purchase; working capital is money the trading fleet is
	// about to spend on cargo, which this purchase does not fund and must not consume — buying
	// a hull out of it stalls the trades that pay for everything, including the next hull.
	WorkingCapital int64
```

In `guardAffordability`, change TERM 2. Replace the `spendable` line and its detail:

```go
	spendable := req.LiveTreasury - floor - heavyReserve - maxGuardInt64(0, req.WorkingCapital)
	need := req.Price + req.MarginOverFloor
	floorOK := spendable >= need
	floorDetail := fmt.Sprintf("treasury %d − floor %d%s − working capital %d = %d >= price %d + margin %d = %d",
		req.LiveTreasury, floor, reserveNote, maxGuardInt64(0, req.WorkingCapital), spendable, req.Price, req.MarginOverFloor, need)
```

and add the helper at the bottom of the file:

```go
// maxGuardInt64 clamps a term non-negative before it reaches the floor arithmetic. A negative
// working capital would ADD spendable credits, which is the one direction a money guard may
// never move.
func maxGuardInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
```

Also update the doc comment's `treasury_floor` line so the merged formula it documents stays accurate:

```
//	treasury_floor : TreasuryReadable && (treasury − floor − heavyReserve − workingCapital) >= price + margin
```

- [ ] **Step 9: Run the whole guard suite**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/fleet/commands/ 2>&1 | grep -Ev '^(ok|\?)' | head -20
go vet ./... && gofmt -l . | head
```

Expected: every pre-existing guard test still passes unchanged (they all leave `WorkingCapital` zero), plus the three new ones.

- [ ] **Step 10: Mutation probe**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
F=internal/application/fleet/commands/purchase_guards.go
cp $F /tmp/pg.orig && \
sed -i '' 's|- maxGuardInt64(0, req.WorkingCapital)$||' $F && \
! grep -q 'maxGuardInt64(0, req.WorkingCapital)$' $F && echo "MUTATION APPLIED" && \
go test ./internal/application/fleet/commands/ -run TestGuardAffordability 2>&1 | grep -E '^(---|FAIL|ok)' ; \
cp /tmp/pg.orig $F && go test ./internal/application/fleet/commands/ -run TestGuardAffordability 2>&1 | tail -2
```

Expected: kills `TestGuardAffordability_WorkingCapitalRaisesTheFloor` and `TestGuardAffordability_WorkingCapitalIsNotWaivedForHeavies`.

- [ ] **Step 11: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
make comment-audit-check ONLY="internal/application/trading/queries internal/application/fleet/commands internal/adapters/grpc"
git add internal/application/trading/queries internal/application/fleet/commands internal/adapters/grpc
git commit --no-verify -m "feat(fleet): promote the unserved-lane read to a shared query; add the working-capital guard term

Two enablers, both behaviour-identical where they touch live code.

(1) UnservedLaneReader moves out of the autosizer's unexported adapter into
trading/queries so ONE instance serves the heavy buyer AND the sensing wave gate.
The autosizer's own source now delegates; its existing tests prove the move.

(2) PurchaseRequest.WorkingCapital raises the affordability floor ABOVE the
immutable reserve, clamped non-negative, NOT waived for the heavy buy (unlike the
heavy reserve, which is accumulated FOR that buy). A zero term is byte-identical,
which is what keeps the migration safe while both coordinators exist.

(3) The guard stack and the hull-class vocabulary are renamed OFF the fleet_autosizer_*
namespace. They are the package's, not the autosizer's, and the rename is what stops a
later deletion sweeping them by filename pattern.

3 mutation probes, each naming its kills."
```

---

### Task 4: The fleet-growth coordinator — wave predicate + heavy buy path

The largest task, and **three facts must change atomically in its final commit**: the growth coordinator starts buying heavies, the autosizer's heavy class stops, and `hullbuy.HeavyBuyerContainers()` is **replaced** so the cap declaration follows the buyer.

Why all three together. `selectHeavyBuyer` (`heavy_reserve_port.go:374-387`) returns the **first DECLARED owner** in `RUNNING`-then-`id ASC` order. Declare both types while both containers run and the winner is decided by container id — arbitrary, and quite possibly the autosizer, which no longer buys heavies. The sensing reservation would then withhold treasury against a cap the actual spender never consults: the silently-divergent reserve spec step 5 exists to prevent, arriving early because Plan A stops before deletion. Any split of these three leaves a window where that is live.

**Files:**
- Create: `run_fleet_growth_coordinator.go`, `run_fleet_growth_reconcile.go`, `fleet_growth_act.go`, `fleet_growth_tune.go` (all in `gobot/internal/application/fleet/commands/`)
- Create: `gobot/internal/application/fleet/commands/run_fleet_growth_coordinator_test.go`, `fleet_growth_act_test.go`
- Rename: `fleet_autosizer_stall.go` → `fleet_growth_stall.go` (+ its test)
- Create: `gobot/internal/adapters/grpc/container_ops_fleet_growth.go`, `fleet_growth_ports.go`
- Create: `gobot/internal/adapters/metrics/fleet_growth_metrics.go`
- Modify: `gobot/internal/domain/container/container.go`, `adapters/grpc/command_factory_registry.go`, `adapters/grpc/container_ops_tune.go`, `cmd/spacetraders-daemon/main.go`
- Modify: `gobot/internal/application/fleet/commands/run_fleet_autosizer_coordinator.go:290-299` (`classDisabled`)
- Modify: `gobot/internal/domain/hullbuy/hull_class.go:73-75` (`HeavyBuyerContainers`) + `hull_class_test.go`
- Rename: `fleet_autosizer_stall.go` → `fleet_growth_stall.go` (+ test)

**Interfaces:**
- Consumes: `common.DeriveWave`/`WaveInputs`/`Wave`/`WaveProbeReason`/`Reachable` (Task 1); `fleetgrowth.WorkingCapital`, `fleetgrowth.TradeCycleWindow`, `ledger.TreasuryHighWaterReader` (Task 2); `queries.UnservedLaneReader` (Task 3); `commands.PurchaseRequest`/`EvaluateGuards`/`GuardName`/`ClassDemand` (kept surface); `common.HeavyReserve`/`HeavyReserveInputs`/`HeavyReserveTarget` (`heavy_reserve.go:127`); `hullbuy.BuyOrder`/`BuyResult`/`HullClassHeavy`/`DefaultHeavyCap`; existing port interfaces `TreasuryReader`, `HeavyCensusReader`, `HeavyYardReader`, `APIUtilizationReader`, `YardPriceReader`, `Purchaser`, `PurchaseNotifier` (`fleet_autosizer_act.go:28-92`) — all kept.
- Produces:
  - `container.ContainerTypeFleetGrowth ContainerType = "FLEET_GROWTH_COORDINATOR"`
  - `commands.RunFleetGrowthCoordinatorCommand`, `commands.RunFleetGrowthCoordinatorHandler`, `NewRunFleetGrowthCoordinatorHandler(clock shared.Clock)`
  - `commands.FleetGrowthTunableDefaults() map[string]int` — keys `growth_enabled`, `heavy_cap`
  - `commands.GrowthMetricsSink` with `RecordWave(playerID string, wave common.Wave, reason common.WaveProbeReason)` plus the heavy-reserve/demand/purchase/blocked series
  - `(*DaemonServer).FleetGrowthCoordinator(ctx, playerID int, agentSymbol string) (string, error)`
  - `grpc.NewFleetGrowthCoordinatorHandler(...)`

- [ ] **Step 1: Register the container type and the recovery builder**

`gobot/internal/domain/container/container.go` — add beside `ContainerTypeFleetAutosizer` (`:104`):

```go
// ContainerTypeFleetGrowth is the standing fleet-growth coordinator: it alternates heavy-hull
// buying and probe buying in waves, from one predicate derived every tick. It runs an infinite
// reconcile loop inside a single Handle(), so it is NOT a CoordinatorOwnsIterations type.
ContainerTypeFleetGrowth ContainerType = "FLEET_GROWTH_COORDINATOR"
```

`gobot/internal/adapters/grpc/command_factory_registry.go` — add beside the `fleet_autosizer` entry (`:165`):

```go
		// fleet_growth: the standing fleet-growth coordinator. Like fleet_autosizer/siting it
		// loops forever inside one Handle(), so it is NOT a CoordinatorOwnsIterations type.
		{CommandType: "fleet_growth", build: buildFleetGrowthCommand},
```

- [ ] **Step 2: Write the failing test for the coordinator's wave behaviour**

Create `gobot/internal/application/fleet/commands/fleet_growth_act_test.go`. Follow `run_fleet_autosizer_coordinator_test.go:1-40`'s fake idiom.

```go
package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

type fakeLanes struct {
	count    int
	readable bool
	err      error
}

func (f *fakeLanes) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	return f.count, f.readable, f.err
}

type fakeOutflow struct {
	total, largest int64
	err            error
}

func (f *fakeOutflow) CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (int64, int64, error) {
	return f.total, f.largest, f.err
}

type recordingWaveSink struct {
	waves   []common.Wave
	reasons []common.WaveProbeReason
	GrowthMetricsSink
}

func (r *recordingWaveSink) RecordWave(playerID string, w common.Wave, reason common.WaveProbeReason) {
	r.waves = append(r.waves, w)
	r.reasons = append(r.reasons, reason)
}

// THE WAVE IS PUBLISHED EVERY TICK ON EVERY PATH. A HEAVY wave and a stalled coordinator both
// look like "no probes bought", so the operator must be able to read the state from a series
// rather than infer it from the absence of one.
func TestGrowthReconcile_PublishesTheWaveEveryTick(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 0, readable: true}})
	h.SetMetricsSink(sink)

	for i := 0; i < 3; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(sink.waves) != 3 {
		t.Fatalf("expected the wave published on all 3 ticks, got %d", len(sink.waves))
	}
	if sink.reasons[0] != common.WaveProbeReasonLanesServed {
		t.Fatalf("expected lanes_served, got %q", sink.reasons[0])
	}
}

// The master switch OFF must publish a PROBE wave, not go silent. A silent coordinator would
// pause probe buying for a buyer that is switched off — a deadlock no spender can clear.
func TestGrowthReconcile_DisabledPublishesProbeAndBuysNothing(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 9, readable: true}, growthEnabled: false})
	h.SetMetricsSink(sink)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Purchased != 0 {
		t.Fatalf("a disabled coordinator must buy nothing, bought %d", res.Purchased)
	}
	if len(sink.waves) != 1 || sink.waves[0] != common.WaveProbe {
		t.Fatalf("expected exactly one PROBE wave published, got %v", sink.waves)
	}
	if sink.reasons[0] != common.WaveProbeReasonGrowthDisabled {
		t.Fatalf("expected growth_disabled, got %q", sink.reasons[0])
	}
}

// OFF STOPS THE READS, not just the buying — that is the whole point of the switch, because
// the shipyard price walk runs BEFORE the guards can block it.
func TestGrowthReconcile_DisabledReadsNoPrices(t *testing.T) {
	prices := &countingYardPrice{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 9, readable: true}, growthEnabled: false})
	h.SetYardPriceReader(prices)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prices.calls != 0 {
		t.Fatalf("a disabled coordinator must walk no shipyards, got %d PriceFor calls", prices.calls)
	}
}

// A PROBE wave buys no heavy, whatever the demand says. The PROBE here comes from demonstrated
// capacity — a peak far below the entry threshold — not from where the live balance happens to
// sit, which is the whole point of the ruled measure.
func TestGrowthReconcile_ProbeWaveBuysNoHeavy(t *testing.T) {
	buyer := &recordingPurchaser{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:     &fakeLanes{count: 9, readable: true},
		treasury:  1_500_000, // momentarily flush, and it must not matter
		highWater: 400_000,   // the peak across the cycle cannot reach the ask
		yardAsk:   1_916_613,
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("a PROBE wave must buy no heavy, got %d buys", buyer.calls)
	}
}

// A HEAVY wave with every guard satisfied buys exactly one hull, dedicated to the trade pool.
func TestGrowthReconcile_HeavyWaveBuysOne(t *testing.T) {
	buyer := &recordingPurchaser{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:    &fakeLanes{count: 9, readable: true},
		treasury: 12_000_000,
		yardAsk:  1_000_000,
		streak:   3, // the anti-thrash streak already satisfied
	})
	h.SetPurchaser(buyer)

	res, err := h.reconcileOnce(context.Background(), growthCmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("expected exactly one heavy bought, got purchased=%d calls=%d", res.Purchased, buyer.calls)
	}
	if buyer.lastOrder.Class != HullClassHeavy {
		t.Fatalf("expected a heavy order, got %q", buyer.lastOrder.Class)
	}
}

// THE ANTI-THRASH STREAK LIVES ON THE BUY, NOT THE PREDICATE. A transient spike in the lane
// ranking must not trigger a seven-figure purchase; a transient spike in the WAVE costs only a
// paused probe tick, which is why the streak is deliberately asymmetric.
func TestGrowthReconcile_HeavyBuyWaitsForTheStreak(t *testing.T) {
	buyer := &recordingPurchaser{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000,
	})
	h.SetPurchaser(buyer)

	// Ticks 1 and 2 are inside the 3-tick streak: HEAVY wave, no purchase.
	for i := 0; i < 2; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if buyer.calls != 0 {
		t.Fatalf("no heavy may be bought before the streak is met, got %d", buyer.calls)
	}
	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if buyer.calls != 1 {
		t.Fatalf("the third consecutive tick must buy, got %d", buyer.calls)
	}
}

// The working-capital term reaches the guard. A treasury that clears price+margin+floor but
// not the observed cargo commitment must refuse the buy.
func TestGrowthReconcile_WorkingCapitalBlocksTheBuy(t *testing.T) {
	buyer := &recordingPurchaser{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 5_000_000, yardAsk: 1_000_000, streak: 3,
		outflow: &fakeOutflow{total: 2_000_000, largest: 100_000},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("the observed cargo commitment must block this buy, got %d", buyer.calls)
	}
}

// Fail-closed: an unreadable ledger is not a zero commitment.
func TestGrowthReconcile_UnreadableOutflowBuysNothing(t *testing.T) {
	buyer := &recordingPurchaser{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000, streak: 3,
		outflow: &fakeOutflow{err: errors.New("ledger down")},
	})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("an unreadable ledger must not fail the tick: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("an unreadable cargo outflow must buy nothing, got %d", buyer.calls)
	}
}
```

Write `growthFixture`, `newGrowthHandlerWith`, `growthCmd`, `recordingPurchaser`, `countingYardPrice` in the same file, constructing the handler through its setters. Read `run_fleet_autosizer_coordinator_test.go` and `fleet_autosizer_act_test.go` first and match their fake shapes exactly — the port interfaces are unchanged.

- [ ] **Step 3: Run it to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/fleet/commands/ -run TestGrowthReconcile -v 2>&1 | tail -10
```

Expected: FAIL — `undefined: newGrowthHandlerWith`, `undefined: GrowthMetricsSink`.

- [ ] **Step 4: Write the command, the handler and the tick state**

Create `gobot/internal/application/fleet/commands/run_fleet_growth_coordinator.go`. Mirror `run_fleet_autosizer_coordinator.go` — same setter-collaborator shape, same infinite `Handle` loop, same per-container state map.

```go
const (
	defaultGrowthTickSeconds = 900
	// defaultGrowthUnservedLanesMin is the ANTI-THRASH streak on the heavy BUY: the unserved-lane
	// shortfall must persist this many consecutive ticks before a large hull is bought. It is on
	// the purchase and deliberately NOT on the wave: a spurious wave costs one paused probe tick,
	// a spurious purchase costs a hull.
	defaultGrowthUnservedLanesMin = 3
	// defaultGrowthRunwayMilliHours is how many MILLI-hours of the trading fleet's measured cargo
	// runway a heavy purchase holds back on top of the immutable reserve. Milli rather than a
	// float because sub-hour runway is the operating range and a float would put NaN inside a
	// money guard.
	defaultGrowthRunwayMilliHours = 2000

	defaultGrowthPurchaseMarginOverFloor     = 200000
	defaultGrowthTreasuryPctPerPurchase      = 25
	defaultGrowthAPIUtilCeilingPct           = 85
	defaultGrowthMaxPremiumOverCheapestPct   = 50
	defaultGrowthZeroEffectAlarmTicks        = 4
	defaultGrowthShipTypeHeavies             = "SHIP_HEAVY_FREIGHTER"
	// defaultGrowthHeavyCap is the SHARED constant, not a copy: the sensing heavy reservation
	// falls back to the same value, and two hand-copied literals is how the withholder and the
	// spender end up saving toward different caps.
	defaultGrowthHeavyCap = hullbuy.DefaultHeavyCap
)

// RunFleetGrowthCoordinatorCommand launches the standing fleet-growth coordinator for a player.
type RunFleetGrowthCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	TickIntervalSecs        int
	HeavyCap                *int // *int so an explicit 0 (operator hold) is told from unset
	UnservedLanesMin        int
	RunwayMilliHours        int
	PurchaseMarginOverFloor int64
	TreasuryPctPerPurchase  int
	APIUtilizationCeilingPct int
	MaxPriceHeavies          int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  *bool
	ShipTypeHeavies           string
	ZeroEffectAlarmTicks      int
}

// UnservedLaneReader is the capacity-short signal: profitable lanes beyond the trade pool.
// readable=false ⇒ the wave is PROBE and no heavy is bought (fail-closed on the buy, release
// on the wave — both are the direction that spends nothing).
type UnservedLaneReader interface {
	UnservedLaneCount(ctx context.Context, playerID int) (count int, readable bool, err error)
}

// CargoOutflowReader reports the trading fleet's observed cargo outflow over a trailing window
// as the two statistics the working-capital formula consumes.
type CargoOutflowReader interface {
	CargoOutflowSince(ctx context.Context, playerID int, since time.Time) (total int64, largestSingle int64, err error)
}

// TreasuryHighWaterReader reports the fleet's demonstrated capacity: the highest balance held
// across a full trade cycle. readable=false (an unobservable window) yields PROBE, never a
// zero — see common.WaveInputs.
type TreasuryHighWaterReader interface {
	TreasuryHighWaterSince(ctx context.Context, playerID shared.PlayerID, since time.Time) (int64, bool, error)
}

// TradeHullCounter is the trade-DEDICATED pool count — the multiplier on the hold-fill term.
// It is deliberately the tag-scoped count, not the broad heavy census: the term asks what the
// TRADING fleet is about to spend, and a heavy tagged elsewhere is not flying a lane.
type TradeHullCounter interface {
	TradeHulls(ctx context.Context, playerID int) (int, error)
}

// GrowthMetricsSink records the coordinator's observation series (pure observation; nil-safe).
type GrowthMetricsSink interface {
	// RecordWave publishes the regime and, on PROBE, which clause forced it. Emitted EVERY tick
	// on EVERY path including the paused one: a HEAVY wave and a stalled coordinator both look
	// like "no probes bought", and only an always-present series tells them apart.
	RecordWave(playerID string, wave common.Wave, reason common.WaveProbeReason)
	RecordGrowthEnabled(playerID string, enabled bool)
	RecordHeavyReserve(playerID string, reserve, target int64, owned, capacity int)
	RecordWorkingCapital(playerID string, credits int64)
	RecordDemand(class HullClass, demand, current int)
	RecordPurchase(class HullClass)
	RecordBlocked(class HullClass, guard GuardName)
	RecordZeroEffectAlarm()
	ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64)
}
```

The handler mirrors `RunFleetAutosizerCoordinatorHandler` field for field, minus `providers` (there is exactly one class) and plus `lanes UnservedLaneReader`, `outflow CargoOutflowReader`, `tradeHulls TradeHullCounter`, `highWater TreasuryHighWaterReader`. Its state struct keeps only:

```go
type growthState struct {
	// heavyShortfallStreak counts consecutive ticks the unserved-lane shortfall has persisted.
	// In-memory and per-container: it is edge-trigger bookkeeping, not operational state, and it
	// is deliberately NOT on the wave predicate, which two containers must agree on and
	// therefore cannot hold a streak for.
	heavyShortfallStreak int
	noEffectStreak       int
	noEffectPaged        bool
}
```

`Handle` is byte-for-byte the autosizer's loop with the names changed (`run_fleet_autosizer_coordinator.go:239-274`).

- [ ] **Step 5: Write the reconcile and the act**

`run_fleet_growth_reconcile.go` — `resolveFleetGrowthConfig` mirrors `resolveFleetAutosizerConfig` (`run_fleet_autosizer_reconcile.go:47-105`), including `resolveHeavyCap`'s pointer semantics (`:301-309`), which move over verbatim.

`reconcileOnce` runs in this order — the order matters and is the same reason the autosizer's does:

```go
func (h *RunFleetGrowthCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunFleetGrowthCoordinatorCommand) (growthResult, error) {
	cfg := resolveFleetGrowthConfig(cmd)
	liveCap, growthOn := h.liveKnobs(ctx, cmd, cfg.HeavyCap)
	cfg.HeavyCap = liveCap
	res := growthResult{}
	pid := strconv.Itoa(cmd.PlayerID)

	if h.metrics != nil {
		h.metrics.RecordGrowthEnabled(pid, growthOn)
	}

	// THE MASTER SWITCH, placed HERE because every port is read below this line — and the
	// expensive one is the shipyard price walk, which runs before any guard can block it.
	//
	// THE PAUSED PATH STILL PUBLISHES A WAVE, and it publishes PROBE. A switched-off buyer has
	// nothing to save for, so pausing probe buying for it would be a deadlock no spender could
	// clear; going silent instead would leave the drain reading a stale regime.
	if !growthOn {
		if h.metrics != nil {
			h.metrics.RecordWave(pid, common.WaveProbe, common.WaveProbeReasonGrowthDisabled)
		}
		h.reportGrowthPaused(ctx, cmd)
		return res, nil
	}

	st := h.coordinatorState(cmd.ContainerID)
	in := h.readTickInputs(ctx, cmd.PlayerID, cfg)

	wave, reason := common.DeriveWave(common.WaveInputs{
		GrowthEnabled:         true,
		UnservedLanes:         in.unservedLanes,
		UnservedLanesReadable: in.unservedLanesOK,
		Target:                in.heavyTarget,
		// The regime is judged on the fleet's DEMONSTRATED CAPACITY, never on the balance it
		// happens to hold this tick. The live balance is carried for the log and the gauges only.
		HighWaterTreasury:        in.highWater,
		HighWaterReadable:        in.highWaterOK,
		LiveTreasuryForReporting: in.treasury,
	})
	if h.metrics != nil {
		h.metrics.RecordWave(pid, wave, reason)
		h.metrics.RecordHeavyReserve(pid, in.heavyReserve, int64(in.heavyTarget), in.heaviesOwned, cfg.HeavyCap)
		h.metrics.RecordWorkingCapital(pid, in.workingCapital)
	}

	// The pricing errand runs on the PROBE wave too: it spends no credits, buys nothing, and its
	// whole job is to make a LATER tick's price readable — which is what lets the wave ever flip.
	h.runHeavyPricingErrand(ctx, cmd, cfg, in)

	// The streak advances on the DEMAND, every tick, so it is not reset by a wave flip.
	demand := h.readHeavyDemand(ctx, cmd.PlayerID, in)
	h.advanceShortfallStreak(st, demand)

	if wave != common.WaveHeavy {
		h.observeClassStall(ctx, cmd, HullClassHeavy, health.TickIdle())
		return res, nil
	}

	bought, unmetNoBuy := h.buyHeavy(ctx, cmd, cfg, demand, in, st)
	...
}
```

`fleet_growth_act.go` holds `readTickInputs` (the autosizer's, `fleet_autosizer_act.go:146-186`, plus the three new reads) and `buyHeavy` (the autosizer's `sizeClass`, `:209-312`, heavy-only). The new part of `readTickInputs`:

```go
	if h.lanes != nil {
		if n, ok, err := h.lanes.UnservedLaneCount(ctx, playerID); err == nil {
			in.unservedLanes, in.unservedLanesOK = n, ok
		}
	}
	// The demonstrated-capacity read. Its window is the SHARED one — the drain measures the same
	// fleet over the same span, and two windows would be two answers to one question.
	if h.highWater != nil {
		if hw, ok, err := h.highWater.TreasuryHighWaterSince(ctx, pid, h.clock.Now().Add(-fleetgrowth.TradeCycleWindow)); err == nil {
			in.highWater, in.highWaterOK = hw, ok
		}
	}
	// The working-capital term. FAIL-CLOSED as a whole: an unreadable ledger or hull count
	// leaves workingCapitalOK false, which refuses the buy — an unknowable commitment is NOT a
	// zero one, and reading it as zero hands back the cheapest floor available exactly when we
	// understand the least.
	if h.outflow != nil && h.tradeHulls != nil {
		total, largest, err := h.outflow.CargoOutflowSince(ctx, playerID, h.clock.Now().Add(-fleetgrowth.TradeCycleWindow))
		hulls, herr := h.tradeHulls.TradeHulls(ctx, playerID)
		if err == nil && herr == nil {
			in.workingCapital = fleetgrowth.WorkingCapital(cfg.RunwayMilliHours, total, hulls, largest)
			in.workingCapitalOK = true
		}
	}
```

and `buyHeavy` builds the request exactly as `buildPurchaseRequest` does (`:316-374`), heavy-only, plus:

```go
		WorkingCapital: in.workingCapital,
```

with the fail-closed rung ahead of it:

```go
	if !in.workingCapitalOK {
		logger.Log("INFO", "Fleet growth: cargo commitment unreadable — no buy", map[string]interface{}{
			"action": "growth_working_capital_unreadable", "container_id": cmd.ContainerID,
		})
		return false, true
	}
```

- [ ] **Step 6: Write the live knobs**

`fleet_growth_tune.go` mirrors `fleet_autosizer_tune.go` exactly (`:1-103`), with `growthEnabledKey = "growth_enabled"` and `heavyCapKey` reused. **Both keys stay BARE (unprefixed)**: `resolveFleetGrowthConfig` clears and re-injects every `growth_*` launch key on each container rebuild, so a tune written to a prefixed key is wiped by the next daemon bounce.

```go
func FleetGrowthTunableDefaults() map[string]int {
	return map[string]int{
		heavyCapKey:      defaultGrowthHeavyCap,
		growthEnabledKey: defaultGrowthEnabled, // 1 = ON. Ships ARMED; the knob is the control.
	}
}
```

- [ ] **Step 7: Write the launch path and the recovery builder**

`gobot/internal/adapters/grpc/container_ops_fleet_growth.go` mirrors `container_ops_fleet_autosizer.go` in full. Launch config is identity-only; `fleetGrowthConfigKeys` lists every `growth_*` key:

```go
var fleetGrowthConfigKeys = []string{
	"growth_tick_secs",
	// NOTE: the live-tunable knobs are the BARE keys "heavy_cap" and "growth_enabled", NOT
	// these prefixed launch keys. resolveFleetGrowthConfig CLEARS and re-injects every growth_*
	// key on each rebuild, so a tune written to a prefixed key would be wiped on the next
	// daemon bounce. The bare keys are untouched by that cycle and therefore survive.
	//
	// The suffix matters: the heavy-buyer cap read resolves a launch cap by its "_heavy_cap"
	// suffix, so this key must keep that ending.
	"growth_heavy_cap",
	"growth_unserved_lanes_min",
	"growth_runway_milli_hours",
	"growth_purchase_margin_over_floor",
	"growth_treasury_pct_per_purchase",
	"growth_api_utilization_ceiling_pct",
	"growth_max_price_heavies",
	"growth_max_premium_over_cheapest_pct",
	"growth_prefer_demand_proximal_yard",
	"growth_ship_type_heavies",
	"growth_zero_effect_alarm_ticks",
}
```

`buildFleetGrowthCommand` reads each with `cfg.OptionalInt`/`OptionalString`, and `HeavyCap: presentIntPtr(cfg, "growth_heavy_cap")` so an explicit 0 survives the round trip (`container_ops_fleet_autosizer.go:200-209`).

- [ ] **Step 8: Register the tune knobs — and pin the mode's constancy across a trade cycle**

`gobot/internal/adapters/grpc/container_ops_tune.go`: add `"growth": string(container.ContainerTypeFleetGrowth)` to `tuneOperationCoordinatorTypes` (`:67-80`), `fleetGrowth := fleetCmd.FleetGrowthTunableDefaults()` beside its siblings (`:83-91`), and a bounds block:

```go
		string(container.ContainerTypeFleetGrowth): {
			"growth_enabled": {Type: "int", Min: 1, Max: 2, Default: fleetGrowth["growth_enabled"], Unit: "flag", Description: "fleet-growth MASTER SWITCH: 1=on (default), 2=off. NOT 0/1 — `tune <key> 0` means revert-to-default fleet-wide, so 0 would make 'off' unexpressible. OFF STOPS THE READS, not just the buying: no shipyard price walk, no demand read, no pricing errand, no purchase — the walk runs BEFORE the guards can block it, so a blocked decision costs the same request budget as an approved one. OFF ALSO FORCES THE WAVE TO PROBE, so probe buying resumes rather than pausing for a buyer that cannot buy. It is NOT a money guard: the immutable 50k floor, the 25% rule and the working-capital term are consts/derived and apply whenever growth is on. Applies next tick, no restart"},
			"heavy_cap":      {Type: "int", Min: 0, Max: 50, Default: fleetGrowth["heavy_cap"], Unit: "hulls", Description: "ceiling on owned HEAVY HULLS (capital exposure), counted FLEET-WIDE regardless of dedicated_fleet tag. The only count-based bound on the class; every other bound is economic. At or over it the wave is PROBE. NOTE: `tune heavy_cap 0` DELETES the key and reverts to the default — to HOLD at zero set the launch key and restart. Applies next tick"},
			"growth_runway_milli_hours": {Type: "int", Min: 0, Max: 10000, Default: fleetGrowth["growth_runway_milli_hours"], Unit: "milli-hours", Description: "MILLI-hours of the TRADING fleet's measured cargo runway a heavy purchase holds back ON TOP OF the immutable 50k reserve. 2000=2h (default), 400=0.4h. Mirrors capital_multiplier_k_milli one layer down, deliberately: both spenders reserve against ONE observed outflow. 0 returns the guard to exactly the immutable floor — never below it. Applies next tick"},
		},
```

Add `growth_runway_milli_hours` to `FleetGrowthTunableDefaults()` so the anti-drift test in `container_ops_tune_test.go` passes.

**Then write the constancy sweeps** in `fleet_growth_act_test.go`. These are the tests that pin the ruling, and they replace the single-point live-state fixture that preceded it — a point fixture reads as a stable PROBE at the trough and a stable HEAVY at the peak, so it would pass whether or not the flapping was fixed.

```go
// THE PROPERTY THE RULING BUYS, asserted end-to-end through the coordinator rather than only
// through the pure predicate: the regime does not change as the fleet moves through a trade
// cycle. The live balance sweeps the whole observed band while the high-water mark — the peak of
// that same band — stays put, and every tick must land on the same wave.
//
// A REACHABLE FLEET IS HEAVY AT EVERY POINT IN ITS CYCLE.
func TestGrowthReconcile_ReachableFleetStaysHeavyAcrossTheCycle(t *testing.T) {
	for live := int64(119_000); live <= 1_500_000; live += 37_313 {
		sink := &recordingWaveSink{}
		h := newGrowthHandlerWith(t, growthFixture{
			lanes:     &fakeLanes{count: 9, readable: true},
			treasury:  live,
			highWater: 1_500_000, // the band's peak clears floor + entry for this ask
			yardAsk:   1_916_613,
		})
		h.SetMetricsSink(sink)

		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("live balance %d: %v", live, err)
		}
		if sink.waves[0] != common.WaveHeavy {
			t.Fatalf("regime flipped to %q at live balance %d — the wave is reading trade-cycle phase", sink.waves[0], live)
		}
	}
}

// AN UNREACHABLE FLEET IS PROBE AT EVERY POINT IN ITS CYCLE — including at a peak that clears
// price+margin momentarily. This is the §395 anti-deadlock regression at the coordinator level:
// a fleet that cannot plausibly reach the ask must never have its probe buying paused for it.
func TestGrowthReconcile_UnreachableFleetStaysProbeAcrossTheCycle(t *testing.T) {
	for live := int64(119_000); live <= 1_500_000; live += 37_313 {
		sink := &recordingWaveSink{}
		buyer := &recordingPurchaser{}
		h := newGrowthHandlerWith(t, growthFixture{
			lanes:     &fakeLanes{count: 9, readable: true},
			treasury:  live,
			highWater: 400_000, // peak far below floor + entry for a 1,916,613 ask
			yardAsk:   1_916_613,
		})
		h.SetMetricsSink(sink)
		h.SetPurchaser(buyer)

		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("live balance %d: %v", live, err)
		}
		if sink.waves[0] != common.WaveProbe || sink.reasons[0] != common.WaveProbeReasonUnreachable {
			t.Fatalf("at live balance %d expected PROBE/unreachable, got %q/%q", live, sink.waves[0], sink.reasons[0])
		}
		if buyer.calls != 0 {
			t.Fatalf("an unreachable fleet bought a heavy at live balance %d", live)
		}
	}
}

// The measure has to be able to say NO, or the sweeps above prove only that a constant is
// constant. A fleet whose peak clears the bar and one whose peak does not must land on
// different regimes at the SAME live balance.
func TestGrowthReconcile_HighWaterDiscriminatesAtOneLiveBalance(t *testing.T) {
	const live = int64(479_798)
	rich := &recordingWaveSink{}
	poor := &recordingWaveSink{}

	hRich := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: live, highWater: 1_500_000, yardAsk: 1_916_613,
	})
	hRich.SetMetricsSink(rich)
	hPoor := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: live, highWater: 400_000, yardAsk: 1_916_613,
	})
	hPoor.SetMetricsSink(poor)

	for _, h := range []*RunFleetGrowthCoordinatorHandler{hRich, hPoor} {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if rich.waves[0] != common.WaveHeavy || poor.waves[0] != common.WaveProbe {
		t.Fatalf("the high-water mark is not discriminating: rich=%q poor=%q at one live balance", rich.waves[0], poor.waves[0])
	}
}

// An unobservable window is PROBE, not a zero high-water. A quiet ledger must not read as a
// fleet that has never held money.
func TestGrowthReconcile_UnreadableHighWaterIsProbe(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 1_500_000,
		highWater: 0, highWaterReadable: false, yardAsk: 1_000_000,
	})
	h.SetMetricsSink(sink)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.waves[0] != common.WaveProbe || sink.reasons[0] != common.WaveProbeReasonCapacityUnreadable {
		t.Fatalf("expected PROBE/capacity_unreadable, got %q/%q", sink.waves[0], sink.reasons[0])
	}
}
```

- [ ] **Step 9: Write the metrics collector**

`gobot/internal/adapters/metrics/fleet_growth_metrics.go` mirrors `fleet_autosizer_metrics.go`'s collector shape (`:40-107`, `:253-283`). The two new series:

```go
			wave: newGaugeVec(
				"fleet_growth_wave",
				"The heavy/probe regime as derived THIS TICK: 1=HEAVY (probe buying paused so the treasury can reach a hull's ask), 0=PROBE. Emitted every tick by BOTH readers — label reader=growth is the coordinator, reader=drain is the probe buy queue — because the failure this series exists to catch is the two DISAGREEING, which a single-writer gauge cannot see. A gap in either reader is a stalled consumer, not a regime",
				"player_id", "reader",
			),
			waveProbeReason: newGaugeVec(
				"fleet_growth_wave_probe_reason",
				"Which clause forced PROBE this tick, one series per reason, 1=this reason 0=not. PROBE otherwise has one name and several meanings, and an operator watching probe buying continue cannot tell 'the fleet cannot plausibly reach a hull this era' from 'the lane surface is down' without it",
				"player_id", "reason",
			),
```

`RecordWave` sets the gauge to 1/0 and sets exactly one reason series to 1 with the rest to 0, so a stale reason cannot linger.

- [ ] **Step 10: Wire the composition root**

`gobot/internal/adapters/grpc/fleet_growth_ports.go` — `NewFleetGrowthCoordinatorHandler`, mirroring `fleet_autosizer_ports.go:34-123`. It reuses, unchanged: `autosizerTreasuryReader`, `autosizerAPIUtilReader`, `autosizerYardPriceReader`, `autosizerHeavyCensus`, `autosizerHeavyYardReader`, `autosizerHeavyYardCatalog`, `newAutosizerPricingErrand`, `autosizerPurchaser`, `autosizerNotifier`, `NewContainerConfigReader`, `health.NewStallEscalator`. Keep the loud `log.Printf` WARNs for each unwired port verbatim — an unwired census stops heavy buying, and that must never be silent.

`gobot/cmd/spacetraders-daemon/main.go` — construct the shared lane reader beside `heavyTargetFinder` (`:1168-1204`) and pass it to both:

```go
	// THE SHARED CAPACITY-SHORT SIGNAL. ONE instance, two consumers: the fleet-growth
	// coordinator (which SPENDS on it) and the sensing wave gate (which PAUSES on it). Two
	// readers of one quantity is how the spender and the withholder end up disagreeing about
	// whether the fleet is capacity-short. Constructed here, at the composition root, precisely
	// so a second one is conspicuous.
	unservedLaneReader := tradingQueries.NewUnservedLaneReader(shipRepo, tradingQueries.NewProfitableLaneReader(marketRepo))

	fleetGrowthHandler := grpc.NewFleetGrowthCoordinatorHandler(
		daemonServer, apiClient, ledgerTreasury, shipRepo, med, waypointRepo, captainEventRepo,
		reachableYardFinder, heavyTargetFinder, unservedLaneReader, transactionRepo,
	)
	if err := mediator.RegisterHandler[*fleetCmd.RunFleetGrowthCoordinatorCommand](med, fleetGrowthHandler); err != nil {
		return fmt.Errorf("failed to register FleetGrowthCoordinator handler: %w", err)
	}
```

- [ ] **Step 11a: Switch OFF the autosizer's heavy class**

The first of the three atomic changes. Edit `run_fleet_autosizer_coordinator.go:290-299`:

```go
// classDisabled reports whether a class is frozen by config. The HEAVY class is disabled here:
// the fleet-growth coordinator owns trade capacity, and two coordinators buying into one pool
// against one treasury would each judge affordability without seeing the other's spend.
func (c autosizerRunConfig) classDisabled(class HullClass) bool {
	switch class {
	case HullClassLight:
		return false
	default:
		return true
	}
}
```

and add a test in `run_fleet_autosizer_coordinator_test.go`:

```go
// EXACTLY ONE HEAVY BUYER. The growth coordinator owns trade capacity; the autosizer must not
// evaluate the heavy class at all while both are deployed.
func TestReconcile_HeavyClassIsDisabled(t *testing.T) {
	heavy := &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{Demand: 9, Current: 0, Readable: true}}
	h := newHandlerWith(heavy)
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if heavy.calls != 0 {
		t.Fatalf("the autosizer must not evaluate heavy demand, got %d calls", heavy.calls)
	}
}
```

- [ ] **Step 11b: REPLACE the heavy-buyer declaration**

The second atomic change, and the one that is easy to get wrong by adding instead of replacing. Edit `gobot/internal/domain/hullbuy/hull_class.go:73-75`:

```go
func HeavyBuyerContainers() []container.ContainerType {
	return []container.ContainerType{container.ContainerTypeFleetGrowth}
}
```

**Replaced, not appended.** The declaration answers "WHICH COORDINATORS BUY HEAVY HULLS", and as of Step 11a the autosizer does not. Two declared owners would let the cap resolve off whichever container id sorts first — see this task's preamble.

Add to `gobot/internal/domain/hullbuy/hull_class_test.go`:

```go
// EXACTLY ONE declared heavy buyer. Two would let the cap resolve off whichever container id
// sorts first rather than off the coordinator that actually spends, so the withholder and the
// spender could save toward different ceilings.
func TestHeavyBuyerContainers_DeclaresExactlyOneOwner(t *testing.T) {
	got := HeavyBuyerContainers()
	if len(got) != 1 {
		t.Fatalf("expected exactly one declared heavy buyer, got %v", got)
	}
	if got[0] != container.ContainerTypeFleetGrowth {
		t.Fatalf("the declared owner must be the coordinator that buys heavies, got %q", got[0])
	}
}
```

and add to `gobot/internal/adapters/parkedsensing/heavy_buyer_cap_port_test.go`:

```go
// A LEFTOVER autosizer container still carrying heavy_cap must not capture the cap while the
// declared owner is live. The scan passes it as a knob-carrying stranger and keeps going, so
// no undeclared-buyer WARN fires either.
func TestHeavyBuyerCapPort_LeftoverAutosizerDoesNotCaptureTheCap(t *testing.T) {
	db := seedContainers(t,
		runningContainer(container.ContainerTypeFleetAutosizer, map[string]any{"heavy_cap": 5}),
		runningContainer(container.ContainerTypeFleetGrowth, map[string]any{"heavy_cap": 9}),
	)
	got, present, exists, err := NewHeavyBuyerCapPort(db).HeavyCap(context.Background(), 1)
	if err != nil || !exists || !present || got != 9 {
		t.Fatalf("the declared owner's cap must win, got (%d,%v,%v,%v)", got, present, exists, err)
	}
}

// RUNNING still outranks PENDING for the declared owner, which is what keeps the cap stable
// across a restart: a PENDING replacement must not displace the live buyer mid-tick.
func TestHeavyBuyerCapPort_RunningGrowthOutranksPendingGrowth(t *testing.T) {
	db := seedContainers(t,
		pendingContainer(container.ContainerTypeFleetGrowth, map[string]any{"heavy_cap": 3}),
		runningContainer(container.ContainerTypeFleetGrowth, map[string]any{"heavy_cap": 9}),
	)
	got, _, _, err := NewHeavyBuyerCapPort(db).HeavyCap(context.Background(), 1)
	if err != nil || got != 9 {
		t.Fatalf("expected the RUNNING container's 9, got %d (%v)", got, err)
	}
}
```

Read `heavy_buyer_cap_port_test.go` first and match its existing `seedContainers` / `runningContainer` / `pendingContainer` helper signatures — sp-59cl1 shipped them.

- [ ] **Step 11c: Rename the stall seam**

The third piece, mechanical:

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
git mv internal/application/fleet/commands/fleet_autosizer_stall.go internal/application/fleet/commands/fleet_growth_stall.go
git mv internal/application/fleet/commands/fleet_autosizer_stall_test.go internal/application/fleet/commands/fleet_growth_stall_test.go
```

Retarget its receivers to `*RunFleetGrowthCoordinatorHandler`. **The seam stays write-only by type** — its one method returns nothing, so wiring it cannot give any sizing decision something to branch on (RULINGS #2). Do not add a return value.

- [ ] **Step 12: Run everything**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go build ./... && go vet ./... && gofmt -l . | head
go test ./internal/application/fleet/... ./internal/adapters/grpc/ ./internal/adapters/metrics/ ./internal/application/trading/... ./internal/domain/hullbuy/ ./internal/adapters/parkedsensing/ 2>&1 | grep -Ev '^(ok|\?)' | head -30
go test ./... -race 2>&1 | grep -Ev '^(ok|\?)' | head -30
```

Expected: all green. Any autosizer heavy test that now fails must be **deleted with the heavy class**, not adjusted to keep it alive.

- [ ] **Step 13: Mutation probes**

Each atomic, each naming its kills:
- `s|if !growthOn {|if false \&\& !growthOn {|` in `run_fleet_growth_reconcile.go` → must kill `TestGrowthReconcile_DisabledPublishesProbeAndBuysNothing` and `TestGrowthReconcile_DisabledReadsNoPrices`.
- `s|if wave != common.WaveHeavy {|if false {|` → must kill `TestGrowthReconcile_ProbeWaveBuysNoHeavy` and `TestGrowthReconcile_UnreachableFleetStaysProbeAcrossTheCycle`.
- `s|HighWaterTreasury:        in.highWater,|HighWaterTreasury:        in.treasury,|` → must kill **both** constancy sweeps. This re-creates the exact defect the ruling fixes, one layer up from Task 1's version of the same probe; if it kills nothing, the sweeps are not sweeping.
- `s|if h.highWater != nil {|if false {|` → must kill `TestGrowthReconcile_UnreadableHighWaterIsProbe` (an unwired reader must fail to PROBE, not silently read zero).
- `s|WorkingCapital: in.workingCapital,|WorkingCapital: 0,|` in `fleet_growth_act.go` → must kill `TestGrowthReconcile_WorkingCapitalBlocksTheBuy`.
- `s|if !in.workingCapitalOK {|if false {|` → must kill `TestGrowthReconcile_UnreadableOutflowBuysNothing`.
- `s|return true$|return false|` in the autosizer's `classDisabled` default arm → must kill `TestReconcile_HeavyClassIsDisabled`.
- In `hull_class.go`, `s|{container.ContainerTypeFleetGrowth}|{container.ContainerTypeFleetGrowth, container.ContainerTypeFleetAutosizer}|` → must kill `TestHeavyBuyerContainers_DeclaresExactlyOneOwner`. **This is the probe that matters**: appending instead of replacing is the plausible mistake, and this is what refuses it.

- [ ] **Step 14: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
make comment-audit-check ONLY="internal/application/fleet/commands internal/adapters/grpc internal/adapters/metrics internal/domain/container internal/domain/hullbuy internal/adapters/parkedsensing"
git add -A internal/ cmd/
git commit --no-verify -m "feat(fleetgrowth): the fleet-growth coordinator — wave predicate + heavy buy path

Drops into the autosizer's shape: same container plumbing, same fail-closed guard
stack, same pricing errand, one class. The wave is derived per tick from
common.DeriveWave and PUBLISHED every tick on every path including the paused one,
labelled by reader, because a HEAVY wave and a stalled coordinator otherwise look
identical and because the failure that matters is the two consumers disagreeing.

growth_enabled inherits sizing_enabled's 'off stops the READS' semantics AND a new
obligation the design missed: off forces the wave to PROBE, or probe buying pauses
forever for a buyer that cannot buy.

The working-capital term reaches guardAffordability and fails CLOSED on an
unreadable ledger. The anti-thrash streak stays on the BUY, never on the predicate:
two containers cannot share one in-memory streak, so a stateful predicate would
either store a derivation or split-brain.

THREE FACTS CHANGE ATOMICALLY: the growth coordinator starts buying heavies, the
autosizer's heavy class stops, and HeavyBuyerContainers is REPLACED so the cap
declaration follows the buyer. selectHeavyBuyer returns the first DECLARED owner in
RUNNING-then-id order, so two declared owners would resolve the cap off whichever
container id sorts first — the withholder saving toward a ceiling the spender never
consults. Any split of these three leaves a window where that is live.

6 mutation probes, each naming its kills."
```

- [ ] **Step 15: Deploy notes — record these on the bead**

Two things an operator will see, both expected, neither a fault:

1. **Between the binary landing and the growth container being launched**, no declared heavy buyer exists. The autosizer's leftover `heavy_cap` config makes it the knob-carrying stranger, so `sensing_heavy_cap_undeclared_buyer` WARNs each tick and the cap resolves off it. This is noise, not harm: no heavy buyer is running, so there is nothing to save for, and the reserve it produces stops reaching the drain's floor once Task 6 lands. **Launch the growth container promptly and the WARN stops.**
2. **The autosizer keeps ticking and keeps logging `autosizer_tick`**, sizing its light class against a demand that is structurally zero. That is unchanged from today and is Plan B's to remove.

Launch the coordinator and verify:

```bash
spacetraders tune --operation growth --show          # growth_enabled=1, heavy_cap resolved
# then confirm the WARN has stopped and the wave is being published by the growth reader.
```

---

### Task 5: Move the heavy-yard pricing errand

It transfers intact — there is no in-flight state to migrate, because both halves of "is an errand already under way?" are derived from durable ship rows every tick (`fleet_autosizer_heavy_pricing.go:26-29`). This task is a rename plus a receiver change plus proof that nothing about the errand's policy moved with it.

**Files:**
- Rename: `fleet_autosizer_heavy_pricing.go` → `heavy_pricing_errand.go` (+ `fleet_autosizer_heavy_pricing_test.go` → `heavy_pricing_errand_test.go`)
- Modify: the receiver on `runHeavyPricingErrand` and its helpers
- Modify: `gobot/internal/adapters/grpc/fleet_autosizer_heavy_ports.go` (the errand's adapter, retargeted)

**Interfaces:**
- Consumes: `HeavyYardCatalogReader.KnownHeavyYards` (`:113`), `HeavyPricingErrandPort` (`:117`), `parkedsensing.SensingParkedFleetTag` (`:59`), `KnownHeavyYard`, `PricingErrandHull`.
- Produces: `(*RunFleetGrowthCoordinatorHandler).runHeavyPricingErrand(ctx, cmd, cfg, in)` — same signature shape, new receiver. The autosizer no longer has one.

- [ ] **Step 1: Write the failing test — the errand runs on the PROBE wave**

Add to `gobot/internal/application/fleet/commands/heavy_pricing_errand_test.go`:

```go
// THE ERRAND RUNS ON THE PROBE WAVE, and that is not incidental — it is what lets the wave ever
// flip. Before any heavy is priced there is no target, so the predicate is PROBE; if the errand
// only ran on HEAVY, no yard would ever be priced and no HEAVY wave could ever occur.
func TestPricingErrand_RunsOnTheProbeWave(t *testing.T) {
	errand := &recordingErrand{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:   &fakeLanes{count: 0, readable: true}, // lanes served => PROBE
		catalog: unpricedHeavyYard("X1-AA-YARD"),
	})
	h.SetHeavyPricingErrandPort(errand)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if errand.dispatches != 1 {
		t.Fatalf("the errand must run on a PROBE wave, got %d dispatches", errand.dispatches)
	}
}

// It spends nothing and buys nothing — it makes a LATER tick's price readable, which is why it
// weakens no money guard by running before one can pass.
func TestPricingErrand_SpendsNothing(t *testing.T) {
	buyer := &recordingPurchaser{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 0, readable: true}, catalog: unpricedHeavyYard("X1-AA-YARD"),
	})
	h.SetHeavyPricingErrandPort(&recordingErrand{})
	h.SetPurchaser(buyer)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buyer.calls != 0 {
		t.Fatalf("the errand must spend nothing, got %d buys", buyer.calls)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/fleet/commands/ -run TestPricingErrand -v 2>&1 | tail -10
```

Expected: FAIL — the errand method is still on the autosizer handler.

- [ ] **Step 3: Move it**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
git mv internal/application/fleet/commands/fleet_autosizer_heavy_pricing.go internal/application/fleet/commands/heavy_pricing_errand.go
git mv internal/application/fleet/commands/fleet_autosizer_heavy_pricing_test.go internal/application/fleet/commands/heavy_pricing_errand_test.go
```

Change every `func (h *RunFleetAutosizerCoordinatorHandler)` in `heavy_pricing_errand.go` to `func (h *RunFleetGrowthCoordinatorHandler)`, and every `*RunFleetAutosizerCoordinatorCommand` parameter to `*RunFleetGrowthCoordinatorCommand`. Change `cfg autosizerRunConfig` to `cfg fleetGrowthRunConfig`. Delete `runHeavyPricingErrand`'s call site from `run_fleet_autosizer_reconcile.go:174` and the autosizer's `SetHeavyYardCatalogReader`/`SetHeavyPricingErrandPort` setters and fields.

**Do not touch the policy.** `heavyPricingErrandsInFlight = 1` (`:37`), `heavyPricingErrandFleet = parkedsensing.SensingParkedFleetTag` (`:59`), `pricingErrandCarrier`'s eligibility rules and the `MannedScoutPost` refusal all move verbatim. The cross-package const in particular: the allowlist and the tag-writer must be the same symbol, not two strings trusted to match.

Retarget the adapter in `gobot/internal/adapters/grpc/fleet_autosizer_heavy_ports.go` — rename the file to `heavy_yard_ports.go` and leave the types alone; only the handler they are wired onto changes (`fleet_growth_ports.go`, from Task 4).

- [ ] **Step 4: Verify the move changed nothing about the errand**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go build ./... && go vet ./...
go test ./internal/application/fleet/commands/ -v 2>&1 | grep -E '^(---|FAIL|ok)' | head -60
```

Run the package UNFILTERED. A `-run` on the errand's own words under-selects its file badly: eight of its tests name neither "Errand" nor "Pricing", including both carrier-eligibility tests, so a filtered run reads green while the rule that keeps the errand out of the trade pool is not being exercised at all.

Expected: **every pre-existing errand test passes with its assertions unchanged.** An assertion that had to be edited means this was not a move.

- [ ] **Step 5: Mutation probe**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
F=internal/application/fleet/commands/heavy_pricing_errand.go
echo "PRECHECK_DIRTY=[$(git status --porcelain)]" && \
cp $F /tmp/hpe.orig && \
sed -i '' 's|const heavyPricingErrandFleet = parkedsensing.SensingParkedFleetTag|const heavyPricingErrandFleet = parkedsensing.SensingParkedFleetTag + "_MUTANT"|' $F && \
grep -q 'SensingParkedFleetTag + "_MUTANT"' $F && echo "MUTATION APPLIED" && \
go test ./internal/application/fleet/commands/ 2>&1 | grep -E '^(---|FAIL|ok)' > /tmp/hpe.mut ; \
echo "MUT_TEST_RC=$?" ; grep -c FAIL /tmp/hpe.mut ; grep -E '^--- FAIL' /tmp/hpe.mut ; \
cp /tmp/hpe.orig $F && go test ./internal/application/fleet/commands/ 2>&1 | tail -2 && \
echo "RESTORED_DIRTY=[$(git status --porcelain)]"
```

Two things this probe gets right that the obvious version does not. It mutates the constant's VALUE rather than replacing it with `"trade"` — replacing it orphans the `parkedsensing` import, so the package does not build and a build failure is not a mutation kill. And it runs the package UNFILTERED, because the carrier-eligibility tests do not match a `-run` on the errand's own words.

Expected: kills the carrier-eligibility tests by name. If it kills nothing, the fixtures are tagging their carriers from the same symbol the predicate reads — the allowlist is being compared against itself, and a drift between it and the tag-writer would empty the eligible set with the suite still green. Fix the fixture (tag from the writer's symbol, and pin the wire value as a literal in at least one) before proceeding.

- [ ] **Step 6: Commit**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
make comment-audit-check ONLY="internal/application/fleet/commands internal/adapters/grpc"
git add -A internal/
git commit --no-verify -m "refactor(fleetgrowth): move the heavy-yard pricing errand onto the growth coordinator

A move, not a redesign: no in-flight state to migrate (both halves of 'is an errand
under way' are derived from durable ship rows), the one-hull-fleet-wide bound and
the single-tag carrier allowlist transfer verbatim. Every pre-existing errand test
passes with its assertions unchanged, which is what makes it a move.

It runs on the PROBE wave too, and that is load-bearing: before any heavy is priced
there is no target, so the predicate is PROBE — an errand gated on HEAVY could
never price the yard that would let the wave flip."
```

---


### Task 6: Wire the wave into the probe drain, and remove the live override

The lockstep lands here: two consumers, one predicate. The drain gains the gate and loses `heavyHold` from its `ProbeBuyFloor` capex term — two mechanisms doing one job is how they drift. It also surfaces `growth_enabled` to the drain, without which a switched-off buyer pauses probe buying forever.

**Files:**
- Modify: `gobot/internal/adapters/parkedsensing/heavy_reserve_port.go` (`HeavyBuyerCapPort.GrowthEnabled`) + `heavy_buyer_cap_port_test.go`
- Create: `gobot/internal/adapters/parkedsensing/wave_port.go` (+ test)
- Modify: `gobot/internal/application/parkedsensing/buyports.go:277-279` (`HeavyReserveReader` → wave reader), `:281-310` (`BuyKnobs` untouched, documented)
- Modify: `gobot/internal/application/parkedsensing/buyqueue.go:47-58`, `:87-100`, `:131`, `:189`, `:234-246`, `:284-299`
- Modify: `gobot/internal/application/scouting/commands/sensing_engine_ports.go:286-316`, `probe_sensing_heartbeat.go:119-140`
- Modify: `gobot/cmd/spacetraders-daemon/main.go:464`
- Test: `gobot/internal/application/parkedsensing/buyqueue_test.go`, `wave_lockstep_test.go`

**Interfaces:**
- Consumes: `common.DeriveWave` (Task 1); `ledger.TreasuryHighWaterReader` and `fleetgrowth.TradeCycleWindow` (Task 2); `queries.UnservedLaneReader` (Task 3); `hullbuy.HeavyBuyerContainers` and `HeavyBuyerCapPort.HeavyCap` (sp-59cl1, merged).
- Produces: `(*HeavyBuyerCapPort).GrowthEnabled(ctx, playerID int) (enabled bool, buyerExists bool, err error)`; `parkedsensing.WaveReader` with `Wave(ctx, playerID int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, error)`; `BuyReport.Wave`, `BuyReport.WaveProbeReason`.

- [ ] **Step 1: Write the failing test for the switch read**

Add to `gobot/internal/adapters/parkedsensing/heavy_buyer_cap_port_test.go`:

```go
// growth_enabled OFF must reach the drain. Otherwise the drain evaluates a wave for a buyer
// that has stopped reading and stopped buying, and pauses probe buying forever — the sp-zg71k
// deadlock re-created through the master switch.
func TestHeavyBuyerCapPort_GrowthDisabledIsReadable(t *testing.T) {
	db := seedContainers(t, runningContainer(container.ContainerTypeFleetGrowth, map[string]any{"growth_enabled": 2}))
	enabled, exists, err := NewHeavyBuyerCapPort(db).GrowthEnabled(context.Background(), 1)
	if err != nil || !exists || enabled {
		t.Fatalf("expected (false,true,nil), got (%v,%v,%v)", enabled, exists, err)
	}
}

// Absent key is the documented default: ON. `tune <key> 0` DELETES the key, so absence IS the
// revert, and a coordinator nobody has tuned must not read as switched off.
func TestHeavyBuyerCapPort_AbsentSwitchIsOn(t *testing.T) {
	db := seedContainers(t, runningContainer(container.ContainerTypeFleetGrowth, map[string]any{"heavy_cap": 5}))
	enabled, exists, err := NewHeavyBuyerCapPort(db).GrowthEnabled(context.Background(), 1)
	if err != nil || !exists || !enabled {
		t.Fatalf("an untuned coordinator must read as ON, got (%v,%v,%v)", enabled, exists, err)
	}
}

// NO HEAVY BUYER AT ALL is quiet and yields "no buyer" — a probe-only deployment must not warn
// every tick, which is the whole reason that rung is silent.
func TestHeavyBuyerCapPort_NoBuyerIsQuietAndNotEnabled(t *testing.T) {
	enabled, exists, err := NewHeavyBuyerCapPort(seedContainers(t)).GrowthEnabled(context.Background(), 1)
	if err != nil || exists || enabled {
		t.Fatalf("expected (false,false,nil), got (%v,%v,%v)", enabled, exists, err)
	}
}

// The switch and the cap must come off the SAME container. Reading them from two containers is
// the split this port was built to close, one knob later.
func TestHeavyBuyerCapPort_SwitchAndCapComeFromOneContainer(t *testing.T) {
	db := seedContainers(t,
		runningContainer(container.ContainerTypeFleetAutosizer, map[string]any{"heavy_cap": 5, "growth_enabled": 2}),
		runningContainer(container.ContainerTypeFleetGrowth, map[string]any{"heavy_cap": 9, "growth_enabled": 1}),
	)
	port := NewHeavyBuyerCapPort(db)
	cap, _, _, err := port.HeavyCap(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enabled, _, err := port.GrowthEnabled(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap != 9 || !enabled {
		t.Fatalf("both knobs must resolve off the declared owner, got cap=%d enabled=%v", cap, enabled)
	}
}
```

- [ ] **Step 2: Run it, then implement**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/adapters/parkedsensing/ -run TestHeavyBuyerCapPort -v 2>&1 | tail -15
```

Expected: FAIL on `GrowthEnabled` undefined; the pre-existing cap tests still pass.

Implement `GrowthEnabled` on `HeavyBuyerCapPort`, reusing the **same** query and `selectHeavyBuyer` the cap read uses so the two knobs can never come off different containers. Factor the shared "find the authoritative buyer and parse its config" half into one unexported helper and have both public methods call it — do not copy the query.

```go
// GrowthEnabled reports the heavy buyer's master switch, read off the SAME container the cap
// resolves from. buyerExists=false means no heavy buyer is deployed at all, which is a quiet,
// expected configuration (a probe-only deployment) rather than a fault.
//
// IT MUST REACH THE DRAIN. A switched-off buyer with a priced target would otherwise leave the
// drain pausing probe buying toward a purchase nothing can make — a deadlock no spender can
// clear. That is why the switch is the wave predicate's FIRST clause rather than a detail of
// the coordinator that owns it.
//
// 1=on, 2=off. Anything else — including the absent-key 0 — is the documented default, ON:
// `tune <key> 0` DELETES the key, so absence IS the revert.
func (p *HeavyBuyerCapPort) GrowthEnabled(ctx context.Context, playerID int) (bool, bool, error) {
```

- [ ] **Step 3: Write the failing drain tests**

Add to `gobot/internal/application/parkedsensing/buyqueue_test.go`:

```go
// THE WAVE GATE. On HEAVY the drain buys no probe — that pause is the entire reason the design
// has phases: a heavy is a lump and probes are a trickle, and without pausing the trickle the
// treasury never reaches the lump.
func TestDrainBuyQueue_HeavyWaveBuysNoProbe(t *testing.T) {
	ports := drainPortsWith(t, waveReader{wave: common.WaveHeavy})
	rep, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Bought != 0 {
		t.Fatalf("a HEAVY wave must buy no probe, bought %d", rep.Bought)
	}
	if rep.Wave != common.WaveHeavy {
		t.Fatalf("the report must carry the wave, got %q", rep.Wave)
	}
}

// THE FREE HALF STILL RUNS. Pausing purchases must not pause re-tasking an idle spare into a
// placement or flying a surplus hull across a gate to a foothold — both cost zero credits and
// zero API calls, and stopping them would make the pause expensive.
func TestDrainBuyQueue_HeavyWaveStillRunsTheFreeHalf(t *testing.T) {
	ports := drainPortsWith(t, waveReader{wave: common.WaveHeavy}, withReusableSpareHull())
	rep, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Reused == 0 {
		t.Fatalf("a HEAVY wave must still re-task idle spares, got %d", rep.Reused)
	}
}

// A HEAVY wave costs NO money read. The treasury read is an API call, and a tick that may not
// buy must not price anything — the same rule the operator spend gate already follows.
func TestDrainBuyQueue_HeavyWaveReadsNoTreasury(t *testing.T) {
	treasury := &countingTreasury{}
	ports := drainPortsWith(t, waveReader{wave: common.WaveHeavy}, withTreasury(treasury))
	if _, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if treasury.calls != 0 {
		t.Fatalf("a HEAVY wave must cost no treasury read, got %d", treasury.calls)
	}
}

// ON THE PROBE WAVE THE FLOOR NO LONGER CARRIES THE HEAVY HOLD. The wave binary-gates probe
// buying, so HoldAt's ramp as a floor term is redundant — and two mechanisms doing one job is
// how they drift.
func TestDrainBuyQueue_ProbeWaveFloorExcludesTheHeavyHold(t *testing.T) {
	// A target well within reach: HoldAt would return a large hold if it still reached the floor.
	ports := drainPortsWith(t, waveReader{wave: common.WaveProbe, target: 1_000_000}, withTreasury(&countingTreasury{credits: 5_000_000}))
	rep, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.HeavyReserveHeld != 0 {
		t.Fatalf("the heavy hold must no longer reach the floor, held %d", rep.HeavyReserveHeld)
	}
	if rep.Bought == 0 {
		t.Fatalf("a PROBE wave with headroom must buy")
	}
}

// THE IMMUTABLE FLOOR STILL BINDS on the PROBE wave. Removing the heavy hold removes ONE term;
// it does not touch the guard underneath it (RULINGS #4).
func TestDrainBuyQueue_ProbeWaveStillHoldsTheImmutableFloor(t *testing.T) {
	ports := drainPortsWith(t, waveReader{wave: common.WaveProbe}, withTreasury(&countingTreasury{credits: common.ImmutableReserveFloor + 1}))
	rep, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Bought != 0 || !rep.FloorHeld {
		t.Fatalf("the immutable floor must still bind, bought=%d floorHeld=%v", rep.Bought, rep.FloorHeld)
	}
}

// THE §395 ANTI-DEADLOCK REGRESSION at the drain. A fleet whose PEAK treasury across a trade
// cycle cannot reach the ask must keep buying probes, behind its own unchanged floor — and it
// must do so at every point in the cycle, including the moments it is momentarily flush.
func TestDrainBuyQueue_UnreachableHeavyDoesNotPauseProbes(t *testing.T) {
	for live := int64(119_000); live <= 1_500_000; live += 137_119 {
		ports := drainPortsWith(t,
			waveReader{wave: common.WaveProbe, reason: common.WaveProbeReasonUnreachable, target: 1_916_613},
			withTreasury(&countingTreasury{credits: live}))
		rep, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock())
		if err != nil {
			t.Fatalf("live balance %d: %v", live, err)
		}
		if rep.Wave != common.WaveProbe {
			t.Fatalf("an unreachable heavy must leave the drain on PROBE at live balance %d, got %q", live, rep.Wave)
		}
	}
}

// THE WAVE READ COSTS NO API CALL. Every read behind the wave port is a local DB query — that is
// what lets it sit ahead of the cheapest-first gate order, and it is the property that would
// break if the regime were ever re-derived from a live balance.
func TestDrainBuyQueue_WaveReadCostsNoTreasuryCall(t *testing.T) {
	treasury := &countingTreasury{}
	ports := drainPortsWith(t, waveReader{wave: common.WaveHeavy}, withTreasury(treasury), withEmptyQueue())
	if _, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if treasury.calls != 0 {
		t.Fatalf("deriving the wave must cost no treasury read, got %d", treasury.calls)
	}
}

// THE OPERATOR SPEND SWITCH IS UNCHANGED and still independent of the wave: expansion_enabled
// off buys nothing whatever the regime says.
func TestDrainBuyQueue_SpendSwitchStillBindsOnTheProbeWave(t *testing.T) {
	k := buyKnobsAllowingSpend()
	k.SpendEnabled = false
	ports := drainPortsWith(t, waveReader{wave: common.WaveProbe})
	rep, err := DrainBuyQueue(context.Background(), ports, 1, k, fixedClock())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Bought != 0 || !rep.SpendingPaused {
		t.Fatalf("the operator switch must still bind, bought=%d paused=%v", rep.Bought, rep.SpendingPaused)
	}
}

// FAIL-CLOSED ON AN UNREADABLE WAVE, and closed here means "buy nothing this tick". An erroring
// wave port is a swappable seam whose zero cannot be trusted.
func TestDrainBuyQueue_UnreadableWaveBuysNothing(t *testing.T) {
	ports := drainPortsWith(t, waveReader{err: errors.New("wave unreadable")})
	rep, err := DrainBuyQueue(context.Background(), ports, 1, buyKnobsAllowingSpend(), fixedClock())
	if err == nil {
		t.Fatalf("an unreadable wave must abort the tick")
	}
	if rep.Bought != 0 {
		t.Fatalf("bought %d on an unreadable wave", rep.Bought)
	}
}
```

Then the **lockstep test**, in a new file `gobot/internal/application/parkedsensing/wave_lockstep_test.go`:

```go
// LOCKSTEP — the design's single most important invariant. The growth coordinator's heavy buyer
// and this drain must read ONE predicate. Given identical inputs the two must return identical
// verdicts across the whole input space; a divergent second derivation in either fails HERE
// rather than in the economics.
func TestWaveLockstep_BothConsumersAgree(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, lanes := range []int{0, 1, 7} {
			for _, readable := range []bool{true, false} {
				for _, ask := range []int64{0, 1_000_000, 1_916_613} {
					for highWater := int64(0); highWater <= 4_000_000; highWater += 137_119 {
						in := common.WaveInputs{
							GrowthEnabled:         enabled,
							UnservedLanes:         lanes,
							UnservedLanesReadable: readable,
							Target: common.HeavyReserve(common.HeavyReserveInputs{
								CapabilityOpen: ask > 0, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: ask,
							}),
							HighWaterTreasury: highWater, HighWaterReadable: true,
						}
						want, wantReason := common.DeriveWave(in)
						// The drain reaches the predicate through its port; the coordinator reaches
						// it directly. Both must land on the same answer for the same facts.
						got, gotReason := waveThroughTheDrainPort(t, in)
						if got != want || gotReason != wantReason {
							t.Fatalf("split-brain at %+v: coordinator=%q/%q drain=%q/%q", in, want, wantReason, got, gotReason)
						}
					}
				}
			}
		}
	}
}

// EXACTLY ONE DEFINITION. A second DeriveWave call site outside the coordinator and the wave
// port is a second assembly of the predicate's inputs, which is how the two consumers drift
// even while both call the same pure function.
func TestWaveLockstep_ExactlyOneDefinitionAndTwoReaders(t *testing.T) {
	sites := grepNonTest(t, "DeriveWave")
	if len(sites) != 3 {
		t.Fatalf("expected the definition plus exactly two readers, got %d: %v", len(sites), sites)
	}
}
```

- [ ] **Step 4: Run to verify failure**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go test ./internal/application/parkedsensing/ -run 'TestDrainBuyQueue_.*Wave|TestWaveLockstep' -v 2>&1 | tail -15
```

Expected: FAIL — `rep.Wave` undefined, `waveReader` undefined.

- [ ] **Step 5: Widen the port**

`gobot/internal/application/parkedsensing/buyports.go` — replace `HeavyReserveReader`:

```go
// WaveReader answers which regime this tick is in, and what the fleet is saving toward.
//
// IT RETURNS THE WHOLE ANSWER, not the inputs to it. The drain must not assemble a second
// WaveInputs and call DeriveWave itself: two assemblies of one predicate is exactly the
// split-brain the one-definition rule exists to prevent, and the port is the seam where the
// coordinator's facts and the drain's meet.
//
// A read ERROR fails CLOSED — the tick buys nothing. This is a swappable seam carrying an error
// in its contract, so the drain cannot assume which implementation it holds and must not treat
// an erroring reader's zero as authoritative.
type WaveReader interface {
	Wave(ctx context.Context, playerID int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, error)
}
```

Rename the `BuyPorts` field from `HeavyReserve` to `Wave` and update `sensing_engine_ports.go:286-316` — the field no longer only reports a reserve, and a name that lies is how the next reader misuses it.

**`BuyKnobs` is untouched.** `SpendEnabled` stays exactly what it is: the operator's switch, independent of the regime. Add one line to its doc noting the wave is a separate gate beside it, so a future reader does not fold the two together.

- [ ] **Step 6: Gate the drain**

`buyqueue.go` — replace the `readHeavyReserve` call at `:47-58` with the wave read, in the same position (ahead of every gate, so the report is populated on every return path):

```go
	// The WAVE is read FIRST, ahead of every gate, so rep.Wave is populated on EVERY return path
	// — including the ones that stop before a floor is built. It is published beside the growth
	// coordinator's own per-tick gauge and the two must not disagree merely because a tick took a
	// short path.
	wave, waveReason, heavyTarget, err := readWave(ctx, p, playerID)
	if err != nil {
		return rep, err
	}
	rep.Wave, rep.WaveProbeReason, rep.HeavyReserveTarget = wave, waveReason, heavyTarget
```

Introduce ONE local beside the existing spend gate at `:87` and use it at all four sites (`:93`, `:131`, `:189`), so a future edit cannot update three of them:

```go
	// THE WAVE GATE, beside the operator's spend switch and for the same reason: a tick that may
	// not buy must not price anything either.
	//
	// WHY A BINARY GATE AND NOT A PRIORITY ORDERING. A heavy is a lump and probes are a trickle.
	// Interleaving the two spenders means the treasury never reaches the lump, which is the entire
	// condition this design exists to remove — a reader who "simplifies" the wave into a per-class
	// priority restores it exactly.
	//
	// It pauses PURCHASES ONLY. The free half below still re-tasks an idle spare into a placement
	// and still flies a surplus hull across a gate to a foothold, both at zero credits and zero API
	// calls, so a paused wave keeps coverage growing on hulls already paid for.
	mayBuy := k.SpendEnabled && wave != common.WaveHeavy
	rep.SpendingPaused = !mayBuy
```

- [ ] **Step 7: Drop `heavyHold` from the floor**

`buyqueue.go:284-299` — `openDrainBudget` no longer takes `heavyTarget` and no longer computes `heavyHold`:

```go
	// THE WAVE OWNS THE HEAVY HOLD-BACK NOW. HoldAt's ramp as a capex term is redundant beside a
	// binary gate: the drain either runs at full speed on the PROBE wave or does not run at all on
	// the HEAVY one. Two mechanisms doing one job is how they drift, and the ramp was the one that
	// could only ever half-stop the trickle.
	//
	// NO GUARD IS RELAXED (RULINGS #4). The immutable floor, the committed-capex reserve, the cargo
	// runway term and the probe cap are all untouched and all still bind on this tick.
	return drainState{
		credits:  credits,
		owned:    owned,
		probeCap: int64(k.ProbeCap),
		floor: domainSensing.ProbeBuyFloor(
			common.ImmutableReserveFloor,
			k.CapexReserve,
			domainSensing.CargoSpendPerHour(spend),
			k.KMilli,
		),
	}, false, nil
```

Delete `drainState.heavyHold` and `BuyReport.HeavyReserveHeld`, and update `probe_sensing_heartbeat.go:131-135`'s `FloorHeld && HeavyReserveHeld > 0` arm in the same edit — leaving a field that always reports 0 is a gauge that lies.

- [ ] **Step 8: Write the adapter**

`gobot/internal/adapters/parkedsensing/wave_port.go`:

```go
// WavePort derives the regime from the same facts the growth coordinator reads, through the same
// shared instances: the same heavy target finder, the same unserved-lane reader, and the heavy
// buyer's own container row for the cap and the switch. The predicate itself is
// common.DeriveWave — this port assembles its inputs and calls it once.
func (p *WavePort) Wave(ctx context.Context, playerID int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, error) {
	enabled, buyerExists, err := p.caps.GrowthEnabled(ctx, playerID)
	if err != nil {
		return "", "", 0, err
	}
	// No heavy buyer deployed at all is a quiet, expected configuration — a probe-only deployment
	// — and it means there is nothing to save for.
	if !buyerExists {
		return common.WaveProbe, common.WaveProbeReasonGrowthDisabled, 0, nil
	}
	target, err := p.reserve.Reserve(ctx, playerID)
	if err != nil {
		return "", "", 0, err
	}
	lanes, lanesOK, err := p.lanes.UnservedLaneCount(ctx, playerID)
	if err != nil {
		return "", "", 0, err
	}
	// Demonstrated capacity, over the SAME window the growth coordinator measures. The two ports
	// read one ledger with one window, which is what keeps the two consumers on one answer.
	highWater, highWaterOK, err := p.highWater.TreasuryHighWaterSince(ctx, pid, p.clock.Now().Add(-fleetgrowth.TradeCycleWindow))
	if err != nil {
		return "", "", 0, err
	}
	wave, reason := common.DeriveWave(common.WaveInputs{
		GrowthEnabled: enabled,
		UnservedLanes: lanes, UnservedLanesReadable: lanesOK,
		Target:            target,
		HighWaterTreasury: highWater,
		HighWaterReadable: highWaterOK,
	})
	return wave, reason, target, nil
}
```

**The ruling removed a cost this task previously carried.** An earlier draft derived the regime from the live balance, which forced a `LiveCredits` call — an API read — into the drain's cheapest-first gate order, breaking the property that a tick with nothing to buy costs no API call. **The high-water read is a local DB query**, so that property survives untouched: every read behind this port is local, which is exactly why it can sit above the gate order at all, matching the placement `readHeavyReserve` already had. Note this explicitly in the commit message — it is a real improvement the ruling bought, not merely a neutral swap.

Wire it in `main.go:464`, replacing `NewHeavyReservePort(...)` with the wave port over the **same** `heavyTargetFinder` and the **same** `unservedLaneReader` instances constructed in Task 4.

- [ ] **Step 9: Publish the drain's wave, and name it in the heartbeat**

The drain's metrics port (`parkedSensingAdapters.NewMetricsPort()`, wired `main.go:1427`) records `fleet_growth_wave{reader="drain"}` every tick on every path, using the same collector Task 4 created. Add to `probe_sensing_heartbeat.go:119-140`, **above** the `SpendingPaused` arm:

```go
	case hb.buy.Wave == common.WaveHeavy:
		// Named before the switch arm because it is the more specific answer: an operator reading
		// "expansion switch off" when the real reason is "the fleet is saving for a heavy" would
		// go looking for a knob nobody touched.
		held = "heavy wave: probe buying is paused while the treasury climbs toward a heavy hull"
```

- [ ] **Step 10: Run everything, including the lockstep**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
go build ./... && go vet ./... && gofmt -l . | head
go test ./internal/application/parkedsensing/ ./internal/adapters/parkedsensing/ ./internal/application/scouting/... 2>&1 | grep -Ev '^(ok|\?)' | head -30
go test ./... -race 2>&1 | grep -Ev '^(ok|\?)' | head -30
```

- [ ] **Step 11: Mutation probes**

- `s|mayBuy := k.SpendEnabled \&\& wave != common.WaveHeavy|mayBuy := k.SpendEnabled|` → must kill `TestDrainBuyQueue_HeavyWaveBuysNoProbe` and `TestDrainBuyQueue_HeavyWaveReadsNoTreasury`.
- `s|mayBuy := k.SpendEnabled \&\& wave != common.WaveHeavy|mayBuy := wave != common.WaveHeavy|` → must kill `TestDrainBuyQueue_SpendSwitchStillBindsOnTheProbeWave`. The two probes together prove the conjunction, which one probe cannot.
- `s|k.CapexReserve,|k.CapexReserve + int64(heavyTarget),|` (re-adding the hold) → must kill `TestDrainBuyQueue_ProbeWaveFloorExcludesTheHeavyHold`.
- In `wave_port.go`, `s|GrowthEnabled: enabled,|GrowthEnabled: true,|` → must kill `TestWaveLockstep_BothConsumersAgree`. **This is the probe that matters**: it is the split-brain the lockstep test exists for.
- In `wave_port.go`, `s|HighWaterTreasury: highWater,|HighWaterTreasury: 0,|` → must kill `TestWaveLockstep_BothConsumersAgree`. The two ports must feed the predicate the same facts, not merely call the same function.

- [ ] **Step 12: Commit the code**

```bash
cd /Users/andres.dandrea/IdeaProjects/cities/spacetraders/gobot
make comment-audit-check ONLY="internal/application/parkedsensing internal/adapters/parkedsensing internal/application/scouting/commands"
git add -A internal/ cmd/
git commit --no-verify -m "feat(sensing): gate the probe drain on the wave; drop the heavy hold from its floor

The lockstep lands: the drain and the growth coordinator read ONE predicate through
one port over the SAME shared lane reader and heavy target finder, and a divergent
second derivation now fails the suite rather than the economics.

The gate is BINARY and pauses PURCHASES ONLY — the free half still re-tasks idle
spares and flies footholds at zero credits, so a paused wave keeps coverage growing
on hulls already paid for. It sits beside the operator spend switch, which is
unchanged and still independent, so a tick that may not buy prices nothing.

growth_enabled now resolves off the SAME container row as heavy_cap, so the drain
can see a switched-off buyer. Without it the drain would pause probe buying toward a
purchase nothing can make.

HoldAt's ramp leaves ProbeBuyFloor's capex term: a binary gate and a ramp are two
mechanisms doing one job. No guard is relaxed — the immutable floor, the committed
capex, the runway term and the probe cap all still bind (RULINGS #4).

The wave read costs NO API call: every read behind the port is a local DB query,
including the high-water one, so the drain keeps the property that a tick with
nothing to buy prices nothing. That is the placement readHeavyReserve already had,
and the ruled measure is what preserved it — a regime derived from a live balance
would have forced a LiveCredits call into the cheapest-first gate order.

5 mutation probes, each naming its kills."
```

- [ ] **Step 13: REMOVE THE LIVE OVERRIDE — after deploy, not before**

`expansion_enabled = 2` was set by hand on staging as a stand-in for the HEAVY wave. Nothing unwinds it, so probe buying stays off forever unless this step runs. It runs **last**, after the wave gate is deployed: removing it earlier resumes probe buying with nothing waving it.

```bash
# 1. Confirm the override is still there and read its current value.
spacetraders tune --operation sensing --show

# 2. DELETE the override rather than pinning it to 1. `tune <key> 0` deletes the key, which
#    reverts to the documented default (ON) fleet-wide; pinning 1 would leave a live override
#    that looks deliberate to the next operator.
spacetraders tune --operation sensing expansion_enabled 0

# 3. Verify it is gone, not set to something.
spacetraders tune --operation sensing --show | grep -i expansion_enabled

# 4. Verify the wave is now what pauses probe buying: fleet_growth_wave must be present for
#    BOTH reader="growth" and reader="drain" and must agree, and the sensing heartbeat's held
#    reason must name the wave rather than the switch.
```

Record the removal on the bead. Per RULINGS #19, an override left in place is live fleet state, and a knob that ships without its override being reconciled is not delivered.

---

## Plan B preconditions

Plan B is spec migration steps 6 (delete the autosizer) and 7 (verify no orphaned demand). It is deliberately not planned here: deletion tasks written now would speculate about what Plan A actually produces, and the Kept surface (spec §343) is far easier to enforce against real code than against a plan.

### The blocking sequencing constraint

**`sp-59cl1` must be merged before the autosizer container is deleted.** As of this writing it **is** — `7698c79f` in main (the gate rewrote `f57aaa96`, so ancestry fails while the work is present; verify by tree, not ancestry). The dependency is recorded explicitly rather than left implied because getting it wrong is invisible:

Before sp-59cl1, `AutosizerCapPort` resolved `heavy_cap` by querying `container_type = ContainerTypeFleetAutosizer`, and `resolveHeavyCap`'s `!containerExists` rung returned `(0, false)` **silently** — deliberately so, on the documented premise "no autosizer ⇒ no heavy buyer ⇒ nothing to save for", because a probe-only deployment would otherwise warn every tick. The epic makes that premise false. Delete the autosizer container without the re-home and the cap resolves to zero, the heavy reservation with it, **while a real heavy buyer runs** — a dormant-knob failure with no error surface in any gauge or heartbeat. That is the entire reason the bead exists.

Plan A already discharges the second half of it: Task 4 Step 11b replaces the declaration so `HeavyBuyerContainers()` names only the growth coordinator. **Plan B therefore needs no further declaration edit** — deleting `ContainerTypeFleetAutosizer` will not touch that list. If Plan A is executed differently and the declaration ends up naming both types, Plan B must fix that first.

### What must be true before Plan B starts

1. **Plan A is deployed and the wave has been observed working**, not merely merged: `fleet_growth_wave` present for `reader="growth"` and `reader="drain"`, agreeing, across at least one full HEAVY→PROBE transition. Deleting the old buyer before the new one is observed buying leaves no fallback.
2. **`hullbuy.HeavyBuyerContainers()` returns exactly `{ContainerTypeFleetGrowth}`** and the `sensing_heavy_cap_undeclared_buyer` WARN is silent.
3. **The growth container is RUNNING** and `spacetraders tune --operation growth --show` resolves `heavy_cap` and `growth_enabled`.
4. **The `expansion_enabled` override is removed** (Task 6 Step 13) and probe buying is governed by the wave.
5. **The kept surface has been re-verified against the code as it then stands**, not against this plan. In particular the contract scaler's two dedicate-at-purchase behaviours — `BuyAndHome` tagging `"contract"` (`contract_scaler_ports.go:166-169`) and `BuyHull` deliberately leaving the hull **untagged** so the warehouse/stocker depot grower re-dedicates (`:188-191`) — must pass with their assertions unedited. Note this surface changed under the sp-tg9no slice-1 revert; read it fresh.

### What Plan A knowingly leaves dead, for Plan B to remove

Recorded now so Plan B does not have to rediscover it:

- **The autosizer's heavy demand provider** stays registered at `fleet_autosizer_ports.go:57` but is skipped by `classDisabled`. It emits nothing and nothing latches it, so it is inert, not orphaned — but it is dead weight and its `autosizerHeavySources` wrapper exists only to delegate to the promoted reader.
- **The autosizer's light demand class**, still running: `DesiredChains` counts `goods_factory_coordinator` containers, a type in `retiredCommandTypes`; `Vacancies` returns a hardcoded 0. Demand is structurally always 0, yet the provider is evaluated every tick at the cost of a full-fleet read and a container-list read.
- **The autosizer's heavy-side tune knobs** — `sizing_enabled` and `heavy_cap` on `ContainerTypeFleetAutosizer` (`container_ops_tune.go:99-101`) — now name a coordinator that does not buy heavies. `heavy_cap` there is actively misleading: it resolves nothing, because the declaration points elsewhere.
- **Autosizer launch keys** for heavy-only knobs in `fleetAutosizerConfigKeys` (`autosizer_heavy_cap`, `autosizer_heavy_unserved_lanes_min`, `autosizer_heavy_treasury_pct_per_purchase`, `autosizer_max_price_heavies`, `autosizer_ship_type_heavies`).
- **`fleet_autosizer_heavies.go`** (`HeavyDemandProvider`, `computeHeavyDemand`) and the autosizer's heavy tick-input reads.
- **The autosizer's leftover container config** carrying `heavy_cap` — the knob-carrying stranger the cap read passes over. Harmless while a declared owner is live; it becomes the authority the moment one is not.

### What Plan B must do that Plan A cannot

- **Add `fleet_autosizer` to `retiredCommandTypes`** (`daemon_server_recovery.go:27`). Without it a persisted RUNNING row has no builder, marks `recovery_failed`, and alarms as an unexplained loss on the first post-retirement boot.
- **Retarget bootstrap's hand-off** (`bootstrap_ports_gate.go:446`) and its running-buyer observation (`bootstrap_ports_observe.go:221`, `obs.AutosizerRunning`). A hand-off to a deleted container type launches nothing, silently.
- **Rename the shared purchaser** — `adapters/grpc/fleet_autosizer_ports.go`'s `autosizerPurchaser` (`:236`) is the contract scaler's buy primitive. Rename the file and type; never delete.
- **Write the structural retirement guard first and prove it FAILS before the deletion**, following `off_gate_retired_test.go:25-35`'s calibration (module-root walk, file-count floor, `main.go` sentinel). A guard that passes before the work is a guard aimed at nothing.
- **Close two guard gaps found during Plan A's planning.** `off_gate_retired_test.go` guards nine identifiers but misses `HullClassExplorer`, `dispatchExplorer`, `reachesAny`, `OffGateWarped` and `stallReasonOffGateNoTarget`. `HullClassExplorer` is the one that matters: it is dead vocabulary at `hull_class.go:23` with a **live** `DedicatedFleet` arm at `:47`, and it is the one re-entry point nothing is watching. Deleting the constant outright is preferable to guarding it.
- **Verify no orphaned demand** — enumerate every `DemandSink`/`DemandBridge`/`DemandSource`/`DemandProvider` with both ends named. `ContractDeliveryDemandBridge` specifically: the capacity reconciler emits onto it and its only reader was the autosizer's `HullClassContractDelivery` arm, which never had a provider. If it ends with no reader it must be **retracted** and deleted, not left latching — a test asserting "nobody reads it" passes against a sink still holding `Demanded: true`.
- **Clean two cosmetic stragglers:** `gobot/docs/refactoring/lanes/adapters-misc.md:31` references a deleted file, and `probe_sensing_stall.go:29`'s `expansionStallCoordinator = "off_gate_expansion"` now names a gate-only pass. Rename the constant, keep the emitted label, or coordinate the dashboard change — do not silently break a panel.

### Does splitting at the 3/6 boundary break the spec's ordering invariant?

**No — with one condition, which Plan A satisfies.** The spec's ordering exists so no step leaves a latched bridge, an unserved demand, or a silently-zeroed reserve. At Plan A's end state:

| Invariant | Status at Plan A's end |
|---|---|
| No latched bridge | **Holds.** The explorer bridge and every producer are already deleted. Plan A creates no new sink. `ContractDeliveryDemandBridge`'s situation is unchanged — the autosizer still exists, so nothing about its (absent) reader changes. |
| No unserved demand | **Holds.** The autosizer's heavy provider is registered but skipped by `classDisabled`; it is never called and emits nothing, so there is no signal for anything to latch. The light provider keeps running against a structurally-zero demand, exactly as it does today. |
| No silently-zeroed reserve | **Holds ONLY because Task 4 Step 11b replaces the declaration.** This is the condition. `selectHeavyBuyer` returns the first DECLARED owner in `RUNNING`-then-`id ASC` order; with both types declared and both containers running, the cap would resolve off whichever container id sorts first — possibly the autosizer, which no longer buys heavies. The withholder would then be saving toward a ceiling the spender never consults. Replacing rather than appending removes that entirely, and is quiet: the scan passes the autosizer's leftover config as a knob-carrying stranger and still returns growth as authoritative, so no undeclared-buyer WARN fires. |

The one genuinely new exposure the split creates is the **deploy window** between the Plan A binary landing and the growth container being launched: no declared owner exists, the autosizer's leftover `heavy_cap` makes it the knobbed stranger, and `sensing_heavy_cap_undeclared_buyer` WARNs each tick. It is noise rather than harm — no heavy buyer is running, so there is nothing to save for, and once Task 6 lands the reserve no longer reaches the drain's floor at all. Launch the growth container promptly. This is recorded in Task 4 Step 15.

**One thing the split makes strictly better.** The spec's ordering has the autosizer alive through step 5 anyway, so Plan A's end state is a state the spec's own sequence passes through. Stopping there rather than continuing to deletion means the Kept-surface contract gets enforced against real, running code — the growth coordinator demonstrably compiling and buying against a guard stack the autosizer still shares — instead of against a plan's assertion that it will.

---

## Self-review

**Spec coverage for steps 1–3.** Wave predicate → Task 1. Working-capital formula and its money guard → Tasks 2, 3. Architecture and inherited container facilities → Task 4. Pricing errand → Task 5. Drain wiring, `heavyHold` removal, lockstep → Task 6. One-definition-two-consumers → Tasks 1, 4, 6. Kept surface → Tasks 3, 4, 5 plus the Plan B preconditions. The spec's testing table maps across every task's TDD steps, with the §395 anti-deadlock regression appearing in Task 1 (predicate), Task 4 (coordinator) and Task 6 (drain), restated on the ruled measure in all three. All seven open questions are resolved above: six decided, one answered in merged code; clause 4 is ruled separately and its reasoning is recorded in full.

**Deviations from the spec, each justified.**
1. **Steps 4 and 5 are dropped as tasks** — both merged before planning finished (`ce82ab64`, `7698c79f`). Plan A consumes step 5's mechanism; Plan B re-verifies step 4's guard gaps.
2. **Migration step 1 is split** into Tasks 1–4, because the pure math, the promoted query and the guard field must be proven behaviour-identical against the *live* autosizer before anything is built on them. Ordering safety is unaffected: no heavy buyer changes until Task 4's final commit.
3. **The purchase guard stack is kept rather than moved**, and the growth coordinator lives in the same package. The alternative is a second money-guard implementation. This adds a third entry to the spec's two-item Kept surface, and Task 3 renames the files so a later pattern-based deletion cannot sweep them.
4. **Task 4 reaches into step 5's territory** for one line — the `HeavyBuyerContainers` replacement — because splitting it from the heavy-class switch-off leaves the reserve resolving off a container that no longer buys. Argued in full under "Does splitting at the 3/6 boundary break the spec's ordering invariant?".
5. **Clause 4 is neither the spec's form nor the `surplus > 0` this plan first recommended.** It is ruled to a high-water reachability test, argued in full at the top. The spec's form makes the regime a function of trade-cycle phase; `surplus > 0` would have removed the §395 regression along with the flapping. The ruled form keeps `HoldAt` as the single definition of the entry arithmetic and changes only which balance the reachability question is asked about.
6. **Task 2 grew a ledger port.** The high-water read needs `MAX(balance_after)` over a window, and `TransactionRepository` carries no aggregate. It lands as a narrow side port beside `GateFeeAggregator`, following that port's documented reasoning about not obliging a dozen unrelated fakes to grow a method. This is the re-cost the ruling authorised; it did not need its own task, because splitting it would give two tasks touching the same table, the same model and the same window.

**Type consistency check.** `DeriveWave(WaveInputs) (Wave, WaveProbeReason)` — Task 1 defines, Tasks 4 and 6 call, Task 6 asserts the call-site count is exactly 3. `Reachable(HeavyReserveTarget, int64) bool` — Task 1 defines, `DeriveWave` is its only production caller. `WaveInputs.HighWaterTreasury`/`HighWaterReadable` — Task 1 defines, Tasks 4 and 6 populate; `LiveTreasuryForReporting` is populated by Task 4 and deliberately read by no clause. `TreasuryHighWaterSince(ctx, shared.PlayerID, time.Time) (int64, bool, error)` — Task 2 defines on `ledger.TreasuryHighWaterReader`, Tasks 4 and 6 consume through ONE instance. `fleetgrowth.TradeCycleWindow` — Task 2 defines, three consumers reference it. `WorkingCapital(int, int64, int, int64) int64` — Task 2 defines, Task 4 calls. `UnservedLaneCount(ctx, int) (int, bool, error)` — Task 3 defines, Task 4 consumes via the `UnservedLaneReader` interface, Task 6 consumes via the wave port. `CargoOutflowSince(ctx, int, time.Time) (int64, int64, error)` — Task 2 defines, Task 4 consumes via `CargoOutflowReader`. `PurchaseRequest.WorkingCapital` — Task 3 adds, Task 4 sets. `HeavyBuyerContainers()` — sp-59cl1 defines, Task 4 replaces its contents. `GrowthEnabled(ctx, int) (bool, bool, error)` — Task 6 defines and consumes. `WaveReader.Wave(...)` — Task 6 defines on both sides.

**One thing the executor must verify against the real database, not a fake.** Task 2 Step 10's empty-window rung depends on what GORM hands back for `MAX()` over zero rows — SQL returns one row containing NULL, not zero rows, and the two shapes need different handling. `readable=false` must cover both. This repo has shipped the empty-is-zero confusion three times; a fake that returns an empty slice will not catch it.

**Known gaps the executor must resolve in place, not defer.** Task 3 Step 1, Task 4 Step 2 and Task 6 Steps 1/3 all require reading an existing test file to match its fake shapes and helper signatures before writing new ones; those helpers are named but not written out, because they must mirror constructors this plan cannot see the current signatures of. Task 6 Step 8's `WavePort` struct fields are shown by use rather than declared, for the same reason. `grepNonTest` in Task 6's call-site assertion needs writing — a `filepath.WalkDir` over the module root excluding `_test.go`, following `off_gate_retired_test.go`'s calibration (file-count floor plus a sentinel) so a mis-aimed walk fails loudly instead of passing vacuously.
