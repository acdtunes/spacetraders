package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// hubMutator is the subset of daemon operations the `fleet hub add`/`remove`
// verbs need, narrowed to an interface so the verb logic is unit-testable without
// a live daemon. *DaemonClient satisfies it. By construction it exposes ONLY the
// FleetHub RPC — no container-restart method — so "the coordinator is never
// restarted" is guaranteed by the surface, not merely asserted. The daemon
// remains the SOLE writer of the persisted standby-station set (RULINGS #3).
type hubMutator interface {
	FleetHub(ctx context.Context, operation, waypoint string, add bool, playerIdent *PlayerIdentifier) (*pb.FleetHubResponse, error)
}

// runFleetHubAdd adds waypoint to operation's coordinator standby ("hub") set,
// live. The coordinator reads its standby set from its own container config every
// discovery pass, so the added hub draws idle dedicated hulls toward it from the
// next tick — no container restart. A no-op (the waypoint was already a hub) is
// reported honestly rather than as a fresh add.
func runFleetHubAdd(ctx context.Context, client hubMutator, operation, waypoint string, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.FleetHub(ctx, operation, waypoint, true, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to add hub %s to the %q operation: %w", waypoint, operation, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• %s is already a hub of the %q operation — standby set unchanged: %v\n", waypoint, operation, resp.StandbyStations), nil
	}
	return fmt.Sprintf("✓ %s added as a hub of the %q operation — its coordinator re-reads the set live and re-homes idle hulls toward it next tick; no container restart. Standby set now: %v\n", waypoint, operation, resp.StandbyStations), nil
}

// runFleetHubRemove removes waypoint from operation's coordinator standby set,
// live. Removing a hub re-homes its idle hulls to the remaining set on the next
// discovery pass; a hull mid-contract-leg is never interrupted (it finishes and
// re-homes when idle). No container restart. Removing a waypoint that is not a hub
// is reported as a no-op.
func runFleetHubRemove(ctx context.Context, client hubMutator, operation, waypoint string, playerIdent *PlayerIdentifier) (string, error) {
	resp, err := client.FleetHub(ctx, operation, waypoint, false, playerIdent)
	if err != nil {
		return "", fmt.Errorf("failed to remove hub %s from the %q operation: %w", waypoint, operation, err)
	}
	if !resp.Changed {
		return fmt.Sprintf("• %s is not a hub of the %q operation — nothing to remove. Standby set: %v\n", waypoint, operation, resp.StandbyStations), nil
	}
	return fmt.Sprintf("✓ %s removed as a hub of the %q operation — idle hulls re-home to the remaining set next tick; a hull mid-leg finishes first. No container restart. Standby set now: %v\n", waypoint, operation, resp.StandbyStations), nil
}

// newFleetHubCommand creates the `fleet hub` subcommand group — the
// operation-oriented, live add/remove of standby-station hubs, the hub analogue
// of `fleet add`/`remove` for ships.
func newFleetHubCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Add or remove a standby-station hub on a running operation, live (no restart)",
		Long: `Manage the standby-station "hubs" of a RUNNING operation's coordinator without
restarting it. A hub is a waypoint an idle dedicated ship homes to between legs;
the coordinator reads its hub set from its own config every discovery pass, so a
hub added draws idle dedicated hulls toward it and a hub removed re-homes its
hulls to the remaining set — all on the next tick, with no container restart and
no interruption to a hull mid-contract-leg.

Examples:
  spacetraders fleet hub add --operation contract --waypoint X1-TW-A1
  spacetraders fleet hub remove --operation contract --waypoint X1-TW-A1`,
	}
	cmd.AddCommand(newFleetHubAddCommand())
	cmd.AddCommand(newFleetHubRemoveCommand())
	return cmd
}

// newFleetHubAddCommand creates the `fleet hub add` subcommand.
func newFleetHubAddCommand() *cobra.Command {
	var (
		operation string
		waypoint  string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a standby-station hub to a running operation, live (no restart)",
		Long: `Add a standby-station hub to a RUNNING operation's coordinator without a
restart. The coordinator re-reads its hub set from the database every discovery
pass, so idle dedicated hulls begin re-homing toward the new hub from the next
tick. No container is restarted and no hull mid-contract-leg is interrupted.

Examples:
  spacetraders fleet hub add --operation contract --waypoint X1-TW-A1
  spacetraders fleet hub add --operation contract --waypoint X1-TW-B2 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if operation == "" {
				return fmt.Errorf("--operation flag is required (the operation whose coordinator owns the hub, e.g. 'contract')")
			}
			if waypoint == "" {
				return fmt.Errorf("--waypoint flag is required (the standby-station waypoint to add)")
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

			msg, err := runFleetHubAdd(ctx, client, operation, waypoint, playerIdent)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&operation, "operation", "", "Operation whose coordinator owns the hub (required, e.g. 'contract')")
	cmd.Flags().StringVar(&waypoint, "waypoint", "", "Standby-station waypoint symbol to add (required)")

	return cmd
}

// newFleetHubRemoveCommand creates the `fleet hub remove` subcommand.
func newFleetHubRemoveCommand() *cobra.Command {
	var (
		operation string
		waypoint  string
	)

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a standby-station hub from a running operation, live (no restart)",
		Long: `Remove a standby-station hub from a RUNNING operation's coordinator without a
restart. Idle dedicated hulls re-home to the remaining hub set on the next
discovery pass; a hull mid-contract-leg finishes its leg and only then re-homes.
No container is restarted.

Examples:
  spacetraders fleet hub remove --operation contract --waypoint X1-TW-A1
  spacetraders fleet hub remove --operation contract --waypoint X1-TW-B2 --agent TORWIND`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if operation == "" {
				return fmt.Errorf("--operation flag is required (the operation whose coordinator owns the hub, e.g. 'contract')")
			}
			if waypoint == "" {
				return fmt.Errorf("--waypoint flag is required (the standby-station waypoint to remove)")
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

			msg, err := runFleetHubRemove(ctx, client, operation, waypoint, playerIdent)
			if err != nil {
				return err
			}
			fmt.Print(msg)
			return nil
		},
	}

	cmd.Flags().StringVar(&operation, "operation", "", "Operation whose coordinator owns the hub (required, e.g. 'contract')")
	cmd.Flags().StringVar(&waypoint, "waypoint", "", "Standby-station waypoint symbol to remove (required)")

	return cmd
}
