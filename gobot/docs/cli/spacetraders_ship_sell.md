## spacetraders ship sell

Sell cargo from a ship

### Synopsis

Sell cargo from a ship at its current location.
Ship must be docked at a marketplace.

Examples:
  spacetraders ship sell --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship sell --ship ENDURANCE-1 --good IRON_ORE --units 100 --agent ENDURANCE

```
spacetraders ship sell [flags]
```

### Options

```
      --good string   Trade good symbol to sell (required)
  -h, --help          help for sell
      --ship string   Ship symbol to sell from (required)
      --units int     Number of units to sell (required)
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

