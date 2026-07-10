## spacetraders ledger report profit-loss

Generate profit & loss statement

### Synopsis

Generate a profit & loss (P&L) statement for a date range.

The P&L statement shows:
- Total revenue by category
- Total expenses by category
- Net profit (revenue - expenses)

Example:
  spacetraders ledger report profit-loss --player-id 1 \
    --start-date 2024-01-01 --end-date 2024-01-31

```
spacetraders ledger report profit-loss [flags]
```

### Options

```
      --end-date string     End date (YYYY-MM-DD) [required]
  -h, --help                help for profit-loss
      --start-date string   Start date (YYYY-MM-DD) [required]
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ledger report](spacetraders_ledger_report.md)	 - Generate financial reports

