# Improvement backlog

Maintained by the daily meta-review session. Format per proposal:

## P<n>: <title>
- Problem:
- Evidence: (decision ids, friction log refs)
- Sketch: (new CLI command / snapshot field / workflow change)
- Expected ROI:
- Score: (re-scored every meta-review)

<!-- Re-scored 2026-07-03 meta-review (s15). Score = impact × evidence-strength ×
     feasibility, 1-10. Higher = do sooner.

     SHIPPED & RETIRED this round:
     - P1 (fix-pipeline gate builds empty package set): FIXED by commit b4a465f
       "fix pipeline works in the monorepo and untrusted worktrees". Verified
       working (L38): phantom-cargo + ship-sell both advanced gate_failed ->
       awaiting_human, so daemon fixes now reach the human-merge gate. Removed
       from the active list. The bottleneck moved DOWNSTREAM to user merge. -->

## P2: `ship refresh` / force-resync verb to reconcile the daemon ship cache
- Problem: The daemon's ship-state cache desyncs from the server and NO Captain
  verb reconciles it — `ship info` reports phantom 40/40 IRON_ORE the server says
  is 0 (cargo desync), and it read a scout one waypoint behind the server
  (position desync, crash-looping scout-tour on API 4204). navigate/orbit/dock/
  refuel return nav+fuel only and never overwrite cargo (L34). Only a daemon
  RESTART re-fetches true state — which the Captain can't trigger. This single
  defect class has frozen TORWIND-1's contract for SIX sessions (d-14..d-18).
