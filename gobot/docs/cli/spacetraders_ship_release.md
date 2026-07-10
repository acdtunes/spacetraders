## spacetraders ship release

Clear a captain reservation on a ship

### Synopsis

Clear a captain reservation, returning the ship to idle so normal
coordinator discovery (mfg, contracts, scouting, trade routes, etc.) can
claim it again.

Examples:
  spacetraders ship release --ship ENDURANCE-1
  spacetraders ship release --ship ENDURANCE-1 --reason "errand complete"

```
spacetraders ship release [flags]
```

### Options

```
  -h, --help            help for release
      --reason string   Free-text release reason (optional)
      --ship string     Ship symbol (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ship](spacetraders_ship.md)	 - Manage ships

