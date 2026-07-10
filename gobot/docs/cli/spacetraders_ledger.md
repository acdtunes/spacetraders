## spacetraders ledger

Financial ledger operations

### Synopsis

View and analyze financial transactions.

The ledger tracks all credit-affecting operations including fuel costs,
cargo trading, ship purchases, and contract payments. Use these commands
to view transaction history and generate financial reports.

Examples:
  spacetraders ledger list --player-id 1
  spacetraders ledger list --category FUEL_COSTS --limit 20
  spacetraders ledger report profit-loss --start-date 2024-01-01 --end-date 2024-01-31
  spacetraders ledger report cash-flow --start-date 2024-01-15 --end-date 2024-01-22

### Options

```
  -h, --help   help for ledger
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
* [spacetraders ledger list](spacetraders_ledger_list.md)	 - List transactions
* [spacetraders ledger report](spacetraders_ledger_report.md)	 - Generate financial reports

