## spacetraders player

Manage players and agents

### Synopsis

Manage players and agents in the local database.

Players represent your SpaceTraders agents with their authentication tokens.
Use these commands to register new agents, list existing ones, and view details.

Examples:
  spacetraders player register --agent ENDURANCE --token <jwt-token>
  spacetraders player list
  spacetraders player info --agent ENDURANCE
  spacetraders player info --player-id 1

### Options

```
  -h, --help   help for player
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
* [spacetraders player info](spacetraders_player_info.md)	 - Show detailed player information
* [spacetraders player list](spacetraders_player_list.md)	 - List all registered players
* [spacetraders player register](spacetraders_player_register.md)	 - Register a new player/agent

