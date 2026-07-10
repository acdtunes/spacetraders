## spacetraders shipyard list

List available ships at a shipyard

### Synopsis

List available ships at a shipyard waypoint.

Shows ship types, names, descriptions, and purchase prices for all ships
available at the specified shipyard.

Examples:
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1 --player-id 1
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1 --agent ENDURANCE

```
spacetraders shipyard list <system-symbol> <waypoint-symbol> [flags]
```

### Options

```
  -h, --help   help for list
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

