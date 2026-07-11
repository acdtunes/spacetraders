## spacetraders ship unreserve-cargo

Release a reserved cargo good for sale on a ship

### Synopsis

Release a good for sale, overriding the default do-not-sell reservation.

Use this for the rare deliberate resale of ship hardware — e.g. selling a spare
MODULE_* you no longer intend to install. The override is persisted per-hull; run
'ship reserve-cargo' to protect the good again.

Examples:
  spacetraders ship unreserve-cargo --ship TORWIND-1E --good MODULE_CARGO_HOLD_III --agent TORWIND
  spacetraders ship unreserve-cargo --ship TORWIND-1E --good MOUNT_MINING_LASER_I --player-id 1

```
spacetraders ship unreserve-cargo [flags]
```

### Options

```
      --good string   Trade good or module symbol (required)
  -h, --help          help for unreserve-cargo
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

