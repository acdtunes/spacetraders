# Era-3 Retrospective (eras 2+3, 2026-07-05 → 07-19)

**Audience: the Admiral (reference archive).** Agents are NOT primed on this document — the
agent-facing distillation (rules and strategies only, no history) is **`PLAYBOOK.md`** at the
repo root. This file preserves the incident record, the numbers, and the why.

Provenance: written 2026-07-18 (Admiral-ordered retrospective, bead st-4nl) from a full mine of
every era-2/era-3 crew session transcript (~46 sessions, captain + shipwright/harbormaster +
economy-analyst/trade-analyst + surveyor + fleet-architect), all 174 bd memories (both DBs), the
mail archive, RULINGS.md, era3-learnings.md, and the era-3 git history (641 commits on main).

---

## 0. Orientation — how eras work here

- The game is **SpaceTraders.io**: weekly universe resets ("eras"). Everything in the game wipes;
  our code, databases (player-partitioned), beads, memories, and doctrine carry over.
- **Era map:** era 2 = 2026-07-05 → 07-12 reset. **Era 3 = `torwind-2026-07-12`**, 07-12 →
  **07-19T13:00Z** (era-4 reset). All eras reused the callsign **TORWIND** — the callsign is NOT
  an era discriminator; the `reset_date` / player_id is (era 3 = player_id 3).
- **Two-phase era model (validated twice):** Phase 1 = gate-construction sprint (optimize
  time-to-gate, not margin; contracts are the funding floor). Phase 2 = frontier expansion +
  heavy trade (optimize per-hull sustained $/hr against the absorption ceiling). Both eras
  completed their gate and reached Phase 2 (era-2 gate X1-KA42-I53 done day ~3.4, bead
  st-wisp-ctf5; era-3 gate X1-VB74-I55 done ~day 2). *Correction to era3-learnings.md (now
  archived at `docs/retrospectives/legacy/era3-learnings.md`): the
  "no gate ever hit 100%" caveat is wrong — st-wisp-ctf5 records 1600 FAB_MATS + 400
  ADVANCED_CIRCUITRY delivered, isComplete:true.*
- Doctrine lives in three layers, in order of authority: **RULINGS.md** (Admiral standing orders,
  append-only, each with date+origin) → **agent templates** (`city/agents/*/prompt.template.md`)
  → **bd memories** (`bd memories <topic>`; sp- db from repo root, st- db from `city/`).
  This briefing is the fourth layer: the incident record and empirical numbers behind them.

## 1. Era-3 scoreboard

