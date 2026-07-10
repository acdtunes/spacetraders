## spacetraders construction

Manage construction site supply operations

### Synopsis

Manage construction site supply operations.

The construction pipeline system delivers materials to construction sites
(e.g., jump gates under construction). It automatically discovers required
materials and creates tasks to produce/acquire and deliver them.

Examples:
  spacetraders construction start X1-FB5-I61 --player-id 1
  spacetraders construction status X1-FB5-I61 --player-id 1

### Options

```
  -h, --help   help for construction
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
* [spacetraders construction start](spacetraders_construction_start.md)	 - Start a pipeline to supply materials to a construction site
* [spacetraders construction status](spacetraders_construction_status.md)	 - Show status of a construction site and any active pipeline
* [spacetraders construction stop](spacetraders_construction_stop.md)	 - Stop the active construction pipeline for a site

