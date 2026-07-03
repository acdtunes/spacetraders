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
the report field alone.

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
iteration) can trigger an L30-class socket hang even as a SINGLE launch.
