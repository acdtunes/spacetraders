---
title: Daemon ship position cache lags server by one waypoint, crash-looping scout-tour with API 4204 "already at destination"
status: merged
kind: fix
---

## Failure signature

`SCOUT` container / `command execution failed: failed to navigate to X1-PZ28-H65:
... API error (status 400): {"error":{"code":4204,"message":"Navigate request
failed. Ship TORWIND-2 is currently located at the destination."}}`

The daemon's cached ship position lags the game server by one waypoint. The
route planner reads the stale position (X1-PZ28-**H64**), plans a hop to the
next market (X1-PZ28-**H65**), and issues the navigate — but the server already
has the ship at H65, so it rejects with 4204. The scout-tour retries 4× (same
error each time), then fails unrecoverably. The scout-fleet-assignment
auto-restart re-spawns a fresh scout-tour, which reproduces the identical crash.

## Evidence

- Ship: TORWIND-2 (SATELLITE, solar, fuel 0/0, speed 9), system X1-PZ28.
- `ship info` / `ship list` reported the scout at **H64**; the server (navigate
  API) authoritatively reported it at **H65**.
- Occurrences today (all identical 4204 to H65):
  - 2026-07-03T03:32:09Z — `container.heartbeat_lost` (container `...-65007a67`).
  - 2026-07-03T09:38:48Z — 4× `container.crashed` + `workflow.failed`
    (container `...-65007a67`, requestIds 019f2758-54ea…/5687…/5792…/589a…).
  - 2026-07-03T10:47:07Z — 4× `container.crashed` + `workflow.failed`
    (container `...-8975e162`, requestIds 019f2796-e180…/e28c…/e392…/e499…),
    immediately after `scout-fleet-assignment-...-a17d1c85` COMPLETED and
    re-spawned the tour.
- Relevant tour log lines: `Zero-fuel ship using BURN mode` → `Route segment
  execution failed` → 4204 → `Retrying after error (attempt 1..3)` → `Container
  failed with unrecoverable error`.

## Expected vs actual

- Expected: on a 4204 "already at destination", the daemon should treat the
  navigate as a no-op success (the ship IS at the target), refresh ship state
  from `GET /my/ships`, and continue the tour to the *next* market.
- Actual: it treats 4204 as a hard error, retries the same stale hop 4×, then
  kills the container. The stale position is never reconciled, so every
  auto-restart reproduces the crash.

## Impact

Crash-loops the only free, always-on intel asset (the solar scout), leaving the
whole fleet idle. Market-data refresh stops until a human/Captain intervenes.

## Workaround (Captain-side, confirmed 2026-07-03)

Unlike the cargo desync below, a **position** desync IS recoverable without a
daemon restart: manually `ship navigate` the scout to a THIRD waypoint (neither
the stale-cached one nor the phantom "already-at" one). The navigate succeeds
(server executes from its true position), and on arrival the daemon re-reads and
reconciles the position cache. Verified: navigating TORWIND-2 H64→**H66**
succeeded with no 4204, ship info then read H66, and a relaunched scout-tour
progressed normally. (Navigating to the phantom destination H65 would re-trigger
4204; navigating to the stale H64 is a no-op — pick a third waypoint.)

## Suspected root cause / relation to other reports

Same root CLASS as `2026-07-02-phantom-cargo-contract-delivery.md`: the daemon's
per-ship state cache drops or fails to apply server-side updates, so `ship info`
diverges from the game server. There it was **cargo** (daemon 40/40, server 0);
here it is **position** (daemon H64, server H65). Both point at the daemon not
re-fetching authoritative ship state (`GET /my/ships`) after a state-changing
op. A single fix — reconcile ship state from the server on nav/cargo mismatch
(or treat 4204/4219 as "server is right, re-sync") — would likely resolve both.
Position, at least, is recoverable in-band; cargo is not (no Captain verb
rewrites the cargo cache — only a restart).
