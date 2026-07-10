## spacetraders workflow tour-run

Fly one idle hull through planner-chosen, guarded multi-hop trade tours (as a daemon container)

### Synopsis

Ask the daemon to fly ONE idle hull through a planner-chosen multi-hop trade tour:
the depth-aware planner picks a tour over the hull's system and its fresh gate
neighbors, and the container flies it leg by leg — buying and selling in tranches,
re-verifying every price live at the dock, and re-planning when reality drifts. This
is the tour twin of 'workflow arb-run' (which flies one captain-named lane); here the
planner chooses the route.

Continuous mode (--iterations -1): on manifest completion the container re-plans from
the hull's CURRENT position and live market and flies the NEXT tour immediately — no
captain in the loop — until margins die (no profitable tour) or it is stopped. This
turns capital velocity from captain-cadence into engine-cadence: one launch, earns
until the market is exhausted. A laden hull's held cargo is fed to the planner as
sell-legs, so a tour that ends holding stock is liquidated by the next one rather than
needing a manual rescue. --iterations N flies exactly N tours; 0 (default) flies one.

Guards (each fails CLOSED):
  - every buy is live-checked against the working-capital floor and the per-tour spend
    cap (default 25% of live treasury, re-resolved each tour in continuous mode);
  - a leg whose live price has moved past tolerance is skipped and re-planned (bounded);
  - a run that ends holding cargo it bought reports FAILED, never a false success.

Execution model: the tour runs INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed travel, restart-safe — a restart re-plans
from current position/cargo, and a continuous run resumes continuous). This command
only starts it and returns the container id; follow it with 'container logs'. The
daemon must be running. Run only on an idle hull.

Examples:
  spacetraders workflow tour-run --ship TORWIND-19 --agent TORWIND
  spacetraders workflow tour-run --ship TORWIND-19 --iterations -1 --agent TORWIND
  spacetraders workflow tour-run --ship TORWIND-19 --max-hops 4 --max-spend 300000 --iterations -1 --agent TORWIND

```
spacetraders workflow tour-run [flags]
```

### Options

```
  -h, --help                          help for tour-run
      --iterations int                Tour count: -1 = CONTINUOUS (re-plan+fly from the new position until margins die), N>0 = N tours, 0 = one tour
      --max-hops int                  Cap the tour to this many hops (0 = planner default, 6)
      --max-spend int                 Per-tour spend cap in credits (0 = 25% of live treasury, re-resolved each tour when --iterations != 0/1)
      --min-margin int                Per-unit margin floor passed to the planner (0 = planner default)
      --replan-limit int              Max live re-plans on price drift, per tour (0 = coordinator default, 2)
      --ship string                   Idle hull to fly the tour (required)
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

