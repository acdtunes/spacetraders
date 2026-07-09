package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
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

Examples:
  spacetraders fleet assign --ship TORWIND-19 --fleet bulk_circuit
  spacetraders fleet unassign --ship TORWIND-19
  spacetraders fleet list`,
	}

	cmd.AddCommand(newFleetAssignCommand())
	cmd.AddCommand(newFleetUnassignCommand())
	cmd.AddCommand(newFleetListCommand())

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
