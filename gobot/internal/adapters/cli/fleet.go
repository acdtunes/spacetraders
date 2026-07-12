package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// NewFleetCommand creates the fleet command group (sp-l7h2).
//
// A dedicated fleet is a named group of ships owned exclusively by one
// operation's coordinator: tagged hulls are invisible to every other
// coordinator's discovery and their claims are rejected atomically at the
// repository. Dedication is permanent ownership ("who owns this hull"),
// distinct from a container claim ("who holds it right now") — assigning a
// busy ship never evicts the current holder; the new fleet takes over when
// the claim is released.
//
// `fleet add`/`fleet remove` (sp-4s9m) are the operation-oriented surface over
// this same tag: a hull can be added to or removed from a RUNNING coordinator's
// dedicated fleet with NO container restart and no interruption to other hulls'
// in-progress work. The coordinator reads its fleet membership live from the DB
// every discovery pass (ship_pool_manager.go FindIdleShipsByFleet), so a freshly
// tagged hull is dispatched on the next tick and a freshly cleared one is dropped
// on the next tick — the daemon is the sole writer of the tag either way
// (RULINGS #3). `assign`/`unassign` remain as the raw fleet-name aliases.
func NewFleetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage dedicated fleets",
		Long: `Manage dedicated fleets — named groups of ships owned exclusively by one
operation's coordinator (contracts, bulk trade circuits, etc.).

A ship dedicated to a fleet is hidden from every other coordinator's
discovery and cannot be claimed by them; only the fleet's own coordinator
dispatches it. Dedication is persisted and survives daemon restarts.
Assigning a busy ship succeeds immediately but never interrupts its current
job — the new fleet takes over when the current claim is released.

'fleet add'/'fleet remove' mutate a RUNNING coordinator's fleet live, with no
container restart: the coordinator re-reads its membership from the DB every
discovery pass, so an added hull is dispatched next tick and a removed one
finishes its current contract leg and then leaves — no stranded contract.

Examples:
  spacetraders fleet add --operation contract --ship TORWIND-5
  spacetraders fleet remove --operation contract --ship TORWIND-1
  spacetraders fleet assign --ship TORWIND-19 --fleet bulk_circuit
  spacetraders fleet unassign --ship TORWIND-19
  spacetraders fleet list`,
	}

	cmd.AddCommand(newFleetAddCommand())
	cmd.AddCommand(newFleetRemoveCommand())
	cmd.AddCommand(newFleetAssignCommand())
	cmd.AddCommand(newFleetUnassignCommand())
	cmd.AddCommand(newFleetListCommand())

	return cmd
}

// fleetMutator is the subset of daemon operations the `fleet add`/`fleet remove`
// verbs need, narrowed to an interface so the operation-scoping logic is
// unit-testable without a live daemon. *DaemonClient satisfies it. The daemon
// remains the SOLE writer of the DedicatedFleet tag (RULINGS #3) — these methods
// are the CLI→daemon RPCs, and the ListFleets read below is only an advisory
// membership guard, never a write.
type fleetMutator interface {
	AssignShipFleet(ctx context.Context, shipSymbol, fleet string, playerID *int32, agentSymbol *string) (*pb.AssignShipFleetResponse, error)
	UnassignShipFleet(ctx context.Context, shipSymbol string, playerID *int32, agentSymbol *string) (*pb.UnassignShipFleetResponse, error)
	ListFleets(ctx context.Context, playerID *int32, agentSymbol *string) (*pb.ListFleetsResponse, error)
}

// runFleetAdd dedicates shipSymbol to operation's fleet, live. operation IS the
// fleet tag ("contract", "trade", "warehouse", "stocker", ...) — the same string
// the coordinator claims under (container_runner.go) — so adding a hull is a
// single AssignShipFleet write through the daemon (the single tag-write path,
// sp-l7h2). The coordinator's next discovery pass reads the new tag from the DB
// (FindIdleShipsByFleet) and dispatches the hull; NO container restart occurs and
// no other hull's in-progress work is touched. Returns the user-facing message.
func runFleetAdd(ctx context.Context, client fleetMutator, operation, shipSymbol string, playerID *int32, agentSymbol *string) (string, error) {
	resp, err := client.AssignShipFleet(ctx, shipSymbol, operation, playerID, agentSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to add %s to the %q fleet: %w", shipSymbol, operation, err)
	}
	return fmt.Sprintf("✓ %s added to the %q fleet — its coordinator discovers it live and dispatches it next tick; no container restart.\n", resp.ShipSymbol, resp.Fleet), nil
}

