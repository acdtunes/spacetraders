## spacetraders history

Cross-era priors: query history across universe resets

### Synopsis

Read-only queries over the live tables, scoped through the eras registry.

History and live data share the same tables (rev 2, in-place player-partitioned
history), so these are ordinary era-scoped reads. Pattern queries default to
--era all; 'history summary' defaults to the latest CLOSED era.

Examples:
  spacetraders history eras
  spacetraders history goods --good ADVANCED_CIRCUITRY --era 1
  spacetraders history summary

### Options

```
  -h, --help   help for history
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
* [spacetraders history contracts](spacetraders_history_contracts.md)	 - Per-era contract economics
* [spacetraders history eras](spacetraders_history_eras.md)	 - List the era registry
* [spacetraders history events](spacetraders_history_events.md)	 - captain_events frequency and timing across eras
* [spacetraders history goods](spacetraders_history_goods.md)	 - Per-era supply/price/volatility priors for a good
* [spacetraders history manufacturing](spacetraders_history_manufacturing.md)	 - Per-product-good pipeline outcomes across eras
* [spacetraders history pnl](spacetraders_history_pnl.md)	 - Era P&L rollup from era-scoped transactions
* [spacetraders history summary](spacetraders_history_summary.md)	 - The cold-start brief for one era

