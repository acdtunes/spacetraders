## spacetraders container get

Get detailed container information

### Synopsis

Show the full detail record for a single background container: its type,
status, owning player, current and max iteration counts, restart count,
creation and last-update timestamps, and any stored metadata.

Where "container list" prints one row per container, this drills into one
container by ID (the value shown in the list's CONTAINER ID column). Reads
live daemon state, so the daemon must be running.

```
spacetraders container get <container-id> [flags]
```

### Options

```
  -h, --help   help for get
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders container](spacetraders_container.md)	 - Manage background containers

