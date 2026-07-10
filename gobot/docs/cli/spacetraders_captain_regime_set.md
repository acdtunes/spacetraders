## spacetraders captain regime set

Declare a price tripwire (adds to, does not replace, existing tripwires)

### Synopsis

Declare a captain price tripwire (spec: sp-zlfv price-regime detector): the
watchkeeper emits a deferred market.regime_shift event once a matching good's
market price crosses in the given direction. Mechanizes the per-wake price
sweep the captain used to hand-roll ("any ore bid >=200 or gas bid >=150
(~3x baseline) triggers an immediate extraction re-consult").

Unlike "captain wake set" (full-replace), each "regime set" call ADDS a new
tripwire to the declared list — it does not remove previously declared
tripwires. Run "captain regime clear" first if you want a clean slate.

--good accepts a class keyword ("ORE" matches any *_ORE symbol; "GAS" matches
the fixed hydrocarbon/liquid-gas set) or a literal comma-separated symbol
list ("IRON_ORE,COPPER_ORE") for an exact match only.

--bid-above/--bid-below (exactly one required) accept either an absolute
sell price ("200") or a multiplier of a recorded baseline price ("3x").
Multiplier mode looks up the OLDEST price-history sample within --window as
the baseline; it will not fire until at least one such sample has been
recorded.

--window serves two purposes: the baseline lookback in multiplier mode, and
the edge-trigger cooldown in both modes — once a crossing fires, the same
good+market+direction will not re-fire until --window elapses and the price
re-crosses (avoids a session-burn loop on every poll while the price stays
crossed).

Examples:
  spacetraders captain regime set --good ORE --bid-above 200
  spacetraders captain regime set --good GAS --bid-above 3x --window 4h
  spacetraders captain regime set --good HYDROCARBON --bid-below 20

```
spacetraders captain regime set --good <ORE|GAS|SYMBOL[,SYMBOL...]> (--bid-above <price|Nx> | --bid-below <price|Nx>) [--window 1h] [flags]
```

### Options

```
      --bid-above string   Fire when sell price rises to or above this: absolute price or a multiplier like "3x"
      --bid-below string   Fire when sell price falls to or below this: absolute price or a multiplier like "3x"
      --good string        Good class ("ORE", "GAS") or comma-separated literal symbol(s)
  -h, --help               help for set
      --window duration    Baseline lookback (multiplier mode) and edge-trigger cooldown (default 1h0m0s)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain regime](spacetraders_captain_regime.md)	 - Inspect or declare the captain's price-regime tripwires

