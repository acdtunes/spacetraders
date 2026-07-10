## spacetraders health

Check daemon health status

### Synopsis

Verify that the daemon is running and responsive.

```
spacetraders health [flags]
```

### Options

```
      --api-budget   Also show API request-budget observability (per-hull req/s, utilization vs ceiling, duty-cycle KPI)
  -h, --help         help for health
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

