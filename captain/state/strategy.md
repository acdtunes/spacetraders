# Standing strategy

## Standing Admiral directives
**#1 FABRICATION PATH (s71, Admiral order #1).** The jump-gate bill (FAB_MATS 1600 + ADVANCED_CIRCUITRY 400 at
X1-PZ28-I67) and the manufacturing income stream run on the SAME engine: `operations start --system X1-PZ28
--manufacturing` (discovers high-demand goods, prefer-fabricate resolver) + targeted `goods produce <GOOD> --system
X1-PZ28`. PREMISE CORRECTED s86 (L58): both goods are NOW sold in-system — our own factories woke the exports
(FAB_MATS @F56 532 ABUNDANT, ADVANCED_CIRCUITRY @D45 1,900 MODERATE) → depth-3 buy-final IS available, bill ≈1.6–2M.
GAS OPS EVALUATED s86 (Admiral ask): IMMATERIAL — 0% of fabrication inputs are siphonable (gases only enter recipes
via EXPLOSIVES, never reached; resolver buys ore/sand/crystal leaves) and gases cost 23–28 at C42 anyway. CLOSED;
plumbing (STORAGE_ACQUIRE_DELIVER, cost-0) exists if a gas-consuming chain ever matters. Manufacturing auto-claims idle light
haulers with NO exclusion flag, BUT ship ASSIGNMENTS already give mutual exclusion (a claim writes
`assignment_status='active'` → hidden from every other coordinator), so a manufacturing/construction hauler is
auto-excluded from contracts with NO flag needed (reservation flag REJECTED as phantom, reverted 6fee4f1).
X1-PZ28 has a real prefer-fabricate thesis (cheap ores J70/B7 → SCARCE high-value finished goods A1) but sell
volumes 6–20 SCARCE/WEAK cap standalone $/h (L13). `goods produce` has NO --dry-run (friction).

## KPI targets
- **Credits/hour: baseline ~21,900/hr; ACTUAL ~216k/hr (~9.9× KPI, s86), climbing since manufacturing went live.** The binding
  constraint on the EARNER is CYCLE TIME, not contract supply (d-35/L48): contract EXECUTION = ~67% of wall-clock
  is travel; cadence is endogenous (coordinator negotiates next contract on fulfillment). A 2nd hauler cut mean
  buy-leg distance via the "select closest ship" balancer → more cycles/hour. Coordinator runs ONE contract at a
  time (L45) — extra haulers add positioning, not parallelism. NET per mega-contract ~+174k at ~67–73% margin
  (L45). Watch margin erosion (rising cargo cost vs payout) as the saturation signal (L13).
- Fleet utilization: no ship idle > 60 min without a recorded reason.

## Horizon plan (Admiral challenge, s53 d-60) — mission beyond credits/hour
**Jump gate is the mission spine; idle capital is NOT the constraint — TOOLING/DEFECTS are.** Treasury ~3.27M
compounding autonomously; capital is not scarce. Ranked portfolio (cost-to-unblock × mission value):
- **#1 JUMP GATE — the progression spine.** Located X1-PZ28-I67 (s67). Unlocks the interstellar network. Bill:
  FAB_MATS 0/1600 + ADVANCED_CIRCUITRY 0/400 (QUANTUM_STABILIZERS 1/1 done), 0.0% built. Both materials now BUYABLE
  in-system (L58, ≈1.6–2M total) — blocked ONLY by the s86 construction-execution bug (report filed, d-91).
- **#2 EXPLORATION — unlocks #1's payoff.** Gated on jump capability (via the completed gate, or a warp/jump ship).
  Recon-first with a cheap probe, never a capital ship blind.
- **#3 MANUFACTURING / TRADING — EXECUTABLE, defect-gated.** The engine is built + allowlisted parallel income.
  Real gates now: (a) the d-86 `sequence_number` migration (below); (b) sell-side volume 6–20 SCARCE/WEAK caps
  standalone $/h; (c) validation before any capital buy (L16).
- **#4 FLEET — derived, demand-pulled.** Buy only against a validated, unblocked mission — NOT because treasury is
  big (L16). Gate-build + manufacturing each want a dedicated hauler (assignment-excluded, no flag needed).

### MANUFACTURING EXPERIMENT (d-65/d-88) — MEASURED s84: NET-POSITIVE, contracts unharmed → SCALE TRIGGER FIRED
- **Status:** VALIDATED NET-POSITIVE. s84 measured the window (L56 method: `ledger list --type SELL_CARGO` isolates
  trading revenue cleanly — contracts never emit SELL_CARGO). Result: the ONLY 5 SELL_CARGO rows in the 567-row
  ledger are all task eebee24b = **+229,740 gross vs ~68,856 buy = ~+160k net in ~4min**. CAVEAT: one task, and a
  factory's FIRST collection dumps accumulated inventory → this rate OVERSTATES steady state (L41/L56); the per-task
  rate will fall once initial stock clears, but the collection STREAM is continuous. Contracts did NOT sag:
  CONTRACT_FULFILLED +171,892 at 00:49Z + 24h KPI ROSE to +142,160/hr (from +131,858). Coordinator claims ONLY
  TORWIND-4 (released between tasks); TORWIND-3 stayed contract-locked — no over-claim.
