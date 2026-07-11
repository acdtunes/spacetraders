## spacetraders scout posts

Manage desired-state scout posts

### Synopsis

Manage the desired-state scout posts the standing scout coordinator
reconciles (spec: sp-cxpq). A post is a per-system "keep these markets scanned"
assignment; the coordinator mans each post with an idle satellite, respawns
tours that die, and retires sweep-once posts after one pass.

"posts add" declares or updates a post (freshness target, standing vs
sweep-once, probe budget); "posts list" shows every post and how many of its
hull slots are currently manned; "posts remove" deletes a post and releases
its hull. Posts and their assignments survive daemon restarts.

Examples:
  spacetraders scout posts add X1-GZ7 --agent ENDURANCE
  spacetraders scout posts list --agent ENDURANCE
  spacetraders scout posts remove X1-GZ7 --agent ENDURANCE

### Options

```
  -h, --help   help for posts
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders scout](spacetraders_scout.md)	 - Standing scout posts: keep systems' market data fresh
* [spacetraders scout posts add](spacetraders_scout_posts_add.md)	 - Add or update a scout post for a system
* [spacetraders scout posts list](spacetraders_scout_posts_list.md)	 - List active scout posts
* [spacetraders scout posts remove](spacetraders_scout_posts_remove.md)	 - Remove a scout post and release its hull

