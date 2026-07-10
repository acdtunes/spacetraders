## spacetraders ledger report

Generate financial reports

### Synopsis

Generate profit & loss and cash flow reports.

Reports analyze transactions over a specified date range to provide
financial insights including revenue, expenses, and net profit.

Examples:
  spacetraders ledger report profit-loss --start-date 2024-01-01 --end-date 2024-01-31
  spacetraders ledger report cash-flow --start-date 2024-01-15 --end-date 2024-01-22

### Options

```
  -h, --help   help for report
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
* [spacetraders ledger report cash-flow](spacetraders_ledger_report_cash-flow.md)	 - Generate cash flow statement
* [spacetraders ledger report profit-loss](spacetraders_ledger_report_profit-loss.md)	 - Generate profit & loss statement

