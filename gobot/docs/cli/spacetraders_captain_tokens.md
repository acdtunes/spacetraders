## spacetraders captain tokens

Per-session token/usage telemetry (tokens/wake + tokens/day)

### Synopsis

Report the fleet's token spend over a recent window, aggregated per agent
session from the claude-code transcripts the gc-managed sessions produced.

Surfaces the two rates the surveyor ritual needs but has had no data source
for: tokens/day (fleet burn) and tokens/wake (the captain's per-activation
cost). Per-agent rows break the four usage components out separately (input,
output, cache-create, cache-read) so a cost model can price them. "Turns" is a
session's inbound prompt count — for the captain that is its wake count.
SINCE_SPAWN is that session's token total across its entire transcript, not
just this window (sp-0zx9) — the cost of skipping a rollover.

When captain.weekly_token_budget is configured, a quota block compares this
window's total tokens against that budget (sp-1vkr): a CONFIGURED proxy, not
a live Claude/Anthropic quota read. Most meaningful at the default --days 7.

This is additive read-only telemetry: it observes transcripts already written
by the externally-run sessions and never touches the wake path.

Examples:
  spacetraders captain tokens
  spacetraders captain tokens --days 1 --json

```
spacetraders captain tokens [flags]
```

### Options

```
      --days int   Window size in days (default 7)
  -h, --help       help for tokens
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

