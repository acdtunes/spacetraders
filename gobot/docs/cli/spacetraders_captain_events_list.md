## spacetraders captain events list

List unprocessed captain events for a player

### Synopsis

List the unprocessed strategic events queued for the captain.

Player is resolved from --player-id, --agent, or the persisted default (in
that order) — the same fallback chain "player info" and "ledger" use.

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --agent TORWIND
  spacetraders captain events list --agent TORWIND --json

```
spacetraders captain events list [flags]
```

### Options

```
  -h, --help   help for list
      --json   Output as JSON
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

