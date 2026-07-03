# Lessons (max 50 — curate ruthlessly)

Format: `L<N> [evidence: decision-ids] — heuristic`

L1 [seed] — Probes are cheap: keep 1 probe per 2-3 markets for price freshness
before committing haulers to a route.
<!-- Seeded from claude-captain/strategies.md. Earned lessons append below. -->
L2 [seed] — You cannot see market prices without a ship physically at the
waypoint; scouting is the only source of price intelligence.
L3 [seed] — Deploy ALL available scouts to cover ALL markets; never leave a
scout idle and never scout only a subset of markets.
L4 [seed] — Prefer solar-powered probes as scouts: zero fuel cost means infinite
runtime and they pay for themselves quickly.
L5 [seed] — Always calculate round-trip fuel (outbound + return + 10% safety
margin). A stranded ship earns zero until rescued.
L6 [seed] — Contracts pay twice (acceptance + delivery) and are guaranteed
income; prefer them over speculative mining/trading in bootstrap.
L7 [seed] — Accept a marginal or slightly-negative contract when capital allows:
it builds reputation and unlocks the next, potentially lucrative, contract.
L8 [seed] — Source contract goods from EXPORT markets (cheapest); avoid buying
at IMPORT markets (most expensive). Mine only when no market option exists.
L9 [seed] — Buy at exporters, sell at importers: this is the most reliable way
to earn credits via arbitrage.
L10 [seed] — Survey asteroid fields before mining: surveyed high-yield deposits
give ~30-50% better yields than blind extraction.
L11 [seed] — Minimum viable mining op is 1 surveyor + 2-3 drones + 1 shuttle;
add shuttles before more drones to avoid a transport bottleneck.
L12 [seed] — Over-mining collapses asteroids (yields drop 70%+). Monitor
per-asteroid yield trends and rotate fields when yields fall >30%.
L13 [seed] — Declining credits/hour despite steady yields signals market
saturation from constantly selling the same export; diversify or reduce volume.
L14 [seed] — Monitor both sides of a supply chain: rising EXPORT prices mean an
import shortage is constraining production and will break your arbitrage.
L15 [seed] — Trade routes are not static; competition equilibrates prices. Keep
multiple routes and exit any route once its margin drops below threshold.
L16 [seed] — Scale incrementally: validate profitability before buying more
ships. Premature scaling depletes credits and leaves the fleet idle.
L17 [seed] — Respect extraction cooldowns; do not attempt to extract again until
the cooldown timer has expired or the operation is wasted.
L18 [seed] — Fuel is an exchange good with volatile, agent-driven prices;
high-demand waypoints spike and can destroy a route's margin.
L19 [d-1,d-2] — The CLI has two backends: socket commands (health, ship,
container, workflow) talk to the daemon; market/ledger/player hit Postgres
directly. On SQLSTATE/DB errors from the latter while the former works, it's a
DB outage, not a total daemon failure — keep operating on socket data.
L20 [d-1,d-2,d-3] — Re-verify capability state every session; it flips between
sessions. Actuation (allowlist) and the market/ledger DB both went from
blocked/down to working between s1 and s2. Test permitted commands live —
don't trust stale "degraded" notes or a report's stale numbers (treasury read 0
while the real balance was 176,547).
L21 [d-3] — batch-contract (and any purchase-planning workflow) FAILS FAST
without cached market data: `cannot plan purchase of <good>: no
profitability/market data available (scout markets first)`. Sequence is
scout-all-markets → let it gather data → THEN batch-contract. Re-running before
data exists just churns and fails again.
L22 [d-3,d-4] — Launch heavy workflows ONE AT A TIME. Firing scout-fleet-
assignment (VRP) and batch-contract (negotiation) together transiently hung the
daemon socket (~2min, context deadline exceeded) and killed the scout
coordinator mid-spawn. Launch one, confirm `health` ok + container RUNNING, then
launch the next. The daemon self-recovers; there is no Captain-side restart.
L23 [d-6] — The pending-event feed emits MULTIPLE rows for a single failure
(e.g. 4x container.crashed + 1x workflow.failed, same container_id + timestamp,
from one retry burst). Before treating repeated crash events as a new incident,
group by container_id+ts — a wall of identical crashes is usually one workflow
retrying (often already-diagnosed), not N fresh failures. Don't re-fire the
failed workflow just because the feed looks alarming; fix the root cause once.
L24 [d-7] — A single solar scout (speed ~9) is too slow to cover a large
(26-market) system: ~5/26 cached after 3 sessions. When a faster ship is idle,
SPLIT the route — give each ship a disjoint `scout-markets --markets <subset>`
so they converge from opposite ends and ~halve time-to-coverage. Idle-hauler
fuel burn on intra-system hops (~68/unit, refuelable anywhere) is trivial vs a
six-figure treasury and the cost of a stalled contract.
L25 [d-3,d-7] — Refines L22: the daemon hang was from CONCURRENT launches (two
heavy workflows in the same instant), NOT from launching while another runs.
A 2nd workflow on a healthy 1-container daemon launched fine. Rule stays: launch
→ confirm health + RUNNING → launch next; never fire two at once.
L26 [d-7] — No `waypoint list` or `market find --good X` exists. To route a ship
to as-yet-unscanned markets, scrape waypoint symbols from a scout container's
metadata JSON (`container get <scout>` → metadata.markets); it's the only way to
get a system's full market route without physically visiting each waypoint.
L27 [d-7,d-8] — container-RUNNING != data-populated. batch-contract failed at
22:18 because the scout that feeds it didn't COMPLETE until 22:29. When
sequencing scout->contract in one session, gate the contract on the scout's
`workflow.finished` event (or a COMPLETED container / confirmed cache), NOT on
the scout merely being RUNNING. Refines L21/L22. Once data was truly present,
the SAME batch-contract went `profitability confirmed -> purchase initiated`.
L28 [d-8] — Treasury/credits telemetry is unreliable and reads LOW/garbage.
Report treasury, credits.threshold events, and the ledger "Balance" column all
misreport (REFUEL rows show the txn amount as balance, not a running total;
`player list` omits credits; `player info` is denied). Reconstruct real balance
by hand: take the last CONTRACT_* running balance as an anchor and sum
subsequent transaction AMOUNTS. Never act on a treasury alarm without this
check — extends L20 (saw 0 vs 176,547; now -216 vs ~175,251). UPDATE s6: the
Balance column and credits.threshold event now BOTH read the true balance
(175,251) — the bug appears FIXED. Trust it, but sanity-check against the ledger
anchor once more before fully relying on it.
L29 [d-8] — A `heartbeat_lost` event on a slow solar scout (speed 9, long
transit legs) is usually transient, not a zombie. Verify with two `ship info`
reads: if position advanced and container restart_count is 0 / status RUNNING,
it's just a transit leg exceeding the heartbeat window — leave it (its infinite
tour doubles as market-data refresh). Only stop it if position is frozen.
L30 [d-9] — A daemon socket hang (`context deadline exceeded` on health/ship/
container/workflow) can happen SPONTANEOUSLY, not just from concurrent launches
(L22/L25), and may NOT self-recover within a session (s6: >5min, 16 probes, no
recovery vs s2's ~2min). Diagnose scope fast with ONE `ledger list` (DB/Postgres
path, L19): if it answers, the socket subsystem is hung but the daemon isn't
dead. There is no Captain-side restart and loops/sleep/Monitor are all denied in
dontAsk mode, so hand-probing health is unbounded token burn — after ~3-4 probes
confirm the hang, record the incident (d-9), mark socket actuation degraded, and
end the session; the daemon recovers between sessions. Escalate to reports/bugs/
if the socket is still hung at the next session's start (3rd signature hit).
UPDATE s7: it WAS still hung at s7 start (3rd hit) — ESCALATED to
`reports/bugs/2026-07-02-daemon-socket-hang.md` (d-10). It is now a filed,
recurring bug, not a fluke; treat future hangs as known until the fix lands.
UPDATE s8: STILL hung (4th hit); the bug report is STILL status:new — a filed
`kind: fix` report does NOT self-resolve on the Captain's timescale. Once a hang
is filed, do NOT re-diagnose or re-probe beyond the ONE socket + ONE DB probe
needed to confirm scope; append the occurrence, defer, end. Burning probes on a
known-open blocker is pure token waste. CORRECTION s9 (operator addendum): the
s6/s7/s8 "hangs" were NOT a daemon code bug — a manual daemon restart raced the
old process's graceful-drain PID lock, so NO daemon ran ~22:55–23:16Z. Only s2
(concurrent-launch) is real socket-hang evidence. Takeaway: a total, multi-session
actuation blackout can be an out-of-band ops event (no daemon running), not the
filed bug — don't over-escalate a recurring blackout as a code defect.
L32 [d-12,d-13] — The daemon's `ship info` cargo can be a PHANTOM: it showed
TORWIND-1 holding 40/40 IRON_ORE while the game server's contract-deliver
endpoint authoritatively reported 0 units (API 4219). The local `PURCHASE_CARGO`
ledgered without the server actually adding cargo. RULE: on any cargo dispute,
the SERVER (delivery/sell API error) is ground truth, not `ship info`. A workflow
that fails on server-reported cargo=0 is DETERMINISTIC, not transient — do NOT
retry it (I reproduced 4219 verbatim on re-launch). A committed `PURCHASE_CARGO`
row also does NOT guarantee the goods exist server-side; it can desync local
credits too. Extends L31 (ledger/success flags are not proof of real state).
L33 [d-13] — `ship sell` can HARD-CRASH the CLI: nil-pointer SIGSEGV in
`APIMetricsCollector.RecordRateLimitWait` (api_metrics.go:134) on the
rate-limit-wait branch. It is unusable as a recovery path until fixed. General
rule: manual cargo-offload verbs are not guaranteed crash-safe; when a workflow
strands cargo, a manual sell may segfault rather than rescue it — verify the verb
works before relying on it mid-recovery.
L31 [d-8,d-10] — A `workflow.finished` event with `success:true` is NOT proof
the work completed. batch-contract e1871c14 emitted success:true yet the ledger
showed no PURCHASE_CARGO / CONTRACT_FULFILLED (bought and delivered nothing) —
the container was torn down by the socket hang mid-nav. ALWAYS cross-check a
workflow outcome against the expected ledger rows before believing it. Pairs
with L28 (ledger is the ground truth for financial state).
L34 [d-14] — A PHANTOM cargo (L32) cannot be cleared by ANY Captain verb: the
SpaceTraders API returns cargo ONLY on GET /my/ships (which the daemon serves
from a stale cache); navigate/orbit/dock/refuel return nav+fuel only, so none of
them overwrites the cargo cache. The phantom survived socket recovery, a workflow
relaunch that saw server cargo=0, and a full session boundary. Only a daemon
RESTART re-fetches true ship state. So on a confirmed phantom, do NOT run
navigate/orbit/dock "to force a refresh" — it just moves the ship pointlessly.
Defer the ship, keep the free assets running, and wait for the restart/fix.
L35 [d-14] — Bug-report frontmatter status DOES advance — the fix pipeline reads
`status:new` reports and writes back. Observed `2026-07-02-daemon-socket-hang.md`
go new -> `gate_failed` (fix attempted, gate blocked) while newer reports stayed
`new`. Read pipeline progress off the status: new = queued/unpicked, gate_failed =
attempted-but-blocked, merged/fixed = landed. This resolves the s8 "can't tell if
the pipeline moved" friction — check the report status each session instead of
guessing. Note: pipeline ordering is NOT priority-aware (a lower-value report was
worked while the critical phantom-cargo blocker sat `new`). UPDATE s11:
`gate_failed` is NOT terminal — the socket-hang report reverted `gate_failed` ->
`new` (re-queued), so a status is a snapshot, not a ratchet. Meanwhile the
phantom-cargo blocker stayed `new` for a 3rd straight session while the pipeline
re-touched the lower-value report — the not-priority-aware failure mode is real and
persistent, not a one-off. Don't infer "no progress ever" from a report sitting at
`new`; infer only "not landed yet." UPDATE s14: the phantom-cargo report finally
went `new` -> **`awaiting_human`** — a NEW status meaning the pipeline PROPOSED a
fix branch that is now gated behind the user's manual merge (propose-only mode,
`captain.auto_merge:false`). Full status ladder observed: new (queued/unpicked) ->
gate_failed (attempted, gate blocked, re-queuable) -> **awaiting_human (fix
proposed, pending user merge)** -> merged/fixed (landed). `awaiting_human` is the
Captain's cue that the blocker's fix EXISTS and the ball is in the user's court —
surface it to the user (which fix, what it unblocks) rather than continuing to treat
the blocker as unactioned.

