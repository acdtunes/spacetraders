## spacetraders market history

View price history for a market/good pair

### Synopsis

View historical price data for a specific market and good.

Shows purchase price, sell price, supply, activity, and trade volume over time.

Examples:
  spacetraders market history --waypoint X1-YZ19-D47 --good SHIP_PLATING --limit 20
  spacetraders market history --waypoint X1-YZ19-D47 --good IRON --window-hours 48

```
spacetraders market history [flags]
```

### Options

```
      --good string        Good symbol (required)
  -h, --help               help for history
      --limit int          Maximum number of records to show (default 20)
      --waypoint string    Waypoint symbol (required)
      --window-hours int   Time window in hours (0 = all time) (default 24)
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

