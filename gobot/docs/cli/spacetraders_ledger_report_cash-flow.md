## spacetraders ledger report cash-flow

Generate cash flow statement

### Synopsis

Generate a cash flow statement grouped by category.

The cash flow statement shows:
- Total inflow (income) by category
- Total outflow (expenses) by category
- Net cash flow by category
- Number of transactions per category

Example:
  spacetraders ledger report cash-flow --player-id 1 \
    --start-date 2024-01-15 --end-date 2024-01-22

```
spacetraders ledger report cash-flow [flags]
```

### Options

```
      --end-date string     End date (YYYY-MM-DD) [required]
      --group-by string     Group by (category) (default "category")
  -h, --help                help for cash-flow
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

