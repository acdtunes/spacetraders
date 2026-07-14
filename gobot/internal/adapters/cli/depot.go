package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// NewDepotCommand builds the `contract depot` verb family for contract-depot management
// (beads sp-u9xa, sp-38xc). A contract depot localizes the contract-fulfilment supply
// chain to a region so the source->destination haul moves off the serialized contract
// critical path onto parallel stockers. Depot topology lives in the daemon (single writer,
// RULINGS #3) and survives restarts; these verbs reach it only through the RPC.
//
// Lifecycle + persistence, all durable through the application Store:
//   - `depot start <name> <spec.json>` persists ONE depot AND launches its coordinators
//     live (no restart); `depot stop <name>` tears those coordinators back down.
//   - `depot apply <spec.json>` DECLARATIVELY makes the persisted set exactly the spec.
//   - `depot add|remove|list|get` and `depot element add|remove|place` are GRANULAR live
//     ops, each persisted immediately (no restart needed).
//
// Nothing is hardcoded: every waypoint, ship, and count is operator data in the spec file
// or the flags.
func NewDepotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "depot",
		Short: "Contract depots: localize contract supply chains to a region",
		Long: `Manage contract depots (sp-u9xa, sp-38xc), under the contract command group.

A contract depot is { destination warehouse(s), stocker(s), pinned delivery hull(s),
source hub(s) } placed in a region, so the dominant source->destination haul runs on
parallel background stockers instead of the serialized contract critical path. Depot
topology lives in the daemon and survives restarts.

  contract depot start <name> <spec.json>  persist one depot AND launch it (live, no restart)
  contract depot stop <name>               stop that depot's running coordinators
  contract depot apply <spec.json>         declaratively apply a whole topology at once
  contract depot add|remove|list|get       granular depot-level ops
  contract depot element ...               granular element-level ops (add/remove/place a ship)`,
	}
	cmd.AddCommand(newDepotStartCommand())
	cmd.AddCommand(newDepotStopCommand())
	cmd.AddCommand(newDepotApplyCommand())
	cmd.AddCommand(newDepotAddCommand())
	cmd.AddCommand(newDepotRemoveCommand())
	cmd.AddCommand(newDepotListCommand())
	cmd.AddCommand(newDepotGetCommand())
	cmd.AddCommand(newDepotElementCommand())
	return cmd
}

// selectDepotFromSpec finds the depot named id in a parsed spec's depots. It errors when
// no depot in the spec carries that id, so `depot start <name>` fails loudly rather than
// silently starting nothing when the operator names a depot the spec does not define.
func selectDepotFromSpec(depots []DepotDTO, id string) (DepotDTO, error) {
	for _, d := range depots {
		if d.ID == id {
			return d, nil
		}
	}
	available := make([]string, 0, len(depots))
	for _, d := range depots {
		available = append(available, d.ID)
	}
	return DepotDTO{}, fmt.Errorf("depot %q not found in spec (defines: %s)", id, strings.Join(available, ", "))
}

// newDepotStartCommand builds `depot start <name> <spec.json>`: persist that depot's
// topology from the spec AND launch its coordinators in one shot — live activation, no
// daemon restart. Idempotent: re-running never double-launches a coordinator already
// running (the launch skips a non-idle hull).
func newDepotStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <DEPOT_NAME> <SPEC_FILE>",
		Short: "Persist a depot from a spec AND launch its coordinators (live, no restart)",
		Long: `Start ONE depot by name from a JSON spec: persist that depot's topology (upsert,
leaving the rest of the set intact) and immediately launch its warehouse + stocker
coordinators — no daemon restart. Idempotent: re-running does not double-launch a
coordinator whose hull is already flying it.

The spec file is the same {"depots":[...]} format 'depot apply' reads; the named depot
must be one of the depots it defines.

Example:
  spacetraders contract depot start central topology.json --agent ENDURANCE`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			depots, err := loadDepotSpecFile(args[1])
			if err != nil {
				return err
			}
			spec, err := selectDepotFromSpec(depots, name)
			if err != nil {
				return err
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			launched, err := client.StartDepot(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, spec)
			if err != nil {
				return fmt.Errorf("failed to start depot %q: %w", name, err)
			}
			fmt.Printf("✓ Depot started: %s (persisted; %d coordinator(s) launched)\n", name, launched)
			return nil
		},
	}
	return cmd
}

