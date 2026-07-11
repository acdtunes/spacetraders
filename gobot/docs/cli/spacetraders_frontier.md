## spacetraders frontier

Standing frontier expansion: auto-buy probes and seed frontier scouts

### Synopsis

Manage the standing frontier expansion coordinator (sp-8w89).

The coordinator closes the manual frontier loop: every tick it ranks the gate-
reachable, uncovered frontier (by known-market count, hop distance, and a virgin
bonus), declares the top system as a sweep-once scout post, and — when the probe
fleet is short of open coverage demand and every money guard passes (price <= 25%
of live treasury, fleet cap, spend cap, purchase cooldown) — buys one probe. The
bought probe lands undedicated in the pool; the scout-post reconciler and its jump
relays claim and move it. The coordinator itself moves and claims nothing, and it
re-derives every decision from persisted state, so it survives daemon restarts.

### Options

```
  -h, --help   help for frontier
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
* [spacetraders frontier start](spacetraders_frontier_start.md)	 - Start the standing frontier expansion coordinator

