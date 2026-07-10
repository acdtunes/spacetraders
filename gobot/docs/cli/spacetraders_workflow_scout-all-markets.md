## spacetraders workflow scout-all-markets

Automatically assign all probe/satellite ships to scout all non-fuel-station markets

### Synopsis

Automatically discovers and assigns all probe/satellite ships in a system to scout
all marketplaces (excluding fuel stations). Uses VRP optimization to distribute markets
efficiently across the fleet.

The command will:
- Find all probe/satellite ships in the specified system
- Find all marketplaces with the MARKETPLACE trait (excluding FUEL_STATION)
- Optimize market distribution using VRP solver
- Create scout-tour containers with infinite iterations

This command is idempotent: ships with existing containers are reused automatically.

Examples:
  # Scout all markets in system X1-GZ7
  spacetraders workflow scout-all-markets --system X1-GZ7 --agent ENDURANCE

  # Scout all markets in system X1-TEST
  spacetraders workflow scout-all-markets --system X1-TEST --player-id 1

```
spacetraders workflow scout-all-markets [flags]
```

### Options

```
  -h, --help            help for scout-all-markets
      --system string   System symbol (required)
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

