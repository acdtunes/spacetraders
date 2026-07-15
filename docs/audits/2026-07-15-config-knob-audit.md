# Config Knob Audit — Dynamic Runtime Tuning for the SpaceTraders gobot

Read-only audit. Scope: `gobot/` (daemon coordinators, CLI, config infra). Nothing modified.
Motivating pain: retuning the freshness sizer's purchase cooldown (10m→1m) and spend cap
(100k→500k) required a **code edit + commit + rebuild + daemon restart** that bounces every
running container. This report maps the full hardcoded-config landscape and the mechanism to
make the retune-prone knobs live-tunable.

---

## 1. Executive Summary

**Three distinct config-source patterns exist today, and only one is genuinely live.**

| Pattern | Where the value lives | How you retune it today | Bounces containers? | Coordinators |
|---|---|---|---|---|
| **A. Persisted container-config, frozen at construction** | container's `config` JSON column, read via `cfg.OptionalInt(...)` | stop+start that container, OR edit the `default*` const + rebuild | just that container (or all, if const) | frontier, **freshness sizer**, scout_post, stocker, tour, contract_hub, construction, depot |
| **B. config.yaml re-injection, frozen at construction** | boot-loaded `config.yaml`; `resolve*Config` **clears the persisted keys and re-injects** on every build | edit `config.yaml` + **full daemon restart** | **YES — every container** | fleet_autosizer, goods_factory/manufacturing, trade_fleet, worker_rebalancer, bootstrap, siting, scouting, contract idle-arb |
| **C. Live provider re-read per tick** | container `config` column, mutated by a gRPC verb, **re-read every pass** via a `ConfigProvider` | `fleet hub add`, `goods factory workers`, `depot element add/place` | **NO restart** | contract standby-hubs, goods_factory worker cap, construction worker cap, depot topology |

The key architectural fact: **`buildCommandForType` (`command_factory_registry.go:450`) runs at
container CREATE and at restart RECOVERY only — never per tick.** It builds an immutable command
struct that the coordinator's `Handle()` loop holds for the life of the run. So for patterns A and
B, the coordinator re-derives config every tick (`resolveConfig(cmd)` is called inside
`reconcileOnce`) but **re-derives it from the frozen `cmd`**, not from the live DB. This is exactly
the sp-aoy2 lesson ("reads must come from persisted DB config, not launch-frozen metadata") applied
to numeric knobs: the seam that would make them live does not exist for them.

Pattern C is the proof that live tuning is already solved for individual knobs — including a
**numeric** one. `MutateFactoryWorkerCap` (sp-ev0n, `container_ops_factory_workers.go:74`) writes
`worker_cap` into a running container's config; `FactoryWorkerCapConfigProvider.WorkerCap()`
re-reads it every production pass. No restart. Validated (`count >= 1`). Survives restart because it
is deliberately excluded from the config.yaml re-injection set.

**Counts (approximate, policy values only — infra trivia excluded):**
- ~130 `default*` constants across ~22 coordinators/engines (`grep -rE '^\s+default[A-Z]' internal/`).
- ~60 config-field defaults in `config/defaults.go` + ~55 `[section]` config.yaml keys (patterns A/B).
- **4** knobs live-tunable today (pattern C): contract hubs, factory workers, construction workers, depot elements.
- **0** generic runtime-config verbs. `config set-player` only sets the default player identity; it is not a knob tuner.

**Headline recommendation.** Do **not** invent new infrastructure. Generalize pattern C into a
single generic runtime-config layer: one `spacetraders tune <container> <key> <value>` verb that
read-modify-writes the container `config` column through the existing race-free
`UpdateContainerConfig` seam, plus a small uniform change to each coordinator's per-tick
`resolveConfig` to read a **live snapshot of the container config** instead of the frozen `cmd`. The
money/spend/cooldown/cap knobs (patterns A/B) are the prime targets — they get retuned as the
economy scales. The hard treasury-fraction guard (`maxTreasuryFractionPercent = 25`) stays a
compile-time constant, or becomes a knob only behind a hard validation ceiling.

---

## 2. Top Dynamic-Knob Candidates (ranked)

Ranking bias (per the mission): per-hull-economics and spend-governor values first; structural and
restart-cheap values last. "Live today?" = does the running loop re-read it per tick right now.

### Tier 1 — spend governors and money guards (retuned as treasury scales; highest value)

