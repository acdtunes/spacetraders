package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// marketGoodFinder is the subset of the market repository the `market find`
// command needs, so unit tests can supply a fake in place of the database.
type marketGoodFinder interface {
	FindMarketsTradingGood(ctx context.Context, goodSymbol, systemSymbol string, playerID int) ([]persistence.MarketGoodListing, error)
}

// sortMarketListings orders listings by the best price for the requested side.
// side "sell" (we sell to the market) sorts by descending purchase price.
// side "buy" or "any" sorts by ascending sell price (what we'd pay to buy).
func sortMarketListings(listings []persistence.MarketGoodListing, side string) {
	switch side {
	case "sell":
		sort.SliceStable(listings, func(i, j int) bool {
			return listings[i].PurchasePrice > listings[j].PurchasePrice
		})
	default:
		sort.SliceStable(listings, func(i, j int) bool {
			return listings[i].SellPrice < listings[j].SellPrice
		})
	}
}

// formatDataAge renders a duration as a short human-readable age string.
// Staleness is never hidden (L58): every row always carries this column.
func formatDataAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%.1fh ago", age.Hours())
	default:
		return fmt.Sprintf("%.1fd ago", age.Hours()/24)
	}
}

// runMarketFind queries every cached market trading goodSymbol, sorts by the
// requested side, and prints waypoint/trade-type/prices/supply/activity/volume
// plus data age (staleness is always shown, never hidden).
func runMarketFind(
	ctx context.Context,
	finder marketGoodFinder,
	goodSymbol string,
	systemSymbol string,
	side string,
	playerID int,
	jsonOut bool,
) error {
	listings, err := finder.FindMarketsTradingGood(ctx, goodSymbol, systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to find markets trading %s: %w", goodSymbol, err)
	}

	sortMarketListings(listings, side)

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(listings)
	}

	if len(listings) == 0 {
		fmt.Printf("No cached markets trade %s\n", goodSymbol)
		return nil
	}

	fmt.Printf("\n=== Markets trading %s ===\n\n", goodSymbol)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WAYPOINT\tTYPE\tBUY PRICE\tSELL PRICE\tSUPPLY\tACTIVITY\tVOLUME\tDATA AGE")
	fmt.Fprintln(w, "--------\t----\t---------\t----------\t------\t--------\t------\t--------")

	now := time.Now()
	for _, l := range listings {
		tradeType := l.TradeType
		if tradeType == "" {
			tradeType = "N/A"
		}
		supply := l.Supply
		if supply == "" {
			supply = "N/A"
		}
		activity := l.Activity
		if activity == "" {
			activity = "N/A"
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%d\t%s\n",
			l.WaypointSymbol,
			tradeType,
			l.PurchasePrice,
			l.SellPrice,
			supply,
			activity,
			l.TradeVolume,
			formatDataAge(now.Sub(l.LastUpdated)),
		)
	}

	w.Flush()
	fmt.Printf("\nTotal: %d market(s)\n\n", len(listings))

	return nil
}

// newMarketFindCommand creates the market find subcommand
func newMarketFindCommand() *cobra.Command {
	var (
		goodSymbol   string
		systemSymbol string
		side         string
		jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "find",
		Short: "Find every cached market trading a good",
		Long: `Find every cached market known to trade a good, across a system or all
known systems, sorted by best price for the requested side.

Always shows data age per market (staleness is never hidden - a stale
availability premise can flip an entire plan).

Examples:
  spacetraders market find --good IRON_ORE --player-id 1
  spacetraders market find --good IRON_ORE --system X1-GZ7 --side sell --agent ENDURANCE
  spacetraders market find --good IRON_ORE --player-id 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if goodSymbol == "" {
				return fmt.Errorf("--good flag is required")
			}
			switch side {
			case "buy", "sell", "any":
			default:
				return fmt.Errorf("--side must be one of: buy, sell, any")
			}

			ctx := context.Background()
			marketRepo, resolvedPlayerID, err := openMarketRepo(ctx)
			if err != nil {
				return err
			}

			return runMarketFind(ctx, marketRepo, goodSymbol, systemSymbol, side, resolvedPlayerID, jsonOut)
		},
	}

	cmd.Flags().StringVar(&goodSymbol, "good", "", "Good symbol to search for (required)")
	cmd.Flags().StringVar(&systemSymbol, "system", "", "Restrict search to this system (default: all systems)")
	cmd.Flags().StringVar(&side, "side", "any", "Sort for best price on this side: buy, sell, or any")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}
