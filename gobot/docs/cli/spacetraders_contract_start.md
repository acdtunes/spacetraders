## spacetraders contract start

Start contract fleet coordinator

### Synopsis

Start a contract fleet coordinator that uses all available idle light hauler ships for continuous contract execution.

The coordinator will:
- Dynamically discover all idle light hauler ships
- Negotiate contracts continuously
- Assign each contract to the ship closest to the purchase market
- Balance ship positions after contract delivery if ship selection changes
- Execute contracts in sequence (one contract at a time)
- Run until stopped

Ships are selected dynamically from the pool of idle haulers. No pre-assignment needed.

Optionally, a static dedicated contract fleet can be configured with
--dedicated-ships: those ships are claim-filtered out of every other
coordinator's discovery pool (mfg/gas/trade-route) and reserved exclusively
for this contract coordinator. Pair with --standby-stations so an idle
dedicated ship homes to the nearest standby waypoint instead of being
balanced to a market. Both flags are optional; omitting them keeps the
coordinator's original behavior (all idle haulers, no dedicated fleet).

Examples:
  spacetraders contract start --player-id 1
  spacetraders contract start --agent ENDURANCE
  spacetraders contract start --agent ENDURANCE \
    --dedicated-ships ENDURANCE-4,ENDURANCE-5,ENDURANCE-6 \
    --standby-stations X1-TEST-J56,X1-TEST-E42,X1-TEST-H49,X1-TEST-B7

```
spacetraders contract start [flags]
```

### Options

```
      --dedicated-ships string    Comma-separated list of ship symbols reserved exclusively for this contract coordinator (optional)
  -h, --help                      help for start
      --standby-stations string   Comma-separated list of waypoints an idle dedicated ship homes to (optional, requires --dedicated-ships)
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

