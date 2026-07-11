## spacetraders workflow

Execute complex multi-step workflows

### Synopsis

Execute automated workflows that run as background daemons.

Workflows are multi-step operations that combine navigation, trading, and scouting
into automated tasks. All workflows run in background containers that can be
monitored using the container commands.

Examples:
  spacetraders workflow batch-contract --ship SHIP-1
  spacetraders workflow scout-markets --ships SCOUT-1,SCOUT-2 --system X1-GZ7 --markets X1-GZ7-A1,X1-GZ7-B2

### Options

```
  -h, --help   help for workflow
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
* [spacetraders workflow arb-run](spacetraders_workflow_arb-run.md)	 - Fly one idle hull through a single captain-directed, guarded arbitrage leg (as a daemon container)
* [spacetraders workflow batch-contract](spacetraders_workflow_batch-contract.md)	 - Execute batch contract workflow
* [spacetraders workflow scout-all-markets](spacetraders_workflow_scout-all-markets.md)	 - Automatically assign all probe/satellite ships to scout all non-fuel-station markets
* [spacetraders workflow scout-markets](spacetraders_workflow_scout-markets.md)	 - Deploy fleet to scout markets with VRP optimization
* [spacetraders workflow siting-coordinator](spacetraders_workflow_siting-coordinator.md)	 - Start the standing factory-siting coordinator (automates factory discovery, placement, and capacity planning)
* [spacetraders workflow stocker](spacetraders_workflow_stocker.md)	 - Fly one dedicated hull as a warehouse-filling stocker loop (as a daemon container)
* [spacetraders workflow tour-run](spacetraders_workflow_tour-run.md)	 - Fly one idle hull through planner-chosen, guarded multi-hop trade tours (as a daemon container)
* [spacetraders workflow trade-fleet-coordinator](spacetraders_workflow_trade-fleet-coordinator.md)	 - Start the standing trade-fleet coordinator (keeps continuous tours alive on 'trade' hulls)
* [spacetraders workflow trade-route](spacetraders_workflow_trade-route.md)	 - Fly one idle hull through the top-ranked arbitrage circuit (as a daemon container)
* [spacetraders workflow warehouse](spacetraders_workflow_warehouse.md)	 - Park an idle hull as a passive inventory warehouse at a home waypoint (as a daemon container)
* [spacetraders workflow worker-rebalancer-coordinator](spacetraders_workflow_worker-rebalancer-coordinator.md)	 - Start the standing worker-rebalancer coordinator (ferries idle lights to worker-starved factory systems)

