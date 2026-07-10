## spacetraders waypoint

Discover waypoints in a system

### Synopsis

List and inspect waypoints from the daemon's waypoint cache.

Unlike the market cache (which only holds physically-visited MARKETPLACE
waypoints), these commands surface every waypoint in a system - including the
JUMP_GATE - syncing from the SpaceTraders API when the cache is empty or stale.

Examples:
  spacetraders waypoint list --system X1-PZ28 --agent ENDURANCE
  spacetraders waypoint list --system X1-PZ28 --type JUMP_GATE
  spacetraders waypoint list --system X1-PZ28 --trait SHIPYARD
  spacetraders waypoint get --waypoint X1-PZ28-I55 --agent ENDURANCE

### Options

```
  -h, --help   help for waypoint
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
* [spacetraders waypoint get](spacetraders_waypoint_get.md)	 - Show detailed information about a waypoint
* [spacetraders waypoint list](spacetraders_waypoint_list.md)	 - List the waypoints of a system

