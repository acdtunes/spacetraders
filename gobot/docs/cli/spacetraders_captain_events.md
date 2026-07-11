## spacetraders captain events

List and acknowledge captain events

### Synopsis

Inspect and drain the strategic-event queue the autonomous captain reads
during its wake ritual. "events list" shows the unprocessed events queued for
a player; "events ack" marks them processed — by explicit IDs, or in bulk with
--all/--before — so they do not resurface on the next wake.

Player is resolved from --player-id, --agent, or the persisted default (in
that order), the same fallback chain the rest of the CLI uses.

Examples:
  spacetraders captain events list --agent TORWIND
  spacetraders captain events ack --agent TORWIND --all

### Options

```
  -h, --help   help for events
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
* [spacetraders captain events ack](spacetraders_captain_events_ack.md)	 - Acknowledge captain events by ID, or in bulk with --all/--before
* [spacetraders captain events list](spacetraders_captain_events_list.md)	 - List unprocessed captain events for a player