// runFleetRemove clears shipSymbol's dedication, live, but ONLY when the hull is
// actually a member of operation's fleet. This is the operation-scoped no-poach
// guard (RULINGS #7 at the operator surface): `fleet remove --operation contract`
// must REFUSE a hull dedicated to `stocker`, so a mistyped operation can never
// yank a hull out of a different coordinator's fleet. Membership is read via
// ListFleets — an advisory pre-check; the authoritative tag write is still the
// daemon's UnassignShipFleet (RULINGS #3).
//
// Removal is a clean hand-off, never a stranding: clearing the tag does not evict
// the hull's current container claim, so a hull removed mid-contract finishes its
// in-progress leg and only then returns to the general pool; the coordinator
// simply stops re-selecting it on the next discovery pass (its additive-only
// startup reconcile never re-adds it). NO container restart occurs.
func runFleetRemove(ctx context.Context, client fleetMutator, operation, shipSymbol string, playerID *int32, agentSymbol *string) (string, error) {
	resp, err := client.ListFleets(ctx, playerID, agentSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to check %s's current fleet before removal: %w", shipSymbol, err)
	}

	currentFleet := ""
	for _, fleet := range resp.Fleets {
		for _, member := range fleet.Ships {
			if member.ShipSymbol == shipSymbol {
				currentFleet = fleet.Name
			}
		}
	}

	switch {
	case currentFleet == "":
		return "", fmt.Errorf("%s is not in the %q fleet — it carries no dedication, so there is nothing to remove", shipSymbol, operation)
	case currentFleet != operation:
		return "", fmt.Errorf("%s is dedicated to the %q fleet, not %q — refusing to remove it from an operation it is not in (use --operation %s, or `fleet unassign --ship %s` to force-clear whatever fleet it is in)", shipSymbol, currentFleet, operation, currentFleet, shipSymbol)
	}

	if _, err := client.UnassignShipFleet(ctx, shipSymbol, playerID, agentSymbol); err != nil {
		return "", fmt.Errorf("failed to remove %s from the %q fleet: %w", shipSymbol, operation, err)
	}
	return fmt.Sprintf("✓ %s removed from the %q fleet — it finishes any in-progress contract leg, then returns to the general pool; no container restart.\n", shipSymbol, operation), nil
}

// newFleetAddCommand creates the `fleet add` subcommand (sp-4s9m) — the
// operation-oriented, live add over the shared DedicatedFleet tag path.
func newFleetAddCommand() *cobra.Command {
	var (
		operation  string
		shipSymbol string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a ship to a running operation's dedicated fleet, live (no restart)",
		Long: `Add a ship to a RUNNING operation's dedicated fleet without restarting its
coordinator. The operation name IS the fleet tag (contract, trade, warehouse,
stocker, manufacturing, ...).

The coordinator re-reads its fleet membership from the database on every
discovery pass, so the added hull is dispatched from the next tick onward. No
container is restarted and no other hull's in-progress contract is interrupted.

Examples:
  spacetraders fleet add --operation contract --ship TORWIND-5
  spacetraders fleet add --operation trade --ship TORWIND-9 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if operation == "" {
				return fmt.Errorf("--operation flag is required (the fleet/operation to add the ship to, e.g. 'contract')")
			}
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			msg, err := runFleetAdd(ctx, client, operation, shipSymbol, playerID, agentSymbol)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&operation, "operation", "", "Operation/fleet to add the ship to (required, e.g. 'contract')")
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}

// newFleetRemoveCommand creates the `fleet remove` subcommand (sp-4s9m) — the
// operation-scoped, live removal with a clean mid-contract hand-off.
func newFleetRemoveCommand() *cobra.Command {
	var (
		operation  string
		shipSymbol string
	)

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a ship from a running operation's dedicated fleet, live (no restart)",
		Long: `Remove a ship from a RUNNING operation's dedicated fleet without restarting its
coordinator. The removal is operation-scoped: it refuses to remove a hull that is
dedicated to a DIFFERENT operation, so a mistyped operation can never pull a hull
out of another coordinator's fleet.

A hull removed mid-contract finishes its current delivery leg and then returns to
the general pool — the dedication tag is cleared but the in-flight container claim
is never evicted, so no contract is stranded. The coordinator stops re-selecting
the hull on its next discovery pass. No container is restarted.

Examples:
  spacetraders fleet remove --operation contract --ship TORWIND-1
  spacetraders fleet remove --operation trade --ship TORWIND-9 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if operation == "" {
				return fmt.Errorf("--operation flag is required (the fleet/operation to remove the ship from, e.g. 'contract')")
			}
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			msg, err := runFleetRemove(ctx, client, operation, shipSymbol, playerID, agentSymbol)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&operation, "operation", "", "Operation/fleet to remove the ship from (required, e.g. 'contract')")
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}

