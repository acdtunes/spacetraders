## spacetraders market volatility

Analyze market price volatility

### Synopsis

Analyze price volatility for goods across all markets.

Shows volatility metrics including mean price, standard deviation, max price change percentage,
and change frequency. Can show specific good or top N most volatile goods.

Examples:
  spacetraders market volatility --good SHIP_PLATING --window-hours 24
  spacetraders market volatility --top 10 --window-hours 48

```
spacetraders market volatility [flags]
```

### Options

```
      --good string        Good symbol to analyze (e.g., SHIP_PLATING)
  -h, --help               help for volatility
      --top int            Number of most volatile goods to show (when --good not specified) (default 10)
      --window-hours int   Time window in hours for analysis (default 24)
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

