## spacetraders captain regime clear

Remove all declared price tripwires (disables the regime detector)

### Synopsis

Remove every currently-declared price tripwire. With no tripwires declared,
the watchkeeper's price-regime detector does not scan at all.

Examples:
  spacetraders captain regime clear

```
spacetraders captain regime clear [flags]
```

### Options

```
  -h, --help   help for clear
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders captain regime](spacetraders_captain_regime.md)	 - Inspect or declare the captain's price-regime tripwires

