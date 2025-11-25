package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewArbitrageCommand creates the arbitrage command with subcommands
func NewArbitrageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "arbitrage",
		Short: "Manage automated arbitrage trading operations",
		Long: `Manage automated arbitrage trading operations.

The arbitrage system discovers and executes profitable buy-sell opportunities
across markets within a single system, coordinating multiple ships in parallel.

Examples:
  spacetraders arbitrage scan --system X1-AU21
  spacetraders arbitrage start --system X1-AU21
  spacetraders arbitrage scan --system X1-AU21 --min-margin 15.0
  spacetraders arbitrage start --system X1-AU21 --max-workers 5`,
	}

	// Add subcommands
	cmd.AddCommand(newArbitrageScanCommand())
	cmd.AddCommand(newArbitrageStartCommand())
	cmd.AddCommand(newArbitrageExportDataCommand())
	cmd.AddCommand(newArbitrageDataStatsCommand())

	return cmd
}

// newArbitrageScanCommand creates the arbitrage scan subcommand
func newArbitrageScanCommand() *cobra.Command {
	var systemSymbol string
	var minMargin float64
	var limit int

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for arbitrage opportunities in a system",
		Long: `Scan all markets in a system for arbitrage opportunities.

The scan will:
- Analyze all market pairs for profitable buy-sell opportunities
- Filter by minimum profit margin threshold
- Score opportunities based on profit, supply, activity, and distance
- Display the top-ranked opportunities

Examples:
  spacetraders arbitrage scan --system X1-AU21
  spacetraders arbitrage scan --system X1-AU21 --min-margin 15.0 --limit 10`,
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

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Resolve agent symbol to player ID if needed
			resolvedPlayerID := playerIdent.PlayerID
			if resolvedPlayerID == 0 && playerIdent.AgentSymbol != "" {
				// Need to connect to database to resolve agent symbol
				cfg, err := config.LoadConfig("")
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}

				db, err := database.NewConnection(&cfg.Database)
				if err != nil {
					return fmt.Errorf("failed to connect to database: %w", err)
				}

				playerRepo := persistence.NewGormPlayerRepository(db)
				ctx := context.Background()
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve agent %s to player ID: %w", playerIdent.AgentSymbol, err)
				}
				resolvedPlayerID = player.ID.Value()
			}

			// Scan for opportunities
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.ScanArbitrageOpportunities(ctx, systemSymbol, resolvedPlayerID, minMargin, limit)
			if err != nil {
				return fmt.Errorf("failed to scan opportunities: %w", err)
			}

			// Display results
			if len(result.Opportunities) == 0 {
				fmt.Println("No arbitrage opportunities found.")
				fmt.Printf("  System:       %s\n", systemSymbol)
				fmt.Printf("  Min Margin:   %.1f%%\n", minMargin)
				return nil
			}

			fmt.Printf("\nFound %d arbitrage opportunities in %s:\n\n", len(result.Opportunities), systemSymbol)
			displayOpportunities(result.Opportunities)

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol to scan (required)")
	cmd.Flags().Float64Var(&minMargin, "min-margin", 10.0, "Minimum profit margin percentage")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum opportunities to display")
	cmd.MarkFlagRequired("system")

	return cmd
}

// newArbitrageStartCommand creates the arbitrage start subcommand
func newArbitrageStartCommand() *cobra.Command {
	var systemSymbol string
	var minMargin float64
	var maxWorkers int
	var minBalance int

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start arbitrage coordinator for continuous trading",
		Long: `Start an arbitrage coordinator for continuous automated trading.

The coordinator will:
- Scan for opportunities every 2 minutes
- Discover idle ships every 30 seconds
- Spawn parallel workers to execute trades
- Continue running until stopped

Multiple ships can trade different opportunities simultaneously.

Examples:
  spacetraders arbitrage start --system X1-AU21
  spacetraders arbitrage start --system X1-AU21 --min-margin 15.0 --max-workers 5
  spacetraders arbitrage start --system X1-AU21 --min-balance 50000`,
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

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Resolve agent symbol to player ID if needed
			resolvedPlayerID := playerIdent.PlayerID
			if resolvedPlayerID == 0 && playerIdent.AgentSymbol != "" {
				// Need to connect to database to resolve agent symbol
				cfg, err := config.LoadConfig("")
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}

				db, err := database.NewConnection(&cfg.Database)
				if err != nil {
					return fmt.Errorf("failed to connect to database: %w", err)
				}

				playerRepo := persistence.NewGormPlayerRepository(db)
				ctx := context.Background()
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve agent %s to player ID: %w", playerIdent.AgentSymbol, err)
				}
				resolvedPlayerID = player.ID.Value()
			}

			// Start coordinator
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.StartArbitrageCoordinator(ctx, systemSymbol, resolvedPlayerID, minMargin, maxWorkers, minBalance)
			if err != nil {
				return fmt.Errorf("failed to start coordinator: %w", err)
			}

			// Display result
			fmt.Println("✓ Arbitrage coordinator started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  System:           %s\n", systemSymbol)
			fmt.Printf("  Min Margin:       %.1f%%\n", minMargin)
			fmt.Printf("  Max Workers:      %d\n", maxWorkers)
			if minBalance > 0 {
				fmt.Printf("  Min Balance:      %d credits\n", minBalance)
			}
			fmt.Printf("  Agent:            %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Printf("  Status:           %s\n", result.Status)
			if result.Message != "" {
				fmt.Printf("  Message:          %s\n", result.Message)
			}
			fmt.Println("\n  The coordinator is scanning for opportunities and dispatching workers.")
			fmt.Println("  Use 'spacetraders container status " + result.ContainerID + "' to check progress.")
			fmt.Println("  Use 'spacetraders container stop " + result.ContainerID + "' to stop the coordinator.")

			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol to trade in (required)")
	cmd.Flags().Float64Var(&minMargin, "min-margin", 10.0, "Minimum profit margin percentage")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 10, "Maximum parallel workers")
	cmd.Flags().IntVar(&minBalance, "min-balance", 0, "Minimum credit balance to maintain (0 = no limit)")
	cmd.MarkFlagRequired("system")

	return cmd
}

