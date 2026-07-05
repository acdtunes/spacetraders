---
title: Contract negotiation deadlocks permanently on cache/server nav-state desync — dock-retry is a no-op and the coordinator error-loops silently (18h fleet-wide income stall)
status: merged
kind: fix
---

## Failure signature

- Component: `CONTRACT_FLEET_COORDINATOR` (container `contract_fleet_coordinator-player-1-35df0a9f`)
- Error class: `Failed to negotiate contract: failed to negotiate: API returned nil result or contract`, repeating every ~60s indefinitely.
- Observed: 2026-07-04T08:03:17Z (last successful fulfillment/ledger row) → 2026-07-05T02:08Z (~18 hours). Treasury frozen at 6,953,128 the whole window; all 5 ships idle; ZERO events emitted to the operator (no workflow.failed — the loop is invisible outside container logs).

## Evidence

- Coordinator log, verbatim, repeating every minute for 18h:
  ```
  [2026-07-05 01:57:33] [INFO] Idle light haulers discovered
  [2026-07-05 01:57:33] [INFO] Negotiating new contract...
  [2026-07-05 01:57:33] [ERROR] Failed to negotiate contract: failed to negotiate: API returned nil result or contract
  ```
- Fleet-wide cache↔server desync confirmed by `ship refresh` at ~02:05Z (before → after):
  - TORWIND-3: cache DOCKED @J70, 25/80 cargo → server IN_ORBIT @I67, 0/80
  - TORWIND-4: cache DOCKED @E54, 66/80 cargo → server IN_ORBIT @E47, 0/80
  - TORWIND-5: cache DOCKED @D45, 62/80 → server DOCKED @D45, 0/80 (cargo phantom only)
- Proof of root cause by remedy: after the three `ship refresh` calls, the very next coordinator cycle (02:08:45Z) negotiated an ALUMINUM contract and spawned `contract-work-TORWIND-5-5fbef448`. No other change was made.

## Code checked

- `internal/adapters/api/client.go:599-650` (`SpaceTradersClient.NegotiateContract`) — issues `POST /my/ships/{ship}/negotiate/contract` via `requestWithErrorParsing` (client.go:1793-1847). Game error codes 4214/4244 ("ship not docked / in transit" class) are deliberately swallowed into `(&ContractNegotiationResult{ErrorCode: code}, nil)` — i.e. `Contract == nil`, `err == nil` (client.go:626-630). Code 4511 (existing contract) is handled separately and does not produce this signature.
- `internal/application/contract/commands/negotiate_contract.go:61-72, 88, 111-124` — on 4214/4244 the handler calls `ensureShipDocked`, which does `stateChanged, _ := ship.EnsureDocked()` on the **local cached entity** and only issues a real Dock API call `if stateChanged`. When the daemon cache already says DOCKED (but the server disagrees), `stateChanged == false` → **no API call is made** → the single retry is byte-identical to the first attempt → falls through to line 88: `"API returned nil result or contract"`. There is no cache reconciliation (no forced GET /my/ships) anywhere on this path.
- `internal/application/contract/commands/run_fleet_coordinator.go:135, 189-197` — the coordinator negotiates with `availableShips[0]` from `FindIdleLightHaulers` (ship_pool_manager.go:82-144; command ship excluded when haulers exist), logs the error at :192, sleeps ~30-60s, and repeats forever. No failure counter, no backoff escalation, no event emission, no state refresh after N consecutive failures.
- Conclusion: existing code demonstrably cannot recover — the only state-reconciliation verb (`ship refresh`, `RefreshShipHandler`) is CLI-only and nothing daemon-side invokes it on this failure path.

## Expected vs actual

- Expected: a negotiate rejection caused by stale local nav state should self-heal (refresh ship from server, dock for real, retry), or at minimum surface as an event after N consecutive failures.
- Actual: the coordinator error-loops silently forever; the entire contract income stream (~120-240k credits/hr) halts until a human/Captain runs `ship refresh` by hand.

## Impact

- ~18h × ~122-242k/hr ≈ **2.2-4.4M credits foregone** in one incident.
- Structurally guaranteed to recur: cache/server nav desyncs are a recurring class (they have recurred after contracts, purchases, and daemon restarts), and any desync that leaves all idle haulers "locally DOCKED, server not-docked" re-arms this exact deadlock.
- The stall is also invisible: no workflow.failed / no event → operator tooling shows a healthy daemon with a RUNNING coordinator.

## Suspected root cause & fix direction

Two composing defects:
1. `ensureShipDocked` trusts the local cache: on 4214/4244 it must NOT no-op — force-refresh the ship from the server (the same reconcile `ship refresh` performs) and then dock, regardless of local `stateChanged`.
2. No terminal/backoff behavior: after N consecutive negotiate failures the coordinator should refresh all pool ships' state from the server and/or emit a failure event instead of looping silently at ERROR level.

Either fix alone breaks the permanent deadlock; (1) is the targeted one.
