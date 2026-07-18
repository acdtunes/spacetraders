# CLI PRIMER — the `spacetraders` CLI: capabilities & knobs

Read this at prime. It is a **map, not the territory**: the live `--help` is always
truth, and this primer only tells you where to look. When help and this doc disagree,
help wins — file a note to refresh the primer.

**How to self-prime.** Enumerate with `--help` at every depth:
`spacetraders --help` → `spacetraders <group> --help` → `spacetraders <group> <verb> --help`.
Help output is generated locally and **never touches the game API** — enumerate freely.
Two offline maps back it up: `man -k spacetraders` (the man index; note it currently
**lags the binary** — see §2) and `captain/CLI_REFERENCE.md` (a full, current flag-level
dump — read it when you need exact flags).

**Token discipline (matters at prime and every wake).** Scope every read: `--system`,
`--top N`, `--limit N`, `--days N`, `--since <dur>`, `--era N`, `--good X`, and `--json`
for machine parsing. A malformed invocation (wrong flag, missing required arg) costs
~3× a correct one — it round-trips an error and you retry. Get the flags from `--help`
before firing a novel verb. Almost every read verb also takes `--agent`/`--player-id`
(agent survives era resets; player-id does not).

**Identity.** The CLI talks to the daemon over a Unix socket
(`--socket`, default `/tmp/spacetraders-daemon.sock`). Resolve the player with
`--agent SYMBOL` (era-stable), `--player-id N`, or the persisted default
(`config set-player`).

---

## 1. Daemon & services (names only — deploys are the shipwright's)

Three processes, three restart surfaces:

- **The daemon** — the Go server behind the Unix socket; owns every container/coordinator
  and is the single writer of game state. `spacetraders health` (liveness),
  `spacetraders version` (build stamp: version, commit, build time). A daemon restart
  rebuilds every container from its persisted `config` column (see §3).
- **The routing service** — a separate Python gRPC service (`gobot/services/routing-service`,
  launched by `run.sh`, default port 50051) that solves the trade-tour / VRP problems.
  Restarted independently of the daemon; its knobs are Layer C (§3.3).
- **The watchkeeper / captain supervisor** — a separate process (`cmd/watchkeeper`) that
  wakes the captain. Its knobs live in `config.yaml [captain]` and change on a
  watchkeeper restart, not a daemon restart. Two stand-down surfaces:
  `captain gag on/off` (soft, live, no restart) and the `captain/DISABLED` sentinel file
  (hard halt; also written by the universe-reset detector; cleared by the Admiral alone).

Starting/stopping/redeploying these processes is the shipwright's job — this primer only
names them so you know which surface a knob lives behind.

---

## 2. Capability map by domain

23 top-level groups. Per verb: one line; only load-bearing flags are called out.

**Heads-up on staleness:** `man -k spacetraders` is currently **missing ~15 live verbs**
(all the newer `workflow` coordinators, `goods factory`, `fleet add/remove/hub`,
`contract depot`, `ship route`/cargo-reservation verbs, `universe transition`,
`captain gag`). `captain/CLI_REFERENCE.md` and the live `--help` are in agreement and
current. Trust `--help`; treat the man pages as a rough index only.

### Era & identity
- **universe** — `status` (compare server resetDate vs open era; non-zero exit on MISMATCH),
  `transition --token <jwt> [--dry-run|--confirm]` (one-command era rollover: validate token,
  flip era without truncating caches, repoint CLI+captain, drain old containers),
  `close` (destructive: truncate caches, blank dead token), `scrub` (delete WIPE-class junk rows).
- **player** — `info`, `list`, `register --agent --token`.
- **config** — `show`, `set-player --agent|--player-id`, `clear-player`.
- **history** — cross-era priors over live tables: `eras`, `summary` (cold-start brief,
  defaults to latest CLOSED era), `goods --good X --era N`, `contracts`, `events`, `pnl`,
  `manufacturing`. Pattern queries default `--era all`.
- **version**, **health** — build stamp / daemon liveness.

