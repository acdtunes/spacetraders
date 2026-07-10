## spacetraders captain

Autonomous captain operations

### Synopsis

Inspect and acknowledge the strategic-event queue the autonomous
captain consumes during its wake ritual.

Player is resolved the same way everywhere: --player-id, or --agent (which
survives across era resets, unlike --player-id), or the persisted default.

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --agent TORWIND --json
  spacetraders captain events ack --player-id 1 --ids 12,13,14
  spacetraders captain events ack --agent TORWIND --all

### Options

```
  -h, --help   help for captain
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders](spacetraders.md)	 - SpaceTraders CLI - Interact with the SpaceTraders daemon
* [spacetraders captain events](spacetraders_captain_events.md)	 - List and acknowledge captain events
* [spacetraders captain regime](spacetraders_captain_regime.md)	 - Inspect or declare the captain's price-regime tripwires
* [spacetraders captain report](spacetraders_captain_report.md)	 - Engine telemetry from the captain event queue
* [spacetraders captain tokens](spacetraders_captain_tokens.md)	 - Per-session token/usage telemetry (tokens/wake + tokens/day)
* [spacetraders captain wake](spacetraders_captain_wake.md)	 - Inspect or declare the captain's wake policy

