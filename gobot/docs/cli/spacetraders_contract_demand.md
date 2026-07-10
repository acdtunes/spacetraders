## spacetraders contract demand

Recurring contract demand joined to cheapest foreign markets (pre-positioning candidates)

### Synopsis

Mine contract history for the goods a HOME system's contracts repeatedly need,
join each to the cheapest FOREIGN market that sells it and (when the home system
sells it) the home ask, and rank the pre-positioning candidates by projected savings.

Read-only: no spending, no dispatch. The home system is an explicit --system flag —
there is no global "home" anchor. Goods with no reachable foreign source are dropped;
goods the home system does not sell are shown but flagged not stock-eligible.

Examples:
  spacetraders contract demand --system X1-KA42
  spacetraders contract demand --system X1-KA42 --min-recurrence 3 --top 10
  spacetraders contract demand --system X1-KA42 --json

```
spacetraders contract demand --system <SYSTEM> [flags]
```

### Options

```
      --era string           Era ID (default: all eras; the home-system filter already confines to the current universe)
  -h, --help                 help for demand
      --json                 Output as JSON
      --min-recurrence int   Minimum distinct contracts demanding a good before it counts as recurring (default 2)
      --system string        Home system to pre-position for, e.g. X1-KA42 [required]
      --top int              Cap on ranked candidate rows (default 20)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders contract](spacetraders_contract.md)	 - Manage contract operations