// newFleetAssignCommand creates the fleet assign subcommand
func newFleetAssignCommand() *cobra.Command {
	var (
		shipSymbol string
		fleet      string
	)

	cmd := &cobra.Command{
		Use:   "assign",
		Short: "Dedicate a ship to a named fleet",
		Long: `Dedicate a ship to a named fleet, making it exclusive to that fleet's
coordinator. Other coordinators (manufacturing, factory, contracts, ...)
will neither discover nor claim it.

If the ship is mid-job for another operation, the assignment still succeeds:
the current job finishes undisturbed, and the fleet takes ownership when the
ship's claim is released. Re-assigning to a different fleet just overwrites
the tag — there is exactly one fleet per ship.

Examples:
  spacetraders fleet assign --ship TORWIND-19 --fleet bulk_circuit
  spacetraders fleet assign --ship TORWIND-7 --fleet contract --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if fleet == "" {
				return fmt.Errorf("--fleet flag is required (use 'fleet unassign' to clear a dedication)")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			response, err := client.AssignShipFleet(ctx, shipSymbol, fleet, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to assign ship fleet: %w", err)
			}

			fmt.Printf("✓ %s dedicated to fleet %q — exclusive to that fleet's coordinator\n", response.ShipSymbol, response.Fleet)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")
	cmd.Flags().StringVar(&fleet, "fleet", "", "Fleet name to dedicate the ship to (required)")

	return cmd
}

// newFleetUnassignCommand creates the fleet unassign subcommand
func newFleetUnassignCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "unassign",
		Short: "Clear a ship's fleet dedication",
		Long: `Clear a ship's fleet dedication, returning it to the general pool so any
coordinator's discovery can claim it again.

If the ship is mid-job for its fleet, the job finishes undisturbed — the
ship simply becomes generally claimable once its current claim is released.

Examples:
  spacetraders fleet unassign --ship TORWIND-19
  spacetraders fleet unassign --ship TORWIND-7 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			response, err := client.UnassignShipFleet(ctx, shipSymbol, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to unassign ship fleet: %w", err)
			}

			fmt.Printf("✓ %s returned to the general pool — claimable by any coordinator again\n", response.ShipSymbol)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}

// newFleetListCommand creates the fleet list subcommand
func newFleetListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every dedicated fleet and its member ships",
		Long: `List every dedicated fleet — each distinct fleet name in use — with its
member ships and whether each member is idle (no active assignment and not
in transit) right now.

Examples:
  spacetraders fleet list
  spacetraders fleet list --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			response, err := client.ListFleets(ctx, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list fleets: %w", err)
			}

			if len(response.Fleets) == 0 {
				fmt.Println("No dedicated fleets — every ship is in the general pool.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "FLEET\tSHIP\tSTATUS")
			for _, fleet := range response.Fleets {
				for _, member := range fleet.Ships {
					status := "busy"
					if member.Idle {
						status = "idle"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", fleet.Name, member.ShipSymbol, status)
				}
			}
			return w.Flush()
		},
	}

	return cmd
}
