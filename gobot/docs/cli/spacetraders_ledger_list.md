## spacetraders ledger list

List transactions

### Synopsis

List financial transactions with optional filtering.

Transactions can be filtered by date range, category, and type.
Results are ordered by timestamp descending (newest first) by default.

Categories:
  FUEL_COSTS        - Fuel expenses
  TRADING_REVENUE   - Income from selling cargo
  TRADING_COSTS     - Expenses from purchasing cargo
  SHIP_INVESTMENTS  - Expenses from purchasing ships
  CONTRACT_REVENUE  - Income from contracts

Transaction Types:
  REFUEL              - Ship refueling
  PURCHASE_CARGO      - Cargo purchase
  SELL_CARGO          - Cargo sale
  PURCHASE_SHIP       - Ship purchase
  CONTRACT_ACCEPTED   - Contract acceptance payment
  CONTRACT_FULFILLED  - Contract fulfillment payment

Examples:
  spacetraders ledger list --player-id 1 --limit 10
  spacetraders ledger list --category FUEL_COSTS
  spacetraders ledger list --start-date 2024-01-15 --end-date 2024-01-22

```
spacetraders ledger list [flags]
```

### Options

```
      --category string     Filter by category
      --end-date string     End date (YYYY-MM-DD)
  -h, --help                help for list
      --json                Output as JSON (full entry fields including good/ship/waypoint attribution)
      --limit int           Maximum number of transactions to return (default 50)
      --offset int          Number of transactions to skip
      --order-by string     Sort order (default "timestamp DESC")
      --start-date string   Start date (YYYY-MM-DD)
      --type string         Filter by transaction type
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ledger](spacetraders_ledger.md)	 - Financial ledger operations

