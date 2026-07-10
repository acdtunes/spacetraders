## spacetraders shipyard

Manage shipyard operations

### Synopsis

Manage shipyard operations including listing available ships and purchasing ships.

Shipyards sell ships of various types. Use these commands to browse available ships
and purchase new vessels for your fleet.

Examples:
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1 --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --quantity 5 --budget 500000 --player-id 1

### Options

```
  -h, --help   help for shipyard
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
* [spacetraders shipyard list](spacetraders_shipyard_list.md)	 - List available ships at a shipyard
* [spacetraders shipyard purchase](spacetraders_shipyard_purchase.md)	 - Purchase ships from a shipyard

