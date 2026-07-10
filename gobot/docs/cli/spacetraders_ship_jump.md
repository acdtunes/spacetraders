## spacetraders ship jump

Jump a ship to a different star system via jump gate

### Synopsis

Jump a ship to a different star system using a jump gate.

If the ship is not currently at a jump gate, it will automatically navigate to
the nearest jump gate in the current system before jumping.

The ship must have a jump drive module installed to use this command.

Examples:
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --player-id 1
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --agent ENDURANCE

```
spacetraders ship jump [flags]
```

### Options

```
  -h, --help            help for jump
      --ship string     Ship symbol to jump (required)
      --system string   Destination system symbol (e.g., X1-ALPHA) (required)
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

