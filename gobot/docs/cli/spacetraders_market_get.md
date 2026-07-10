## spacetraders market get

Get market data for a waypoint

### Synopsis

Query cached market data for a specific waypoint.

Shows trade goods with supply, activity, purchase price, sell price, and volume.

Examples:
  spacetraders market get --waypoint X1-TEST-A1 --player-id 1
  spacetraders market get --waypoint X1-GZ7-B2 --agent ENDURANCE

```
spacetraders market get [flags]
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

* [spacetraders market](spacetraders_market.md)	 - View market data

