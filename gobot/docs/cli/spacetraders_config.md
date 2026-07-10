## spacetraders config

Manage configuration settings

### Synopsis

Manage SpaceTraders configuration settings.

Configuration is loaded from multiple sources with priority:
1. Environment variables (ST_* prefix)
2. Config file (config.yaml)
3. Default values

User preferences (default player) are stored in ~/.spacetraders/config.json

Examples:
  spacetraders config show
  spacetraders config set-player --agent ENDURANCE
  spacetraders config set-player --player-id 1
  spacetraders config clear-player

### Options

```
  -h, --help   help for config
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
* [spacetraders config clear-player](spacetraders_config_clear-player.md)	 - Clear default player setting
* [spacetraders config set-player](spacetraders_config_set-player.md)	 - Set default player
* [spacetraders config show](spacetraders_config_show.md)	 - Show current configuration

