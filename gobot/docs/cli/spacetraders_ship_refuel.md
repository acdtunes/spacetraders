## spacetraders ship refuel

Refuel a ship at its current location

### Synopsis

Refuel a ship at its current location.
Ship must be docked at a waypoint with fuel available.

Examples:
  spacetraders ship refuel --ship AGENT-1 --player-id 1
  spacetraders ship refuel --ship AGENT-1 --units 100 --player-id 1
  spacetraders ship refuel --ship ENDURANCE-1 --agent ENDURANCE

```
spacetraders ship refuel [flags]
```

### Options

```
  -h, --help          help for refuel
      --ship string   Ship symbol to refuel (required)
      --units int     Specific fuel units to purchase (omit for full tank)
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

