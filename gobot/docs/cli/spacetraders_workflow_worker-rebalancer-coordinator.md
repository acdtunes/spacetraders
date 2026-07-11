## spacetraders workflow worker-rebalancer-coordinator

Start the standing worker-rebalancer coordinator (ferries idle lights to worker-starved factory systems)

### Synopsis

Start the STANDING worker-rebalancer coordinator for a player (sp-f5pr). It reconciles
every tick: it finds worker-starved factory systems (a factory past its warm-up window with
no in-system idle light and fewer lights than factory chains) and ferries the nearest idle
undedicated light-hauler from a source system that can spare one — then reclaims the hull on
arrival so the destination factory mans it in-system.

Launch it once and worker starvation self-heals across daemon restarts; the coordinator
holds no in-memory state (every clock/cap is derived from ship + container rows).

Ownership: each ferry claims its own hull under operation="worker_ferry" (occupancy, not a
dedication — the coordinator claims nothing directly, and never poaches a pinned or
captain-reserved hull). Guards fail closed: any unreadable state ⇒ no ferry that tick.

Tuning is config-driven (config.yaml [worker_rebalancer], live on daemon restart):
  enabled                  on/off (default on)
  tick_seconds             reconcile cadence (default 60)
  vacancy_min_minutes      factory warm-up before a system counts as starved (default 15)
  source_min_idle          idle lights a source must hold to donate one (default 2)
  ferry_cooldown_seconds   per-system suppress window after a ferry (default 600)
  max_concurrent_ferries   cap on simultaneous ferries (default 2)
  max_lights_per_system    per-system light cap incl. in-flight (default 0 = uncapped)

Examples:
  spacetraders workflow worker-rebalancer-coordinator --agent TORWIND --dry-run
  spacetraders workflow worker-rebalancer-coordinator --agent TORWIND
  spacetraders workflow worker-rebalancer-coordinator --player-id 1

```
spacetraders workflow worker-rebalancer-coordinator [flags]
```

### Options

```
      --dry-run   Decide and log the ferry it would dispatch, but ferry nothing
  -h, --help      help for worker-rebalancer-coordinator
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

