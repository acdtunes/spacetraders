## spacetraders scout

Standing scout posts: keep systems' market data fresh

### Synopsis

Manage the standing scout-post coordinator (sp-cxpq).

A scout post is a desired-state assignment — "keep this system's markets scanned"
— that the coordinator reconciles every tick: it claims an idle satellite for
each unmanned post, respawns any tour that dies, and retires sweep-once posts
after one pass. Posts and their hull assignments survive daemon restarts.

### Options

```
  -h, --help   help for scout
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders](spacetraders.md)	 - SpaceTraders CLI - Interact with the SpaceTraders daemon
* [spacetraders scout posts](spacetraders_scout_posts.md)	 - Manage desired-state scout posts
* [spacetraders scout start](spacetraders_scout_start.md)	 - Start the standing scout-post coordinator

