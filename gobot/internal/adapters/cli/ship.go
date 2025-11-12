package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewShipCommand creates the ship command with subcommands
func NewShipCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ship",
		Short: "Manage ships",
		Long: `Manage ships and view ship information.

Ships are your vessels in the SpaceTraders universe. Use these commands to
view your fleet, check ship details, and monitor status.

Examples:
  spacetraders ship list --agent ENDURANCE
  spacetraders ship info --ship ENDURANCE-1 --agent ENDURANCE`,
	}

	// Add subcommands
	cmd.AddCommand(newShipListCommand())
	cmd.AddCommand(newShipInfoCommand())

	return cmd
}

// newShipListCommand creates the ship list subcommand
func newShipListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all ships for a player",
		Long: `List all ships owned by a player/agent.

Shows ship symbol, location, navigation status, fuel, and cargo levels.

Examples:
  spacetraders ship list --player-id 1
  spacetraders ship list --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Create repositories and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			waypointRepo := persistence.NewGormWaypointRepository(db)
			shipRepo := persistence.NewGormShipRepository(db, apiClient, playerRepo, waypointRepo)
			handler := ship.NewListShipsHandler(shipRepo, playerRepo)

			// Execute query
			ctx := context.Background()
			var playerIDPtr *int
			if playerIdent.PlayerID > 0 {
				playerIDPtr = &playerIdent.PlayerID
			}

			response, err := handler.Handle(ctx, &ship.ListShipsQuery{
				PlayerID:    playerIDPtr,
				AgentSymbol: playerIdent.AgentSymbol,
			})
			if err != nil {
				return fmt.Errorf("failed to list ships: %w", err)
			}

			result := response.(*ship.ListShipsResponse)

			if len(result.Ships) == 0 {
				fmt.Println("No ships found.")
				return nil
			}

			// Display table
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SHIP SYMBOL\tLOCATION\tSTATUS\tFUEL\tCARGO")
			fmt.Fprintln(w, "-----------\t--------\t------\t----\t-----")

			for _, s := range result.Ships {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%d/%d\n",
					s.ShipSymbol(),
					s.CurrentLocation().Symbol,
					s.NavStatus(),
					s.Fuel().Current,
					s.Fuel().Capacity,
					s.CargoUnits(),
					s.CargoCapacity(),
				)
			}

			w.Flush()

			return nil
		},
	}

	return cmd
}

// newShipInfoCommand creates the ship info subcommand
func newShipInfoCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show detailed ship information",
		Long: `Show detailed information about a specific ship.

Displays ship location, navigation status, fuel levels, cargo capacity,
cargo contents, and engine specifications.

Examples:
  spacetraders ship info --ship ENDURANCE-1 --player-id 1
  spacetraders ship info --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Create repositories and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			waypointRepo := persistence.NewGormWaypointRepository(db)
			shipRepo := persistence.NewGormShipRepository(db, apiClient, playerRepo, waypointRepo)
			handler := ship.NewGetShipHandler(shipRepo, playerRepo)

			// Execute query
			ctx := context.Background()
			var playerIDPtr *int
			if playerIdent.PlayerID > 0 {
				playerIDPtr = &playerIdent.PlayerID
			}

			response, err := handler.Handle(ctx, &ship.GetShipQuery{
				ShipSymbol:  shipSymbol,
				PlayerID:    playerIDPtr,
				AgentSymbol: playerIdent.AgentSymbol,
			})
			if err != nil {
				return fmt.Errorf("failed to get ship: %w", err)
			}

			result := response.(*ship.GetShipResponse)
			s := result.Ship

			// Display ship info
			fmt.Printf("Ship Information\n")
			fmt.Printf("================\n\n")
			fmt.Printf("Ship Symbol:    %s\n", s.ShipSymbol())
			fmt.Printf("Location:       %s\n", s.CurrentLocation().Symbol)
			fmt.Printf("Nav Status:     %s\n", s.NavStatus())
			fmt.Printf("Fuel:           %d / %d\n", s.Fuel().Current, s.Fuel().Capacity)
			fmt.Printf("Cargo:          %d / %d units\n", s.CargoUnits(), s.CargoCapacity())
			fmt.Printf("Engine Speed:   %d\n", s.EngineSpeed())

			// Show cargo contents if any
			if s.CargoUnits() > 0 {
				fmt.Printf("\nCargo Contents:\n")
				for _, item := range s.Cargo().Inventory {
					fmt.Printf("  - %s: %d units (%s)\n", item.Name, item.Units, item.Symbol)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}
