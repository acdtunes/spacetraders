## spacetraders captain events ack

Acknowledge captain events by ID, or in bulk with --all/--before

### Synopsis

Mark captain events processed, either by explicit IDs or in bulk.

Exactly one of --ids, --all, or --before is required. --all and --before
resolve the player from --player-id, --agent, or the persisted default (in
that order), same as "captain events list".

Examples:
  spacetraders captain events ack --player-id 1 --ids 12,13,14
  spacetraders captain events ack --agent TORWIND --all
  spacetraders captain events ack --agent TORWIND --before 2026-07-08T00:00:00Z

```
spacetraders captain events ack [flags]
```

### Options

```
      --all             Acknowledge every pending event for the resolved player
      --before string   Acknowledge pending events created before this RFC3339 timestamp
  -h, --help            help for ack
      --ids string      Comma-separated event IDs to acknowledge
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain events](spacetraders_captain_events.md)	 - List and acknowledge captain events

