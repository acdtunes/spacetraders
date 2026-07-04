---
title: Construction pipelines can never deliver — DELIVER_TO_CONSTRUCTION has no activation path and no registered executor
status: merged
kind: fix
---

## Failure signature

- command_type: `manufacturing_coordinator` (container `parallel_manufacturing-X1-PZ28-eff8e06b`) executing construction pipeline `6c02cbe3-655f-4a13-91a5-5d8e431886a4` (pipeline_type=CONSTRUCTION, site X1-PZ28-I67, depth 1).
- Symptom: pipeline status EXECUTING, progress 0.0% for 5+ hours; the coordinator assigned ONLY COLLECT_SELL / ACQUIRE_DELIVER manufacturing tasks to both workers (TORWIND-4/5); ZERO construction task assignments ever; gate bill unchanged (FAB_MATS 0/1600, ADVANCED_CIRCUITRY 0/400).
- No error is ever logged — the failure mode is silent (construction tasks sit PENDING forever).

## Evidence

- `construction status X1-PZ28-I67` at 2026-07-04T06:14Z: `Status: EXECUTING, Progress: 0.0%`, FAB_MATS 0/1600, ADVANCED_CIRCUITRY 0/400 — ~5h after adoption ("Started recovered PLANNING pipeline 6c02cbe3" at coordinator boot 01:19:15Z, session 85 log).
- Coordinator log (`container logs parallel_manufacturing-X1-PZ28-eff8e06b`, window 06:05–06:18Z): continuous "Rescued 3–5 COLLECT_SELL tasks to queue", "COLLECTION pipeline task activation summary", "Max fabrication pipelines reached"; every worker assignment in the window is COLLECT_SELL or ACQUIRE_DELIVER (tasks b771c477, 9658d525, c2c3555b); the word "construction" never appears at runtime.
- Supply is NOT the blocker: both materials are buyable in-system right now — FAB_MATS @ X1-PZ28-F56 (532, ABUNDANT, vol 20), ADVANCED_CIRCUITRY @ X1-PZ28-D45 (1,900, MODERATE, vol 20). Even the buyable material got zero assignments.

## Code checked

Read-only investigation over the daemon source (two passes, session 86):

- **Task lifecycle**: `internal/domain/manufacturing/task.go:467` (`MarkReady` PENDING→READY), `:482` (`AssignShip` READY→ASSIGNED). `TaskQueue.Enqueue` silently drops non-READY tasks (`internal/application/manufacturing/services/task_queue.go:82`); the assigner only pulls READY tasks from the queue (`task_assignment_manager.go:210`, assign loop `:236-333`).
- **Every recurring PENDING→READY+enqueue path is type-filtered and skips construction**:
  - `SupplyMonitor.ActivateSupplyGatedTasks` — ACQUIRE_DELIVER only (`supply_monitor.go:1059`).
  - `SupplyMonitor.ActivateCollectionPipelineTasks` — COLLECT_SELL + PipelineTypeCollection only (`supply_monitor.go:1255,1266,1326`; the "COLLECTION pipeline task activation summary" log is `:1358`).
  - `SupplyMonitor.markCollectTasksReady` — COLLECT_SELL, factory-state driven (`supply_monitor.go:254-408`).
  - `PipelineLifecycleManager.ScanAndCreatePipelines` — FABRICATION + COLLECTION branches only (`pipeline_lifecycle_manager.go:196-265`).
  - `TaskRescuer.RescueReadyTasks` — switch has cases only for CollectSell/AcquireDeliver/StorageAcquireDeliver (`task_rescuer.go:52-79`); DELIVER_TO_CONSTRUCTION hits no case, silently ignored.
  - There is NO activation/rescue path keyed to `PipelineTypeConstruction` or `TaskTypeDeliverToConstruction` anywhere in the runtime loop. The only enqueue opportunity is one-shot at startup recovery (`state_recovery_manager.go:314-333`, enqueue `:357-359`) and requires the task to ALREADY be READY — a depth-1 final DELIVER_TO_CONSTRUCTION has unmet input dependencies at recovery, so it stays PENDING forever with nothing to ever re-evaluate it.
- **No executor registered**: `cmd/spacetraders-daemon/main.go:499-508` registers executors only for AcquireDeliver / CollectSell / Liquidate / StorageAcquireDeliver. Even if a DELIVER_TO_CONSTRUCTION task were somehow assigned, `RunManufacturingTaskWorkerHandler.GetExecutor` fails (`run_manufacturing_task_worker.go:98-103`). No construction coordinator handler is registered in the composition root at all.
- **Starvation/priority refuted**: base priorities are DELIVER_TO_CONSTRUCTION=75 > COLLECT_SELL=50 > ACQUIRE_DELIVER=10 (`task.go:67-83`), and `GetReadyTasks` sorts by effective priority with aging (`task_queue.go:142-198,349-396`) — a READY construction task would win the queue immediately. The observed zero assignments are only explicable by the task never entering the queue.
- **Planner output** (context): `construction_pipeline_planner.go:162-266` — at depth >= 3 (or raw material) it creates ONE dependency-free DELIVER_TO_CONSTRUCTION that buys the final good at market (`:174,185-193`); at depth 1 it fabricates (ACQUIRE_DELIVER input tasks `:311,342` + final DELIVER_TO_CONSTRUCTION depending on them `:258-266`). Tasks are created PENDING (`task.go:197`); the pipeline is left in PLANNING and only `StateRecoveryManager` (`state_recovery_manager.go:87-98`) ever starts it.

Why existing code does NOT already solve this: the construction subsystem is wired for persistence and planning only (pipeline row + task rows, adopted by generic `FindByStatus(PLANNING,EXECUTING)` recovery which ignores type) — the coordinator runtime was never taught the CONSTRUCTION pipeline type or the DELIVER_TO_CONSTRUCTION task type. Three independent gaps (no activation, no rescue, no executor); any one alone is fatal.

## Expected vs actual

- Expected: coordinator adopts the CONSTRUCTION pipeline, its tasks become READY as dependencies/markets allow, workers acquire + deliver to the site, `construction status` material counts climb.
- Actual: structurally impossible with the current binary. Tasks stay PENDING forever; no error, no event, pipeline reads "EXECUTING" indefinitely.

## Impact

The jump gate (X1-PZ28-I67) — Admiral directive #1 and the mission spine — cannot progress AT ALL by any CLI path. `construction start` is the only construction actuator and its output is dead-on-arrival regardless of depth, worker count, or material availability. Treasury (5.35M), materials (both buyable in-system), and haulers are all ready; only this wiring blocks the mission.

## Suspected root cause and fix sketch

Half-shipped subsystem: planner + persistence landed (d-86 migration made the rows persist) but the coordinator runtime was never extended. Minimal fix:

1. Register a DELIVER_TO_CONSTRUCTION executor in `main.go` (acquire good — buy at market or collect from factory per task spec — navigate to construction site, `SupplyConstruction` API call, record progress).
2. Add an activation path for construction tasks: include TaskTypeDeliverToConstruction in the supply monitor's recurring activation (dependency check + market/factory availability → MarkReady + Enqueue), and add a TaskRescuer case so orphaned READY construction tasks re-queue.
3. (Optional hardening) Have the coordinator log a WARNING when it adopts a pipeline containing task types it has no executor for — this failure was silent for 5+ hours and would have been silent forever.

Secondary, separable issue: at depth 1 the planner fabricates FAB_MATS from raw inputs even when the finished good is ABUNDANT at a market (`construction_pipeline_planner.go:174`); operationally we will re-plan at depth 3 once execution works, so no code change is required for that if depth-3 semantics are preserved.
