## spacetraders container list

List all containers

### Synopsis

List all background containers with their status.

By default, only active containers (RUNNING, INTERRUPTED) are shown.
Use --show-all to see containers in all states including completed and failed.

```
spacetraders container list [flags]
```

### Options

```
  -h, --help            help for list
      --show-all        Show containers in all states (default: only RUNNING and INTERRUPTED)
      --status string   Filter by status (RUNNING, COMPLETED, FAILED, etc.) or comma-separated list
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

