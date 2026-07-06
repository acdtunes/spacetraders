> ENGINE MOVED: this workspace is legacy. The captain is a city agent (acd run captain); state lives in beads (sp- db). See docs/superpowers/specs/2026-07-06-ai-engine-city-bridge-design.md.

# Automation Guide — the container engine and how to propose new automations

You have full read access to the bot's code — this guide orients you, the
code is the truth. You can propose and ship new automations built on the
container engine. Proposals go
to `reports/bugs/YYYY-MM-DD-<slug>.md` with `kind: automation` frontmatter;
the fix pipeline builds them in an isolated worktree, gated by build+tests,
and the result lands as a branch for Admiral review.

## The container engine (what your automations run on)

- **Containers** are daemon goroutines with lifecycle (PENDING→RUNNING→
  COMPLETED/FAILED/STOPPED), iteration counts (finite or infinite), restart
  policies (max 3), heartbeats (30s, stale detection), and DB-persisted logs
  (`container logs <id>`). Everything you see in `container list` is one.
- **The coordinator/worker pattern** is the engine's power tool: a long-lived
  COORDINATOR container discovers resources (e.g. idle haulers), spawns
  per-task WORKER containers, and reacts to their completion via
  WorkerCompletedEvent on the ship event bus (instant, no polling). Exemplars
  you already operate: contract_fleet_coordinator (discovers haulers,
  balances positions, one worker per contract), scout-tour (single worker,
  infinite iterations), manufacturing coordinator (priority task queues,
  supply-chain resolution, parallel workers), gas extraction (siphon workers +
  storage ships feeding manufacturing via STORAGE_ACQUIRE_DELIVER tasks).
- **Ship assignment** prevents two containers from commanding one ship;
  assignments release on completion/failure.
- **Commands** flow through a mediator (CQRS): an automation = one or more
  command handlers (application layer) + a container op that runs them +
  a gRPC RPC + a CLI verb. The routing service (OR-Tools) is available for
  path/tour/fleet-partition optimization (Dijkstra/TSP/VRP).

## Anatomy of a good automation proposal

1. **Problem + evidence**: what recurring work/opportunity, with decision ids
   and ledger/log data. ROI estimate in $/h or risk retired.
2. **Code checked (MANDATORY)**: the existing files/functions you read and
   the evidence they do not already solve this. No section = auto-rejected.
2. **Design sketch**: coordinator or worker? What does it discover, what does
   it spawn, what events does it react to, when does it stop? Which existing
   exemplar is it closest to? What CLI surface (`<verb> start/status/stop`)?
3. **Safety**: iteration caps, spend caps (--budget style flags), how it
   avoids fighting existing coordinators for ships, kill path (container stop).
4. **Acceptance**: the observable behavior that proves it works (containers
   visible, ledger rows, KPI movement).

## Constraints the pipeline enforces

- Automations build in an isolated worktree; supervisor runs build+tests.
- Automations auto-merge like everything else once the gate passes. They get
  double the build time and no diff cap; budget shared with features.
- Additive schema changes (new model columns/tables) are applied automatically
  by the daemon AutoMigrate on startup — no hand-written migration needed.
  DESTRUCTIVE migrations (drop/rename/type-change) still need human signoff.
- No new dependencies.
