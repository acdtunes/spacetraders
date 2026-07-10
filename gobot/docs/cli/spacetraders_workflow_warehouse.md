## spacetraders workflow warehouse

Park an idle hull as a passive inventory warehouse at a home waypoint (as a daemon container)

### Synopsis

Ask the daemon to dedicate one idle hull as a passive inventory warehouse
parked at a home waypoint, as a recovery-safe daemon container (sp-dchv Lane B).

The warehouse buffers a whitelist of contract goods: tour/trade deposit legs drop
cheap cross-system goods into it, and contract workers source those goods from it
in-system at zero market ask (inventory-first sourcing). The warehouse does no
work of its own — it is a standing buffer hull.

Execution model: the warehouse runs INSIDE the daemon as a container
(single-writer, claim-release-on-death, restart-safe). The hull is dedicated to
the "warehouse" fleet, so no other coordinator can poach it. This command only
starts it and returns the container id; follow it with 'container logs'. The
daemon must be running. Run this only on a genuinely idle hull.

Examples:
  spacetraders workflow warehouse --ship ENDURANCE-9 --waypoint X1-GZ7-H1 --goods IRON_ORE,ALUMINUM --agent ENDURANCE
  spacetraders workflow warehouse --ship ENDURANCE-9 --waypoint X1-GZ7-H1 --goods COPPER --player-id 1

```
spacetraders workflow warehouse [flags]
```

### Options

```
      --goods string      Comma-separated whitelist of goods the warehouse buffers (required)
  -h, --help              help for warehouse
      --ship string       Idle hull to dedicate as the warehouse (required)
      --waypoint string   Home waypoint to park the warehouse hull at (required)
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

