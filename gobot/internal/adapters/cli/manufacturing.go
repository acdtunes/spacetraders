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

// newManufacturingStartCommand creates the manufacturing start subcommand (task-based pipeline)
func newManufacturingStartCommand() *cobra.Command {
	var systemSymbol string
	var minPrice int
	var maxWorkers int
	var minBalance int
	var dryRun bool
	var limit int
	var strategy string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start task-based manufacturing coordinator",
		Long: `Start a task-based manufacturing coordinator for a system.

This manufacturing system uses a task-based pipeline architecture:
- Creates manufacturing pipelines with atomic tasks (ACQUIRE, DELIVER, COLLECT, SELL)
- Manages task dependencies and executes them in parallel when possible
- Tracks factory state for production timing
- Monitors supply levels to determine when production is ready
- Persists pipeline state for crash recovery

The coordinator runs indefinitely until stopped with 'container stop'.

Use --dry-run to preview the planned tasks without executing them.

Acquisition strategies (--strategy):
  prefer-buy:       Always buy if a market exists (fastest, default)
  prefer-fabricate: Fabricate unless supply is HIGH/ABUNDANT (aggressive)
  smart:            Fabricate only when supply is SCARCE/LIMITED (conservative)

Examples:
  spacetraders manufacturing start --system X1-AU21
  spacetraders manufacturing start --system X1-AU21 --min-price 2000 --max-workers 3
  spacetraders manufacturing start --system X1-AU21 --dry-run
  spacetraders manufacturing start --system X1-AU21 --dry-run --strategy smart
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

			// Handle dry-run mode
			if dryRun {
				return runDryRunMode(systemSymbol, playerIdent, minPrice, limit, strategy)
			}

			// Connect to daemon
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Resolve player ID
			playerID := playerIdent.PlayerID

			// Start parallel coordinator
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.StartParallelManufacturingCoordinator(ctx, systemSymbol, playerID, minPrice, maxWorkers, minBalance)
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
			fmt.Println("\nThis coordinator uses task-based pipelines with:")
			fmt.Println("  - Atomic tasks (ACQUIRE, DELIVER, COLLECT, SELL)")
			fmt.Println("  - Dependency-based execution")
			fmt.Println("  - Factory state tracking for production timing")
			fmt.Println("  - Persistent state for crash recovery")
			fmt.Println("\nUse 'spacetraders container logs " + result.ContainerID + "' to monitor progress")
			fmt.Println("Use 'spacetraders container stop " + result.ContainerID + "' to stop")

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol to manufacture in (required)")
	cmd.Flags().IntVar(&minPrice, "min-price", 1000, "Minimum purchase price threshold for opportunities")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 5, "Maximum parallel manufacturing workers")
	cmd.Flags().IntVar(&minBalance, "min-balance", 0, "Minimum credit balance to maintain (0 = no limit)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview planned tasks without executing")
	cmd.Flags().IntVar(&limit, "limit", 3, "Maximum opportunities to plan in dry-run mode")
	cmd.Flags().StringVar(&strategy, "strategy", "prefer-buy", "Acquisition strategy: prefer-buy, prefer-fabricate, smart")
	cmd.MarkFlagRequired("system")

	return cmd
}

// runDryRunMode displays planned tasks without executing them
func runDryRunMode(systemSymbol string, playerIdent *PlayerIdentifier, minPrice int, limit int, strategyStr string) error {
	// Parse strategy
	strategy := goodsServices.StrategyPreferBuy // default
	switch strategyStr {
	case "prefer-buy":
		strategy = goodsServices.StrategyPreferBuy
	case "prefer-fabricate":
		strategy = goodsServices.StrategyPreferFabricate
	case "smart":
		strategy = goodsServices.StrategySmart
	default:
		return fmt.Errorf("unknown strategy: %s (valid: prefer-buy, prefer-fabricate, smart)", strategyStr)
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
	supplyChainResolver := goodsServices.NewSupplyChainResolverWithStrategy(goods.ExportToImportMap, marketRepo, strategy)
	// For dry-run, we only need marketRepo for market lookups - pass nil for unused deps
	marketLocator := goodsServices.NewMarketLocator(marketRepo, nil, nil, nil)

	demandFinder := services.NewManufacturingDemandFinder(
		marketRepo,
		waypointProvider,
		goods.ExportToImportMap,
		supplyChainResolver,
	)

	pipelinePlanner := services.NewPipelinePlanner(marketLocator)

	// Scan for opportunities
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	finderConfig := services.DemandFinderConfig{
		MinPurchasePrice: minPrice,
		MaxOpportunities: limit,
	}

	opportunities, err := demandFinder.FindHighDemandManufacturables(ctx, systemSymbol, playerID, finderConfig)
	if err != nil {
		return fmt.Errorf("failed to scan opportunities: %w", err)
	}

	if len(opportunities) == 0 {
		fmt.Println("\n[DRY RUN] No manufacturing opportunities found.")
		fmt.Printf("  System:     %s\n", systemSymbol)
		fmt.Printf("  Min Price:  %d\n", minPrice)
		fmt.Printf("  Strategy:   %s\n", strategy)
		return nil
	}

	fmt.Printf("\n[DRY RUN] Found %d opportunities in %s (strategy: %s)\n", len(opportunities), systemSymbol, strategy)
	fmt.Println("═══════════════════════════════════════════════════════════════════════")

	// Plan each opportunity and display tasks
	for i, opp := range opportunities {
		fmt.Printf("\n┌─ Opportunity %d: %s ─────────────────────────────────────────────────\n", i+1, opp.Good())
		fmt.Printf("│  Sell Market:    %s\n", opp.SellMarket().Symbol)
		fmt.Printf("│  Purchase Price: %d credits\n", opp.PurchasePrice())
		fmt.Printf("│  Activity:       %s\n", opp.Activity())
		fmt.Printf("│  Supply:         %s\n", opp.Supply())
		fmt.Printf("│  Score:          %.1f\n", opp.Score())
		fmt.Println("│")

		// Create pipeline to get tasks
		pipeline, tasks, factoryStates, err := pipelinePlanner.CreatePipeline(ctx, opp, systemSymbol, playerID)
		if err != nil {
			fmt.Printf("│  ⚠ Failed to plan: %v\n", err)
			fmt.Println("└────────────────────────────────────────────────────────────────────────")
			continue
		}

		fmt.Printf("│  Pipeline ID:    %s\n", truncate(pipeline.ID(), 16))
		fmt.Printf("│  Total Tasks:    %d\n", len(tasks))
		fmt.Printf("│  Factory States: %d\n", len(factoryStates))
		fmt.Println("│")
		fmt.Println("│  PLANNED TASKS:")
		fmt.Println("│  ──────────────")

		// Group tasks by type for display
		for _, task := range tasks {
			deps := ""
			if len(task.DependsOn()) > 0 {
				deps = fmt.Sprintf(" (depends: %d tasks)", len(task.DependsOn()))
			}

			// Use GetDestination() which returns the correct location based on task type
			location := task.GetDestination()
			if location == "" {
				location = "(unknown)"
			}

			fmt.Printf("│    [%s] %s at %s%s\n",
				task.TaskType(),
				task.Good(),
				location,
				deps,
			)
		}

		fmt.Println("│")
		fmt.Println("│  FACTORY STATES:")
		fmt.Println("│  ───────────────")
		for _, fs := range factoryStates {
			fmt.Printf("│    %s produces %s (inputs: %v)\n",
				truncate(fs.FactorySymbol(), 14),
				fs.OutputGood(),
				fs.RequiredInputs(),
			)
		}

		fmt.Println("└────────────────────────────────────────────────────────────────────────")
	}

	fmt.Println("\n[DRY RUN] No tasks were executed. Remove --dry-run to start manufacturing.")

	return nil
}
