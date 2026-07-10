## spacetraders market

View market data

### Synopsis

Query cached market data for waypoints and systems.

Markets show trade goods with supply, activity, purchase prices, sell prices,
and trade volumes. Use these commands to find trading opportunities.

Examples:
  spacetraders market get --waypoint X1-GZ7-B2 --agent ENDURANCE
  spacetraders market list --system X1-GZ7 --agent ENDURANCE

### Options

```
  -h, --help   help for market
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
* [spacetraders market find](spacetraders_market_find.md)	 - Find every cached market trading a good
* [spacetraders market get](spacetraders_market_get.md)	 - Get market data for a waypoint
* [spacetraders market history](spacetraders_market_history.md)	 - View price history for a market/good pair
* [spacetraders market list](spacetraders_market_list.md)	 - List markets in a system
* [spacetraders market spreads](spacetraders_market_spreads.md)	 - Rank pure-arbitrage lanes in a system from cached markets
* [spacetraders market volatility](spacetraders_market_volatility.md)	 - Analyze market price volatility

