## spacetraders captain report

Engine telemetry from the captain event queue

### Synopsis

Report captain-engine telemetry over a recent window: event volume,
acknowledgement latency, unprocessed backlog, and per-type counts.

Player is resolved from --player-id, --agent, or the persisted default (in
that order) — the same fallback chain "captain events list" uses.

Examples:
  spacetraders captain report --player-id 1
  spacetraders captain report --agent TORWIND --days 14 --json

```
spacetraders captain report [flags]
```

### Options

```
      --days int   Window size in days (default 7)
  -h, --help       help for report
      --json       Output as JSON
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain](spacetraders_captain.md)	 - Autonomous captain operations

