package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// dbWaypointProviderAdapter adapts GormWaypointRepository to IWaypointProvider interface
type dbWaypointProviderAdapter struct {
	repo *persistence.GormWaypointRepository
}

func newDBWaypointProviderAdapter(repo *persistence.GormWaypointRepository) system.IWaypointProvider {
	return &dbWaypointProviderAdapter{repo: repo}
}

func (a *dbWaypointProviderAdapter) GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error) {
	// Ignore playerID - the DB adapter doesn't need it for lookups
	return a.repo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
}

// NewManufacturingCommand creates the manufacturing command with subcommands
func NewManufacturingCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manufacturing",
		Short: "Manage automated manufacturing arbitrage operations",
		Long: `Manage automated manufacturing arbitrage operations.

The manufacturing system discovers high-demand goods (markets with high import prices),
manufactures them using the supply chain, and sells them for profit.

Unlike traditional arbitrage (buy low, sell high), this system:
- Identifies demand by finding high import prices at markets
- Uses the factory/supply chain system to produce goods
- Leverages idle hauler ships (shared with arbitrage pool)
- Coordinates parallel manufacturing operations

Examples:
  spacetraders manufacturing scan --system X1-AU21
  spacetraders manufacturing start --system X1-AU21`,
	}

	// Add subcommands
	cmd.AddCommand(newManufacturingScanCommand())
	cmd.AddCommand(newManufacturingStartCommand())

	return cmd
}

