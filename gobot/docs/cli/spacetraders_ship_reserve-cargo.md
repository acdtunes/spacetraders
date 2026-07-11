## spacetraders ship reserve-cargo

Mark a cargo good as do-not-sell on a ship

### Synopsis

Reserve a cargo good so no coordinator or CLI sell ever liquidates it.

Ship hardware bought for outfitting (MODULE_*/MOUNT_*) is reserved by DEFAULT —
you only need this verb to protect an additional good, or to re-protect a module
you previously released with 'ship unreserve-cargo'. The reservation is persisted
per-hull and survives daemon restarts.

Examples:
  spacetraders ship reserve-cargo --ship TORWIND-1E --good ANTIMATTER --agent TORWIND
  spacetraders ship reserve-cargo --ship TORWIND-1E --good MODULE_CARGO_HOLD_III --player-id 1

```
spacetraders ship reserve-cargo [flags]
```

### Options

```
      --good string   Trade good or module symbol (required)
  -h, --help          help for reserve-cargo
      --ship string   Ship symbol (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ship](spacetraders_ship.md)	 - Manage ships

