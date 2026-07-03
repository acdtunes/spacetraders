---
title: operations start --manufacturing crashes the whole daemon in a restart loop (nil eventSubscriber panic on a naked goroutine)
status: merged
kind: fix
---

## Failure signature

Launching `spacetraders operations start --system X1-PZ28 --manufacturing --max-workers 1 --max-pipelines 1`
puts the **daemon process** into a restart loop (~every 40-50s) instead of running a manufacturing coordinator.
The manufacturing container never progresses past its `State recovery complete` log line, never claims a ship,
and never discovers any goods. This blocks the entire manufacturing/fabrication mission thread.

## Evidence (session 74, 2026-07-03 ~22:36–22:38Z)

- Container launched: `parallel_manufacturing-X1-PZ28-b7565a76`.
- The **daemon's** container "created" timestamps advanced every ~40-50s across probes: 22:36:02 → 22:36:54
  → 22:37:26 → 22:38:19 — i.e. the daemon process itself was restarting.
- The manufacturing container's own **Restart Count stayed 0** — confirming it is the DAEMON crashing, not the
  container's internal retry loop.
- The coordinator log reached only `State recovery complete` (0 pipelines / 0 tasks / 0 factories) before each
  restart reset it → zero experiment signal.
- Other coordinators (contract fleet coordinator `35df0a9f`, scout tour) run on this same daemon indefinitely
  with no instability — only the manufacturing coordinator destabilizes it.
- Recovery: `container stop parallel_manufacturing-X1-PZ28-b7565a76` (persists STATUS=STOPPED to the DB) →
  the daemon **immediately** stabilized and the restart loop ceased. `operations stop --manufacturing` did
  NOT work (it had lost operation tracking across the restarts). The contract earner was unharmed — treasury
  climbed +223,448 through the ~3-min loop (coordinator work commits to the DB regardless, per prior L44).

## Expected vs actual

- **Expected:** the manufacturing coordinator runs in its own container; if it cannot start, it fails only its
  OWN container (logged/FAILED), leaving the daemon and all other containers healthy.
- **Actual:** it panics on a naked goroutine with no `recover()`, killing the whole daemon process; the
  supervisor restarts the daemon, which re-recovers the still-RUNNING manufacturing container, which panics
  again → an unbounded ~40-50s restart loop that only a `container stop` breaks.

## Impact

