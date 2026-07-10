## spacetraders fleet list

List every dedicated fleet and its member ships

### Synopsis

List every dedicated fleet — each distinct fleet name in use — with its
member ships and whether each member is idle (no active assignment and not
in transit) right now.

Examples:
  spacetraders fleet list
  spacetraders fleet list --agent TORWIND

```
spacetraders fleet list [flags]
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

* [spacetraders fleet](spacetraders_fleet.md)	 - Manage dedicated fleets

