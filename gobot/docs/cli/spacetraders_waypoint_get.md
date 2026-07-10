## spacetraders waypoint get

Show detailed information about a waypoint

### Synopsis

Show detailed information about a single waypoint.

Displays the waypoint's system, type, coordinates, traits, and orbitals. The
waypoint is auto-fetched from the API when it is not cached.

Examples:
  spacetraders waypoint get --waypoint X1-PZ28-I55 --agent ENDURANCE
  spacetraders waypoint get --waypoint X1-PZ28-I55 --player-id 1

```
spacetraders waypoint get [flags]
```

### Options

```
  -h, --help              help for get
      --waypoint string   Waypoint symbol (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders waypoint](spacetraders_waypoint.md)	 - Discover waypoints in a system

