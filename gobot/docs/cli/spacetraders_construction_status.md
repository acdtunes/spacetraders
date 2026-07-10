## spacetraders construction status

Show status of a construction site and any active pipeline

### Synopsis

Show status of a construction site and any active pipeline.

This command shows:
- Construction site completion status
- Required materials and their delivery progress
- Active pipeline status (if any)

Examples:
  spacetraders construction status X1-FB5-I61 --player-id 1

```
spacetraders construction status <construction-site> [flags]
```

### Options

```
  -h, --help   help for status
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders construction](spacetraders_construction.md)	 - Manage construction site supply operations

