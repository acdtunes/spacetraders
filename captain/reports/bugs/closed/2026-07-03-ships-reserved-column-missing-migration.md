---
title: Reservation feature shipped without a DB migration — ships.reserved missing, all contract assignment fails (SQLSTATE 42703)
status: obsolete
kind: fix
---

## Failure signature

```
[ERROR] Failed to save ship assignment TORWIND-3: ERROR: column "reserved" of relation "ships" does not exist (SQLSTATE 42703)
```

Emitted by the contract fleet coordinator on EVERY ship-assignment attempt, once
every ~10s, since 2026-07-03 21:27:39 (the moment the coordinator container
restarted onto the post-feature binary).

## Evidence

- Coordinator container `contract_fleet_coordinator-player-1-35df0a9f`
  (`container logs`): the error fires immediately after each
  `Selected TORWIND-3 (distance: 630.06 units)` → `Assigning TORWIND-3 to worker
  container` → `Failed to save ship assignment ... column "reserved" ... does not
  exist`. No worker container is ever spawned; the coordinator spins in a
  select→assign→fail retry loop.
- Ledger: last `CONTRACT_*` row is `CONTRACT_ACCEPTED +32,591 @2026-07-03
  18:27:20`. Zero contract activity in the ~3.3h since the regression began —
  contract income is fully stalled.
- First `reserved`-column error timestamp (21:27:39) coincides exactly with the
  coordinator container's creation time (21:27:37) = binary swap to the
  reservation-feature build.

## Root cause (traced in ../gobot)

Commit `985701a "feat(captain): Add a ship-reservation flag ..."` added the
columns as GORM struct tags on `ShipModel` but never added a production
migration:

- `internal/adapters/persistence/models.go:116` —
  `Reserved bool \`gorm:"column:reserved;default:false"\``
- `internal/adapters/persistence/models.go:117` —
  `ReservationReason string \`gorm:"column:reservation_reason"\``

These columns only materialize under GORM `AutoMigrate`, which is **test-only**:
- `internal/infrastructure/database/connection.go:86-100` `AutoMigrate` is called
  ONLY from `NewTestConnection` (`connection.go:78`), and its model list includes
  `&persistence.ShipModel{}` (`connection.go:92`). So SQLite test DBs get the
  column and tests pass green.
- Production `NewConnection` (`connection.go:16-63`) and the daemon bootstrap
  (`cmd/spacetraders-daemon/main.go:103`) run **no** migration/AutoMigrate.
  Production schema changes are hand-applied SQL under `migrations/` via `psql`
  (`migrations/README.md:23-36`). No migration adding `reserved` /
  `reservation_reason` exists.

The write that fails is the full-model upsert:
- `internal/adapters/api/ship_repository.go:731` `Save()` →
  `clause.OnConflict{UpdateAll: true}` (`ship_repository.go:738-742`) emits every
  `ShipModel` column, including the two missing ones → 42703.

Blast radius is BOTH assignment paths (both call `Save`):
- coordinator: `internal/application/contract/commands/run_fleet_coordinator.go:370`
- standalone `batch-contract`: `internal/adapters/grpc/container_runner.go:605`
  (and `releaseShipAssignments` at `:634`).

So there is no CLI-level workaround: `batch-contract --ship X` fails identically.
The only assignment method that avoids the columns is the unused `ClaimShip`
partial `Updates(...)` (`ship_repository.go:892-941`), which neither path calls.

## Expected vs actual

- Expected: coordinator selects an idle hauler, persists the assignment, spawns a
  worker, contract executes.
- Actual: assignment persist fails on the missing column; no worker spawns; the
  entire contract earner (the fleet's sole income stream) is down.

## Impact

TOTAL contract-income outage since 2026-07-03 21:27. Fleet is otherwise healthy
(daemon socket OK, ships safe, no strand). ~112k/hr earner producing 0/hr until
fixed. HIGHEST priority.

## Suggested fix

Add and apply a production migration adding the two columns, e.g.:

```sql
-- migrations/0XX_add_ship_reservation_columns.up.sql
ALTER TABLE ships ADD COLUMN reserved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ships ADD COLUMN reservation_reason TEXT NOT NULL DEFAULT '';
```

NOTE FOR OPERATOR: because the daemon does not auto-run migrations
(`main.go` / `connection.go`), the fix landing in code is NOT sufficient — the
`ALTER TABLE` must be applied to the LIVE Postgres DB out-of-band (`psql -f`),
after which the already-running coordinator self-heals on its next assignment
(no daemon restart strictly required, though a restart is harmless). Until the
column exists on the live DB, contract income stays at zero.


## OBSOLETE (2026-07-03) — superseded by a different resolution

Correct diagnosis, but the P0 was resolved two other ways before this fix
could land: (1) the ship-reservation feature that added the reserved column
was REVERTED (the flag was redundant — assignment already excludes haulers),
so the column no longer exists in the models; (2) the daemon now runs
additive AutoMigrate on startup, so any future model column is applied to
Postgres automatically. A migration adding reserved/reservation_reason would
now create orphaned schema. Branch discarded by the stale-base guard (main
advanced past it) — exactly its intended behavior.
