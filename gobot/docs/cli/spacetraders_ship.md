## spacetraders ship

Manage ships

### Synopsis

Manage ships and view ship information.

Ships are your vessels in the SpaceTraders universe. Use these commands to
view your fleet, check ship details, monitor status, and perform ship operations.

Examples:
  spacetraders ship list --agent ENDURANCE
  spacetraders ship info --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship navigate --ship ENDURANCE-1 --destination X1-GZ7-B1 --agent ENDURANCE
  spacetraders ship dock --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship orbit --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship refuel --ship ENDURANCE-1 --agent ENDURANCE

### Options

```
  -h, --help   help for ship
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
* [spacetraders ship buy](spacetraders_ship_buy.md)	 - Buy cargo for a ship
* [spacetraders ship dock](spacetraders_ship_dock.md)	 - Dock a ship at its current location
* [spacetraders ship info](spacetraders_ship_info.md)	 - Show detailed ship information
* [spacetraders ship jettison](spacetraders_ship_jettison.md)	 - Jettison cargo from a ship into space
* [spacetraders ship jump](spacetraders_ship_jump.md)	 - Jump a ship to a different star system via jump gate
* [spacetraders ship list](spacetraders_ship_list.md)	 - List all ships for a player
* [spacetraders ship navigate](spacetraders_ship_navigate.md)	 - Navigate a ship to a destination waypoint
* [spacetraders ship orbit](spacetraders_ship_orbit.md)	 - Put a ship into orbit from docked position
* [spacetraders ship outfit](spacetraders_ship_outfit.md)	 - Install, remove, or list ship modules
* [spacetraders ship refresh](spacetraders_ship_refresh.md)	 - Force-resync a ship's cached state from the server
* [spacetraders ship refuel](spacetraders_ship_refuel.md)	 - Refuel a ship at its current location
* [spacetraders ship release](spacetraders_ship_release.md)	 - Clear a captain reservation on a ship
* [spacetraders ship reserve](spacetraders_ship_reserve.md)	 - Reserve a ship for the captain's direct manual use
* [spacetraders ship reserve-cargo](spacetraders_ship_reserve-cargo.md)	 - Mark a cargo good as do-not-sell on a ship
* [spacetraders ship reserved-cargo](spacetraders_ship_reserved-cargo.md)	 - Show a ship's cargo do-not-sell reservations
* [spacetraders ship sell](spacetraders_ship_sell.md)	 - Sell cargo from a ship
* [spacetraders ship unreserve-cargo](spacetraders_ship_unreserve-cargo.md)	 - Release a reserved cargo good for sale on a ship