// displayOpportunities formats and displays arbitrage opportunities
func displayOpportunities(opps []ArbitrageOpportunityResult) {
	// Table header
	fmt.Println("┌──────┬────────────────┬────────────────┬────────────────┬───────────┬────────┬─────────┬───────┐")
	fmt.Println("│ Rank │ Good           │ Buy Market     │ Sell Market    │ Margin %  │ Profit │ Supply  │ Score │")
	fmt.Println("├──────┼────────────────┼────────────────┼────────────────┼───────────┼────────┼─────────┼───────┤")

	for i, opp := range opps {
		fmt.Printf("│ %4d │ %-14s │ %-14s │ %-14s │ %8.1f%% │ %6d │ %-7s │ %5.0f │\n",
			i+1,
			truncateString(opp.Good, 14),
			truncateString(opp.BuyMarket, 14),
			truncateString(opp.SellMarket, 14),
			opp.ProfitMargin,
			opp.EstimatedProfit,
			opp.BuySupply,
			opp.Score,
		)
	}

	fmt.Println("└──────┴────────────────┴────────────────┴────────────────┴───────────┴────────┴─────────┴───────┘")
}

// truncateString truncates a string to a maximum length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// newArbitrageExportDataCommand creates the arbitrage export-data subcommand
func newArbitrageExportDataCommand() *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "export-data",
		Short: "Export arbitrage execution logs to CSV for ML training",
		Long: `Export arbitrage execution logs to a CSV file for machine learning training.

The CSV includes all successful execution logs with features such as:
- Opportunity details (good, markets, prices, margins)
- Ship state (cargo, fuel)
- Execution results (profit, duration, units traded)
- Derived metrics (profit per second, margin accuracy)

The exported data can be used to train genetic algorithms or ML models
to optimize opportunity scoring and selection.

Examples:
  spacetraders arbitrage export-data --output training_data.csv
  spacetraders arbitrage export-data --output /path/to/data.csv --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Require output path
			if outputPath == "" {
				return fmt.Errorf("--output flag is required")
			}

			// Connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}
			defer database.Close(db)

			// Create repository
			logRepo := persistence.NewGormArbitrageExecutionLogRepository(db)

			// Resolve player ID if needed
			resolvedPlayerID := playerIdent.PlayerID
			if resolvedPlayerID == 0 && playerIdent.AgentSymbol != "" {
				playerRepo := persistence.NewGormPlayerRepository(db)
				ctx := context.Background()
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve agent %s to player ID: %w", playerIdent.AgentSymbol, err)
				}
				resolvedPlayerID = player.ID.Value()
			}

			// Convert to domain type
			playerID, err := resolvePlayerID(resolvedPlayerID)
			if err != nil {
				return fmt.Errorf("invalid player ID: %w", err)
			}

			// Export to CSV
			fmt.Printf("Exporting execution logs for player %d...\n", resolvedPlayerID)

			ctx := context.Background()
			if err := logRepo.ExportToCSV(ctx, playerID, outputPath); err != nil {
				return fmt.Errorf("failed to export data: %w", err)
			}

			// Get stats
			count, err := logRepo.CountByPlayerID(ctx, playerID)
			if err != nil {
				return fmt.Errorf("failed to count logs: %w", err)
			}

			fmt.Println("✓ Data exported successfully")
			fmt.Printf("  Output file:      %s\n", outputPath)
			fmt.Printf("  Total logs:       %d\n", count)
			fmt.Printf("  Player ID:        %d\n", resolvedPlayerID)
			if playerIdent.AgentSymbol != "" {
				fmt.Printf("  Agent:            %s\n", playerIdent.AgentSymbol)
			}
			fmt.Println("\nThe CSV file contains all successful arbitrage execution logs.")
			fmt.Println("Use this data to train genetic algorithms or machine learning models.")

			return nil
		},
	}

	cmd.Flags().StringVar(&outputPath, "output", "", "Output CSV file path (required)")
	cmd.MarkFlagRequired("output")

	return cmd
}

// newArbitrageDataStatsCommand creates the arbitrage data-stats subcommand
func newArbitrageDataStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data-stats",
		Short: "Display statistics about arbitrage execution logs",
		Long: `Display statistics about collected arbitrage execution logs.

