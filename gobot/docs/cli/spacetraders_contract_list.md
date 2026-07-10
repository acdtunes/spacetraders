## spacetraders contract list

List contracts for a player

### Synopsis

List contracts for a player, one row per contract, including deadline
and time remaining - the decision-critical column for evaluating whether a
contract is still worth pursuing.

Examples:
  spacetraders contract list --player-id 1
  spacetraders contract list --player-id 1 --json

```
spacetraders contract list [flags]
```

### Options

```
  -h, --help   help for list
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

