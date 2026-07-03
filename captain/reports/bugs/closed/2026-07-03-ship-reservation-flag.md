---
title: Add a ship-reservation flag so a hauler can be held out of the contract coordinator's auto-claim
status: rejected
kind: feature
---

## Requirement (from the captain's Horizon plan, strategy.md; Admiral standing order s71/d-79)

Both mission threads need a DEDICATED hauler that runs a non-contract stream in
parallel with the contract earner:

1. **Jump-gate fabrication** (construction of X1-PZ28-I67: FAB_MATS 0/1600 +
   ADVANCED_CIRCUITRY 0/400 — both fabrication-only, `construction start` depth 0–2).
2. **Manufacturing** (`operations start --manufacturing`, strategy.md d-65 experiment).

Today this is impossible. The contract coordinator (`contract start`), the
manufacturing coordinator, and the construction pipeline ALL discover "idle light
hauler" ships dynamically and auto-claim them. Whichever coordinator runs grabs the
sole productive hauler, so a ship bought/held for the gate or manufacturing stream
gets pulled into contracts instead (L46 layer c). There is no way to say "this ship
is reserved; do not auto-claim it."

## Sketch

Add a per-ship **reserved** flag that the hauler-discovery step of every coordinator
respects, plus a CLI verb to set/clear it (follow the hexagonal pattern used by
`feat(ship): add ship refresh` and the merged `waypoint` verbs — application command
handler + tests, gRPC RPC, daemon service impl, CLI command):

1. `spacetraders ship reserve --ship TORWIND-4 [--reason "gate hauler"]` — mark the
   ship reserved (persist on the ship record / daemon store).
2. `spacetraders ship unreserve --ship TORWIND-4` — clear it.
3. (Optional) surface a `RESERVED` column in `ship list`.
4. Make the "discover idle light haulers" filter in the contract coordinator, the
   manufacturing coordinator, and the construction pipeline EXCLUDE reserved ships.

Acceptance: with TORWIND-4 reserved, `contract start` (running) never selects it —
its logs show it discovering only the unreserved haulers — while
`operations start --manufacturing` (or `construction start`) CAN use it. Clearing
the reservation returns it to the contract pool.

## Why now

The captain has 3 light-hauler-class ships imminent (contract hauler #2 bought s71,
d-78) and >2.3M idle treasury. Fleet capacity is the single binding constraint on
both mission horizons; this flag is the pivotal enabler that lets a dedicated hauler
serve the gate/manufacturing streams WITHOUT starving the proven contract earner.

## Constraints

- TDD; follow existing CLI/query/gRPC/coordinator patterns; no new dependencies.
- Regenerate protos if the .proto changes (worktree has `make proto`).
- The default (unreserved) behavior must be unchanged so the current contract
  coordinator keeps auto-claiming as today.


## REJECTED (2026-07-03, Admiral + engineering)

Redundant. Ship ASSIGNMENTS already provide the mutual exclusion this flag
duplicated: the contract coordinator's FindIdleLightHaulers Filter 5 admits
only ship.IsIdle() ships (no active assignment), and the manufacturing
coordinator holds ship.AssignToContainer for its whole run. A manufacturing
hauler is therefore already invisible to contracts while working. This report
was filed from a settings-only --dry-run WITHOUT reading the assignment code.
Feature reverted. Root-cause process fix: reports must now cite the existing
code checked (see CLAUDE.md 'Verification gate').