// newDepotStopCommand builds `depot stop <name>`: tear down that depot's running
// warehouse + stocker coordinators.
func newDepotStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <DEPOT_NAME>",
		Short: "Stop a depot's running warehouse + stocker coordinators",
		Long: `Stop the running coordinators (warehouse + stocker containers) of the named depot,
leaving the depot's persisted topology intact so a later 'depot start' can relaunch it.
Containers belonging to other depots or operations are left running.

Example:
  spacetraders contract depot stop central --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			stopped, err := client.StopDepot(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, name)
			if err != nil {
				return fmt.Errorf("failed to stop depot %q: %w", name, err)
			}
			fmt.Printf("✓ Depot stopped: %s (%d coordinator container(s) stopped)\n", name, stopped)
			return nil
		},
	}
	return cmd
}

// newDepotGetCommand builds `depot get <name>`: show one depot's full topology (each
// element class with its waypoints and crewing ships).
func newDepotGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <DEPOT_NAME>",
		Short: "Show one depot's full topology",
		Long: `Show the full topology of one depot: its id and every element (warehouse, stocker,
delivery-hull, source-hub) with its waypoint and crewing ship. Reads live daemon state.

Example:
  spacetraders contract depot get central --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			depots, err := client.ListDepots(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to get depot: %w", err)
			}
			for _, d := range depots {
				if d.ID == name {
					printDepotDetail(d)
					return nil
				}
			}
			return fmt.Errorf("depot %q not found", name)
		},
	}
	return cmd
}

// printDepotDetail renders one depot's element classes as a compact, message-friendly
// listing.
func printDepotDetail(d *DepotDTO) {
	fmt.Printf("Depot: %s\n", d.ID)
	printDepotRole("warehouses", d.Warehouses)
	printDepotRole("stockers", d.Stockers)
	printDepotRole("delivery-hulls", d.DeliveryHulls)
	printDepotRole("source-hubs", d.SourceHubs)
}

func printDepotRole(role string, elems []DepotElementDTO) {
	if len(elems) == 0 {
		fmt.Printf("  %-14s (none)\n", role+":")
		return
	}
	for _, e := range elems {
		ship := e.ShipSymbol
		if ship == "" {
			ship = "(uncrewed)"
		}
		fmt.Printf("  %-14s %s @ %s\n", role+":", ship, e.Waypoint)
	}
}

func newDepotApplyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <SPEC_FILE>",
		Short: "Declaratively apply a whole depot topology from a JSON spec",
		Long: `Apply an entire depot topology at once from a JSON spec file. The persisted
set becomes EXACTLY the spec: depots in the spec are upserted, depots no longer in it
are removed. This is the bulk equivalent of a series of 'depot add'/'depot remove'.

Spec format ({"depots":[...]}):
  {
    "depots": [
      {
        "id": "central",
        "warehouses":     [{"waypoint": "X1-A-1", "ship_symbol": "WH-1"}],
        "delivery_hulls": [{"waypoint": "X1-A-1", "ship_symbol": "DH-1"}],
        "stockers":       [{"waypoint": "X1-SRC-1", "ship_symbol": "ST-1"}],
        "source_hubs":    [{"waypoint": "X1-HUB-1"}]
      }
    ]
  }

Example:
  spacetraders depot apply topology.json --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			depots, err := loadDepotSpecFile(args[0])
			if err != nil {
				return err
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			count, err := client.ApplyDepotTopology(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, depots)
			if err != nil {
				return fmt.Errorf("failed to apply depot topology: %w", err)
			}
			fmt.Printf("✓ Applied depot topology: %d depot(s) now persisted\n", count)
			return nil
		},
	}
	return cmd
}

func newDepotAddCommand() *cobra.Command {
	var (
		warehouses    []string
		stockers      []string
		deliveryHulls []string
		sourceHubs    []string
	)
	cmd := &cobra.Command{
		Use:   "add <DEPOT_ID>",
		Short: "Add one depot (granular) without disturbing the rest",
		Long: `Add a single depot with the given id and elements, leaving the rest of the
topology untouched. A depot needs at least one --warehouse (its routing anchor).

Each element flag is "WAYPOINT" or "WAYPOINT@SHIP" (the ship is optional — an
uncrewed slot declares sizing before a hull is pinned). Flags are repeatable.

Examples:
  spacetraders depot add central --warehouse X1-A-1@WH-1 --delivery-hull X1-A-1@DH-1 --agent ENDURANCE
  spacetraders depot add outpost --warehouse X1-B-2@WH-2 --stocker X1-SRC-1@ST-1 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := buildDepotDTO(args[0], warehouses, stockers, deliveryHulls, sourceHubs)
			if err != nil {
				return err
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := client.AddDepot(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, spec); err != nil {
				return fmt.Errorf("failed to add depot: %w", err)
			}
			fmt.Printf("✓ Depot added: %s\n", spec.ID)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&warehouses, "warehouse", nil, "Destination warehouse element: WAYPOINT or WAYPOINT@SHIP (repeatable; at least one required)")
	cmd.Flags().StringArrayVar(&stockers, "stocker", nil, "Background stocker element: WAYPOINT or WAYPOINT@SHIP (repeatable)")
	cmd.Flags().StringArrayVar(&deliveryHulls, "delivery-hull", nil, "Pinned delivery-hull element: WAYPOINT or WAYPOINT@SHIP (repeatable)")
	cmd.Flags().StringArrayVar(&sourceHubs, "source-hub", nil, "Source-hub element: WAYPOINT or WAYPOINT@SHIP (repeatable)")
	return cmd
}

func newDepotRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <DEPOT_ID>",
		Short: "Remove one depot by id (granular, idempotent)",
		Long: `Remove the depot with the given id, leaving the rest of the topology intact.
Removing a depot that does not exist is not an error.

Example:
  spacetraders depot remove central --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := client.RemoveDepot(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, args[0]); err != nil {
				return fmt.Errorf("failed to remove depot: %w", err)
			}
			fmt.Printf("✓ Depot removed: %s\n", args[0])
			return nil
		},
	}
	return cmd
}

func newDepotListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the player's contract depots",
		Long: `List every contract depot configured for a player: id and the count of each
element class (warehouses, stockers, delivery hulls, source hubs). Prints "No contract
depots configured." when the player has none. Reads live daemon state.

Example:
  spacetraders depot list --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			depots, err := client.ListDepots(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list depots: %w", err)
			}
			if len(depots) == 0 {
				fmt.Println("No contract depots configured.")
				return nil
			}
			fmt.Printf("%-16s  %-10s  %-9s  %-14s  %s\n", "DEPOT", "WAREHOUSE", "STOCKER", "DELIVERY-HULL", "SOURCE-HUB")
			for _, c := range depots {
				fmt.Printf("%-16s  %-10d  %-9d  %-14d  %d\n", c.ID, len(c.Warehouses), len(c.Stockers), len(c.DeliveryHulls), len(c.SourceHubs))
			}
			return nil
		},
	}
	return cmd
}

func newDepotElementCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "element",
		Short: "Granular element ops: add/remove/place a ship in a depot role",
		Long: `Add, remove, or place a single element in a depot role. --role is one of
warehouse, stocker, delivery-hull, source-hub. Each op persists immediately (no restart).`,
	}
	cmd.AddCommand(newDepotElementAddCommand())
	cmd.AddCommand(newDepotElementRemoveCommand())
	cmd.AddCommand(newDepotElementPlaceCommand())
	return cmd
}

