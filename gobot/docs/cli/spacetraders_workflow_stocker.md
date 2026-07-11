## spacetraders workflow stocker

Fly one dedicated hull as a warehouse-filling stocker loop (as a daemon container)

### Synopsis

Ask the daemon to fly ONE dedicated idle hull as a STOCKER LOOP: each round-trip it
need-ranks the most-needed supported stock good (highest savings-per-unit × units-short),
buys it at the cheapest foreign market (live-verified at the dock), hauls it home to the
warehouse, and deposits it — filling the home warehouse that the trade tours rationally
won't (they correctly prefer direct sells; the stocker dedicates capacity instead).

Continuous mode (--iterations -1): the container fills until nothing is left to stock (the
warehouse is at target, or nothing eligible is affordable/fresh), then completes honestly.
Relaunch it when contracts drain the warehouse. --iterations N runs exactly N productive
round-trips; 0 (default) runs one.

Guards (each fails CLOSED):
  - every buy is live-verified at the dock and capped by the capital ceiling (10% of live
    treasury), the per-leg budget (--budget-per-leg), and the working-capital reserve;
  - the foreign price must be fresh (--max-market-age-minutes, default 75);
  - a run that ends holding cargo it bought but never deposited reports FAILED, never a
    false success (the next run deposits it first).

Execution model: the stocker runs INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed travel, restart-safe — a laden hull resumes
deposit-first). This command only starts it and returns the container id; follow it with
'container logs'. The daemon must be running, and a 'workflow warehouse' must be running at
--warehouse-waypoint. Run only on an idle, dedicated hull.

Examples:
  spacetraders workflow stocker --ship STOCKER-1 --warehouse-waypoint X1-GZ7-H1 --iterations -1 --agent ENDURANCE
  spacetraders workflow stocker --ship STOCKER-1 --warehouse-waypoint X1-GZ7-H1 --budget-per-leg 200000 --iterations -1 --agent ENDURANCE

```
spacetraders workflow stocker [flags]
```

### Options

```
      --budget-per-leg int            Per-buy-leg spend cap in credits (0 = no explicit per-leg cap)
  -h, --help                          help for stocker
      --iterations int                Round-trip count: -1 = CONTINUOUS (fill until nothing left to stock), N>0 = N round-trips, 0 = one
      --max-market-age-minutes int    Freshness cap on the foreign ask at pick (0 = coordinator default, 75)
      --ship string                   Dedicated idle hull to fly the stocker loop (required)
      --target-per-good int           Fill-target override per good (0 = the miner's measured demand units)
      --warehouse-waypoint string     Home warehouse waypoint to deposit into; its system is the demand anchor (required)
      --working-capital-reserve int   Hard spend floor: never drop live treasury below this (0 = coordinator default, 50k)
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

