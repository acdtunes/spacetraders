## spacetraders captain regime

Inspect or declare the captain's price-regime tripwires

### Synopsis

Manage the captain's price-regime tripwires (spec: sp-zlfv price-regime
detector). A tripwire is a standing "watch this good's sell price" rule: the
watchkeeper emits a deferred market.regime_shift event once a matching good
crosses the declared threshold, mechanizing the per-wake price sweep the
captain used to hand-roll.

Tripwires are additive: "regime set" adds one without disturbing the others,
"regime list" prints every declared tripwire, and "regime clear" removes them
all (with none declared, the detector does not scan at all). The declared set
lives in the supervisor state file and survives restarts.

Examples:
  spacetraders captain regime set --good ORE --bid-above 200
  spacetraders captain regime list
  spacetraders captain regime clear

### Options

```
  -h, --help   help for regime
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain](spacetraders_captain.md)	 - Autonomous captain operations
* [spacetraders captain regime clear](spacetraders_captain_regime_clear.md)	 - Remove all declared price tripwires (disables the regime detector)
* [spacetraders captain regime list](spacetraders_captain_regime_list.md)	 - List the captain's currently declared price tripwires
* [spacetraders captain regime set](spacetraders_captain_regime_set.md)	 - Declare a price tripwire (adds to, does not replace, existing tripwires)

