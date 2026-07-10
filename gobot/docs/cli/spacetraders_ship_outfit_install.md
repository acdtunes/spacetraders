## spacetraders ship outfit install

Install a module (from the ship's cargo) onto a ship

### Synopsis

Install a module onto a ship. The module must already be in the ship's cargo.

Examples:
  spacetraders ship outfit install --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE

```
spacetraders ship outfit install [flags]
```

### Options

```
  -h, --help            help for install
      --module string   Module symbol to install, e.g. MODULE_CARGO_HOLD_III (required)
      --ship string     Ship symbol to install onto (required)
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

