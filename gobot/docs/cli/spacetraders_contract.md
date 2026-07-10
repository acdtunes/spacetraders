## spacetraders contract

Manage contract operations

### Synopsis

Manage contract operations with automatic fleet coordination.

Contract commands allow you to automate contract execution using all available idle light hauler ships.

Examples:
  spacetraders contract start
  spacetraders container list
  spacetraders container stop <container-id>

### Options

```
  -h, --help   help for contract
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
* [spacetraders contract demand](spacetraders_contract_demand.md)	 - Recurring contract demand joined to cheapest foreign markets (pre-positioning candidates)
* [spacetraders contract get](spacetraders_contract_get.md)	 - Show full detail for a contract
* [spacetraders contract list](spacetraders_contract_list.md)	 - List contracts for a player
* [spacetraders contract start](spacetraders_contract_start.md)	 - Start contract fleet coordinator

