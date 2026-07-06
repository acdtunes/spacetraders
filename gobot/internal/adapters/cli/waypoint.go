package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// NewWaypointCommand creates the waypoint command with subcommands
func NewWaypointCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "waypoint",
		Short: "Discover waypoints in a system",
		Long: `List and inspect waypoints from the daemon's waypoint cache.

Unlike the market cache (which only holds physically-visited MARKETPLACE
waypoints), these commands surface every waypoint in a system - including the
JUMP_GATE - syncing from the SpaceTraders API when the cache is empty or stale.

Examples:
  spacetraders waypoint list --system X1-PZ28 --agent ENDURANCE
  spacetraders waypoint list --system X1-PZ28 --type JUMP_GATE
  spacetraders waypoint list --system X1-PZ28 --trait SHIPYARD
  spacetraders waypoint get --waypoint X1-PZ28-I55 --agent ENDURANCE`,
	}

	cmd.AddCommand(newWaypointListCommand())
	cmd.AddCommand(newWaypointGetCommand())

	return cmd
}

// newWaypointListCommand creates the waypoint list subcommand
func newWaypointListCommand() *cobra.Command {
	var (
		systemSymbol string
		trait        string
		waypointType string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the waypoints of a system",
		Long: `List the waypoints of a system from the daemon's waypoint cache.

Shows each waypoint's symbol, type, coordinates, and traits. Optionally filter
by trait (e.g. SHIPYARD, MARKETPLACE) or type (e.g. JUMP_GATE). The system is
synced from the API when the cache is empty or stale.

Examples:
  spacetraders waypoint list --system X1-PZ28 --agent ENDURANCE
  spacetraders waypoint list --system X1-PZ28 --type JUMP_GATE
  spacetraders waypoint list --system X1-PZ28 --trait SHIPYARD --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
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

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			var traitPtr *string
			if trait != "" {
				traitPtr = &trait
			}
			var typePtr *string
			if waypointType != "" {
				typePtr = &waypointType
			}

			response, err := client.ListWaypoints(ctx, systemSymbol, traitPtr, typePtr, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list waypoints: %w", err)
			}

			if len(response.Waypoints) == 0 {
				fmt.Println("No waypoints found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WAYPOINT\tTYPE\tX\tY\tTRAITS")
			fmt.Fprintln(w, "--------\t----\t-\t-\t------")

			for _, wp := range response.Waypoints {
				fmt.Fprintf(w, "%s\t%s\t%.0f\t%.0f\t%s\n",
					wp.Symbol,
					wp.Type,
					wp.X,
					wp.Y,
					strings.Join(wp.Traits, ", "),
				)
			}

			w.Flush()

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol (required)")
	cmd.Flags().StringVar(&trait, "trait", "", "Filter by trait (e.g. SHIPYARD, MARKETPLACE)")
	cmd.Flags().StringVar(&waypointType, "type", "", "Filter by type (e.g. JUMP_GATE)")

	return cmd
}

// newWaypointGetCommand creates the waypoint get subcommand
func newWaypointGetCommand() *cobra.Command {
	var waypointSymbol string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show detailed information about a waypoint",
		Long: `Show detailed information about a single waypoint.

Displays the waypoint's system, type, coordinates, traits, and orbitals. The
waypoint is auto-fetched from the API when it is not cached.

Examples:
  spacetraders waypoint get --waypoint X1-PZ28-I55 --agent ENDURANCE
  spacetraders waypoint get --waypoint X1-PZ28-I55 --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if waypointSymbol == "" {
				return fmt.Errorf("--waypoint flag is required")
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

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			playerID, agentSymbol := playerPointers(playerIdent)

			response, err := client.GetWaypoint(ctx, waypointSymbol, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to get waypoint: %w", err)
			}

			wp := response.Waypoint

			fmt.Printf("Waypoint:      %s\n", wp.Symbol)
			fmt.Printf("System:        %s\n", wp.SystemSymbol)
			fmt.Printf("Type:          %s\n", wp.Type)
			fmt.Printf("Coordinates:   (%.0f, %.0f)\n", wp.X, wp.Y)
			fmt.Printf("Has Fuel:      %t\n", wp.HasFuel)
			if len(wp.Traits) > 0 {
				fmt.Printf("Traits:        %s\n", strings.Join(wp.Traits, ", "))
			}
			if len(wp.Orbitals) > 0 {
				fmt.Printf("Orbitals:      %s\n", strings.Join(wp.Orbitals, ", "))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&waypointSymbol, "waypoint", "", "Waypoint symbol (required)")

	return cmd
}
