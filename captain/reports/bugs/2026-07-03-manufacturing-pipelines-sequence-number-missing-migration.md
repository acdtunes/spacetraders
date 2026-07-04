---
title: manufacturing pipelines cannot persist — column "sequence_number" does not exist (ManufacturingPipelineModel excluded from startup AutoMigrate)
status: merged
kind: fix
---

## Failure signature

With the d-83 daemon-restart-loop fix now deployed, `operations start --system X1-PZ28
--manufacturing` no longer crashes the daemon — but the manufacturing coordinator is INERT:
every pipeline persist fails with a missing-column error, so it finds opportunities, persists
nothing, claims no ship, and produces zero output.

```
[INFO]  Found 2 fabrication opportunities
[ERROR] Failed to persist pipeline: failed to get max sequence number: ERROR: column "sequence_number" does not exist (SQLSTATE 42703)
[INFO]  Found 10 factory collection opportunities
[ERROR] Failed to persist collection pipeline: failed to get max sequence number: ERROR: column "sequence_number" does not exist (SQLSTATE 42703)
[INFO]  Idle light haulers discovered
```

This is the SAME defect class as the s73 P0 (`ships.reserved` missing-migration, SQLSTATE 42703):
a Go model gained a persisted field, but the live Postgres table never got the column.

## Evidence (session 77, 2026-07-03 ~23:48Z)

- Container: `parallel_manufacturing-X1-PZ28-ad53e8ef` (launched clean, daemon stayed HEALTHY the
  whole time — the d-83 fix works; this is a distinct, downstream defect).
- The coordinator progressed past `State recovery complete` → `Supply monitor started` → found 2
  fabrication + 10 collection opportunities, then failed both persists with the 42703 above.
- No ship was claimed (persist fails BEFORE the ship-assignment/task step), so the contract earner
  was unharmed. I stopped the inert container (`container stop` → STATUS=STOPPED); daemon healthy.

## Expected vs actual

- Expected: the manufacturing coordinator persists a pipeline, creates tasks, claims an idle light
  hauler, and begins acquiring/fabricating goods.
- Actual: the very first DB write in the persist path (`SELECT COALESCE(MAX(sequence_number),0)`)
  errors 42703 because `manufacturing_pipelines` has no `sequence_number` column; no pipeline is
  ever created; manufacturing produces nothing.

## Impact

Blocks the ENTIRE manufacturing/fabrication mission thread — BOTH mission horizons depend on this
engine: (#3) manufacturing income and (#1) jump-gate fabrication via `construction start` (which
uses the same pipeline persistence). The contract earner is unaffected (separate code path). With
the d-83 fix landed, this missing migration is now the SOLE remaining blocker on mission progress.

## Code checked (verification gate)

Investigated read-only in `../gobot` (module `github.com/andrescamacho/spacetraders-go`); no code
changed, daemon not re-triggered.

1. **The failing query** — `internal/adapters/persistence/manufacturing_pipeline_repository.go`
   `Create()` (lines 23-46) runs, BEFORE the insert:
   ```go
   r.db.WithContext(ctx).Model(&ManufacturingPipelineModel{}).
       Where("player_id = ?", pipeline.PlayerID()).
       Select("COALESCE(MAX(sequence_number), 0)").Scan(&maxSeq)  // ~line 29
   // → wrapped as "failed to get max sequence number: %w"
   ```
   Table: `manufacturing_pipelines`.

2. **The model DOES declare the field** — `internal/adapters/persistence/models.go:313-340`:
   ```go
   type ManufacturingPipelineModel struct {
       ID             string `gorm:"column:id;primaryKey;size:64"`
       SequenceNumber int    `gorm:"column:sequence_number;not null;default:0"`  // line 316
       ...
   }
   func (ManufacturingPipelineModel) TableName() string { return "manufacturing_pipelines" }  // :338-340
   ```
   So the Go model expects `sequence_number`; the live table lacks it → schema drift.

3. **Persist call sites** — `internal/application/manufacturing/services/manufacturing/pipeline_lifecycle_manager.go`:
   fabrication `pipelineRepo.Create` → "Failed to persist pipeline" (~:324-325); collection →
   "Failed to persist collection pipeline" (~:500-501); storage collection (~:605-606). All route
   through the repo `Create` above. Repo wired at `cmd/spacetraders-daemon/main.go:464`.

4. **ROOT CAUSE — model excluded from startup AutoMigrate.** The daemon calls
   `database.AutoMigrate(db)` on startup (`cmd/spacetraders-daemon/main.go:117`, the ce10b92
   "reconcile schema on startup" pass, non-fatal). That function
   (`internal/infrastructure/database/connection.go:86-100`) passes only 11 models:
   PlayerModel, WaypointModel, ContainerModel, ContainerLogModel, ShipModel, SystemGraphModel,
   MarketData, ContractModel, GoodsFactoryModel, TransactionModel, CaptainEventModel.
   **`ManufacturingPipelineModel` is NOT in the list** (nor `ManufacturingTaskModel`,
   `ManufacturingFactoryStateModel`, `ManufacturingTaskDependencyModel`, gas/storage models).
   Because the model is excluded, AutoMigrate never touches `manufacturing_pipelines` and never adds
   the new column. GORM AutoMigrate is ADDITIVE (it adds missing columns to existing tables), so
   simply adding the model to the list would create the column on the next restart.

5. **No hand-written migration covers it** — `migrations/` stops at `007_fix_contracts_primary_key`;
   none creates `manufacturing_pipelines` or adds `sequence_number`. The migrations README states
   schema is expected to come from `AutoMigrate()` — which excludes this table.

## Suggested fix (preferred: durable, no out-of-band psql needed)

Add the manufacturing (and other omitted) models to the AutoMigrate list in
`internal/infrastructure/database/connection.go:86-100`, e.g.:
```go
&persistence.ManufacturingPipelineModel{},
&persistence.ManufacturingTaskModel{},
&persistence.ManufacturingFactoryStateModel{},
&persistence.ManufacturingTaskDependencyModel{},
```
Because AutoMigrate runs on daemon startup (main.go:117) and is additive, the next restart will
`ALTER TABLE manufacturing_pipelines ADD COLUMN sequence_number BIGINT NOT NULL DEFAULT 0` (and
create any other missing manufacturing columns/tables) automatically — self-healing, unlike the
s73 case which needed a manual psql ALTER. This closes the migration blind spot for the whole
manufacturing subsystem, not just this one column.

Immediate out-of-band unblock (optional, if a restart isn't imminent):
```sql
ALTER TABLE manufacturing_pipelines ADD COLUMN sequence_number BIGINT NOT NULL DEFAULT 0;
```

## Operator note

I cannot run psql (CLI-only actuator) or edit/deploy code directly. The preferred fix is a code
change the fix pipeline can merge; it takes effect on the next daemon restart with NO manual DB
step. After it lands + the daemon restarts, re-run `operations start --system X1-PZ28
--manufacturing` and confirm the coordinator persists a pipeline and claims a hauler (no 42703).
