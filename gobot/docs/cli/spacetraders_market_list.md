## spacetraders market list

List markets in a system

### Synopsis

Query all cached market data for a system with optional age filtering.

Shows waypoint symbols, number of goods available, and last update timestamp.

Examples:
  spacetraders market list --system X1-TEST --player-id 1
  spacetraders market list --system X1-GZ7 --max-age-minutes 60 --agent ENDURANCE

```
spacetraders market list [flags]
```

### Options

```
  -h, --help                  help for list
      --max-age-minutes int   Only show markets updated within this many minutes (0 = all)
      --system string         System symbol (required)
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

