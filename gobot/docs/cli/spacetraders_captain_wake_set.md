## spacetraders captain wake set

Declare the captain's wake policy (replaces any previously declared policy)

### Synopsis

Declare when the supervisor should wake the captain outside its default
heartbeat cadence (spec: sp-sk68 wake model).

Each invocation fully replaces the previously declared policy: flags omitted
from this call are NOT carried over from a prior "wake set" call. Declare
every override you want active in a single invocation.

--next-wake-at accepts either a relative duration ("+3h", "+30m") applied
from the moment this command runs, or an absolute RFC3339 timestamp. It is
always capped at the supervisor's never-wake safety ceiling
(MaxWakeIntervalMinutes past the last session), so it can delay a wake but
can never suppress one indefinitely.

--interrupt-types REPLACES (not extends) the default set of event types that
force an immediate wake.

The declaration takes effect on the very next supervisor poll — no restart
required.

Examples:
  spacetraders captain wake set --next-wake-at +3h
  spacetraders captain wake set --next-wake-at 2026-07-06T18:00:00Z
  spacetraders captain wake set --credits-above 500000
  spacetraders captain wake set --credits-below 10000
  spacetraders captain wake set --interrupt-types workflow.failed,container.crashed

```
spacetraders captain wake set [--next-wake-at +3h|<RFC3339>] [--credits-above N] [--credits-below N] [--interrupt-types a,b,c] [flags]
```

### Options

```
      --credits-above int        Force a wake once credits rise to or above this amount
      --credits-below int        Force a wake once credits fall to or below this amount
  -h, --help                     help for set
      --interrupt-types string   Comma-separated event types that force an immediate wake (replaces the default set)
      --next-wake-at string      Next wake time: relative ("+3h") or an RFC3339 absolute timestamp
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain wake](spacetraders_captain_wake.md)	 - Inspect or declare the captain's wake policy

