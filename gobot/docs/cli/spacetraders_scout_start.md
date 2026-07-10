## spacetraders scout start

Start the standing scout-post coordinator

### Synopsis

Start the scout-post coordinator for a player. It reconciles the posts
table every tick — manning unmanned posts with idle satellites, respawning dead
tours, retiring completed sweep-once posts — and re-adopts its posts and
assignments after a daemon restart.

Examples:
  spacetraders scout start --agent ENDURANCE
  spacetraders scout start --player-id 1 --tick 30s

```
spacetraders scout start [flags]
```

### Options

```
  -h, --help            help for start
      --tick duration   Reconcile cadence (e.g. 30s); 0 uses the coordinator default
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders scout](spacetraders_scout.md)	 - Standing scout posts: keep systems' market data fresh

