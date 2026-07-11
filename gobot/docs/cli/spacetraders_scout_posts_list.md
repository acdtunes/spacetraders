## spacetraders scout posts list

List active scout posts

### Synopsis

List the scout posts declared for a player, one row per post: system symbol,
kind (standing or sweep-once), target scan freshness, configured probe/hull
budget, and how many of those hull slots the coordinator currently has manned
("(unmanned)" when none). Prints "No scout posts configured." when the player
has none.

Player is resolved from --player-id, --agent, or the persisted default. Reads
live daemon state, so the daemon must be running.

Examples:
  spacetraders scout posts list --agent ENDURANCE

```
spacetraders scout posts list [flags]
```

### Options

```
  -h, --help   help for list
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

