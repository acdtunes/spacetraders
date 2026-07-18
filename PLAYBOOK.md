# PLAYBOOK — Standing Rules & Strategies (all crew)

Read this when you are primed (cold start of any session), after your role template and
`RULINGS.md`. Everything here is a standing rule or a strategy, era-agnostic. Numbers marked
**(prior)** are fitted starting points from past eras: plan with them, but re-measure this
universe before betting on them. RULINGS.md outranks this file; this file outranks habit.

---

## 1. The era

- The universe resets weekly. Era identity = `reset_date` / player_id (`universe status`),
  never the agent callsign — callsigns are reused across eras.
- **Two phases, always.** Phase 1 = gate-construction sprint: optimize TIME-TO-GATE, not
  margin; treasury is never the Phase-1 constraint — supply-state and time are. Phase 2 =
  frontier expansion + heavy trade: optimize per-hull sustained $/hr against the absorption
  ceiling. Detect the flip with `construction status <gate-waypoint>`; never assume the phase.
- **Hour-0 duties:** start the bootstrap reconciler (`workflow bootstrap`) — it plays the
  early game hands-off (DATA: guarded staged probe buys + scout-all-markets to the coverage
  bar → INCOME: frigate retire, contract hubs, capital-gated hauler buys, starts the batch-
  contract operation → GATE: starts the construction pipeline, sticky to COMPLETE). The
  captain STARTS operations and TURNS KNOBS; hand-flying the early game is the fallback,
  not the plan — watch the bootstrap heartbeat (phase · progress · blockers) and clear
  blockers via knobs, not manual flying. Frontier expansion stays OFF until the gate
  completes (it expands over gate edges — there is nothing to expand pre-gate); the
  freshsizer boots standing and takes over the durable freshness fleet once markets are
  scanned. Then: pin the era KPI on the strategy bead — metric basis (net credit delta over
  closed hours) plus measurement window — before any optimization talk; economy-analyst
  delivers the era economy map once; schedule the construction shakedown inside the first
  ~12h, graded only on material-delivered > 0.
- **Three walls bound every era:** (1) the API rate limit — per ACCOUNT, cannot be sharded
  around; (2) market absorption / sink depth; (3) the era clock — every capex must pay back
  inside remaining era-hours.
- **Endgame runbook:** freeze all hull/probe purchases at T-10h before reset; rundown at T-6h;
  dump cargo at T-1.5h. A late purchase is pure leaderboard loss. Nothing gets live-armed
  unvalidated inside the final 12h — an unvalidated lever waits for next era.
- The leaderboard scores CREDITS. Rank is won on per-hull sustain, not fleet count.

## 2. Market physics (priors — refit each era)

- **Trade volume = per-tranche depth.** A market absorbs ~volume units per price step. Size
  haulers to the lane's trade volume, never to hold size. Manufacturing lanes run thin
  (vol ~6 **(prior)**); EXCHANGE goods run deep (vol ~180–240 **(prior)**).
- **Your own trading moves prices.** Sustained buying walks the ask up (~+5% per tranche
  bought **(prior)**); selling into an import crushes the bid (~−1.5% per tranche **(prior)**,
  40–60% on a dump). Bid bounce half-life ~1–1.5h; full reversion ~8–9h; a crushed lane is
  dead 12–24h **(priors)**. Give worked lanes fallow time; budget margin decay into every plan.
- **Feeding a factory's imports revives and grows its EXPORT volume and is itself
  margin-positive. Feeding never deepens the import side** — you cannot fatten a sink.
  Arbitrage is sink-limited: depth grows ONLY by adding markets (new systems), never by
  pushing harder on one.
- Prices are only visible with a ship present and go stale fast. Freshness is a fleet-wide
  requirement — never blind-cut price scans to save API; stale prices turn arbitrage into
  losses.
- **Column semantics:** market columns are the MARKET's perspective. Buy at EXPORTERS, sell at
  IMPORTERS; profit = destination-BUY − source-SELL. When a spread looks too good, settle the
  direction with one live transaction before scaling.
