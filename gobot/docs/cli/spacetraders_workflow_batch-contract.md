## spacetraders workflow batch-contract

Execute batch contract workflow

### Synopsis

Execute automated contract workflow that negotiates, accepts, purchases goods,
delivers cargo, and fulfills contracts. Runs in background as a daemon.

The daemon will automatically:
- Negotiate new contracts or resume existing ones
- Evaluate contract profitability
- Accept contracts
- Purchase required goods from cheapest markets
- Deliver cargo (handles multi-trip delivery)
- Fulfill contracts
- Return a container ID for tracking progress

This runs a single contract to completion. For continuous, multi-contract
operation across all idle light hauler ships, use 'spacetraders contract start'
instead.

Examples:
  spacetraders workflow batch-contract --ship SHIP-1 --player-id 1
  spacetraders workflow batch-contract --ship SHIP-1 --agent ENDURANCE

```
spacetraders workflow batch-contract [flags]
```

### Options

```
  -h, --help          help for batch-contract
      --ship string   Ship symbol to use for contracts (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders workflow](spacetraders_workflow.md)	 - Execute complex multi-step workflows

