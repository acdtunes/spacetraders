# st-drm restart-resilience fix — bootstrap singleton admission + in-flight acquisition guard

Epic **st-drm**, beads **st-drm.14** (D1) and **st-drm.6** (D2). Outside-In TDD, red-first.
Pure Go work in `gobot/`. No daemon/twin booted, no live-stack or bootstrap-harness e2e run —
verification is `go build ./...` + targeted `go test` + `go vet` on the touched packages, and a
rebuild of both binaries.

---

## DEFECT 1 — Bootstrap singleton admission (per player)  [st-drm.14]

### Root cause (from the live evidence)
After a daemon kill+reboot the daemon correctly **recovered** lifetime-1's bootstrap container
(recovery re-adopts a persisted coordinator) **and** also accepted the test's fresh
`workflow bootstrap` launch → **two** bootstrap-command containers RUNNING concurrently for player 1
(`bootstrap-player-1-53a85cc8` recovered + `bootstrap-player-1-d4a8596d` fresh). Each brain ran its own
reconcile loop and bought its own probes → 6 satellites for a target of 3, treasury drained 175000 → 0.
On the real API this is a catastrophic double-spend after **any** daemon restart.

### Fix
`DaemonServer.BootstrapCoordinator` (the single launch path both the CLI and the gRPC service funnel
through) now rejects a fresh launch when a bootstrap coordinator for the player is already **ACTIVE**,
naming it: `bootstrap already running: <id>`.

"Active" spans the three lifecycle states that mean *running / starting / pending-recovery*:
`RUNNING`, `PENDING`, and `INTERRUPTED` (running-when-daemon-stopped, pending recovery). Scanning all
three closes the restart-recovery race: whether the fresh launch arrives **before** the daemon re-adopts
the persisted coordinator (it is `INTERRUPTED` in the DB) or **after** (it is `RUNNING`), it is seen and
rejected. Recovery of a persisted container is preserved: it re-adopts through `RecoverRunningContainers`
→ `recoverContainer`, a path that does **not** funnel through `BootstrapCoordinator`, so this guard never
blocks a legitimate restart recovery.

### Exact location
- Admission check: `gobot/internal/adapters/grpc/container_ops_bootstrap.go:32`
  (inside `DaemonServer.BootstrapCoordinator`, before any container id/row is created).
- Helper: `activeBootstrapContainerID(ctx, lister, playerID)` at
  `gobot/internal/adapters/grpc/container_ops_bootstrap.go:98` — mirrors the existing
  `firstContainerIDOfType` status-scan idiom. It takes a narrow `containerStatusLister` interface
  (`ListByStatusSimple`) so it is unit-testable with a fake, no DB. `*persistence.ContainerRepositoryGORM`
  satisfies it. Match is on `ContainerType == "BOOTSTRAP_COORDINATOR"` (the domain type is what the repo
  persists as `ContainerType`).

### Multi-active recovery de-dup — deliberately NOT added
The task allowed "recover the newest, mark the rest superseded *only if cheap*." It is **not** cheap:
`RecoverRunningContainers` is a ~150-line function with era-scoping, worker-adoption, and lost-container
diffing; injecting per-type de-dup would be invasive and risky. It is also unnecessary going forward —
the admission guard prevents a **second** active bootstrap row from ever being created (the fresh launch
that produced the duplicate is now rejected), so recovery cannot surface two. Left out per "only if cheap".

---

## DEFECT 2 — In-flight-aware acquisition dispatch  [st-drm.6]

### Root cause
A staged buy dispatches an acquisition whose new hull does not necessarily surface in the **same** tick's
observation. With only the count guard (`ProbeCount < target`), consecutive ticks that observe the still-
unmet count each re-dispatch — buying 3 for a target of 3 already being met. Invariant wanted: **at most
one in-flight acquisition per (player, shipType)**; a tick observing need>0 while one is active dispatches
nothing, and the first tick after it lands re-derives need from the world.

### Seam chosen — a domain-level "pending acquisition" port (of the two the task offered)
A new **`AcquisitionTracker`** port on the coordinator, consulted before **every** staged buy, with an
adapter that answers it from the container repo. This keeps the dispatch decision inside the hexagon
(testable through a fake, exactly like the other ports), leaves the phase logic untouched, and follows
the code's existing container-repo-scan idiom (`contractFleetCoordinatorRunning` / `containerTypeRunning`).

- Port definition: `gobot/internal/application/bootstrap/commands/run_bootstrap_coordinator.go:169`
  (`AcquisitionTracker.InFlight(ctx, playerID, shipType)`), setter `SetAcquisitionTracker` — **nil-safe**
  (unset ⇒ the legacy count-only staging, so all existing wiring/tests are unaffected).
- Reconciler guard (the dispatch seam): helper `acquisitionInFlight` at
  `run_bootstrap_reconcile.go:354`, called at the top of each staged buy:
  - probe (DATA): `run_bootstrap_reconcile.go:261`
  - hauler (INCOME): `run_bootstrap_income.go:138`
  - gate worker (GATE): `run_bootstrap_gate.go:337`
  A tracker **read error fails CLOSED** (treated as in-flight, no dispatch) — a repo hiccup must never
  green-light a double-buy. On a skip it records the `acquisition_in_flight` blocker + a heartbeat line
  (never a silent stall).
