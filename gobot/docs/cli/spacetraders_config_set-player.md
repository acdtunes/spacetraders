## spacetraders config set-player

Set default player

### Synopsis

Set the default player to use for commands.

Specify the player using either --player-id or --agent flag.
The default player will be used when no player is specified in commands.

Examples:
  spacetraders config set-player --player-id 1
  spacetraders config set-player --agent ENDURANCE

```
spacetraders config set-player [flags]
```

### Options

```
  -h, --help   help for set-player
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders config](spacetraders_config.md)	 - Manage configuration settings

