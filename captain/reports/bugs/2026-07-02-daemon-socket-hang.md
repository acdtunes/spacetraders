---
title: Daemon socket subsystem hangs with "context deadline exceeded" and does not self-recover
status: gate_failed
kind: fix
---

## Failure signature

`failed to connect to daemon: failed to connect to daemon socket: context deadline exceeded`

Returned by every socket-backed CLI verb (`health`, `ship`, `container`,
`workflow`, `operations`). The DB-backed path (`ledger`, `market`, `player`)
stays fully responsive throughout — so the daemon process is alive, but its
Unix-socket listener (`/tmp/spacetraders-daemon.sock`) is wedged.

## Occurrences (4 sessions — escalation threshold met, still unresolved)

- **s2 (2026-07-02)** — launch-induced. Firing two heavy workflows concurrently
  (VRP scout-fleet-assignment + contract negotiation) hung the socket ~2 min;
  it self-recovered. Mitigated by "launch one at a time" (L22/L25).
- **s6 (2026-07-02, ~23:04–23:10Z)** — SPONTANEOUS. No launch; the hang began
  during read-only assessment, coincident with container
  `batch_contract_workflow-TORWIND-1-e1871c14` hitting `Route segment execution
  failed / context canceled` at 23:04:02Z. Probed `health` 16× over ~5 min with
  no self-recovery.
- **s7 (2026-07-02, ~23:11Z)** — STILL hung at session start.
  3 probes (`health` ×2, `container list` ×1) all returned `context deadline
  exceeded`. DB path (`ledger list --player-id 1`) answered instantly.
- **s8 (2026-07-02, ~23:15Z, this session)** — STILL hung. `health` and
  `container list` both returned `context deadline exceeded`; DB path answered
  instantly (still 7 txns, treasury 175,251, no fulfillment). ~11 min after the
  23:04Z onset with no self-recovery. This report was already `status: new` at
  s8 start — the fix has not yet landed.

## Evidence

- Container `batch_contract_workflow-TORWIND-1-e1871c14` (contract_workflow):
  restarted 23:00:58Z, resumed the active IRON_ORE contract, logged
  `Contract profitability confirmed` → `Multi-trip purchase initiated` →
  navigating, then at **23:04:02Z**: `Route segment execution failed` →
  `Context canceled, stopping container` → released ship. (Logs read in s6
  before the socket wedged; now unrecoverable — socket down.)
- Container `scout-tour-TORWIND-2-65007a67` (scout_tour) — died in the same
  window; `heartbeat_lost` fired 23:10:51Z (last heartbeat 23:03:55Z).
- Pending feed emitted `workflow.finished success` for BOTH containers at
  23:04:02Z, yet the ledger shows the work did NOT complete (see below) — the
  success flag is unreliable when the container is torn down by the socket fault.
- Ledger (DB path, s7): still **7 transactions**, last a 19:36Z REFUEL. No
  `PURCHASE_CARGO`, no `CONTRACT_FULFILLED`. Treasury frozen at 175,251 since
  19:36Z. The accepted IRON_ORE contract (+1,547 acceptance, 19:16Z) remains
  unfulfilled — its 2nd payment is blocked.

## Expected vs actual

- **Expected:** the socket listener stays responsive independently of any single
  container's route failure; a container's `context canceled` should not wedge
  the daemon's socket for other callers. On transient stalls, the listener
  recovers within seconds.
- **Actual:** a container route-segment failure appears to wedge the shared
  socket listener; it stayed hung >5 min in s6 and was still hung >7 min later
  at s7 start, blocking ALL actuation while the daemon otherwise runs.

## Impact

Total loss of actuation. The Captain cannot move ships, launch/stop containers,
or run workflows while the socket is hung — only read-only DB queries work. The
IRON_ORE contract has been blocked for 3 consecutive sessions. There is no
Captain-side remedy: no CLI verb restarts the daemon, process control is
permission-denied, and no `daemon status`/socket-health introspection exists.

## Suspected root cause

The socket listener and container-execution paths appear to share a lock or a
single goroutine/handler that a `context canceled` on a container's route
segment can block indefinitely. The strong correlation in s6 (hang began exactly
at the 23:04:02Z route-segment failure) points at container teardown holding a
resource the socket accept/serve loop needs. Candidate fixes: (a) isolate the
socket listener from container lifecycle (separate goroutine + timeout on any
shared mutex), (b) add a watchdog that resets a wedged listener, (c) expose a
`daemon status` / socket-health verb and a Captain-invokable restart so hangs are
observable and recoverable without waiting for an out-of-band process restart.

## Operator addendum (2026-07-02, human)

Occurrences s6/s7 were NOT the bot: a manual daemon restart raced the old
process's graceful-drain PID lock, so no daemon was running from ~22:55 until
the forced restart at ~23:16. Disregard s6/s7 as evidence.

Occurrence s2 remains valid and is the real scope of this report: launching
two heavy workflows simultaneously (scout-all-markets + batch-contract) made
the daemon socket unresponsive (~2 min of context-deadline-exceeded health
checks) while the process stayed alive. Suspect: gRPC accept loop or container
startup path contention. Repro: fire both workflows within one second on a
fresh daemon.
