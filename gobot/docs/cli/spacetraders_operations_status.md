## spacetraders operations status

Show status of all running operations

### Synopsis

Display the status of all running gas extraction and manufacturing operations.

Shows a unified view of:
  - Gas coordinators (gas_coordinator containers)
  - Manufacturing coordinators (manufacturing_coordinator containers)
  - Siphon workers (gas_siphon_worker containers)
  - Manufacturing task workers (manufacturing_task_worker containers)

Examples:
  spacetraders operations status
  spacetraders operations status --player-id 1

```
spacetraders operations status [flags]
```

### Options

```
  -h, --help   help for status
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders operations](spacetraders_operations.md)	 - Manage resource extraction and manufacturing operations