// newManufacturingScanCommand creates the manufacturing scan subcommand
func newManufacturingScanCommand() *cobra.Command {
	var systemSymbol string
	var minPrice int
	var limit int

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for manufacturing opportunities in a system",
		Long: `Scan all markets in a system for manufacturing opportunities.

The scan will:
- Find goods with high import prices (demand signal)
- Filter to goods that can be manufactured (exist in supply chain)
- Build dependency trees for each good
- Score opportunities based on price, activity, supply, and complexity
- Display the top-ranked opportunities

Scoring factors (data-driven):
- Purchase price (40%): Higher price = more potential revenue
- Activity (30%): WEAK markets = stable prices (best)
- Supply (20%): Higher supply = more volume opportunity
- Tree depth (10%): Shallower trees = faster execution

Examples:
  spacetraders manufacturing scan --system X1-AU21
  spacetraders manufacturing scan --system X1-AU21 --min-price 2000 --limit 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Require system symbol
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
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

			// Resolve player ID if needed
			playerID := playerIdent.PlayerID
			if playerID == 0 && playerIdent.AgentSymbol != "" {
				playerRepo := persistence.NewGormPlayerRepository(db)
				ctx := context.Background()
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve agent %s to player ID: %w", playerIdent.AgentSymbol, err)
				}
				playerID = player.ID.Value()
			}

			// Create repositories and services
			marketRepo := persistence.NewMarketRepository(db)
			waypointRepo := persistence.NewGormWaypointRepository(db)
			waypointProvider := newDBWaypointProviderAdapter(waypointRepo)
			supplyChainResolver := goodsServices.NewSupplyChainResolver(goods.ExportToImportMap, marketRepo)

			demandFinder := services.NewManufacturingDemandFinder(
				marketRepo,
				waypointProvider,
				goods.ExportToImportMap,
				supplyChainResolver,
			)

			// Scan for opportunities
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			config := services.DemandFinderConfig{
				MinPurchasePrice: minPrice,
				MaxOpportunities: limit,
			}

			opportunities, err := demandFinder.FindHighDemandManufacturables(ctx, systemSymbol, playerID, config)
			if err != nil {
				return fmt.Errorf("failed to scan opportunities: %w", err)
			}

			// Display results
			if len(opportunities) == 0 {
				fmt.Println("No manufacturing opportunities found.")
				fmt.Printf("  System:       %s\n", systemSymbol)
				fmt.Printf("  Min Price:    %d\n", minPrice)
				return nil
			}

			fmt.Printf("\nFound %d manufacturing opportunities in %s:\n\n", len(opportunities), systemSymbol)
			displayManufacturingOpportunities(opportunities)

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol to scan (required)")
	cmd.Flags().IntVar(&minPrice, "min-price", 1000, "Minimum purchase price threshold")
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum opportunities to display")
	cmd.MarkFlagRequired("system")

	return cmd
}

// displayManufacturingOpportunities formats and displays manufacturing opportunities
func displayManufacturingOpportunities(opps []*trading.ManufacturingOpportunity) {
	fmt.Println("┌──────┬────────────────────┬────────────────┬─────────────┬──────────┬─────────┬───────┬────────┬───────┐")
	fmt.Println("│ Rank │ Good               │ Sell Market    │ Price       │ Activity │ Supply  │ Depth │ Inputs │ Score │")
	fmt.Println("├──────┼────────────────────┼────────────────┼─────────────┼──────────┼─────────┼───────┼────────┼───────┤")

	for i, opp := range opps {
		// Get activity with default
		activity := opp.Activity()
		if activity == "" {
			activity = "N/A"
		}
		// Get supply with default
		supply := opp.Supply()
		if supply == "" {
			supply = "N/A"
		}

		fmt.Printf("│ %4d │ %-18s │ %-14s │ %11d │ %-8s │ %-7s │ %5d │ %6d │ %5.1f │\n",
			i+1,
			truncate(opp.Good(), 18),
			truncate(opp.SellMarket().Symbol, 14),
			opp.PurchasePrice(),
			truncate(activity, 8),
			truncate(supply, 7),
			opp.TreeDepth(),
			opp.InputCount(),
			opp.Score(),
		)
	}

	fmt.Println("└──────┴────────────────────┴────────────────┴─────────────┴──────────┴─────────┴───────┴────────┴───────┘")

	fmt.Println("\nScoring factors (data-driven from arbitrage analysis):")
	fmt.Println("  - Purchase Price (40%): Higher price = more potential revenue")
	fmt.Println("  - Activity (30%): WEAK=100, RESTRICTED=50, STRONG=25, GROWING=0 (WEAK is best!)")
	fmt.Println("  - Supply (20%): ABUNDANT=100, HIGH=80, MODERATE=60, LIMITED=40, SCARCE=20")
	fmt.Println("  - Tree Depth (10%): Shallower trees = faster execution (100 - depth*20)")
	fmt.Println("\nNote: WEAK activity markets have stable prices; GROWING markets have volatile prices.")
}

// newManufacturingStartCommand creates the manufacturing start subcommand
func newManufacturingStartCommand() *cobra.Command {
	var systemSymbol string
	var minPrice int
	var maxWorkers int
	var minBalance int

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start automated manufacturing coordinator",
		Long: `Start an automated manufacturing coordinator for a system.

The coordinator will:
- Continuously scan for high-demand manufacturable goods
- Spawn worker containers to produce and sell goods
- Auto-discover idle hauler ships from shared pool
- Run parallel manufacturing operations (max controlled by --max-workers)
- Automatically restart failed workers

The coordinator runs indefinitely until stopped with 'container stop'.

Examples:
  spacetraders manufacturing start --system X1-AU21
  spacetraders manufacturing start --system X1-AU21 --min-price 2000 --max-workers 3
  spacetraders manufacturing start --system X1-AU21 --min-balance 100000`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Require system symbol
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
			}

			// Connect to daemon
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Resolve player ID
			playerID := playerIdent.PlayerID

			// Start coordinator
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.StartManufacturingCoordinator(ctx, systemSymbol, playerID, minPrice, maxWorkers, minBalance)
			if err != nil {
				return fmt.Errorf("failed to start manufacturing coordinator: %w", err)
			}

			// Display result
			fmt.Println("\nManufacturing Coordinator Started")
			fmt.Println("=================================")
			fmt.Printf("Container ID:   %s\n", result.ContainerID)
			fmt.Printf("System:         %s\n", result.SystemSymbol)
			fmt.Printf("Min Price:      %d\n", result.MinPrice)
			fmt.Printf("Max Workers:    %d\n", result.MaxWorkers)
			if result.MinBalance > 0 {
				fmt.Printf("Min Balance:    %d\n", result.MinBalance)
			}
			fmt.Printf("Status:         %s\n", result.Status)
			fmt.Printf("\n%s\n", result.Message)
			fmt.Println("\nUse 'spacetraders container logs " + result.ContainerID + "' to monitor progress")
			fmt.Println("Use 'spacetraders container stop " + result.ContainerID + "' to stop")

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol to manufacture in (required)")
	cmd.Flags().IntVar(&minPrice, "min-price", 1000, "Minimum purchase price threshold for opportunities")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 5, "Maximum parallel manufacturing workers")
	cmd.Flags().IntVar(&minBalance, "min-balance", 0, "Minimum credit balance to maintain (0 = no limit)")
	cmd.MarkFlagRequired("system")

	return cmd
}
