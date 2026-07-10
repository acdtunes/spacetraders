## spacetraders goods produce

Produce a good using automated supply chain fabrication

### Synopsis

Produce a good using automated supply chain fabrication.

The goods factory will:
- Build a complete dependency tree to raw materials
- Identify what can be bought vs what must be fabricated
- Use idle hauler ships to execute production
- Acquire whatever quantity is available at markets
- Poll for production completion at manufacturing waypoints

The factory operates with a market-driven model - it acquires whatever quantity
is available rather than producing fixed amounts.

Examples:
  spacetraders goods produce ADVANCED_CIRCUITRY --system X1-GZ7 --player-id 1
  spacetraders goods produce MACHINERY --system X1-GZ7 --agent ENDURANCE

```
spacetraders goods produce <good> [flags]
```

### Options

```
  -h, --help             help for produce
      --inputs-only      Construction-support mode: feed the dependency tree but do NOT harvest the fabricated output — leave it in factory stock for a construction pipeline to source
      --iterations int   Number of production iterations (-1 for infinite, 0 or 1 for single run, >1 for specific count) (default 1)
      --system string    System symbol where production will occur (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders goods](spacetraders_goods.md)	 - Manage automated goods production

