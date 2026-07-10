## spacetraders operations stop

Stop running operations

### Synopsis

Stop running gas extraction and/or manufacturing operations.

Without flags, stops ALL coordinators (both gas and manufacturing).
Use --gas or --manufacturing to stop only specific operation types.

Examples:
  # Stop all operations
  spacetraders operations stop

  # Stop only gas operations
  spacetraders operations stop --gas

  # Stop only manufacturing operations
  spacetraders operations stop --manufacturing

  # Stop operations in a specific system
  spacetraders operations stop --system X1-AU21

```
spacetraders operations stop [flags]
```

### Options

```
      --gas             Stop gas extraction operations
  -h, --help            help for stop
      --manufacturing   Stop manufacturing operations
      --system string   Only stop operations in this system
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

