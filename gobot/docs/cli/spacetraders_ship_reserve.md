## spacetraders ship reserve

Reserve a ship for the captain's direct manual use

### Synopsis

Reserve a ship for the captain's own direct, manual use, hiding it from
every coordinator's assignment discovery (mfg, contracts, scouting, trade
routes, etc.).

A captain reservation is persisted as an assignment row, so it survives
daemon restarts and is excluded from the stale-claim reconciliation pass —
no coordinator can claim a reserved hull out from under you, and refreshing
the ship's cache will never release the reservation on your behalf. Use
'ship release' when you're done with it.

If the reserved hull was the last idle ship of its role, a warning is
printed — the reservation still succeeds; the warning is advisory only.

Examples:
  spacetraders ship reserve --ship ENDURANCE-1 --reason "manual gate-supply errand"
  spacetraders ship reserve --ship ENDURANCE-1 --agent ENDURANCE

```
spacetraders ship reserve [flags]
```

### Options

```
  -h, --help            help for reserve
      --reason string   Free-text reason, shown in 'ship list' (optional)
      --ship string     Ship symbol (required)
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

