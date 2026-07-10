## spacetraders universe close

Close a universe era (destructive: truncates caches, blanks the dead token)

### Synopsis

Close the named era: stamp closed_at + final_credits, blank the dead player
token, truncate the market_data and system_graphs caches, and backfill
waypoints.era_id where NULL. Refuses unless --confirm echoes the era name.
Re-running on an already-closed era is an idempotent no-op. Player-scoped
history (transactions, contracts, ...) is never touched.

```
spacetraders universe close [flags]
```

### Options

```
      --confirm string   must echo the era name to confirm the destructive close
      --era string       era name to close
  -h, --help             help for close
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders universe](spacetraders_universe.md)	 - Universe era registry and reset operations

