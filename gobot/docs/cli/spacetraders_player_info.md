## spacetraders player info

Show detailed player information

### Synopsis

Show detailed information about a specific player.

Specify the player using either --player-id or --agent flag.

The API token is masked by default so it does not accumulate in logs or
transcripts. Pass --show-token to print the full token.

Examples:
  spacetraders player info --player-id 1
  spacetraders player info --agent ENDURANCE
  spacetraders player info --show-token

```
spacetraders player info [flags]
```

### Options

```
  -h, --help         help for info
      --show-token   Print the full API token instead of a masked prefix
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders player](spacetraders_player.md)	 - Manage players and agents