### Wake & events (the captain's queue)
- **captain events** — `list [--json]` (unprocessed strategic events), `ack --ids a,b,c | --all | --before <t>`.
- **captain wake** — `set` (declare wake policy; **full-replace** each call:
  `--next-wake-at +3h|<RFC3339>`, `--credits-above N`, `--credits-below N`,
  `--interrupt-types a,b,c`), `show`, `watch add|list|clear` (one-shot wake watches, additive).
- **captain regime** — `set` (price tripwire, **additive**: `--good ORE|GAS|SYM,SYM`,
  `--bid-above|--bid-below <abs|Nx>`, `--window <dur>`), `list`, `clear`.
- **captain report** — engine telemetry (`--days N` default 7, `--json`).
- **captain tokens** — token spend from transcripts: tokens/day + tokens/wake (`--days N`, `--json`).
- **captain gag** — `on --reason "..."` / `off` / `status` (soft supervisor stand-down, live).

### Fleet (ships, dedications, containers)
- **ship** — `list`, `info`, `navigate --ship --destination` (in-system), `route` (point-to-point
  across ANY reachable system), `dock`/`orbit`/`refuel`/`refresh`, `buy`/`sell`/`jettison`,
  `jump` (via jump gate), `outfit install|remove|list`, `reserve`/`release` (captain manual hold),
  `reserve-cargo`/`reserved-cargo`/`unreserve-cargo` (do-not-sell marks).
