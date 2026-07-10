## spacetraders goods status

Check the status of a goods factory

### Synopsis

Check the status and progress of a goods factory.

Displays detailed information about the factory including:
- Current status (PENDING, ACTIVE, COMPLETED, FAILED, STOPPED)
- Target good and system
- Production progress (nodes completed vs total)
- Quantity acquired and total cost
- Dependency tree (with --tree flag)

Examples:
  spacetraders goods status factory_12345
  spacetraders goods status factory_12345 --tree

```
spacetraders goods status <factory-id> [flags]
```

### Options

```
  -h, --help   help for status
      --tree   Display the full dependency tree with visual indicators
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders goods](spacetraders_goods.md)	 - Manage automated goods production

