## spacetraders config clear-player

Clear default player setting

### Synopsis

Remove the default player setting.

After clearing, you must explicitly specify --player-id or --agent
for all commands that require player context.

Example:
  spacetraders config clear-player

```
spacetraders config clear-player [flags]
```

### Options

```
  -h, --help   help for clear-player
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

