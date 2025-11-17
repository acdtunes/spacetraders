package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// NewShipyardCommand creates the shipyard command with subcommands
func NewShipyardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shipyard",
		Short: "Manage shipyard operations",
		Long: `Manage shipyard operations including listing available ships and purchasing ships.

Shipyards sell ships of various types. Use these commands to browse available ships
and purchase new vessels for your fleet.

Examples:
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1 --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --player-id 1
  spacetraders shipyard batch-purchase --ship AGENT-1 --type SHIP_PROBE --quantity 5 --budget 500000 --player-id 1`,
	}

	// Add subcommands
	cmd.AddCommand(newShipyardListCommand())
	cmd.AddCommand(newShipyardPurchaseCommand())
	cmd.AddCommand(newShipyardBatchPurchaseCommand())

	return cmd
}

// newShipyardListCommand creates the shipyard list subcommand
func newShipyardListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <system-symbol> <waypoint-symbol>",
		Short: "List available ships at a shipyard",
		Long: `List available ships at a shipyard waypoint.

Shows ship types, names, descriptions, and purchase prices for all ships
available at the specified shipyard.

Examples:
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1 --player-id 1
  spacetraders shipyard list X1-GZ7 X1-GZ7-A1 --agent ENDURANCE`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			systemSymbol := args[0]
			waypointSymbol := args[1]

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Get daemon client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Call daemon via gRPC
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			response, err := client.GetShipyardListings(ctx, systemSymbol, waypointSymbol, playerIdent.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to get shipyard listings: %w", err)
			}

			if len(response.Listings) == 0 {
				fmt.Println("No ships available at this shipyard.")
				return nil
			}

			// Display table
			fmt.Printf("Shipyard: %s\n\n", response.ShipyardSymbol)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TYPE\tNAME\tPRICE\tDESCRIPTION")
			fmt.Fprintln(w, "----\t----\t-----\t-----------")

			for _, listing := range response.Listings {
				// Truncate description if too long
				description := listing.Description
				if len(description) > 60 {
					description = description[:57] + "..."
				}

				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n",
					listing.ShipType,
					listing.Name,
					listing.PurchasePrice,
					description,
				)
			}

			w.Flush()

			if response.ModificationFee > 0 {
				fmt.Printf("\nModification Fee: %d credits\n", response.ModificationFee)
			}

			return nil
		},
	}

	return cmd
}