- The parallel manufacturing income stream (Horizon #3) is UNRUNNABLE.
- The jump-gate fabrication campaign (Horizon #1, `construction start X1-PZ28-I67` for FAB_MATS/ADVANCED_CIRCUITRY)
  runs on the SAME fabrication engine and is very likely blocked by the same wiring gap — so BOTH mission
  threads are gated on this one defect.
- Any accidental `operations start --manufacturing` will restart-loop the production daemon that runs the
  record contract earner. (The earner self-heals because its work is DB-committed, but this is a live
  stability hazard.)

## Suspected root cause (code-verified, read-only, over ../gobot)

An **unrecovered nil-pointer panic on the manufacturing coordinator's execution goroutine**, caused by a
missing dependency-wiring call in `main.go`.

1. `internal/application/manufacturing/commands/run_parallel_manufacturing_coordinator.go:199-203` dereferences
   `h.eventSubscriber` right after state recovery:
   ```go
   workerCompletedCh := h.eventSubscriber.SubscribeWorkerCompleted(cmd.ContainerID)   // :200 — h.eventSubscriber is nil → panic
   taskReadyCh := h.eventSubscriber.SubscribeTasksBecameReady(cmd.PlayerID)
   ```
   `h.eventSubscriber` (declared :75) is only ever assigned by `SetEventSubscriber` (:139).
2. **That setter is never called for this handler.** In `cmd/spacetraders-daemon/main.go:548-571` the parallel
   manufacturing handler is constructed and only `SetStorageRecoveryService` / `SetStorageOperationRepository`
   are wired. The **contract** fleet coordinator, by contrast, is correctly wired at `main.go:413`
   (`contractFleetCoordinatorHandler.SetEventSubscriber(shipEventBus)`) — which is exactly why contracts run
   fine and only manufacturing crashes. The nil is an unassigned interface value, so `:200` panics with
   `invalid memory address or nil pointer dereference`.
3. The command runs on a **naked goroutine with no recover()**: `internal/adapters/grpc/container_runner.go:143`
   (`go r.execute()`), and `execute()` (:246-382) has only `defer close(r.done)` — no `defer recover()`. The
   mediator `Send` path (`internal/application/mediator/mediator.go:56-83`) also has no recover. A panic on a
   goroutine with no recover terminates the whole process → supervisor restart → `daemon_server.go:329-338`
   re-recovers the RUNNING manufacturing container → panic again → the observed loop. The container's own
   RestartCount stays 0 because the container error-bookkeeping in `execute()`/`handleError` is never reached
   (the process is already dead).
4. The `State recovery complete` line is `internal/application/manufacturing/services/manufacturing/state_recovery_manager.go:430-433`,
   logged at :192 immediately before the panic site at :200 — so it is always the last line, and the all-zero
   counts are just a fresh-DB coincidence, **NOT** a zero-goods edge case (the crash is unconditional, before
   any discovery/VRP call).
5. **Secondary, same class (latent):** `h.eventPublisher` is likewise never set in `main.go`; it is passed to
   the SupplyMonitor at `run_parallel_manufacturing_coordinator.go:579` and run on another naked goroutine
   `go supplyMonitor.Run(ctx)` (:580) — a second daemon-killing panic waiting behind the first.

## Suggested fix

1. **Real fix (wiring):** in `cmd/spacetraders-daemon/main.go` after constructing the parallel manufacturing
   handler (~:548), add `SetEventSubscriber(shipEventBus)` and `SetEventPublisher(shipEventBus)`, mirroring the
   contract coordinator at `main.go:413`.
2. **Defense in depth:** nil-check `h.eventSubscriber`/`h.eventPublisher` in `Handle` and return an error rather
   than dereferencing at :200, so a future mis-wire fails only its own container.
3. **Containment:** add `defer func(){ if rec := recover(); rec != nil { … route through handleError … } }()`
   in `ContainerRunner.execute()` (`container_runner.go:246`) so NO coordinator panic can ever take down the
   whole daemon again.

## Code checked

- `internal/application/manufacturing/commands/run_parallel_manufacturing_coordinator.go` — `Handle` calls
  `recoverState` (:192, logs "State recovery complete"), `startSupplyMonitor` (:197), then dereferences
  `h.eventSubscriber` (:200) and `h.eventPublisher` (via SupplyMonitor :579-580). `eventSubscriber` declared
  :75, assigned only by `SetEventSubscriber` :139. → confirms the nil-deref site.
- `cmd/spacetraders-daemon/main.go:548-571` — parallel manufacturing handler construction: only
  `SetStorageRecoveryService`/`SetStorageOperationRepository` called; NO `SetEventSubscriber`/`SetEventPublisher`.
  vs `main.go:413` where the contract coordinator IS wired with `SetEventSubscriber(shipEventBus)`. → confirms
  the missing-wiring root cause and why only manufacturing is affected (does NOT already handle it).
- `internal/adapters/grpc/container_runner.go:143` (`go r.execute()`), `:246-382` (`execute()`, only
  `defer close(r.done)`, no recover), `:183-194` (`container stop` persists STATUS=STOPPED). → confirms the
  naked-goroutine-panic → whole-process-death mechanism and why `container stop` breaks the loop.
- `internal/adapters/grpc/daemon_server.go:329-338` (+ impl :593) — `RecoverRunningContainers` re-runs RUNNING
  top-level containers on daemon startup. → confirms why the loop repeats until STATUS=STOPPED.
- `internal/application/mediator/mediator.go:56-83` — `Send` has no recover. → confirms no recover on the path.
- `internal/application/manufacturing/services/manufacturing/state_recovery_manager.go:430-433` — the
  "State recovery complete" log with pipeline/task counts. → confirms the last-log-line timing.

Evidence these files do NOT already solve the problem: the contract coordinator's correct `SetEventSubscriber`
wiring (`main.go:413`) is present and absent for manufacturing (`main.go:548-571`); no `recover()` exists on the
`go r.execute()` goroutine path; and the panic site executes unconditionally before any discovery/claim logic,
so no existing guard prevents it.