Shows metrics including:
- Total number of execution logs
- Success/failure rates
- Average profit per execution
- Average duration
- Data collection rate

Use this command to monitor data collection progress before
training optimization models.

Examples:
  spacetraders arbitrage data-stats
  spacetraders arbitrage data-stats --player-id 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}
			defer database.Close(db)

			// Create repository
			logRepo := persistence.NewGormArbitrageExecutionLogRepository(db)

			// Resolve player ID if needed
			resolvedPlayerID := playerIdent.PlayerID
			if resolvedPlayerID == 0 && playerIdent.AgentSymbol != "" {
				playerRepo := persistence.NewGormPlayerRepository(db)
				ctx := context.Background()
				player, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
				if err != nil {
					return fmt.Errorf("failed to resolve agent %s to player ID: %w", playerIdent.AgentSymbol, err)
				}
				resolvedPlayerID = player.ID.Value()
			}

			// Convert to domain type
			playerID, err := resolvePlayerID(resolvedPlayerID)
			if err != nil {
				return fmt.Errorf("invalid player ID: %w", err)
			}

			// Get statistics
			ctx := context.Background()

			totalCount, err := logRepo.CountByPlayerID(ctx, playerID)
			if err != nil {
				return fmt.Errorf("failed to count logs: %w", err)
			}

			successfulLogs, err := logRepo.FindSuccessfulRuns(ctx, playerID, 0)
			if err != nil {
				return fmt.Errorf("failed to retrieve successful logs: %w", err)
			}

			// Calculate statistics
			successCount := len(successfulLogs)
			failureCount := totalCount - successCount
			successRate := 0.0
			if totalCount > 0 {
				successRate = float64(successCount) / float64(totalCount) * 100.0
			}

			avgProfit := 0.0
			avgDuration := 0.0
			avgProfitPerSecond := 0.0

			if successCount > 0 {
				totalProfit := 0
				totalDuration := 0
				totalProfitPerSec := 0.0

				for _, log := range successfulLogs {
					totalProfit += log.ActualNetProfit()
					totalDuration += log.ActualDuration()
					totalProfitPerSec += log.ProfitPerSecond()
				}

				avgProfit = float64(totalProfit) / float64(successCount)
				avgDuration = float64(totalDuration) / float64(successCount)
				avgProfitPerSecond = totalProfitPerSec / float64(successCount)
			}

			// Display statistics
			fmt.Println("\n=== Arbitrage Execution Log Statistics ===")
			fmt.Printf("Player ID:              %d\n", resolvedPlayerID)
			if playerIdent.AgentSymbol != "" {
				fmt.Printf("Agent:                  %s\n", playerIdent.AgentSymbol)
			}
			fmt.Println()
			fmt.Printf("Total Executions:       %d\n", totalCount)
			fmt.Printf("  Successful:           %d (%.1f%%)\n", successCount, successRate)
			fmt.Printf("  Failed:               %d (%.1f%%)\n", failureCount, 100.0-successRate)
			fmt.Println()

			if successCount > 0 {
				fmt.Printf("Average Metrics (Successful):\n")
				fmt.Printf("  Profit per trade:     %.0f credits\n", avgProfit)
				fmt.Printf("  Duration per trade:   %.0f seconds\n", avgDuration)
				fmt.Printf("  Profit per second:    %.2f credits/sec\n", avgProfitPerSecond)
				fmt.Println()
			}

			fmt.Println("Data Collection Status:")
			if totalCount == 0 {
				fmt.Println("  ⚠ No execution logs collected yet")
				fmt.Println("  Run arbitrage operations to start collecting training data")
			} else if successCount < 100 {
				fmt.Printf("  ⚠ Insufficient data for Genetic Algorithm (%d < 100)\n", successCount)
				fmt.Println("  Continue running arbitrage to collect more data")
			} else if successCount < 1000 {
				fmt.Printf("  ✓ Sufficient for Genetic Algorithm (%d >= 100)\n", successCount)
				fmt.Printf("  ⚠ Insufficient for ML training (%d < 1000)\n", successCount)
				fmt.Println("  Continue running to enable ML optimization")
			} else {
				fmt.Printf("  ✓ Excellent dataset for ML training (%d >= 1000)\n", successCount)
				fmt.Println("  Ready for advanced optimization models")
			}

			fmt.Println("\nNext Steps:")
			if successCount >= 100 {
				fmt.Println("  1. Export data:  spacetraders arbitrage export-data --output training_data.csv")
				fmt.Println("  2. Train models using the exported CSV file")
			} else {
				fmt.Println("  1. Run more arbitrage operations to collect data")
				fmt.Printf("  2. Target: %d more successful executions\n", 100-successCount)
			}

			return nil
		},
	}

	return cmd
}

// resolvePlayerID converts an int to a PlayerID domain value object
func resolvePlayerID(playerID int) (shared.PlayerID, error) {
	return shared.NewPlayerID(playerID)
}
