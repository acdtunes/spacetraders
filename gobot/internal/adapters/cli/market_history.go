package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// openPriceHistoryRepo opens the recorded-price archive. Unlike the market cache it is not
// player-scoped, so no player resolution is needed.
func openPriceHistoryRepo() (*persistence.GormMarketPriceHistoryRepository, error) {
	db, err := openDatabase()
	if err != nil {
		return nil, err
	}
	return persistence.NewGormMarketPriceHistoryRepository(db), nil
}

func printGoodVolatility(ctx context.Context, repo *persistence.GormMarketPriceHistoryRepository, goodSymbol string, windowHours int) error {
	metrics, err := repo.GetVolatilityMetrics(ctx, goodSymbol, windowHours)
	if err != nil {
		return fmt.Errorf("failed to get volatility metrics: %w", err)
	}

	fmt.Printf("\n=== Volatility Metrics for %s ===\n", goodSymbol)
	fmt.Printf("Time Window: %d hours\n\n", windowHours)
	fmt.Printf("Mean Price:        %.2f credits\n", metrics.MeanPrice)
	fmt.Printf("Std Deviation:     %.2f\n", metrics.StdDeviation)
	fmt.Printf("Max Price Change:  %.2f%%\n", metrics.MaxPriceChange)
	fmt.Printf("Change Frequency:  %.2f changes/hour\n", metrics.ChangeFrequency)
	fmt.Printf("Sample Size:       %d records\n\n", metrics.SampleSize)

	if metrics.SampleSize == 0 {
		fmt.Println("Note: No price history data available for this good in the specified window.")
	}
	return nil
}

func printMostVolatileGoods(ctx context.Context, repo *persistence.GormMarketPriceHistoryRepository, topN, windowHours int) error {
	volatileGoods, err := repo.FindMostVolatileGoods(ctx, topN, windowHours)
	if err != nil {
		return fmt.Errorf("failed to find volatile goods: %w", err)
	}

	if len(volatileGoods) == 0 {
		fmt.Printf("No volatile goods found in the last %d hours\n", windowHours)
		return nil
	}

	fmt.Printf("\n=== Top %d Most Volatile Goods ===\n", len(volatileGoods))
	fmt.Printf("Time Window: %d hours\n\n", windowHours)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RANK\tGOOD\tVOLATILITY SCORE\tCHANGE COUNT")
	fmt.Fprintln(w, "----\t----\t----------------\t------------")

	for i, good := range volatileGoods {
		fmt.Fprintf(w, "%d\t%s\t%.2f\t%d\n",
			i+1,
			good.GoodSymbol,
			good.VolatilityScore,
			good.ChangeCount,
		)
	}

	w.Flush()
	fmt.Println()
	return nil
}

func printPriceHistory(ctx context.Context, repo *persistence.GormMarketPriceHistoryRepository, waypointSymbol, goodSymbol string, windowHours, limit int) error {
	var since time.Time
	if windowHours > 0 {
		since = time.Now().Add(-time.Duration(windowHours) * time.Hour)
	}
	history, err := repo.GetPriceHistory(ctx, waypointSymbol, goodSymbol, since, limit)
	if err != nil {
		return fmt.Errorf("failed to get price history: %w", err)
	}

	if len(history) == 0 {
		fmt.Printf("No price history found for %s at %s\n", goodSymbol, waypointSymbol)
		return nil
	}

	printMarketStability(ctx, repo, waypointSymbol, goodSymbol, windowHours)

	fmt.Printf("=== Price History for %s at %s ===\n\n", goodSymbol, waypointSymbol)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RECORDED AT\tBUY PRICE\tSELL PRICE\tSUPPLY\tACTIVITY\tVOLUME")
	fmt.Fprintln(w, "-----------\t---------\t----------\t------\t--------\t------")

	for _, record := range history {
		supplyStr := "N/A"
		if supply := record.Supply(); supply != nil && *supply != "" {
			supplyStr = *supply
		}
		activityStr := "N/A"
		if activity := record.Activity(); activity != nil && *activity != "" {
			activityStr = *activity
		}

		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%d\n",
			record.RecordedAt().Format("2006-01-02 15:04"),
			record.PurchasePrice(),
			record.SellPrice(),
			supplyStr,
			activityStr,
			record.TradeVolume(),
		)
	}

	w.Flush()
	fmt.Printf("\nTotal records: %d\n\n", len(history))
	return nil
}

