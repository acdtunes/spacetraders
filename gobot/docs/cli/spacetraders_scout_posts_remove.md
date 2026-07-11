## spacetraders scout posts remove

Remove a scout post and release its hull

### Synopsis

Remove the scout post for a system and release the satellite(s) manning it
back to the idle pool for reassignment. Takes the system symbol (as shown in
the SYSTEM column of "scout posts list") as its sole argument.

Player is resolved from --player-id, --agent, or the persisted default. Reads
and mutates live daemon state, so the daemon must be running.

Examples:
  spacetraders scout posts remove X1-GZ7 --agent ENDURANCE

```
spacetraders scout posts remove <SYSTEM> [flags]
```

### Options

```
  -h, --help   help for remove
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders scout posts](spacetraders_scout_posts.md)	 - Manage desired-state scout posts

