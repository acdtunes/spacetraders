## spacetraders market spreads

Rank pure-arbitrage lanes in a system from cached markets

### Synopsis

Rank standing buy-export / sell-import spreads across every cached market in
a system, entirely from cache (no live API calls).

For each good it finds the best source (where you BUY, paying the market's SELL
price / ask) and destination (where you SELL, receiving the market's BUY price /
bid), then ranks lanes by volume-capped spread: (dest bid - source ask) x the
minimum tradable volume. Volume-capping matters because a fat per-unit spread on
a thin market is worth less than a modest spread on a deep one.

The CLEARS FLOOR column flags whether each lane's per-unit spread clears the
bid-floor discipline the trade-route executor enforces. The scan still ranks by
capped spread (so you see every standing spread), but trade-route flies the
highest-ranked lane whose CLEARS FLOOR = yes - a deeper capped lane that is
sub-floor is refused, not flown.

Examples:
  spacetraders market spreads --system X1-GZ7 --agent ENDURANCE
  spacetraders market spreads --system X1-GZ7 --top 10 --player-id 1
  spacetraders market spreads --system X1-GZ7 --json --agent ENDURANCE

```
spacetraders market spreads [flags]
```

### Options

```
  -h, --help            help for spreads
      --json            Output as JSON
      --system string   System symbol to scan (required)
      --top int         Show only the top N lanes (0 = all)
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

