# Era-4 Memory Review — Pre-stage (DREAM CONSOLIDATION, RECOMMEND-ONLY)

> **Admiral decisions (2026-07-18, applied):**
> - **Captain model = Opus 4.8** (pin unchanged). The `crew-model-policy` fable-5 city
>   memory is confirmed-wrong → RETIRE (both db copies) at the reset gate.
> - **The dense engineering gap-lines got a new home: `ENGINEERING.md`** (repo root, the
>   shipwright's field manual) rather than the shipwright template — keeps the template lean.
>   The 8 shipwright-destined GAP lines + the twin/test KEEP-AS-MEMORY invariants now live in
>   ENGINEERING.md §1–§5; those source memories retire once verified against the book.
> - GAP lines APPLIED to the books this session (commit trailer references this doc):
>   PLAYBOOK §9/§11, CLI-PRIMER §1/§2/§3, captain template (hub-roster), ENGINEERING.md (new).
>
> **Crash note:** the writing session crashed mid-file; the KEEP-AS-MEMORY row-detail and the
> RETIRE-stale list below the CONSOLIDATED-RETIRE table were not written. The 20 GAP lines and
> the 111-row CONSOLIDATED-RETIRE mapping ARE complete. Retirement executes post-reset
> (backup-first, per-key); the stale-list is re-derivable as "every memory not named in a
> KEEP/CONSOLIDATE/CONSOLIDATED-RETIRE row." Regenerate the tail before the retirement pass.


**Date:** 2026-07-18
**Purpose:** Classify every `bd` memory (engineering-db `sp-` + city-db `st-`) ahead of the
era 3 → era 4 reset (2026-07-19T13:00Z) under the **dream-consolidation** model
(PLAYBOOK §12 "Memory consolidation"): durable knowledge moves INTO the canonical books
(RULINGS.md, PLAYBOOK.md, CLI-PRIMER.md, the four `city/agents/*/prompt.template.md`) and the
memory store shrinks to only genuinely operational, era-scoped state. A memory duplicating a
book line is debt (token cost every prime + a contradiction waiting to happen).

**Judged against the CURRENT books** (all re-read today, 2026-07-18, after their heavy update):
RULINGS.md now carries **20 rulings** (grew from 15 — #16 gc-off-limits, #17 protected-paths+prod-isolation,
#18 three-build-lanes, #19 closed-is-not-armed, #20 never-block-on-Admiral, all "consolidated 2026-07-18");
PLAYBOOK.md added the **dream-cycle** subsection; the templates absorbed most operational doctrine.

**Verdicts** (per PLAYBOOK §12):
1. **CONSOLIDATED-RETIRE** — content already lives in a book; cite the destination. Retire after approval.
2. **CONSOLIDATE-GAP** — timeless rule NOT yet in any book; destination + exact line(s) to add given below.
3. **KEEP-AS-MEMORY** — genuinely operational/era-scoped, or an engineering code-fact with no proportionate
   book home. Kept deliberately rare.
4. **RETIRE** — era-3-stale with no transferable rule, or a duplicate.

**Execution (all verdicts):** RECOMMEND-ONLY. Retirement is **backup-first, per-key, Admiral-approved,
AFTER era-close** (the sp-p0oy protocol). **Admiral-sourced memories retire ONLY with explicit sign-off**
(flagged `[Admiral]` below). CONSOLIDATE-GAP lines are applied to the books by the team lead
(through the shipwright, Tier-3, with Admiral sign-off) BEFORE the source memory is retired.

---

## Summary counts

| Verdict | Eng (`sp-`) | City (`st-`) | Total |
|---|---|---|---|
| CONSOLIDATED-RETIRE | 96 | 15 | 111 |
| CONSOLIDATE-GAP | 19 | 1 | 20 |
| KEEP-AS-MEMORY | 2 | 4 | 6 |
| RETIRE (stale/dup) | 35 | 2 | 37 |
| **Total** | **152** | **22** | **174** |

The 20 CONSOLIDATE-GAP memories collapse into **~19 distinct book lines** (some share a line).
The 6 KEEP-AS-MEMORY are 2 engineering-craft facts + 4 twin/test-harness code invariants that
have **no proportionate crew-book home** — flagged below as a possible structural gap (there is
no engineering/testing reference book among the four).

---

## Contradictions for Admiral decision

**Only one true contradiction remains** (the era-3 lane-cap conflict is now RESOLVED — see note).

1. **`crew-model-policy` — captain model, city copy vs RULINGS #9.**
   - City-db copy: *"captain session runs claude-fable-5; all other crew run sonnet-5"* (Admiral 2026-07-06).
   - **RULINGS #9** (canonical, 2026-07-10, restated in the 2026-07-18 amendment): *"Captain session =
     claude-opus-4-8. Standing crew = sonnet-5."* The eng-db copy already matches RULINGS #9.
   - Book wins → recommendation: **captain = opus-4-8**; RETIRE the city fable-5 copy, CONSOLIDATED-RETIRE
     the eng copy into RULINGS #9. **Admiral: confirm the era-4 captain model** before respawn — the
     agent.toml → city.toml `args_append` pin is silent if wrong.

**Resolved (no longer a contradiction):** the old `concurrency-cap-3-lanes-total` vs "no caps" conflict
is gone — the current books carry BOTH on separate axes: **RULINGS #18** ("Three build lanes TOTAL")
governs live-lane concurrency, **RULINGS #10** ("No merge caps") governs fixes/features per day. Both
`concurrency-cap-3-lanes-total` (city) and `orchestration-policy` (city) are now CONSOLIDATED-RETIRE.

---

## CONSOLIDATE-GAP — timeless rules to add to the books (then retire the source memory)

Grouped by destination. Each line is generic (no era specifics / bead-id war stories) and ready to apply.
Source memory keys in parentheses.

### PLAYBOOK.md → §9 (Operations discipline)
- *(cache-desync-doctrine)* "A phantom/stale ship-state cache — wrong cargo/position/role vs the server,
  API 4204 'already at destination', or a foreign-cargo hull silently benched — is cleared in-band with
  `ship refresh` (force GET /my/ships), NEVER a daemon restart; restarting a healthy daemon to clear a
  desync is a defect reflex. On any ship-state error naming cargo/position/role, refresh the whole pool,
  not just the named hull."

### PLAYBOOK.md → §11 (Tooling rules)
- *(db-query-hygiene-learned…)* "Daemon Postgres tables `ships`, `market_data`, `shipyard_inventory` are
  MULTI-PLAYER — always scope aggregates by the current era's `player_id`, or you count competitors and
  manufacture phantom bugs (`gate_edges` is universe-wide, correctly unscoped). `/my/ships` paginates
  20/page — read `meta.total` and every page before counting."
- *(rtk-compact-git-log…, rtk-gate-hygiene…)* "The `rtk` proxy filters output: `rtk git log` hides merge
  commits (verify merges with `rtk proxy git log --graph`) and `rtk git status/diff` filters `.jsonl`
  churn — use RAW `git` for any gate-critical hygiene or merge-verification check."
- *(git-reset-hard-on-the-shared-main-checkout)* "NEVER `git reset --hard` a shared checkout: it reverts
  tracked `.beads/issues.jsonl`, making bd abandon its real dolt db for a fresh empty shadow (looks like
  total data loss; the dolt data is safe under `.beads/dolt/`). Abort a bad merge with `git merge --abort`
  or restore only your own files."
- *(gc-mail-send-can-fail-silently…, captain-mail-mark-read-not-read)* "`gc mail send` can fail SILENTLY
  behind packs/core warnings — confirm the `Sent message … to <role>` line before treating mail as
  delivered. `gc mail read` does not reliably persist read-state through the rtk proxy (mail keeps
  re-notifying) — use `acd mail mark-read <id>` / archive to actually clear, verified with `acd mail count`."

### CLI-PRIMER.md → §1 (Daemon & services)
- *(socket-backends-doctrine)* "Two data planes back the CLI: the Unix socket (health/ship/container/workflow)
  is the DAEMON; market/ledger/player/history reads are POSTGRES. A SQLSTATE error while the socket still
  answers is a DB-side outage, not daemon death — keep operating on socket data; don't hand-probe repeatedly."

### CLI-PRIMER.md → §2 (Fleet — ship / shipyard)
- *(captain-navigate-owns-fuel)* "`ship navigate`/`ship route` spawn a routed container that plans segments
  and AUTO-REFUELS en route — never manually `refuel` or compute fuel/coordinates before dispatching a nav;
  manual fuel planning is a defect signature."
- *(manual-ship-purchase…, captain-hull-naming-and-shipyard-price)* "`shipyard purchase` is ASYNC (spawns a
  batch container; verify via ship count + `transactions`, not CLI output) and buys AT THE YARD WHERE THE
  BUYER IS DOCKED — dock the buyer at the target yard first. Concurrent single-buys on one buyer RACE the
  claim handoff — use `--quantity N` in one call. A LIMITED-supply yard's price inflates with repeated buys,
  and `--budget B` buys only as many as fit B (budget for the risen price or use a fresh yard). Read the
  `Purchased SHIP_* at <wp>` transaction rows for true per-unit price. Callsigns run …-1..9 then …-A..Z
  (never …-10) — detection greps must use `-([0-9]|[A-Z])`."

### CLI-PRIMER.md → §3 (Knob system / observability)
- *(captain-container-get-launch-frozen)* "`container get` serializes a coordinator's launch-frozen
  metadata: live mutations (`fleet hub add/remove`, `tune`, `goods factory`, `construction override`) do
  NOT appear there until a daemon restart re-syncs from the DB. Verify a live-mutated value by its
  behavioral effect or the DB row, never by `container get`."

### shipwright template → ## Delegation
- *(worktree-isolation-is-soft, worktree-dispatch-hazard, worktree-stray-commit-to-main, shipwright-lane-main-tree-pollution)*
  "Worktree isolation is SOFT — each lane gets a separate checkout + cwd, but not a write-sandbox; agents
  can still write to main via absolute paths. Give agents WORKTREE-ABSOLUTE edit paths (bare `gobot/…`
  relatives resolve against main), NEVER run two lanes on the SAME FILE concurrently (the one true
  corruption path — serialize them), and verify the main working tree is pristine
  (`git status --porcelain gobot/`) after every lane before deploying."
- *(fake-blindness-sp-1hp9)* "When a feature writes rows with relational constraints, the test plan must
  include at least one end-to-end path through the REAL persistence layer (test-DB with FKs live)
  exercising the write ORDER — port/boundary fakes validate logic but never schema or insert-ordering."
- *(spawned-fix-agents-share-this-session…)* "Spawned lanes share the session's job tmp — a prior task's
  scratch file can be read by a later lane; brief agents to use a unique per-bead filename or inline the
  content, and verify the final commit subject before merge."

### shipwright template → ## Supervision
- *(respawn-protocol-taskstop-the-predecessor-first)* "TaskStop a predecessor/zombie lane FIRST — even when
  it looks dead (death-marker, zero activity) — and confirm it stopped before respawning or adopting its
  worktree; a zombified session can still hold its process and wake into the same worktree as the resumer."

### shipwright template → ## Verify
- *(gobot-uses-inverted-market-columns)* "gobot stores market columns INVERTED vs the API:
  `market_data.PurchasePrice` = the market's BUY column = what you RECEIVE selling = the API's `sellPrice`;
  `SellPrice` = the ask = what you PAY. A test/consumer that maps API `purchasePrice`/`sellPrice` straight
  is the inverted-margin trap (overstates spreads ~2×)."

### shipwright template → ## Money paths
- *(shipwright-coordinator-defaults-must-match-fleet-practice)* "When a standing coordinator takes over a
  manual operator workflow, its config DEFAULTS must reproduce the operator's STANDING practice (e.g. the
  captain's always-flown reserve), not the code's per-run defaults — a silent default swap can legally draw
  the fleet down. Encode the values the human actually flew."

### shipwright template → ## Deploy
- *(deploy-recovery-if-make-deploy-daemon-fails…, shipwright-daemon-deploy-recovery)* "If a deploy leaves
  the daemon down and `launchctl print` shows `state = spawn scheduled` + repeated `exit code 78 EX_CONFIG`,
  that is a restart-throttle WEDGE, not a broken binary (confirm by running the binary directly). Recover
  with `launchctl bootout gui/<uid>/<label>` then `bootstrap gui/<uid> <plist>` — a plain deploy retry does
  not clear the throttle."
- *(the-supervisor-daemon-is-the-watchkeeper…)* "The watchkeeper plist must carry `BD_REAL=<abs path to the
  real bd>` — the bd-router shim shadows the real bd and the plist PATH lacks it, so without it every captain
  wake fails silently (gc → bd → exit 127); re-add after any plist regen."

### captain template → ## Fleet logistics doctrine
- *(shared-enumerate-hulls-by-location)* "To read a hub's roster or buffer, enumerate ALL hulls by
  `location_symbol` (+ role, container, cargo) — never an assumed/remembered set; hulls get repurposed into
  warehouse/stocker roles mid-era. Never buffer a good the hub already EXPORTS locally."

---

## CONSOLIDATED-RETIRE — content already in a book (retire after approval)

Admiral-sourced (`[Admiral]`, retire only with explicit sign-off) first, then alphabetical, per db.
"via GAP line" = the destination is one of the CONSOLIDATE-GAP lines above (retire only after that line lands).

| Memory key | db | Destination (already-in-book) |
|---|---|---|
| admiral-afk-never-a-dependency `[Admiral]` | eng | RULINGS #20 (never block on the Admiral) |
| admiral-boundary-ruling-2026-07-10-harbormaster-shipwright `[Admiral]` | eng | shipwright + captain ## Chain of command (engineering ≠ fleet control) |
| admiral-working-style-reinforced-2026-07-15-corrected `[Admiral]` | eng | captain ## Engine improvement (file automation specs, not manual band-aids) |
| captain-api-boundary `[Admiral]` | eng | RULINGS #3 + captain HR1 + economy-analyst HR1 (SELECT-only psql) |
| captain-automation-needs-complete-validated-guards `[Admiral]` | eng | RULINGS #4 + PLAYBOOK §10 (re-enable guards per-path) |
| captain-capex-autonomy `[Admiral]` | eng | captain ## Autonomy + RULINGS #6/#20 |
| captain-census-per-wake `[Admiral]` | eng | captain ## Scaling auto-assess (idle audit) + wake ritual |
| captain-engine-restart-protocol `[Admiral]` | eng | captain HR3 (open/kill income stream = refute consult) + ## era rule 6 + RULINGS #4 |
| captain-file-proper-bugs-not-notes `[Admiral]` | eng | captain ## Engine improvement (observations never accumulate unfiled) |
| captain-fleet-hands-runbook-protocol `[Admiral]` | eng | shipwright ## Chain of command (RUNBOOK bead); dup of admiral-boundary-ruling |
| captain-fleet-ops-direct-cli `[Admiral]` | eng | captain ## Chain of command (sole actuator via CLI) + ## Autonomy |
| captain-hull-doctrine-no-frigates `[Admiral]` | eng | captain ## Fleet logistics (command frigate) + PLAYBOOK §5 |
| captain-mail-notify `[Admiral]` | eng | RULINGS #8 + PLAYBOOK §11 (every send `--notify`) |
| captain-money-deploy-acceptance `[Admiral]` | eng | captain wake ritual step 5 (verify guard params) + PLAYBOOK §10 |
| captain-prefer-static-assignment `[Admiral]` | eng | captain ## economy (static dedication) + RULINGS #7 |
| captain-scaling-autoassess `[Admiral]` | eng | captain ## Scaling auto-assess (5 points) + PLAYBOOK §5 |
| captain-single-agent-constraint `[Admiral]` | eng | PLAYBOOK §3 (serial, one contract) + §9 (one agent per operation) |
| captain-wake-mail-read `[Admiral]` | eng | captain wake ritual step 1 (sweep to unread-zero, read+mark) |
| captain-wake-quiet-close `[Admiral]` | eng | captain wake ritual step 7 (routine chat close = one line) |
| captain-worker-abundance `[Admiral]` | eng | PLAYBOOK §5 + captain ## Fleet logistics (cheap dual-duty lights) |
| context-preserving-pause-admiral-2026-07-11-refines `[Admiral]` | eng | shipwright ## Delegation (death-safe yield: bank findings, resume on rebase) |
| hard-rule-admiral-2026-07-07-emphatic-never `[Admiral]` | eng | RULINGS #16 (gc source off-limits) |
| lane-cap-is-not-the-captain-s-concern `[Admiral]` | eng | RULINGS #18 + shipwright ## Delegation (lane cap is engineering's) |
| lane-pause-resume-protocol-admiral-2026-07-11 `[Admiral]` | eng | shipwright ## Delegation (yielded lane resumes on rebase) |
| nothing-is-deferred-to-the-next-era-admiral `[Admiral]` | eng | PLAYBOOK §12 (nothing deferred to next era) |
| notify-amendment-admiral-order-2026-07-09-overrides `[Admiral]` | eng | RULINGS #8 (every captain mail nudged) |
| pipeline-refill-admiral-directive-2026-07-09-appends `[Admiral]` | eng | shipwright ## continuous improvement loop (keep the pipeline full) |
| shared-captain-state-files-deprecated `[Admiral]` | eng | RULINGS #11 + captain HR2 (no state files; beads are the record) |
| shared-contract-flow-over-margin `[Admiral]` | eng | RULINGS #1 amendment (weighting via config) + PLAYBOOK §3 |
| shared-daemon-restart-must-be-resilient `[Admiral]` | eng | RULINGS #2 (daemon restarts always resilient) |
| shared-scout-freshness-model `[Admiral]` | eng | PLAYBOOK §5 (freshness = circuit time; partition) + CLI-PRIMER §2 (scout) |
| subagent-model-tiering-admiral-directive-2026-07-09 `[Admiral]` | eng | shipwright ## Delegation + RULINGS #9 amendment (pick model per dispatch) |
| a-leaked-captain-reservation-silently-blocks-every-coordinat | eng | PLAYBOOK §9 (forced reservations leak and block coordinators) |
| acd-gc-mail-actionable-crew-mail-must-use | eng | PLAYBOOK §11 + RULINGS #8 (`--notify`) |
| agent-lane-dispatch-silently-inherits-the-agent-definition | eng | shipwright ## Delegation (pick model explicitly) + RULINGS #9 |
| agent-stall-root-cause-2026-07-09-the | eng | shipwright ## Supervision (worktree-first; take-over; no-worktree ≠ no-work) |
| amends-routing-service-deploys-memory-the-kickstart-boot | eng | shipwright ## Deploy (routing special lane: regen proto, kickstart first) |
| bd-ready-type-does-not-accept-a-comma | eng | PLAYBOOK §11 (comma type-lists return empty); dup of comma-lists |
| bd-ready-type-does-not-accept-comma-lists | eng | PLAYBOOK §11 (use `bd ready -l <label>`) |
| before-filing-a-bug-dispatching-a-fix-lane | eng | PLAYBOOK §12 (validate symptom-vs-code) + captain ## Engine improvement |
| captain-contracts-are-the-reliable-core | eng | PLAYBOOK §3/§1 (contracts = crash-proof funding floor) |
| captain-contracts-never-skip | eng | RULINGS #1 (never skip contracts) |
| captain-dont-reactive-thrash-containers | eng | PLAYBOOK §9 (hands off a running fleet) |
| captain-freshness-derives-from-planner-cap | eng | PLAYBOOK §5 (stale system is invisible to tours) |
| captain-net-treasury-lags-measure-window | eng | PLAYBOOK §8 + captain ## economy (treasury lags; sum ledger by category) |
| captain-nudge-shipwright-on-file | eng | RULINGS #8 + captain ## Engine improvement (mail nudge on file) |
| captain-per-engine-throughput-check | eng | captain ## Conduct (constraint audit) + PLAYBOOK §8 (per-line rates) |
| captain-report-only-closed-hours | eng | PLAYBOOK §8 (KPI = net over closed hours; gross ~3× net) |
| captain-scout-tours-are-multiship | eng | PLAYBOOK §5 + CLI-PRIMER §2 (scout: N probes disjoint partitioned) |
| captain-wake-model-sp-sk68 | eng | captain wake ritual step 8 + CLI-PRIMER §2 (captain wake) |
| checking-whether-a-prometheus-metric-exists-on-metrics | eng | shipwright ## Verify (exact-name anchored greps, multiple samples) |
| construction-manages-own-factories | eng | PLAYBOOK §4 + CLI-PRIMER §2 (construction `--depth`) + captain era rule 4 |
| crashloop-resumes-on-deploy-sp-ess3 | eng | captain ## Engine improvement (coordinators own relaunch; don't re-roll) |
| crew-model-policy | eng | RULINGS #9 (captain = opus-4-8); see Contradictions |
| deploy-acceptance-reads-verify-at-the-effect-point | eng | shipwright ## Verification + PLAYBOOK §10 (verify at the EFFECT point) |
| deploy-checklist-every-daemon-boundary-rebuilds-both-binarie | eng | shipwright ## Deploy (`make restart-daemon` + `make install-cli`) |
| deploy-rule-addition-sql-migrations-in-gobot-migrations | eng | shipwright ## Deploy (SQL migrations = manual psql + pg_constraint verify) |
| dispatch-briefs-load-rulings-md-sp-nvpn-2026 | eng | shipwright ## RULINGS.md registry + ## Delegation (quote RULINGS verbatim) |
| economy-analyst-arb-profitability-gate | eng | PLAYBOOK §2 (own trading moves prices; margin decay; re-price) |
| economy-analyst-arb-sink-limited-not-source | eng | PLAYBOOK §2 (arbitrage is sink-limited; add markets) + §5 |
| economy-analyst-arb-vs-serialized-contracts | eng | PLAYBOOK §2/§3/§5 (serial ceiling; arb parallelizes; realized fills) |
| economy-analyst-contracts-uncontrollable-cycletime-only | eng | PLAYBOOK §3 ($/hr on cycle-time; can't select) + RULINGS #1 |
| economy-analyst-price-impact-model | eng | PLAYBOOK §2 (price-impact priors + slow recovery; refit each era) |
| friction-beads-must-carry-a-queue-label-e | eng | PLAYBOOK §11 + shipwright ## Queue (queue label at creation) |
| gate-integrity-verify-before-trust-2026-07-09 | eng | RULINGS #12 + shipwright ## Verify (numstat non-empty before trusting) |
| l27-d-7-d-8-container-running-data | eng | PLAYBOOK §8 (RUNNING is a process state, not progress) |
| l36-d-16-a-multi-session-hold-needs | eng | captain ## Conduct (every self-imposed limit carries a named expiry) |
| l40-d-20-d-21-d-59-a | eng | PLAYBOOK §9 (income.stalled during churn benign) + captain wake ritual step 1 |
| lane-close-out-includes-killing-the-agent-not | eng | shipwright ## Close-out (stop the agent) + ## Supervision |
| margin-and-per-line-p-l-claims-must | eng | PLAYBOOK §8 (never mix gross/net) + CLI-PRIMER §2 (`ledger report profit-loss`) |
| notifying-the-captain-of-a-live-change-ruling | eng | RULINGS #8 + captain ## Engine improvement (mail + nudge) |
| probe-assignment-status-idle-snapshots-are-misleading-do | eng | PLAYBOOK §5 (charting ≠ scanning; verify markets are read) |
| routing-service-deploys-are-a-separate-step-the | eng | shipwright ## Deploy (routing lane: regen proto, kickstart first) |
| rtk-gate-hygiene-2026-07-09-rtk-git | eng | PLAYBOOK §11 via the rtk-proxy GAP line |
| shared-agent-stall-at-gate-fresh-rescue | eng | shipwright ## Supervision (take over; fresh-adopt) + ## Delegation |
| shared-deploy-one-at-a-time-verified | eng | shipwright ## Deploy (batch by content; health-check; notify) + RULINGS #8/#12 |
| shared-hold-merged-bead-deploy-note | eng | shipwright ## Deploy (merged-not-deployed is a normal honest state) |
| shared-manufacturing-feeding-verdict | eng | PLAYBOOK §2 (feeding grows export, can't fatten a sink) + §7 + §6 (API ceiling) |
| shared-notify-captain-on-delivery | eng | RULINGS #8 + shipwright ## Notify (light one-line notice; detail in bead note) |
| shared-orchestration-liveness-and-dispatch | eng | shipwright ## Supervision (liveness signals flap; verify ground truth) |
| shared-spacetraders-cli-ops | eng | CLI-PRIMER §2 (CLI any-cwd; shipyard visibility) + PLAYBOOK §11 |
| shared-subagent-throughput | eng | shipwright ## Delegation (cap at 3; test changed package) + RULINGS #18 |
| shared-verify-deployed-head-before-filing | eng | shipwright ## Verify (numstat on real main HEAD) + PLAYBOOK §12 |
| shipwright-daemon-deploy-recovery | eng | shipwright ## Deploy via the launchd-wedge GAP line |
| shipwright-lane-main-tree-pollution | eng | shipwright ## Delegation via the worktree-isolation GAP line |
| shipwright-orchestration-protocol-v2-binding-admiral-ordered | eng | shipwright template (whole: Delegation/Supervision/Verify/Deploy/Notify/Close-out) + RULINGS #12/#13 |
| shipwright-verify-live-mutation-at-persisted-store | eng | shipwright ## Verify (live cross-check at the store the consumer reads) + ## Verification |
| structural-gate-infrastructure-go-no-go-jump-gate | eng | captain ## Consults (structural go/no-go: step 0 supply-feasibility) + PLAYBOOK §4 |
| the-live-or-tools-vrp-routing-engine-is | eng | CLI-PRIMER §1 (routing service path) + §3.3 |
| validate-capability-before-filing-feature | eng | PLAYBOOK §12 (validate symptom-vs-code before 'build X') + captain ## Engine improvement |
| verification-doctrine | eng | PLAYBOOK §8/§10 + shipwright ## Verification + captain wake ritual step 5 |
| warehouse-value-is-time-not-money | eng | PLAYBOOK §3 (source from warehouse first) + captain ## Fleet logistics (deposits book zero) |
| workflow-subagent-worktrees-under-claude-worktrees-trigger-t | eng | shipwright ## Delegation (never stage issues.jsonl; hook re-sweeps) |
| worktree-commits-in-the-spacetraders-repo-must-always | eng | shipwright ## Delegation (commit `--no-verify` unconditionally) |
| worktree-dispatch-hazard-2026-07-15-st-780 | eng | shipwright ## Delegation via the worktree-isolation GAP line |
| worktree-stray-commit-to-main-pattern-recurred-2x | eng | shipwright ## Delegation via the worktree-isolation GAP line |
| admiral-working-style-2026-07-12-reinforced-the `[Admiral]` | city | shipwright ## Delegation (orchestrator delegates all implementation + investigation) |
| acceptance-reads-must-exercise-the-failing-case-not | city | shipwright ## Verification + PLAYBOOK §10 + captain wake ritual step 5 (verify the FAILING case) |
| bd-db-resolution-is-cwd-based-from-city | city | PLAYBOOK §11 + shipwright ## Queue (bd resolves by cwd) |
| bd-update-notes-replaces-the-whole-notes-field | city | PLAYBOOK §11 + shipwright ## Close-out (`--append-notes`, never `--notes`) |
| before-acting-on-any-stranded-hull-cargo-nav | city | PLAYBOOK §8 (trust live API over local DB for hull facts) + economy-analyst consult step 2 |
| concurrency-cap-3-lanes-total-admiral-ruling-2026 | city | RULINGS #18 (three build lanes total) + shipwright ## Delegation; contradiction now resolved |
| daemon-fleet-observability-d1-confirmed-ship-list-findallbyp | city | PLAYBOOK §8 (`ship list` reads local DB; trust live API) |
| deploy-close-out-must-end-with-a-p0 | city | shipwright ## Close-out (P0 sweep before done) + captain wake ritual step 7 |
| dormant-config-consumer-enumeration-when-a-fix-makes | city | shipwright ## Delegation + ## Arming + RULINGS #19 (enumerate consumers at magnitude) |
| gc-mail-send-syntax-positional-recipient-first-s | city | captain/shipwright templates ## Basic CLI (mail examples: `-s`/`-m`/`--notify`) |
| heavy-not-trading-despite-a-tour-run-container | city | PLAYBOOK §9 (one container per hull; two controllers = loops) |
| lane-pause-enforcement-a-lane-counts-as-running | city | shipwright ## Delegation/Supervision (TaskStop frees the slot) + RULINGS #18 |
| orchestration-policy | city | RULINGS #10 (no caps) + #18 (three lanes) + #9 (model) + shipwright ## Delegation/Throughput |
| shipwright-session-beads-not-tasks | city | PLAYBOOK §11 (type=session beads are bookkeeping) + shipwright ## Queue |
| visual-features-need-on-screen-verification-not-just | city | shipwright ## Verification (rendered-layout + screenshot) + PLAYBOOK §10 + captain wake ritual step 5 |
