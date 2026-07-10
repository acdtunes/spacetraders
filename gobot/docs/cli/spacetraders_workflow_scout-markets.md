## spacetraders workflow scout-markets

Deploy fleet to scout markets with VRP optimization

### Synopsis

Distribute markets across multiple ships using Vehicle Routing Problem optimization.

The daemon will:
- Check for existing scout-tour containers for each ship (reuses if found)
- For ships needing containers:
  - Optimize market distribution using VRP solver
  - Create scout-tour containers with assigned markets
- Return combined results (new + reused containers)

This command is idempotent: ships with existing containers are reused automatically.

Examples:
  # Deploy 2 scouts to 5 markets
  spacetraders workflow scout-markets --ships SCOUT-1,SCOUT-2 --system X1-TEST --markets X1-TEST-A1,X1-TEST-B2,X1-TEST-C3,X1-TEST-D4,X1-TEST-E5 --agent ENDURANCE

  # Single ship (no VRP optimization needed)
  spacetraders workflow scout-markets --ships SCOUT-1 --system X1-GZ7 --markets X1-GZ7-A1,X1-GZ7-B2 --agent ENDURANCE

  # Infinite loop
  spacetraders workflow scout-markets --ships SCOUT-1,SCOUT-2,SCOUT-3 --system X1-TEST --markets X1-TEST-A1,X1-TEST-B2,X1-TEST-C3 --iterations -1 --agent ENDURANCE

```
spacetraders workflow scout-markets [flags]
```

### Options

```
  -h, --help             help for scout-markets
      --iterations int   Number of complete tours (-1 = infinite, 0 = the default of 1; N tours otherwise) (default 1)
      --markets string   Comma-separated list of market waypoints (required)
      --ships string     Comma-separated list of ship symbols (required)
      --system string    System symbol (required)
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