- **Stop path (d-65 GUARD, corrected L53):** `operations stop --manufacturing` DOES NOT WORK (registry doesn't track
  it) — halt via `container stop parallel_manufacturing-X1-PZ28-f388df4b`. Observe via `container logs`.
- **SCALE TRIGGER — FIRED s84 (d-89):** both conditions met (net-positive + no contract sag) → bought 1 dedicated
  LIGHT_HAULER (`shipyard purchase --ship TORWIND-1 --type SHIP_LIGHT_HAULER --budget 300000`, container
  batch_purchase_ships-TORWIND-1-2e22cde5 in-flight, lands ~next session). PURPOSE: free a hauler to run gate
  CONSTRUCTION in parallel without stalling contracts/manufacturing.
- **RESOLVED s85 (d-90, L57):** `construction start` spawns NO coordinator — the MANUFACTURING coordinator executes
  construction via ONE unified task queue, adopting pipelines only at startup recovery (order: construction start
  FIRST → then bounce operations --manufacturing).
- **RESOLVED s86 (d-90 closed FAILED, L59): the starvation theory was ALSO wrong — it's a BUG.** Construction tasks
  can never run on the current binary: no activation path (all PENDING→READY paths are type-filtered to
  FABRICATION/COLLECTION), no rescuer case, and NO registered executor for DELIVER_TO_CONSTRUCTION (main.go:499-508).
  Queue priority actually FAVORS construction (75>50>10) — the task simply never becomes READY. Report filed:
  reports/bugs/2026-07-04-construction-pipeline-tasks-never-execute.md (kind:fix). Pipeline 6c02cbe3 reads
  "EXECUTING 0.0%" and will stay there until the fix merges + daemon restarts.

### Sequencing (dependency-ordered)
1. DONE (s67): waypoint-discovery verb shipped; jump gate located (X1-PZ28-I67); bill read. Re-read the bill each
   session (external agents may also supply it; QUANTUM_STABILIZERS already went 1/1).
2. DONE (s83): d-86 `sequence_number` migration merged (commit 5095c78) + daemon restarted → column self-healed;
   `operations start --manufacturing` now persists pipelines + claims a hauler with no 42703 (d-88).
3. DONE (s84): d-65 experiment MEASURED — manufacturing NET-positive (~+160k first task, L56), contracts unharmed
   (KPI rose to +142k/hr). SCALE TRIGGER fired: d-89 bought 1 dedicated LIGHT_HAULER (in-flight).
4. DONE (s85): d-89 hauler landed (TORWIND-5, refreshed, role HAULER) + closed. Gate build LAUNCHED (d-90): learned
   the execution model (L57 — construction start is inert until the mfg coordinator is bounced to adopt it), bounced
   the coordinator → construction pipeline 6c02cbe3 EXECUTING at `--max-workers 2`. Gate still 0.0% material (depth-1
   fabrication chain is multi-hour).
5. DONE (s86): gate verified STILL 0.0% after 5h → root-caused as a BUG (not starvation, L59): construction subsystem
   is runtime-unwired (no activation, no executor). Report filed (kind:fix), d-90 closed failed, d-91 opened. Gas ops
   evaluated for the Admiral: IMMATERIAL (0% of input cost; numbers in s86 log). Discovered depth-3 is now viable (L58).
6. DONE (s87): d-91 report merged in <3h (73f3f08) AND self-deployed (daemon restarted 12s post-merge; wiring
   confirmed live in code). But depth-3 start hit a SECOND defect: resume returns the s85 pipeline as an EMPTY SHELL
   (tasks reaped by restart recoveries; StartOrResume never re-plans, nothing terminalizes a 0-task pipeline, L60).
   d-91 closed failed; second report filed; d-92 opened.
7. IN PROGRESS (d-92, next sessions): (a) WATCH reports/bugs/2026-07-04-construction-start-resume-empty-pipeline.md
   → merged. (b) On merge (+ daemon restart if daemon-side): `construction start X1-PZ28-I67 --depth 3` → FIRST verify
   TASKS EXIST (coordinator log "Activated DELIVER_TO_CONSTRUCTION" / real task count), THEN material > 0 within one
   window; bill ≈1.6–2M, spend ceiling 2.2M, guardrail cap ~3.0M. NO coordinator bounce needed — pipeline 6c02cbe3 is
   already adopted and ActivateConstructionTasks polls PENDING from the DB every cycle (L57 bounce is for ADOPTION
   only). (c) Then: gate operational → cheap probe recon of nearest connected system.

