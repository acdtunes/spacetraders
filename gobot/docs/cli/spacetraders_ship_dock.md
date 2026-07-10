## spacetraders ship dock

Dock a ship at its current location

### Synopsis

Dock a ship at its current location.
Ship must be in orbit to dock.

Examples:
  spacetraders ship dock --ship AGENT-1 --player-id 1
  spacetraders ship dock --ship ENDURANCE-1 --agent ENDURANCE

```
spacetraders ship dock [flags]
```

### Options

```
  -h, --help          help for dock
      --ship string   Ship symbol to dock (required)
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

