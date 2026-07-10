## spacetraders operations

Manage resource extraction and manufacturing operations

### Synopsis

Unified management for resource operations including gas extraction and manufacturing.

This command provides a single entry point for starting, monitoring, and stopping
both gas extraction and manufacturing operations.

Examples:
  # Start both gas and manufacturing operations
  spacetraders operations start --system X1-AU21 --gas --manufacturing

  # Start only gas extraction
  spacetraders operations start --system X1-AU21 --gas --siphons SIPHON-1,SIPHON-2 --storage STORAGE-1

  # Start only manufacturing
  spacetraders operations start --system X1-AU21 --manufacturing --min-price 2000

  # View status of all operations
  spacetraders operations status

  # Stop operations by type
  spacetraders operations stop --gas
  spacetraders operations stop --manufacturing

### Options

```
  -h, --help   help for operations
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
* [spacetraders operations start](spacetraders_operations_start.md)	 - Start resource operations in a system
* [spacetraders operations status](spacetraders_operations_status.md)	 - Show status of all running operations
* [spacetraders operations stop](spacetraders_operations_stop.md)	 - Stop running operations