- Evidence: L32, L34, L37 (whole-cache-consistency, cargo AND position),
  d-12/d-13/d-14/d-16/d-17, s9/s10 friction ("no ship refresh / force-resync
  verb"), 6-session HOLD chain. Now the TOP durable lever: P1 (the gate) shipped,
  so the pipeline can propose daemon fixes — but a Captain-side resync verb
  recovers the WHOLE desync class in one command regardless of any single fix.
- Sketch: `spacetraders ship refresh --ship X` → forces GET /my/ships/X and
  overwrites the local cargo+nav cache from the server response. (The
  server-side GET is authoritative per L32; the daemon just needs to write it
  through instead of serving stale cache.)
- Expected ROI: Turns a multi-session revenue freeze into a one-command recovery
  whenever local state desyncs — the exact pain that produced 6 consecutive HOLD
  sessions. Also a general trust-restorer for `ship info`. Complementary to the
  awaiting_human phantom-cargo daemon fix (that fixes one write path; this
  recovers the whole class, and works even before/without a daemon restart).
- Score: 9  → PROMOTED to reports/bugs/2026-07-03-ship-refresh-force-resync.md

## P3: `market find --good <GOOD>` over cached data
- Problem: No way to ask "which markets buy/sell good X" across a system. To
  locate IRON_ORE the Captain scraped waypoint symbols out of a scout container's
  metadata JSON, then ran market-get on each — a 26-command sweep replaced by
  what should be one query.
- Evidence: L26, s3 friction ("cannot confirm IRON_ORE availability without a
  full scout lap"), s4 friction ("no waypoint list / market find --good X").
- Sketch: `spacetraders market find --good IRON_ORE --system X1-PZ28 [--side buy|sell]`
  → lists cached waypoints trading that good with price/supply/age.
- Expected ROI: Collapses every "where can I source/sell X" decision from a
  26-call sweep to one call; directly speeds every trade-route and contract
  sourcing decision (the core money loop).
- Score: 7

## P4: `contract list` / `contract get` — observe terms and deadlines
- Problem: Contract quantity, deliver-to waypoint, and DEADLINE are unobservable.
  `contract list` doesn't exist (only `contract start`); terms are visible only
  in batch-contract container logs, which get purged. A deadline miss would be
  silent.
- Evidence: s1 friction ("contract list does not exist"), s4 friction
  ("batch-contract logs purged before I could read contract terms"), s5 friction
  ("still blind to contract terms/deadline").
- Sketch: `spacetraders contract list` (active/available with deadline column) +
  `contract get <id>` (full terms). Reads from the daemon's contract state.
- Expected ROI: Removes a blind spot that risks silent deadline forfeits and
  makes contract profitability judgeable before committing a ship. Note: the
  stranded IRON_ORE contract has sat 6 sessions with its deadline unobservable —
  if it lapses during the HOLD the Captain would never see it coming.
- Score: 7

## P5: Captain visibility into proposed (`awaiting_human`) fix branches
- Problem: When the pipeline proposes a fix it flips a report to `awaiting_human`,
  but that is the ONLY in-band signal — the Captain cannot see which branch
  (`captain/fix-*`), what it changes, or how to nudge it. It can only tell the
  user "a fix is pending" without the actionable review context. With P1 shipped
  the pipeline now regularly produces awaiting_human fixes (2 already: phantom-
  cargo, ship-sell), so this blind spot is now on the ACTIVE critical path — the
  fleet's unfreeze is gated on the user merging a branch the Captain can't
  describe.
- Evidence: s14 friction ("a proposed fix (awaiting_human) is invisible to the
  Captain except via report frontmatter"), d-18 note-to-user, L35 (awaiting_human
  = surface to user). Two reports currently awaiting_human.
- Sketch: When the pipeline proposes a fix, write a one-line pointer into the
  report frontmatter/body: `fix_branch: captain/fix-<slug>` + a one-sentence
  summary of the change. OR a `captain fix status` verb listing proposed
  branches, their target report, and diff summary.
- Expected ROI: Lets the Captain hand the user an actionable merge request
  ("merge captain/fix-phantom-cargo, which rewrites the cache-write path, to
  unfreeze TORWIND-1") instead of a bare "pending". Shortens the downstream
  merge-gate latency that is now the binding constraint on every filed fix.
- Score: 6

## P6: Reliable current-credits readout
- Problem: Historically treasury/credits telemetry read garbage (0, or negative)
  and had to be hand-reconstructed by summing ledger AMOUNTS from the last
  CONTRACT_* anchor. `player list` omits credits; `player info` is not
  allowlisted. As of s6 the ledger Balance column and credits.threshold event
  appear FIXED (read the true balance), so this is partly resolved — but a single
  trusted "current credits" field is still not directly readable.
- Evidence: L20, L28, s5 friction ("treasury/credits effectively unreadable"),
  s6 note ("looks FIXED").
- Sketch: Add credits to `player list` output, OR allowlist `player info`, OR a
  `player balance` verb that returns the authoritative current credits.
- Expected ROI: Removes the most-repeated manual reconstruction; one number the
  Captain checks every session. Downgraded because the ledger anchor now works.
- Score: 5

## P7: Deterministic nav/cargo error should trigger state re-sync, not blind retry
- Problem: A deterministic server error (API 4204 position / 4219 cargo) is a
  DESYNC signal, but the daemon's auto-restart blindly re-spawns the same
  workflow into the same stale-cache condition, reproducing the crash verbatim
  (4 retries × 2 container instances observed) and amplifying one desync into a
  crash storm. A 42xx "already at destination / cargo=0" should force a ship-state
  re-fetch before any retry.
- Evidence: L37, d-16/d-17, s13 friction ("auto-restart amplifies a
  deterministic desync into a crash storm"), scout-position-cache-desync report.
- Sketch: In the container retry path, classify 4204/4219 as non-retryable-
  without-resync: on hit, force GET /my/ships/X (same primitive as P2) and
  reconcile before re-attempting; give up after 1 resync+retry instead of looping.
- Expected ROI: Stops one desync from burning N container instances and turns a
  crash-loop into a self-heal. Overlaps P2's primitive (build P2 first; this is
  the daemon-side consumer of it). Medium — mostly prevents wasted retries rather
  than unlocking revenue.
- Score: 5

## P8: Fix-pipeline priority signal + picked-up timestamp
- Problem: The Captain reads new/gate_failed/awaiting_human/merged off report
  frontmatter (L35) but cannot tell WHEN a report was picked up, nor influence
  ordering. The critical phantom-cargo blocker sat `new` for 5 sessions while a
  lower-value report was re-touched — the pipeline is not priority-aware.
- Evidence: L35, s8/s10 friction, 5-session phantom-cargo starvation (d-14..d-18).
- Sketch: A `priority:` frontmatter field the Captain sets and the pipeline
  honors, plus a status transition to `in_progress`/`picked` when work starts.
- Expected ROI: Directs limited fix throughput to the highest-leverage blocker.
  Downgraded from prior rounds: with P1 shipped the pipeline now clears reports
  (2 reached awaiting_human), so raw throughput is less starved than it looked —
  ordering is a refinement, not a bottleneck.
- Score: 4

## P9: `workflow.finished` success flag should reflect real work done
- Problem: `workflow.finished success:true` fired for a batch-contract that (per
  ledger) bought and delivered nothing — the container was torn down mid-nav.
  Every workflow outcome must be cross-checked against ledger rows, a manual step.
- Evidence: L31, s7 friction ("workflow.finished carries an unreliable success
  flag").
- Sketch: Emit success only when the terminal step actually committed its side
  effect (or add an `effect_confirmed` field), so the flag is trustworthy.
- Expected ROI: Removes a manual ledger cross-check from every workflow and
  prevents false "done" readings that mask stranded work. Medium; the ledger
  cross-check is cheap once habituated.
- Score: 4

## P10: Pending-event dedup / collapse by container_id+ts
- Problem: A single failure emits multiple rows (e.g. 4× container.crashed + 1×
  workflow.failed, same container_id + timestamp) from one retry burst, inflating
  the feed and risking masking a genuinely new failure.
- Evidence: L23, s3 friction, s10 (events 31-35 = one burst).
- Sketch: Collapse events sharing container_id within a small time window into a
  single row with a count, in whatever feed the Captain's prompt is built from.
- Expected ROI: Cleaner assessment step; lower risk of missing a real incident in
  a wall of duplicates. Well-mitigated already by the group-by heuristic (L23),
  so low marginal value.
- Score: 3

## P11: A permitted poll primitive / `health --wait <secs>`
- Problem: sleep/loops/Monitor are all denied in dontAsk mode, so riding out a
  transient socket hang means hand-issuing bare `health` probes one at a time,
  burning ~5s+tokens each with no bounded wait.
- Evidence: s2/s6 friction. NOTE: the operator addendum (L30 CORRECTION) found
  the s6/s7/s8 blackout was a manual-restart PID-lock race, not a real recurring
  daemon hang — so the need to "ride out hangs" is rarer than it looked.
- Sketch: `spacetraders health --wait 60` that retries internally until healthy
  or timeout, returning once.
- Expected ROI: Bounded, low-token wait during genuine transient hangs. Low
  priority since sustained blackouts were an ops artifact, not a code defect.
- Score: 2
