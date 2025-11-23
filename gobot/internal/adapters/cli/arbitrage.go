package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
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

			// Scan for opportunities
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := client.ScanArbitrageOpportunities(ctx, systemSymbol, playerIdent.PlayerID, minMargin, limit)
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
  spacetraders arbitrage start --system X1-AU21 --min-margin 15.0 --max-workers 5`,
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

			// Start coordinator
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.StartArbitrageCoordinator(ctx, systemSymbol, playerIdent.PlayerID, minMargin, maxWorkers)
			if err != nil {
				return fmt.Errorf("failed to start coordinator: %w", err)
			}

			// Display result
			fmt.Println("✓ Arbitrage coordinator started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  System:           %s\n", systemSymbol)
			fmt.Printf("  Min Margin:       %.1f%%\n", minMargin)
			fmt.Printf("  Max Workers:      %d\n", maxWorkers)
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

