## spacetraders

SpaceTraders CLI - Interact with the SpaceTraders daemon

### Synopsis

SpaceTraders CLI provides commands to interact with your SpaceTraders fleet.
The CLI communicates with the daemon via Unix socket for efficient operation.

Examples:
  spacetraders ship navigate --ship AGENT-1 --destination X1-GZ7-B1
  spacetraders ship dock --ship AGENT-1
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --quantity 3
  spacetraders market get --waypoint X1-GZ7-A1
  spacetraders workflow batch-contract --ship AGENT-1 --iterations 5
  spacetraders container list
  spacetraders container logs <container-id>

### Options

```
      --agent string    Agent symbol (alternative to player-id)
  -h, --help            help for spacetraders
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain](spacetraders_captain.md)	 - Autonomous captain operations
* [spacetraders config](spacetraders_config.md)	 - Manage configuration settings
* [spacetraders construction](spacetraders_construction.md)	 - Manage construction site supply operations
* [spacetraders container](spacetraders_container.md)	 - Manage background containers
* [spacetraders contract](spacetraders_contract.md)	 - Manage contract operations
* [spacetraders fleet](spacetraders_fleet.md)	 - Manage dedicated fleets
* [spacetraders frontier](spacetraders_frontier.md)	 - Standing frontier expansion: auto-buy probes and seed frontier scouts
* [spacetraders goods](spacetraders_goods.md)	 - Manage automated goods production
* [spacetraders health](spacetraders_health.md)	 - Check daemon health status
* [spacetraders history](spacetraders_history.md)	 - Cross-era priors: query history across universe resets
* [spacetraders ledger](spacetraders_ledger.md)	 - Financial ledger operations
* [spacetraders market](spacetraders_market.md)	 - View market data
* [spacetraders operations](spacetraders_operations.md)	 - Manage resource extraction and manufacturing operations
* [spacetraders player](spacetraders_player.md)	 - Manage players and agents
* [spacetraders scout](spacetraders_scout.md)	 - Standing scout posts: keep systems' market data fresh
* [spacetraders ship](spacetraders_ship.md)	 - Manage ships
* [spacetraders shipyard](spacetraders_shipyard.md)	 - Manage shipyard operations
* [spacetraders system](spacetraders_system.md)	 - Inspect system-level topology
* [spacetraders tour](spacetraders_tour.md)	 - Multi-hop trade-tour tooling (sp-1ek0)
* [spacetraders universe](spacetraders_universe.md)	 - Universe era registry and reset operations
* [spacetraders version](spacetraders_version.md)	 - Print the CLI build stamp (version, commit, build time)
* [spacetraders waypoint](spacetraders_waypoint.md)	 - Discover waypoints in a system
* [spacetraders workflow](spacetraders_workflow.md)	 - Execute complex multi-step workflows

