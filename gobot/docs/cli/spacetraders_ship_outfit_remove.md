## spacetraders ship outfit remove

Remove an installed module from a ship (back into cargo)

### Synopsis

Remove an installed module from a ship. The module is placed back into the
ship's cargo.

Examples:
  spacetraders ship outfit remove --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE

```
spacetraders ship outfit remove [flags]
```

### Options

```
  -h, --help            help for remove
      --module string   Module symbol to remove (required)
      --ship string     Ship symbol to remove from (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ship outfit](spacetraders_ship_outfit.md)	 - Install, remove, or list ship modules

