# Release Train — deploy cadence doctrine (2026-07-11, Admiral-ordered)

**Problem:** every daemon restart cycles the fleet's containers (minutes of re-adoption
churn), so deploying every merge taxes revenue — but holding merges for "everything
ready" leaves fixed defects idle on main while production runs the old binary. The train
is the middle path: **scheduled boundaries + a hot exception class + free-deploy
surfaces.**

## Surfaces — only the daemon needs a train

| Surface | Policy | Why |
|---|---|---|
| **gobot daemon** | TRAIN (below) | restart churns containers — the only costly boundary |
| routing service | rides any daemon train, or solo kickstart when a change is routing-only | stateless planner, fail-open design |
| grafana / prometheus reload | **deploy on gate, no batching** | zero fleet impact |
| visualizer (web+server) | **deploy on gate** | zero fleet impact; explicit relaunch (no supervisor assumption) |
| watchkeeper | **deploy on gate** | zero fleet impact |

## The schedule (this era; times UTC, off-minute per fleet doctrine)

- **HOT (now-class):** fires the moment its payload gates — see qualification below.
  Next: **a5j7-p2** (furnace unblock) carrying the merged-and-waiting d6gl + 686e
  (+ prometheus reload for StrandedHull).
- **Scheduled trains:** **16:40Z · 19:40Z · 22:40Z · 01:40Z · 04:40Z** — a train ships
  whatever is gated when doors close (**15 min before departure**). Stragglers take the
  next train; nothing is ever held FOR a straggler. An empty train doesn't run.
- **FREEZE at 06:30Z (2026-07-12)** — 30 min before the T-6h era-end protocols. After
  the freeze: emergencies only, captain co-sign required. The era's final hours run on a
  proven binary; era-end behavior changes are config/ops, not deploys.
- **Era-3:** same pattern, cadence re-tuned to crew rhythm at boot (suggest 4h).

## HOT qualification (ships solo on gate, no waiting)

1. Any **P0**.
2. **P1 MONEY** with an active bleed *or* a blocked revenue program (e.g. furnaces
   parked awaiting the fix) — captain or Admiral names it hot.
3. **Guard-integrity regression** (a money guard weakened/bypassed in prod).

Everything else — features, observability, refactors — waits for the scheduled train.

## What never ships hot

Large features (C1-class). They ride a scheduled train **config-gated dark** (default
OFF), and the enablement flip is a separate, later, reversible config restart — never
bundled with the binary that introduces the code.

## Per-train checklist (harbormaster)

1. Doors close T-15: freeze the payload list (gated merges only; verify numstat + push).
2. Rebuild → kickstart (daemon + routing if included). One restart.
3. Per-payload live acceptance read within 10 min (the failing-case origin, not a proxy).
4. Captain flag: ONE mail, per-payload ACCEPT WHEN lines + any ops levers unblocked.
5. Close-out: P0 sweep + mail sweep-to-zero before reporting done.
6. **Revert note in the flag:** the previous binary's SHA (rebuild = rollback) + any
   config flips to reverse. Features ship dark, so binary rollback is always clean.

## Current manifest (at doctrine adoption, ~13:45Z)

| Train | Payload |
|---|---|
| HOT (on a5j7-p2 gate) | a5j7-p2 + d6gl + 686e + prometheus reload → captain restarts furnaces & stocker |
| 16:40Z | rh2z (if gated), 64je C1 dark (if gated), yuq9 (if landed) |
| 19:40Z+ | remainder of queue as gated: xdk6, 8cz9-recovery, liquidation twin |
| on gate (no train) | 6z7v financial dashboard (grafana-only) |
