## spacetraders ship list

List all ships for a player

### Synopsis

List all ships owned by a player/agent.

Shows ship symbol, location, navigation status, fuel, cargo levels, role,
dedicated fleet (permanent pin, e.g. "contract", or "-" if unpinned), owning
assignment (container id or "-"), and cache age. Rows are sorted by ship
symbol in natural order (TORWIND-2 before TORWIND-10).

The FLEET column is a one-glance check for a hull pinned to the wrong fleet
at purchase time (the sp-lybx incident) — no need to cross-check each ship
against 'fleet list' individually.

Examples:
  spacetraders ship list --player-id 1
  spacetraders ship list --agent ENDURANCE
  spacetraders ship list --player-id 1 --json

```
spacetraders ship list [flags]
```

### Options

```
  -h, --help   help for list
      --json   Output as JSON
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

