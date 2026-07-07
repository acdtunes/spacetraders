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
// mapping (PurchasePrice→Bid, SellPrice→Ask) + RankSpreads.
func spreadsFixtureListings() []persistence.SystemMarketGoodListing {
	now := time.Now()
	return []persistence.SystemMarketGoodListing{
		{WaypointSymbol: "X1-SYS-E41", GoodSymbol: "FIREARMS", TradeType: "EXPORT", PurchasePrice: 250, SellPrice: 300, TradeVolume: 60, LastUpdated: now},
		{WaypointSymbol: "X1-SYS-J56", GoodSymbol: "FIREARMS", TradeType: "IMPORT", PurchasePrice: 900, SellPrice: 950, TradeVolume: 20, LastUpdated: now},
		{WaypointSymbol: "X1-SYS-A1", GoodSymbol: "GADGETS", TradeType: "EXPORT", PurchasePrice: 80, SellPrice: 100, TradeVolume: 2, LastUpdated: now},
		{WaypointSymbol: "X1-SYS-B2", GoodSymbol: "GADGETS", TradeType: "IMPORT", PurchasePrice: 1100, SellPrice: 1150, TradeVolume: 40, LastUpdated: now},
	}
}

// TestSystemListingsToGoodListings_MapsMarketColumns is the adapter-boundary
// inverted-column guard: the market's BUY column (PurchasePrice) must become the
// domain Bid (what we receive) and the SELL column (SellPrice) must become Ask
// (what we pay). Swapping them here would silently overstate every spread ~2x.
func TestSystemListingsToGoodListings_MapsMarketColumns(t *testing.T) {
	got := systemListingsToGoodListings([]persistence.SystemMarketGoodListing{
		{WaypointSymbol: "X1-SYS-E41", GoodSymbol: "FIREARMS", TradeType: "EXPORT", PurchasePrice: 250, SellPrice: 300, TradeVolume: 60},
	})

	require.Len(t, got, 1)
	g := got[0]
	require.Equal(t, 250, g.Bid, "market BUY price (PurchasePrice) must map to Bid = what we receive selling TO it")
	require.Equal(t, 300, g.Ask, "market SELL price (SellPrice) must map to Ask = what we pay buying FROM it")
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
