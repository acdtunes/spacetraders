## spacetraders construction stop

Stop the active construction pipeline for a site

### Synopsis

Stop the active construction pipeline for a construction site.

This command cancels the pipeline (so it stops spawning new tasks) and
cancels any not-yet-started tasks (PENDING/READY/ASSIGNED). Tasks already
EXECUTING are left to finish or fail naturally. Ships claimed by a
now-cancelled task are released so they re-enter fleet discovery.

Returns a clear error if there is no active construction pipeline for the
site (never started, or already stopped).

Examples:
  spacetraders construction stop X1-FB5-I61 --player-id 1

```
spacetraders construction stop <construction-site> [flags]
```

### Options

```
  -h, --help   help for stop
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

