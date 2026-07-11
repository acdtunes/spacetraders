## spacetraders ship reserved-cargo

Show a ship's cargo do-not-sell reservations

### Synopsis

Show which cargo is reserved (do-not-sell) on a ship.

Lists the per-hull reservation overrides and, for each good currently in the
hold, whether it is reserved and why — the default MODULE_*/MOUNT_* rule or an
explicit override set with 'ship reserve-cargo'/'ship unreserve-cargo'.

Examples:
  spacetraders ship reserved-cargo --ship TORWIND-1E --agent TORWIND
  spacetraders ship reserved-cargo --ship TORWIND-1E --player-id 1

```
spacetraders ship reserved-cargo [flags]
```

### Options

```
  -h, --help          help for reserved-cargo
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

