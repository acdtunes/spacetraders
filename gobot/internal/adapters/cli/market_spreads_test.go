package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

type fakeSystemListingsFinder struct {
	listings []persistence.SystemMarketGoodListing
	err      error
}

func (f *fakeSystemListingsFinder) FindAllGoodListingsInSystem(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]persistence.SystemMarketGoodListing, error) {
	return f.listings, f.err
}

// spreadsFixtureListings mirrors the domain hand-computed fixture, but as cached
// persistence rows, so the CLI test exercises the full adapter path: column
// mapping (PurchasePrice→Ask, SellPrice→Bid) + RankSpreads.
//
// Every row quotes purchase_price ABOVE sell_price, because a real market charges
// more to sell us a unit than it pays to buy one — that gap is its rake. The rows
// carried the two prices transposed until sp-en5h7, matching market_data's own
// corruption; read through the corrected mapping they were crossed listings, which
// RankSpreads refuses outright, so the scan ranked nothing at all.
//
// Prices are unchanged from the domain fixture (E41 pays 250 / charges 300, J56
// pays 900 / charges 950, and so on) — only the column each one sits in was wrong.
func spreadsFixtureListings() []persistence.SystemMarketGoodListing {
	now := time.Now()
	return []persistence.SystemMarketGoodListing{
		{WaypointSymbol: "X1-SYS-E41", GoodSymbol: "FIREARMS", TradeType: "EXPORT", PurchasePrice: 300, SellPrice: 250, TradeVolume: 60, LastUpdated: now},
		{WaypointSymbol: "X1-SYS-J56", GoodSymbol: "FIREARMS", TradeType: "IMPORT", PurchasePrice: 950, SellPrice: 900, TradeVolume: 20, LastUpdated: now},
		{WaypointSymbol: "X1-SYS-A1", GoodSymbol: "GADGETS", TradeType: "EXPORT", PurchasePrice: 100, SellPrice: 80, TradeVolume: 2, LastUpdated: now},
		{WaypointSymbol: "X1-SYS-B2", GoodSymbol: "GADGETS", TradeType: "IMPORT", PurchasePrice: 1150, SellPrice: 1100, TradeVolume: 40, LastUpdated: now},
	}
}

// TestSystemListingsToGoodListings_MapsMarketColumns is the adapter-boundary
// inverted-column guard: purchase_price is the ASK — what we PAY buying FROM the
// market, the LARGER of the two — and must become the domain Ask; sell_price is
// the BID — what we RECEIVE selling TO it, the smaller — and must become Bid.
// Swapping them here would silently overstate every spread ~2x. This guard named
// the two columns the other way round until sp-en5h7.
func TestSystemListingsToGoodListings_MapsMarketColumns(t *testing.T) {
	got := systemListingsToGoodListings([]persistence.SystemMarketGoodListing{
		{WaypointSymbol: "X1-SYS-E41", GoodSymbol: "FIREARMS", TradeType: "EXPORT", PurchasePrice: 300, SellPrice: 250, TradeVolume: 60},
	})

	require.Len(t, got, 1)
	g := got[0]
	require.Equal(t, 250, g.Bid, "market sell_price (the BID: what we receive selling TO it) must map to Bid")
	require.Equal(t, 300, g.Ask, "market purchase_price (the ASK: what we pay buying FROM it) must map to Ask")
	require.Equal(t, "FIREARMS", g.Good)
	require.Equal(t, "X1-SYS-E41", g.Waypoint)
	require.Equal(t, 60, g.Volume)
}

func TestRunMarketSpreads_RanksLanesFromCache(t *testing.T) {
	finder := &fakeSystemListingsFinder{listings: spreadsFixtureListings()}

	out := captureStdout(t, func() {
		require.NoError(t, runMarketSpreads(context.Background(), finder, "X1-SYS", 1, 0, false))
	})

	// FIREARMS must appear before GADGETS (deeper volume-capped spread), and the
	// hand-computed numbers must be present (spread/unit 600, capped 12000).
	firearmsIdx := strings.Index(out, "FIREARMS")
	gadgetsIdx := strings.Index(out, "GADGETS")
	require.NotEqual(t, -1, firearmsIdx, "FIREARMS lane must be printed")
	require.NotEqual(t, -1, gadgetsIdx, "GADGETS lane must be printed")
	require.Less(t, firearmsIdx, gadgetsIdx, "FIREARMS (capped 12000) must rank above GADGETS (capped 2000)")
	require.Contains(t, out, "X1-SYS-E41", "must name the source (buy) waypoint")
	require.Contains(t, out, "X1-SYS-J56", "must name the destination (sell) waypoint")
	require.Contains(t, out, "12000", "FIREARMS capped spread 600*20=12000 must be shown")
}

// The scan must flag which ranked lanes clear the executor's bid-floor discipline,
// so the captain can see WHY trade-route flies a lower-ranked lane. In the fixture
// FIREARMS ranks #1 by capped spread (12000) but its per-unit spread (600) is
// sub-floor, while the lower-ranked GADGETS (spread/u 1000) clears the floor — the
// exact sp-sh6w confusion the column resolves.
func TestRunMarketSpreads_FlagsClearsFloorColumn(t *testing.T) {
	finder := &fakeSystemListingsFinder{listings: spreadsFixtureListings()}

	out := captureStdout(t, func() {
		require.NoError(t, runMarketSpreads(context.Background(), finder, "X1-SYS", 1, 0, false))
	})

	require.Contains(t, out, "CLEARS FLOOR", "the scan must show a CLEARS FLOOR column")

	var firearmsLine, gadgetsLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "FIREARMS") {
			firearmsLine = line
		}
		if strings.Contains(line, "GADGETS") {
			gadgetsLine = line
		}
	}
	require.NotEmpty(t, firearmsLine, "FIREARMS lane row must be printed")
	require.NotEmpty(t, gadgetsLine, "GADGETS lane row must be printed")
	require.Contains(t, firearmsLine, "no", "FIREARMS (spread/u 600 < 1000) must be flagged as NOT clearing the floor")
	require.Contains(t, gadgetsLine, "yes", "GADGETS (spread/u 1000 >= 1000) must be flagged as clearing the floor")
}

func TestRunMarketSpreads_TopNTruncates(t *testing.T) {
	finder := &fakeSystemListingsFinder{listings: spreadsFixtureListings()}

	out := captureStdout(t, func() {
		require.NoError(t, runMarketSpreads(context.Background(), finder, "X1-SYS", 1, 1, false))
	})

	require.Contains(t, out, "FIREARMS", "top lane must be shown")
	require.NotContains(t, out, "GADGETS", "--top 1 must truncate to the single best lane")
}

func TestRunMarketSpreads_PropagatesRepositoryError(t *testing.T) {
	finder := &fakeSystemListingsFinder{err: errors.New("db down")}

	err := runMarketSpreads(context.Background(), finder, "X1-SYS", 1, 0, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "db down")
}
