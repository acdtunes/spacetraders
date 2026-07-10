## spacetraders system gates

Print the cross-system jump-gate adjacency

### Synopsis

Print the cross-system jump-gate adjacency.

Without --system, prints the adjacency for every system currently in the local
gate-graph store (era-scoped: dead-era rows are never shown). With --system,
prints just that system's connections, fetching them live from the API and
persisting them if the store has no fresh entry.

Examples:
  spacetraders system gates
  spacetraders system gates --system X1-KA42

```
spacetraders system gates [flags]
```

### Options

```
  -h, --help            help for gates
      --system string   Print only this system's connections (fetches live on a store miss), e.g. X1-KA42
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders system](spacetraders_system.md)	 - Inspect system-level topology

