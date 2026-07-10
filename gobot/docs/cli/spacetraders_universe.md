## spacetraders universe

Universe era registry and reset operations

### Synopsis

Inspect and manage the universe era lifecycle.

A universe era is keyed by the server resetDate. 'universe status' compares the
live server resetDate against the open era row and signals a MISMATCH (non-zero
exit) when the universe has reset under the fleet.

Examples:
  spacetraders universe status

### Options

```
  -h, --help   help for universe
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
* [spacetraders universe close](spacetraders_universe_close.md)	 - Close a universe era (destructive: truncates caches, blanks the dead token)
* [spacetraders universe scrub](spacetraders_universe_scrub.md)	 - Delete WIPE-class player-scoped junk rows for a closed era
* [spacetraders universe status](spacetraders_universe_status.md)	 - Compare server resetDate against the open era

