## spacetraders goods

Manage automated goods production

### Synopsis

Manage automated goods production using supply chain fabrication.

The goods factory system recursively produces any good in the SpaceTraders economy
by building complete dependency trees, acquiring raw materials, and coordinating
multi-ship production operations.

Examples:
  spacetraders goods produce ADVANCED_CIRCUITRY --system X1-GZ7
  spacetraders goods status <factory-id>
  spacetraders goods stop <factory-id>

### Options

```
  -h, --help   help for goods
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
* [spacetraders goods produce](spacetraders_goods_produce.md)	 - Produce a good using automated supply chain fabrication
* [spacetraders goods status](spacetraders_goods_status.md)	 - Check the status of a goods factory
* [spacetraders goods stop](spacetraders_goods_stop.md)	 - Stop a running goods factory

