package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// marketSystemListingsFinder is the subset of the market repository the
// `market spreads` command needs, so unit tests can supply a fake in place of
// the database.
type marketSystemListingsFinder interface {
	FindAllGoodListingsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]persistence.SystemMarketGoodListing, error)
}

// systemListingsToGoodListings maps cached market rows into the trading domain's
// GoodListing, renaming the persisted columns to the market-side names exactly
// once, at this adapter boundary:
//
//	SellPrice     (what we RECEIVE selling TO the market) → Bid
//	PurchasePrice (what we PAY buying FROM the market)    → Ask
//
// Getting this mapping backwards is the inverted-margin trap that overstates
// every spread ~2x (market-doctrine); RankSpreads then computes destBid−sourceAsk.
// It WAS backwards here until sp-en5h7 — harmlessly, because market_data was
// itself transposed, so this adapter and the writer were wrong in opposite
// directions and the displayed spreads came out right.
func systemListingsToGoodListings(listings []persistence.SystemMarketGoodListing) []trading.GoodListing {
	out := make([]trading.GoodListing, len(listings))
	for i, l := range listings {
		out[i] = trading.GoodListing{
			Good:      l.GoodSymbol,
			Waypoint:  l.WaypointSymbol,
			TradeType: l.TradeType,
			Bid:       l.SellPrice,
			Ask:       l.PurchasePrice,
			Supply:    l.Supply,
			Activity:  l.Activity,
			Volume:    l.TradeVolume,
		}
	}
	return out
}

// oldestListing returns the least-recently-updated timestamp across a set of
// cached rows, so the scanner can surface the staleness of the data it ranked
// (staleness is never hidden — a stale premise can flip an entire plan, L58).
func oldestListing(listings []persistence.SystemMarketGoodListing) (time.Time, bool) {
	var oldest time.Time
	found := false
	for _, l := range listings {
		if l.LastUpdated.IsZero() {
			continue
		}
		if !found || l.LastUpdated.Before(oldest) {
			oldest = l.LastUpdated
			found = true
		}
	}
	return oldest, found
}

// runMarketSpreads ranks pure-arbitrage lanes for a system entirely from cache:
// for every good it finds the best buy-here (source Ask) / sell-there (dest Bid)
// pair and ranks by volume-capped spread. No live API calls — it reads only what
// scouts have already cached.
func runMarketSpreads(
	ctx context.Context,
	finder marketSystemListingsFinder,
	systemSymbol string,
	playerID int,
	topN int,
	jsonOut bool,
) error {
	listings, err := finder.FindAllGoodListingsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to scan market listings in %s: %w", systemSymbol, err)
	}

	lanes := trading.RankSpreads(systemListingsToGoodListings(listings))
	if topN > 0 && len(lanes) > topN {
		lanes = lanes[:topN]
	}

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(lanes)
	}

	if len(lanes) == 0 {
		fmt.Printf("No profitable arbitrage lanes in cached markets for %s\n", systemSymbol)
		fmt.Println("(need at least two markets trading the same good with a positive dest-bid minus source-ask spread)")
		return nil
	}

	fmt.Printf("\n=== Arbitrage lanes in %s (from cache) ===\n", systemSymbol)
	if oldest, ok := oldestListing(listings); ok {
		fmt.Printf("Oldest cached market in scan: %s\n", formatDataAge(time.Since(oldest)))
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RANK\tGOOD\tBUY AT (SRC)\tSRC ASK\tSELL AT (DEST)\tDEST BID\tSPREAD/U\tVOL CAP\tCAPPED SPREAD\tCLEARS FLOOR")
	fmt.Fprintln(w, "----\t----\t-----------\t-------\t--------------\t--------\t--------\t-------\t-------------\t------------")
	for i, lane := range lanes {
		// The scan still ranks by CAPPED spread, but flags which lanes actually clear
		// the executor's bid-floor discipline: trade-route flies the highest-ranked
		// lane whose CLEARS FLOOR = yes, skipping a deeper-but-sub-floor lane.
		clearsFloor := "no"
		if lane.ClearsFloor() {
			clearsFloor = "yes"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%d\t%s\t%d\t%d\t%d\t%d\t%s\n",
			i+1,
			lane.Good,
			lane.SourceWaypoint,
			lane.SourceAsk,
			lane.DestWaypoint,
			lane.DestBid,
			lane.SpreadPerUnit,
			lane.VolumeCap,
			lane.CappedSpread,
			clearsFloor,
		)
	}
	w.Flush()
	fmt.Printf("\nTotal lanes: %d\n", len(lanes))
	fmt.Printf("(CLEARS FLOOR = yes when spread/u >= %d, the bid-floor discipline; trade-route flies the highest-ranked lane that clears it.)\n\n", trading.MinBidMargin)

	return nil
}

// newMarketSpreadsCommand creates the market spreads subcommand.
func newMarketSpreadsCommand() *cobra.Command {
	var (
		systemSymbol string
		topN         int
		jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "spreads",
		Short: "Rank pure-arbitrage lanes in a system from cached markets",
		Long: `Rank standing buy-export / sell-import spreads across every cached market in
a system, entirely from cache (no live API calls).

For each good it finds the best source (where you BUY, paying the market's SELL
price / ask) and destination (where you SELL, receiving the market's BUY price /
bid), then ranks lanes by volume-capped spread: (dest bid - source ask) x the
minimum tradable volume. Volume-capping matters because a fat per-unit spread on
a thin market is worth less than a modest spread on a deep one.

The CLEARS FLOOR column flags whether each lane's per-unit spread clears the
bid-floor discipline the trade-route executor enforces. The scan still ranks by
capped spread (so you see every standing spread), but trade-route flies the
highest-ranked lane whose CLEARS FLOOR = yes - a deeper capped lane that is
sub-floor is refused, not flown.

Examples:
  spacetraders market spreads --system X1-GZ7 --agent ENDURANCE
  spacetraders market spreads --system X1-GZ7 --top 10 --player-id 1
  spacetraders market spreads --system X1-GZ7 --json --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if systemSymbol == "" {
				return fmt.Errorf("--system flag is required")
			}

			ctx := context.Background()
			marketRepo, resolvedPlayerID, err := openMarketRepo(ctx)
			if err != nil {
				return err
			}

			return runMarketSpreads(ctx, marketRepo, systemSymbol, resolvedPlayerID, topN, jsonOut)
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "System symbol to scan (required)")
	cmd.Flags().IntVar(&topN, "top", 0, "Show only the top N lanes (0 = all)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}