// printMarketStability is best-effort: the history table is still worth printing when the
// stability read fails, so the error is deliberately swallowed.
func printMarketStability(ctx context.Context, repo *persistence.GormMarketPriceHistoryRepository, waypointSymbol, goodSymbol string, windowHours int) {
	stability, err := repo.GetMarketStability(ctx, waypointSymbol, goodSymbol, windowHours)
	if err != nil || stability == nil {
		return
	}
	fmt.Printf("\n=== Market Stability Analysis ===\n")
	fmt.Printf("Market:          %s\n", waypointSymbol)
	fmt.Printf("Good:            %s\n", goodSymbol)
	fmt.Printf("Stability Score: %.2f/100 (higher = more stable)\n", stability.StabilityScore)
	fmt.Printf("Price Range:     %d credits\n", stability.PriceRange)
	fmt.Printf("Avg Change:      %.2f%%\n\n", stability.AvgChangeSize)
}

// newMarketVolatilityCommand creates the market volatility subcommand
func newMarketVolatilityCommand() *cobra.Command {
	var (
		goodSymbol  string
		topN        int
		windowHours int
	)

	cmd := &cobra.Command{
		Use:   "volatility",
		Short: "Analyze market price volatility",
		Long: `Analyze price volatility for goods across all markets.

Shows volatility metrics including mean price, standard deviation, max price change percentage,
and change frequency. Can show specific good or top N most volatile goods.

Examples:
  spacetraders market volatility --good SHIP_PLATING --window-hours 24
  spacetraders market volatility --top 10 --window-hours 48`,
		RunE: func(cmd *cobra.Command, args []string) error {
			priceHistoryRepo, err := openPriceHistoryRepo()
			if err != nil {
				return err
			}

			ctx := context.Background()
			if goodSymbol != "" {
				return printGoodVolatility(ctx, priceHistoryRepo, goodSymbol, windowHours)
			}
			return printMostVolatileGoods(ctx, priceHistoryRepo, topN, windowHours)
		},
	}

	cmd.Flags().StringVar(&goodSymbol, "good", "", "Good symbol to analyze (e.g., SHIP_PLATING)")
	cmd.Flags().IntVar(&topN, "top", 10, "Number of most volatile goods to show (when --good not specified)")
	cmd.Flags().IntVar(&windowHours, "window-hours", 24, "Time window in hours for analysis")

	return cmd
}

// newMarketHistoryCommand creates the market history subcommand
func newMarketHistoryCommand() *cobra.Command {
	var (
		waypointSymbol string
		goodSymbol     string
		limit          int
		windowHours    int
	)

	cmd := &cobra.Command{
		Use:   "history",
		Short: "View price history for a market/good pair",
		Long: `View historical price data for a specific market and good.

Shows purchase price, sell price, supply, activity, and trade volume over time.

Examples:
  spacetraders market history --waypoint X1-YZ19-D47 --good SHIP_PLATING --limit 20
  spacetraders market history --waypoint X1-YZ19-D47 --good IRON --window-hours 48`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if waypointSymbol == "" {
				return fmt.Errorf("--waypoint flag is required")
			}
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}

			priceHistoryRepo, err := openPriceHistoryRepo()
			if err != nil {
				return err
			}

			return printPriceHistory(context.Background(), priceHistoryRepo,
				waypointSymbol, goodSymbol, windowHours, limit)
		},
	}

	cmd.Flags().StringVar(&waypointSymbol, "waypoint", "", "Waypoint symbol (required)")
	cmd.Flags().StringVar(&goodSymbol, "good", "", "Good symbol (required)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of records to show")
	cmd.Flags().IntVar(&windowHours, "window-hours", 24, "Time window in hours (0 = all time)")

	return cmd
}
