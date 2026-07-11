## spacetraders tour

Multi-hop trade-tour tooling (sp-1ek0)

### Synopsis

Tooling for the multi-hop trade-tour program (spec: sp-1ek0) — the graduation
path from single-lane trading to chained A→B→C tours that keep a hull's cargo
hold working across several legs.

Currently exposes the "report" subcommand, which computes the A→B graduation
gate — completed-tour count and guard violations, tour realized $/hr versus the
trailing single-lane rate, and median plan-vs-realized unit-price error — over
a trailing window.

Examples:
  spacetraders tour report --agent TORWIND
  spacetraders tour report --since 72h

### Options

```
  -h, --help   help for tour
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
* [spacetraders tour report](spacetraders_tour_report.md)	 - Report the three A→B graduation-gate metrics from tour telemetry + ledger

