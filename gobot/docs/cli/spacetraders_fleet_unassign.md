## spacetraders fleet unassign

Clear a ship's fleet dedication

### Synopsis

Clear a ship's fleet dedication, returning it to the general pool so any
coordinator's discovery can claim it again.

If the ship is mid-job for its fleet, the job finishes undisturbed — the
ship simply becomes generally claimable once its current claim is released.

Examples:
  spacetraders fleet unassign --ship TORWIND-19
  spacetraders fleet unassign --ship TORWIND-7 --agent TORWIND

```
spacetraders fleet unassign [flags]
```

### Options

```
  -h, --help          help for unassign
      --ship string   Ship symbol (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders fleet](spacetraders_fleet.md)	 - Manage dedicated fleets

