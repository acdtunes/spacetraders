## spacetraders captain wake

Inspect or declare the captain's wake policy

### Synopsis

Inspect or declare when the supervisor wakes the captain outside its default
heartbeat cadence (spec: sp-sk68 wake model).

"wake set" declares the standing policy — the next scheduled wake, credit
thresholds that force a wake, and which event types interrupt immediately —
with each call fully replacing the prior policy. "wake show" prints the
currently declared policy (or the defaults if none). "wake watch" arms
one-shot wake watches on a specific ship arrival or container terminal state,
which fire once and auto-disarm independently of the standing policy.

A declaration takes effect on the very next supervisor poll — no restart
required.

Examples:
  spacetraders captain wake set --next-wake-at +3h
  spacetraders captain wake show

### Options

```
  -h, --help   help for wake
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
* [spacetraders captain wake set](spacetraders_captain_wake_set.md)	 - Declare the captain's wake policy (replaces any previously declared policy)
* [spacetraders captain wake show](spacetraders_captain_wake_show.md)	 - Show the captain's currently declared wake policy
* [spacetraders captain wake watch](spacetraders_captain_wake_watch.md)	 - Arm, list, or clear one-shot wake watches

