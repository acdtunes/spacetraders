## spacetraders operations start

Start resource operations in a system

### Synopsis

Start gas extraction and/or manufacturing operations in a system.

At least one of --gas or --manufacturing must be specified.

Gas Extraction:
  Deploys siphon ships to extract resources from gas giants and storage ships
  to buffer the extracted resources. Manufacturing haulers will automatically
  pick up buffered resources via STORAGE_ACQUIRE_DELIVER tasks.

Manufacturing:
  Discovers high-demand goods, manufactures them using the supply chain,
  and sells them for profit using a task-based pipeline architecture.

Examples:
  # Start both operations
  spacetraders operations start --system X1-AU21 --gas --manufacturing \
    --siphons SIPHON-1 --storage STORAGE-1 --min-price 2000

  # Gas only with auto-selected gas giant
  spacetraders operations start --system X1-AU21 --gas \
    --siphons SIPHON-1,SIPHON-2 --storage STORAGE-1

  # Manufacturing only with custom strategy
  spacetraders operations start --system X1-AU21 --manufacturing \
    --strategy prefer-fabricate --max-workers 5

  # Dry run to preview operations
  spacetraders operations start --system X1-AU21 --gas --manufacturing --dry-run

```
spacetraders operations start [flags]
```

### Options

```
      --dry-run                        Preview operations without executing
      --force                          Override fuel validation warnings (gas)
      --gas                            Enable gas extraction operation
      --gas-giant string               Gas giant waypoint (optional, auto-selects if not provided)
  -h, --help                           help for start
      --manufacturing                  Enable manufacturing operation
      --max-collection-pipelines int   Maximum concurrent collection pipelines (0 = unlimited)
      --max-leg-time int               Max time per leg in minutes (gas, 0 = no limit)
      --max-pipelines int              Maximum concurrent fabrication pipelines (manufacturing) (default 3)
      --max-workers int                Maximum parallel workers (manufacturing) (default 5)
      --min-balance int                Minimum credit balance to maintain (manufacturing)
      --min-price int                  Minimum purchase price threshold (manufacturing) (default 1000)
      --siphons string                 Comma-separated siphon ship symbols (required for gas)
      --storage string                 Comma-separated storage ship symbols (required for gas)
      --strategy string                Acquisition strategy: prefer-buy, prefer-fabricate, smart (default "prefer-fabricate")
      --system string                  System symbol (required)
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

