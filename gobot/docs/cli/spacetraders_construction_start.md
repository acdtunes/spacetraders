## spacetraders construction start

Start a pipeline to supply materials to a construction site

### Synopsis

Start a pipeline to supply materials to a construction site.

The pipeline will:
- Fetch construction site requirements from the API
- Create tasks for each required material
- Produce/acquire materials based on supply chain depth
- Deliver materials to the construction site

Supply chain depth controls how much to produce:
  0 - Full production (mine/produce everything from scratch)
  1 - Buy raw materials only (produce intermediates)
  2 - Buy intermediate goods (only final assembly)
  3 - Buy final product (no production, just delivery)

--min-supply lowers the floor the sourcing locator will buy EXPORT
materials down to (default floor: MODERATE). For example, --min-supply
SCARCE lets the pipeline source from a market even when its supply has
dropped all the way to SCARCE, instead of waiting for it to recover to
MODERATE or better. Only ABUNDANT, HIGH, MODERATE, LIMITED, and SCARCE
are accepted. Left unset, behavior is unchanged from the MODERATE default.
The floor is persisted on the pipeline, so it also applies when resuming
an existing, in-progress pipeline and when recovering materials that were
deferred because no market met the floor at the time.

The pipeline is IDEMPOTENT - running this command again will resume
an existing pipeline instead of creating a new one.

Examples:
  spacetraders construction start X1-FB5-I61 --player-id 1
  spacetraders construction start X1-FB5-I61 --system X1-FB5 --depth 3 --player-id 1
  spacetraders construction start X1-FB5-I61 --min-supply SCARCE --player-id 1

```
spacetraders construction start <construction-site> [flags]
```

### Options

```
      --depth int           Supply chain depth (0=full, 1=raw, 2=intermediate, 3=buy final) (default 3)
  -h, --help                help for start
      --max-workers int     Maximum parallel workers (default 5)
      --min-supply string   Lower the EXPORT sourcing floor below the default MODERATE (one of ABUNDANT, HIGH, MODERATE, LIMITED, SCARCE)
      --system string       System symbol for market lookups (defaults to deriving from construction site)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders construction](spacetraders_construction.md)	 - Manage construction site supply operations

