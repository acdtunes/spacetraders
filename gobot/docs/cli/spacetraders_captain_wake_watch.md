## spacetraders captain wake watch

Arm, list, or clear one-shot wake watches

### Synopsis

Arm one-shot wake watches that fire a single captain wake the first time a
specific ship arrives or a specific container reaches a terminal state — or at
a deadline, whichever comes first — then auto-disarm (spec: sp-oyer).

Watches are operational ephemera, SEPARATE from the standing wake policy: they
have their own store, multiple coexist independently, and "captain wake set"'s
full-replace semantics never touch them (and vice versa).

A fired watch reaches the captain as an interrupt-class wake mail tagged
"matched" (the arrival/terminal event was seen) or "deadline-fired" (the
deadline passed first — e.g. the arrival event was lost), then clears itself.

### Options

```
  -h, --help   help for watch
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
* [spacetraders captain wake watch add](spacetraders_captain_wake_watch_add.md)	 - Arm a one-shot wake watch (adds to, does not replace, existing watches)
* [spacetraders captain wake watch clear](spacetraders_captain_wake_watch_clear.md)	 - Disarm all one-shot wake watches
* [spacetraders captain wake watch list](spacetraders_captain_wake_watch_list.md)	 - List the currently-armed one-shot wake watches

