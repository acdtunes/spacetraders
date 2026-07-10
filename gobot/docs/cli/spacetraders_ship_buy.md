## spacetraders ship buy

Buy cargo for a ship

### Synopsis

Buy cargo for a ship from the market at its current location.
Ship must be docked at a marketplace.

Examples:
  spacetraders ship buy --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship buy --ship ENDURANCE-1 --good IRON_ORE --units 100 --agent ENDURANCE

```
spacetraders ship buy [flags]
```

### Options

```
      --good string   Trade good symbol to buy (required)
  -h, --help          help for buy
      --ship string   Ship symbol to buy for (required)
      --units int     Number of units to buy (required)
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

