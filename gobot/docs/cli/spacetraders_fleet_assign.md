## spacetraders fleet assign

Dedicate a ship to a named fleet

### Synopsis

Dedicate a ship to a named fleet, making it exclusive to that fleet's
coordinator. Other coordinators (manufacturing, factory, contracts, ...)
will neither discover nor claim it.

If the ship is mid-job for another operation, the assignment still succeeds:
the current job finishes undisturbed, and the fleet takes ownership when the
ship's claim is released. Re-assigning to a different fleet just overwrites
the tag — there is exactly one fleet per ship.

Examples:
  spacetraders fleet assign --ship TORWIND-19 --fleet bulk_circuit
  spacetraders fleet assign --ship TORWIND-7 --fleet contract --agent TORWIND

```
spacetraders fleet assign [flags]
```

### Options

```
      --fleet string   Fleet name to dedicate the ship to (required)
  -h, --help           help for assign
      --ship string    Ship symbol (required)
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

