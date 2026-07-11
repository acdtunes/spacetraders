## spacetraders frontier start

Start the standing frontier expansion coordinator

### Synopsis

Start the frontier expansion coordinator for a player. Run it with --dry-run
first to watch a cycle's decisions (ranking, would-declare, would-buy) without
buying or declaring anything, then start it for real.

Examples:
  spacetraders frontier start --agent ENDURANCE --dry-run
  spacetraders frontier start --agent ENDURANCE
  spacetraders frontier start --player-id 1 --tick 60s --max-probe-fleet 40 --max-spend-per-cycle 100000

```
spacetraders frontier start [flags]
```

### Options

```
      --dry-run                      Log decisions without buying or declaring anything
      --expansion-max-hops int       Gate-graph reach for the expansion queue; 0 uses the default (3)
  -h, --help                         help for start
      --max-probe-fleet int          Total satellite cap; 0 uses the default (40)
      --max-spend-per-cycle int      Max probe spend per trailing window; 0 uses the default (100000)
      --purchase-cooldown duration   Min time between probe buys (e.g. 10m); 0 uses the default
      --tick duration                Reconcile cadence (e.g. 60s); 0 uses the coordinator default
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders frontier](spacetraders_frontier.md)	 - Standing frontier expansion: auto-buy probes and seed frontier scouts

