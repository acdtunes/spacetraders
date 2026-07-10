## spacetraders fleet

Manage dedicated fleets

### Synopsis

Manage dedicated fleets — named groups of ships owned exclusively by one
operation's coordinator (contracts, bulk trade circuits, etc.).

A ship dedicated to a fleet is hidden from every other coordinator's
discovery and cannot be claimed by them; only the fleet's own coordinator
dispatches it. Dedication is persisted and survives daemon restarts.
Assigning a busy ship succeeds immediately but never interrupts its current
job — the new fleet takes over when the current claim is released.

Examples:
  spacetraders fleet assign --ship TORWIND-19 --fleet bulk_circuit
  spacetraders fleet unassign --ship TORWIND-19
  spacetraders fleet list

### Options

```
  -h, --help   help for fleet
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders](spacetraders.md)	 - SpaceTraders CLI - Interact with the SpaceTraders daemon
* [spacetraders fleet assign](spacetraders_fleet_assign.md)	 - Dedicate a ship to a named fleet
* [spacetraders fleet list](spacetraders_fleet_list.md)	 - List every dedicated fleet and its member ships
* [spacetraders fleet unassign](spacetraders_fleet_unassign.md)	 - Clear a ship's fleet dedication