**1. `defaultSizerMaxSpend` — freshness sizer probe spend cap**
`run_market_freshness_sizer_coordinator.go:67` · current `500000` · config key `max_spend_per_cycle`
- Controls: max probe spend within the trailing 1h window for the market-freshness auto-buyer.
- Retune scenario: **the literal motivating pain.** Ramp probe purchasing up/down as data-coverage
  need vs. treasury changes. Comment shows it was hand-edited 100k→500k on 2026-07-15.
- Blast radius: too high → over-buys probes, drains treasury (still bounded by the 25% guard + fleet cap); too low → freshness SLA breaches persist.
- Live today? **No** (pattern A; frozen `cmd`, resolved from const default because container launched with key unset).
- Verb: `spacetraders tune <freshsizer-container> max_spend_per_cycle 500000`.

**2. `defaultSizerCooldown` — freshness sizer purchase cooldown**
`run_market_freshness_sizer_coordinator.go:68` · current `1m` · config key `purchase_cooldown_secs`
- Controls: min wall-clock between probe buys. The other half of the motivating retune (10m→1m).
- Retune scenario: throttle/accelerate the buy cadence live during a coverage push or a treasury dip.
- Blast radius: too short → rapid fleet growth (bounded by spend window + fleet cap); too long → slow coverage ramp. Low risk, both bounds hold.
- Live today? **No** (pattern A).

**3. `defaultMaxSpendPerCycle` / `defaultPurchaseCooldown` — frontier expansion**
`run_frontier_expansion_coordinator.go:64-65` · `100000`, `10m` · keys `max_spend_per_cycle`, `purchase_cooldown_secs`
- Controls: the frontier coordinator's twin spend cap + cooldown for expansion probe buys.
- Retune scenario: same class as #1/#2 but for coverage expansion rather than freshness; the two coordinators serialize against each other via the shared ledger cooldown, so they get retuned together.
- Blast radius: identical bounds (25% guard + fleet cap). Low.
- Live today? **No** (pattern A).

**4. `trade_fleet_max_spend` / `trade_fleet_reserve` / `trade_fleet_reserve_treasury_pct`**
`container_ops_trade_fleet_coordinator.go:106,109,110` · config.yaml `[trade_fleet]`
- Controls: per-tour spend ceiling and working-capital reserve floor for the standing trade fleet — the single biggest credit mover in the bot.
- Retune scenario: raise the reserve during an era-end rundown; widen per-tour spend when treasury is deep. `config.yaml` comments show `working_capital_reserve` was hand-retuned to 1M after a 419k trough (line 152-156).
- Blast radius: **high** — this governs the main income engine's risk. Mistuned reserve can starve tours or over-expose treasury. Deserves bounds + audit.
- Live today? **No** (pattern B; needs full daemon restart today — worst blast radius on change).

**5. `manufacturing.working_capital_reserve` + `working_capital_reserve_treasury_pct`**
`config.yaml:161-162`, `container_ops_manufacturing.go:17-18`
- Controls: the input-buy floor every manufacturing chain respects (the `max(50000, configured)` guard).
- Retune scenario: raise the floor after an incident (config.yaml comment cites a 1.12M MICROPROCESSORS buy that dropped treasury to 83k, line 158-160).
- Blast radius: **high** — a too-low floor lets a chain spend treasury to near-zero on one input buy.
- Live today? **No** (pattern B).

**6. `contract idle_arb.max_spend` / `leash_radius`**
`config.yaml:141-143`, `idle_arb.go:30`
- Controls: idle-hull arbitrage-harvest spend cap and geographic leash. config.yaml comments show heavy retuning (80u→150u re-admitted the best leg; 100k→200k for cap-bound legs).
- Retune scenario: this is demonstrably the **most-retuned pair in the codebase** — three dated captain retunes in the config.yaml comment block alone.
- Blast radius: medium — bad leash collapses harvest throughput (documented 300k/hr→29k/hr regression); overspend bounded by treasury guards.
- Live today? **No** (pattern B).

**7. `defaultHeavyTreasuryPctPerPurchase` — autosizer heavy-freighter buy gate**
`run_fleet_autosizer_coordinator.go:32` · `25` · config.yaml `autosizer_reserve_treasury_pct` (sibling)
- Controls: max % of treasury a single heavy-freighter purchase may cost.
- Retune scenario: loosen when treasury is deep and unserved lanes pile up; tighten during a drawdown.
- Blast radius: **high** — this is a capex governor on the most expensive hull class. Bound the ceiling hard.
- Live today? **No** (pattern B).