| Metric | Result |
|---|---|
| Final treasury | **~227M credits** (Jul 18 18:40Z, still earning; era-2 closed ~7.7M — a ~30× era) |
| Leaderboard | **#9** (TORWIND), +40M over #10 SAFPLUSPLUS ~185M; #11 ~111M back |
| Gate | X1-VB74-I55 **COMPLETE ~day 2**; full Phase 2 (cross-gate arb in 2+ abroad systems) |
| Throughput | sustained **~10–12M cr/hr gross realized**, peaks ~12–14M; **net treasury growth ~3–5M/hr** |
| Target | "$15M/hr" — **NOT reached** (judged above the era's absorption ceiling absent fresh market depth) |
| Fleet | frigate+probes → ~24 mid-era → **~40+ trade hulls + ~90–157 probes** (~9 real heavies) |
| Biggest left-on-table | the OR-Tools sequencer sat **dormant all era** (unarmed env var); a green-lit heavy-hauler tranche stalled on captain downtime |

**The three walls of era 3** (bead sp-pwwe — memorize these; they will bind era 4 too):
1. **API rate limit: 2 req/s, per ACCOUNT** — cannot be sharded around. Only ~7.5% of era-3
   calls were trades; ~92% was nav/market/dock/refuel/poll overhead. API efficiency, not hull
   count, is the late-game lever.
2. **Market absorption / sink depth** — measured fleet-wide sell ceiling ~42 units/tick;
   ~6 hulls saturate one system's arb. Depth grows ONLY by adding markets (new systems), never
   by fattening one.
3. **The era clock** — every capex must pay back inside remaining era-hours; stop all purchases
   T-8–10h before reset (a late hull/probe is pure leaderboard loss).

**How "15M/hr" failed as a goal:** it was never pinned to gross-realized vs net-treasury-growth —
a 3× difference (each trade buys ~⅔ of what it sells). The endgame optimized while arguing past
itself. **Era-4 rule: define the KPI metric + measurement window on the strategy bead at hour 0.**
Net treasury delta over closed hours is ground truth; the leaderboard is CREDITS.

## 2. Game mechanics that carry over (the physics)

Mechanisms below are stable across eras. **Every fitted coefficient, waypoint, price, and hull
symbol is era-3-stale — re-measure this universe before leaning on a number.**

### 2.1 Market physics
- **Trade volume = per-tranche depth.** A "volume 6" market absorbs ~6 units per price-step;
  manufacturing lanes typically vol 6, EXCHANGE goods deep (vol 180–240). **Size haulers to the
  lane's trade volume, not to hold size** — an 80-cargo hauler is oversized for a vol-6 lane.
- **Your own trading moves prices.** Sustained buying walked asks +66–80% in hours (era-3 fit:
  ask +~5%/tranche bought; bid −~1.5%/tranche sold). Selling into an import crushes bids
  40–60% and the lane is dead 12–24h; short-term bid bounce half-life ~1–1.5h, full reversion
  fitted ~8–9h. Give a crushed lane 2–3h fallow minimum. Budget margin decay into every plan;
  routes equilibrate — hold several, exit below the margin floor.
- **Feeding a factory's IMPORTS revives and grows its EXPORT volume** (observed 6 → 15–22) and
  is itself margin-positive. But feeding **never deepens the import side** — you cannot fatten a
  sink. Arbitrage is sink-limited, not source-limited.
- **Supply ladder** ABUNDANT→MODERATE→RESTRICTED/SCARCE and **activity** (WEAK=thin,
  GROWING=recovering, STRONG) drive prices; "not sold in-system" is a time-stamped observation,
  not standing truth — re-sweep before locking a premise.
- **Prices are only visible with a ship present** and go stale fast. Cutting post-trade scans to
  save API caused real arb losses in era 3. Admiral: *"prices matter in every market!"* — freshness
  is a fleet-wide requirement, not an "arb core" nicety.
- **Column semantics (recurring trap):** market columns are the MARKET's perspective — its
  purchase price is what it pays YOU (your sell side). Buy at EXPORTERS, sell at IMPORTERS;
  profit = dest-BUY − source-SELL. An inversion here overstated spreads 2× in the twin and
  produced at least two wrong analyses. When in doubt, verify against one live transaction.

### 2.2 Contracts
- **One contract active at a time** (serial). Payouts on accept AND deliver; lumpy — derive $/hr
  from several cycles, never one. Theoretical fully-compressed ceiling ~20/hr; era 3 ran ~10/hr.
- **Cycle-TIME is the only contract lever:** pre-position idle dual-duty haulers at export-origin
  hubs (closest-ship-wins compresses the buy leg); warehouse stock fulfills by zero-ask
  withdrawal. Pre-staged idle haulers at hubs are DELIBERATE, not waste (Admiral correction,
  both eras).
- **Contract legs never leave the worker's system** (RULING #14): a ~30-min cross-gate round
  trip on the one-contract clock needs ~200k+ savings to break even; it never does.
- **Phase dependence:** contracts funded Phase 1 (+20.8M over one 48h window while trade ran
  net-negative subsidizing gate inputs). Late-era, per-hull contract marginal fell to ~10k/hr vs
  ~254k/hr on arb, and the Admiral decommissioned the contract machine (07-16, st-bed).
  RULING #1 still binds the ENGINE — it may never refuse/value-filter a contract while contracts
  run; re-weighting the portfolio is the Admiral/captain's call, not code's.
- **Fat-tier draws exist:** weapons-class contract goods draw at multiples of the median —
  stock the fat tier from the first warehouse day.

### 2.3 Construction / the gate
- **Only the home gate must be built** — it opens the whole connected graph. Canonical bill both
  eras ≈ **1600 FAB_MATS + 400 ADVANCED_CIRCUITRY** (+1 QUANTUM_STABILIZERS in era 3). The bill
  is a public read — read it fresh (`construction status <gate-waypoint>`).
- **The fill model (Admiral, 07-13):** gate materials are the EXPORT of a source factory
  (era 3: FAB_MATS@F48, ADV_CIRC@D42). You always BUY that output and haul it — but pure
  market-buying at scale is self-defeating: each buy depletes export supply, the ask climbs
  ("the bill is not 1.58M, it's much larger — as we buy, the bid explodes"), the buy-ceiling
  guard trips, the fill stalls. **Sustain the fill with a parallel feed-the-factory-inputs loop**
  (buy the factory's imports elsewhere, sell to it) so its export stays flowing and affordable.
  Feed the WHOLE supply chain, for every factory, not just tier-1 inputs. Never frame this as
  "manufacture the final good ourselves."
- **Throughput is gated by the material's minimum supply state**, not treasury — run
  `construction start --min-supply SCARCE`, pay premium asks, proceed incrementally. Phase-1
  constraints are supply-state and time, never treasury.
- Era-3's FAB_MATS bottleneck was **supply-capped, not hull-capped** — 4 haulers already
  saturated it; more hulls would have added nothing. Diagnose which cap binds before scaling.
- Gate-support factories run `--inputs-only`; the construction pipeline is their sole buyer.

### 2.4 Fleet, probes, expansion
- **Rivals run 8,000+ ships** — no practical API-side fleet cap at our scale. The 20× gap to top
  agents is per-hull SUSTAIN (idle gaps, duty-cycle), not count. Idle audit before any buy.
- **Wide (multi-system) tours realized 2.6× single-system profit** (943k vs 355k/tour, zero
  losing) — but the single-hop candidate horizon silently dropped fat 3+hop lanes 5,817 times.
  Candidate widening (hop depth ≥2) is the single biggest trade lever; it shipped era-3-final
  but see §6 (validation debt).
- **Absorption scaling law:** deepen one abroad system to its ~6-hull ceiling, then WIDEN to the
  next scout-confirmed system (opening a second abroad system roughly doubled absorption).
  Past the ceiling, marginal hulls collapse to sub-floor filler (<100k/hr).
- **Probes:** CHARTING ≠ SCANNING (a charted system can have 0 scanned markets — era 3 found 47
  charted-unscanned markets sitting invisible). The tour planner only sees markets fresher than
  its age cap (engine setting, era-3 = 75 min): **a stale system is INVISIBLE to the money
  engine** — stale → no tours → no revenue → looks unimportant (self-reinforcing blind spot).
  Freshness = probe circuit time: partition circuits, never "scan faster."
- **Probe economics:** prices varied 8× by shipyard (~20k home vs 161–235k abroad) — buy at the
  cheap yard, keep a purchase agent DOCKED at the yard, and make sure any per-cycle spend cap
  exceeds the unit price (a 100k cap vs a 161k probe = silent starvation forever).
- **Heavies:** era-3 late heavies earned 2.96× per hull (1.7× cargo/trade-call), ~8h payback —
  worth buying even late, but respect the T-8–10h purchase freeze.
- **Ship-buying mechanics:** the buy command docks and auto-buys (read `--help` before
  hand-rolling); buy with the command frigate at the shipyard; construction ops source workers
  from unassigned idle hulls.

### 2.5 Refuted strategies (do not relitigate without new evidence)
- **Mining/gas extraction:** REFUTED twice. Ore bids 67–74/u and dump to floor within ~2 sale
  visits; even 300 u/hr grosses ~21k/hr against a ~700k/hr fleet baseline; ore is ~1% of
  FAB_MATS' output value, so "free inputs" is a rounding error. Feeding factory inputs from
  markets beats extraction everywhere it was measured.
- **"Manufacture the final good ourselves"** for gate mats — wrong frame (see §2.3).
- **Cross-system contract sourcing** — RULING #14's origin (sp-9hu8): the optimizer picked
  cross-gate sources the serial contract clock can't afford; the 25k penalty underpriced the
  mistake ~8×.
- **Value-filtering contracts** — shipped once (sp-snmb), violated a ruling the agent never saw,
  reverted twice. RULINGS.md exists because of this incident.
- **Fleet-architect as a standing role** — retired mid-era-3 (6 consults in 5 days, all
  captain-initiated); fleet-sizing refutation folded into the economy-analyst.

## 3. The era-4 playbook

### Hour 0 (reset day — the Admiral runs the transition)
The 9-phase runbook is `docs/runbooks/universe-reset.md`-class doctrine (see repo). Sequence:
freeze (watchkeeper touches `captain/DISABLED` on resetDate mismatch) → pg_dump snapshot →
`universe transition --dry-run` preview → beads era-close (label+close+demote era-scoped beads)
→ **memory review gate (Admiral approves KEEP/REWRITE/RETIRE — era-3 facts must not become
era-4 false priors)** → `universe transition --agent <NEW> --token <jwt> --confirm` (validates
the token BEFORE any write; player-partitioned DB — **never truncate; the legacy `universe
close` verb is forbidden**) → seed the era-4 strategy bead → bring-up (`rm captain/DISABLED`).
Known open defects in this path: **sp-m602** (transition missed the `captain.player_id`
repoint once — supervisor woke as the dead player; verify it), **sp-peht** (cutover bring-up).
Verify with `universe status` (must show the new era, "in sync").

### Hours 0–12 (Phase 1 opens)
1. Contracts on from hour 0 (RULING #1) — they are the funding floor and the crash-proof engine.
2. Read the live gate bill; identify the bottleneck material's source factory; start the
   feed-the-inputs loop alongside output-buying immediately. `construction start --min-supply
   SCARCE`; pay premiums — Phase 1 buys SPEED.
3. Run the construction shakedown against the real gate inside ~12h, graded only on
   material-delivered > 0. Expect first-exercise defects in clusters; keep the fix loop
   in-crew and same-day.
4. Economy-analyst delivers the era-start economy map ONCE (production chains, activity states,
   extraction viability, shipyard geography, where feeding applies) — the feeding thesis is a
   hypothesis to re-verify against THIS universe, not an inherited fact.
5. 2–3 scouting probes only pre-gate (buy at the cheap yard); no extraction/gas hulls without a
   proven delivery path.
6. Pin gate haulers with durable, restart-surviving dedication BEFORE the first fill task
   (RULING #7).
7. Arb runs alongside the gate — it funds materials and grows sink depth (era-3 evidence:
   arb ACCELERATES the gate, it does not compete with it).

### Phase 1 → Phase 2 (gate completes ~day 2–4)
- Detector: `construction status <gate>` flips COMPLETE. Bootstrap's GATE phase is **sticky on
  ConstructionStarted** (`run_bootstrap_reconcile.go:222` — the L57 lesson: without stickiness
  the phase thrashes back to INCOME and re-buys the haulers it just repurposed).
- During Phase-1 fill lulls, **pre-harden Phase-2 tooling** (the era-3 endgame scramble was
  this rule ignored): at ~50% gate, shake down `ship jump` + one guarded cross-gate circuit
  end-to-end; validate the tour-widening/sequencer knobs on replay (see §6 validation debts).
- At flip: expand through the gate; scale trade hard; margin discipline returns; frontier sinks
  pay best; capex autonomy per RULINGS #6 + the captain's 5-point scaling auto-assess.

### Phase 2 (the long middle)
- KPI: per-hull sustained $/hr, measured on net treasury deltas over closed hours.
- Scale by the absorption law: deepen to ~6 hulls/system, then widen to the next fresh system;
  keep freshsizer fed (probe circuits partitioned; nothing older than the planner's age cap).
- Buy discipline every wake: idle-audit first, measured demand, ≤25% treasury, payback inside
  remaining era-hours (<~30h target mid-era).
- API budget from day 0: priority-aware limiter (fundamental calls — dock/orbit/refuel/
  get-market/navigate — outrank scans), caching, reuse windows. Don't discover saturation at
  92% mid-era. Never blind-cut price scans (stale-price arb losses).
- HANDS OFF the running fleet (see §4.5) — configure, then let it run.

### Endgame (last 12h)
- **T-10–8h: stop ALL hull/probe purchases. T-6h: rundown. T-1.5h: dump cargo.**
- No live-arming unvalidated levers under the clock — era 3's "forget the replay, I take the
  risk" is the anti-pattern; anything not validated by T-12h waits for next era.
- Bank the retrospective: era-close beads, memory KEEP/REWRITE/RETIRE list, capability manifest
  (see §4.8), handoff mail.

## 4. Mistakes that must not recur (incident → root cause → standing rule)

### 4.1 THE defining failure: built-but-never-armed ("closed ≠ armed")
The single most $/hr-critical optimizer of era 3 — the OR-Tools trade-tour sequencer (sp-y05b,
+12.8% replay $/hr) — was merged, closed, and **sat DORMANT the entire era behind an unset env
var**. The captain discovered it by hand on Jul 17. Admiral: *"WHY THE FUCK ARE WE BUILDING
THINGS THAT NEED ARMING AND FORGETTING TO ARM THEM?"* This bit the crew ≥3× (sequencer,
candidate widening, freshsizer hold). Root cause: features ship default-off (byte-identical,
governance-gated — correct), the bead closes on merge, and arming is a separate UNTRACKED step.
**Standing rule (sp-nc0m, open P1 ledger):** a bead touching an armable knob is NOT closed until
the knob is ARMED or consciously-disabled-with-reason, recorded in the arming ledger; audit the
dormant-knob list at every deploy and every captain wake. Merged ≠ live; closed ≠ armed;
RUNNING ≠ effect.

### 4.2 Money-guard incidents (why RULING #4 is layered and fail-closed)
- sp-bp6f: trade circuits with no spend floor drained **11M → 43k**.
- sp-9aoc: factory input-buying with no floor burned **848k → 23k in one minute**.
- sp-2dv4: no chain-margin guard — 1.17M sold into already-crushed bids.
- sp-5nqx: an arb retry-loop re-bought past `--max-spend` (guards must bind across retries).
- sp-rqwm: a MAKE stage sold at the BUY market (column semantics again).
- Re-enable discipline: guards are **per-path** — after a freeze, re-enable ONE engine at a
  time; a blanket re-enable "validated none" and lost ~20M.
- Verify guard **PARAMETERS**, not just presence/behavior (the "53k night": a guard was live
  with the wrong floor).
- **Guards must scale with treasury:** a 100k/window cap tuned for a poor fleet throttled the
  probe ramp while 5.75M sat idle. Fail-closed, treasury-relative, floors junior only to the
  fixed 50k working-capital reserve (RULING #5).
- Un-pausing an income stream IS opening one — file the refute consult first.

### 4.3 Merge/gate integrity (why RULINGS #12/#13 exist)
- **Empty-merge incident (sp-k0di):** captain-gate accepted message-only commits — three
  "landed" fixes never existed in the binary, yet were deployed, closed, and notified. Rule:
  commit in the worktree BEFORE gating; after the gate, verify the merged SHA's numstat lists
  your files against ACTUAL main HEAD (the gate may squash — check what's really on main).
- **Gate contamination (sp-ezar):** the gate's squash-merge ran without `--no-verify`, so the
  beads pre-commit hook swept `.beads/issues.jsonl` into every merge — the catastrophic case is
  sweeping PEER lanes' files onto main. Fixed via `commit-tree` (bypasses index+hook) +
  `assertNoForeignStaged`. Corollaries: commit with `--no-verify` in lanes, never stage
  `issues.jsonl`, and note **rtk filters .jsonl from git output — use RAW git for gate checks**.
- **Proto-provision bug (sp-a3r9, verify before first proto bead):** captain-gate `--provision`
  reverts a worktree's regenerated proto (overwrites the bead's new `daemon.pb.go` with main's
  stale one) — proto-changing beads landed by manual build/test + FF-merge in era 3.
- **Stray commits to main ×2** and the **Jul-14 git-reset catastrophe** (an agent reset away
  committed work; recovery + re-push). Worktree isolation is NOT a write-sandbox — keep the
  main-checkout path out of agent briefs entirely (relative paths only), orchestrator runs the
  gate, verify the main tree pristine after every lane.
- **bd tracker corruption (git-reset-hard incident):** bd silently fell back to an empty
  embedded-dolt shadow; 785-issue jsonl restored from a stale 721. If bd output looks
  impossibly empty, suspect db resolution before "no work."

### 4.4 Prod-is-the-test-env (the verification law)
Five separate live-only regressions (sp-ht1f/o34q/snmb/n0x7/pafv) passed the gate green — unit
fakes are "too ideal" (no FK enforcement, no one-ship-one-container claim semantics).
**L42: merged ≠ proven until first LIVE exercise.** Verify every deploy against the FAILING case
named on the bead (never a healthy neighbor sharing its label) at the EFFECT point — the first
effect action (a ferry, a buy, a park, a claim, a ledger movement). A boot line, a RUNNING
status, a row in a table prove nothing. Visual features need an on-screen render check (era-3's
nebula shipped invisible with green backing-store tests). Repeat-offender signature: **4–5
patches on the same root cause = you're fighting the architecture** — the trade-route CLI-runner
family (r3cl/sh6w/2sam/sj7p/ynuf) ended only with the structural fix (sp-zewt: promote to daemon
container; RULING #3's origin).

### 4.5 Ops churn — the operator was the bottleneck
Era-3's biggest self-inflicted revenue loss: fleet peaked 11.7M/hr and operator "fixes"
(restarting coordinators, stopping 11 containers, reassigning hulls mid-tour) crashed it to
**−1.4M/hr within the hour**. Stopping a container mid-tour aborts the tour → cooldowns →
collapse. Analyst's own words: *"I was the bottleneck."*
- **One container per hull** — two controllers on one ship = endless jump-loops, zero trades.
- **HANDS OFF once configured** — hands-off is the highest-value trade action; intervene only
  through config/beads, not by killing containers.
- `income.stalled:trading` during tour-relaunch churn is often benign — verify ledger flow (one
  SELL_CARGO) before treating it as real.
- Manual `ship reserve --force` leaks captain reservations that block coordinators — audit
  ownership before manual fleet action; prefer filing the engine fix.
- Deploy restarts churn the whole container fleet — batch daemon restarts by CONTENT (HOT
  payloads solo; ≥2 regular payloads; captain request; pre-freeze sweep — never wall-clock).

### 4.6 Orchestration & delegation failures
- **Captain liveness was mission-critical and failed.** The era-3 captain ran headless, went
  down repeatedly (~51 unanswered nudges/escalations at era end), and the endgame's one real
  lever — a green-lit heavy-hauler tranche — stalled until the Admiral bought hulls manually.
  The Admiral spent most of era 3 typing into the SHIPWRIGHT session instead. Era 4: watchdog +
  auto-respawn the captain; the sole fleet-scaling actuator can't be a single fragile session.
- **Fleet commands are NEVER delegated to subagents** (Admiral, 07-13: the subagent burned an
  hour rediscovering the CLI). Captain flies via CLI directly; subagents may READ code/data.
- **Read the CLI help first** — both eras' captains hand-rolled what a verb already did
  (auto-refuel on navigate, dock+auto-buy). A malformed invocation costs ~3× (usage dump).
- **Agents idle silently without reporting** (known failure mode, ~6 named incidents): verify
  ground truth in the worktree directly (diff, tests, gate log) — never trust the claim, never
  block on a dead agent; take over, commit, gate.
- **3-lane concurrency cap TOTAL** (Admiral 07-11, no exempt classes): past it, suite contention
  + stale-rebase cascades go negative-sum; `-race` suites eat disk (below 5GB free:
  `go clean -cache` first). Exceeding requires NAMING which lane yields, in the dispatch itself.
- **Largest-diff lanes first**, trickle the rest, one direction of stale-cascade; never two
  lanes on one file (the only true corruption path).
- **Ultracode/mikado workflows lost completed work twice** (Jul 16 W1–W3 restarts; Jul 18 the
  era-ending mikado refactor lost finished Mikado parts). Checkpoint long workflows; verify
  each wave landed (numstat) before launching the next.
- **Stale agent definitions:** era-3 built "100s of features" through a stale `spacetraders-dev`
  agent def on the wrong model before anyone noticed. Audit which agent/model a pipeline
  actually uses; delete stale defs.
- **Model tiering (RULING #9 refined in practice):** sonnet for mechanical fully-spec'd lanes;
  opus for root-causing/design; the Admiral later added Fable for review panels ("the best
  model on each agent by task complexity, with review gates"). Era-3 practice drifted
  opus-heavy (128 opus / 9 sonnet of 177 dispatches) — pick deliberately, per dispatch.
- **Mail hygiene:** the dual-inbox bug silently dropped the shipwright's inbox (session answers
  to two names; `gc mail inbox` resolves the FIRST) — sweep explicitly (`gc mail inbox
  shipwright`). Crew mail bodies arriving EMPTY is a still-open bug (st-8xd). `gc mail send`
  is positional-recipient, `-s`/`-m` — and every send carries `--notify` (un-nudged mail sits
  unread; RULING #8).
- **bd traps:** `bd ready --type bug,feature` comma-lists return EMPTY (run per-type or by
  label only); `bd update --notes` REPLACES the field (use `--append-notes`); db resolution is
  cwd-based (sp- from repo root, st- from `city/` — running from the wrong cwd shows a
  convincing empty queue); friction beads need their consuming-queue label AT creation;
  `type=session` beads are bookkeeping, never tasks.
- **The open queue lies:** sessions die mid-close, so open beads include already-merged work —
  content-grep main for the bead-id + symptom before dispatching any lane.
- **"Archaeology" ban:** report concise current state, act; don't narrate incident history at
  the Admiral (charged both eras). Memories record the RULE, not the incident, under a stable
  key (`bd remember --key <role>-<topic>`), updated in place; Admiral-sourced memories are
  KEEP-class and never pruned without sign-off.

### 4.7 Measurement traps (each produced a wrong decision before being caught)
1. **Gross vs net** (~3×) — pin the KPI (see §1).
2. **Transaction-table sums don't reconcile with credits** — the analyst projected "+15.5M/6h"
   while the balance was flat (spend ate earnings). Project off CREDIT DELTAS only.
3. **Ask/bid inversion** — "pays 15.6k" was the ask side; real bid 7,738. Bit captain AND
   analyst. One live transaction settles it.
4. **Corrupt DB frame column** mislabeled 8 probes + 3 lights as heavies (fleet "19" heavies,
   real 9) — trust the live API over the local DB for hull facts; before acting on any
   "stranded hull" claim, confirm via live API.
5. **Too-short validation windows** — frontier discovery has ~12-min end-to-end latency;
   called "failed" twice before it completed. Use ≥15-min windows for async pipelines.
6. **EXECUTING/RUNNING proves persistence, not progress** — demand the first observable OUTPUT
   within one window from any never-exercised subsystem.
7. **Measurement windows are opportunity-cost math** — in a week-scale era with minute-scale
   cycles, HOURS of data decide hull-scale bets; "wait a day" is almost always wrong math.

### 4.8 Era-transition regressions (the reset is a destroyer of capability)
- **sp-jav2:** retiring a "second coordinator" deleted a 41-file parallel-manufacturing
  subpackage as collateral — era 3 spent days restoring multi-hull fill, fabrication, and
  continuous refill from `ef2281b8^`. **Keep a capability manifest across the reset; validate
  symptom-vs-code before filing "build X"** (a live symptom may be a deleted capability, not a
  missing feature).
- **Era-2 lessons were relitigated in era 3** (multi-hauler contract staging, CLI-verb
  rediscovery, monitoring bans) — doctrine must land in RULINGS/templates/memories, not chat.
- **Memory hygiene at the boundary:** era-N waypoints/coefficients/hull symbols become era-N+1
  FALSE PRIORS unless the KEEP/REWRITE/RETIRE review runs with the Admiral before the new
  captain wakes.
- The runbook itself was wrong once (ordered a truncate; the DB is player-partitioned — never
  truncate) and the transition missed the player_id repoint once (sp-m602). Dry-run first.

## 5. Standing constraints

### 5.1 RULINGS.md (repo root) — the 15 standing Admiral orders, binding on everyone
Read the file; summary: (1) never skip/value-floor contracts · (2) daemon restarts always
resilient, all operational state persists · (3) single-writer ship state (daemon only) ·
(4) money guards fail CLOSED, never weakened · (5) parametrize, don't hardcode (50k reserve is
deliberately non-tunable) · (6) hull buys = measured demand + ≤25% treasury · (7) ownership
model is law (no poaching pins; frigate hauls last) · (8) every live change mails AND nudges
the captain · (9) crew model policy (captain=opus-4-8, crew=sonnet-5, shipwright delegates to
ephemeral subagents, model picked per dispatch) · (10) no merge caps · (11) bd is the tracker,
`bd remember` is memory · (12) merges are COMMITS — verify the merged SHA's diffstat ·
(13) only captain-gate merges agent work to main · (14) contract ops are single-system ·
(15) captain-set wake triggers are ONE-SHOT.

### 5.2 Standing orders NOT in RULINGS.md (live in memories/templates — equally binding)
- **The Admiral is ALWAYS away** — never block, never ask to choose; act on your best
  recommendation and surface async. Passivity is the one failure mode. (Most-cited order.)
- **3-lane concurrency cap** (07-11, supersedes all exempt classes).
- **gc source code is OFF-LIMITS, full stop** (07-07) — gc is out-of-repo shared runtime infra;
  a hot-swap breaks the whole fleet's comms. USE gc; never MODIFY it, even for a real bug.
- **Game state via the `spacetraders` CLI only** — no raw API calls (07-09).
- **Engineering ↔ fleet boundary:** shipwright never commands hulls; captain never edits code.
  Admiral-ordered ops: shipwright does the engineering half, hands the fleet half to the
  captain as a runbook bead (07-10).
- **Nothing deferred to next era** (07-11) — "next era" is not a backlog state.
- **Tier-3 rails move only with Admiral sign-off:** agent templates (`city/agents/`), the
  watchkeeper (`internal/captain/`), the captain-gate binary, the kill switch
  (`captain/DISABLED` — Admiral's alone; if you see it, idle). A pipeline that can rewrite its
  own gate has no gate.
- **Merge-cap history:** caps exist to throttle UNATTENDED autonomy, never attended war-rooms —
  that distinction is why RULING #10 deleted them.
- **Delegated approvals are verified against the BEAD, not a peer's relay** — a surveyor mail
  once overstated an approval; the shipwright correctly refused to act on it.
- **Big features need Admiral sign-off BEFORE code** (`bd human <id>`), never retroactively.

## 6. The engine you inherit (do NOT rebuild any of this)

One `spacetraders` cobra CLI + three daemons (`spacetraders-daemon`, `routing-service` [Python
tour solver], `watchkeeper`), deployed via launchd (`deploy/launchd/`). Standing reconciling
coordinators (catalog: `gobot/internal/domain/container/container.go:40-136`):

- **Trading:** trade-fleet coordinator (continuous profit-ranked tours), tour solver with fitted
  market-price model + OR-Tools prize-collecting sequencer + beam fallback, replay harness
  (`routing-service/model/`), impact-aware lane ranking, absorption ledger (planned
  reservations + recovery shadows), $/hr rate objective, closed-tour mode, candidate widening
  to 2–3 gate-hops, reposition-reach, priority-aware API rate limiter.
- **Contracts:** contract fleet coordinator + sourcing optimizer + ship pool + contract-depot +
  hub-placement coordinator (single-system by RULING #14).
- **Construction:** construction coordinator + unified gate-fill + pipeline planner (parallel
  supply workers, fabrication, continuous refill — the restored sp-jav2 capability).
- **Bootstrap:** DATA→INCOME→GATE reconciler (`workflow bootstrap`, live-by-default), phase
  DERIVED from live observation each tick (never a persisted enum), GATE sticky on
  ConstructionStarted, fail-closed on unreadable state.
- **Scouting:** freshsizer (P90 value-weighted market-freshness auto-sizer), scout posts,
  frontier expansion (demand-measured probe buys), shipyard backfill, probe-reuse +
  cross-system relay, manning watchdog.
- **Fleet:** worker rebalancer, capacity autosizer (guarded auto-buy), capacity reconciler
  (SENSE→PLAN→DIFF→GOVERN→CONVERGE, capex proposals), auto-outfit, warehouse + stocker
  (durable dedication), gas/siting.
- **Safety:** the money-guard stack (trade floor bp6f, factory floor 9aoc, chain-margin 2dv4,
  cross-container spend cap w3he, absorption bounds, per-run min-margin), 25%-treasury const,
  claims/ownership (atomic ClaimShip, fleet pins), CAS ship writes with retry, lifecycle
  re-adoption on restart, panic barriers + supervised boot + crash-loop escalation.
- **Captain infra:** watchkeeper (DB-backed detectors → event beads + mail + tmux nudge),
  captain events queue, one-shot wake triggers/tripwires, `captain report` telemetry, dynamic
  GAG (soft pause), kill switch.
- **Observability:** Prometheus (:9091) + Grafana (:3000) via `docker-compose.metrics.yml`,
  7+ dashboards, `/metrics` :9092, fleet-health alerts wired to watchkeeper paging.
- **Era machinery:** `universe transition` single-command rollover, player-partitioned history
  (`history summary|goods|contracts|pnl --era <era>` — priors are hypotheses), `universe
  status` era-identity check.
- **CLI self-teaching:** `--help` at every depth + ~120 man pages (`man -k spacetraders`) +
  `captain/CLI_REFERENCE.md` (offline convenience; live `--help` is truth).

### 6.1 Config threading (three layers — know which one your knob lives in)
(A) `config.yaml` → stamped into container config at (re)launch only (a coordinator restart
re-reads it; config.yaml edits bounce EVERY container). (B) **live `tune` verb** → validates
against the TuneBound registry, amends persisted container config, re-read per tick, survives
restart, audited as a `config.tuned` captain event — the preferred lever. (C) routing env →
`run.sh` `${VAR:-default}` exports read once per solve. **An armed routing knob = an
UNCOMMITTED run.sh export; `git checkout -- run.sh` disarms everything — treat run.sh diffs as
live fleet state, not cruft.**
⚠️ **OPEN DEFECT sp-ve3q/sp-rsgc: a coordinator restart resets live-TUNED knobs to defaults —
silently disarming tuning. Until fixed, re-verify tuned values after every restart.**

### 6.2 Knob ledger at era-3 close (sp-nc0m is the authoritative tracker — keep it current)
- **ARMED (verified 07-17, most live-armed WITHOUT full replay validation — era-4 must
  re-validate or disarm):** `TOUR_SOLVER_OBJECTIVE=rate`, `RATE_ARMED_LONG=1`,
  `SEQUENCER=ortools`, `MAX_PLANNED_TRANCHES=3`, `FULL_SCORE_TOP_N=35`, `ORTOOLS_MAX_NODES=160`
  (all run.sh); `max_tour_systems=4`, `reposition_reach_enabled`, `reposition_rate_floor_enabled`,
  `max_probe_price=100000`, `api_priority_scheduling_enabled` (config/tune).
- **DORMANT (built, off):** `candidate_hop_depth` (default 1 = single-hop — **the #1 $/hr lever**,
  593 unpaired sinks behind it; arming was mid-flight at reset — see the two
  `*_TEMP.py` replay/timing harnesses in `routing-service/`); `placement_score_enabled`
  (disabled on a now-stale premise — re-evaluate); `ORTOOLS_TIME_VALUE` (placeholder, never swept).
- **DELIBERATELY OFF (data-backed):** `closed_tours` (replay −25% beam / −19.6% ortools).

### 6.3 Known-broken / handle-with-care at era-4 open
- **The digital twin is UNMERGED.** The twin server lives on branch `feat/twin-digital-twin`
  (not on main — main's `twin/` has tests only). DATA harness 8/8 green; the INCOME (9) +
  GATE (8) e2e runs were built but NEVER executed. Merge the branch and run the full harness
  before trusting the bootstrap loop in era 4 (design:
  `docs/superpowers/specs/2026-07-06-spacetraders-digital-twin-design.md`).
- **sp-a3r9:** captain-gate `--provision` corrupts proto-regenerating worktrees (§4.3).
- **sp-ubwi:** gate fill caps at 2 parallel lanes (1:1 task-per-material pairing).
- **sp-2jrz:** capacity reconciler still plans for the decommissioned contract domain and
  re-strands trade hulls (the "9 idle lights" bug) — decommissioning a domain must update the
  reconciler.
- **sp-pyas:** trade full-hold gridlock (heavies buy loads they can't sell).
- **Routing-service deploys regenerate BOTH proto sides** (Python stubs are gitignored —
  regenerate in the service venv or it serves the old proto); kickstart routing FIRST, then
  the daemon. Daemon plist needs `ExitTimeOut ≥ 35` so launchd honors the drain.
- **Go build cache:** phantom "package X is not in std" errors from parallel jobs are
  environmental — `go clean -cache`, retry; don't chase your diff.

## 7. Era-4 day-1 checklist (condensed)

1. Admiral: run the 9-phase reset runbook (§3 Hour 0). Verify `universe status` = new era,
   `player info` = new player_id, and the `captain.player_id` repoint (sp-m602).
2. Verify the captain session is ALIVE and stays alive (watchdog); confirm wake policy set.
3. Sweep the inherited run.sh/config armed-knob state: decide validate-vs-disarm for every
   entry in §6.2 (the era-end arms were risk-accepted, not validated). Update sp-nc0m.
4. Confirm restart-recovery: `make restart-daemon` → "N recovered, 0 lost" line, roster diff.
5. Contracts on hour 0; gate bill read; feed-loop planned; shakedown scheduled inside 12h.
6. Economy-analyst: era-start economy map (once). Re-fit price-impact/absorption coefficients
   before any model-driven decision.
7. Triage inherited open beads (§8): close the stale, keep the live — grep main first.
8. Set the era-4 strategy bead: KPI definition (metric + window), phase, targets, player_id.

## 8. Inherited open work (triage list, era-3 close)

**Live P1s (verified still-relevant):** sp-nc0m (arming ledger process), sp-ve3q/sp-rsgc
(restart resets tuned knobs — fix early, it silently disarms everything), sp-pyas (full-hold
gridlock), sp-2jrz (reconciler re-strands hulls), sp-7q5t (593 unpaired sinks — candidate
widening validation), sp-u8jc (cross-system probe relay arming), sp-1txd (capacity autosizer),
sp-vh1s (unified gate-fill), sp-hvtx (bootstrapper twin-harness epic — build the twin),
sp-a3r9 (gate --provision proto bug), sp-ubwi (2-lane gate cap), sp-1z4q (whole-codebase RPP
refactor — the era-ending mikado run FAILED and lost completed steps; restart deliberately,
with checkpoints).
**Reset machinery:** sp-nax3 (transition verb — landed, verify), sp-peht (bring-up), sp-m602
(player_id repoint miss).
**City side:** st-drm.* (bootstrap harness), st-8xd (empty mail bodies — still biting),
st-7zk (capacity reconciler epic), st-4nl (this retrospective — close on delivery).
**Queue pollution warning:** ~150 open sp- beads include era-stale strategy/handoff beads
(sp-59xl, sp-4m2s, sp-lh69), unlabeled `friction:` P3s from eras 1–2, and already-merged-but-
open work. Run the era-close triage before trusting `bd ready`.

## 9. Crew process doctrine — what your templates already say, and the gaps this doc fills

Your `city/agents/<role>/prompt.template.md` already encodes: the autonomy prime-doctrine, the
two-phase era model, the wake ritual (mail→events→assess→act→validate-deploys→scaling-assess→
record→declare-next-wake), consult lifecycle (refute-first, structured notes,
Recommendation/Evidence/Confidence/What-would-change-my-mind), the 5-point scaling auto-assess,
warehousing stages, frigate staging, dispatch-brief anatomy (worktree-first, recon coordinates,
RULINGS quoted, TDD, gate invocation verbatim, numstat verify, do-NOT list), deploy batching,
notify+acceptance loop, Tier-3 rails. **Honor the templates; this document adds the WHY.**

The gaps the templates do NOT yet carry (this doc + memories are the only record):
1. **The arming ledger discipline** (§4.1) — propose promoting to RULINGS.md.
2. **The captain-liveness requirement** (§4.6) — the watchkeeper escalation loop must have
   teeth; 50 escalations went unanswered at era-3 close.
3. **KPI definition at era start** (§1).
4. **The capability manifest across resets** (§4.8).
5. **The incident→ruling causal history** (§4 throughout) — why each rule exists.
6. **The empirical market-physics numbers** (§2) — refit, but start from these priors.

### Token/cost discipline (both eras' recurring Admiral complaint)
No monitors or polling between wakes — the wake model is the only standing sensor; batch
everything into heartbeats. Scope every read (`--top`, `--tail 20`, `--level ERROR`, `--era`,
`--system`). No archaeology. One-line routine closes. Heavy interactive skill work runs in
DISPOSABLE sessions, never in standing crew sessions (shared weekly quota). Era-3 burned 5.8M+
tokens on one ultracode day — parallel agent fleets are for P1 blast-radius/epics, not routine.

---

*Sources: full session-mine reports + raw extracts archived in the retrospective scratchpad
(era3/ captain-report, shipwright notes, economy-report, beads-memory-report, repo-report);
canonical artifacts: RULINGS.md, era3-learnings.md, city/agents/*/prompt.template.md,
sp-nc0m (arming ledger), docs/audits/2026-07-15-config-knob-audit.md, the universe-reset
runbook. Verified live 2026-07-18: TORWIND player_id 3, 227.3M credits, era `torwind-2026-07-12`,
next reset 2026-07-19T13:00Z.*
