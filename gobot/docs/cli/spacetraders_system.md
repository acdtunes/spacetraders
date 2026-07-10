## spacetraders system

Inspect system-level topology

### Synopsis

Inspect system-level topology such as the cross-system jump-gate graph.

The jump-gate graph is the API's own truth about which systems are reachable
from which by a single jump. It is cached in a local store and refreshed lazily
on a miss, so 'system gates' both reveals the known map and charts a named
system live on demand.

### Options

```
  -h, --help   help for system
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders](spacetraders.md)	 - SpaceTraders CLI - Interact with the SpaceTraders daemon
* [spacetraders system gates](spacetraders_system_gates.md)	 - Print the cross-system jump-gate adjacency

