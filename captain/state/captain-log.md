# Captain's log

<!-- Newest entries at the bottom. Supervisor may trim the oldest entries. -->

## 2026-07-02 — First session: assessed, planned, blocked from actuation

**Fleet:** TORWIND-1 (COMMAND, 40 cargo, 400/400 fuel, @X1-PZ28-A1) and TORWIND-2
(SATELLITE, 0/0 fuel = solar, @X1-PZ28-H64). Both idle/DOCKED. System X1-PZ28.
Treasury: 0 credits. No active containers.

**Decisions (d-1, d-2):** Bootstrap plan — TORWIND-2 scouts all X1-PZ28 markets
(free solar intel; L2/L3/L4); TORWIND-1 runs batch-contract for acceptance
capital (L6). Both recorded as plan-of-record; NEITHER executed (see below).

**friction: advisory-mode permissions block ALL actuation.** `.claude/settings.json`
runs `dontAsk` with an allowlist of read-only commands only. Every mutating
command (scout-all-markets, batch-contract, navigate, refuel, dock, purchase,
contract start, operations start) is denied. The persona says "you act" but the
permission layer forbids it — the Captain cannot move the fleet from this
session. Impact: 2 idle ships, 0 credits, no progress possible until the
allowlist is widened to include the specific mutating verbs the playbook needs.

**friction: Postgres-backed read commands are DOWN.** `market list/get/history`,
`ledger`, and `player list` fail with `SQLSTATE 28P01` (password auth failed for
user "spacetraders"; DB unreachable at :5432). These commands query Postgres
directly; socket-based commands (health, ship info, container, workflow) work.
Signature: `command_type=market|ledger|player + error=DB_CONN(28P01)`. Impact:
NO price intelligence and NO financial reporting even in read-only advisory mode.
First occurrence — logged, not yet escalated (contract threshold is 3x). Marked
degraded in strategy.md.

**friction: `contract list` does not exist.** The allowlist entry
`Bash(bin/spacetraders contract list:*)` references a non-existent subcommand;
the only `contract` verb is `start`. No CLI way to enumerate active/available
contracts — I'm blind to contract state.

