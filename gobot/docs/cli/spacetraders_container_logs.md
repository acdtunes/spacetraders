## spacetraders container logs

Get logs from a container

### Synopsis

Retrieve logs for a specific container from the database.

Both --limit and --tail fetch the N most recent entries (query is ORDER BY
timestamp DESC LIMIT N) and print them oldest-first/newest-last, matching
tail(1) — the newest line is always the last one printed. --tail and --limit
are mutually exclusive; if both are given, --tail wins.

Examples:
  spacetraders container logs navigate-SCOUT-1-1234567890
  spacetraders container logs navigate-SCOUT-1-1234567890 --tail 50
  spacetraders container logs navigate-SCOUT-1-1234567890 --limit 50
  spacetraders container logs navigate-SCOUT-1-1234567890 --level ERROR

```
spacetraders container logs <container-id> [flags]
```

### Options

```
  -h, --help           help for logs
      --level string   Filter by log level (INFO, WARNING, ERROR, DEBUG)
      --limit int      Maximum number of log entries (newest N) (default 100)
      --tail int       Show only the last N log entries (newest N); overrides --limit if both are set
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders container](spacetraders_container.md)	 - Manage background containers