- **fleet** — dedicated hull groups: `list`, `assign --ship --fleet`, `unassign`,
  `add`/`remove --operation <op> --ship` (mutate a RUNNING coordinator's fleet live, no restart),
  `hub` (add/remove a standby-station hub live).
- **container** — `list`, `get`, `logs <id>`, `stop <id>`. Every workflow verb returns a
  container id; follow it with `container logs`.

### Money & contracts
- **ledger** — `list` (`--category`, `--limit`, `--start-date`/`--end-date`; categories:
  FUEL_COSTS, TRADING_REVENUE/COSTS, SHIP_INVESTMENTS, CONTRACT_REVENUE),
  `report profit-loss` / `report cash-flow` (`--start-date`/`--end-date`).
- **contract** — `list`, `get`, `start` (contract fleet coordinator), `demand` (recurring
  contract demand joined to cheapest source markets — pre-positioning candidates),
  `depot` (localize contract supply chains to a region).

### Markets (all cache-only reads, no live API)
- **market** — `spreads --system [--top N] [--json]` (rank pure-arbitrage lanes; CLEARS FLOOR
  column shows which lanes trade-route will actually fly), `find --good` (every cached market
  trading a good), `get --waypoint`, `list --system`, `history` (price history for a market/good),
  `volatility`.

### Trading (arb legs, tours, standing coordinators)
- **tour** — `report [--since 168h]` (the sp-1ek0 graduation gate: completed tours + guard
  violations, tour $/hr vs single-lane, plan-vs-realized price error).
- **workflow arb-run** — fly one idle hull one captain-named lane, guarded (`--ship --good
  --buy-at --sell-at`, `--max-units`, `--max-spend`, `--min-margin`, `--working-capital-reserve`).
- **workflow tour-run** — fly one idle hull a planner-chosen multi-hop tour
  (`--ship`, `--iterations -1`=continuous / `N` / `0`=one, `--max-hops` 0=planner default 6,
  `--max-spend` 0=25% of live treasury).
- **workflow trade-route** — fly one idle hull the top-ranked arbitrage circuit.
- **workflow trade-fleet-coordinator** — standing coordinator that keeps continuous tours
  alive on all `trade` hulls.
- **workflow warehouse** — park an idle hull as a passive buffer (`--ship --waypoint --goods`).
- **workflow stocker** — one dedicated hull that fills the home warehouse
  (`--ship --warehouse-waypoint`, `--iterations`, `--budget-per-leg`, `--max-market-age-minutes`).
- **workflow batch-contract** — batch contract workflow for one ship.

### Construction
- **construction** — `status <site>`, `start <site>` (supply pipeline;
  `--depth 0..3` full-production→delivery-only, `--min-supply ABUNDANT..SCARCE` sourcing floor,
  `--good-override GOOD:key=val` / `--overrides <json>` per-good buy-gating; idempotent/resumable),
  `override --site --good [--min-supply|--strategy|--price-ceiling-mult|--clear]` (tune a RUNNING
  pipeline's per-good gate live), `stop`.

### Scouting & expansion
- **frontier** — `start` (standing frontier-expansion coordinator: ranks uncovered frontier,
  declares top system as a sweep post, auto-buys a probe behind the money-guard stack),
  `status` (split, backlog, probes, blockers).
- **scout** — `posts add <SYSTEM> [--freshness 60m] [--hulls N] [--kind standing|sweep-once]`
  (desired-state post; N probes run disjoint partitioned tours → ~N× freshness at same API rate),
  `posts list`, `posts remove`, `start` (standing scout-post coordinator).
- **waypoint** — `list --system [--type X] [--trait X]`, `get --waypoint` (syncs from API on a
  cache miss; surfaces JUMP_GATE and non-market waypoints the market cache omits).
- **shipyard** — `list <SYSTEM> <WP>`, `purchase --ship --type SHIP_X [--quantity N] [--budget C]`.
- **system** — `gates` (cross-system jump-gate adjacency; charts a named system live on a miss).

### Manufacturing & factories
- **operations** — gas extraction: `start --system --gas --siphons S1,S2 --storage ST1`,
  `status`, `stop --gas|--manufacturing` (`--manufacturing` is legacy-container cleanup only).
- **goods** — recursive supply-chain fabrication: `produce GOOD --system`, `status <id>`,
  `stop <id>`, `factory` (tune a running factory live — worker cap etc., no restart).
- **workflow siting-coordinator** — automates factory discovery/placement/capacity planning.
- **workflow worker-rebalancer-coordinator** — ferries idle lights to worker-starved factory systems.
- **workflow fleet-autosizer** — sizes the hull pool to demand, auto-buys behind the money guards.
- **workflow capacity-reconciler** — drives actual contract-delivery topology toward the computed
  desired topology, capex-paced.
- **workflow auto-outfit** — installs the highest-marginal-value module on the most saturated hull, guarded.
- **workflow shipyard-backfill** — backfills charted-but-unscanned shipyard systems, deeper-first.

### Bootstrap
- **workflow bootstrap** — the standing cold-start reconciler (sp-3nbe): observes the world,
  derives the phase (never a stored cursor), acts on the delta behind guards. Slice 1 runs the
  DATA phase (buy probes → scout-all-markets → hold at coverage_bar). **Live by default**;
  `--dry-run` to log-without-acting; config in `config.yaml [bootstrap]`.

### Tuning
- **tune** — read or write a RUNNING container's live knobs, no restart. See §3.2.

---

## 3. The knob system — three layers

Three independent surfaces set fleet behavior. Know which layer a knob is on, because that
determines how you change it and what a restart does to your change.

### 3.1 Layer A — `config.yaml` (restart-applied)

Boot-loaded config, one `[section]` per subsystem. Precedence: env `ST_*` > `config.yaml` >
built-in defaults (`config/defaults.go`). Sections (root `Config` struct):

| Section | Subsystem | Source pattern |
|---|---|---|
| `database`, `api`, `routing`, `daemon`, `logging`, `metrics` | infra (pools, timeouts, rate-limit, health) | **BOOT** — process restart |
| `captain` | watchkeeper supervisor (separate process) | **BOOT** — watchkeeper restart (see below) |
| `contract` | contract fleet + idle-arb (`idle_arb.max_spend`, `leash_radius`) | **B** |
| `absorption` | idle-hull absorption/harvest | **A/B** |
| `trade_fleet` | standing trade fleet — biggest credit mover (`working_capital_reserve`, `trade_fleet_max_spend`, `_reserve`, `_cooldown_secs`, `_max_concurrent/min_margin/max_hops`) | **B** |
| `trade_impact` | market-impact decay model | B |
| `worker_rebalancer` | worker ferrying (`ferry_cooldown_secs`, `max_concurrent_ferries`, `vacancy_min_minutes`) | **B** |
| `manufacturing` (+ `manufacturing.siting`) | factories (`working_capital_reserve`, `chain_pnl_kill_threshold_per_hour`/`_window_hours`, `input_price_ceiling_multiplier`, feed bounds; siting = 12 weights/caps) | **B** |
| `scouting` | scout-post thresholds/cooldowns | A (+ some B) |
| `fleet_autosizer` | hull-pool sizing (`fleet_ceiling_total/lights/heavies/warehouse`, `heavy_treasury_pct_per_purchase`, purchase margins) | **B** |
| `bootstrap` | cold-start (`probe_target`, `coverage_bar`, `reserve_margin`, `bootstrap_disabled`, `dry_run`) | **B** |
| `capacity_reconciler` | contract-topology reconciler (`reserve_floor_credits`) | B |
| `ship_resync` | ship-state resync cadence | BOOT/A |

**The three source patterns (the load-bearing distinction):**
- **A — persisted container-config, frozen at construction.** Value lives in the container's
  `config` JSON column, read via `cfg.OptionalInt`. Change it by stop+start of that container,
  or (for the default) edit the `default*` const + rebuild. **A live `tune` write survives a
  daemon restart verbatim** (the column is the recovery source). Coordinators: freshsizer,
  frontier, scout_post, stocker, tour, contract_hub, construction, depot.
- **B — `config.yaml` re-injected, frozen at construction.** `resolve*Config` **clears the
  persisted keys and re-injects from `config.yaml` on every build**, so changing it means edit
  `config.yaml` + **full daemon restart (bounces every container)** — AND a live `tune` of a
  pattern-B knob is **clobbered on the next restart** by the re-injection. Coordinators:
  fleet_autosizer, manufacturing/goods_factory, trade_fleet, worker_rebalancer, bootstrap,
  siting, scouting, contract idle-arb.
- **C — live provider, re-read per tick.** The container `config` column mutated by a gRPC verb
  and re-read every pass. **No restart.** This is the pattern `tune` (§3.2), `fleet hub add`,
  `goods factory`, and `construction override` all use.

**What a restart does:** rebuilds every container from its `config` column. Pattern-A live
values persist; pattern-B knobs snap back to `config.yaml`. Re-verify live tunes after any
restart (see §3.4).

**Watchkeeper (`config.yaml [captain]`) — a separate process, retuned by restarting the
watchkeeper.** Key knobs: `heartbeat_minutes` (default wake cadence), `poll_interval_seconds`,
`max_sessions_per_hour`, `session_timeout_minutes`, `credits_thresholds` (wake tripwires),
`max_wake_interval_minutes` (never-wake safety ceiling — a wake policy can delay but never
suppress a wake), `meta_review_days`, `universe_check_hours`, `income_stall_hours`,
`stream_down_minutes`/`expected_streams` (liveness), `weekly_token_budget` +
`quota_alert_threshold_pct` (a configured token-budget PROXY, not a live Anthropic quota),
`pinned_hull_containerless_minutes` (watchdog), `briefing_disabled`, self-improvement gates
(`auto_merge`, `max_fixes_per_day`, `max_features_per_day`, `max_feature_diff_lines`),
`engine_mode` (legacy|bridge). Runtime wake overrides are declared live via `captain wake set`
(§2) — those do NOT need a restart.

### 3.2 Layer B — live `tune` (no restart)

`spacetraders tune --operation <op> [<key> [<value>]]` reads or writes a RUNNING coordinator's
knobs. Read = omit value (table of value/default/min-max/unit/desc; add `--json`). Write = give
value; the daemon validates against a static bounds registry (out-of-bounds / unknown key is
**rejected before any write**), amends only that container's `config` column, and the coordinator
re-reads its config at the next tick start — the change lands next tick and **survives a daemon
restart**. `value 0` or `--reset` reverts to the documented default. Every effective tune emits a
`config.tuned` captain audit event (these knobs move real credits — no silent writes).

**Registry — SIX tunable coordinator types** (the `tune --help` text still says only
freshsizer+frontier; that text lags the code, which registers all six):

| `--operation` | Key knobs (bounds in parens) | What it governs |
|---|---|---|
| **freshsizer** | `max_spend_per_cycle` [0,5M], `purchase_cooldown_secs` [10,86400], `spend_window_secs`, `max_probe_fleet` [0,200], `max_probes_per_system`, `sla_seconds`, `target_percentile` [1,100], `value_weighted` {1,2}, `worst_cycle_seconds`, `cycle_dampening_percent`, `breach_response_percent` [1,500], `release_slack_percent`, `release_stable_window_secs`, `reserved_frontier_floor`, `hold_unscanned_market_posts` {0,1} | market-freshness probe auto-buyer sizing |
| **frontier** | `max_spend_per_cycle`, `purchase_cooldown_secs`, `max_probe_fleet`, `proximal_yard_hop_penalty`, `probe_sibling_price_margin`, `max_probe_price`, `breadth_fraction_percent` [1,100], `max_depth_pathfinders` [1,20], `max_depth_hops` [1,12], `objective_bias_percent`, `off_gate_*` (queue_exhaustion_cycles, warp_range_fuel, value_weight, fuel_weight), `reserved_freshness_floor`, `discovery_share` [0,100], `scan_only` (deprecated → discovery_share), `probe_reuse_enabled` {0,1}, `edge_relay_max_hops`, `reuse_value_ceiling`, `snowball_neighbors` {0,1}, `post_inflight_timeout_secs` | frontier expansion depth-vs-breadth, probe reuse, spend guards |
| **scoutpost** | `manning_stall_cycles` [1,1440], `manning_stall_correction_cap`, `scout_cross_system_relay_enabled` {0,1}, `scout_relay_max_hops` [1,12] | scout-post manning + cross-system reuse relay |
| **contract** | `min_home_contract_workers` [0,200], `depot_buffer_min_source_distance` [0,5000] | contract worker reserve + depot buffering floor |
| **autooutfit** | `min_telemetry_samples`, `price_ceiling` [0,5M], `max_installs_per_tick` [1,20], `payback_horizon_hours`, `treasury_reserve`, `max_treasury_fraction_pct` [1,100] | auto-outfit upgrade gating |
| **shipyardbackfill** | `max_dispatches_per_cycle` [1,100], `backfill_max_hops` [1,1000] | shipyard-backfill sweep pacing/reach |

Every `{0,1}`/flag knob and every reuse/relay master switch **defaults to 0 = off = byte-identical**
to prior behavior (see §3.4). Target by `--operation <alias>` (freshest-heartbeat coordinator of
that type) or by explicit `<container-id>` (must be RUNNING/PENDING — a STOPPED container has no
loop to retune).

### 3.3 Layer C — routing env (`run.sh` exports)

The routing service reads `TOUR_SOLVER_*` env vars at process start. `run.sh` sets each with a
`${VAR:-default}` so an operator override wins; the Python code carries its own fail-safe default
and clamp. **Uncommitted `run.sh` exports are live fleet state** — an armed knob is a runtime
override that a `git checkout -- run.sh` + routing restart disarms. Changing any of these requires
a **routing-service restart** (not a daemon restart).

| Env var | `run.sh` launch | code default | clamp | Governs |
|---|---|---|---|---|
| `TOUR_SOLVER_OBJECTIVE` | `rate` | `profit` | rate\|profit | tour selection objective ($/hr-primary vs profit-primary) |
| `TOUR_SOLVER_RATE_ARMED_LONG` | `1` (armed) | off | 0/1 | for long tours (cap>2) this is the SOLE objective governor |
| `TOUR_SOLVER_SEQUENCER` | `ortools` (armed) | `beam` | ortools\|beam | market-sequencing engine (OR-Tools prize-collecting vs greedy beam) |
| `TOUR_SOLVER_MAX_PLANNED_TRANCHES` | `3` (armed) | 2 | [1,6] | tranches loaded per market/good-side (throughput knob) |
| `TOUR_SOLVER_FULL_SCORE_TOP_N` | `35` (armed) | 20 | [10,100] | stage-2 full-scoring shortlist size (candidate widening) |
| `TOUR_SOLVER_ORTOOLS_MAX_NODES` | `160` (armed) | 80 | [40,400] | OR-Tools per-model node cap (bites only under sequencer=ortools) |
| `TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS` | (unset → default) | 3 | [2,5] | global per-call OR-Tools wall budget (protects p99 solve latency) |
| `TOUR_SOLVER_ORTOOLS_MAX_SUBSETS` | (unset → default) | 8 | [1,32] | max subset models solved per call |
| `TOUR_SOLVER_ORTOOLS_TIME_VALUE` | (unset → default) | 10.0 | [0,1000] | time-value pricing (credits/second) in the objective |

Also in `run.sh` (process knobs, not solver policy): `ROUTING_HOST` (0.0.0.0), `ROUTING_PORT`
(50051), `TSP_TIMEOUT` (5s), `VRP_TIMEOUT` (30s). Solver constant worth knowing:
`MAX_HOPS_DEFAULT = 6` (planner hop ceiling). **Doc/code drift to flag:** `run.sh`'s comment
calls the budget knob `ORTOOLS_TIME_BUDGET_SECONDS`, but the code reads
`TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS` — use the code name.

### 3.4 Arming conventions

- **Default-off = byte-identical.** New features ship as a `{0,1}` knob (Layer B) or an unset
  export (Layer C) that reproduces prior behavior exactly. Closing/merging a bead ships the code
  **default-off**; *arming* it (a `tune ... 1` or a `run.sh` export + restart) is a **separate,
  untracked step**. A merged bead is not a live feature.
- **Keep an arming ledger.** Because arming is untracked, maintain a running list of which
  default-off knobs are currently armed and where (the `run.sh` armed exports are self-documenting
  in their comment blocks; Layer B arms are only visible via `tune --operation <op>` reads).
- **A restart can reset live state.** A daemon restart snaps every pattern-B knob back to
  `config.yaml`; a routing restart re-applies `run.sh` (so an *uncommitted* armed export is lost
  if the shell that set it is gone, but a committed/`run.sh`-default arm re-applies). **Re-verify
  every intended arm after any restart** — read Layer B with `tune --operation <op>`, confirm
  Layer C with the routing process env / `run.sh`.

---

## 4. Hard constants (compile-time; not knobs)

- **`maxTreasuryFractionPercent = 25`** (probebuy + frontier coordinator) — RULINGS #6 hard
  per-hull ceiling: a single probe buy may never exceed 25% of live treasury. A guard, never
  weakened; if ever made tunable, only behind a hard ceiling of 25.
- **`ImmutableReserveFloor = 50000`** — RULINGS #5 working-capital floor. Effective reserve is
  `max(50000, configured)`; the 50k is an immutable lower bound. Mirrored across tour, factory,
  trade-route, outfitting, and auto-outfit engines (all 50000).
- **Per-tour spend cap = 25% of live treasury** (tour-run default when `--max-spend 0`,
  re-resolved each tour in continuous mode).
- **Stocker capital ceiling = 10% of live treasury** per buy; **tour deposit ceiling ≈ 10%**
  of treasury staged as working deposit.
- **`MAX_HOPS_DEFAULT = 6`** — routing planner hop ceiling (also the `tour-run --max-hops`
  planner default).

---

## 5. Token discipline — scoping flags quick reference

Fire scoped; verify novel flags at `--help` first (a malformed call costs ~3×).

| Verb | Scope it with |
|---|---|
| `market spreads` | `--system` (required), `--top N`, `--json` |
| `market list` / `find` / `get` / `history` | `--system` / `--good` / `--waypoint` |
| `captain report` / `captain tokens` | `--days N` (default 7), `--json` |
| `captain events list` | `--json` (then `ack --before`/`--all` in bulk) |
| `tour report` | `--since <dur>` (default 168h) |
| `ledger list` | `--category`, `--limit N`, `--start-date`/`--end-date` |
| `ledger report` | `profit-loss`/`cash-flow` + `--start-date`/`--end-date` |
| `history *` | `--era N` (patterns default `all`), `--good X` |
| `waypoint list` | `--system` (required), `--type`, `--trait` |
| `scout posts add` | `--freshness`, `--hulls N`, `--kind` |
| `container logs` | one container id (from the workflow verb that spawned it) |
| `tune --operation <op>` | read one `<key>` instead of the full table; `--json` for scripts |

General rules: add `--json` when parsing programmatically; always pass `--agent SYMBOL`
(era-stable) over `--player-id`; scope every list/history read by system/era/date/window rather
than dumping the default full set.
