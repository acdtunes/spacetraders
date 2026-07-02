# Standing strategy

## KPI targets
- Credits/hour: establish a baseline over the first 24h of operation, then set
  a target 20% above baseline. Record the baseline here when known.
- Fleet utilization: no ship idle > 60 minutes without a recorded reason.

## Current posture
- Bootstrap mode: observe, learn the fleet, prefer contracts and proven trade
  routes over speculative arbitrage until the credits/hour baseline exists.

## Degraded capabilities (as of 2026-07-02)
- **Actuation: BLOCKED.** Advisory-mode permissions (dontAsk + read-only
  allowlist) deny every mutating CLI command. The Captain can observe and plan
  but cannot move the fleet. External blocker (settings.json), not strategic.
  Plan-of-record queued in decisions.jsonl (d-1 scout, d-2 contract).
- **Market intelligence: DOWN.** market/ledger/player commands hit Postgres
  directly and fail (SQLSTATE 28P01, DB unreachable). No price data, no P&L
  until the DB is restored. Rely on socket-based commands (ship/container/
  workflow) meanwhile.
- **Contract visibility: NONE.** No working command lists contracts
  (`contract list` doesn't exist; only `contract start`).

## Revision protocol
Revise this file at any heartbeat where actuals diverge from targets for 2+
consecutive sessions. Note the revision + reason in captain-log.md.