### Tier 2 — fleet caps and hull budgets (retuned at scale inflections)

**8. `defaultFleetCeilingTotal/Lights/Heavies/Warehouse` — autosizer fleet caps**
`run_fleet_autosizer_coordinator.go:23-26` · `50/35/15/8` · config.yaml `autosizer_fleet_ceiling_*`
- Controls: hard ceilings on fleet composition. Retuned every time the economy outgrows the current ceiling.
- Blast radius: medium — a raised ceiling authorizes more capex; bounded by per-purchase treasury guards. A ceiling set below current fleet is a no-op (never sells).
- Live today? **No** (pattern B).

**9. `defaultSizerMaxProbeFleet` / `defaultMaxProbeFleet` — probe fleet caps**
`run_market_freshness_sizer_coordinator.go:66`, `run_frontier_expansion_coordinator.go:63` · `40` each · key `max_probe_fleet`
- Controls: total satellite cap gating both probe-buyers.
- Retune scenario: raise as the mapped universe grows and more standing posts need manning.
- Blast radius: low — bounded by spend caps and treasury guard.
- Live today? **No** (pattern A).

**10. `defaultContractHubMaxHaulersPerHub` — contract hub hauler cap**
`run_contract_hub_coordinator.go:49` · `3`
- Controls: max haulers assigned per contract hub.
- Retune scenario: tune hub concentration vs. spread as contract volume shifts.
- Blast radius: low-medium — affects hauler distribution, not spend.
- Live today? **No** (pattern A). Note: the **hub set itself** is already live (`fleet hub add/remove`); this is the per-hub hauler count next to it — a natural extension of an existing live verb.

**11. `defaultConstructionWorkerCap` / `defaultConstructionLotUnits`**
`run_construction_coordinator.go:44,67` · `5`, `40`
- Controls: construction fan-out width and per-lot purchase size.
- Live today? **Worker cap: YES** (pattern C — `construction ... workers` verb, `cli/construction.go:671`). Lot units: **No**. This coordinator already has the live seam wired for one knob; adding lot_units is incremental.

### Tier 3 — control-loop tuning (behavioral, moderate value)

**12. `defaultReleaseSlackPercent` — freshness hysteresis**
`run_market_freshness_sizer_coordinator.go:65` · `60` (% of SLA below which a probe is shed)
- Controls: anti-flap hysteresis on probe release. Retune if the fleet flaps or holds probes too long.
- Blast radius: low. Live today? **No** (pattern A).

**13. `defaultSLASeconds` — freshness SLA target**
`run_market_freshness_sizer_coordinator.go:61` · `3600` · key `sla_seconds`
- Controls: the freshness target the whole sizer sizes against. Tightening it raises demand fleet-wide.
- Blast radius: medium — drives aggregate probe demand. Live today? **No** (pattern A).

**14. `chain_pnl_kill_threshold_per_hour` / `chain_pnl_window_hours` — factory kill switch**
`run_factory_coordinator_chain_pnl_kill.go:33,38` · `30000`, `6` · config.yaml `[manufacturing]`
- Controls: the loss-rate at which a manufacturing chain is auto-killed, and the averaging window.
- Retune scenario: loosen during a known transient input-price spike; tighten to cut losers faster.
- Blast radius: medium — too tight kills viable chains; too loose bleeds credits. Live today? **No** (pattern B).

**15. `defaultContractHubRehomeHysteresis` / `defaultContractHubEWMAHalfLife`**
`run_contract_hub_coordinator.go:57,45` · `50.0`, `23.0`
- Controls: margin a re-home must clear, and the demand-smoothing half-life.
- Blast radius: low-medium — governs hub churn. Live today? **No** (pattern A).

**16. `worker_rebalancer_ferry_cooldown_secs` / `max_concurrent_ferries` / `vacancy_min_minutes`**
`run_worker_rebalancer_coordinator.go:45,50,33` · `600`, `2`, `15` · config.yaml `[worker_rebalancer]`
- Controls: how aggressively idle workers are ferried between systems.
- Blast radius: low — movement policy, no direct spend. Live today? **No** (pattern B).

