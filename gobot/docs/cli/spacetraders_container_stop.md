## spacetraders container stop

Stop a running container

### Synopsis

Ask the daemon to stop a running background container by ID, printing the
resulting status (e.g. STOPPING or STOPPED) and a short message. Take the ID
from the CONTAINER ID column of "container list".

Reads and mutates live daemon state, so the daemon must be running.

```
spacetraders container stop <container-id> [flags]
```

### Options

```
  -h, --help   help for stop
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

