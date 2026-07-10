## spacetraders tour report

Report the three A→B graduation-gate metrics from tour telemetry + ledger

### Synopsis

Compute the multi-hop trade-tour graduation gate (sp-1ek0) over a trailing window:
  1. completed tours and guard violations (FAILED tour_run containers);
  2. tour realized $/hr vs the trailing single-lane $/hr;
  3. median plan-vs-realized unit-price error %.
The gate passes at 10 tours, >=1.5x single-lane $/hr, and <=15% median price error.

```
spacetraders tour report [flags]
```

### Options

```
  -h, --help             help for report
      --since duration   Trailing window to measure (default 168h = 7 days) (default 168h0m0s)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders tour](spacetraders_tour.md)	 - Multi-hop trade-tour tooling (sp-1ek0)

