## spacetraders ship jettison

Jettison cargo from a ship into space

### Synopsis

Jettison cargo from a ship, permanently discarding it.

Use this to dispose of stranded or unsellable cargo (e.g. bait/leftover units
blocking a hull) when no reachable market buys the good — the last resort
when a direct sell isn't possible. The ship is automatically moved to orbit
first if it is currently docked, since jettisoning requires orbit.

Examples:
  spacetraders ship jettison --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship jettison --ship ENDURANCE-1 --good GAS --units 12 --agent ENDURANCE

```
spacetraders ship jettison [flags]
```

### Options

```
      --good string   Trade good symbol to jettison (required)
  -h, --help          help for jettison
      --ship string   Ship symbol to jettison cargo from (required)
      --units int     Number of units to jettison (required)
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

