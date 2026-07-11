## spacetraders workflow trade-fleet-coordinator

Start the standing trade-fleet coordinator (keeps continuous tours alive on 'trade' hulls)

### Synopsis

Start the STANDING trade-fleet coordinator for a player (sp-1278) — the tour twin of
'contract start'. It reconciles the 'trade'-dedicated fleet every tick: any hull parked
by an honest tour exit (margins died in both systems, or a sold-out completion) is
relaunched into a fresh CONTINUOUS tour after a per-hull cooldown that lets the local
ground breathe (the rich->tapped->rich cycle). A hull mid-tour is never disturbed.

This retires the captain's hand-relaunch loop: launch it once and every trade hull keeps
touring on its own, re-adopted across daemon restarts.

Ownership: each tour claims its own hull under operation="trade" (the coordinator claims
nothing). Captain off-switches are respected for free — a captain-reserved hull is
skipped, and unpinning a hull from the 'trade' fleet removes it from the coordinator's
view with no restart.

Tuning is config-driven (config.yaml [trade_fleet], live on daemon restart):
  enabled                on/off (default on)
  cooldown_seconds       per-hull relaunch cooldown (default 180)
  max_concurrent_tours   cap on simultaneous tours (0 = unlimited, bounded by fleet size)
  tick_seconds           reconcile cadence (default 30)
  max_hops / max_spend / min_margin / replan_limit / working_capital_reserve
                         per-tour caps (0 = the tour's own default)

Examples:
  spacetraders workflow trade-fleet-coordinator --agent TORWIND
  spacetraders workflow trade-fleet-coordinator --player-id 1

```
spacetraders workflow trade-fleet-coordinator [flags]
```

### Options

```
  -h, --help   help for trade-fleet-coordinator
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders workflow](spacetraders_workflow.md)	 - Execute complex multi-step workflows