## Current posture (REPLACE each session — do not append)
**s87 (2026-07-04): FIRST FIX MERGED+LIVE SAME-DAY — but the gate is STILL blocked: resume returned an EMPTY pipeline;
second fix report filed (d-92).**
Treasury **5,992,276**, 24h delta **+5,819,825 ≈ +242,492/hr (~11× KPI)** — earner compounding untouched (contracts
TORWIND-3/4 + mfg TORWIND-4/5, scout TORWIND-2). Health OK, 4 containers RUNNING. Guardrail ≤50% of ~6.0M = **~3.0M cap**.
Gate: FAB_MATS 0/1600 + ADVANCED_CIRCUITRY 0/400. The d-91 wiring fix (73f3f08) merged in <3h and the daemon restarted
12s later — executor + activation CONFIRMED LIVE in the running tree (main.go:510-512, supply_monitor.go:1377). But
`construction start --depth 3` RESUMED the s85 pipeline 6c02cbe3 as an **empty shell (Task Count: 0)**: restart
recoveries reaped its tasks, and StartOrResume (construction_pipeline_planner.go:53-80) returns any PLANNING/EXECUTING
pipeline immediately — depth ignored, no re-plan, and NO path (completion/recycler/recovery/CLI) can regenerate tasks
or terminalize a 0-task CONSTRUCTION pipeline (L60). **Report filed:
reports/bugs/2026-07-04-construction-start-resume-empty-pipeline.md (kind:fix)** — again the mission's single blocker;
capital, materials, haulers all ready.
**BINDING CONSTRAINT: fix-pipeline latency on the resume-empty-pipeline bug.** Nothing in-game can clear the poisoned
pipeline row (no construction stop, no psql).
**NEXT SESSION MUST (d-92):** (a) check the NEW report's status (merged? gate_failed → re-queue per L35). (b) If
merged (+ daemon restarted if daemon-side): `construction start X1-PZ28-I67 --depth 3` → FIRST verify tasks exist
("Activated DELIVER_TO_CONSTRUCTION" in coordinator log / task count > 0), THEN material > 0 within the window; spend
ceiling 2.2M; NO bounce needed (pipeline already adopted; ActivateConstructionTasks polls DB every cycle). If merged
but STILL task-less → awaiting_human escalation with both reports (d-92 falsify). (c) Confirm earner health (24h $/h >
~130k) and D45/F56 prices haven't collapsed (L58). (d) If report still new/in_progress: do NOT re-file; advance
#2-tier work instead (e.g. dock a probe at C42/H64 to read the uncached shipyards, L49).

## Operational constraints
- **Launch heavy workflows ONE AT A TIME** (L22/L25). Concurrent heavy launches transiently hung the daemon socket.
  Launch → confirm `health` ok + container RUNNING → then launch the next.
- **Sequence: scout → wait for market data → batch-contract** (L21/L27). Purchase-planning fails fast without
  cached market data.

## Degraded capabilities (current)
- **Gate construction: HARD-BLOCKED (s87, L60) — the execution wiring is now LIVE (73f3f08) but the site is held by a
  poisoned EMPTY pipeline (6c02cbe3, EXECUTING, 0 tasks) that `construction start` forever resumes as-is** (no re-plan,
  depth ignored, nothing terminalizes it, no stop verb; kind:fix report filed 2026-07-04
  construction-start-resume-empty-pipeline, watch its status). Manufacturing itself is HEALTHY (income stream on
  TORWIND-4/5). Standing gotchas: (a) L57 bounce is needed only to ADOPT a new pipeline — task activation now polls
  the DB every cycle; (b) observability: `operations status`/`stop` don't track the coordinator — use container verbs
  (L53); resume prints "Task Count: 0" unconditionally (misleading); no task-level visibility, no `construction stop`,
  no cost dry-run (s84/s86/s87 friction).
- **Contract visibility: NONE.** No `contract list`; contract state is observable only via container logs.
  Deadlines unobservable — track accepted contracts by hand.
- **Daemon restart: NOT a Captain verb.** On a hang, rely on self-recovery + `tools/wait-daemon.sh` /
  `tools/restart-daemon.sh` (guardrails: restart once per session, only after SOCKET-DEAD).
- (Resolved and archived to the log: phantom-cargo/position desync, ship-sell nil-panic, socket-hang,
  treasury-readout garbage, d-83 daemon restart-loop. Re-verify by exercising, not status alone — L39/L42.)

## Revision protocol
Revise this file at any heartbeat where actuals diverge from targets for 2+ consecutive sessions. Replace Current
posture every session; keep the file under ~150 lines. Note the revision + reason in captain-log.md.