// newShipyardPurchaseCommand creates the shipyard purchase subcommand
func newShipyardPurchaseCommand() *cobra.Command {
	var (
		purchasingShip   string
		shipType         string
		shipyardWaypoint string
	)

	cmd := &cobra.Command{
		Use:   "purchase",
		Short: "Purchase a ship from a shipyard",
		Long: `Purchase a ship from a shipyard.

The purchasing ship will:
1. Auto-discover nearest shipyard that sells the desired ship type (if not specified)
2. Navigate to the shipyard waypoint if not already there
3. Dock if in orbit
4. Purchase the specified ship type
5. Return the new ship entity

The operation runs in a background container that can be monitored.

Examples:
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_MINING_DRONE --waypoint X1-GZ7-A1 --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if purchasingShip == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if shipType == "" {
				return fmt.Errorf("--type flag is required")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Get daemon client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Call daemon via gRPC
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			response, err := client.PurchaseShip(ctx, purchasingShip, shipType, playerIdent.PlayerID, playerIdent.AgentSymbol, shipyardWaypoint)
			if err != nil {
				return fmt.Errorf("failed to purchase ship: %w", err)
			}

			// Display result
			fmt.Println("✓ Ship purchase started successfully")
			fmt.Printf("  Container ID:     %s\n", response.ContainerId)
			fmt.Printf("  Purchasing Ship:  %s\n", purchasingShip)
			fmt.Printf("  Ship Type:        %s\n", shipType)
			if shipyardWaypoint != "" {
				fmt.Printf("  Shipyard:         %s\n", shipyardWaypoint)
			} else {
				fmt.Printf("  Shipyard:         Auto-discovering...\n")
			}
			fmt.Printf("  Status:           %s\n", response.Status)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", response.ContainerId)

			return nil
		},
	}

	cmd.Flags().StringVar(&purchasingShip, "ship", "", "Ship symbol to use for navigation (required)")
	cmd.Flags().StringVar(&shipType, "type", "", "Ship type to purchase (e.g., SHIP_PROBE, SHIP_MINING_DRONE) (required)")
	cmd.Flags().StringVar(&shipyardWaypoint, "waypoint", "", "Shipyard waypoint (optional - will auto-discover if not provided)")

	return cmd
}

// newShipyardBatchPurchaseCommand creates the shipyard batch-purchase subcommand
func newShipyardBatchPurchaseCommand() *cobra.Command {
	var (
		purchasingShip   string
		shipType         string
		quantity         int
		maxBudget        int
		shipyardWaypoint string
	)

	cmd := &cobra.Command{
		Use:   "batch-purchase",
		Short: "Purchase multiple ships in batch",
		Long: `Purchase multiple ships of the same type in a batch.

The command will purchase as many ships as possible within constraints:
- Quantity requested
- Maximum budget allocated
- Player's available credits

The purchasing ship will be used to navigate to the shipyard if needed.
The operation runs in a background container that can be monitored.

Examples:
  spacetraders shipyard batch-purchase --ship AGENT-1 --type SHIP_PROBE --quantity 5 --budget 500000 --player-id 1
  spacetraders shipyard batch-purchase --ship AGENT-1 --type SHIP_MINING_DRONE --quantity 10 --budget 1000000 --waypoint X1-GZ7-A1 --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if purchasingShip == "" {
				return fmt.Errorf("--ship flag is required")
			}
			if shipType == "" {
				return fmt.Errorf("--type flag is required")
			}
			if quantity <= 0 {
				return fmt.Errorf("--quantity must be greater than 0")
			}
			if maxBudget <= 0 {
				return fmt.Errorf("--budget must be greater than 0")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Get daemon client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Call daemon via gRPC
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			response, err := client.BatchPurchaseShips(ctx, purchasingShip, shipType, quantity, maxBudget, playerIdent.PlayerID, playerIdent.AgentSymbol, shipyardWaypoint)
			if err != nil {
				return fmt.Errorf("failed to batch purchase ships: %w", err)
			}

			// Display result
			fmt.Println("✓ Batch ship purchase started successfully")
			fmt.Printf("  Container ID:     %s\n", response.ContainerId)
			fmt.Printf("  Purchasing Ship:  %s\n", purchasingShip)
			fmt.Printf("  Ship Type:        %s\n", shipType)
			fmt.Printf("  Quantity:         %d\n", quantity)
			fmt.Printf("  Max Budget:       %d credits\n", maxBudget)
			if shipyardWaypoint != "" {
				fmt.Printf("  Shipyard:         %s\n", shipyardWaypoint)
			} else {
				fmt.Printf("  Shipyard:         Auto-discovering...\n")
			}
			fmt.Printf("  Status:           %s\n", response.Status)
			fmt.Printf("\nTrack progress with: spacetraders container logs %s\n", response.ContainerId)

			return nil
		},
	}

	cmd.Flags().StringVar(&purchasingShip, "ship", "", "Ship symbol to use for navigation (required)")
	cmd.Flags().StringVar(&shipType, "type", "", "Ship type to purchase (e.g., SHIP_PROBE, SHIP_MINING_DRONE) (required)")
	cmd.Flags().IntVar(&quantity, "quantity", 0, "Number of ships to purchase (required)")
	cmd.Flags().IntVar(&maxBudget, "budget", 0, "Maximum budget in credits (required)")
	cmd.Flags().StringVar(&shipyardWaypoint, "waypoint", "", "Shipyard waypoint (optional - will auto-discover if not provided)")

	return cmd
}
