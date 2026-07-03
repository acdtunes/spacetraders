---
title: Phantom cargo — daemon ship cache holds 40 IRON_ORE the game server says the ship never received, blocking contract delivery
status: merged
kind: fix
---

## Failure signature

Contract delivery fails deterministically with a server-side 400 while the
daemon's own ship state shows the cargo present:

```
failed to deliver contract: API error (status 400):
{"error":{"code":4219,"message":"Failed to update ship cargo. Ship TORWIND-1
cargo does not contain 10 unit(s) of IRON_ORE. Ship has 0 unit(s) of IRON_ORE.",
"data":{"shipSymbol":"TORWIND-1","tradeSymbol":"IRON_ORE","cargoUnits":0,
"unitsToRemove":10}}}
```

Meanwhile `spacetraders ship info --ship TORWIND-1` reports:

```
Cargo: 40 / 40 units
Cargo Contents:
  - : 40 units (IRON_ORE)
```

The game server's deliver endpoint is authoritative — it says the ship holds
**0** IRON_ORE. The daemon's cached ship state (40/40) is fiction.

## Timeline / how it arose

The daemon was manually restarted at ~23:16Z (per the operator addendum on
`2026-07-02-daemon-socket-hang.md`; the prior process had exited ~22:55Z). On the
fresh daemon:

- **23:16:14Z** — `batch_contract_workflow-TORWIND-1-e1871c14` created.
- **23:16:55Z** — `PURCHASE_CARGO -2,080` posts to the ledger (40 units IRON_ORE
  @ ~52, bought at the B7 exporter). Treasury 174,963 → 172,883.
- **23:21:33Z** — workflow navigates to delivery waypoint X1-PZ28-H63, reports
  "Ship already at destination", then delivery fails: server says 0 units. 4×
  crash events, container FAILED.
- **23:32:46Z (this session)** — re-launched batch-contract
  (`...-b47f99e2`); reproduced the exact 4219 "Ship has 0 unit(s)" failure,
  deterministically, 4 retries, container FAILED.

So the local `PURCHASE_CARGO` committed (ledger debit + cargo cache = 40) but the
server-side purchase never actually added the cargo to the ship — a
purchase/cargo write that succeeds locally without the server-side effect, or a
ship-cargo cache that is never reconciled against the server after the purchase.

## Reproduction

With TORWIND-1 DOCKED at X1-PZ28-H63 holding (per daemon) 40/40 IRON_ORE and an
accepted-but-unfulfilled IRON_ORE contract:

```
bin/spacetraders workflow batch-contract --ship TORWIND-1 --iterations 1 --player-id 1
# -> "Ship has 0 unit(s) of IRON_ORE" every time; container FAILED.
```

## Expected vs actual

- **Expected:** after a successful `PURCHASE_CARGO`, the ship's cargo on the
  server matches the daemon cache, so delivery removes the units and the contract
  fulfills (paying its 2nd tranche). If a purchase does not land server-side, the
  ledger debit should be rolled back and cargo cache should read 0 — never a
  local 40 vs server 0 split.
- **Actual:** daemon cache = 40 IRON_ORE, server = 0. The contract can never be
  delivered, and the 2,080-credit debit was recorded for cargo the agent does not
  actually own — the local financial state is desynced from the server too.

## Impact

- The accepted IRON_ORE contract (+1,547 acceptance at 19:16Z) is permanently
  un-fulfillable in the current daemon state; its delivery payment is stranded.
- Local ledger overstates spend / understates credits by ~2,080 vs the true
  server balance (phantom debit).
- Manual recovery is also blocked: `ship sell` crashes with a nil-pointer panic
  (see `2026-07-02-ship-sell-nil-panic.md`), so the Captain cannot even offload /
  re-verify the phantom cargo. Both offload paths for TORWIND-1 are dead.

## Suspected root cause

Ship-cargo state is written optimistically on purchase and not reconciled against
the authoritative server ship record before the delivery call — or the
purchase's server round-trip failed silently while the local ledger + cargo cache
were still updated. Candidate fixes: (a) re-fetch ship cargo from the server
immediately before contract delivery and trust the server count; (b) make
purchase atomic — only commit the ledger debit + cargo increment after the server
confirms the cargo on the ship; (c) on restart, reconcile all ship cargo caches
against the server so stale/phantom cargo self-heals.

## Captain workaround

Marked contract-fulfillment and TORWIND-1 cargo state DEGRADED in strategy.md. No
further batch-contract re-launches on TORWIND-1 until the daemon reconciles ship
state with the server (likely a clean re-fetch on next restart). Scout TORWIND-2
continues its (healthy, solar, free) market tour.
