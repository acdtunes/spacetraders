## spacetraders player register

Register a new player/agent

### Synopsis

Register a new player/agent with the SpaceTraders API token.

You must first register with the SpaceTraders API at https://spacetraders.io
to obtain your unique agent symbol and JWT token.

The token will be stored securely in the local database and used for all
API requests on behalf of this agent.

Example:
  spacetraders player register --agent ENDURANCE --token eyJ... --faction COSMIC

```
spacetraders player register [flags]
```

### Options

```
      --agent string     Agent symbol (required)
      --faction string   Starting faction (optional)
  -h, --help             help for register
      --new              Register a new agent via the API using ST_ACCOUNT_TOKEN and create its era row
      --token string     SpaceTraders API JWT token (required unless --new)
```

### Options inherited from parent commands

```
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders player](spacetraders_player.md)	 - Manage players and agents

