## spacetraders universe status

Compare server resetDate against the open era

### Synopsis

Print the server resetDate and next reset alongside the open era's recorded
reset date. Exits non-zero on MISMATCH so the Watchkeeper can script detection.
With no open era row it prints NO ERA and exits zero (pre-registration state).

```
spacetraders universe status [flags]
```

### Options

```
  -h, --help   help for status
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders universe](spacetraders_universe.md)	 - Universe era registry and reset operations

