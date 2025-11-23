package cli

import (
	"context"
	"fmt"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/setup"
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
view your fleet, check ship details, monitor status, and perform ship operations.

Examples:
  spacetraders ship list --agent ENDURANCE
  spacetraders ship info --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship navigate --ship ENDURANCE-1 --destination X1-GZ7-B1 --agent ENDURANCE
  spacetraders ship dock --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship orbit --ship ENDURANCE-1 --agent ENDURANCE
  spacetraders ship refuel --ship ENDURANCE-1 --agent ENDURANCE`,
	}

	// Add subcommands
	cmd.AddCommand(newShipListCommand())
	cmd.AddCommand(newShipInfoCommand())
	cmd.AddCommand(newShipNavigateCommand())
	cmd.AddCommand(newShipDockCommand())
	cmd.AddCommand(newShipOrbitCommand())
	cmd.AddCommand(newShipRefuelCommand())
	cmd.AddCommand(newShipJumpCommand())
	cmd.AddCommand(newShipSellCommand())

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
			// Get daemon client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Call daemon via gRPC
			ctx := context.Background()
			var playerID *int32
			if playerIdent.PlayerID > 0 {
				pid := int32(playerIdent.PlayerID)
				playerID = &pid
			}

			var agentSymbol *string
			if playerIdent.AgentSymbol != "" {
				agentSymbol = &playerIdent.AgentSymbol
			}

			response, err := client.ListShips(ctx, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list ships: %w", err)
			}

			if len(response.Ships) == 0 {
				fmt.Println("No ships found.")
				return nil
			}

			// Display table
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SHIP SYMBOL\tLOCATION\tSTATUS\tFUEL\tCARGO\tSPEED")
			fmt.Fprintln(w, "-----------\t--------\t------\t----\t-----\t-----")

			for _, s := range response.Ships {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%d/%d\t%d\n",
					s.Symbol,
					s.Location,
					s.NavStatus,
					s.FuelCurrent,
					s.FuelCapacity,
					s.CargoUnits,
					s.CargoCapacity,
					s.EngineSpeed,
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

			// Get daemon client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Call daemon via gRPC
			ctx := context.Background()
			var playerID *int32
			if playerIdent.PlayerID > 0 {
				pid := int32(playerIdent.PlayerID)
				playerID = &pid
			}

			var agentSymbol *string
			if playerIdent.AgentSymbol != "" {
				agentSymbol = &playerIdent.AgentSymbol
			}

			response, err := client.GetShip(ctx, shipSymbol, playerID, agentSymbol)
			if err != nil {
				return fmt.Errorf("failed to get ship: %w", err)
			}

			s := response.Ship

			// Display ship info
			fmt.Printf("Ship Information\n")
			fmt.Printf("================\n\n")
			fmt.Printf("Ship Symbol:    %s\n", s.Symbol)
			fmt.Printf("Role:           %s\n", s.Role)
			fmt.Printf("Location:       %s\n", s.Location)
			fmt.Printf("Nav Status:     %s\n", s.NavStatus)
			fmt.Printf("Fuel:           %d / %d\n", s.FuelCurrent, s.FuelCapacity)
			fmt.Printf("Cargo:          %d / %d units\n", s.CargoUnits, s.CargoCapacity)
			fmt.Printf("Engine Speed:   %d\n", s.EngineSpeed)

			// Show cargo contents if any
			if s.CargoUnits > 0 {
				fmt.Printf("\nCargo Contents:\n")
				for _, item := range s.CargoInventory {
					fmt.Printf("  - %s: %d units (%s)\n", item.Name, item.Units, item.Symbol)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol (required)")

	return cmd
}

// newShipNavigateCommand creates the ship navigate subcommand
func newShipNavigateCommand() *cobra.Command {
	var (
		shipSymbol  string
		destination string
	)

	cmd := &cobra.Command{
		Use:   "navigate",
		Short: "Navigate a ship to a destination waypoint",
		Long: `Navigate a ship to a destination waypoint within the same system.

The daemon will automatically:
- Orbit the ship if docked
- Plan the optimal route (including refuel stops if needed)
- Navigate to the destination
- Return a container ID for tracking progress

Examples:
  spacetraders ship navigate --ship AGENT-1 --destination X1-GZ7-B1 --player-id 1
  spacetraders ship navigate --ship SCOUT-2 --destination X1-GZ7-A1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destination == "" {
				return fmt.Errorf("--destination flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Execute navigate command
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.NavigateShip(ctx, shipSymbol, destination, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("navigation failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Navigation started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship:             %s\n", result.ShipSymbol)
			fmt.Printf("  Destination:      %s\n", result.Destination)
			fmt.Printf("  Status:           %s\n", result.Status)
			fmt.Printf("  Estimated Time:   %d seconds\n", result.EstimatedTime)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", result.ContainerID)

			return nil
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to navigate (required)")
	cmd.Flags().StringVar(&destination, "destination", "", "Destination waypoint symbol (required)")

	return cmd
}

// newShipDockCommand creates the ship dock subcommand
func newShipDockCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "dock",
		Short: "Dock a ship at its current location",
		Long: `Dock a ship at its current location.
Ship must be in orbit to dock.

Examples:
  spacetraders ship dock --ship AGENT-1 --player-id 1
  spacetraders ship dock --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.DockShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("dock failed: %w", err)
			}

			fmt.Println("✓ Dock operation started")
			fmt.Printf("  Container ID: %s\n", result.ContainerID)
			fmt.Printf("  Ship:         %s\n", result.ShipSymbol)
			fmt.Printf("  Status:       %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to dock (required)")

	return cmd
}

// newShipOrbitCommand creates the ship orbit subcommand
func newShipOrbitCommand() *cobra.Command {
	var shipSymbol string

	cmd := &cobra.Command{
		Use:   "orbit",
		Short: "Put a ship into orbit from docked position",
		Long: `Put a ship into orbit from its current docked position.
Ship must be docked to orbit.

Examples:
  spacetraders ship orbit --ship AGENT-1 --player-id 1
  spacetraders ship orbit --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.OrbitShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("orbit failed: %w", err)
			}

			fmt.Println("✓ Orbit operation started")
			fmt.Printf("  Container ID: %s\n", result.ContainerID)
			fmt.Printf("  Ship:         %s\n", result.ShipSymbol)
			fmt.Printf("  Status:       %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to orbit (required)")

	return cmd
}

// newShipRefuelCommand creates the ship refuel subcommand
func newShipRefuelCommand() *cobra.Command {
	var (
		shipSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "refuel",
		Short: "Refuel a ship at its current location",
		Long: `Refuel a ship at its current location.
Ship must be docked at a waypoint with fuel available.

Examples:
  spacetraders ship refuel --ship AGENT-1 --player-id 1
  spacetraders ship refuel --ship AGENT-1 --units 100 --player-id 1
  spacetraders ship refuel --ship ENDURANCE-1 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var unitsPtr *int
			if units > 0 {
				unitsPtr = &units
			}

			result, err := client.RefuelShip(ctx, shipSymbol, playerIdent.PlayerID, playerIdent.AgentSymbol, unitsPtr)
			if err != nil {
				return fmt.Errorf("refuel failed: %w", err)
			}

			fmt.Println("✓ Refuel operation started")
			fmt.Printf("  Container ID:  %s\n", result.ContainerID)
			fmt.Printf("  Ship:          %s\n", result.ShipSymbol)
			fmt.Printf("  Fuel Added:    %d\n", result.FuelAdded)
			fmt.Printf("  Credits Cost:  %d\n", result.CreditsCost)
			fmt.Printf("  Status:        %s\n", result.Status)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to refuel (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Specific fuel units to purchase (omit for full tank)")

	return cmd
}

// newShipJumpCommand creates the ship jump subcommand
func newShipJumpCommand() *cobra.Command {
	var (
		shipSymbol        string
		destinationSystem string
	)

	cmd := &cobra.Command{
		Use:   "jump",
		Short: "Jump a ship to a different star system via jump gate",
		Long: `Jump a ship to a different star system using a jump gate.

If the ship is not currently at a jump gate, it will automatically navigate to
the nearest jump gate in the current system before jumping.

The ship must have a jump drive module installed to use this command.

Examples:
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --player-id 1
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if destinationSystem == "" {
				return fmt.Errorf("--system flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			fmt.Printf("Initiating jump for ship %s to system %s...\n", shipSymbol, destinationSystem)

			result, err := client.JumpShip(ctx, shipSymbol, destinationSystem, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("jump failed: %w", err)
			}

			if !result.Success {
				return fmt.Errorf("jump failed: %s", result.Error)
			}

			// Display results
			if result.NavigatedToGate {
				fmt.Printf("✓ Navigated to jump gate: %s\n", result.JumpGateSymbol)
			}

			fmt.Printf("✓ %s\n", result.Message)

			if result.CooldownSeconds > 0 {
				fmt.Printf("⏱  Jump cooldown: %d seconds\n", result.CooldownSeconds)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to jump (required)")
	cmd.Flags().StringVar(&destinationSystem, "system", "", "Destination system symbol (e.g., X1-ALPHA) (required)")

	return cmd
}

// newShipSellCommand creates the ship sell subcommand
func newShipSellCommand() *cobra.Command {
	var (
		shipSymbol string
		goodSymbol string
		units      int
	)

	cmd := &cobra.Command{
		Use:   "sell",
		Short: "Sell cargo from a ship",
		Long: `Sell cargo from a ship at its current location.
Ship must be docked at a marketplace.

Examples:
  spacetraders ship sell --ship AGENT-1 --good IRON_ORE --units 50 --player-id 1
  spacetraders ship sell --ship ENDURANCE-1 --good IRON_ORE --units 100 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shipSymbol == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}
			if units <= 0 {
				return fmt.Errorf("--units must be greater than 0")
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

			// Create dependencies
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			waypointRepo := persistence.NewGormWaypointRepository(db)
			systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
			graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
			graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
			shipRepo := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService)
			marketRepo := persistence.NewMarketRepository(db)

			// Create mediator with ledger handlers registered
			transactionRepo := persistence.NewGormTransactionRepository(db)
			playerResolver := common.NewPlayerResolver(playerRepo)
			registry := setup.NewHandlerRegistry(
				transactionRepo,
				playerResolver,
				nil, // clock (defaults to real clock)
				nil, // marketRepo (not needed for this CLI command)
				nil, // shipRepo (not needed for this CLI command)
				nil, // waypointProvider (not needed for this CLI command)
				nil, // shipAssignmentRepo (not needed for this CLI command)
				nil, // daemonClient (not needed for this CLI command)
			)
			mediator, err := registry.CreateConfiguredMediator()
			if err != nil {
				return fmt.Errorf("failed to create mediator: %w", err)
			}

			// Create handler
			handler := shipCmd.NewSellCargoHandler(shipRepo, playerRepo, apiClient, marketRepo, mediator)

			// Resolve player ID
			ctx := context.Background()
			var resolvedPlayerID int
			if playerIdent.PlayerID > 0 {
				resolvedPlayerID = playerIdent.PlayerID
			} else {
				// Look up player by agent symbol
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve player from agent symbol: %w", err)
				}
				resolvedPlayerID = player.ID.Value()
			}

			// Execute command
			response, err := handler.Handle(ctx, &shipCmd.SellCargoCommand{
				ShipSymbol: shipSymbol,
				GoodSymbol: goodSymbol,
				Units:      units,
				PlayerID:   shared.MustNewPlayerID(resolvedPlayerID),
			})
			if err != nil {
				return fmt.Errorf("sell cargo command failed: %w", err)
			}

			result, ok := response.(*shipCmd.SellCargoResponse)
			if !ok {
				return fmt.Errorf("unexpected response type")
			}

			// Display success
			fmt.Println("✓ Cargo sold successfully")
			fmt.Printf("  Ship:          %s\n", shipSymbol)
			fmt.Printf("  Good:          %s\n", goodSymbol)
			fmt.Printf("  Units Sold:    %d\n", result.UnitsSold)
			fmt.Printf("  Total Revenue: %d credits\n", result.TotalRevenue)

			return nil
		},
	}

	cmd.Flags().StringVar(&shipSymbol, "ship", "", "Ship symbol to sell from (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Trade good symbol to sell (required)")
	cmd.Flags().IntVar(&units, "units", 0, "Number of units to sell (required)")

	return cmd
}
