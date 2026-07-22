package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
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
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --quantity 5 --budget 500000 --player-id 1`,
	}

	// Add subcommands
	cmd.AddCommand(newShipyardListCommand())
	cmd.AddCommand(newShipyardPurchaseCommand())
	cmd.AddCommand(newShipyardYardsCommand())

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
			client, err := connectDaemon()
			if err != nil {
				return err
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
		quantity         int
		maxBudget        int
		shipyardWaypoint string
	)

	cmd := &cobra.Command{
		Use:   "purchase",
		Short: "Purchase ships from a shipyard",
		Long: `Purchase one or more ships from a shipyard.

The command will purchase ships within the following constraints:
- Quantity requested (default: 1)
- Maximum budget allocated (if specified, 0 = no limit)
- Player's available credits

The purchasing ship will:
1. Auto-discover nearest shipyard that sells the desired ship type (if not specified)
2. Navigate to the shipyard waypoint if not already there
3. Dock if in orbit
4. Purchase the specified ship(s)

The operation runs in a background container that can be monitored.

Examples:
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_PROBE --quantity 5 --budget 500000 --player-id 1
  spacetraders shipyard purchase --ship AGENT-1 --type SHIP_MINING_DRONE --quantity 10 --waypoint X1-GZ7-A1 --player-id 1`,
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
			if maxBudget < 0 {
				return fmt.Errorf("--budget cannot be negative")
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Get daemon client
			client, err := connectDaemon()
			if err != nil {
				return err
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
			fmt.Println("✓ Ship purchase started successfully")
			fmt.Printf("  Container ID:     %s\n", response.ContainerId)
			fmt.Printf("  Purchasing Ship:  %s\n", purchasingShip)
			fmt.Printf("  Ship Type:        %s\n", shipType)
			fmt.Printf("  Quantity:         %d\n", quantity)
			if maxBudget > 0 {
				fmt.Printf("  Max Budget:       %d credits\n", maxBudget)
			} else {
				fmt.Printf("  Max Budget:       No limit\n")
			}
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
	cmd.Flags().IntVar(&quantity, "quantity", 1, "Number of ships to purchase (default: 1)")
	cmd.Flags().IntVar(&maxBudget, "budget", 0, "Maximum budget in credits (0 = no limit, default: 0)")
	cmd.Flags().StringVar(&shipyardWaypoint, "waypoint", "", "Shipyard waypoint (optional - will auto-discover if not provided)")

	return cmd
}

// shipyardYardsProvider is the narrow read seam newShipyardYardsCommand needs
// from the saved shipyard-inventory scan cache. Satisfied by
// *persistence.ShipyardInventoryRepositoryGORM; a fake stands in for CLI
// rendering tests (shipyard_yards_test.go) — era-scoping and price ordering
// are real behavior asserted separately against a real DB
// (shipyard_inventory_repository_test.go).
type shipyardYardsProvider interface {
	ListSavedYards(ctx context.Context, playerID int, shipTypes []string) ([]shipyard.ShipTypeAvailability, error)
}

// newShipyardYardsCommand creates the shipyard yards subcommand (sp-qx29f):
// a read-only query over the SAVED shipyard_inventory scan cache, unlike
// `shipyard list` which requires a ship physically docked at the yard.
func newShipyardYardsCommand() *cobra.Command {
	var shipTypes []string

	cmd := &cobra.Command{
		Use:   "yards",
		Short: "List saved shipyards dealing in a ship type (current era only, no ship presence required)",
		Long: `Query the SAVED shipyard_inventory scan cache for yards dealing in the given
ship type(s) — the daemon's per-yard catalog written by the shipyard scanner.

Unlike 'shipyard list', NO ship needs to be physically present and docked at
the yard: this reads data already collected by prior scans. Results are
scoped to the CURRENT open era (a yard whose only rows are from a closed era
is excluded) and ordered by purchase price ascending.

Examples:
  spacetraders shipyard yards --type SHIP_HEAVY_FREIGHTER --player-id 1
  spacetraders shipyard yards --type SHIP_HEAVY_FREIGHTER --type SHIP_PROBE --player-id 1
  spacetraders shipyard yards --player-id 1   # every saved yard, any type`,
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			repo := persistence.NewShipyardInventoryRepository(db)
			return runShipyardYards(context.Background(), repo, os.Stdout, playerIdent.PlayerID, shipTypes)
		},
	}

	cmd.Flags().StringSliceVar(&shipTypes, "type", nil, "Ship type(s) to filter by (repeatable or comma-list; omit for every saved type)")

	return cmd
}

func runShipyardYards(ctx context.Context, p shipyardYardsProvider, out io.Writer, playerID int, shipTypes []string) error {
	rows, err := p.ListSavedYards(ctx, playerID, shipTypes)
	if err != nil {
		return fmt.Errorf("failed to list saved yards: %w", err)
	}

	if len(rows) == 0 {
		fmt.Fprintln(out, "No saved yards found.")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SYSTEM\tWAYPOINT\tTYPE\tPRICE\tSUPPLY\tLAST SCANNED")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			r.SystemSymbol, r.WaypointSymbol, r.ShipType, r.PurchasePrice, r.Supply,
			r.LastScanned.Format("2006-01-02 15:04:05"))
	}
	w.Flush()

	return nil
}
