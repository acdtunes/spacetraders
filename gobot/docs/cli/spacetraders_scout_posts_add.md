## spacetraders scout posts add

Add or update a scout post for a system

### Synopsis

Add (or update) a desired-state scout post for a system. The coordinator
mans it with the nearest idle satellite on its next tick. Re-adding an existing
post updates its freshness/kind without evicting the hull already manning it.

--kind standing (default) keeps the system fresh forever; --kind sweep-once runs
a single tour then auto-removes the post, freeing its hull for the next one — the
shape the captain seeds frontier-census systems with.

Examples:
  spacetraders scout posts add X1-GZ7 --agent ENDURANCE
  spacetraders scout posts add X1-JP61 --freshness 45m --agent ENDURANCE
  spacetraders scout posts add X1-KA42 --kind sweep-once --agent ENDURANCE

```
spacetraders scout posts add <SYSTEM> [flags]
```

### Options

```
      --freshness duration   Target market-scan freshness (e.g. 60m) (default 1h0m0s)
  -h, --help                 help for add
      --kind string          Post kind: standing or sweep-once (default "standing")
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders scout posts](spacetraders_scout_posts.md)	 - Manage desired-state scout posts

