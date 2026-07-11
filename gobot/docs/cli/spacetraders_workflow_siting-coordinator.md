## spacetraders workflow siting-coordinator

Start the standing factory-siting coordinator (automates factory discovery, placement, and capacity planning)

### Synopsis

Start the STANDING factory-siting coordinator for a player (sp-vdld) — the factory twin
of 'trade-fleet-coordinator'. It is the standing "brain" that automates factory discovery,
placement, and capacity planning, retiring the captain's manual expansion sweeps.

Each slow tick (default 15min) it:
  SCAN     enumerate candidate (good, system) factory sites that pass the export-site hard gate
           (a factory is map-fixed — you cannot manufacture where a good only IMPORTs/EXCHANGEs),
           in-system input eligibility (supply-first), and market-data freshness.
  SCORE    branchPL projection x tour-alignment - input-competition - staleness.
  MAINTAIN pick the top-K portfolio (K = floor(haulers / workers_per_chain)), subject to
           per-system and per-input-market concentration caps.
  ACT      launch missing top-K chains THROUGH the guard stack (each launched goods_factory
           coordinator runs 2dv4 + a5j7 + C2 + r5a6 on its own passes — guards veto at zero
           cost, never bypassed); retire chains that fall out of top-K via a clean stop, with
           hysteresis to prevent thrash.
  EMIT     post scout-demand for stale-but-promising sites so coverage refreshes them.

It is LIVE BY DEFAULT: launched here it is ACTIVE immediately (no enablement flip). Set
[manufacturing.siting] siting_disabled=true in config.yaml to stand the whole brain down.

Ownership: it claims nothing itself — each goods_factory chain it launches claims its own
hulls through the existing factory path. It composes with (never duplicates) the per-chain
guards: it drives portfolio membership; each chain keeps its own safety.

Tuning is config-driven (config.yaml [manufacturing.siting], live on daemon restart):
  siting_disabled            emergency off-switch (default off = ACTIVE)
  dry_run                    evaluate + log decisions but take no action (watch mode)
  tick_interval_secs         reconcile cadence (default 900)
  top_k / workers_per_chain  portfolio size (top_k pins it; else derived, default /3.5)
  weight_* / max_chains_*    score weights and concentration caps
  freshness_max_secs / emit_staleness_secs / retire_hysteresis_ticks / ...

Examples:
  spacetraders workflow siting-coordinator --agent TORWIND
  spacetraders workflow siting-coordinator --player-id 1

```
spacetraders workflow siting-coordinator [flags]
```

### Options

```
  -h, --help   help for siting-coordinator
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

