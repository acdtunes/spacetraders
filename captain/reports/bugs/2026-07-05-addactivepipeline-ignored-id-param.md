---
title: "AddActivePipeline ignores its id parameter (misleading signature)"
status: merged
kind: fix
---

# AddActivePipeline ignores its `id` parameter

## Failure signature

`PipelineLifecycleManager.AddActivePipeline(id string, pipeline *manufacturing.ManufacturingPipeline)`
discards `id` entirely — it delegates to `registry.Register(pipeline)`, which keys the
registry by `pipeline.ID()`. A caller passing an `id` that differs from `pipeline.ID()`
gets behavior that contradicts the signature (the pipeline is stored under `pipeline.ID()`,
not under the supplied `id`), with no error or warning.

- Interface decl: `internal/application/manufacturing/services/manufacturing/pipeline_lifecycle_manager.go:48`
- Concrete method: `internal/application/manufacturing/services/manufacturing/pipeline_lifecycle_manager.go:183`
- Registry keying: `active_pipeline_registry.go` `Register()` keys by `pipeline.ID()`

## Evidence

Grep of all callers (`grep -rn AddActivePipeline internal/ cmd/`) found a single call site:

- `internal/application/manufacturing/commands/run_parallel_manufacturing_coordinator.go:470-471`
  ```go
  for id, pipeline := range result.ActivePipelines {
      pipelineMgr.AddActivePipeline(id, pipeline)
  }
  ```

`result.ActivePipelines` is populated exclusively as
`result.ActivePipelines[pipeline.ID()] = pipeline`
(`state_recovery_manager.go:366`). So the single caller's `id` is always exactly
`pipeline.ID()` — the parameter is dead, and removing it is behaviorally safe.

## Expected vs actual

- Expected: signature reflects reality — either the registry keys by the supplied `id`,
  or the `id` parameter does not exist.
- Actual: `id` is silently ignored; the registry always keys by `pipeline.ID()`.

## Suspected root cause and suggested fix

Root cause: leftover parameter from an earlier registry API that keyed by an
externally-supplied id. The registry now derives its key from `pipeline.ID()`, leaving
`id` vestigial.

Suggested fix (confirmed safe, all callers pass `pipeline.ID()`):
1. Remove `id string` from the interface method `PipelineManager.AddActivePipeline` and
   from the concrete `PipelineLifecycleManager.AddActivePipeline`.
2. Update the sole call site to `pipelineMgr.AddActivePipeline(pipeline)`.
   (The loop variable `id` becomes unused; iterate with `for _, pipeline := range ...`.)
3. Add a characterization test first documenting that after `AddActivePipeline(pipeline)`,
   `GetActivePipelines()` keys the entry by `pipeline.ID()`.

## Why deferred (not fixed in this pass)

The only remediation (parameter removal) requires editing the call site in the
`internal/application/manufacturing/commands` package, which is outside the authorized
file/package scope for this fix task (scope was limited to
`internal/application/manufacturing/services/manufacturing`). Per the task's hard rule
(do not force a fix whose signature ripples beyond the assigned packages), the change is
deferred here with the fix fully specified above. It is a low-risk, mechanical change once
the commands package is in scope.

Merged by harbormaster cleanup batch 2026-07-05 — param removed, caller updated.
