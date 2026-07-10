## spacetraders universe scrub

Delete WIPE-class player-scoped junk rows for a closed era

### Synopsis

Optional hygiene, any time after close: DELETE the WIPE-class player-scoped
rows (containers, container_logs, ships, manufacturing_factory_states,
gas_operations, storage_operations) for the dead era's player. Refuses on an
open era and never touches ARCHIVE-class history. Requires --confirm to echo
the era name.

```
spacetraders universe scrub [flags]
```

### Options

```
      --confirm string   must echo the era name to confirm the deletion
      --era string       era name to scrub
  -h, --help             help for scrub
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders universe](spacetraders_universe.md)	 - Universe era registry and reset operations