L36 [d-16] — A multi-session HOLD needs a falsifiable off-ramp. Without an
observable answering "has any fix landed since I last looked," waiting is
faith-based and can run unbounded. Use the bug-report status (L35) as that
observable AND attach an explicit exit condition to every HOLD (e.g. "if the
blocker is still `status:new` at the next meta-review, promote fix-pipeline
priority-ordering to the top of the backlog"). A bounded HOLD beats an
open-ended one.

L37 [d-16,d-17] — The daemon ship-state cache desync (L32/L34) is NOT
cargo-specific — it also corrupts POSITION: `ship info` read the scout at H64
while the server said H65, crash-looping scout-tour with API 4204 "Ship is
currently located at the destination." It is a whole-cache-consistency defect,
not one phantom field. KEY DIFFERENCE: a POSITION desync IS Captain-recoverable
(cargo is not, L34) — manually `ship navigate` to a THIRD waypoint (neither the
stale-cached one nor the phantom "already-at" one); the API executes from the
ship's TRUE position and the daemon re-reads/reconciles on arrival, then
relaunch the tour. Navigating to the phantom destination re-triggers 4204;
navigating to the stale position is a no-op — pick a third waypoint.

L38 [meta-s15, backlog-P1] — VERIFIED the fix-pipeline gate fix (commit b4a465f
"fix pipeline works in the monorepo and untrusted worktrees") earned its keep.
Backlog P1 diagnosed the gate running `go build ./...` in the captain workspace
(empty Go module) so every daemon fix `gate_failed` forever — the daemon source
lives in the sibling `../gobot` repo. After b4a465f landed, the pipeline began
PROPOSING fixes: phantom-cargo AND ship-sell-nil-panic both advanced
new -> awaiting_human (gate passed, branch proposed, pending user merge), where
pre-fix BOTH picked-up reports were stuck gate_failed. The KPI it promised
("unblock the entire fix pipeline") moved: 0 -> 2 fixes reaching the human-merge
gate. Caveat: the report file the s11 backlog claimed it promoted
(2026-07-03-fix-pipeline-gate-empty-packages.md) was NEVER created — the fix
shipped as a commit anyway, so a missing promotion file != un-shipped fix; verify
shipped improvements against the git log, not the presence of a promotion report.
The bottleneck has now moved DOWNSTREAM: fixes land in awaiting_human and wait on
the user's manual merge (propose-only, captain.auto_merge:false).

L39 [d-19,d-20] — A merged daemon fix only takes effect after the daemon
RESTARTS, and a merged whole-cache desync (L32/L34/L37) clears itself on that
restart by re-fetching `GET /my/ships`. VERIFIED: the phantom-cargo fix went
awaiting_human -> merged (s16); the very next session TORWIND-1's `ship info`
flipped from a 6-session phantom 40/40 to true 0/40, and a clean batch-contract
then ran the purchase-then-deliver path with no 4219. Two operational takeaways:
(1) a total socket blackout IMMEDIATELY after fixes merge is the expected
restart-to-apply window (L30 operator-addendum class) — DEFER one session, don't
escalate; the daemon returns with true server state. (2) Confirm a fix actually
LANDED by observed behavior (ship info now matches server, workflow runs clean),
not by the report status alone — status `merged` says the code is in, the restart
+ a clean run is what proves it works. Full bug-report status ladder now seen
end-to-end: new -> gate_failed -> awaiting_human -> merged -> (restart) -> effect.

L40 [d-20,d-21] — batch-contract's route planner can UNDER-FUEL before a long leg
(left B7 with 242 fuel for a leg needing 280 -> API 4203 fuel-exhaustion crash), but
a single 4203 is SELF-HEALING, not a strand: the container auto-restarts
(restart_count 1), refuels the ship to full, and resumes to the delivery waypoint.
Do NOT intervene on the first 4203 — treat it like a transient crash (L23) and let
the container recover. Escalate to a route-planner bug only on a CRASH-LOOP (restart
count climbing with no progress + refuel bleed). Companion insight: a fuel "refuel
storm" (10 REFUELs / ~2,600cr in 13min on one contract trip) looks alarming but is
NOT a money pit if the contract payout dwarfs it — the IRON_ORE contract fulfilled
for +8,806 against ~2,950 total cost, net strongly positive. Judge a route by
NET P&L from the ledger, not by raw fuel-line alarm. Also: the fleet-report `Credits`
field can lag the ledger by a full CONTRACT_FULFILLED (read 170,085 while the ledger
Balance was 178,459) — anchor treasury to the last CONTRACT_* ledger row (L28), never
the report field alone. CONFIRMED s23 [d-28]: a fresh 4203 (242 vs 280 fuel required) on the
contract-work container auto-restarted (restart_count 1) and resumed with cargo 40/40 intact
(ship advanced B33->I68) — left it alone, no strand, no loop.
ADDENDUM s52 [d-59] — the self-heal generalizes BEYOND 4203 to a TRANSIENT API-ERROR BURST class. A ~30s
SpaceTraders `API error (status 404): 404 page not found` burst on `dock ship` / `get ship` (reload) killed TWO
consecutive contract workers (b9ce3620, 4d2aa5f2) — each exhausted its 3 fast retries INSIDE the burst window —
but the coordinator re-spawned a THIRD worker AFTER the window closed and it ran clean (dock/GET/refuel all
succeed seconds later; ship never stranded, cargo intact, same contract resumed). So a wall of container.crashed +
workflow.failed on the earner is often ONE upstream API hiccup, not a defect: the ship EXISTING + later calls
succeeding proves the 404 is transient (not a ship-identity/routing bug). Recovery = the coordinator's re-spawn;
do NOT stop/reassign the running successor (that sabotages the in-flight delivery). Escalate only if the signature
recurs 3+ SESSIONS (CLAUDE.md) or a worker crash-LOOPS with no clean re-spawn and the ship sits idle >60min with
cargo (a real strand).

L41 [d-22,d-23] — Contract payouts are LUMPY and occasionally ENORMOUS: one negotiated
contract netted ~+155k (CONTRACT_ACCEPTED +61,803 / CONTRACT_FULFILLED +167,097), ~24x a
typical IRON_ORE contract's +8,806, on a single ~10-min run. Consequences: (1) a
credits/hour figure computed over a window that one big contract dominates is a WEAK
throughput baseline — derive the real rate from several contracts of a steady run, not
one lucky draw. (2) Contracts are the highest-ROI bootstrap activity by a wide margin;
committing an idle hauler to a continuous `batch-contract --iterations -1` loop compounds
income with near-zero downside (guaranteed payouts, workflow pre-checks profitability,
stoppable via `container stop`). (3) A single contract's upfront cargo buy can be large
(~73k here) — size the 50%-treasury guardrail against the biggest plausible contract, not
the average one.

L42 [d-24] — A bug report marked `merged` is NOT proof the bug is gone in the RUNNING
tool: at s20 `ship sell` still segfaulted at the exact source line (api_metrics.go:134)
whose fix `cfad670` was already in `git log`. When a client-side CLI crash persists after
its fix merges, suspect a STALE BINARY (the deployed `bin/spacetraders` built before the
fix) before assuming a code regression — a merged commit only helps once the binary is
rebuilt/redeployed. Re-verify fixes by ACTUALLY EXERCISING the command (L39), and reopen
the report (merged -> new) on a confirmed recurrence so the pipeline/operator sees the
false-green. Tell client-side (in-process CLI panic) from daemon-side by the stack: if the
whole trace is main.main -> cli.Execute -> ... with no socket hop, it's the CLI binary.
UPDATE s28 [d-34] — NOW VERIFIED CRASH-SAFE. Exercised `ship sell` on an idle non-contract
cargo-bearing ship: it returned a graceful API 4219 instead of the SIGSEGV, so the deployed
binary IS rebuilt (the nil-panic is gone). Confirms the L42 method worked (a `merged` report
was false-green until the binary was exercised; today it's genuinely fixed). Caveat: crash-safety
!= a proven sale — the test hit phantom cargo (server=0), so 0 units actually sold; the full
sell path realizing credits is still unproven (needs a real cargo-bearing sale).

L43 [d-23,d-25] — `workflow batch-contract --iterations N` does NOT loop N contracts: the
container self-completes after ONE contract regardless of N (observed identically for N=5
and N=-1: "Iteration 1 completed -> Container completed successfully -> Released ship"),
despite the help text claiming "-1 for infinite." So batch-contract is a SINGLE-contract
primitive; relaunching it per heartbeat is the only way to chain contracts with it — not
truly autonomous-continuous. For continuous operation use `contract start` (the fleet
coordinator, "runs until stopped", dynamically discovers idle haulers) instead. CAVEAT to
verify: `contract start` selects "idle LIGHT HAULER ships" — a COMMAND-role ship may not
qualify; if the coordinator finds 0 eligible ships it idles/exits, and the fallback is
per-contract batch-contract relaunches. Also: launching `contract start` (a heavy discovery
iteration) can trigger an L30-class socket hang even as a SINGLE launch. RESOLVED (s21): the
CAVEAT is answered — a COMMAND-role ship DOES qualify; the coordinator negotiated+executed 3
contracts through TORWIND-1 (COMMAND). `contract start` is the proven continuous primitive.

L44 [d-25,d-26] — A daemon SOCKET hang costs OBSERVABILITY, not money: the coordinator's
contract work (negotiate/accept/purchase/deliver/fulfill) commits to the DB/server even while
the socket subsystem is wedged (`context deadline exceeded` on health/ship/container). Proof:
treasury climbed 503,700 -> 525,695 across a hang window in which I could issue ZERO socket
commands — three contracts landed in the ledger regardless. So a recurring L30/L43 hang from
`contract start` is NOT a reason to stop or abandon the coordinator; the fleet keeps earning
and the daemon self-recovers between sessions. Scope a hang with ONE socket + ONE ledger probe
(L30), confirm treasury via ledger (the earner is unaffected), record + defer. The hang only
becomes a MONEY problem if a session shows the socket hung AND no new CONTRACT_* ledger rows
since the last known contract — only then does it block progress rather than just visibility.

L45 [d-29] — The `contract start` coordinator runs ONE contract at a time ("Execute contracts in
sequence (one contract at a time)" in its help text) regardless of how many idle haulers it
discovers — extra ships only add position flexibility, NOT parallel contract throughput. So more
haulers do NOT scale contract income; the binding constraint on credits/hour is CONTRACT SUPPLY
(negotiation cadence, lumpy L41), not ship count or execution. Verified per-contract NET economics
from the ledger by pairing each ACCEPTED->cargo/fuel costs->FULFILLED cycle: 3 mega-contracts
netted +155,443/+169,942/+197,680 (avg ~174k) at ~67-73% margin — execution is robustly
net-positive. Also: compute a
contract's true NET by hand-pairing ledger rows (no `contract list`/P&L verb exists) — gross
payout alone overstates it by ~30%.
CORRECTED s29 [d-35/L48]: the "supply is the limiter, extra ships buy nothing" conclusion was WRONG.
One-at-a-time bounds PARALLELISM, not throughput. Decomposing real timestamps (L48) showed cycle time is
67% travel; a 2nd hauler cuts mean buy-leg distance via the coordinator's "select closest ship" balancer →
more cycles/hour → higher $/h even one-at-a-time. RULE flips: to grow credits/hour, cut CYCLE TIME (hauler
capacity/positioning, now the top experiment d-35) OR add a validated parallel route (L46) — NOT "wait for
more contract supply" (cadence is endogenous, 67% of it is compressible travel).

L46 [d-32] — The parallel trade-route lever (the only diversification beyond supply-gated
contracts, L45) is blocked at THREE layers, NONE of them capital — so ~700k idle treasury is
NOT the constraint, tooling is: (a) ACTUATOR — a manual arbitrage must offload via `ship sell`,
which is DEGRADED (L42 nil-panic; report `merged` but binary unverified), so no manual sale
completes; (b) INTELLIGENCE — the solar scout yields ONE price snapshot per market per tour
(`market history` returns a single record/good), so a route spread can't be validated as stable
vs transient/mirage (e.g. J70->A1 shows huge paper spreads — MEDICINE +5,779/u, CLOTHING
+6,422/u — but J70 source supply is LIMITED); (c) COORDINATION — `contract start` auto-claims
any idle light hauler, so a 2nd hauler bought for a route gets grabbed for contracts instead.
RULE: do NOT buy a 2nd hauler to "diversify" until `ship sell` is confirmed rebuilt AND a route
is live-validated by one round-trip. The route candidate is quantified and execution-ready;
the gate is the ship-sell actuator fix, not treasury.
UPDATE s28 [d-34] — layer (a) ACTUATOR is now LIFTED: `ship sell` verified crash-safe. The route
is now blocked at TWO layers, not three — (b) INTELLIGENCE (single-snapshot scout, unvalidated
spread) and (c) COORDINATION (coordinator auto-claims idle haulers, so no route ship can be
reserved). Progress is a gate-clear, not a green light: still need a stable-spread read AND a way
to hold a hauler out of the coordinator before a live J70->A1 round-trip.
UPDATE s53 [d-60] — layer (d) BUY ACTUATOR was thought MISSED: no `ship buy` verb (ship subcommands are only
dock/info/jump/list/navigate/orbit/refresh/refuel/sell). Concluded manual arbitrage was UNEXECUTABLE and the
trade horizon tooling-blocked at the actuator.
WITHDRAWN s58 [d-65] (Admiral correction) — that "unexecutable / ship-buy-blocked" framing was WRONG. Cargo
acquisition being workflow-INTERNAL is not a blocker; it is the SOLUTION: the `operations start --manufacturing`
engine (and `goods produce <GOOD>`) is an already-built, already-allowlisted PARALLEL income stream that discovers
high-demand goods, sources them via a supply-chain resolver (buy-vs-fabricate, `--strategy prefer-buy|
prefer-fabricate|smart`), and sells for profit. Verified s58: dry-run launches cleanly; X1-PZ28 has a real
prefer-fabricate thesis (abundant cheap ores at J70/B7 → SCARCE high-value finished goods at A1, QUANTUM_DRIVES
sell 141,736). So diversification is NOT actuator-blocked. The REAL constraints are: (1) FLEET CAPACITY —
manufacturing auto-claims idle haulers with NO ship-exclusion flag, so it races the contract coordinator (layer c)
for the sole productive hauler; a clean parallel run needs a DEDICATED hauler. (2) SELL-SIDE VOLUME — A1
finished-goods volumes are 6–20, SCARCE/WEAK (L13 thin/self-collapsing), capping standalone $/h. (3) VALIDATION —
L16: measure net manufacturing $/h before buying a dedicated hauler. RULE (revised): the binding constraint on the
trade/manufacturing horizon is FLEET CAPACITY + validation, NOT tooling and NOT capital (2.06M idle). The
waypoint/system-discovery gap still stands for the JUMP-GATE/EXPLORATION horizons, but the `ship buy` ask is
WITHDRAWN — the engine already exists. Experiment designed in d-65, deferred past the d-37 verdict to avoid
corrupting it.

L47 [d-34] — Phantom cargo (L32/L34) RECURS after every contract fulfillment: TORWIND-1 finished
the AMMONIA_ICE contract and its `ship info` showed 9/40 leftover AMMONIA_ICE while the server
authoritatively held 0 (API 4219 on sell). So the L32 whole-cache desync is not a one-off — it
regenerates each contract cycle. NEW: a dedicated reconcile verb `ship refresh` (force GET
/my/ships -> overwrite cargo/nav cache) now EXISTS in the CLI — exactly the in-band phantom fix
L34 said was impossible — BUT it is NOT allowlisted (PERMISSION DENIED in dontAsk mode). So the
phantom is still not Captain-clearable in-band: the fix EXISTS as a verb but is out of reach.
Allowlisting `ship refresh` would close the L34 gap entirely (Captain could reconcile phantom
cargo without a daemon restart). Until then: defer the phantom (low-impact; the coordinator's next
contract re-fetches ship state, L39), don't restart a healthy daemon just to clear it.
CONFIRMED AGAIN s30 [d-36]: idle-post-contract TORWIND-1 showed 21/40 CLOTHING while the server held 0
(graceful 4219). STRUCTURAL CONSEQUENCE: because post-contract leftover is ALWAYS phantom, a REAL end-to-end
sale can NEVER be validated on the idle command ship — it requires a deliberate buy-at-export -> sell-at-import
round-trip on a hauler reserved OUT of the coordinator. The sell actuator (crash-safe, d-34) is NOT the gate
on the parallel route (L46); the reservation problem (L46 layer c) is.
UPDATE s32 [d-39] — `ship refresh` IS NOW ALLOWLISTED and WORKS (the user granted my s30 ask). The L34 gap is
CLOSED: the Captain can reconcile a desynced ship cache in-band WITHOUT a daemon restart. Exercised it on
TORWIND-3 — it reconciled from GET /my/ships successfully. And it fixes MORE than cargo: it also corrects a
desynced ROLE field (see L50). So on any confirmed phantom (cargo, position, or role), the first move is now
`ship refresh --ship <sym>`, not "defer and wait for a restart."

L48 [d-35] — Contract cycle time is TRAVEL-DOMINATED, and cadence is ENDOGENOUS — decompose it before
declaring credits/hour "supply-gated." Pairing ledger CONTRACT_ACCEPTED/FULFILLED rows with the REFUEL/
PURCHASE_CARGO between them, and cross-reading the coordinator's ship-selection log, over a 6-contract span:
accept->fulfill EXECUTION (travel+buy+deliver) = 67% of wall-clock vs fulfill->next-accept negotiation gaps
= 33%. Bimodal: mega contracts fulfil in ~90s at coordinator-logged "distance 0.00" (ship already at the
provider market); small contracts drag 21-28 min at "distance 630-714 units" (86% of execution time for 1.5%
of revenue). The coordinator log "Using command ship as fallback (no hauler ships exist)" reveals the root
cause: with a 1-ship pool its "select closest ship" position-balancer is INERT. HEURISTIC: to find the
binding constraint on a sequential-contract earner, decompose real timestamps into travel vs negotiation —
if travel dominates, capacity/positioning (not contract supply) is binding, and a 2nd ship compresses cycle
time = more cycles/hour even one-at-a-time. Also: the coordinator surfaces per-contract provider DISTANCE and
cargo type in its logs (`container logs <coordinator>`) — the closest thing to a `contract list` verb for
reconstructing cycle economics. Corrects L45.
ADDENDUM s36 [d-43] — the distance-only "select closest" is NOT a uniform speed-blind leak: a slow hauler that
finishes a long haul INSIDE a market cluster then gets re-selected for successive near-zero-distance contracts
(observed PRECIOUS_STONES@714 -> ALUMINUM@0.00 -> AMMONIA_ICE@52.01), so the speed-blind penalty is BOUNDED to
ISOLATED far contracts, not every cycle. A faster ship sitting idle while the slower ship runs distance~0
contracts is DESIGNED-OPTIMAL (routing those to the farther ship would ADD travel), not a leak. So escalate a
"selection should weight ETA not distance" bug ONLY when a distance->400 contract is routed onto the slow ship
while the faster ship is genuinely closer/idle — do NOT mis-fire on benign closest-ship idling.
ADDENDUM s37 [d-44] — CRITICAL QUALIFIER: the speed-blind selection is currently INERT, because the faster ship
(TORWIND-1) is a COMMAND ship, and the coordinator selects among LIGHT HAULERS only ("Idle light haulers
discovered"), using the command ship ONLY as a fallback "when no hauler ships exist" (L43). Once a real hauler
exists, the command ship is EXCLUDED from the candidate pool — so a far contract routed to the sole slow hauler
(observed CLOTHING@714.27 to TORWIND-3 while TORWIND-1 idled) is UNAVOIDABLE fleet-composition cost, NOT the
coordinator ignoring a faster ELIGIBLE ship. The escalation trigger can therefore only fire with a 2+-LIGHT-HAULER
fleet; do NOT file a speed-blind bug while there is one eligible hauler (no faster candidate existed to choose).
The far-haul cost on a single slow hauler is a CAPACITY/SPEED question (the d-37 experiment), not a routing bug.

L49 [d-37] — To PRICE a shipyard you must physically send a ship there: market scout tours populate market
GOODS, not shipyard SHIP-LISTINGS, so "wait for the free scout to price A2" was a dead end — `shipyard list`
stayed empty ("No ships available") across sessions until TORWIND-1 deliberately visited+docked at A2, which
populated it instantly (SHIP_LIGHT_HAULER 308,497). Corollary lever: the contract coordinator detects worker
completion via a ~53min TIMEOUT (not a completion event), so after each fulfillment the command ship sits IDLE
until the timeout fires — that gap is both a real throughput leak (~1/3 of the fulfill->next-accept negotiation
time, L48) AND a SAFE window to borrow the command ship for a side-errand (pricing, a probe) without racing the
coordinator. Used exactly that window to price A2 and buy the first hauler. Also: a SHIP_LIGHT_HAULER here is
2x cargo (80 vs 40) but ~0.4x speed (15 vs 36) of the command ship — bigger holds cut multi-trip buy legs but
slower travel lengthens each leg, so the net cycle-time effect of a hauler is NOT obvious a priori; measure
$/h over 24h (d-37) rather than assuming.
REFINED s34 [d-40] — the "~53min timeout, not a completion event" claim is a CEILING, not the whole story: the
coordinator ALSO detects a FAST worker via a "Contract completed by <ship>" EVENT and re-selects in ~3 min (a
FOOD mega ran select@14:30:24 -> "Contract completed"@14:33:31). So completion is event-detected when a worker
exits cleanly; the 30-53min timeout is the FALLBACK for slow/stuck workers. A ship that completes fast does NOT
sit idle-until-timeout — the borrow-window (above) only opens on slow contracts.

L50 [d-39] — A freshly PURCHASED ship can land in the daemon cache with an EMPTY Role, making it INVISIBLE to
role-based coordinators. TORWIND-3 (bought as SHIP_LIGHT_HAULER) showed `Role: (empty)` in `ship info` while
the server held `Role: HAULER`; the contract coordinator's "discover idle light haulers" step reads that cache,
found no role, logged "no hauler ships exist," and kept falling back to the command ship — so the whole reason
for buying the hauler was silently defeated. This is the L32/L37 whole-cache-desync class on a NEW field
(Role). FIX (in-band, cheap): `ship refresh --ship <sym>` re-fetches GET /my/ships and populates the true role
(now allowlisted, L47). HEURISTIC: after ANY ship purchase, `ship refresh` the new ship before expecting a
role-based coordinator/workflow to pick it up — don't spend sessions waiting for a coordinator to "discover" a
ship whose cached role is blank. If refresh sets the role but the coordinator STILL misses it, the filter keys
on a different field (frame symbol) → then it's a real discovery bug worth a report.
CONFIRMED WORKING s34 [d-39/d-40] — the refresh fix took: the coordinator selected TORWIND-3 (haul) and it
fulfilled a +265,866 FOOD mega in ~3 min. BUT the activation had a SECOND gate: a role refresh is NOT seen by
an ALREADY-RUNNING coordinator (it caches its eligible-hauler list per-iteration in memory) — the still-running
process's 14:22 selection STILL logged "no hauler ships exist"; the refreshed Role was picked up only after the
coordinator's OWN container RESTARTED (14:30:19) and re-read the cache. HEURISTIC ADDENDUM: after `ship refresh`
on a new hauler, the role-based coordinator activates on its next CONTAINER RESTART, not merely its next
in-loop selection — if you need it sooner, restart the coordinator container (weigh L30 hang risk); otherwise
wait for the natural restart. The frame-symbol-filter branch is now DISPROVEN — the filter keys on cached Role.