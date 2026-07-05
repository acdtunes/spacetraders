---
title: construction start forever resumes an EMPTY pipeline — a 0-task CONSTRUCTION pipeline permanently blocks its site (no re-plan, no terminalization, no stop verb)
status: merged
kind: fix
---

## Failure signature

- Command: `spacetraders construction start X1-PZ28-I67 --depth 3 --player-id 1`
- Output: `Resumed existing construction pipeline / Pipeline ID: 6c02cbe3-655f-4a13-91a5-5d8e431886a4 / Task Count: 0 / Status: EXECUTING`
- Symptom: the resumed pipeline has ZERO tasks in the DB (its original tasks were cancelled by earlier daemon-restart recoveries), `--depth 3` is silently ignored, and no runtime path will ever create tasks for it or retire it. The construction site X1-PZ28-I67 (the jump gate, mission priority #1) is permanently blocked: FAB_MATS 0/1600, ADVANCED_CIRCUITRY 0/400, pipeline "EXECUTING" at 0.0% forever.
- This is DISTINCT from (and surfaced immediately after) the fix in commit 73f3f08 (report 2026-07-04-construction-pipeline-tasks-never-execute.md): that fix wired activation + rescuer + executor for DELIVER_TO_CONSTRUCTION and is confirmed live, but it is UNREACHABLE because no DELIVER_TO_CONSTRUCTION task exists and nothing can create one for an already-existing pipeline.

## Evidence

- 2026-07-04T06:50:58Z: fix 73f3f08 merged; daemon restarted (coordinator container `parallel_manufacturing-X1-PZ28-eff8e06b` recreated at 06:51:10Z, 12s after merge).
- Coordinator recovery on the FIXED binary (container log 06:51:14Z): "Recovered 3 active pipelines" (incl. the CONSTRUCTION pipeline), "Recovered 3 tasks: 2 ready, 0 interrupted, 0 retried" — the 3 tasks are 2 COLLECT_SELL + 1 ACQUIRE_DELIVER; ZERO construction tasks exist. Over the following poll cycles, no "Activated DELIVER_TO_CONSTRUCTION task" / "Construction task activation summary" lines appear (the new ActivateConstructionTasks finds no PENDING DELIVER_TO_CONSTRUCTION rows).
- ~06:56Z: `construction start X1-PZ28-I67 --depth 3` → "Resumed existing construction pipeline ... Task Count: 0" (verbatim above). Materials unchanged 0/1600, 0/400.
- How the tasks died: recovery reaps tasks whose pipeline isn't in the PLANNING/EXECUTING set at that instant — e.g. today's boot logged `Cancelled task 371ad9e9 (ACQUIRE_DELIVER) - pipeline not active`. The construction pipeline's tasks (created s85, 2026-07-04 ~01:19Z) were reaped across the s85–s87 coordinator restarts, leaving an EXECUTING pipeline with 0 tasks.

## Expected vs actual

- Expected: `construction start <site> --depth 3` yields a pipeline with one dependency-free buy+deliver (DELIVER_TO_CONSTRUCTION) task per outstanding material, which the now-registered construction executor runs; site material counts start climbing.
- Actual: the idempotency check returns the stale empty pipeline immediately; the depth flag and task creation code are only reachable on the brand-new-pipeline path; the empty pipeline can never complete, fail, be recycled, or be stopped, so `FindByConstructionSite` returns it forever.

## Impact

Jump-gate construction — the mission's #1 priority — is hard-blocked with everything else ready: treasury ~6.0M vs a ~1.6–2M all-buyable material bill (FAB_MATS @F56 ABUNDANT, ADVANCED_CIRCUITRY @D45 MODERATE), haulers available, and the 73f3f08 executor/activation fix deployed but starved of tasks. Every future construction site is equally exposed: any coordinator restart while a construction pipeline's tasks are transiently unprotected reaps the tasks and bricks the site.

## Code checked

- `gobot/internal/application/manufacturing/services/construction_pipeline_planner.go:53-80` (`StartOrResume`) — the idempotency check: if `pipelineRepo.FindByConstructionSite` returns a pipeline, it returns `{Pipeline: existing, IsResumed: true}` IMMEDIATELY. No task-count check, no re-plan. `supplyChainDepth` (the `--depth` flag) is consulted only on the new-pipeline path (`:109`, `:131-135`); task creation (`createTasksForMaterial`, `:162-279`, which builds `NewDeliverToConstructionTask` at `:185-193`/`:258-266`) is reachable ONLY from that new-pipeline path.
- `gobot/internal/infrastructure/database/manufacturing_pipeline_repository.go:218-241` (`FindByConstructionSite`) — filters `pipeline_type = CONSTRUCTION AND status IN (PLANNING, EXECUTING)`; a terminal (COMPLETED/FAILED/CANCELLED) pipeline would free the site for a fresh start. `:308-361` (`modelToPipeline`) never loads tasks — so the CLI's "Task Count" is 0 on every resume regardless of DB state (secondary observability defect: the readout can't distinguish a healthy resume from an empty shell).
- `gobot/internal/application/manufacturing/services/pipeline_lifecycle_manager.go:196-265` (`ScanAndCreatePipelines`) — creates only FABRICATION (`:269`) and COLLECTION (`:399`) pipelines/tasks; no construction branch → nothing regenerates construction tasks at runtime.
- `gobot/internal/application/manufacturing/services/pipeline_completion_checker.go:120-159` (`evaluateCompletion`) — `ShouldComplete` requires `FinalCollections >= 1` (a COMPLETED COLLECT_SELL of the pipeline's ProductGood); `ShouldFail` requires a non-retryable failed task. A 0-task CONSTRUCTION pipeline satisfies neither → never terminalized.
- `gobot/internal/application/manufacturing/services/pipeline_recycler.go:53-82` (`DetectStuckPipelines`) — only recycles at >= 5 FAILED tasks; 0 tasks → never recycled.
- `gobot/internal/application/manufacturing/services/state_recovery_manager.go:104-128` — empty-pipeline cleanup applies ONLY to `PipelineTypeCollection` (`:107`); CONSTRUCTION excluded. `:198-257` — tasks whose `PipelineID` is not in the active PLANNING/EXECUTING set are cancelled ("pipeline not active", log at `:249`): the reaping mechanism that emptied pipeline 6c02cbe3.
- Conclusion: on current HEAD there is no CLI verb, coordinator loop, recovery step, recycler, or completion path that can either (a) create tasks for an existing task-less CONSTRUCTION pipeline or (b) move it to a terminal status so `construction start` re-plans. The subsystem fixed in 73f3f08 cannot be reached.

## Suspected root cause and suggested fix

`StartOrResume` equates "a PLANNING/EXECUTING pipeline row exists" with "the pipeline is healthy". Minimal fix: on the resume path, load the pipeline's incomplete tasks (taskRepo, by pipeline id); if there are none and the construction site still has unmet material requirements (constructionSiteRepo), re-plan — run `createTasksForMaterial` for each outstanding material at the REQUESTED depth against the existing pipeline (or mark the empty pipeline FAILED and fall through to the create-new path). Defense in depth (optional but cheap): extend the recovery empty-pipeline cleanup at `state_recovery_manager.go:104-128` to CONSTRUCTION pipelines, and make the CLI print the real persisted task count. A `construction stop` verb would also give operators an escape hatch, but the re-plan-on-empty fix alone unblocks the site.
