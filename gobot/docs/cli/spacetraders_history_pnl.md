## spacetraders history pnl

Era P&L rollup from era-scoped transactions

### Synopsis

Net by category or operation type, plus the daily ramp curve when a single era is given.

```
spacetraders history pnl [flags]
```

### Options

```
      --by-category    Group by transaction category (default)
      --by-operation   Group by operation type
      --era string     Era ID (default: all eras)
  -h, --help           help for pnl
      --json           Output as JSON
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders history](spacetraders_history.md)	 - Cross-era priors: query history across universe resets

