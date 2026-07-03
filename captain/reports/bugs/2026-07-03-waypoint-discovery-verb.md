---
title: Add waypoint/system discovery read verbs (jump gate + exploration are invisible)
status: merged
kind: feature
---

## Requirement (from the captain's Horizon plan, strategy.md s53/d-60)

The market cache only contains physically-visited MARKETPLACE waypoints. The
jump gate is not a marketplace, so it is invisible: the captain cannot obtain
its waypoint symbol to `ship navigate` there, nor enumerate neighboring
systems to `ship jump` to. The daemon already holds this data (waypoints table
with type + traits; `ship jump` auto-locates the nearest jump gate) but
exposes no READ verb.

## Sketch

Add a `waypoint` command group to the CLI (follow the hexagonal pattern used
by `feat(ship): add ship refresh` — application query handler + tests, gRPC
RPC, daemon service impl, CLI command):

1. `spacetraders waypoint list --system X1-PZ28 [--trait SHIPYARD|MARKETPLACE|...] [--type JUMP_GATE|...]`
   — lists waypoints from the daemon's waypoint cache (symbol, type, traits, x/y),
   syncing the system's waypoints from the API when the cache is empty/stale.
2. `spacetraders waypoint get --waypoint X1-PZ28-I55` — one waypoint's detail.

Acceptance: the captain can find the system's JUMP_GATE waypoint symbol and
all SHIPYARD waypoints without physically visiting anything.

## Constraints

- TDD; follow existing CLI/query/gRPC patterns; no new dependencies.
- Regenerate protos if the .proto changes (worktree has make proto).
