## spacetraders shipyard purchase

Purchase ships from a shipyard

### Synopsis

Purchase one or more ships from a shipyard.

The command will purchase ships within the following constraints:
- Quantity requested (default: 1)
- Maximum budget allocated (if specified, 0 = no limit)
- Player's available credits

The purchasing ship will:
1. Auto-discover nearest shipyard that sells the desired ship type (if not specified)
2. Navigate to the shipyard waypoint if not already there
3. Dock if in orbit
4. Purchase the specified ship(s)

The operation runs in a background container that can be monitored.

Examples:
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --quantity 5 --budget 500000 --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_MINING_DRONE --quantity 10 --waypoint X1-GZ7-A1 --player-id 1

```
spacetraders shipyard purchase [flags]
```

### Options

```
      --budget int        Maximum budget in credits (0 = no limit, default: 0)
  -h, --help              help for purchase
      --quantity int      Number of ships to purchase (default: 1) (default 1)
      --ship string       Ship symbol to use for navigation (required)
      --type string       Ship type to purchase (e.g., SHIP_PROBE, SHIP_MINING_DRONE) (required)
      --waypoint string   Shipyard waypoint (optional - will auto-discover if not provided)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders shipyard](spacetraders_shipyard.md)	 - Manage shipyard operations

