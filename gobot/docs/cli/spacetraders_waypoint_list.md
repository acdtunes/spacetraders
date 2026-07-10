## spacetraders waypoint list

List the waypoints of a system

### Synopsis

List the waypoints of a system from the daemon's waypoint cache.

Shows each waypoint's symbol, type, coordinates, and traits. Optionally filter
by trait (e.g. SHIPYARD, MARKETPLACE) or type (e.g. JUMP_GATE). The system is
synced from the API when the cache is empty or stale.

Examples:
  spacetraders waypoint list --system X1-PZ28 --agent ENDURANCE
  spacetraders waypoint list --system X1-PZ28 --type JUMP_GATE
  spacetraders waypoint list --system X1-PZ28 --trait SHIPYARD --player-id 1

```
spacetraders waypoint list [flags]
```

### Options

```
  -h, --help            help for list
      --system string   System symbol (required)
      --trait string    Filter by trait (e.g. SHIPYARD, MARKETPLACE)
      --type string     Filter by type (e.g. JUMP_GATE)
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

