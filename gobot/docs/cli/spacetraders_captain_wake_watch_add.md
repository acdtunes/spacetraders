## spacetraders captain wake watch add

Arm a one-shot wake watch (adds to, does not replace, existing watches)

### Synopsis

Arm a one-shot wake watch on a ship arrival or a container terminal state.

The target is "ship:<SYMBOL>:arrival" or "container:<ID>:terminal". The watch
fires a single wake the first time a matching event is seen OR at its deadline,
whichever comes first, then auto-disarms.

--by sets the deadline: a relative duration ("+20m") applied from now, or an
absolute RFC3339 timestamp. When omitted:
  - ship:arrival watches prefer an ETA-derived deadline (sp-970u): a
    best-effort read of the ship's live nav gives now+(ETA-now)×(1+--eta-margin)
    when the ship is IN_TRANSIT with a known arrival time. Any failure to read
    or use that nav (ship not found/docked, stale data, ...) falls back to
    --default-deadline from now, so this never blocks or fails the add.
  - container:terminal watches always use --default-deadline from now — there
    is no ETA concept for a container.
Either way the watch is always deadline'd, so a watch whose match event is
lost still fires (tagged deadline-fired) and clears itself rather than arming
forever. The CLI prints which deadline was used.

Each "watch add" ADDS a watch; run "captain wake watch clear" for a clean
slate. Multiple watches coexist and fire independently.

Examples:
  spacetraders captain wake watch add ship:TORWIND-E:arrival
  spacetraders captain wake watch add ship:TORWIND-E:arrival --by +20m
  spacetraders captain wake watch add container:c-9f2a:terminal --by 2026-07-10T18:00:00Z

```
spacetraders captain wake watch add <ship:SYMBOL:arrival | container:ID:terminal> [--by +20m|<RFC3339>] [flags]
```

### Options

```
      --by string                   Deadline: relative ("+20m") or an RFC3339 absolute timestamp (default: derived from ship ETA for ship:arrival, else --default-deadline from now)
      --default-deadline duration   Deadline applied when --by is omitted and no ETA can be derived, measured from now (default 30m0s)
      --eta-margin float            Fractional margin added atop a ship's live ETA for a ship:arrival watch when --by is omitted (e.g. 0.25 = +25%) (default 0.25)
  -h, --help                        help for add
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain wake watch](spacetraders_captain_wake_watch.md)	 - Arm, list, or clear one-shot wake watches

