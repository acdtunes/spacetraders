## spacetraders ship refresh

Force-resync a ship's cached state from the server

### Synopsis

Force a fresh GET /my/ships/<symbol> against the SpaceTraders API and
overwrite the daemon's local cargo + nav cache with the server response.

Use this to reconcile a desynced ship cache (e.g. phantom cargo or a stale
position) without restarting the daemon and without moving the ship. The
reconciled state is printed on success.

Examples:
  spacetraders ship refresh --ship ENDURANCE-1 --player-id 1
  spacetraders ship refresh --ship ENDURANCE-1 --agent ENDURANCE

```
spacetraders ship refresh [flags]
```

### Options

```
  -h, --help          help for refresh
      --ship string   Ship symbol (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ship](spacetraders_ship.md)	 - Manage ships

