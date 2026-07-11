## spacetraders ship outfit list

List the modules installed on a ship

### Synopsis

List the modules currently installed on a ship, along with its
reactor power / module slot / crew budget summary.

Power, slots, and crew are computed offline from the ship's last-synced
state (sp-el60) - reactors, frames, and crew capacity have no swap endpoint
in the SpaceTraders API, so these budgets are permanent for the life of the
hull and don't require a live trial-and-error install to check.

Pass --candidate to check offline whether a not-yet-installed module would
fit. The candidate's own power/crew/slot requirements are resolved
automatically from another ship in the fleet that has it installed (sp-el60
acceptance fix) - there is no catalog of unowned module specs to take them
from on the command line, so there are no --power/--crew/--slots flags. If
no ship anywhere has ever carried the candidate symbol, the requirements are
reported as unknown and the verdict is UNKNOWN-REQUIREMENTS, never a
trivially-satisfied CAN-INSTALL.

Examples:
  spacetraders ship outfit list --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship outfit list --ship ENDURANCE-1 --agent ENDURANCE \
    --candidate MODULE_CARGO_HOLD_III

```
spacetraders ship outfit list [flags]
```

### Options

```
      --candidate string   Symbol of a not-yet-installed module to check offline install feasibility for
  -h, --help               help for list
      --ship string        Ship symbol whose modules to list (required)
```

### Options inherited from parent commands

```
      --agent string    Agent symbol (alternative to player-id)
      --player-id int   Player ID (required if agent not specified)
      --socket string   Path to daemon Unix socket (default "/tmp/spacetraders-daemon.sock")
  -v, --verbose         Enable verbose output
```

### SEE ALSO

* [spacetraders ship outfit](spacetraders_ship_outfit.md)	 - Install, remove, or list ship modules

