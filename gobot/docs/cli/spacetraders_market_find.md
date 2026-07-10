## spacetraders market find

Find every cached market trading a good

### Synopsis

Find every cached market known to trade a good, across a system or all
known systems, sorted by best price for the requested side.

Always shows data age per market (staleness is never hidden - a stale
availability premise can flip an entire plan).

Examples:
  spacetraders market find --good IRON_ORE --player-id 1
  spacetraders market find --good IRON_ORE --system X1-GZ7 --side sell --agent ENDURANCE
  spacetraders market find --good IRON_ORE --player-id 1 --json

```
spacetraders market find [flags]
```

### Options

```
      --good string     Good symbol to search for (required)
  -h, --help            help for find
      --json            Output as JSON
      --side string     Sort for best price on this side: buy, sell, or any (default "any")
      --system string   Restrict search to this system (default: all systems)
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

