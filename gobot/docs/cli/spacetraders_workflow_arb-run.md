## spacetraders workflow arb-run

Fly one idle hull through a single captain-directed, guarded arbitrage leg (as a daemon container)

### Synopsis

Ask the daemon to fly ONE idle hull through a single captain-specified arbitrage
leg: buy a good at a source waypoint, route (cross-gate if needed) to a destination
waypoint, sell once, and stop. This is the safe middle between hand-flying an arb leg
and the autonomous 'workflow trade-route' circuit — you name the lane, it flies it
once, guarded.

Guards (each fails CLOSED, refusing the buy, rather than risk an overspend):
  - the hull must actually be at --buy-at before anything is bought;
  - the live source ask vs the destination bid must clear --min-margin;
  - the tranche is capped by --max-units, the hull's hold, and --max-spend;
  - the buy must not drop live treasury below the working-capital reserve.

Execution model: the run executes INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed travel, restart-safe). This command only
starts it and returns the container id; follow it with 'container logs'. The daemon
must be running. Run this only on a genuinely idle hull.

Examples:
  spacetraders workflow arb-run --ship ENDURANCE-7 --good IRON_ORE --buy-at X1-GZ7-A1 --sell-at X1-GZ7-B2 --agent ENDURANCE
  spacetraders workflow arb-run --ship ENDURANCE-7 --good FUEL --buy-at X1-GZ7-H1 --sell-at X1-AB3-C4 --max-units 40 --max-spend 200000 --min-margin 500 --player-id 1

```
spacetraders workflow arb-run [flags]
```

### Options

```
      --buy-at string                 Source waypoint to buy at (required)
      --good string                   Good to buy at the source and sell at the destination (required)
  -h, --help                          help for arb-run
      --max-spend int                 Working-capital cap on the buy in credits (0 = no explicit cap)
      --max-units int                 Cap the tranche to this many units (0 = the hull's full available cargo)
      --min-margin int                Per-unit margin floor: abort before buying if (dest bid − source ask) < this (0 = only reject a non-positive margin)
      --sell-at string                Destination waypoint to sell at, may be cross-system (required)
      --ship string                   Idle hull to fly the arb leg (required; must already be at --buy-at)
      --working-capital-reserve int   Hard spend floor: never drop live treasury below this (0 = coordinator default)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders workflow](spacetraders_workflow.md)	 - Execute complex multi-step workflows