**17. `defaultInputPriceCeilingMultiplier` — manufacturing input price gate**
`input_price_ceiling.go:39` · `1.5` · config.yaml `input_price_ceiling_multiplier`
- Controls: max multiple over baseline an input may cost before the buy is refused.
- Blast radius: medium — too low starves chains of inputs; too high overpays. Live today? **No** (pattern B).

**18. `defaultDepositCeilingPct` — tour deposit ceiling**
`run_tour_coordinator.go:89` · `10`
- Controls: max % of treasury a tour may stage as working deposit.
- Blast radius: medium (spend governor). Live today? **No** (pattern A).

**19. `defaultFeedSaturationMaxUnits` / `MinUnits` — manufacturing feed tranche bounds**
`feeding_policy.go:38,42` · `200`, `25`
- Controls: per-window feed tranche sizing (the 25-200u band referenced in config.yaml:172).
- Blast radius: low-medium. Live today? **No** (pattern B, via fabrication_efficiency path).

### Tier 4 — borderline (restart-cheap; dynamic is nice-to-have, not urgent)

**20. Tick intervals** — `defaultSizerTickSeconds` (60), `defaultScoutPostTickSeconds` (30),
`defaultAutosizerTickSeconds` (900), `defaultContractHubTickSeconds` (300), etc.
- Cadence of each reconcile loop. Dynamic-tunable is convenient but a restart is cheap for cadence and these rarely move. Include them for completeness in the generic layer (near-free once the mechanism exists), don't prioritize migrating them.
- Live today? **No** (pattern A/B). Config key `tick_interval_secs` per coordinator.

**21. API client retry/backoff** — `defaultMaxRetries` (10), `defaultBackoffBase` (2s), `defaultTimeout` (30s)
`internal/adapters/api/client.go:24-26` + config.yaml `api.rate_limit`/`api.retry`
- Global API pressure knobs. **Process-wide, not per-container** — a different (boot-frozen) class; a live change would need a client-level reconfigure, not a container config write. Lower priority; note but don't fold into the per-container mechanism.

### Explicitly NOT candidates (keep compile-time)