- Routes decay as competition equilibrates: hold several, exit below the margin floor, budget
  round-trip fuel +10%. "Not sold in-system" is a time-stamped observation — re-sweep before
  locking a premise.

## 3. Contracts

- One contract is active at a time — the engine is serial. Contract $/hr is won on
  CYCLE-TIME, nothing else: pre-position idle dual-duty haulers at export-origin hubs
  (closest-ship-wins compresses the buy leg); idle staged haulers at hubs are deliberate,
  not waste.
- Source from the warehouse first — zero-ask withdrawal beats a market buy. Stock the
  fat-tier goods (weapons-class draws run at multiples of the median) from the first
  warehouse day.
- Contract legs never leave the worker's system (RULINGS #14). Cross-system logistics belongs
  to the trade engine.
- Payouts are lumpy (accept + deliver): derive $/hr from several cycles, never one.
- The engine never refuses, skips, or value-filters a contract (RULINGS #1). Portfolio
  weighting between contracts and trade is a captain/Admiral call, made through config —
  never through code that declines work.

## 4. Construction & the gate

- Only the home gate must be built; it opens the entire connected graph. The bill is a public
  read (~1600 FAB_MATS + 400 ADVANCED_CIRCUITRY **(prior)** — read it fresh).
- **The fill model:** gate materials are the EXPORT of a source factory. Buy that output and
  haul it — AND run a parallel feed-the-factory loop (buy the factory's imports elsewhere,
  sell to it) so its export stays flowing and affordable. Pure market-buying at scale
  self-defeats: each buy drains export supply and walks the ask into the buy-ceiling guard.
  Feed the WHOLE supply chain, for every factory. Never frame the fix as "manufacture the
  final good ourselves."
- Throughput is gated by the material's minimum supply state, not treasury: run
  `construction start --min-supply SCARCE`, pay premium asks, proceed incrementally.
- Diagnose WHICH cap binds before scaling: a supply-capped material gains nothing from more
  haulers.
- Gate-support factories run `--inputs-only`; the construction pipeline is their sole buyer.
- Pin gate haulers with durable, restart-surviving dedication BEFORE the first fill task.
  Construction sources workers from unassigned idle hulls.
- During Phase-1 fill lulls, pre-harden Phase-2 tooling: at ~50% gate, shake down `ship jump`
  and one guarded cross-gate circuit end-to-end. Expect first-exercise defects in clusters;
  fix in-crew, same-day.

## 5. Fleet & scaling

- **The scaling law:** deepen one system to its arb ceiling (~6 hulls **(prior)**), then WIDEN
  to the next scout-confirmed fresh system — opening a second system roughly doubles
  absorption. Past the ceiling, marginal hulls collapse to sub-floor filler.
- Wide multi-system tours out-earn single-system ~2.6× **(prior)** — candidate hop-depth and
  planner coverage are the biggest trade levers; audit that the planner actually SEES the
  lanes you think it sees.
- Every buy passes the 5-point scaling auto-assess (captain template): idle audit first,
  constraint named, measured demand, ≤25% treasury + payback inside remaining era-hours,
  then act THIS wake. Heavies earn ~3× per hull **(prior)** and stay worth buying until the
  endgame freeze.
- Buy at the cheap shipyard — hull and probe prices vary up to ~8× by yard **(prior)**; keep a
  purchase agent docked at the yard. Any per-cycle spend cap must exceed the unit price it is
  supposed to allow, or it silently starves the buyer forever.
- **Probes and coverage:** charting a system is NOT scanning its markets — verify markets are
  actually read. The tour planner only sees markets fresher than its age cap: a stale system
  is INVISIBLE to the money engine (stale → no tours → no revenue → looks unimportant — a
  self-reinforcing blind spot). Freshness equals probe circuit time: partition circuits;
  never try to "scan faster."
- Pre-gate, buy 2–3 scouting probes only; no extraction or gas hulls without a proven
  delivery path.

## 6. API budget

- The rate limit is per ACCOUNT. Fleet growth does not add API capacity — API efficiency is
  the late-game lever, not hull count. The overwhelming majority of calls are nav/scan/dock
  overhead, not trades.
- Prioritize fundamental calls (dock, orbit, refuel, get-market, navigate) over bulk scans;
  cache aggressively; widen reuse windows deliberately — but never blind-cut price scans
  (see §2).

## 7. Refuted strategies (do not relitigate without new evidence)

- **Mining / gas extraction:** ore bids collapse within ~2 sale visits and raw inputs are ~1%
  of finished-good value — feeding factories from markets beats extraction everywhere
  measured.
- **"Manufacture the final good ourselves"** for gate materials — wrong frame (see §4).
- **Cross-system contract sourcing** — the serial contract clock can never afford it.
- **Value-filtering contracts** — forbidden (RULINGS #1).
- **Monitoring/polling between wakes** — the wake model is the only standing sensor; batch
  everything into heartbeats; watch live only when the immediate next action hangs on a
  single-shot outcome, then kill the watch.
- **A second warehouse at the same waypoint** — resolution is newest-RUNNING-wins; the second
  is dead capital.

## 8. Measurement rules

- The KPI is NET credit delta over closed hours. Gross realized revenue runs ~3× net **(prior)**
  — never mix the two, and never project income from transaction-table sums (spend eats
  earnings); project from credit deltas only.
- Trust the live API over any local DB or cache for hull facts (location, cargo, role) before
  acting; check cache age on every cached read — a cache whose age you have not checked is
  not evidence.
- Async pipelines get ≥15-minute validation windows — end-to-end latency makes shorter
  windows produce false failures.
- EXECUTING/RUNNING is a process state, not progress: demand the first observable OUTPUT
  (a ferry, a buy, a park, a claim, a ledger movement) within one window from any
  never-exercised subsystem.
- Measurement windows are opportunity-cost math: in a week-scale era with minute-scale
  cycles, HOURS of data decide hull-scale bets. Grade the evidence trend, not the review
  date — monotonic movement as predicted is ANSWERED.

## 9. Operations discipline

- **Hands off a running fleet.** Once configured, non-intervention is the highest-value
  operator action. Never stop a container or reassign a hull mid-tour to "fix" throughput —
  a mid-tour abort cascades cooldowns fleet-wide. Intervene through `tune`, config, and
  beads, not by killing containers.
- One container per hull, one agent per operation — two controllers on one resource produces
  loops, never trades.
- Ownership audit before any manual fleet action: whose hull, which standing policy? Prefer
  filing the engine fix; if you must bridge, captain-owned hull only, fix bead filed first,
  time-boxed, end-state verified. Avoid forced reservations — they leak and block
  coordinators.
- An `income.stalled` during tour-relaunch churn is often benign — verify ledger flow (one
  SELL) before treating it as real.
- Daemon restarts churn every container: batch deploys by content, never by wall-clock.
  After ANY restart: read the recovery line (N recovered, 0 lost), diff the fleet roster,
  and RE-VERIFY live-tuned knob values and fleet dedications — a restart can silently reset
  them to defaults.

## 10. Engineering discipline (details live in the shipwright template)

- **Closed is not armed.** Features ship default-off by convention; a bead that ships an
  armable knob stays OPEN until the knob is armed — or consciously disabled with the reason
  recorded — in the arming ledger. Audit the dormant-knob list at every deploy. A dormant
  knob is not a delivered feature.
- **Merged is not proven.** Verify every deploy live at the EFFECT point, against the FAILING
  case named on the bead — never a healthy neighbor sharing its label. Visual features are
  proven on screen, never by backing-store queries.
- Three or more bugs sharing one root cause means you are fighting the architecture — file
  the structural fix instead of patch N+1. When a fix unblocks a previously-masked code
  path, the newly-reachable path is exactly where the next bug hides — verify it. One
  worker's bug must never panic the daemon.
- Money guards fail CLOSED and are never weakened as a side effect. Re-enable guards
  PER-PATH, one engine at a time, and verify guard PARAMETERS, not just presence. Spend
  guards scale with treasury above the fixed working-capital reserve.
- All code moves worktree → captain-gate → main. Commit before gating (`--no-verify`, never
  stage `issues.jsonl`); verify the merged SHA's numstat on the REAL main HEAD. Protected
  paths are never touched by build agents: `gobot/internal/captain/**`, `cmd/captain-gate/**`,
  `city/agents/**`. The `gc` city-gateway SOURCE is off-limits, full stop — use it, never
  modify it.
- Test infrastructure never targets the production socket or database — force-inject test
  endpoints so a stray run cannot reach prod.

## 11. Tooling rules

- `bd` resolves by cwd: the engineering queue (sp-) from the REPO ROOT, the city db (st-)
  from `city/`. An impossibly-empty queue means wrong cwd, not "no work."
- Query queues with `bd ready -l <label>` — comma type-lists silently return empty. Use
  `--append-notes` (plain `--notes` REPLACES the field). Every friction bead carries its
  consuming-queue label AT creation. `type=session` beads are bookkeeping, never tasks.
- The open queue can contain already-merged work — grep main for the bead-id/symptom before
  dispatching a lane.
- Mail: sweep every inbox you answer to EXPLICITLY by role name; every send carries
  `--notify`; read bodies before archiving; a truncated Admiral message gets a resend
  request, never a guess.
- The CLI teaches itself: `--help` at every depth, `man -k spacetraders`. Never invent a verb
  or guess flags. Scope every read (`--system`, `--top`, `--tail`, `--era`, `--level`) —
  unscoped dumps burn the turn. The capability map and the full knob system (config layers,
  live `tune` registry, env overrides, arming conventions) live in `CLI-PRIMER.md` at the
  repo root — read it at prime; the live `--help` remains the truth.

## 12. The era boundary

- Transition runs the reset runbook: freeze → pg snapshot → dry-run preview → era-close
  beads → memory review (KEEP/REWRITE/RETIRE with the Admiral — last era's waypoints,
  prices, coefficients, and hull symbols are FALSE PRIORS for the new universe) →
  `universe transition` (the DB is player-partitioned: NEVER truncate; verify the active
  player-id repointed) → seed the strategy bead → bring-up.
- Before retiring or replacing any subsystem, write its capability manifest and prove the
  successor covers every item — collateral deletion of load-bearing capability is how eras
  lose working features.
- A live symptom in a fresh era may be a regressed or deleted capability, not a missing
  feature — validate symptom-vs-code before filing "build X."
- Nothing is deferred "to next era" — that is not a backlog state.

### Memory consolidation (the dream cycle)
- bd memories are the WORKING layer (fast, episodic, ungated). The books — RULINGS.md
  (Admiral orders), PLAYBOOK.md (rules & strategies), CLI-PRIMER.md (tooling), the role
  templates (role behavior) — are CONSOLIDATED doctrine (curated, reviewed, primed once).
- A memory that stabilizes — survives sessions, gets cited, stops changing — is PROMOTED:
  file a consolidation bead, land the generic rule in the right book, then retire the
  memory. A memory that duplicates a book line is debt: token cost on every prime plus a
  contradiction waiting to happen (the book wins any conflict unless the Admiral rules
  otherwise).
- Retirement is backup-first, per-key, Admiral-approved; Admiral-sourced memories retire
  only with explicit sign-off.
- The era boundary runs the FULL consolidation review (every memory: consolidated-retire /
  consolidate-then-retire / keep-as-memory / retire-stale). The store that survives into a
  new era should be small and operational — books carry the doctrine.

---

*Maintenance: standing-rule changes land here the same way RULINGS.md changes do — through
the shipwright with Admiral sign-off (Tier-3). Refit the **(prior)** numbers each era and
update them in place.*
