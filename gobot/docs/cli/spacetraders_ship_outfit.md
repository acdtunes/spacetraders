## spacetraders ship outfit

Install, remove, or list ship modules

### Synopsis

Install, remove, or list ship modules (e.g. MODULE_CARGO_HOLD_III).

Installing a module requires it to already be in the ship's cargo (buy it as a
good at a shipyard first). The daemon atomically claims the hull, gates the
shipyard modification fee on the working-capital reserve, docks, installs, and
persists the ship's new cargo capacity.

Examples:
  spacetraders ship outfit install --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE
  spacetraders ship outfit remove  --ship ENDURANCE-1 --module MODULE_CARGO_HOLD_III --agent ENDURANCE
  spacetraders ship outfit list    --ship ENDURANCE-1 --agent ENDURANCE

### Options

```
  -h, --help   help for outfit
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
* [spacetraders ship outfit install](spacetraders_ship_outfit_install.md)	 - Install a module (from the ship's cargo) onto a ship
* [spacetraders ship outfit list](spacetraders_ship_outfit_list.md)	 - List the modules installed on a ship
* [spacetraders ship outfit remove](spacetraders_ship_outfit_remove.md)	 - Remove an installed module from a ship (back into cargo)

