## spacetraders workflow trade-route

Fly one idle hull through the top-ranked arbitrage circuit (as a daemon container)

### Synopsis

Ask the daemon to fly a single idle hull through the top-ranked pure-arbitrage
circuit in a system, as a recovery-safe daemon container under trade-analyst
discipline: buy at the exporter, sell at the importer, in tranches of at most 18
units per visit, and keep looping only while the destination bid clears basis+1000
(the acquisition cost plus the bid-floor). The circuit stops the moment the margin
dies and the hull is released back to idle.

This complements the mfg coordinator, which only trades its own fabrication
targets: trade-route exploits the standing buy-export/sell-import spreads nobody
else works, using idle-gap hulls (a contract-pool hauler between contracts, a
factory hauler between tasks) as free capacity.

Execution model: the circuit runs INSIDE the daemon as a container (single-writer,
claim-release-on-death, RouteExecutor-backed navigation, restart-safe). This command
only starts it and returns the container id; follow it with 'container logs'. The
daemon must be running. Run this only on a genuinely idle hull — the daemon refuses a
hull it is actively flying.

Examples:
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --agent ENDURANCE
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --max-visits 20 --player-id 1
  spacetraders workflow trade-route --ship ENDURANCE-7 --system X1-GZ7 --dest X1-ABC-STATION --agent ENDURANCE

```
spacetraders workflow trade-route [flags]
```

### Options

```
      --dest string      Pin the circuit to this destination waypoint or system, instead of auto-selecting a lane (waives the cross-system gate penalty for the targeted lane only)
  -h, --help             help for trade-route
      --max-visits int   The RUN's total visit budget across every lane it commits to (0 = default 50); the run only stops early on a margin/starvation/error exit, and always at a leg boundary with the hold empty (sp-1hj5)
      --ship string      Idle hull to fly the circuit (required)
      --system string    System to scan for arbitrage lanes (required)
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

