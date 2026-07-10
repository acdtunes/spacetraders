## spacetraders ship navigate

Navigate a ship to a destination waypoint

### Synopsis

Navigate a ship to a destination waypoint within the same system.

The daemon will automatically:
- Orbit the ship if docked
- Plan the optimal route (including refuel stops if needed)
- Navigate to the destination
- Return a container ID for tracking progress

Examples:
  spacetraders ship navigate --ship AGENT-1 --destination X1-GZ7-B1 --player-id 1
  spacetraders ship navigate --ship SCOUT-2 --destination X1-GZ7-A1 --agent ENDURANCE

```
spacetraders ship navigate [flags]
```

### Options

```
      --destination string   Destination waypoint symbol (required)
  -h, --help                 help for navigate
      --ship string          Ship symbol to navigate (required)
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

