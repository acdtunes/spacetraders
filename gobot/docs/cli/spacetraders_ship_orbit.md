## spacetraders ship orbit

Put a ship into orbit from docked position

### Synopsis

Put a ship into orbit from its current docked position.
Ship must be docked to orbit.

Examples:
  spacetraders ship orbit --ship AGENT-1 --player-id 1
  spacetraders ship orbit --ship ENDURANCE-1 --agent ENDURANCE

```
spacetraders ship orbit [flags]
```

### Options

```
  -h, --help          help for orbit
      --ship string   Ship symbol to orbit (required)
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

