# Lessons archive (pruned from lessons.md at the 50-cap)

Grep-able history; not loaded into sessions. Each line notes a one-word reason.

L10 [seed] — Survey asteroid fields before mining: surveyed high-yield deposits give ~30-50% better yields than blind extraction. [pruned s71: dormant — fleet is contract/manufacturing, mining never exercised in 71 sessions]

L11 [seed] — Minimum viable mining op is 1 surveyor + 2-3 drones + 1 shuttle; add shuttles before more drones to avoid a transport bottleneck. [pruned s73: dormant — mining never exercised in 73 sessions, made room for L52]

L7 [seed] — Accept a marginal or slightly-negative contract when capital allows: it builds reputation and unlocks the next, potentially lucrative, contract. [pruned s75: bootstrap-era — with a mature +123k/hr contract earner and 3.13M treasury the captain never deliberately takes a negative contract; made room for L53]

L38 [meta-s15, backlog-P1] — VERIFIED the fix-pipeline gate fix (b4a465f) earned its keep: after it landed, phantom-cargo + ship-sell-nil-panic both advanced new -> awaiting_human (0 -> 2 fixes reaching the merge gate). Nugget retained: verify shipped improvements against the git log, not a promotion report (a missing promotion file != un-shipped fix). [pruned s77: historical one-time verification, fully played out — the fix-pipeline now auto-merges (c37568b); the "exercise/verify against git log, not status" nugget is redundant with L39/L42; made room for L54]

L26 [d-7] — No `waypoint list` or `market find --good X` exists; scrape waypoint symbols from a scout container's metadata JSON (`container get <scout>` → metadata.markets) to route to unscanned markets. [pruned s78: obsolete — `waypoint list/get` shipped s67 (d-74), so the metadata-scrape workaround is superseded by a real verb; compression pass]

L33 [d-13] — `ship sell` hard-crashes the CLI: nil-pointer SIGSEGV in `APIMetricsCollector.RecordRateLimitWait` (api_metrics.go:134); unusable as a recovery path until fixed. [pruned s78: fixed — verified crash-safe s28/d-34 (graceful API 4219, no SIGSEGV); the "verify a verb works before relying on it mid-recovery" nugget lives in L42; compression pass]

L34 [d-14] — A PHANTOM cargo cannot be cleared by any Captain verb (navigate/orbit/dock/refuel don't overwrite the cargo cache); only a daemon RESTART re-fetches true state. [pruned s78: superseded — `ship refresh` is now allowlisted (L47, s32) and reconciles cargo/position/role in-band without a restart; the phantom-recurrence heuristic lives in L32/L47; compression pass]

L17 [seed] — Respect extraction cooldowns or the operation is wasted. [pruned s86: no-miners — hauler-only fleet, no extraction verb ever exercised; cap made room for L58]