func newDepotElementAddCommand() *cobra.Command {
	var role, waypoint, ship string
	cmd := &cobra.Command{
		Use:   "add <DEPOT_ID>",
		Short: "Add one element to a depot role",
		Long: `Add one element to an existing depot's named role.

Example:
  spacetraders depot element add central --role stocker --waypoint X1-SRC-2 --ship ST-2 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireDepotRole(role); err != nil {
				return err
			}
			return runDepotElementOp(func(ctx context.Context, client *DaemonClient, ident *PlayerIdentifier) error {
				return client.AddDepotElement(ctx, ident.PlayerID, ident.AgentSymbol, args[0], role, waypoint, ship)
			}, fmt.Sprintf("✓ Element added to %s (%s @ %s)\n", args[0], ship, waypoint))
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Depot role: warehouse | stocker | delivery-hull | source-hub (required)")
	cmd.Flags().StringVar(&waypoint, "waypoint", "", "Waypoint to place the element at (required)")
	cmd.Flags().StringVar(&ship, "ship", "", "Ship symbol crewing the element (optional for an uncrewed slot)")
	return cmd
}

func newDepotElementRemoveCommand() *cobra.Command {
	var role, ship string
	cmd := &cobra.Command{
		Use:   "remove <DEPOT_ID>",
		Short: "Remove the element crewed by a ship from a depot role",
		Long: `Remove the element crewed by --ship from an existing depot's named role.

Example:
  spacetraders depot element remove central --role stocker --ship ST-2 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireDepotRole(role); err != nil {
				return err
			}
			return runDepotElementOp(func(ctx context.Context, client *DaemonClient, ident *PlayerIdentifier) error {
				return client.RemoveDepotElement(ctx, ident.PlayerID, ident.AgentSymbol, args[0], role, ship)
			}, fmt.Sprintf("✓ Element crewed by %s removed from %s\n", ship, args[0]))
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Depot role: warehouse | stocker | delivery-hull | source-hub (required)")
	cmd.Flags().StringVar(&ship, "ship", "", "Ship symbol crewing the element to remove (required)")
	return cmd
}

func newDepotElementPlaceCommand() *cobra.Command {
	var role, ship, waypoint string
	cmd := &cobra.Command{
		Use:   "place <DEPOT_ID>",
		Short: "Reposition the element crewed by a ship to a waypoint",
		Long: `Reposition the element crewed by --ship in a depot role to --waypoint (e.g.
park a delivery hull at its warehouse). The element must already exist.

Example:
  spacetraders depot element place central --role delivery-hull --ship DH-1 --waypoint X1-A-1 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireDepotRole(role); err != nil {
				return err
			}
			return runDepotElementOp(func(ctx context.Context, client *DaemonClient, ident *PlayerIdentifier) error {
				return client.PlaceDepotElement(ctx, ident.PlayerID, ident.AgentSymbol, args[0], role, ship, waypoint)
			}, fmt.Sprintf("✓ Element crewed by %s placed at %s in %s\n", ship, waypoint, args[0]))
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Depot role: warehouse | stocker | delivery-hull | source-hub (required)")
	cmd.Flags().StringVar(&ship, "ship", "", "Ship symbol crewing the element to reposition (required)")
	cmd.Flags().StringVar(&waypoint, "waypoint", "", "Target waypoint (required)")
	return cmd
}

// runDepotElementOp is the shared connect->call->report spine for the granular element
// verbs, so each verb body is just its client call and success line.
func runDepotElementOp(call func(context.Context, *DaemonClient, *PlayerIdentifier) error, successMsg string) error {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return err
	}
	client, err := connectDaemon()
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := call(ctx, client, playerIdent); err != nil {
		return err
	}
	fmt.Print(successMsg)
	return nil
}

// depotRoleNames is the set of valid --role values, mirroring the domain role names so
// a mistyped role is rejected client-side before the RPC.
var depotRoleNames = map[string]bool{"warehouse": true, "stocker": true, "delivery-hull": true, "source-hub": true}

func requireDepotRole(role string) error {
	if !depotRoleNames[role] {
		return fmt.Errorf("invalid --role %q (want one of warehouse, stocker, delivery-hull, source-hub)", role)
	}
	return nil
}

// depotTopologyFile is the on-disk shape of a `depot apply` spec file.
type depotTopologyFile struct {
	Depots []DepotDTO `json:"depots"`
}

// loadDepotSpecFile reads and parses a topology spec file into its depots. It accepts
// either the wrapped object form {"depots":[...]} or a bare array [...].
func loadDepotSpecFile(path string) ([]DepotDTO, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read depot spec %q: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var depots []DepotDTO
		if err := json.Unmarshal(data, &depots); err != nil {
			return nil, fmt.Errorf("parse depot spec %q: %w", path, err)
		}
		return depots, nil
	}
	var file depotTopologyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse depot spec %q: %w", path, err)
	}
	return file.Depots, nil
}

// buildDepotDTO assembles a DepotDTO from an id and the repeatable element flags.
func buildDepotDTO(id string, warehouses, stockers, deliveryHulls, sourceHubs []string) (DepotDTO, error) {
	wh, err := parseDepotElementFlags(warehouses)
	if err != nil {
		return DepotDTO{}, err
	}
	st, err := parseDepotElementFlags(stockers)
	if err != nil {
		return DepotDTO{}, err
	}
	dh, err := parseDepotElementFlags(deliveryHulls)
	if err != nil {
		return DepotDTO{}, err
	}
	sh, err := parseDepotElementFlags(sourceHubs)
	if err != nil {
		return DepotDTO{}, err
	}
	return DepotDTO{ID: id, Warehouses: wh, Stockers: st, DeliveryHulls: dh, SourceHubs: sh}, nil
}

func parseDepotElementFlags(raws []string) ([]DepotElementDTO, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]DepotElementDTO, 0, len(raws))
	for _, raw := range raws {
		e, err := parseDepotElementFlag(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// parseDepotElementFlag parses one element flag "WAYPOINT" or "WAYPOINT@SHIP" into a
// DepotElementDTO. The waypoint is required; the ship is optional (an uncrewed slot).
func parseDepotElementFlag(raw string) (DepotElementDTO, error) {
	parts := strings.SplitN(raw, "@", 2)
	waypoint := strings.TrimSpace(parts[0])
	if waypoint == "" {
		return DepotElementDTO{}, fmt.Errorf("invalid element %q (want WAYPOINT or WAYPOINT@SHIP)", raw)
	}
	element := DepotElementDTO{Waypoint: waypoint}
	if len(parts) == 2 {
		element.ShipSymbol = strings.TrimSpace(parts[1])
	}
	return element, nil
}
