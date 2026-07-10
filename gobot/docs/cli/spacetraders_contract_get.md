## spacetraders contract get

Show full detail for a contract

### Synopsis

Show full detail for one contract, including per-delivery progress
(good, units required, units fulfilled) and both payment components.

Examples:
  spacetraders contract get contract-abc123 --player-id 1
  spacetraders contract get contract-abc123 --player-id 1 --json

```
spacetraders contract get <id> [flags]
```

### Options

```
  -h, --help   help for get
      --json   Output as JSON
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders contract](spacetraders_contract.md)	 - Manage contract operations