- **`maxTreasuryFractionPercent = 25`** (`run_frontier_expansion_coordinator.go:58`) — the hard per-hull
  25%-treasury guard. Deliberately a non-tunable constant (its own code comment says so: "RULINGS #5's
  hard-floor exception, guards are never weakened"). If ever tuned, only behind a hard ceiling of 25.
- Ship-type strings (`SHIP_PROBE`, `SHIP_LIGHT_HAULER`), container-ID formats, API paths, schema names,
  ranking-log limits, buffer sizes — structural, not policy.

---

## 3. Mechanism Recommendation

**Extend pattern C into one generic runtime-config layer. Reuse every existing seam.**

### 3.1 Where values live — container `config` column (Store A), not a new table

Keep using the container `config` JSON column. `UpdateContainerConfig`
(`container_repository.go:124`) is already a **single-column, race-free** GORM update — its own doc
says "config has no other writer during a run (set once at Add and only ever amended here), so a
caller's read-modify-write of the config map is race-free at the column level." That is precisely the
substrate a `tune` verb needs. A new `runtime_settings` key-value table would duplicate this and lose
the free restart-recovery (the config column is already the recovery source). **Do not add a table.**

### 3.2 How running coordinators pick up changes — per-tick live read, not notify

Two options; prefer the uniform one:

- **(a) Bespoke provider per knob** — the current sp-jcke/sp-ev0n approach: a `ConfigProvider` type +
  a `MutateX` daemon method + a CLI verb, per knob. Proven, but it does not scale to ~130 knobs.
- **(b) Generic live-config read (recommended).** Every pattern-A/B coordinator already calls
  `resolveConfig(cmd)` **inside `reconcileOnce`, once per tick** (see
  `run_frontier_expansion_coordinator.go:406`, `run_market_freshness_sizer_coordinator.go:324`). The
  only change needed is to swap the *source* of that resolve from the frozen `cmd` to a **live
  snapshot of the container config**. Inject a `containerConfigReader` (backed by
  `containerRepo.Get(containerID, playerID)` → decode JSON, mirroring the existing providers) and have
  `resolveConfig` read keys from it each tick, falling back to `cmd` (launch value) then the `default*`
  const. This is a small, uniform, per-coordinator change that reuses the exact read path
  `StandbyStationConfigProvider`/`FactoryWorkerCapConfigProvider` already use. No polling thread, no
  notify bus — the tick *is* the poll, and a per-tick `Get` on one indexed row is cheap.

Notify/watch is unnecessary complexity: reconcile cadences are seconds-to-minutes; a knob change
taking effect on the next tick is well within operational expectations.

### 3.3 The generic verb

```
spacetraders tune <container-id|--operation freshsizer> <key> <value>   # set a live knob
spacetraders tune <container-id> <key> 0                                # 0/false → revert to default
spacetraders tune <container-id> --show                                 # list current effective knobs + source
```

Daemon side: one `MutateContainerConfigKey(ctx, containerID, key, value, playerID)` method that
generalizes `MutateFactoryWorkerCap` (`container_ops_factory_workers.go:74`) — locate the running
container, validate the key against a bounds registry, read-modify-write the config column via
`UpdateContainerConfig`, return old/new + changed flag (idempotent no-op when unchanged, exactly as
the hub and worker-cap verbs do). Optionally resolve `--operation` → container by type via
`FindActiveCoordinatorByType` (the same lookup `MutateStandbyStation` uses).

### 3.4 Validation / bounds — a per-key registry (mandatory)

A mistyped `25` → `2500%` must not fire. Ship a static bounds registry keyed by
`<engine>.<knob>`: `{type, min, max, unit}`. Examples:
`max_spend_per_cycle ∈ [0, 5_000_000]`, `purchase_cooldown_secs ∈ [10, 86_400]`,
`*_treasury_pct ∈ [1, 25]` (never above the hard 25 guard), `max_probe_fleet ∈ [0, 200]`. The verb
rejects out-of-bounds before writing. This registry doubles as the `--show` metadata and as the
documentation of every tunable knob. Compose with the existing **`0/false → documented default`**
idiom: `0` is always valid and means "revert," matching `factoryWorkerCapFromConfigMap`'s `<=0 → no
override` and `resolveConfig`'s `<= 0 → default`.

### 3.5 Audit trail — these knobs move real credits

Every `tune` write must emit a structured, durable record: who (operator/agent), when, container,
key, old→new. Two low-cost options that fit existing idioms: (1) emit a **captain event** through the
same `EventRecorder` the coordinators already hold (so a tune shows up in the captain's interrupt
stream), and/or (2) append to the decisions/friction log the captain already keeps. Do **not** let a
credit-moving knob change be a silent DB write (today's `UpdateContainerConfig` is silent).

### 3.6 Composition with container recovery — the one real subtlety

Restart-recovery rebuilds the command from the config column, so pattern-A live values **survive
verbatim** (good — same as worker_cap). **But pattern-B coordinators re-inject their knobs from
config.yaml on every build** (`resolveManufacturingConfig`, `resolveFleetAutosizerConfig`, etc., which
*clear* the persisted keys — see `manufacturingConfigKeys` at `container_ops_manufacturing.go:16`). A
live-tuned pattern-B value would be **clobbered on the next restart** by the config.yaml re-injection.

The resolution is already demonstrated by worker_cap: it is deliberately **excluded from
`manufacturingConfigKeys`** so the container config is authoritative and the config.yaml value is only
a global default that fills when no per-op override exists. **Design rule for migrating a pattern-B
knob to live-tunable: remove it from its `*ConfigKeys` re-injection set and treat container-config as
the live override, config.yaml as the default-of-record.** This must be decided per knob and is the
main source of per-knob work in the migration.

### 3.7 What NOT to build

No new table. No notify/watch bus. No new persistence layer. No change to the 25% treasury guard's
compile-time status. Reuse `UpdateContainerConfig`, `containerRepo.Get`, the `configReader`, the
`EventRecorder`, and the `MutateX`/`ConfigProvider` shape already proven three times.

---

## 4. Full Inventory Appendix

Category: **A** = persisted container-config (frozen at construction, restart-recovery re-reads);
**B** = config.yaml re-injected (clears persisted keys each build; full daemon restart to change);
**C** = live per-tick (dynamic today); **BOOT** = process-boot frozen (config/defaults.go, flags).
"Cand" = dynamic-knob candidate worth a live verb.

### Freshness sizer — `run_market_freshness_sizer_coordinator.go`
| Knob | Line | Value | Cat | Cand |
|---|---|---|---|---|
| defaultSizerTickSeconds | 60 | 60 | A | borderline |
| defaultSLASeconds | 61 | 3600 | A | Y |
| defaultSeedCycleSeconds | 62 | 180 | A | Y |
| defaultMinCycleSamples | 63 | 3 | A | Y |
| defaultMaxProbesPerSystem | 64 | 8 | A | Y |
| defaultReleaseSlackPercent | 65 | 60 | A | Y |
| defaultSizerMaxProbeFleet | 66 | 40 | A | Y |
| **defaultSizerMaxSpend** | 67 | 500000 | A | **Y (top)** |
| **defaultSizerCooldown** | 68 | 1m | A | **Y (top)** |
| defaultSizerSpendWindow | 69 | 1h | A | Y |

### Frontier expansion — `run_frontier_expansion_coordinator.go`
| Knob | Line | Value | Cat | Cand |
|---|---|---|---|---|
| maxTreasuryFractionPercent | 58 | 25 | A | **N — hard guard** |
| defaultTickSeconds | 62 | 60 | A | borderline |
| defaultMaxProbeFleet | 63 | 40 | A | Y |
| defaultMaxSpendPerCycle | 64 | 100000 | A | Y (top) |
| defaultPurchaseCooldown | 65 | 10m | A | Y (top) |
| defaultSpendWindow | 66 | 1h | A | Y |
| defaultExpansionMaxHops | 67 | 3 | A | Y |
| defaultMaxFrontierPostsInFlight | 68 | 5 | A | Y |
| defaultFrontierFreshness | 69 | 60m | A | Y |
| defaultWeightKnownMarket/HopPenalty/VirginBonus | 72-74 | 10/5/15 | A | Y |

### Fleet autosizer — `run_fleet_autosizer_coordinator.go` (config.yaml `[fleet_autosizer]`, cat B)
| Knob | Line | Value | Cand |
|---|---|---|---|
| defaultAutosizerTickSeconds | 17 | 900 | borderline |
| defaultPurchaseCapPerTick | 18 | 1 | Y |
| defaultFleetCeilingTotal/Lights/Heavies/Warehouse | 23-26 | 50/35/15/8 | Y |
| defaultPurchaseMarginOverFloor | 28 | 200000 | Y |
| defaultLightRotationSlots | 29 | 3.5 | Y |
| defaultHeavyMarginalRateFloor | 30 | 0.7 | Y |
| defaultHeavyUnservedLanesMin | 31 | 3 | Y |
| **defaultHeavyTreasuryPctPerPurchase** | 32 | 25 | Y (guard-bound) |
| defaultAPIUtilCeilingPct | 33 | 85 | Y |
| defaultPaybackSafetyFactor | 34 | 0.5 | Y |
| defaultPurchaseCutoffEraMinusHours | 35 | 3.0 | Y |
| defaultMaxPremiumOverCheapestPct | 36 | 50 | Y |
| defaultZeroEffectAlarmTicks | 37 | 4 | N (diagnostic) |
| defaultWarehouseMinChainTickPersistence | 43 | 2 | Y |
| defaultWarehouseCapacityTargetHours | 44 | 2.0 | Y |

### Contract hub — `run_contract_hub_coordinator.go` (cat A)
| Knob | Line | Value | Cand |
|---|---|---|---|
| defaultContractHubTickSeconds | 40 | 300 | borderline |
| defaultContractHubEWMAHalfLife | 45 | 23.0 | Y |
| defaultContractHubMaxHaulersPerHub | 49 | 3 | Y |
| defaultContractHubBaselineCoverage | 54 | 1000000.0 | Y |
| defaultContractHubRehomeHysteresis | 57 | 50.0 | Y |
| defaultContractHubExpectedRemainingContracts | 58 | 10.0 | Y |

### Manufacturing / factory (config.yaml `[manufacturing]`, cat B unless noted)
| Knob | File:Line | Value | Cand |
|---|---|---|---|
| working_capital_reserve | config.yaml:162 · manufacturing.go:17 | 1000000 | Y (top) |
| working_capital_reserve_treasury_pct | container_ops_manufacturing.go:18 | — | Y |
| defaultChainPnLKillThresholdPerHour | run_factory_coordinator_chain_pnl_kill.go:33 | 30000 | Y |
| defaultChainPnLWindowHours | run_factory_coordinator_chain_pnl_kill.go:38 | 6 | Y |
| defaultInputRecoveryReattemptMinutes | run_factory_coordinator_input_pause.go:68 | 194 | Y |
| defaultRestWindowMinutes | run_factory_coordinator_rest_signal.go:58 | 90 | Y |
| defaultInputPriceCeilingMultiplier | input_price_ceiling.go:39 | 1.5 | Y |
| defaultRescueMultiplier | input_source_selector.go:30 | 1.2 | Y |
| defaultFeedSaturationMax/MinUnits | feeding_policy.go:38,42 | 200/25 | Y |
| defaultFabricateMaxDepth | fabricate_depth.go:30 | 3 | Y |
| defaultThroughputBuyRateMultiple/PerLotMultiple | unified_gate_fill.go:112,116 | 2.0/1.0 | Y |
| defaultConstructionWorkerCap | run_construction_coordinator.go:44 | 5 | **C (live)** |
| defaultConstructionLotUnits | run_construction_coordinator.go:67 | 40 | Y |

### Trade fleet (config.yaml `[trade_fleet]`, cat B) — `run_trade_fleet_coordinator.go` + keys at `container_ops_trade_fleet_coordinator.go:100-115`
| Knob | Value | Cand |
|---|---|---|
| working_capital_reserve (config.yaml:156) | 1000000 | Y (top) |
| trade_fleet_max_spend | — | Y (top) |
| trade_fleet_reserve / _reserve_treasury_pct | — | Y (top) |
| trade_fleet_cooldown_secs | 180 | Y |
| trade_fleet_max_concurrent | — | Y |
| trade_fleet_min_margin | — | Y |
| trade_fleet_max_hops | — | Y |
| defaultRelaunchBackoffMaxSeconds | 1800 | Y |
| defaultMassParkWindowSeconds/MinHulls | 120/4 | Y |

### Worker rebalancer (config.yaml `[worker_rebalancer]`, cat B) — `run_worker_rebalancer_coordinator.go`
| Knob | Line | Value | Cand |
|---|---|---|---|
| defaultRebalancerTickSeconds | 25 | 60 | borderline |
| defaultVacancyMinMinutes | 33 | 15 | Y |
| defaultSourceMinIdle | 38 | 2 | Y |
| defaultFerryCooldownSeconds | 45 | 600 | Y |
| defaultMaxConcurrentFerries | 50 | 2 | Y |
| defaultMaxLightsPerSystem | 55 | 0 | Y |

### Siting (config.yaml `[manufacturing.siting]`, cat B) — `run_siting_coordinator.go:47-59`
12 knobs: tick (900), workers_per_chain (3.5), freshness_max (7200), 4 ranking weights (1.0 each),
max_chains_system/input (3/2), retire_hysteresis (2), scout_cooldown (3600). All cat B, all Cand=Y
(policy weights + caps), tick borderline.

### Bootstrap (config.yaml `[bootstrap]`, cat B) — `run_bootstrap_coordinator.go:16-44`
tick (300), probe_target (3), coverage_bar (0.9), reserve_margin (0.5), hauler_target (4),
income_bar (10000), min_contract_earners (1), gate_worker_target (6). Cat B, Cand=Y for the
economic bars/targets.

### Scout post — `run_scout_post_coordinator.go` (cat A; some [scouting] cat B)
tick (30), market_drift_threshold (2), market_drift_max_age (60m), undersized_avg_hop (3m),
undersized_rewarn_cooldown (3h), max_reposition_jumps (12), reposition_failure_cooldown (30m),
respawn_attempt_cap (10), respawn_park_window (30m). Cand=Y for the thresholds/cooldowns.

### Other coordinators
| Knob | File:Line | Value | Cat | Cand |
|---|---|---|---|---|
| defaultStockerStandingTick | run_stocker_coordinator.go:39 | 30s | A | borderline |
| defaultDepositCeilingPct | run_tour_coordinator.go:89 | 10 | A | Y |
| defaultModelArtifactPath | run_tour_coordinator.go:67 | (path) | A | N (structural) |
| defaultDirectScanInterval | scout_tour.go:26 | 15m | A | Y |
| defaultTourStartJitterMax | scout_tour.go:47 | 120s | B [scouting] | borderline |
| defaultAbsorptionPlannedTTLSlack | idle_arb.go:30 | 15m | A | Y |

### Boot-frozen (cat BOOT — `config/defaults.go`; process restart to change, separate class)
- DB pool (max_open 25, max_idle 5, lifetime 5m), API (timeout 30s, rate_limit 2/30, retry 3/1s),
  routing timeouts + gate backoff (5m/6x/2h, defaults.go:78-86), daemon (max_containers 100,
  health_check 30s, restart_policy 3/5s/2.0), logging rotation.
- **Captain supervisor** (`captain.*`, config.yaml:98-125 + defaults.go:149-250): heartbeat_minutes,
  max_sessions_per_hour, poll_interval, model, thresholds, etc. — a **separate process** (`cmd/captain`),
  retuned by restarting the captain, not the daemon. Out of the per-container mechanism's scope; flag
  as its own tuning surface if the Admiral wants captain knobs live too.
- API client consts (`api/client.go:24-26`): timeout 30s, max_retries 10, backoff_base 2s — process-wide, not per-container.

### Live today (cat C — the precedents, no restart)
| Knob | Verb | Daemon method | Provider |
|---|---|---|---|
| contract `standby_stations` | `fleet hub add/remove` | `MutateStandbyStation` (container_ops_fleet_hub.go:83) | StandbyStationConfigProvider:125 |
| goods_factory `worker_cap` | `goods factory workers` | `MutateFactoryWorkerCap` (container_ops_factory_workers.go:74) | FactoryWorkerCapConfigProvider:118 |
| construction worker cap | `construction ... workers` | (analogue) | cli/construction.go:671 |
| depot topology/elements | `depot element add/remove/place` | AddDepotElement/PlaceDepotElement (container_ops_depot.go:95,146) | stocker re-reads supported_goods per pass |

---

## 5. Proposed Bead Decomposition

**Bead 1 — Generic runtime-config mechanism (verb + write + live-read seam).**
Generalize `MutateFactoryWorkerCap` into `MutateContainerConfigKey(containerID, key, value)` + a
`spacetraders tune <container|--operation> <key> <value>` CLI verb, backed by the existing
`UpdateContainerConfig`. Add a `containerConfigReader` (backed by `containerRepo.Get`) and a bounds
registry (§3.4). Include the audit-trail emit (§3.5).
*Acceptance:* `tune <freshsizer> max_spend_per_cycle 300000` on a running daemon changes the value the
next reconcile tick reads, with no restart, rejects out-of-bounds input, and emits an audit record —
proven by an integration test that drives one tick before and after the mutation.

**Bead 2 — Migrate the freshness sizer + frontier coordinators (pattern A, the motivating pain).**
Wire both coordinators' per-tick `resolveConfig`/`resolveSizerConfig` to read from the live
`containerConfigReader` (fallback: launch `cmd` → const default). Register their spend/cooldown/cap
knobs in the bounds registry.
*Acceptance:* the exact motivating retune (freshness `purchase_cooldown` 10m→1m, `max_spend` 100k→500k)
is applied live via `tune`, takes effect within one tick, and survives a simulated daemon restart —
no code edit, no rebuild, no restart.

**Bead 3 — Migrate the pattern-B credit-movers (trade_fleet, manufacturing, autosizer treasury knobs).**
For each targeted knob: remove it from its `*ConfigKeys` re-injection set (§3.6), make container-config
the live override with config.yaml as the default-of-record, and wire the live read. Focus on the
high-blast-radius money knobs (working_capital_reserve, trade_fleet_max_spend/reserve,
autosizer treasury_pct). Enforce the 25% ceiling in the bounds registry.
*Acceptance:* `trade_fleet` working-capital reserve is retuned live, takes effect next tick, and a
subsequent daemon restart does **not** clobber it back to the config.yaml value (regression test for
the re-injection-clobber trap).

**Bead 4 — Coverage + docs: remaining Tier 2-3 knobs and `tune --show`.**
Add the remaining ranked knobs (fleet ceilings, contract-hub, factory pnl-kill, siting weights) to the
registry; implement `tune --show` to list every effective knob, its value, source (default/launch/live),
and bounds. Optionally fold tick intervals in (near-free once the mechanism exists).
*Acceptance:* `tune --show <container>` lists all tunable knobs with current value + source for every
migrated coordinator, and the bounds registry is the single documented source of truth for what is
tunable.