- Adapter: `bootstrapAcquisitionTracker` at `gobot/internal/adapters/grpc/bootstrap_ports.go:371`, backed
  by `batchPurchaseInFlight` (`bootstrap_ports.go:352`) — scans the container repo for a RUNNING/PENDING
  `batch_purchase_ships` container for the player. Wired at `bootstrap_ports.go:77`.

### Scope note (honest boundary)
`derivePhase`, the need math, caps, and capital gates are **unchanged** — the guard is an additional
early check on the existing dispatch path. Note that the current bootstrap buy is **synchronous**
(`bootstrapAcquirer.Buy` sends `BatchPurchaseShipsCommand` through the mediator, which blocks on
`NavigateRouteCommand` until arrival and then persists the new hull locally via `createIdleAssignment`
+ `refreshPurchasedShip`; `SyncAllFromAPI` upserts without deleting). So within one **sequential** post-D1
lifetime the synchronous buy already cannot overshoot — the observed triple-dispatch was the **D1**
concurrent-container race, which D1 removes. The D2 port is therefore the enforced, tested defensive guard
at the dispatch seam: it prevents re-dispatch whenever a batch-purchase acquisition container is active,
hardening the invariant and future-proofing it (e.g. if a buy is later routed through the async
`batch_purchase_ships` container path, the guard is already active with no reconciler change). The buy was
deliberately **not** flipped to async in this change: routing haulers/gate-workers through the async
container would lose their post-buy dedicate+place (the batch container has no dedication step), it has no
coordinator precedent (only the CLI uses that path), and it cannot be integration-tested here — a large,
unverifiable change against the "keep it small / no live-stack" mandate.

---

## Behavior deliberately NOT changed (and why)
- **Phase logic** (`derivePhase`, need/coverage/income math, caps, capital & readiness gates, staging =
  one buy per tick): untouched — the task forbids changing it; the fixes are purely additive guards.
- **Recovery** (`RecoverRunningContainers` / `recoverContainer`): untouched — it must keep re-adopting a
  persisted coordinator; the D1 guard sits only on the fresh-launch path, so recovery is unaffected.
- **The synchronous buy path** (mediator `BatchPurchaseShipsCommand`, dedicate+place for haulers/gate
  workers): untouched — see the scope note; flipping to async is out of scope and unverifiable here.
- **Multi-active recovery de-dup**: not added ("only if cheap" — it is not, and the admission guard makes
  it unnecessary).

---

## Verification
- `go build ./...` — green.
- `go vet ./internal/adapters/grpc/ ./internal/application/bootstrap/commands/` — clean.
- `go test ./internal/application/bootstrap/commands/ -count=1` — ok (full suite, no regression).
- `go test ./internal/adapters/grpc/ -short -count=1` — ok (13.8s, full package short mode, no regression).
- New tests (TDD red-first; the red was a genuine compile-absent red, then green after impl):
  - D1 `container_ops_bootstrap_admission_test.go`:
    - `TestActiveBootstrapContainerID_RunningRecoveredCountsAsActive` — a RECOVERED (RUNNING) coordinator counts as active and is named.
    - `TestActiveBootstrapContainerID_PendingRecoveryCountsAsActive` — INTERRUPTED (pending-recovery) counts as active.
    - `TestActiveBootstrapContainerID_PendingCountsAsActive` — PENDING (starting) counts as active.
    - `TestActiveBootstrapContainerID_NoActiveBootstrap_AllowsLaunch` — other types / no active bootstrap ⇒ launch allowed (and it scans all three statuses).
    - `TestActiveBootstrapContainerID_RepoError_FailsClosed` — repo error surfaces (caller rejects the launch).
  - D2 `run_bootstrap_inflight_test.go`:
    - `TestBootstrap_ProbeBuy_SkippedWhileAcquisitionInFlight` — active acquisition ⇒ tick dispatches nothing (no price-check, no buy), blocker `acquisition_in_flight`, tracker consulted for the probe type.
    - `TestBootstrap_ProbeBuy_DispatchesWhenNoneInFlight` — none active ⇒ dispatches exactly once.
    - `TestBootstrap_ProbeBuy_TrackerReadError_FailsClosed` — tracker error ⇒ fail closed, no buy.
    - `TestBootstrap_ProbeBuy_NoOvershoot_WhenHullLagsBehindObservation` — multi-tick lag model (hull surfaces 2 ticks after dispatch): buys the target (3) EXACTLY once, no overshoot; container-exited ⇒ next tick may dispatch again.
- Binaries rebuilt: `make -C gobot build-cli build-daemon` → `gobot/bin/spacetraders`,
  `gobot/bin/spacetraders-daemon` (both fresh; gitignored, so carried in the worktree, not committed).

---

## Quality gates
Active tests pass · all touched-package unit tests pass · build green · `go vet` clean · no test skips ·
test budget respected (2 distinct D1 behaviors → 5 parametrized-by-status pins; 2 distinct D2 behaviors →
4 pins incl. the multi-tick acceptance) · no mocks inside the hexagon (fakes at the ports only) · business
language in tests.
