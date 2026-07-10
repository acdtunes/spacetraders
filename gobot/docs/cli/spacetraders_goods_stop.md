## spacetraders goods stop

Stop a running goods factory

### Synopsis

Stop a running goods factory.

This will gracefully stop the factory coordinator and release any assigned ships
back to the idle pool.

Examples:
  spacetraders goods stop factory_12345

```
spacetraders goods stop <factory-id> [flags]
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

* [spacetraders goods](spacetraders_goods.md)	 - Manage automated goods production

